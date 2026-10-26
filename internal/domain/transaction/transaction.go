package transaction

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

type State string

const (
	StatePending          State = "PENDING"
	StatePendingReference State = "PENDING_REFERENCE"
	StateProcessed        State = "PROCESSED"
	StateRejected         State = "REJECTED"
	StateFailed           State = "FAILED"
)

const (
	FailInsufficientFunds         = "INSUFFICIENT_FUNDS"
	FailReferenceNotFound         = "REFERENCE_NOT_FOUND"
	FailInvalidReferenceState     = "INVALID_REFERENCE_STATE"
	FailInvalidReferenceOperation = "INVALID_REFERENCE_OPERATION"
	FailDuplicateReversal         = "DUPLICATE_REVERSAL"
	FailReversalExceedsBalance    = "REVERSAL_EXCEEDS_BALANCE"
	FailCurrencyMismatch          = "CURRENCY_MISMATCH"
	FailInvalidAmount             = "INVALID_AMOUNT"
)

var (
	ErrInvalidKind       = errors.New("invalid transaction kind")
	ErrTerminalState     = errors.New("transaction is in terminal state and cannot transition")
	ErrInvalidTransition = errors.New("invalid state transition")
	ErrLossMustBeZero    = errors.New("LOSS transaction requires amount to be exactly 0.00")
	ErrNonLossMustBePos  = errors.New("transaction kind requires amount strictly greater than zero")
	ErrReferenceRequired = errors.New("referenceExternalTransactionId is required for REFUND and ROLLBACK")
)

// WagerTransaction represents a financial or gaming operation.
type WagerTransaction struct {
	id                             uuid.UUID
	providerID                     string
	externalTransactionID          string
	idempotencyKey                 string
	payloadHash                    string
	walletID                       uuid.UUID
	playerID                       uuid.UUID
	roundID                        string
	gameID                         string
	kind                           Kind
	amount                         money.Money
	referenceExternalTransactionID *string
	resolvedInternalReferenceID    *uuid.UUID
	state                          State
	failureCode                    *string
	balanceAtProcess               *money.Money
	retryCount                     int
	nextRetryAt                    *time.Time
	createdAt                      time.Time
	updatedAt                      time.Time
}

// NewExternal creates an external transaction in PENDING state.
func NewExternal(
	id uuid.UUID,
	providerID string,
	externalTransactionID string,
	idempotencyKey string,
	payloadHash string,
	walletID uuid.UUID,
	playerID uuid.UUID,
	roundID string,
	gameID string,
	kind Kind,
	amount money.Money,
	refExtID *string,
) (*WagerTransaction, error) {
	if kind == KindOpening {
		return nil, errors.New("OPENING transactions are internal only")
	}

	if err := validateKindAndAmount(kind, amount, refExtID); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	return &WagerTransaction{
		id:                             id,
		providerID:                     providerID,
		externalTransactionID:          externalTransactionID,
		idempotencyKey:                 idempotencyKey,
		payloadHash:                    payloadHash,
		walletID:                       walletID,
		playerID:                       playerID,
		roundID:                        roundID,
		gameID:                         gameID,
		kind:                           kind,
		amount:                         amount,
		referenceExternalTransactionID: refExtID,
		state:                          StatePending,
		retryCount:                     0,
		createdAt:                      now,
		updatedAt:                      now,
	}, nil
}

// NewOpening creates an internal OPENING transaction for a newly initialized wallet.
func NewOpening(id uuid.UUID, walletID uuid.UUID, playerID uuid.UUID, amount money.Money) (*WagerTransaction, error) {
	if !amount.IsPositive() {
		return nil, ErrNonLossMustBePos
	}
	now := time.Now().UTC()
	return &WagerTransaction{
		id:               id,
		providerID:       "internal",
		walletID:         walletID,
		playerID:         playerID,
		kind:             KindOpening,
		amount:           amount,
		state:            StateProcessed,
		balanceAtProcess: &amount,
		createdAt:        now,
		updatedAt:        now,
	}, nil
}

// Rehydrate reconstructs a transaction from the database.
func Rehydrate(
	id uuid.UUID,
	providerID string,
	externalTransactionID string,
	idempotencyKey string,
	payloadHash string,
	walletID uuid.UUID,
	playerID uuid.UUID,
	roundID string,
	gameID string,
	kind Kind,
	amount money.Money,
	referenceExternalTransactionID *string,
	resolvedInternalReferenceID *uuid.UUID,
	state State,
	failureCode *string,
	balanceAtProcess *money.Money,
	retryCount int,
	nextRetryAt *time.Time,
	createdAt time.Time,
	updatedAt time.Time,
) *WagerTransaction {
	return &WagerTransaction{
		id:                             id,
		providerID:                     providerID,
		externalTransactionID:          externalTransactionID,
		idempotencyKey:                 idempotencyKey,
		payloadHash:                    payloadHash,
		walletID:                       walletID,
		playerID:                       playerID,
		roundID:                        roundID,
		gameID:                         gameID,
		kind:                           kind,
		amount:                         amount,
		referenceExternalTransactionID: referenceExternalTransactionID,
		resolvedInternalReferenceID:    resolvedInternalReferenceID,
		state:                          state,
		failureCode:                    failureCode,
		balanceAtProcess:               balanceAtProcess,
		retryCount:                     retryCount,
		nextRetryAt:                    nextRetryAt,
		createdAt:                      createdAt,
		updatedAt:                      updatedAt,
	}
}

