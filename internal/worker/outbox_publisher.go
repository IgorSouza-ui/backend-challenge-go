package worker

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/events"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
	"github.com/junglegaming/backend-challenge-go/internal/infra/sqs"
)

type EventPublisher interface {
	Publish(ctx context.Context, env *events.EventEnvelope) error
}

type OutboxPublisher struct {
	txManager  repository.DBTransactionManager
	outboxRepo repository.OutboxRepository
	publisher  EventPublisher
	logger     *slog.Logger
	interval   time.Duration
	batchSize  int
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

func NewOutboxPublisher(
	txManager repository.DBTransactionManager,
	outboxRepo repository.OutboxRepository,
	publisher *sqs.Publisher,
	logger *slog.Logger,
) *OutboxPublisher {
	return NewOutboxPublisherWithInterface(txManager, outboxRepo, publisher, logger)
}

func NewOutboxPublisherWithInterface(
	txManager repository.DBTransactionManager,
	outboxRepo repository.OutboxRepository,
	publisher EventPublisher,
	logger *slog.Logger,
) *OutboxPublisher {
	return &OutboxPublisher{
		txManager:  txManager,
		outboxRepo: outboxRepo,
		publisher:  publisher,
		logger:     logger,
		interval:   300 * time.Millisecond,
		batchSize:  20,
		stopCh:     make(chan struct{}),
	}
}

func (w *OutboxPublisher) Start(ctx context.Context) {
	w.logger.Info("starting Outbox Publisher worker")
	w.wg.Add(1)
	go w.loop(ctx)
}

func (w *OutboxPublisher) Stop(ctx context.Context) error {
	w.logger.Info("stopping Outbox Publisher worker...")
	close(w.stopCh)

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		w.logger.Info("Outbox Publisher stopped cleanly")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *OutboxPublisher) loop(ctx context.Context) {
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
			w.ProcessBatchOnce(ctx)
		}
	}
}

func (w *OutboxPublisher) ProcessBatchOnce(ctx context.Context) {
	tx, err := w.txManager.Begin(ctx)
	if err != nil {
		w.logger.Error("failed to begin tx for outbox batch", "error", err)
		return
	}
	defer tx.Rollback(ctx)

	events, err := w.outboxRepo.FetchPendingForUpdate(ctx, tx, w.batchSize)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			w.logger.Error("failed to fetch pending outbox events", "error", err)
		}
		return
	}

	if len(events) == 0 {
		return
	}

	for _, env := range events {
		if err := w.publisher.Publish(ctx, env); err != nil {
			w.logger.Error("failed to publish outbox event to SQS, backing off", "error", err, "eventId", env.EventID)
			// Exponential backoff
			nextRetry := time.Now().UTC().Add(time.Duration(math.Pow(2, float64(env.Version))) * time.Second)
			_ = w.outboxRepo.IncrementRetry(ctx, tx, env.EventID, nextRetry)
			continue
		}

		if err := w.outboxRepo.MarkPublished(ctx, tx, env.EventID); err != nil {
			w.logger.Error("failed to mark outbox event as published", "error", err, "eventId", env.EventID)
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		w.logger.Error("failed to commit outbox published batch", "error", err)
	}
}
