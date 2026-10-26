package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/domain/events"
)

type OutboxRepository struct {
	pool *pgxpool.Pool
}

func NewOutboxRepository(pool *pgxpool.Pool) *OutboxRepository {
	return &OutboxRepository{pool: pool}
}

func (r *OutboxRepository) Create(ctx context.Context, tx pgx.Tx, env *events.EventEnvelope) error {
	const query = `
		INSERT INTO outbox_events (
			id, aggregate_type, aggregate_id, event_type, payload,
			status, retry_count, next_retry_at, created_at
		) VALUES ($1, $2, $3, $4, $5, 'PENDING', 0, NOW(), NOW())
	`
	payloadBytes, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("failed to marshal outbox event: %w", err)
	}

	_, err = tx.Exec(ctx, query,
		env.EventID,
		env.EventType,
		env.AggregateID,
		env.EventType,
		payloadBytes,
	)
	if err != nil {
		return fmt.Errorf("failed to insert outbox event: %w", err)
	}
	return nil
}

func (r *OutboxRepository) FetchPendingForUpdate(ctx context.Context, tx pgx.Tx, limit int) ([]*events.EventEnvelope, error) {
	const query = `
		SELECT id, payload
		FROM outbox_events
		WHERE status = 'PENDING' AND next_retry_at <= NOW()
		ORDER BY created_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`
	rows, err := tx.Query(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*events.EventEnvelope
	for rows.Next() {
		var id uuid.UUID
		var payloadBytes []byte
		if err := rows.Scan(&id, &payloadBytes); err != nil {
			return nil, err
		}
		var env events.EventEnvelope
		if err := json.Unmarshal(payloadBytes, &env); err != nil {
			return nil, err
		}
		list = append(list, &env)
	}
	return list, rows.Err()
}

func (r *OutboxRepository) MarkPublished(ctx context.Context, tx pgx.Tx, eventID uuid.UUID) error {
	const query = `
		UPDATE outbox_events
		SET status = 'PUBLISHED', published_at = NOW()
		WHERE id = $1
	`
	_, err := tx.Exec(ctx, query, eventID)
	return err
}

func (r *OutboxRepository) IncrementRetry(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, nextRetry time.Time) error {
	const query = `
		UPDATE outbox_events
		SET retry_count = retry_count + 1, next_retry_at = $1
		WHERE id = $2
	`
	_, err := tx.Exec(ctx, query, nextRetry, eventID)
	return err
}
