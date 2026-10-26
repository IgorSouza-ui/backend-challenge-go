package sqs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/domain/events"
)

type SQSClient interface {
	SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

type Publisher struct {
	client   SQSClient
	queueURL string
}

func NewPublisher(client SQSClient, queueURL string) *Publisher {
	return &Publisher{client: client, queueURL: queueURL}
}

func (p *Publisher) Publish(ctx context.Context, env *events.EventEnvelope) error {
	bodyBytes, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("failed to marshal event for SQS: %w", err)
	}

	dedupID := env.EventID.String()
	groupID := env.AggregateID.String()

	_, err = p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(p.queueURL),
		MessageBody:            aws.String(string(bodyBytes)),
		MessageDeduplicationId: aws.String(dedupID),
		MessageGroupId:         aws.String(groupID),
	})
	if err != nil {
		return fmt.Errorf("failed to send message to SQS: %w", err)
	}

	return nil
}

// SQS Message Envelope
type WagerRequestedSQSMessage struct {
	MessageID  string                `json:"messageId"`
	Type       string                `json:"type"`
	OccurredAt string                `json:"occurredAt"`
	Data       WagerRequestedDataDTO `json:"data"`
}

type WagerRequestedDataDTO struct {
	ProviderID            string `json:"providerId"`
	ExternalTransactionID string `json:"externalTransactionId"`
	IdempotencyKey        string `json:"idempotencyKey"`
	PlayerID              string `json:"playerId"`
	WalletID              string `json:"walletId"`
	RoundID               string `json:"roundId"`
	GameID                string `json:"gameId"`
	Kind                  string `json:"kind"`
	Money                 struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"money"`
	ReferenceExternalTransactionID *string `json:"referenceExternalTransactionId,omitempty"`
}
