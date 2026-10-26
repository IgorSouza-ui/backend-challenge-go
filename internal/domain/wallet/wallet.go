package wallet

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

var (
	ErrInvalidWalletID   = errors.New("invalid wallet ID")
	ErrInvalidPlayerID   = errors.New("invalid player ID")
	ErrInsufficientFunds = errors.New("insufficient funds for debit")
	ErrNegativeBalance   = errors.New("wallet balance cannot be negative")
	ErrCurrencyMismatch  = errors.New("currency mismatch with wallet currency")
	ErrInvalidAmount     = errors.New("operation amount must be strictly positive")
)

// Wallet is the aggregate root for player financial balances.
type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	currency  string
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// New creates a new Wallet with initial version 1.
func New(id, playerID uuid.UUID, initialBalance money.Money) (*Wallet, error) {
	if id == uuid.Nil {
		return nil, ErrInvalidWalletID
	}
	if playerID == uuid.Nil {
		return nil, ErrInvalidPlayerID
	}
	if initialBalance.IsNegative() {
		return nil, ErrNegativeBalance
	}

	now := time.Now().UTC()
	return &Wallet{
		id:        id,
		playerID:  playerID,
		currency:  initialBalance.Currency(),
		balance:   initialBalance,
		version:   1,
		createdAt: now,
		updatedAt: now,
	}, nil
}

// Rehydrate reconstructs a Wallet from persistence without triggering version increments or domain events.
func Rehydrate(id, playerID uuid.UUID, currency string, balance money.Money, version int64, createdAt, updatedAt time.Time) (*Wallet, error) {
	if id == uuid.Nil {
		return nil, ErrInvalidWalletID
	}
	if playerID == uuid.Nil {
		return nil, ErrInvalidPlayerID
	}
	if balance.Currency() != currency {
		return nil, ErrCurrencyMismatch
	}
	if balance.IsNegative() {
		return nil, ErrNegativeBalance
	}

	return &Wallet{
		id:        id,
		playerID:  playerID,
		currency:  currency,
		balance:   balance,
		version:   version,
		createdAt: createdAt,
		updatedAt: updatedAt,
	}, nil
}

// Getters
func (w *Wallet) ID() uuid.UUID        { return w.id }
func (w *Wallet) PlayerID() uuid.UUID  { return w.playerID }
func (w *Wallet) Currency() string     { return w.currency }
func (w *Wallet) Balance() money.Money { return w.balance }
func (w *Wallet) Version() int64       { return w.version }
func (w *Wallet) CreatedAt() time.Time { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time { return w.updatedAt }

// Debit decreases the wallet balance by amount.
// Preserves balance >= 0, currency match, strictly positive amount, and increments version.
func (w *Wallet) Debit(amount money.Money) (balanceBefore, balanceAfter money.Money, err error) {
	if !amount.IsPositive() {
		return money.Money{}, money.Money{}, ErrInvalidAmount
	}
	if amount.Currency() != w.currency {
		return money.Money{}, money.Money{}, fmt.Errorf("%w: wallet is %s, amount is %s", ErrCurrencyMismatch, w.currency, amount.Currency())
	}

	cmp, err := w.balance.Compare(amount)
	if err != nil {
		return money.Money{}, money.Money{}, err
	}
	if cmp < 0 {
		return money.Money{}, money.Money{}, ErrInsufficientFunds
	}

	newBalance, err := w.balance.Sub(amount)
	if err != nil {
		return money.Money{}, money.Money{}, err
	}
	if newBalance.IsNegative() {
		return money.Money{}, money.Money{}, ErrNegativeBalance
	}

	balanceBefore = w.balance
	balanceAfter = newBalance

	w.balance = newBalance
	w.version++
	w.updatedAt = time.Now().UTC()

	return balanceBefore, balanceAfter, nil
}

// Credit increases the wallet balance by amount.
// Enforces currency match, strictly positive amount, and increments version.
func (w *Wallet) Credit(amount money.Money) (balanceBefore, balanceAfter money.Money, err error) {
	if !amount.IsPositive() {
		return money.Money{}, money.Money{}, ErrInvalidAmount
	}
	if amount.Currency() != w.currency {
		return money.Money{}, money.Money{}, fmt.Errorf("%w: wallet is %s, amount is %s", ErrCurrencyMismatch, w.currency, amount.Currency())
	}

	newBalance, err := w.balance.Add(amount)
	if err != nil {
		return money.Money{}, money.Money{}, err
	}

	balanceBefore = w.balance
	balanceAfter = newBalance

	w.balance = newBalance
	w.version++
	w.updatedAt = time.Now().UTC()

	return balanceBefore, balanceAfter, nil
}
