package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/events"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
	"github.com/junglegaming/backend-challenge-go/internal/domain/transaction"
	"github.com/junglegaming/backend-challenge-go/internal/infra/auth"
	infrahttp "github.com/junglegaming/backend-challenge-go/internal/infra/http"
	"github.com/junglegaming/backend-challenge-go/internal/usecase"
	"github.com/junglegaming/backend-challenge-go/internal/worker"
	"go.uber.org/fx"
)

// TestMandatory_TwoBetsOf80On100Balance tests the strict requirement from README line 202:
// "uma carteira com 100.00 BRL recebe, ao mesmo tempo, duas apostas distintas de 80.00 BRL.
// O resultado deve conter uma aposta processada, uma rejeição por saldo insuficiente,
// saldo final de 20.00 BRL e um único débito no ledger."
func TestMandatory_TwoBetsOf80On100Balance(t *testing.T) {
	ctx := context.Background()
	txMgr := &MockTxManager{}
	wRepo := NewInMemoryWalletRepo()
	txRepo := NewInMemoryTransactionRepo()
	lRepo := NewInMemoryLedgerRepo()
	oRepo := NewInMemoryOutboxRepo()
	iRepo := NewInMemoryInboxRepo()

	openUC := usecase.NewOpenWalletUseCase(txMgr, wRepo, txRepo, lRepo, oRepo)
	processUC := usecase.NewProcessWagerUseCase(txMgr, wRepo, txRepo, lRepo, oRepo, iRepo)
	reconcileUC := usecase.NewReconcileWalletUseCase(wRepo, lRepo)

	// 1. Open Wallet with 100.00 BRL
	playerID := uuid.New()
	initBal := money.MustNew("100.00", "BRL")
	wOut, err := openUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
		CorrelationID:  "corr-open-1",
	})
	if err != nil {
		t.Fatalf("failed opening wallet: %v", err)
	}

	walletID := wOut.ID

	// 2. Prepare 2 distinct 80.00 BRL bets
	betAmount := money.MustNew("80.00", "BRL")
	input1 := usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "bet-1",
		IdempotencyKey:        "provider-a:bet-1",
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  transaction.KindBet,
		Money:                 betAmount,
		CorrelationID:         "corr-bet-1",
	}

	input2 := usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "bet-2",
		IdempotencyKey:        "provider-a:bet-2",
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  transaction.KindBet,
		Money:                 betAmount,
		CorrelationID:         "corr-bet-2",
	}

	// 3. Execute concurrently
	var wg sync.WaitGroup
	var out1, out2 *usecase.ProcessWagerOutput
	var err1, err2 error

	wg.Add(2)
	go func() {
		defer wg.Done()
		out1, err1 = processUC.Execute(ctx, input1)
	}()
	go func() {
		defer wg.Done()
		out2, err2 = processUC.Execute(ctx, input2)
	}()
	wg.Wait()

	if err1 != nil {
		t.Fatalf("bet1 execution error: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("bet2 execution error: %v", err2)
	}

	// Verify: exactly ONE is PROCESSED, exactly ONE is REJECTED
	processedCount := 0
	rejectedCount := 0

	for _, out := range []*usecase.ProcessWagerOutput{out1, out2} {
		if out.Status == string(transaction.StateProcessed) {
			processedCount++
		}
		if out.Status == string(transaction.StateRejected) {
			rejectedCount++
			if out.FailureCode == nil || *out.FailureCode != transaction.FailInsufficientFunds {
				t.Fatalf("expected failure code INSUFFICIENT_FUNDS, got %v", out.FailureCode)
			}
		}
	}

	if processedCount != 1 || rejectedCount != 1 {
		t.Fatalf("expected 1 processed and 1 rejected, got %d processed and %d rejected", processedCount, rejectedCount)
	}

	// 4. Verify final wallet balance is exactly 20.00 BRL
	w, err := wRepo.GetByID(ctx, walletID)
	if err != nil {
		t.Fatalf("get wallet failed: %v", err)
	}
	if w.Balance().AmountString() != "20.00" {
		t.Fatalf("expected final balance 20.00, got %s", w.Balance().AmountString())
	}

	// 5. Verify reconciliation (stored balance matches ledger calculation)
	recon, err := reconcileUC.Execute(ctx, usecase.ReconcileWalletInput{WalletID: walletID})
	if err != nil {
		t.Fatalf("reconciliation failed: %v", err)
	}
	if !recon.Consistent {
		t.Fatalf("expected reconciliation to be consistent, got difference %s", recon.Difference.AmountString())
	}
	if recon.StoredBalance.AmountString() != "20.00" {
		t.Fatalf("expected stored balance 20.00, got %s", recon.StoredBalance.AmountString())
	}

	// Checked entries = 2 (1 opening credit of 100 + 1 bet debit of 80)
	if recon.CheckedEntries != 2 {
		t.Fatalf("expected exactly 2 ledger entries, got %d", recon.CheckedEntries)
	}
}

