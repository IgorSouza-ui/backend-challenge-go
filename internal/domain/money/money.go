package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

var (
	ErrInvalidCurrency      = errors.New("currency must be a 3-letter uppercase ISO 4217 code")
	ErrInvalidAmountFormat  = errors.New("amount must be a valid decimal string with up to 2 decimal places")
	ErrCurrencyMismatch     = errors.New("cannot operate on different currencies")
	ErrAmountOverflow       = errors.New("money arithmetic overflow")
	ErrNegativeAmountNotAll = errors.New("amount cannot be negative")
)

// Money is an immutable value object representing a monetary amount in a specific currency.
// Stored internally as an int64 of minimum units (cents) with a fixed scale of 2.
// E.g.: 25.00 BRL = 2500 cents.
type Money struct {
	cents    int64
	currency string
}

// New creates a Money instance from a decimal string and an ISO 4217 currency.
// It strictly validates:
// - Currency is 3 uppercase ASCII letters.
// - Amount format has at most 2 decimal places.
// - Rejects scientific notation, NaN, Infinity, whitespace.
func New(amountStr, currency string) (Money, error) {
	if err := validateCurrency(currency); err != nil {
		return Money{}, err
	}

	cents, err := parseAmountToCents(amountStr)
	if err != nil {
		return Money{}, err
	}

	return Money{
		cents:    cents,
		currency: currency,
	}, nil
}

// FromCents creates a Money instance directly from cents and currency.
func FromCents(cents int64, currency string) (Money, error) {
	if err := validateCurrency(currency); err != nil {
		return Money{}, err
	}
	return Money{
		cents:    cents,
		currency: currency,
	}, nil
}

// MustNew panics on error, useful for tests and constants.
func MustNew(amountStr, currency string) Money {
	m, err := New(amountStr, currency)
	if err != nil {
		panic(err)
	}
	return m
}

// Zero creates a zero-valued Money instance for the given currency.
func Zero(currency string) (Money, error) {
	if err := validateCurrency(currency); err != nil {
		return Money{}, err
	}
	return Money{cents: 0, currency: currency}, nil
}

// Cents returns the internal int64 representation.
func (m Money) Cents() int64 {
	return m.cents
}

// Currency returns the 3-letter currency code.
func (m Money) Currency() string {
	return m.currency
}

// AmountString formats the money to "X.YY" string.
func (m Money) AmountString() string {
	isNegative := m.cents < 0
	absCents := m.cents
	if isNegative {
		absCents = -absCents
	}

	units := absCents / 100
	decimals := absCents % 100

	if isNegative {
		return fmt.Sprintf("-%d.%02d", units, decimals)
	}
	return fmt.Sprintf("%d.%02d", units, decimals)
}

// IsZero returns true if cents == 0.
func (m Money) IsZero() bool {
	return m.cents == 0
}

// IsPositive returns true if cents > 0.
func (m Money) IsPositive() bool {
	return m.cents > 0
}

// IsNegative returns true if cents < 0.
func (m Money) IsNegative() bool {
	return m.cents < 0
}

// Add returns m + other, checking currency match and overflow.
func (m Money) Add(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, other.currency)
	}

	// Overflow check
	if (other.cents > 0 && m.cents > math.MaxInt64-other.cents) ||
		(other.cents < 0 && m.cents < math.MinInt64-other.cents) {
		return Money{}, ErrAmountOverflow
	}

	return Money{
		cents:    m.cents + other.cents,
		currency: m.currency,
	}, nil
}

// Sub returns m - other, checking currency match and overflow.
func (m Money) Sub(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, other.currency)
	}

	// Overflow check
	if (other.cents < 0 && m.cents > math.MaxInt64+other.cents) ||
		(other.cents > 0 && m.cents < math.MinInt64+other.cents) {
		return Money{}, ErrAmountOverflow
	}

	return Money{
		cents:    m.cents - other.cents,
		currency: m.currency,
	}, nil
}

// Negate returns -m, checking overflow.
func (m Money) Negate() (Money, error) {
	if m.cents == math.MinInt64 {
		return Money{}, ErrAmountOverflow
	}
	return Money{
		cents:    -m.cents,
		currency: m.currency,
	}, nil
}

// Compare returns -1 if m < other, 0 if m == other, 1 if m > other.
func (m Money) Compare(other Money) (int, error) {
	if m.currency != other.currency {
		return 0, fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	if m.cents < other.cents {
		return -1, nil
	}
	if m.cents > other.cents {
		return 1, nil
	}
	return 0, nil
}

// Equals checks if both amount and currency match.
func (m Money) Equals(other Money) bool {
	return m.currency == other.currency && m.cents == other.cents
}

// JSON serialization: {"amount":"25.00","currency":"BRL"}
type moneyJSON struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(moneyJSON{
		Amount:   m.AmountString(),
		Currency: m.currency,
	})
}

func (m *Money) UnmarshalJSON(data []byte) error {
	var mj moneyJSON
	if err := json.Unmarshal(data, &mj); err != nil {
		return err
	}
	parsed, err := New(mj.Amount, mj.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// Helper validations

func validateCurrency(c string) error {
	if len(c) != 3 {
		return ErrInvalidCurrency
	}
	for i := 0; i < 3; i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return ErrInvalidCurrency
		}
	}
	return nil
}

func parseAmountToCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ErrInvalidAmountFormat
	}

	// Reject scientific notation or non-numeric tokens
	if strings.ContainsAny(s, "eE+ \t\r\n") {
		return 0, ErrInvalidAmountFormat
	}

	isNegative := false
	if strings.HasPrefix(s, "-") {
		isNegative = true
		s = s[1:]
	}

	parts := strings.Split(s, ".")
	if len(parts) > 2 {
		return 0, ErrInvalidAmountFormat
	}

	integralPart := parts[0]
	if integralPart == "" {
		integralPart = "0"
	}

	// Validate integral digits
	for _, ch := range integralPart {
		if ch < '0' || ch > '9' {
			return 0, ErrInvalidAmountFormat
		}
	}

	units, err := strconv.ParseInt(integralPart, 10, 64)
	if err != nil {
		return 0, ErrAmountOverflow
	}

	var decimalCents int64 = 0
	if len(parts) == 2 {
		decStr := parts[1]
		if len(decStr) > 2 {
			return 0, fmt.Errorf("%w: max 2 decimal places allowed, got %d", ErrInvalidAmountFormat, len(decStr))
		}
		for _, ch := range decStr {
			if ch < '0' || ch > '9' {
				return 0, ErrInvalidAmountFormat
			}
		}
		if len(decStr) == 1 {
			decStr += "0"
		}
		decVal, err := strconv.ParseInt(decStr, 10, 64)
		if err != nil {
			return 0, err
		}
		decimalCents = decVal
	}

	// Check multiplication overflow
	if units > (math.MaxInt64-decimalCents)/100 {
		return 0, ErrAmountOverflow
	}

	totalCents := units*100 + decimalCents
	if isNegative {
		totalCents = -totalCents
	}

	return totalCents, nil
}
