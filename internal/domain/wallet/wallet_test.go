package wallet_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

func TestWallet_DebitAndCredit(t *testing.T) {
	wid := uuid.New()
	pid := uuid.New()
	initial := money.MustNew("100.00", "BRL")

	w, err := wallet.New(wid, pid, initial)
	if err != nil {
		t.Fatalf("failed to create wallet: %v", err)
	}

	if w.Version() != 1 {
		t.Fatalf("expected initial version 1, got %d", w.Version())
	}

	// Debit 80.00
	debitAmt := money.MustNew("80.00", "BRL")
	before, after, err := w.Debit(debitAmt)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}
	if before.AmountString() != "100.00" || after.AmountString() != "20.00" {
		t.Fatalf("unexpected balances: before=%s, after=%s", before.AmountString(), after.AmountString())
	}
	if w.Version() != 2 {
		t.Fatalf("expected version 2, got %d", w.Version())
	}

	// Try Debit 80.00 again (insufficient funds: balance is 20.00)
	_, _, err = w.Debit(debitAmt)
	if err != wallet.ErrInsufficientFunds {
		t.Fatalf("expected ErrInsufficientFunds, got %v", err)
	}
	if w.Version() != 2 {
		t.Fatalf("version should not increment on failed debit, got %d", w.Version())
	}

	// Credit 30.00 -> balance becomes 50.00
	creditAmt := money.MustNew("30.00", "BRL")
	beforeCredit, afterCredit, err := w.Credit(creditAmt)
	if err != nil {
		t.Fatalf("credit failed: %v", err)
	}
	if beforeCredit.AmountString() != "20.00" || afterCredit.AmountString() != "50.00" {
		t.Fatalf("unexpected balances: before=%s, after=%s", beforeCredit.AmountString(), afterCredit.AmountString())
	}
	if w.Version() != 3 {
		t.Fatalf("expected version 3, got %d", w.Version())
	}
}

func TestWallet_CurrencyMismatch(t *testing.T) {
	w, _ := wallet.New(uuid.New(), uuid.New(), money.MustNew("100.00", "BRL"))
	usd := money.MustNew("10.00", "USD")

	_, _, err := w.Debit(usd)
	if err == nil {
		t.Fatalf("expected error debiting USD on BRL wallet")
	}

	_, _, err = w.Credit(usd)
	if err == nil {
		t.Fatalf("expected error crediting USD on BRL wallet")
	}
}