// TestIdempotency_50ParallelRequests tests that 50 concurrent requests with the SAME key produce exactly 1 debit
func TestIdempotency_50ParallelRequests(t *testing.T) {
	ctx := context.Background()
	txMgr := &MockTxManager{}
	wRepo := NewInMemoryWalletRepo()
	txRepo := NewInMemoryTransactionRepo()
	lRepo := NewInMemoryLedgerRepo()
	oRepo := NewInMemoryOutboxRepo()
	iRepo := NewInMemoryInboxRepo()

	openUC := usecase.NewOpenWalletUseCase(txMgr, wRepo, txRepo, lRepo, oRepo)
	processUC := usecase.NewProcessWagerUseCase(txMgr, wRepo, txRepo, lRepo, oRepo, iRepo)

	playerID := uuid.New()
	wOut, _ := openUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: money.MustNew("100.00", "BRL"),
		CorrelationID:  "corr-1",
	})

	input := usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "bet-repeat",
		IdempotencyKey:        "provider-a:bet-repeat",
		PlayerID:              playerID,
		WalletID:              wOut.ID,
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  transaction.KindBet,
		Money:                 money.MustNew("10.00", "BRL"),
		CorrelationID:         "corr-repeat",
	}

	const concurrency = 50
	var wg sync.WaitGroup
	results := make([]*usecase.ProcessWagerOutput, concurrency)
	errorsList := make([]error, concurrency)

	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		idx := i
		go func() {
			defer wg.Done()
			results[idx], errorsList[idx] = processUC.Execute(ctx, input)
		}()
	}
	wg.Wait()

	// Verify all returned success with balance 90.00
	for i := 0; i < concurrency; i++ {
		if errorsList[i] != nil {
			t.Fatalf("request %d failed: %v", i, errorsList[i])
		}
		if results[i].Balance.AmountString() != "90.00" {
			t.Fatalf("expected balance 90.00, got %s", results[i].Balance.AmountString())
		}
	}

	// Final balance must be 90.00, meaning only 1 debit occurred
	w, _ := wRepo.GetByID(ctx, wOut.ID)
	if w.Balance().AmountString() != "90.00" {
		t.Fatalf("expected final balance 90.00, got %s (debited multiple times!)", w.Balance().AmountString())
	}
}

// TestParallelDistinctWallets verifies that independent wallets process concurrently without blocking each other
func TestParallelDistinctWallets(t *testing.T) {
	ctx := context.Background()
	txMgr := &MockTxManager{}
	wRepo := NewInMemoryWalletRepo()
	txRepo := NewInMemoryTransactionRepo()
	lRepo := NewInMemoryLedgerRepo()
	oRepo := NewInMemoryOutboxRepo()
	iRepo := NewInMemoryInboxRepo()

	openUC := usecase.NewOpenWalletUseCase(txMgr, wRepo, txRepo, lRepo, oRepo)
	processUC := usecase.NewProcessWagerUseCase(txMgr, wRepo, txRepo, lRepo, oRepo, iRepo)

	const numWallets = 20
	walletIDs := make([]uuid.UUID, numWallets)
	playerIDs := make([]uuid.UUID, numWallets)

	for i := 0; i < numWallets; i++ {
		playerIDs[i] = uuid.New()
		out, err := openUC.Execute(ctx, usecase.OpenWalletInput{
			PlayerID:       playerIDs[i],
			InitialBalance: money.MustNew("50.00", "BRL"),
			CorrelationID:  fmt.Sprintf("open-%d", i),
		})
		if err != nil {
			t.Fatalf("failed to open wallet %d: %v", i, err)
		}
		walletIDs[i] = out.ID
	}

	var wg sync.WaitGroup
	wg.Add(numWallets)
	for i := 0; i < numWallets; i++ {
		idx := i
		go func() {
			defer wg.Done()
			extID := fmt.Sprintf("bet-w-%d", idx)
			out, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
				ProviderID:            "provider-a",
				ExternalTransactionID: extID,
				IdempotencyKey:        "provider-a:" + extID,
				PlayerID:              playerIDs[idx],
				WalletID:              walletIDs[idx],
				RoundID:               "r1",
				GameID:                "slots",
				Kind:                  transaction.KindBet,
				Money:                 money.MustNew("15.00", "BRL"),
				CorrelationID:         extID,
			})
			if err != nil || out.Status != string(transaction.StateProcessed) {
				t.Errorf("wallet %d failed: err=%v status=%v", idx, err, out)
			}
		}()
	}
	wg.Wait()

	for i := 0; i < numWallets; i++ {
		w, _ := wRepo.GetByID(ctx, walletIDs[i])
		if w.Balance().AmountString() != "35.00" {
			t.Fatalf("wallet %d expected 35.00, got %s", i, w.Balance().AmountString())
		}
	}
}

