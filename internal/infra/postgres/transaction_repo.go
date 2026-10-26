package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
	"github.com/junglegaming/backend-challenge-go/internal/domain/transaction"
)

type TransactionRepository struct {
	pool *pgxpool.Pool
}

func NewTransactionRepository(pool *pgxpool.Pool) *TransactionRepository {
	return &TransactionRepository{pool: pool}
}

func (r *TransactionRepository) Create(ctx context.Context, tx pgx.Tx, t *transaction.WagerTransaction) error {
	const query = `
		INSERT INTO wager_transactions (
			id, provider_id, external_transaction_id, idempotency_key, payload_hash,
			wallet_id, player_id, round_id, game_id, kind, amount, currency,
			reference_external_transaction_id, resolved_internal_reference_id,
			state, failure_code, balance_at_process, retry_count, next_retry_at,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19, $20, $21
		)
	`
	var balCents *int64
	if t.BalanceAtProcess() != nil {
		c := t.BalanceAtProcess().Cents()
		balCents = &c
	}

	_, err := tx.Exec(ctx, query,
		t.ID(),
		t.ProviderID(),
		t.ExternalTransactionID(),
		t.IdempotencyKey(),
		t.PayloadHash(),
		t.WalletID(),
		t.PlayerID(),
		t.RoundID(),
		t.GameID(),
		string(t.Kind()),
		t.Amount().Cents(),
		t.Amount().Currency(),
		t.ReferenceExternalTransactionID(),
		t.ResolvedInternalReferenceID(),
		string(t.State()),
		t.FailureCode(),
		balCents,
		t.RetryCount(),
		t.NextRetryAt(),
		t.CreatedAt(),
		t.UpdatedAt(),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return repository.ErrConflict
		}
		return fmt.Errorf("failed to insert wager transaction: %w", err)
	}
	return nil
}

func (r *TransactionRepository) Update(ctx context.Context, tx pgx.Tx, t *transaction.WagerTransaction) error {
	const query = `
		UPDATE wager_transactions
		SET state = $1, failure_code = $2, balance_at_process = $3,
		    resolved_internal_reference_id = $4, retry_count = $5,
		    next_retry_at = $6, updated_at = $7
		WHERE id = $8
	`
	var balCents *int64
	if t.BalanceAtProcess() != nil {
		c := t.BalanceAtProcess().Cents()
		balCents = &c
	}

	_, err := tx.Exec(ctx, query,
		string(t.State()),
		t.FailureCode(),
		balCents,
		t.ResolvedInternalReferenceID(),
		t.RetryCount(),
		t.NextRetryAt(),
		t.UpdatedAt(),
		t.ID(),
	)
	if err != nil {
		return fmt.Errorf("failed to update wager transaction: %w", err)
	}
	return nil
}

func (r *TransactionRepository) GetByID(ctx context.Context, id uuid.UUID) (*transaction.WagerTransaction, error) {
	const query = `
		SELECT id, provider_id, external_transaction_id, idempotency_key, payload_hash,
		       wallet_id, player_id, round_id, game_id, kind, amount, currency,
		       reference_external_transaction_id, resolved_internal_reference_id,
		       state, failure_code, balance_at_process, retry_count, next_retry_at,
		       created_at, updated_at
		FROM wager_transactions
		WHERE id = $1
	`
	return r.scanOne(r.pool.QueryRow(ctx, query, id))
}

func (r *TransactionRepository) GetByProviderAndExternalID(ctx context.Context, providerID, externalID string) (*transaction.WagerTransaction, error) {
	const query = `
		SELECT id, provider_id, external_transaction_id, idempotency_key, payload_hash,
		       wallet_id, player_id, round_id, game_id, kind, amount, currency,
		       reference_external_transaction_id, resolved_internal_reference_id,
		       state, failure_code, balance_at_process, retry_count, next_retry_at,
		       created_at, updated_at
		FROM wager_transactions
		WHERE provider_id = $1 AND external_transaction_id = $2
	`
	return r.scanOne(r.pool.QueryRow(ctx, query, providerID, externalID))
}

