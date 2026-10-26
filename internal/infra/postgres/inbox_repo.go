package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type InboxRepository struct {
	pool *pgxpool.Pool
}

func NewInboxRepository(pool *pgxpool.Pool) *InboxRepository {
	return &InboxRepository{pool: pool}
}

// RegisterOrCheck returns isNew=true if the message was not seen before.
// If it was seen before with the same hash, isNew=false.
// If seen before with a different hash, it returns an error.
func (r *InboxRepository) RegisterOrCheck(ctx context.Context, tx pgx.Tx, consumerName, messageID, payloadHash string) (bool, error) {
	const selectQuery = `
		SELECT payload_hash
		FROM inbox_messages
		WHERE consumer_name = $1 AND message_id = $2
	`
	var existingHash string
	err := tx.QueryRow(ctx, selectQuery, consumerName, messageID).Scan(&existingHash)
	if err == nil {
		if existingHash != payloadHash {
			return false, fmt.Errorf("message %s already processed with different payload hash", messageID)
		}
		return false, nil // duplicate seen
	}
	if err != pgx.ErrNoRows {
		return false, err
	}

	const insertQuery = `
		INSERT INTO inbox_messages (id, consumer_name, message_id, payload_hash, status, created_at, processed_at)
		VALUES ($1, $2, $3, $4, 'PROCESSED', NOW(), NOW())
	`
	_, err = tx.Exec(ctx, insertQuery, uuid.New(), consumerName, messageID, payloadHash)
	if err != nil {
		return false, fmt.Errorf("failed to register inbox message: %w", err)
	}

	return true, nil
}