// TestMultiInstanceConcurrency simulates 3 application instances competing on the same wallet
func TestMultiInstanceConcurrency(t *testing.T) {
	ctx := context.Background()
	txMgr := &MockTxManager{}
	wRepo := NewInMemoryWalletRepo()
	txRepo := NewInMemoryTransactionRepo()
	lRepo := NewInMemoryLedgerRepo()
	oRepo := NewInMemoryOutboxRepo()
	iRepo := NewInMemoryInboxRepo()

	openUC := usecase.NewOpenWalletUseCase(txMgr, wRepo, txRepo, lRepo, oRepo)
	reconcileUC := usecase.NewReconcileWalletUseCase(wRepo, lRepo)

	// 3 independent instances sharing the same database repositories
	instances := []*usecase.ProcessWagerUseCase{
		usecase.NewProcessWagerUseCase(txMgr, wRepo, txRepo, lRepo, oRepo, iRepo),
		usecase.NewProcessWagerUseCase(txMgr, wRepo, txRepo, lRepo, oRepo, iRepo),
		usecase.NewProcessWagerUseCase(txMgr, wRepo, txRepo, lRepo, oRepo, iRepo),
	}

	playerID := uuid.New()
	wOut, _ := openUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: money.MustNew("150.00", "BRL"),
		CorrelationID:  "corr-init",
	})

	// 30 concurrent bets of 10.00 BRL across 3 instances -> 15 processed, 15 rejected
	const totalBets = 30
	var wg sync.WaitGroup
	outputs := make([]*usecase.ProcessWagerOutput, totalBets)

	wg.Add(totalBets)
	for i := 0; i < totalBets; i++ {
		idx := i
		inst := instances[idx%len(instances)]
		go func() {
			defer wg.Done()
			extID := fmt.Sprintf("multi-bet-%d", idx)
			out, err := inst.Execute(ctx, usecase.ProcessWagerInput{
				ProviderID:            "provider-a",
				ExternalTransactionID: extID,
				IdempotencyKey:        "provider-a:" + extID,
				PlayerID:              playerID,
				WalletID:              wOut.ID,
				RoundID:               "round-multi",
				GameID:                "crash",
				Kind:                  transaction.KindBet,
				Money:                 money.MustNew("10.00", "BRL"),
				CorrelationID:         extID,
			})
			if err != nil {
				t.Errorf("unexpected error on bet %d: %v", idx, err)
			}
			outputs[idx] = out
		}()
	}
	wg.Wait()

	processed, rejected := 0, 0
	for _, o := range outputs {
		if o.Status == string(transaction.StateProcessed) {
			processed++
		} else if o.Status == string(transaction.StateRejected) {
			rejected++
		}
	}
	if processed != 15 || rejected != 15 {
		t.Fatalf("expected 15 processed and 15 rejected, got %d processed and %d rejected", processed, rejected)
	}

	recon, err := reconcileUC.Execute(ctx, usecase.ReconcileWalletInput{WalletID: wOut.ID})
	if err != nil || !recon.Consistent || recon.StoredBalance.AmountString() != "0.00" {
		t.Fatalf("expected consistent 0.00 balance, got consistent=%v balance=%s", recon.Consistent, recon.StoredBalance.AmountString())
	}
}

