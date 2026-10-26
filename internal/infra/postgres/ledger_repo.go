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
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
)

type LedgerRepository struct {
	pool *pgxpool.Pool
}

func NewLedgerRepository(pool *pgxpool.Pool) *LedgerRepository {
	return &LedgerRepository{pool: pool}
}

func (r *LedgerRepository) Create(ctx context.Context, tx pgx.Tx, entry *ledger.WalletLedgerEntry) error {
	const query = `
		INSERT INTO wallet_ledger (
			id, wallet_id, transaction_id, direction, amount, currency,
			balance_before, balance_after, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := tx.Exec(ctx, query,
		entry.ID(),
		entry.WalletID(),
		entry.TransactionID(),
		string(entry.Direction()),
		entry.Amount().Cents(),
		entry.Amount().Currency(),
		entry.BalanceBefore().Cents(),
		entry.BalanceAfter().Cents(),
		entry.CreatedAt(),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return repository.ErrConflict
		}
		return fmt.Errorf("failed to insert ledger entry: %w", err)
	}
	return nil
}

func (r *LedgerRepository) ListByWallet(ctx context.Context, walletID uuid.UUID, cursor *time.Time, limit int) ([]*ledger.WalletLedgerEntry, *time.Time, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var query string
	var rows pgx.Rows
	var err error

	if cursor == nil {
		query = `
			SELECT id, wallet_id, transaction_id, direction, amount, currency,
			       balance_before, balance_after, created_at
			FROM wallet_ledger
			WHERE wallet_id = $1
			ORDER BY created_at DESC, id DESC
			LIMIT $2
		`
		rows, err = r.pool.Query(ctx, query, walletID, limit)
	} else {
		query = `
			SELECT id, wallet_id, transaction_id, direction, amount, currency,
			       balance_before, balance_after, created_at
			FROM wallet_ledger
			WHERE wallet_id = $1 AND created_at < $2
			ORDER BY created_at DESC, id DESC
			LIMIT $3
		`
		rows, err = r.pool.Query(ctx, query, walletID, *cursor, limit)
	}

	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var entries []*ledger.WalletLedgerEntry
	var lastCreatedAt *time.Time

	for rows.Next() {
		var (
			id          uuid.UUID
			wid         uuid.UUID
			tid         uuid.UUID
			dirStr      string
			amtCents    int64
			currency    string
			beforeCents int64
			afterCents  int64
			createdAt   time.Time
		)

		if err := rows.Scan(&id, &wid, &tid, &dirStr, &amtCents, &currency, &beforeCents, &afterCents, &createdAt); err != nil {
			return nil, nil, err
		}

		amt, _ := money.FromCents(amtCents, currency)
		before, _ := money.FromCents(beforeCents, currency)
		after, _ := money.FromCents(afterCents, currency)

		entry, err := ledger.New(id, wid, tid, ledger.Direction(dirStr), amt, before, after, createdAt)
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, entry)
		lastCreatedAt = &createdAt
	}

	return entries, lastCreatedAt, rows.Err()
}

func (r *LedgerRepository) CalculateBalance(ctx context.Context, walletID uuid.UUID) (money.Money, int, error) {
	const query = `
		SELECT 
			COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN amount ELSE -amount END), 0) AS total_cents,
			COUNT(*) AS total_entries,
			COALESCE(MAX(currency), 'BRL') AS currency
		FROM wallet_ledger
		WHERE wallet_id = $1
	`
	var (
		totalCents int64
		count      int
		curr       string
	)
	err := r.pool.QueryRow(ctx, query, walletID).Scan(&totalCents, &count, &curr)
	if err != nil {
		return money.Money{}, 0, fmt.Errorf("failed to calculate balance from ledger: %w", err)
	}

	m, err := money.FromCents(totalCents, curr)
	if err != nil {
		return money.Money{}, 0, err
	}
	return m, count, nil
}