// Transitions

func (t *WagerTransaction) MarkProcessed(balanceAtProcess money.Money, resolvedRef *uuid.UUID) error {
	if t.IsTerminal() {
		return fmt.Errorf("%w: currently %s", ErrTerminalState, t.state)
	}
	t.state = StateProcessed
	t.balanceAtProcess = &balanceAtProcess
	if resolvedRef != nil {
		t.resolvedInternalReferenceID = resolvedRef
	}
	t.updatedAt = time.Now().UTC()
	return nil
}

func (t *WagerTransaction) MarkRejected(failureCode string) error {
	if t.IsTerminal() {
		return fmt.Errorf("%w: currently %s", ErrTerminalState, t.state)
	}
	t.state = StateRejected
	t.failureCode = &failureCode
	t.updatedAt = time.Now().UTC()
	return nil
}

func (t *WagerTransaction) MarkRejectedWithBalance(failureCode string, balanceAtProcess money.Money) error {
	if t.IsTerminal() {
		return fmt.Errorf("%w: currently %s", ErrTerminalState, t.state)
	}
	t.state = StateRejected
	t.failureCode = &failureCode
	t.balanceAtProcess = &balanceAtProcess
	t.updatedAt = time.Now().UTC()
	return nil
}

func (t *WagerTransaction) MarkPendingReference(nextRetry time.Time) error {
	if t.IsTerminal() {
		return fmt.Errorf("%w: currently %s", ErrTerminalState, t.state)
	}
	t.state = StatePendingReference
	t.nextRetryAt = &nextRetry
	t.updatedAt = time.Now().UTC()
	return nil
}

func (t *WagerTransaction) IncrementRetry(nextRetry time.Time) {
	t.retryCount++
	t.nextRetryAt = &nextRetry
	t.updatedAt = time.Now().UTC()
}

func (t *WagerTransaction) IsTerminal() bool {
	return t.state == StateProcessed || t.state == StateRejected || t.state == StateFailed
}

// Getters
func (t *WagerTransaction) ID() uuid.UUID                 { return t.id }
func (t *WagerTransaction) ProviderID() string            { return t.providerID }
func (t *WagerTransaction) ExternalTransactionID() string { return t.externalTransactionID }
func (t *WagerTransaction) IdempotencyKey() string        { return t.idempotencyKey }
func (t *WagerTransaction) PayloadHash() string           { return t.payloadHash }
func (t *WagerTransaction) WalletID() uuid.UUID           { return t.walletID }
func (t *WagerTransaction) PlayerID() uuid.UUID           { return t.playerID }
func (t *WagerTransaction) RoundID() string               { return t.roundID }
func (t *WagerTransaction) GameID() string                { return t.gameID }
func (t *WagerTransaction) Kind() Kind                    { return t.kind }
func (t *WagerTransaction) Amount() money.Money           { return t.amount }
func (t *WagerTransaction) ReferenceExternalTransactionID() *string {
	return t.referenceExternalTransactionID
}
func (t *WagerTransaction) ResolvedInternalReferenceID() *uuid.UUID {
	return t.resolvedInternalReferenceID
}
func (t *WagerTransaction) State() State                   { return t.state }
func (t *WagerTransaction) FailureCode() *string           { return t.failureCode }
func (t *WagerTransaction) BalanceAtProcess() *money.Money { return t.balanceAtProcess }
func (t *WagerTransaction) RetryCount() int                { return t.retryCount }
func (t *WagerTransaction) NextRetryAt() *time.Time        { return t.nextRetryAt }
func (t *WagerTransaction) CreatedAt() time.Time           { return t.createdAt }
func (t *WagerTransaction) UpdatedAt() time.Time           { return t.updatedAt }

func validateKindAndAmount(k Kind, m money.Money, ref *string) error {
	switch k {
	case KindLoss:
		if !m.IsZero() {
			return ErrLossMustBeZero
		}
	case KindBet, KindWin:
		if !m.IsPositive() {
			return ErrNonLossMustBePos
		}
	case KindRefund, KindRollback:
		if !m.IsPositive() {
			return ErrNonLossMustBePos
		}
		if ref == nil || *ref == "" {
			return ErrReferenceRequired
		}
	default:
		return ErrInvalidKind
	}
	return nil
}
