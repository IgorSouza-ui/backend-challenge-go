package ledger

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

var (
	ErrInvalidEntryID       = errors.New("invalid entry ID")
	ErrInvalidWalletID      = errors.New("invalid wallet ID")
	ErrInvalidTransactionID = errors.New("invalid transaction ID")
	ErrInvalidDirection     = errors.New("direction must be DEBIT or CREDIT")
	ErrInvalidAmount        = errors.New("ledger entry amount must be strictly positive")
	ErrMathInconsistency    = errors.New("balanceAfter must equal balanceBefore +/- amount")
	ErrCurrencyMismatch     = errors.New("currency mismatch across ledger amounts")
)

// WalletLedgerEntry is an immutable record in the double-entry/audit ledger.
type WalletLedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

// New creates and validates an immutable ledger entry.
func New(
	id uuid.UUID,
	walletID uuid.UUID,
	transactionID uuid.UUID,
	direction Direction,
	amount money.Money,
	balanceBefore money.Money,
	balanceAfter money.Money,
	createdAt time.Time,
) (*WalletLedgerEntry, error) {
	if id == uuid.Nil {
		return nil, ErrInvalidEntryID
	}
	if walletID == uuid.Nil {
		return nil, ErrInvalidWalletID
	}
	if transactionID == uuid.Nil {
		return nil, ErrInvalidTransactionID
	}
	if direction != Debit && direction != Credit {
		return nil, ErrInvalidDirection
	}
	if !amount.IsPositive() {
		return nil, ErrInvalidAmount
	}

	curr := amount.Currency()
	if balanceBefore.Currency() != curr || balanceAfter.Currency() != curr {
		return nil, ErrCurrencyMismatch
	}

	// Invariant validation: balanceAfter = balanceBefore +/- amount
	if direction == Debit {
		expectedAfter, err := balanceBefore.Sub(amount)
		if err != nil {
			return nil, err
		}
		if !expectedAfter.Equals(balanceAfter) {
			return nil, fmt.Errorf("%w: debit %s from %s should be %s, got %s",
				ErrMathInconsistency, amount.AmountString(), balanceBefore.AmountString(),
				expectedAfter.AmountString(), balanceAfter.AmountString())
		}
	} else {
		expectedAfter, err := balanceBefore.Add(amount)
		if err != nil {
			return nil, err
		}
		if !expectedAfter.Equals(balanceAfter) {
			return nil, fmt.Errorf("%w: credit %s to %s should be %s, got %s",
				ErrMathInconsistency, amount.AmountString(), balanceBefore.AmountString(),
				expectedAfter.AmountString(), balanceAfter.AmountString())
		}
	}

	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	return &WalletLedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     createdAt,
	}, nil
}

// Getters
func (e *WalletLedgerEntry) ID() uuid.UUID              { return e.id }
func (e *WalletLedgerEntry) WalletID() uuid.UUID        { return e.walletID }
func (e *WalletLedgerEntry) TransactionID() uuid.UUID   { return e.transactionID }
func (e *WalletLedgerEntry) Direction() Direction       { return e.direction }
func (e *WalletLedgerEntry) Amount() money.Money        { return e.amount }
func (e *WalletLedgerEntry) BalanceBefore() money.Money { return e.balanceBefore }
func (e *WalletLedgerEntry) BalanceAfter() money.Money  { return e.balanceAfter }
func (e *WalletLedgerEntry) CreatedAt() time.Time       { return e.createdAt }
