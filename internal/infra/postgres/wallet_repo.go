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
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

type WalletRepository struct {
	pool *pgxpool.Pool
}

func NewWalletRepository(pool *pgxpool.Pool) *WalletRepository {
	return &WalletRepository{pool: pool}
}

func (r *WalletRepository) Create(ctx context.Context, tx pgx.Tx, w *wallet.Wallet) error {
	const query = `
		INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := tx.Exec(ctx, query,
		w.ID(),
		w.PlayerID(),
		w.Currency(),
		w.Balance().Cents(),
		w.Version(),
		w.CreatedAt(),
		w.UpdatedAt(),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return repository.ErrConflict
		}
		return fmt.Errorf("failed to insert wallet: %w", err)
	}
	return nil
}

func (r *WalletRepository) GetByID(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	const query = `
		SELECT id, player_id, currency, balance, version, created_at, updated_at
		FROM wallets
		WHERE id = $1
	`
	return r.scanOne(r.pool.QueryRow(ctx, query, id))
}

func (r *WalletRepository) GetByIDForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*wallet.Wallet, error) {
	const query = `
		SELECT id, player_id, currency, balance, version, created_at, updated_at
		FROM wallets
		WHERE id = $1
		FOR UPDATE
	`
	return r.scanOne(tx.QueryRow(ctx, query, id))
}

func (r *WalletRepository) GetByPlayerAndCurrency(ctx context.Context, playerID uuid.UUID, currency string) (*wallet.Wallet, error) {
	const query = `
		SELECT id, player_id, currency, balance, version, created_at, updated_at
		FROM wallets
		WHERE player_id = $1 AND currency = $2
	`
	return r.scanOne(r.pool.QueryRow(ctx, query, playerID, currency))
}

func (r *WalletRepository) UpdateBalance(ctx context.Context, tx pgx.Tx, w *wallet.Wallet) error {
	const query = `
		UPDATE wallets
		SET balance = $1, version = $2, updated_at = $3
		WHERE id = $4 AND version = $5
	`
	oldVersion := w.Version() - 1
	res, err := tx.Exec(ctx, query,
		w.Balance().Cents(),
		w.Version(),
		w.UpdatedAt(),
		w.ID(),
		oldVersion,
	)
	if err != nil {
		return fmt.Errorf("failed to update wallet balance: %w", err)
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("lost update detected: wallet %s version %d modified concurrently", w.ID(), oldVersion)
	}
	return nil
}

func (r *WalletRepository) scanOne(row pgx.Row) (*wallet.Wallet, error) {
	var (
		id        uuid.UUID
		playerID  uuid.UUID
		currency  string
		cents     int64
		version   int64
		createdAt time.Time
		updatedAt time.Time
	)
	err := row.Scan(&id, &playerID, &currency, &cents, &version, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, fmt.Errorf("failed to scan wallet: %w", err)
	}

	bal, err := money.FromCents(cents, currency)
	if err != nil {
		return nil, err
	}

	return wallet.Rehydrate(id, playerID, currency, bal, version, createdAt, updatedAt)
}
