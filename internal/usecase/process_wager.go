package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/junglegaming/backend-challenge-go/internal/domain/events"
	"github.com/junglegaming/backend-challenge-go/internal/domain/idempotency"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
	"github.com/junglegaming/backend-challenge-go/internal/domain/transaction"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

var (
	ErrPayloadConflict  = errors.New("idempotency key reused with different payload")
	ErrExternalIDReused = errors.New("external transaction id already used with different idempotency key")
)

type ProcessWagerInput struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           transaction.Kind
	Money                          money.Money
	ReferenceExternalTransactionID *string
	CorrelationID                  string
	IsSQS                          bool
	MessageID                      string
}

type ProcessWagerOutput struct {
	TransactionID    uuid.UUID   `json:"transactionId"`
	Status           string      `json:"status"`
	FailureCode      *string     `json:"failureCode,omitempty"`
	Balance          money.Money `json:"balance"`
	IdempotentReplay bool        `json:"idempotentReplay"`
}

type ProcessWagerUseCase struct {
	txManager  repository.DBTransactionManager
	walletRepo repository.WalletRepository
	txRepo     repository.TransactionRepository
	ledgerRepo repository.LedgerRepository
	outboxRepo repository.OutboxRepository
	inboxRepo  repository.InboxRepository
}

func NewProcessWagerUseCase(
	txManager repository.DBTransactionManager,
	walletRepo repository.WalletRepository,
	txRepo repository.TransactionRepository,
	ledgerRepo repository.LedgerRepository,
	outboxRepo repository.OutboxRepository,
	inboxRepo repository.InboxRepository,
) *ProcessWagerUseCase {
	return &ProcessWagerUseCase{
		txManager:  txManager,
		walletRepo: walletRepo,
		txRepo:     txRepo,
		ledgerRepo: ledgerRepo,
		outboxRepo: outboxRepo,
		inboxRepo:  inboxRepo,
	}
}

