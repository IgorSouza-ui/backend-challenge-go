package usecase

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/events"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
	"github.com/junglegaming/backend-challenge-go/internal/domain/transaction"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

type OpenWalletInput struct {
	PlayerID       uuid.UUID
	InitialBalance money.Money
	CorrelationID  string
}

type OpenWalletOutput struct {
	ID       uuid.UUID   `json:"id"`
	PlayerID uuid.UUID   `json:"playerId"`
	Balance  money.Money `json:"balance"`
	Version  int64       `json:"version"`
}

type OpenWalletUseCase struct {
	txManager  repository.DBTransactionManager
	walletRepo repository.WalletRepository
	txRepo     repository.TransactionRepository
	ledgerRepo repository.LedgerRepository
	outboxRepo repository.OutboxRepository
}

func NewOpenWalletUseCase(
	txManager repository.DBTransactionManager,
	walletRepo repository.WalletRepository,
	txRepo repository.TransactionRepository,
	ledgerRepo repository.LedgerRepository,
	outboxRepo repository.OutboxRepository,
) *OpenWalletUseCase {
	return &OpenWalletUseCase{
		txManager:  txManager,
		walletRepo: walletRepo,
		txRepo:     txRepo,
		ledgerRepo: ledgerRepo,
		outboxRepo: outboxRepo,
	}
}

func (uc *OpenWalletUseCase) Execute(ctx context.Context, input OpenWalletInput) (*OpenWalletOutput, error) {
	// Check if wallet already exists for this player and currency
	existing, err := uc.walletRepo.GetByPlayerAndCurrency(ctx, input.PlayerID, input.InitialBalance.Currency())
	if err == nil && existing != nil {
		return nil, fmt.Errorf("%w: wallet already exists for player %s in currency %s",
			repository.ErrConflict, input.PlayerID, input.InitialBalance.Currency())
	}

	walletID := uuid.New()
	w, err := wallet.New(walletID, input.PlayerID, input.InitialBalance)
	if err != nil {
		return nil, err
	}

	tx, err := uc.txManager.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Persist wallet
	if err := uc.walletRepo.Create(ctx, tx, w); err != nil {
		return nil, err
	}

	// If initial balance is positive, create OPENING transaction, ledger entry and outbox events
	if input.InitialBalance.IsPositive() {
		txID := uuid.New()
		openingTx, err := transaction.NewOpening(txID, walletID, input.PlayerID, input.InitialBalance)
		if err != nil {
			return nil, err
		}

		if err := uc.txRepo.Create(ctx, tx, openingTx); err != nil {
			return nil, err
		}

		zeroMoney, _ := money.Zero(input.InitialBalance.Currency())
		ledgerEntry, err := ledger.New(
			uuid.New(),
			walletID,
			txID,
			ledger.Credit,
			input.InitialBalance,
			zeroMoney,
			input.InitialBalance,
			w.CreatedAt(),
		)
		if err != nil {
			return nil, err
		}

		if err := uc.ledgerRepo.Create(ctx, tx, ledgerEntry); err != nil {
			return nil, err
		}

		// Outbox: WagerTransactionProcessed & WalletBalanceChanged
		processedEvt, err := events.NewProcessedEvent(input.CorrelationID, events.WagerTransactionProcessedData{
			TransactionID: txID,
			ProviderID:    "internal",
			WalletID:      walletID,
			PlayerID:      input.PlayerID,
			Kind:          string(transaction.KindOpening),
			Money:         input.InitialBalance,
			Balance:       input.InitialBalance,
		})
		if err != nil {
			return nil, err
		}
		if err := uc.outboxRepo.Create(ctx, tx, processedEvt); err != nil {
			return nil, err
		}

		balanceEvt, err := events.NewBalanceChangedEvent(input.CorrelationID, events.WalletBalanceChangedData{
			WalletID:      walletID,
			TransactionID: txID,
			Direction:     string(ledger.Credit),
			Money:         input.InitialBalance,
			BalanceBefore: zeroMoney,
			BalanceAfter:  input.InitialBalance,
			WalletVersion: w.Version(),
		})
		if err != nil {
			return nil, err
		}
		if err := uc.outboxRepo.Create(ctx, tx, balanceEvt); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit open wallet: %w", err)
	}

	return &OpenWalletOutput{
		ID:       w.ID(),
		PlayerID: w.PlayerID(),
		Balance:  w.Balance(),
		Version:  w.Version(),
	}, nil
}
