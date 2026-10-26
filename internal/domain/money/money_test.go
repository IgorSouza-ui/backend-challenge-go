package money_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func TestMoney_CreationAndFormatting(t *testing.T) {
	tests := []struct {
		amountStr string
		currency  string
		wantCents int64
		wantStr   string
		wantErr   bool
	}{
		{"25.00", "BRL", 2500, "25.00", false},
		{"0.00", "BRL", 0, "0.00", false},
		{"0", "BRL", 0, "0.00", false},
		{"0.5", "BRL", 50, "0.50", false},
		{"1000.99", "BRL", 100099, "1000.99", false},
		{"-25.00", "BRL", -2500, "-25.00", false},
		{"", "BRL", 0, "", true},
		{"25.000", "BRL", 0, "", true}, // 3 decimals forbidden
		{"NaN", "BRL", 0, "", true},
		{"Infinity", "BRL", 0, "", true},
		{"1e6", "BRL", 0, "", true},
		{"25.00", "brl", 0, "", true},  // lowercase invalid
		{"25.00", "USDD", 0, "", true}, // 4 letters invalid
		{"25.00", "BR", 0, "", true},   // 2 letters invalid
	}

	for _, tt := range tests {
		m, err := money.New(tt.amountStr, tt.currency)
		if (err != nil) != tt.wantErr {
			t.Errorf("New(%q, %q) error = %v, wantErr %v", tt.amountStr, tt.currency, err, tt.wantErr)
			continue
		}
		if !tt.wantErr {
			if m.Cents() != tt.wantCents {
				t.Errorf("New(%q, %q) cents = %d, want %d", tt.amountStr, tt.currency, m.Cents(), tt.wantCents)
			}
			if m.AmountString() != tt.wantStr {
				t.Errorf("New(%q, %q) string = %q, want %q", tt.amountStr, tt.currency, m.AmountString(), tt.wantStr)
			}
		}
	}
}

func TestMoney_Arithmetic(t *testing.T) {
	m1 := money.MustNew("25.00", "BRL")
	m2 := money.MustNew("10.50", "BRL")
	mUSD := money.MustNew("10.50", "USD")

	// Add
	sum, err := m1.Add(m2)
	if err != nil || sum.AmountString() != "35.50" {
		t.Fatalf("Add failed: got %v, err %v", sum.AmountString(), err)
	}

	// Currency Mismatch on Add
	_, err = m1.Add(mUSD)
	if err == nil {
		t.Fatalf("expected error on currency mismatch")
	}

	// Sub
	diff, err := m1.Sub(m2)
	if err != nil || diff.AmountString() != "14.50" {
		t.Fatalf("Sub failed: got %v, err %v", diff.AmountString(), err)
	}

	// Compare
	cmp, err := m1.Compare(m2)
	if err != nil || cmp != 1 {
		t.Fatalf("Compare failed: got %d", cmp)
	}

	cmp2, err := m2.Compare(m1)
	if err != nil || cmp2 != -1 {
		t.Fatalf("Compare failed: got %d", cmp2)
	}

	cmpEqual, err := m1.Compare(m1)
	if err != nil || cmpEqual != 0 {
		t.Fatalf("Compare equal failed: got %d", cmpEqual)
	}
}

func TestMoney_Overflow(t *testing.T) {
	maxM, _ := money.FromCents(math.MaxInt64, "BRL")
	one, _ := money.FromCents(1, "BRL")

	_, err := maxM.Add(one)
	if err == nil {
		t.Fatalf("expected overflow on max + 1")
	}

	minM, _ := money.FromCents(math.MinInt64, "BRL")
	_, err = minM.Sub(one)
	if err == nil {
		t.Fatalf("expected overflow on min - 1")
	}
}

func TestMoney_JSON(t *testing.T) {
	m := money.MustNew("25.00", "BRL")
	bytes, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	expected := `{"amount":"25.00","currency":"BRL"}`
	if string(bytes) != expected {
		t.Fatalf("got %s, want %s", string(bytes), expected)
	}

	var parsed money.Money
	if err := json.Unmarshal([]byte(expected), &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if !parsed.Equals(m) {
		t.Fatalf("unmarshaled not equal to original")
	}
}