func (uc *ProcessWagerUseCase) Execute(ctx context.Context, input ProcessWagerInput) (*ProcessWagerOutput, error) {
	// 1. Compute canonical payload hash
	canonicalPayload := idempotency.CanonicalPayload{
		ProviderId:            input.ProviderID,
		ExternalTransactionId: input.ExternalTransactionID,
		PlayerId:              input.PlayerID.String(),
		WalletId:              input.WalletID.String(),
		RoundId:               input.RoundID,
		GameId:                input.GameID,
		Kind:                  string(input.Kind),
		Money: idempotency.CanonicalMoney{
			Amount:   input.Money.AmountString(),
			Currency: input.Money.Currency(),
		},
		ReferenceExternalTransactionId: input.ReferenceExternalTransactionID,
	}

	payloadHash, err := idempotency.ComputeHash(canonicalPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to compute canonical payload hash: %w", err)
	}

	// 2. Fast pre-check idempotency by (providerID, idempotencyKey)
	if out, err := uc.checkIdempotencyAndExternalID(ctx, input, payloadHash); out != nil || err != nil {
		return out, err
	}

	// 3. Begin SQL Transaction
	tx, err := uc.txManager.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 4. Pessimistic row-level lock on wallet (serializes concurrent operations on the same wallet)
	w, err := uc.walletRepo.GetByIDForUpdate(ctx, tx, input.WalletID)
	if err != nil {
		return nil, fmt.Errorf("failed to lock wallet: %w", err)
	}

	// 5. Re-check idempotency and externalTransactionId inside the wallet lock
	// This guarantees that 50 concurrent requests with the same key see the committed result of the first winner.
	if out, err := uc.checkIdempotencyAndExternalID(ctx, input, payloadHash); out != nil || err != nil {
		return out, err
	}

	// 6. If SQS, register in Inbox
	if input.IsSQS && input.MessageID != "" {
		isNew, err := uc.inboxRepo.RegisterOrCheck(ctx, tx, "sqs-wager-consumer", input.MessageID, payloadHash)
		if err != nil {
			return nil, err
		}
		if !isNew {
			_ = tx.Commit(ctx)
			return &ProcessWagerOutput{
				Status:           "PROCESSED",
				Balance:          w.Balance(),
				IdempotentReplay: true,
			}, nil
		}
	}

	if w.PlayerID() != input.PlayerID {
		return nil, errors.New("player does not own this wallet")
	}
	if w.Currency() != input.Money.Currency() {
		return nil, fmt.Errorf("currency mismatch: wallet is %s, transaction is %s", w.Currency(), input.Money.Currency())
	}

	// 7. Initialize transaction domain entity
	txID := uuid.New()
	txEntity, err := transaction.NewExternal(
		txID,
		input.ProviderID,
		input.ExternalTransactionID,
		input.IdempotencyKey,
		payloadHash,
		input.WalletID,
		input.PlayerID,
		input.RoundID,
		input.GameID,
		input.Kind,
		input.Money,
		input.ReferenceExternalTransactionID,
	)
	if err != nil {
		return nil, err
	}

	// 8. Handle References (REFUND, ROLLBACK, and optional reference on WIN)
	var parentTx *transaction.WagerTransaction
	hasRef := input.ReferenceExternalTransactionID != nil && *input.ReferenceExternalTransactionID != ""
	if input.Kind == transaction.KindRefund || input.Kind == transaction.KindRollback ||
		(input.Kind == transaction.KindWin && hasRef) {

		parentRefID := *input.ReferenceExternalTransactionID
		parent, err := uc.txRepo.GetByProviderAndExternalID(ctx, input.ProviderID, parentRefID)
		if err != nil && errors.Is(err, repository.ErrNotFound) {
			// Out-of-order: parent transaction not arrived yet!
			return uc.parkPendingReference(ctx, tx, txEntity, w, parentRefID, input.CorrelationID)
		} else if err != nil {
			return nil, fmt.Errorf("error querying reference transaction: %w", err)
		}

		// If parent exists but is still in PENDING or PENDING_REFERENCE, also wait
		if parent.State() == transaction.StatePending || parent.State() == transaction.StatePendingReference {
			return uc.parkPendingReference(ctx, tx, txEntity, w, parentRefID, input.CorrelationID)
		}

		parentTx = parent
		if parentTx.State() != transaction.StateProcessed {
			return uc.rejectTransaction(ctx, tx, txEntity, w, transaction.FailInvalidReferenceState, input.CorrelationID)
		}
		if parentTx.PlayerID() != input.PlayerID || parentTx.WalletID() != input.WalletID ||
			parentTx.RoundID() != input.RoundID || parentTx.Amount().Currency() != input.Money.Currency() {
			return uc.rejectTransaction(ctx, tx, txEntity, w, transaction.FailInvalidReferenceOperation, input.CorrelationID)
		}

		if input.Kind == transaction.KindWin {
			// WIN referencing a BET must reference a processed BET in the same round
			if parentTx.Kind() != transaction.KindBet {
				return uc.rejectTransaction(ctx, tx, txEntity, w, transaction.FailInvalidReferenceOperation, input.CorrelationID)
			}
		} else {
			// REFUND or ROLLBACK: amount must match exactly
			if !parentTx.Amount().Equals(input.Money) {
				return uc.rejectTransaction(ctx, tx, txEntity, w, transaction.FailInvalidAmount, input.CorrelationID)
			}
			if input.Kind == transaction.KindRefund && parentTx.Kind() != transaction.KindBet {
				return uc.rejectTransaction(ctx, tx, txEntity, w, transaction.FailInvalidReferenceOperation, input.CorrelationID)
			}
			if input.Kind == transaction.KindRollback &&
				(parentTx.Kind() != transaction.KindBet && parentTx.Kind() != transaction.KindWin && parentTx.Kind() != transaction.KindRefund) {
				return uc.rejectTransaction(ctx, tx, txEntity, w, transaction.FailInvalidReferenceOperation, input.CorrelationID)
			}

			// Prevent duplicate reversal (either REFUND or ROLLBACK) on the same parent transaction
			alreadyReversed, err := uc.txRepo.HasProcessedReversal(ctx, tx, parentTx.ID())
			if err != nil {
				return nil, err
			}
			if alreadyReversed {
				return uc.rejectTransaction(ctx, tx, txEntity, w, transaction.FailDuplicateReversal, input.CorrelationID)
			}
		}
	}

	// 9. Execute domain business logic per kind
	var balanceBefore, balanceAfter money.Money
	var direction ledger.Direction
	createLedger := false

	switch input.Kind {
	case transaction.KindBet:
		bBefore, bAfter, err := w.Debit(input.Money)
		if err != nil {
			if errors.Is(err, wallet.ErrInsufficientFunds) {
				return uc.rejectTransaction(ctx, tx, txEntity, w, transaction.FailInsufficientFunds, input.CorrelationID)
			}
			return nil, err
		}
		balanceBefore = bBefore
		balanceAfter = bAfter
		direction = ledger.Debit
		createLedger = true

	case transaction.KindWin:
		bBefore, bAfter, err := w.Credit(input.Money)
		if err != nil {
			return nil, err
		}
		balanceBefore = bBefore
		balanceAfter = bAfter
		direction = ledger.Credit
		createLedger = true

	case transaction.KindLoss:
		balanceBefore = w.Balance()
		balanceAfter = w.Balance()
		createLedger = false

	case transaction.KindRefund:
		bBefore, bAfter, err := w.Credit(input.Money)
		if err != nil {
			return nil, err
		}
		balanceBefore = bBefore
		balanceAfter = bAfter
		direction = ledger.Credit
		createLedger = true

	case transaction.KindRollback:
		if parentTx.Kind() == transaction.KindBet {
			bBefore, bAfter, err := w.Credit(input.Money)
			if err != nil {
				return nil, err
			}
			balanceBefore = bBefore
			balanceAfter = bAfter
			direction = ledger.Credit
			createLedger = true
		} else if parentTx.Kind() == transaction.KindWin || parentTx.Kind() == transaction.KindRefund {
			bBefore, bAfter, err := w.Debit(input.Money)
			if err != nil {
				if errors.Is(err, wallet.ErrInsufficientFunds) {
					return uc.rejectTransaction(ctx, tx, txEntity, w, transaction.FailReversalExceedsBalance, input.CorrelationID)
				}
				return nil, err
			}
			balanceBefore = bBefore
			balanceAfter = bAfter
			direction = ledger.Debit
			createLedger = true
		}
	}

	// 10. Persist changes atomically
	var parentID *uuid.UUID
	if parentTx != nil {
		pid := parentTx.ID()
		parentID = &pid
	}

	_ = txEntity.MarkProcessed(balanceAfter, parentID)
	if err := uc.txRepo.Create(ctx, tx, txEntity); err != nil {
		return nil, err
	}

	if createLedger {
		if err := uc.walletRepo.UpdateBalance(ctx, tx, w); err != nil {
			return nil, err
		}

		ledgerEntry, err := ledger.New(
			uuid.New(),
			w.ID(),
			txID,
			direction,
			input.Money,
			balanceBefore,
			balanceAfter,
			time.Now().UTC(),
		)
		if err != nil {
			return nil, err
		}
		if err := uc.ledgerRepo.Create(ctx, tx, ledgerEntry); err != nil {
			return nil, err
		}

		balanceEvt, err := events.NewBalanceChangedEvent(input.CorrelationID, events.WalletBalanceChangedData{
			WalletID:      w.ID(),
			TransactionID: txID,
			Direction:     string(direction),
			Money:         input.Money,
			BalanceBefore: balanceBefore,
			BalanceAfter:  balanceAfter,
			WalletVersion: w.Version(),
		})
		if err != nil {
			return nil, err
		}
		if err := uc.outboxRepo.Create(ctx, tx, balanceEvt); err != nil {
			return nil, err
		}
	}

	processedEvt, err := events.NewProcessedEvent(input.CorrelationID, events.WagerTransactionProcessedData{
		TransactionID:         txID,
		ProviderID:            input.ProviderID,
		ExternalTransactionID: input.ExternalTransactionID,
		WalletID:              w.ID(),
		PlayerID:              w.PlayerID(),
		RoundID:               input.RoundID,
		GameID:                input.GameID,
		Kind:                  string(input.Kind),
		Money:                 input.Money,
		Balance:               balanceAfter,
	})
	if err != nil {
		return nil, err
	}
	if err := uc.outboxRepo.Create(ctx, tx, processedEvt); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit wager transaction: %w", err)
	}

	return &ProcessWagerOutput{
		TransactionID:    txID,
		Status:           string(transaction.StateProcessed),
		Balance:          balanceAfter,
		IdempotentReplay: false,
	}, nil
}