func (r *TransactionRepository) GetByProviderAndIdempotencyKey(ctx context.Context, providerID, key string) (*transaction.WagerTransaction, error) {
	const query = `
		SELECT id, provider_id, external_transaction_id, idempotency_key, payload_hash,
		       wallet_id, player_id, round_id, game_id, kind, amount, currency,
		       reference_external_transaction_id, resolved_internal_reference_id,
		       state, failure_code, balance_at_process, retry_count, next_retry_at,
		       created_at, updated_at
		FROM wager_transactions
		WHERE provider_id = $1 AND idempotency_key = $2
	`
	return r.scanOne(r.pool.QueryRow(ctx, query, providerID, key))
}

func (r *TransactionRepository) HasProcessedReversal(ctx context.Context, tx pgx.Tx, parentID uuid.UUID) (bool, error) {
	const query = `
		SELECT EXISTS (
			SELECT 1
			FROM wager_transactions
			WHERE resolved_internal_reference_id = $1
			  AND kind IN ('REFUND', 'ROLLBACK')
			  AND state = 'PROCESSED'
		)
	`
	var exists bool
	err := tx.QueryRow(ctx, query, parentID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check processed reversal: %w", err)
	}
	return exists, nil
}

func (r *TransactionRepository) ListPendingReferences(ctx context.Context, limit int) ([]*transaction.WagerTransaction, error) {
	const query = `
		SELECT id, provider_id, external_transaction_id, idempotency_key, payload_hash,
		       wallet_id, player_id, round_id, game_id, kind, amount, currency,
		       reference_external_transaction_id, resolved_internal_reference_id,
		       state, failure_code, balance_at_process, retry_count, next_retry_at,
		       created_at, updated_at
		FROM wager_transactions
		WHERE state = 'PENDING_REFERENCE' AND (next_retry_at IS NULL OR next_retry_at <= NOW())
		ORDER BY created_at ASC
		LIMIT $1
	`
	rows, err := r.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*transaction.WagerTransaction
	for rows.Next() {
		t, err := r.scanRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func (r *TransactionRepository) scanOne(row pgx.Row) (*transaction.WagerTransaction, error) {
	t, err := r.scanRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return t, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func (r *TransactionRepository) scanRow(row scannable) (*transaction.WagerTransaction, error) {
	var (
		id                             uuid.UUID
		providerID                     string
		externalTransactionID          string
		idempotencyKey                 string
		payloadHash                    string
		walletID                       uuid.UUID
		playerID                       uuid.UUID
		roundID                        string
		gameID                         string
		kindStr                        string
		amountCents                    int64
		currency                       string
		referenceExternalTransactionID *string
		resolvedInternalReferenceID    *uuid.UUID
		stateStr                       string
		failureCode                    *string
		balanceCents                   *int64
		retryCount                     int
		nextRetryAt                    *time.Time
		createdAt                      time.Time
		updatedAt                      time.Time
	)

	err := row.Scan(
		&id, &providerID, &externalTransactionID, &idempotencyKey, &payloadHash,
		&walletID, &playerID, &roundID, &gameID, &kindStr, &amountCents, &currency,
		&referenceExternalTransactionID, &resolvedInternalReferenceID,
		&stateStr, &failureCode, &balanceCents, &retryCount, &nextRetryAt,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	amt, err := money.FromCents(amountCents, currency)
	if err != nil {
		return nil, err
	}

	var balAtProcess *money.Money
	if balanceCents != nil {
		m, err := money.FromCents(*balanceCents, currency)
		if err != nil {
			return nil, err
		}
		balAtProcess = &m
	}

	return transaction.Rehydrate(
		id, providerID, externalTransactionID, idempotencyKey, payloadHash,
		walletID, playerID, roundID, gameID, transaction.Kind(kindStr), amt,
		referenceExternalTransactionID, resolvedInternalReferenceID,
		transaction.State(stateStr), failureCode, balAtProcess,
		retryCount, nextRetryAt, createdAt, updatedAt,
	), nil
}
