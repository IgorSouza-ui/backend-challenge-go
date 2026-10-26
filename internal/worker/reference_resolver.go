package worker

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/junglegaming/backend-challenge-go/internal/domain/events"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
	"github.com/junglegaming/backend-challenge-go/internal/domain/transaction"
)

type ReferenceResolver struct {
	txManager  repository.DBTransactionManager
	walletRepo repository.WalletRepository
	txRepo     repository.TransactionRepository
	ledgerRepo repository.LedgerRepository
	outboxRepo repository.OutboxRepository
	logger     *slog.Logger
	interval   time.Duration
	maxRetries int
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

func NewReferenceResolver(
	txManager repository.DBTransactionManager,
	walletRepo repository.WalletRepository,
	txRepo repository.TransactionRepository,
	ledgerRepo repository.LedgerRepository,
	outboxRepo repository.OutboxRepository,
	logger *slog.Logger,
) *ReferenceResolver {
	return &ReferenceResolver{
		txManager:  txManager,
		walletRepo: walletRepo,
		txRepo:     txRepo,
		ledgerRepo: ledgerRepo,
		outboxRepo: outboxRepo,
		logger:     logger,
		interval:   1 * time.Second,
		maxRetries: 5,
		stopCh:     make(chan struct{}),
	}
}

func (w *ReferenceResolver) Start(ctx context.Context) {
	w.logger.Info("starting Reference Resolver worker")
	w.wg.Add(1)
	go w.loop(ctx)
}

func (w *ReferenceResolver) Stop(ctx context.Context) error {
	w.logger.Info("stopping Reference Resolver worker...")
	close(w.stopCh)

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		w.logger.Info("Reference Resolver stopped cleanly")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *ReferenceResolver) loop(ctx context.Context) {
	defer w.wg.Done()
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.ResolvePendingOnce(ctx)
		}
	}
}

func (w *ReferenceResolver) ResolvePendingOnce(ctx context.Context) {
	pendingList, err := w.txRepo.ListPendingReferences(ctx, 20)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			w.logger.Error("failed listing pending reference transactions", "error", err)
		}
		return
	}

	for _, pendingTx := range pendingList {
		w.tryResolve(ctx, pendingTx)
	}
}

func (w *ReferenceResolver) rejectPending(ctx context.Context, tx pgx.Tx, pendingTx *transaction.WagerTransaction, wltBalance money.Money, code string) {
	_ = pendingTx.MarkRejectedWithBalance(code, wltBalance)
	_ = w.txRepo.Update(ctx, tx, pendingTx)

	rejectedEvt, _ := events.NewRejectedEvent(pendingTx.ID().String(), events.WagerTransactionRejectedData{
		TransactionID:         pendingTx.ID(),
		ProviderID:            pendingTx.ProviderID(),
		ExternalTransactionID: pendingTx.ExternalTransactionID(),
		WalletID:              pendingTx.WalletID(),
		PlayerID:              pendingTx.PlayerID(),
		Kind:                  string(pendingTx.Kind()),
		FailureCode:           code,
		Money:                 pendingTx.Amount(),
	})
	_ = w.outboxRepo.Create(ctx, tx, rejectedEvt)
	_ = tx.Commit(ctx)
}