func (uc *ProcessWagerUseCase) checkIdempotencyAndExternalID(
	ctx context.Context,
	input ProcessWagerInput,
	payloadHash string,
) (*ProcessWagerOutput, error) {
	existingTxByKey, err := uc.txRepo.GetByProviderAndIdempotencyKey(ctx, input.ProviderID, input.IdempotencyKey)
	if err == nil && existingTxByKey != nil {
		if existingTxByKey.PayloadHash() != payloadHash {
			return nil, ErrPayloadConflict
		}
		bal := money.Money{}
		if existingTxByKey.BalanceAtProcess() != nil {
			bal = *existingTxByKey.BalanceAtProcess()
		}
		return &ProcessWagerOutput{
			TransactionID:    existingTxByKey.ID(),
			Status:           string(existingTxByKey.State()),
			FailureCode:      existingTxByKey.FailureCode(),
			Balance:          bal,
			IdempotentReplay: true,
		}, nil
	}

	existingTxByExt, err := uc.txRepo.GetByProviderAndExternalID(ctx, input.ProviderID, input.ExternalTransactionID)
	if err == nil && existingTxByExt != nil {
		return nil, ErrExternalIDReused
	}

	return nil, nil
}

func (uc *ProcessWagerUseCase) parkPendingReference(
	ctx context.Context,
	tx pgx.Tx,
	txEntity *transaction.WagerTransaction,
	w *wallet.Wallet,
	parentRefID string,
	correlationID string,
) (*ProcessWagerOutput, error) {
	nextRetry := time.Now().UTC().Add(3 * time.Second)
	balSnapshot := w.Balance()
	_ = txEntity.MarkPendingReference(nextRetry)

	if err := uc.txRepo.Create(ctx, tx, txEntity); err != nil {
		return nil, err
	}

	pendingEvt, err := events.NewPendingReferenceEvent(correlationID, events.WagerTransactionPendingReferenceData{
		TransactionID:                  txEntity.ID(),
		ProviderID:                     txEntity.ProviderID(),
		ExternalTransactionID:          txEntity.ExternalTransactionID(),
		WalletID:                       txEntity.WalletID(),
		Kind:                           string(txEntity.Kind()),
		ReferenceExternalTransactionID: parentRefID,
	})
	if err != nil {
		return nil, err
	}
	if err := uc.outboxRepo.Create(ctx, tx, pendingEvt); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return &ProcessWagerOutput{
		TransactionID:    txEntity.ID(),
		Status:           string(transaction.StatePendingReference),
		Balance:          balSnapshot,
		IdempotentReplay: false,
	}, nil
}

