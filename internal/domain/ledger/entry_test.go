package ledger_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func TestLedger_Validation(t *testing.T) {
	wid := uuid.New()
	tid := uuid.New()
	amt := money.MustNew("25.00", "BRL")
	before := money.MustNew("100.00", "BRL")
	afterDebit := money.MustNew("75.00", "BRL")
	afterCredit := money.MustNew("125.00", "BRL")
	wrongAfter := money.MustNew("80.00", "BRL")

	// Valid Debit
	eDebit, err := ledger.New(uuid.New(), wid, tid, ledger.Debit, amt, before, afterDebit, time.Now())
	if err != nil || eDebit == nil {
		t.Fatalf("expected valid debit entry, got error: %v", err)
	}

	// Valid Credit
	eCredit, err := ledger.New(uuid.New(), wid, tid, ledger.Credit, amt, before, afterCredit, time.Now())
	if err != nil || eCredit == nil {
		t.Fatalf("expected valid credit entry, got error: %v", err)
	}

	// Inconsistent math (debit 25 from 100 should be 75, not 80)
	_, err = ledger.New(uuid.New(), wid, tid, ledger.Debit, amt, before, wrongAfter, time.Now())
	if err == nil {
		t.Fatalf("expected math inconsistency error")
	}

	// Zero or negative amount rejected
	zeroAmt, _ := money.Zero("BRL")
	_, err = ledger.New(uuid.New(), wid, tid, ledger.Debit, zeroAmt, before, before, time.Now())
	if err == nil {
		t.Fatalf("expected error for zero amount in ledger")
	}
}