// TestOutOfOrder_RefundBeforeBet_And_Expiration verifies PENDING_REFERENCE resolution and TTL expiration
func TestOutOfOrder_RefundBeforeBet_And_Expiration(t *testing.T) {
	ctx := context.Background()
	txMgr := &MockTxManager{}
	wRepo := NewInMemoryWalletRepo()
	txRepo := NewInMemoryTransactionRepo()
	lRepo := NewInMemoryLedgerRepo()
	oRepo := NewInMemoryOutboxRepo()
	iRepo := NewInMemoryInboxRepo()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	openUC := usecase.NewOpenWalletUseCase(txMgr, wRepo, txRepo, lRepo, oRepo)
	processUC := usecase.NewProcessWagerUseCase(txMgr, wRepo, txRepo, lRepo, oRepo, iRepo)
	resolver := worker.NewReferenceResolver(txMgr, wRepo, txRepo, lRepo, oRepo, logger)

	playerID := uuid.New()
	wOut, _ := openUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: money.MustNew("100.00", "BRL"),
		CorrelationID:  "corr-1",
	})

	// 1. REFUND arrives BEFORE the BET "delayed-bet-1"
	delayedBetID := "delayed-bet-1"
	refundOut, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "refund-early-1",
		IdempotencyKey:                 "provider-a:refund-early-1",
		PlayerID:                       playerID,
		WalletID:                       wOut.ID,
		RoundID:                        "round-oo",
		GameID:                         "roulette",
		Kind:                           transaction.KindRefund,
		Money:                          money.MustNew("30.00", "BRL"),
		ReferenceExternalTransactionID: &delayedBetID,
		CorrelationID:                  "corr-ref-early",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if refundOut.Status != string(transaction.StatePendingReference) {
		t.Fatalf("expected PENDING_REFERENCE, got %s", refundOut.Status)
	}

	// 2. Now the delayed BET arrives and is processed (100.00 -> 70.00)
	betOut, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: delayedBetID,
		IdempotencyKey:        "provider-a:" + delayedBetID,
		PlayerID:              playerID,
		WalletID:              wOut.ID,
		RoundID:               "round-oo",
		GameID:                "roulette",
		Kind:                  transaction.KindBet,
		Money:                 money.MustNew("30.00", "BRL"),
		CorrelationID:         "corr-bet-delayed",
	})
	if err != nil || betOut.Status != string(transaction.StateProcessed) {
		t.Fatalf("expected delayed bet PROCESSED, got err=%v status=%v", err, betOut)
	}

	// 3. Run ReferenceResolver -> should resolve the pending REFUND and restore balance to 100.00
	resolver.ResolvePendingOnce(ctx)

	resolvedTx, err := txRepo.GetByID(ctx, refundOut.TransactionID)
	if err != nil || resolvedTx.State() != transaction.StateProcessed {
		t.Fatalf("expected refund to transition to PROCESSED, got %v", resolvedTx.State())
	}
	w, _ := wRepo.GetByID(ctx, wOut.ID)
	if w.Balance().AmountString() != "100.00" {
		t.Fatalf("expected balance restored to 100.00, got %s", w.Balance().AmountString())
	}

	// 4. Test expiration when referenced transaction NEVER arrives
	missingBetID := "never-arrived-bet"
	orphanOut, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "rollback-orphan",
		IdempotencyKey:                 "provider-a:rollback-orphan",
		PlayerID:                       playerID,
		WalletID:                       wOut.ID,
		RoundID:                        "round-orphan",
		GameID:                         "roulette",
		Kind:                           transaction.KindRollback,
		Money:                          money.MustNew("20.00", "BRL"),
		ReferenceExternalTransactionID: &missingBetID,
		CorrelationID:                  "corr-orphan",
	})
	if err != nil || orphanOut.Status != string(transaction.StatePendingReference) {
		t.Fatalf("expected PENDING_REFERENCE, got %v", orphanOut)
	}

	// Run 5 retry cycles until maxRetries (5) expires it to REJECTED (REFERENCE_NOT_FOUND)
	for i := 0; i < 5; i++ {
		resolver.ResolvePendingOnce(ctx)
	}

	expiredTx, _ := txRepo.GetByID(ctx, orphanOut.TransactionID)
	if expiredTx.State() != transaction.StateRejected {
		t.Fatalf("expected expired pending reference to be REJECTED, got %s", expiredTx.State())
	}
	if expiredTx.FailureCode() == nil || *expiredTx.FailureCode() != transaction.FailReferenceNotFound {
		t.Fatalf("expected failure code REFERENCE_NOT_FOUND, got %v", expiredTx.FailureCode())
	}
}