func (uc *ProcessWagerUseCase) rejectTransaction(
	ctx context.Context,
	tx pgx.Tx,
	txEntity *transaction.WagerTransaction,
	w *wallet.Wallet,
	failCode string,
	correlationID string,
) (*ProcessWagerOutput, error) {
	_ = txEntity.MarkRejectedWithBalance(failCode, w.Balance())

	if err := uc.txRepo.Create(ctx, tx, txEntity); err != nil {
		return nil, err
	}

	rejectedEvt, err := events.NewRejectedEvent(correlationID, events.WagerTransactionRejectedData{
		TransactionID:         txEntity.ID(),
		ProviderID:            txEntity.ProviderID(),
		ExternalTransactionID: txEntity.ExternalTransactionID(),
		WalletID:              w.ID(),
		PlayerID:              w.PlayerID(),
		Kind:                  string(txEntity.Kind()),
		FailureCode:           failCode,
		Money:                 txEntity.Amount(),
	})
	if err != nil {
		return nil, err
	}
	if err := uc.outboxRepo.Create(ctx, tx, rejectedEvt); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit rejected transaction: %w", err)
	}

	return &ProcessWagerOutput{
		TransactionID:    txEntity.ID(),
		Status:           string(transaction.StateRejected),
		FailureCode:      &failCode,
		Balance:          w.Balance(),
		IdempotentReplay: false,
	}, nil
}