func (w *ReferenceResolver) tryResolve(ctx context.Context, pendingTx *transaction.WagerTransaction) {
	if pendingTx.ReferenceExternalTransactionID() == nil {
		return
	}

	parentRefID := *pendingTx.ReferenceExternalTransactionID()
	parent, err := w.txRepo.GetByProviderAndExternalID(ctx, pendingTx.ProviderID(), parentRefID)

	tx, txErr := w.txManager.Begin(ctx)
	if txErr != nil {
		w.logger.Error("failed to begin tx for reference resolution", "error", txErr)
		return
	}
	defer tx.Rollback(ctx)

	wlt, wErr := w.walletRepo.GetByIDForUpdate(ctx, tx, pendingTx.WalletID())
	if wErr != nil {
		return
	}

	if err != nil {
		// Parent still not found
		if pendingTx.RetryCount()+1 >= w.maxRetries {
			// TTL / retries expired -> transition to REJECTED with REFERENCE_NOT_FOUND
			w.logger.Warn("pending reference expired, rejecting transaction", "transactionId", pendingTx.ID(), "ref", parentRefID)
			w.rejectPending(ctx, tx, pendingTx, wlt.Balance(), transaction.FailReferenceNotFound)
			return
		}

		// Exponential backoff
		nextRetry := time.Now().UTC().Add(time.Duration(math.Pow(2, float64(pendingTx.RetryCount()+1))) * time.Second)
		pendingTx.IncrementRetry(nextRetry)
		_ = w.txRepo.Update(ctx, tx, pendingTx)
		_ = tx.Commit(ctx)
		return
	}

	// Parent FOUND! Validate parent state, amount, wallet, and duplicate reversal
	if parent.State() != transaction.StateProcessed {
		w.rejectPending(ctx, tx, pendingTx, wlt.Balance(), transaction.FailInvalidReferenceState)
		return
	}
	if parent.WalletID() != wlt.ID() || !parent.Amount().Equals(pendingTx.Amount()) {
		w.rejectPending(ctx, tx, pendingTx, wlt.Balance(), transaction.FailInvalidReferenceOperation)
		return
	}
	alreadyReversed, revErr := w.txRepo.HasProcessedReversal(ctx, tx, parent.ID())
	if revErr != nil {
		return
	}
	if alreadyReversed {
		w.rejectPending(ctx, tx, pendingTx, wlt.Balance(), transaction.FailDuplicateReversal)
		return
	}

	var balanceBefore, balanceAfter money.Money
	var direction ledger.Direction

	if pendingTx.Kind() == transaction.KindRefund {
		if parent.Kind() != transaction.KindBet {
			w.rejectPending(ctx, tx, pendingTx, wlt.Balance(), transaction.FailInvalidReferenceOperation)
			return
		}
		bBefore, bAfter, err := wlt.Credit(pendingTx.Amount())
		if err != nil {
			return
		}
		balanceBefore = bBefore
		balanceAfter = bAfter
		direction = ledger.Credit
	} else if pendingTx.Kind() == transaction.KindRollback {
		switch parent.Kind() {
		case transaction.KindBet:
			bBefore, bAfter, err := wlt.Credit(pendingTx.Amount())
			if err != nil {
				return
			}
			balanceBefore = bBefore
			balanceAfter = bAfter
			direction = ledger.Credit
		case transaction.KindWin, transaction.KindRefund:
			bBefore, bAfter, err := wlt.Debit(pendingTx.Amount())
			if err != nil {
				w.rejectPending(ctx, tx, pendingTx, wlt.Balance(), transaction.FailReversalExceedsBalance)
				return
			}
			balanceBefore = bBefore
			balanceAfter = bAfter
			direction = ledger.Debit
		default:
			w.rejectPending(ctx, tx, pendingTx, wlt.Balance(), transaction.FailInvalidReferenceOperation)
			return
		}
	}

	parentID := parent.ID()
	_ = pendingTx.MarkProcessed(balanceAfter, &parentID)
	_ = w.txRepo.Update(ctx, tx, pendingTx)
	_ = w.walletRepo.UpdateBalance(ctx, tx, wlt)

	ledgerEntry, _ := ledger.New(
		uuid.New(),
		wlt.ID(),
		pendingTx.ID(),
		direction,
		pendingTx.Amount(),
		balanceBefore,
		balanceAfter,
		time.Now().UTC(),
	)
	_ = w.ledgerRepo.Create(ctx, tx, ledgerEntry)

	balanceEvt, _ := events.NewBalanceChangedEvent(pendingTx.ID().String(), events.WalletBalanceChangedData{
		WalletID:      wlt.ID(),
		TransactionID: pendingTx.ID(),
		Direction:     string(direction),
		Money:         pendingTx.Amount(),
		BalanceBefore: balanceBefore,
		BalanceAfter:  balanceAfter,
		WalletVersion: wlt.Version(),
	})
	_ = w.outboxRepo.Create(ctx, tx, balanceEvt)

	processedEvt, _ := events.NewProcessedEvent(pendingTx.ID().String(), events.WagerTransactionProcessedData{
		TransactionID:         pendingTx.ID(),
		ProviderID:            pendingTx.ProviderID(),
		ExternalTransactionID: pendingTx.ExternalTransactionID(),
		WalletID:              wlt.ID(),
		PlayerID:              wlt.PlayerID(),
		RoundID:               pendingTx.RoundID(),
		GameID:                pendingTx.GameID(),
		Kind:                  string(pendingTx.Kind()),
		Money:                 pendingTx.Amount(),
		Balance:               balanceAfter,
	})
	_ = w.outboxRepo.Create(ctx, tx, processedEvt)

	_ = tx.Commit(ctx)
	w.logger.Info("successfully resolved out-of-order pending reference!", "transactionId", pendingTx.ID())
}