// TestReversalCombinations_DuplicateAndExceedsBalance tests REFUND/ROLLBACK combinations and LOSS
func TestReversalCombinations_DuplicateAndExceedsBalance(t *testing.T) {
	ctx := context.Background()
	txMgr := &MockTxManager{}
	wRepo := NewInMemoryWalletRepo()
	txRepo := NewInMemoryTransactionRepo()
	lRepo := NewInMemoryLedgerRepo()
	oRepo := NewInMemoryOutboxRepo()
	iRepo := NewInMemoryInboxRepo()

	openUC := usecase.NewOpenWalletUseCase(txMgr, wRepo, txRepo, lRepo, oRepo)
	processUC := usecase.NewProcessWagerUseCase(txMgr, wRepo, txRepo, lRepo, oRepo, iRepo)

	playerID := uuid.New()
	wOut, _ := openUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: money.MustNew("50.00", "BRL"),
		CorrelationID:  "corr-open",
	})

	// 1. BET 20.00 -> balance 30.00
	betExtID := "bet-100"
	_, _ = processUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: betExtID,
		IdempotencyKey:        "k-bet-100",
		PlayerID:              playerID,
		WalletID:              wOut.ID,
		RoundID:               "r-100",
		GameID:                "slots",
		Kind:                  transaction.KindBet,
		Money:                 money.MustNew("20.00", "BRL"),
	})

	// 2. LOSS 0.00 -> balance stays 30.00, no ledger entry created
	lossOut, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "loss-100",
		IdempotencyKey:        "k-loss-100",
		PlayerID:              playerID,
		WalletID:              wOut.ID,
		RoundID:               "r-100",
		GameID:                "slots",
		Kind:                  transaction.KindLoss,
		Money:                 money.MustNew("0.00", "BRL"),
	})
	if err != nil || lossOut.Status != string(transaction.StateProcessed) {
		t.Fatalf("expected LOSS processed, got %v %v", err, lossOut)
	}

	// 3. First REFUND on bet-100 -> PROCESSED, balance 50.00
	ref1, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "ref-100-a",
		IdempotencyKey:                 "k-ref-100-a",
		PlayerID:                       playerID,
		WalletID:                       wOut.ID,
		RoundID:                        "r-100",
		GameID:                         "slots",
		Kind:                           transaction.KindRefund,
		Money:                          money.MustNew("20.00", "BRL"),
		ReferenceExternalTransactionID: &betExtID,
	})
	if err != nil || ref1.Status != string(transaction.StateProcessed) {
		t.Fatalf("expected first refund PROCESSED, got %v %v", err, ref1)
	}

	// 4. Second reversal (ROLLBACK) on the SAME bet-100 -> REJECTED with DUPLICATE_REVERSAL
	rbDup, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "rb-100-dup",
		IdempotencyKey:                 "k-rb-100-dup",
		PlayerID:                       playerID,
		WalletID:                       wOut.ID,
		RoundID:                        "r-100",
		GameID:                         "slots",
		Kind:                           transaction.KindRollback,
		Money:                          money.MustNew("20.00", "BRL"),
		ReferenceExternalTransactionID: &betExtID,
	})
	if err != nil || rbDup.Status != string(transaction.StateRejected) || *rbDup.FailureCode != transaction.FailDuplicateReversal {
		t.Fatalf("expected DUPLICATE_REVERSAL, got %v %v", err, rbDup)
	}

	// 5. WIN 40.00 -> balance 90.00, then spend 70.00 -> balance 20.00, then ROLLBACK WIN 40.00 -> REVERSAL_EXCEEDS_BALANCE
	winExtID := "win-200"
	_, _ = processUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: winExtID,
		IdempotencyKey:        "k-win-200",
		PlayerID:              playerID,
		WalletID:              wOut.ID,
		RoundID:               "r-200",
		GameID:                "slots",
		Kind:                  transaction.KindWin,
		Money:                 money.MustNew("40.00", "BRL"),
	})
	_, _ = processUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "bet-spend-70",
		IdempotencyKey:        "k-bet-spend-70",
		PlayerID:              playerID,
		WalletID:              wOut.ID,
		RoundID:               "r-201",
		GameID:                "slots",
		Kind:                  transaction.KindBet,
		Money:                 money.MustNew("70.00", "BRL"),
	})

	rbWin, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "rb-win-200",
		IdempotencyKey:                 "k-rb-win-200",
		PlayerID:                       playerID,
		WalletID:                       wOut.ID,
		RoundID:                        "r-200",
		GameID:                         "slots",
		Kind:                           transaction.KindRollback,
		Money:                          money.MustNew("40.00", "BRL"),
		ReferenceExternalTransactionID: &winExtID,
	})
	if err != nil || rbWin.Status != string(transaction.StateRejected) || *rbWin.FailureCode != transaction.FailReversalExceedsBalance {
		t.Fatalf("expected REVERSAL_EXCEEDS_BALANCE, got %v %v", err, rbWin)
	}
}

