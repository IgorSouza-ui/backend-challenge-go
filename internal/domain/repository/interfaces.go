package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/junglegaming/backend-challenge-go/internal/domain/events"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/transaction"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

var (
	ErrNotFound     = errors.New("entity not found")
	ErrConflict     = errors.New("entity already exists or unique constraint violation")
	ErrDuplicateKey = errors.New("duplicate key violation")
)

type WalletRepository interface {
	Create(ctx context.Context, tx pgx.Tx, w *wallet.Wallet) error
	GetByID(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	GetByIDForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*wallet.Wallet, error)
	GetByPlayerAndCurrency(ctx context.Context, playerID uuid.UUID, currency string) (*wallet.Wallet, error)
	UpdateBalance(ctx context.Context, tx pgx.Tx, w *wallet.Wallet) error
}

type TransactionRepository interface {
	Create(ctx context.Context, tx pgx.Tx, t *transaction.WagerTransaction) error
	Update(ctx context.Context, tx pgx.Tx, t *transaction.WagerTransaction) error
	GetByID(ctx context.Context, id uuid.UUID) (*transaction.WagerTransaction, error)
	GetByProviderAndExternalID(ctx context.Context, providerID, externalID string) (*transaction.WagerTransaction, error)
	GetByProviderAndIdempotencyKey(ctx context.Context, providerID, key string) (*transaction.WagerTransaction, error)
	HasProcessedReversal(ctx context.Context, tx pgx.Tx, parentID uuid.UUID) (bool, error)
	ListPendingReferences(ctx context.Context, limit int) ([]*transaction.WagerTransaction, error)
}

type LedgerRepository interface {
	Create(ctx context.Context, tx pgx.Tx, entry *ledger.WalletLedgerEntry) error
	ListByWallet(ctx context.Context, walletID uuid.UUID, cursor *time.Time, limit int) ([]*ledger.WalletLedgerEntry, *time.Time, error)
	CalculateBalance(ctx context.Context, walletID uuid.UUID) (calculatedBalance money.Money, totalEntries int, err error)
}

type OutboxRepository interface {
	Create(ctx context.Context, tx pgx.Tx, env *events.EventEnvelope) error
	FetchPendingForUpdate(ctx context.Context, tx pgx.Tx, limit int) ([]*events.EventEnvelope, error)
	MarkPublished(ctx context.Context, tx pgx.Tx, eventID uuid.UUID) error
	IncrementRetry(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, nextRetry time.Time) error
}

type InboxRepository interface {
	RegisterOrCheck(ctx context.Context, tx pgx.Tx, consumerName, messageID, payloadHash string) (isNew bool, err error)
}

type DBTransactionManager interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}
