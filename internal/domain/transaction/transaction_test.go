package transaction_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/transaction"
)

func TestTransaction_CreationAndTransitions(t *testing.T) {
	wid := uuid.New()
	pid := uuid.New()
	tid := uuid.New()
	amt := money.MustNew("25.00", "BRL")

	// BET creation
	tx, err := transaction.NewExternal(tid, "provider-a", "ext-1", "key-1", "hash-1", wid, pid, "r1", "g1", transaction.KindBet, amt, nil)
	if err != nil {
		t.Fatalf("failed to create BET transaction: %v", err)
	}

	if tx.State() != transaction.StatePending {
		t.Fatalf("expected initial state PENDING, got %s", tx.State())
	}

	// Mark Processed
	balanceAfter := money.MustNew("75.00", "BRL")
	err = tx.MarkProcessed(balanceAfter, nil)
	if err != nil {
		t.Fatalf("mark processed failed: %v", err)
	}

	if tx.State() != transaction.StateProcessed {
		t.Fatalf("expected state PROCESSED, got %s", tx.State())
	}

	// Cannot transition from terminal state
	err = tx.MarkRejected("TEST_FAIL")
	if err == nil {
		t.Fatalf("expected error transitioning from terminal state PROCESSED")
	}
}

func TestTransaction_LossMustBeZero(t *testing.T) {
	wid := uuid.New()
	pid := uuid.New()
	amtNonZero := money.MustNew("10.00", "BRL")
	zeroAmt, _ := money.Zero("BRL")

	// LOSS with non-zero amount must fail
	_, err := transaction.NewExternal(uuid.New(), "provider-a", "ext-1", "key-1", "hash-1", wid, pid, "r1", "g1", transaction.KindLoss, amtNonZero, nil)
	if err != transaction.ErrLossMustBeZero {
		t.Fatalf("expected ErrLossMustBeZero, got %v", err)
	}

	// LOSS with zero amount must succeed
	lossTx, err := transaction.NewExternal(uuid.New(), "provider-a", "ext-1", "key-1", "hash-1", wid, pid, "r1", "g1", transaction.KindLoss, zeroAmt, nil)
	if err != nil || lossTx == nil {
		t.Fatalf("expected valid LOSS transaction with 0.00 amount, got error: %v", err)
	}
}

func TestTransaction_RefundRequiresReference(t *testing.T) {
	wid := uuid.New()
	pid := uuid.New()
	amt := money.MustNew("25.00", "BRL")

	// REFUND without reference must fail
	_, err := transaction.NewExternal(uuid.New(), "provider-a", "ext-1", "key-1", "hash-1", wid, pid, "r1", "g1", transaction.KindRefund, amt, nil)
	if err != transaction.ErrReferenceRequired {
		t.Fatalf("expected ErrReferenceRequired, got %v", err)
	}

	// REFUND with reference must succeed
	ref := "parent-bet-123"
	refTx, err := transaction.NewExternal(uuid.New(), "provider-a", "ext-1", "key-1", "hash-1", wid, pid, "r1", "g1", transaction.KindRefund, amt, &ref)
	if err != nil || refTx == nil {
		t.Fatalf("expected valid REFUND transaction, got error: %v", err)
	}

	// Can mark PendingReference
	err = refTx.MarkPendingReference(time.Now().Add(5 * time.Second))
	if err != nil || refTx.State() != transaction.StatePendingReference {
		t.Fatalf("failed to mark PENDING_REFERENCE")
	}
}
