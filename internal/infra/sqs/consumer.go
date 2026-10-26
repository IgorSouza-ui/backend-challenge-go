package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/transaction"
	"github.com/junglegaming/backend-challenge-go/internal/usecase"
)

type ConsumerConfig struct {
	QueueURL          string
	MaxMessages       int32
	WaitTimeSeconds   int32
	VisibilityTimeout int32
	Concurrency       int
}

type Consumer struct {
	client  SQSClient
	cfg     ConsumerConfig
	wagerUC *usecase.ProcessWagerUseCase
	logger  *slog.Logger
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

func NewConsumer(
	client SQSClient,
	cfg ConsumerConfig,
	wagerUC *usecase.ProcessWagerUseCase,
	logger *slog.Logger,
) *Consumer {
	if cfg.MaxMessages == 0 {
		cfg.MaxMessages = 10
	}
	if cfg.WaitTimeSeconds == 0 {
		cfg.WaitTimeSeconds = 10
	}
	if cfg.VisibilityTimeout == 0 {
		cfg.VisibilityTimeout = 30
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 2
	}
	return &Consumer{
		client:  client,
		cfg:     cfg,
		wagerUC: wagerUC,
		logger:  logger,
		stopCh:  make(chan struct{}),
	}
}

func (c *Consumer) Start(ctx context.Context) {
	c.logger.Info("starting SQS wager consumer", "queue", c.cfg.QueueURL)
	for i := 0; i < c.cfg.Concurrency; i++ {
		c.wg.Add(1)
		go c.workerLoop(ctx, i)
	}
}

func (c *Consumer) Stop(ctx context.Context) error {
	c.logger.Info("stopping SQS wager consumer...")
	close(c.stopCh)

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		c.logger.Info("SQS consumer stopped cleanly")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Consumer) workerLoop(ctx context.Context, workerID int) {
	defer c.wg.Done()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}

		res, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(c.cfg.QueueURL),
			MaxNumberOfMessages: c.cfg.MaxMessages,
			WaitTimeSeconds:     c.cfg.WaitTimeSeconds,
			VisibilityTimeout:   c.cfg.VisibilityTimeout,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			c.logger.Error("failed to receive messages from SQS", "error", err, "worker", workerID)
			time.Sleep(1 * time.Second)
			continue
		}

		for _, msg := range res.Messages {
			if err := c.handleMessage(ctx, msg); err != nil {
				c.logger.Error("failed processing SQS message, letting visibility expire for redrive", "error", err, "messageId", *msg.MessageId)
				// Do not delete -> SQS retry or redrive to DLQ
			} else {
				// Delete message after successful commit
				_, _ = c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
					QueueUrl:      aws.String(c.cfg.QueueURL),
					ReceiptHandle: msg.ReceiptHandle,
				})
			}
		}
	}
}

func (c *Consumer) handleMessage(ctx context.Context, msg types.Message) error {
	var envelope WagerRequestedSQSMessage
	if err := json.Unmarshal([]byte(*msg.Body), &envelope); err != nil {
		c.logger.Error("poison message: invalid JSON envelope, will be sent to DLQ", "error", err)
		return err
	}

	d := envelope.Data
	pid, err := uuid.Parse(d.PlayerID)
	if err != nil {
		return fmt.Errorf("invalid player UUID in SQS message: %w", err)
	}
	wid, err := uuid.Parse(d.WalletID)
	if err != nil {
		return fmt.Errorf("invalid wallet UUID in SQS message: %w", err)
	}

	m, err := money.New(d.Money.Amount, d.Money.Currency)
	if err != nil {
		return fmt.Errorf("invalid money in SQS message: %w", err)
	}

	input := usecase.ProcessWagerInput{
		ProviderID:                     d.ProviderID,
		ExternalTransactionID:          d.ExternalTransactionID,
		IdempotencyKey:                 d.IdempotencyKey,
		PlayerID:                       pid,
		WalletID:                       wid,
		RoundID:                        d.RoundID,
		GameID:                         d.GameID,
		Kind:                           transaction.Kind(d.Kind),
		Money:                          m,
		ReferenceExternalTransactionID: d.ReferenceExternalTransactionID,
		CorrelationID:                  envelope.MessageID,
		IsSQS:                          true,
		MessageID:                      envelope.MessageID,
	}

	_, err = c.wagerUC.Execute(ctx, input)
	return err
}