// TestCrossChannelIdempotency_HTTPAndSQS verifies deduplication across HTTP and SQS Inbox
func TestCrossChannelIdempotency_HTTPAndSQS(t *testing.T) {
	ctx := context.Background()
	txMgr := &MockTxManager{}
	wRepo := NewInMemoryWalletRepo()
	txRepo := NewInMemoryTransactionRepo()
	lRepo := NewInMemoryLedgerRepo()
	oRepo := NewInMemoryOutboxRepo()
	iRepo := NewInMemoryInboxRepo()

	openUC := usecase.NewOpenWalletUseCase(txMgr, wRepo, txRepo, lRepo, oRepo)
	processUC := usecase.NewProcessWagerUseCase(txMgr, wRepo, txRepo, lRepo, oRepo, iRepo)

	playerID := uuid.New()
	wOut, _ := openUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: money.MustNew("100.00", "BRL"),
	})

	// 1. Process via HTTP
	httpOut, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "tx-cross-1",
		IdempotencyKey:        "provider-a:tx-cross-1",
		PlayerID:              playerID,
		WalletID:              wOut.ID,
		RoundID:               "r1",
		GameID:                "slots",
		Kind:                  transaction.KindBet,
		Money:                 money.MustNew("25.00", "BRL"),
		IsSQS:                 false,
	})
	if err != nil || httpOut.IdempotentReplay {
		t.Fatalf("expected fresh HTTP execution, got err=%v replay=%v", err, httpOut)
	}

	// 2. Same transaction arrives via SQS -> idempotent replay, balance remains 75.00
	sqsOut, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "tx-cross-1",
		IdempotencyKey:        "provider-a:tx-cross-1",
		PlayerID:              playerID,
		WalletID:              wOut.ID,
		RoundID:               "r1",
		GameID:                "slots",
		Kind:                  transaction.KindBet,
		Money:                 money.MustNew("25.00", "BRL"),
		IsSQS:                 true,
		MessageID:             "sqs-msg-1",
	})
	if err != nil || !sqsOut.IdempotentReplay || sqsOut.Balance.AmountString() != "75.00" {
		t.Fatalf("expected SQS idempotent replay with 75.00, got err=%v out=%+v", err, sqsOut)
	}
}

type mockEventPublisher struct {
	mu        sync.Mutex
	published map[uuid.UUID]int
}

func (m *mockEventPublisher) Publish(ctx context.Context, env *events.EventEnvelope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published[env.EventID]++
	return nil
}

// TestConcurrentOutboxPublishers verifies that multiple OutboxPublisher workers do not double-publish
func TestConcurrentOutboxPublishers(t *testing.T) {
	ctx := context.Background()
	txMgr := &MockTxManager{}
	wRepo := NewInMemoryWalletRepo()
	txRepo := NewInMemoryTransactionRepo()
	lRepo := NewInMemoryLedgerRepo()
	oRepo := NewInMemoryOutboxRepo()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	openUC := usecase.NewOpenWalletUseCase(txMgr, wRepo, txRepo, lRepo, oRepo)
	for i := 0; i < 15; i++ {
		_, _ = openUC.Execute(ctx, usecase.OpenWalletInput{
			PlayerID:       uuid.New(),
			InitialBalance: money.MustNew("10.00", "BRL"),
		})
	}

	pub := &mockEventPublisher{published: make(map[uuid.UUID]int)}
	w1 := worker.NewOutboxPublisherWithInterface(txMgr, oRepo, pub, logger)
	w2 := worker.NewOutboxPublisherWithInterface(txMgr, oRepo, pub, logger)
	w3 := worker.NewOutboxPublisherWithInterface(txMgr, oRepo, pub, logger)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); w1.ProcessBatchOnce(ctx); w1.ProcessBatchOnce(ctx) }()
	go func() { defer wg.Done(); w2.ProcessBatchOnce(ctx); w2.ProcessBatchOnce(ctx) }()
	go func() { defer wg.Done(); w3.ProcessBatchOnce(ctx); w3.ProcessBatchOnce(ctx) }()
	wg.Wait()

	// 15 wallets * 2 events each = 30 events, each published exactly once
	if len(pub.published) != 30 {
		t.Fatalf("expected 30 published events, got %d", len(pub.published))
	}
	for id, count := range pub.published {
		if count != 1 {
			t.Fatalf("event %s published %d times (expected 1)", id, count)
		}
	}
}

