package events

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

const (
	TypeWagerTransactionProcessed        = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         = "WagerTransactionRejected"
	TypeWalletBalanceChanged             = "WalletBalanceChanged"
	TypeWagerTransactionPendingReference = "WagerTransactionPendingReference"
)

// EventEnvelope is the standard envelope for outbox events.
type EventEnvelope struct {
	EventID       uuid.UUID       `json:"eventId"`
	EventType     string          `json:"eventType"`
	AggregateID   uuid.UUID       `json:"aggregateId"`
	CorrelationID string          `json:"correlationId"`
	CausationID   *string         `json:"causationId,omitempty"`
	OccurredAt    string          `json:"occurredAt"` // RFC3339 UTC
	Version       int             `json:"version"`
	Data          json.RawMessage `json:"data"`
}

// Payloads

type WagerTransactionProcessedData struct {
	TransactionID         uuid.UUID   `json:"transactionId"`
	ProviderID            string      `json:"providerId"`
	ExternalTransactionID string      `json:"externalTransactionId"`
	WalletID              uuid.UUID   `json:"walletId"`
	PlayerID              uuid.UUID   `json:"playerId"`
	RoundID               string      `json:"roundId"`
	GameID                string      `json:"gameId"`
	Kind                  string      `json:"kind"`
	Money                 money.Money `json:"money"`
	Balance               money.Money `json:"balance"`
}

type WagerTransactionRejectedData struct {
	TransactionID         uuid.UUID   `json:"transactionId"`
	ProviderID            string      `json:"providerId"`
	ExternalTransactionID string      `json:"externalTransactionId"`
	WalletID              uuid.UUID   `json:"walletId"`
	PlayerID              uuid.UUID   `json:"playerId"`
	Kind                  string      `json:"kind"`
	FailureCode           string      `json:"failureCode"`
	Money                 money.Money `json:"money"`
}

type WalletBalanceChangedData struct {
	WalletID      uuid.UUID   `json:"walletId"`
	TransactionID uuid.UUID   `json:"transactionId"`
	Direction     string      `json:"direction"` // DEBIT / CREDIT
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
}

type WagerTransactionPendingReferenceData struct {
	TransactionID                  uuid.UUID `json:"transactionId"`
	ProviderID                     string    `json:"providerId"`
	ExternalTransactionID          string    `json:"externalTransactionId"`
	WalletID                       uuid.UUID `json:"walletId"`
	Kind                           string    `json:"kind"`
	ReferenceExternalTransactionID string    `json:"referenceExternalTransactionId"`
}

// Helpers to construct envelopes

func NewProcessedEvent(
	correlationID string,
	data WagerTransactionProcessedData,
) (*EventEnvelope, error) {
	bytes, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return &EventEnvelope{
		EventID:       uuid.New(),
		EventType:     TypeWagerTransactionProcessed,
		AggregateID:   data.TransactionID,
		CorrelationID: correlationID,
		OccurredAt:    time.Now().UTC().Format(time.RFC3339),
		Version:       1,
		Data:          bytes,
	}, nil
}

func NewRejectedEvent(
	correlationID string,
	data WagerTransactionRejectedData,
) (*EventEnvelope, error) {
	bytes, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return &EventEnvelope{
		EventID:       uuid.New(),
		EventType:     TypeWagerTransactionRejected,
		AggregateID:   data.TransactionID,
		CorrelationID: correlationID,
		OccurredAt:    time.Now().UTC().Format(time.RFC3339),
		Version:       1,
		Data:          bytes,
	}, nil
}

func NewBalanceChangedEvent(
	correlationID string,
	data WalletBalanceChangedData,
) (*EventEnvelope, error) {
	bytes, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return &EventEnvelope{
		EventID:       uuid.New(),
		EventType:     TypeWalletBalanceChanged,
		AggregateID:   data.WalletID,
		CorrelationID: correlationID,
		OccurredAt:    time.Now().UTC().Format(time.RFC3339),
		Version:       1,
		Data:          bytes,
	}, nil
}

func NewPendingReferenceEvent(
	correlationID string,
	data WagerTransactionPendingReferenceData,
) (*EventEnvelope, error) {
	bytes, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return &EventEnvelope{
		EventID:       uuid.New(),
		EventType:     TypeWagerTransactionPendingReference,
		AggregateID:   data.TransactionID,
		CorrelationID: correlationID,
		OccurredAt:    time.Now().UTC().Format(time.RFC3339),
		Version:       1,
		Data:          bytes,
	}, nil
}