func signTestJWT(secret string, providerID string, roles []string, exp time.Time) string {
	claims := auth.CustomClaims{
		ProviderID: providerID,
		Roles:      roles,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, _ := t.SignedString([]byte(secret))
	return s
}

// TestHTTPAndAuth_ProviderIsolation_And_Conflicts verifies OAuth2/JWT RBAC, provider isolation, and 409 conflicts
func TestHTTPAndAuth_ProviderIsolation_And_Conflicts(t *testing.T) {
	txMgr := &MockTxManager{}
	wRepo := NewInMemoryWalletRepo()
	txRepo := NewInMemoryTransactionRepo()
	lRepo := NewInMemoryLedgerRepo()
	oRepo := NewInMemoryOutboxRepo()
	iRepo := NewInMemoryInboxRepo()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	handlers := infrahttp.NewHandlers(
		usecase.NewOpenWalletUseCase(txMgr, wRepo, txRepo, lRepo, oRepo),
		usecase.NewGetWalletUseCase(wRepo),
		usecase.NewGetLedgerUseCase(lRepo),
		usecase.NewProcessWagerUseCase(txMgr, wRepo, txRepo, lRepo, oRepo, iRepo),
		usecase.NewGetTransactionUseCase(txRepo),
		usecase.NewReconcileWalletUseCase(wRepo, lRepo),
		logger,
	)

	secret := "test-secret"
	jwtVal := auth.NewJWTValidator(secret, "")
	router := infrahttp.NewRouter(handlers, jwtVal, logger)
	ts := httptest.NewServer(router)
	defer ts.Close()

	internalToken := signTestJWT(secret, "internal-service", []string{"internal"}, time.Now().Add(time.Hour))
	providerAToken := signTestJWT(secret, "provider-a", []string{"provider"}, time.Now().Add(time.Hour))
	providerBToken := signTestJWT(secret, "provider-b", []string{"provider"}, time.Now().Add(time.Hour))
	expiredToken := signTestJWT(secret, "provider-a", []string{"provider"}, time.Now().Add(-time.Hour))

	// 1. Missing token -> 401
	req, _ := http.NewRequest("POST", ts.URL+"/wallets", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing token, got %d", resp.StatusCode)
	}

	// 2. Expired token -> 401
	req, _ = http.NewRequest("POST", ts.URL+"/wagering/transactions", nil)
	req.Header.Set("Authorization", "Bearer "+expiredToken)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired token, got %d", resp.StatusCode)
	}

	// 3. Provider A attempting to open a wallet (internal-only endpoint) -> 403
	playerID := uuid.New()
	openBody, _ := json.Marshal(map[string]any{
		"playerId":       playerID.String(),
		"initialBalance": map[string]string{"amount": "100.00", "currency": "BRL"},
	})
	req, _ = http.NewRequest("POST", ts.URL+"/wallets", bytes.NewReader(openBody))
	req.Header.Set("Authorization", "Bearer "+providerAToken)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 when provider calls POST /wallets, got %d", resp.StatusCode)
	}

	// 4. Internal service opens wallet -> 201 Created
	req, _ = http.NewRequest("POST", ts.URL+"/wallets", bytes.NewReader(openBody))
	req.Header.Set("Authorization", "Bearer "+internalToken)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 when internal service calls POST /wallets, got %d", resp.StatusCode)
	}
	var walletResp usecase.OpenWalletOutput
	_ = json.NewDecoder(resp.Body).Decode(&walletResp)

	// 5. Provider A attempting to submit transaction with providerId="provider-b" -> 403 Forbidden
	txBodyMismatch, _ := json.Marshal(map[string]any{
		"providerId":            "provider-b",
		"externalTransactionId": "ext-1",
		"playerId":              playerID.String(),
		"walletId":              walletResp.ID.String(),
		"roundId":               "r1",
		"gameId":                "g1",
		"kind":                  "BET",
		"money":                 map[string]string{"amount": "10.00", "currency": "BRL"},
	})
	req, _ = http.NewRequest("POST", ts.URL+"/wagering/transactions", bytes.NewReader(txBodyMismatch))
	req.Header.Set("Authorization", "Bearer "+providerAToken)
	req.Header.Set("Idempotency-Key", "key-mismatch")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for provider mismatch, got %d", resp.StatusCode)
	}

	// 6. Provider A submits valid BET -> 200 OK
	txBodyValid, _ := json.Marshal(map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": "ext-a-1",
		"playerId":              playerID.String(),
		"walletId":              walletResp.ID.String(),
		"roundId":               "r1",
		"gameId":                "g1",
		"kind":                  "BET",
		"money":                 map[string]string{"amount": "10.00", "currency": "BRL"},
	})
	req, _ = http.NewRequest("POST", ts.URL+"/wagering/transactions", bytes.NewReader(txBodyValid))
	req.Header.Set("Authorization", "Bearer "+providerAToken)
	req.Header.Set("Idempotency-Key", "key-a-1")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var txOut usecase.ProcessWagerOutput
	_ = json.NewDecoder(resp.Body).Decode(&txOut)

	// 7. Provider B attempting to read Provider A's transaction -> 403 Forbidden
	req, _ = http.NewRequest("GET", ts.URL+"/wagering/transactions/"+txOut.TransactionID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+providerBToken)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 when Provider B reads Provider A transaction, got %d", resp.StatusCode)
	}

	// 8. Same Idempotency-Key with DIFFERENT payload (amount 20.00 instead of 10.00) -> 409 Conflict
	txBodyConflict, _ := json.Marshal(map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": "ext-a-1",
		"playerId":              playerID.String(),
		"walletId":              walletResp.ID.String(),
		"roundId":               "r1",
		"gameId":                "g1",
		"kind":                  "BET",
		"money":                 map[string]string{"amount": "20.00", "currency": "BRL"},
	})
	req, _ = http.NewRequest("POST", ts.URL+"/wagering/transactions", bytes.NewReader(txBodyConflict))
	req.Header.Set("Authorization", "Bearer "+providerAToken)
	req.Header.Set("Idempotency-Key", "key-a-1")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict on payload divergence, got %d", resp.StatusCode)
	}
}

// TestUberFx_LifecycleAndGracefulShutdown verifies Uber Fx modular composition and clean shutdown
func TestUberFx_LifecycleAndGracefulShutdown(t *testing.T) {
	app := fx.New(
		fx.NopLogger,
		fx.Provide(
			func() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) },
			func() repository.DBTransactionManager { return &MockTxManager{} },
			func() repository.WalletRepository { return NewInMemoryWalletRepo() },
			func() repository.TransactionRepository { return NewInMemoryTransactionRepo() },
			func() repository.LedgerRepository { return NewInMemoryLedgerRepo() },
			func() repository.OutboxRepository { return NewInMemoryOutboxRepo() },
			func() repository.InboxRepository { return NewInMemoryInboxRepo() },
			usecase.NewOpenWalletUseCase,
			usecase.NewGetWalletUseCase,
			usecase.NewGetLedgerUseCase,
			usecase.NewProcessWagerUseCase,
			usecase.NewGetTransactionUseCase,
			usecase.NewReconcileWalletUseCase,
			worker.NewReferenceResolver,
		),
		fx.Invoke(func(lc fx.Lifecycle, resolver *worker.ReferenceResolver) {
			lc.Append(fx.Hook{
				OnStart: func(ctx context.Context) error {
					resolver.Start(context.Background())
					return nil
				},
				OnStop: func(ctx context.Context) error {
					return resolver.Stop(ctx)
				},
			})
		}),
	)

	startCtx, cancelStart := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStart()
	if err := app.Start(startCtx); err != nil {
		t.Fatalf("fx app failed to start: %v", err)
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStop()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("fx app failed to stop cleanly: %v", err)
	}
}
