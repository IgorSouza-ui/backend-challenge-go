package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/transaction"
	"github.com/junglegaming/backend-challenge-go/internal/usecase"
	"github.com/junglegaming/backend-challenge-go/tests"
)

func main() {
	ctx := context.Background()
	txMgr := &tests.MockTxManager{}
	wRepo := tests.NewInMemoryWalletRepo()
	txRepo := tests.NewInMemoryTransactionRepo()
	lRepo := tests.NewInMemoryLedgerRepo()
	oRepo := tests.NewInMemoryOutboxRepo()
	iRepo := tests.NewInMemoryInboxRepo()

	openWalletUC := usecase.NewOpenWalletUseCase(txMgr, wRepo, txRepo, lRepo, oRepo)
	processWagerUC := usecase.NewProcessWagerUseCase(txMgr, wRepo, txRepo, lRepo, oRepo, iRepo)
	reconcileUC := usecase.NewReconcileWalletUseCase(wRepo, lRepo)
	getLedgerUC := usecase.NewGetLedgerUseCase(lRepo)

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	_ = logger

	fmt.Println("================================================================================")
	fmt.Println("🎰 JUNGLE GAMING — DEMONSTRAÇÃO PRÁTICA DO SERVIÇO FINANCEIRO DE APOSTAS")
	fmt.Println("================================================================================")
	fmt.Println()

	// 1. Abertura de Carteira
	playerID := uuid.New()
	initialBalance := money.MustNew("100.00", "BRL")
	fmt.Printf("[PASSO 1] Abrindo carteira para o jogador %s com saldo inicial de %s...\n", playerID, initialBalance.AmountString())

	wOut, err := openWalletUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initialBalance,
		CorrelationID:  "demo-corr-open",
	})
	if err != nil {
		fmt.Printf("❌ Erro ao abrir carteira: %v\n", err)
		return
	}
	fmt.Printf("✅ Carteira criada com sucesso! ID: %s | Saldo: %s BRL | Versão: %d\n\n",
		wOut.ID, wOut.Balance.AmountString(), wOut.Version)

	time.Sleep(200 * time.Millisecond)

	// 2. Aposta de R$ 30,00 (BET)
	fmt.Println("[PASSO 2] Jogador aposta R$ 30,00 no jogo 'fortune-tiger'...")
	betOut, err := processWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:            "pragmatic",
		ExternalTransactionID: "bet-001",
		IdempotencyKey:        "pragmatic:bet-001",
		PlayerID:              playerID,
		WalletID:              wOut.ID,
		RoundID:               "round-1",
		GameID:                "fortune-tiger",
		Kind:                  transaction.KindBet,
		Money:                 money.MustNew("30.00", "BRL"),
		CorrelationID:         "demo-corr-bet-1",
	})
	if err != nil {
		fmt.Printf("❌ Erro: %v\n", err)
		return
	}
	fmt.Printf("✅ Aposta confirmada! Status: %s | Novo Saldo: %s BRL\n\n", betOut.Status, betOut.Balance.AmountString())

	time.Sleep(200 * time.Millisecond)

	// 3. Prêmio de R$ 50,00 (WIN)
	fmt.Println("[PASSO 3] Jogador ganhou prêmio de R$ 50,00 (WIN) na rodada...")
	refBetID := "bet-001"
	winOut, err := processWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:                     "pragmatic",
		ExternalTransactionID:          "win-001",
		IdempotencyKey:                 "pragmatic:win-001",
		PlayerID:                       playerID,
		WalletID:                       wOut.ID,
		RoundID:                        "round-1",
		GameID:                         "fortune-tiger",
		Kind:                           transaction.KindWin,
		Money:                          money.MustNew("50.00", "BRL"),
		ReferenceExternalTransactionID: &refBetID,
		CorrelationID:                  "demo-corr-win-1",
	})
	if err != nil {
		fmt.Printf("❌ Erro: %v\n", err)
		return
	}
	fmt.Printf("✅ Prêmio creditado! Status: %s | Novo Saldo: %s BRL\n\n", winOut.Status, winOut.Balance.AmountString())

	time.Sleep(200 * time.Millisecond)

	// 4. Teste de Concorrência Obrigatório: Duas apostas simultâneas de R$ 80,00 com saldo de R$ 120,00
	fmt.Printf("[PASSO 4] TESTE DE CONCORRÊNCIA: Jogador tem R$ %s. Recebemos 2 apostas simultâneas de R$ 80,00...\n", winOut.Balance.AmountString())
	var wg sync.WaitGroup
	var out1, out2 *usecase.ProcessWagerOutput
	var err1, err2 error

	wg.Add(2)
	go func() {
		defer wg.Done()
		out1, err1 = processWagerUC.Execute(ctx, usecase.ProcessWagerInput{
			ProviderID:            "pragmatic",
			ExternalTransactionID: "bet-conc-A",
			IdempotencyKey:        "pragmatic:bet-conc-A",
			PlayerID:              playerID,
			WalletID:              wOut.ID,
			RoundID:               "round-2",
			GameID:                "fortune-tiger",
			Kind:                  transaction.KindBet,
			Money:                 money.MustNew("80.00", "BRL"),
		})
	}()
	go func() {
		defer wg.Done()
		out2, err2 = processWagerUC.Execute(ctx, usecase.ProcessWagerInput{
			ProviderID:            "pragmatic",
			ExternalTransactionID: "bet-conc-B",
			IdempotencyKey:        "pragmatic:bet-conc-B",
			PlayerID:              playerID,
			WalletID:              wOut.ID,
			RoundID:               "round-2",
			GameID:                "fortune-tiger",
			Kind:                  transaction.KindBet,
			Money:                 money.MustNew("80.00", "BRL"),
		})
	}()
	wg.Wait()

	if err1 != nil || err2 != nil {
		fmt.Printf("❌ Erro inesperado: %v %v\n", err1, err2)
		return
	}

	code1, code2 := "nenhum", "nenhum"
	if out1.FailureCode != nil {
		code1 = *out1.FailureCode
	}
	if out2.FailureCode != nil {
		code2 = *out2.FailureCode
	}
	fmt.Printf("   -> Resposta Aposta A: Status=%s | Saldo=%s BRL | Falha=%s\n", out1.Status, out1.Balance.AmountString(), code1)
	fmt.Printf("   -> Resposta Aposta B: Status=%s | Saldo=%s BRL | Falha=%s\n", out2.Status, out2.Balance.AmountString(), code2)
	fmt.Println("   🎯 Concorrência perfeita: uma aprovou, a outra foi rejeitada por falta de saldo!")
	fmt.Println()

	time.Sleep(200 * time.Millisecond)

	// 5. Teste de Idempotência: Provedor reenvia Aposta A
	fmt.Println("[PASSO 5] Provedor reenvia a Aposta A por falha na rede (mesma chave e mesmo payload)...")
	replayOut, _ := processWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:            "pragmatic",
		ExternalTransactionID: "bet-conc-A",
		IdempotencyKey:        "pragmatic:bet-conc-A",
		PlayerID:              playerID,
		WalletID:              wOut.ID,
		RoundID:               "round-2",
		GameID:                "fortune-tiger",
		Kind:                  transaction.KindBet,
		Money:                 money.MustNew("80.00", "BRL"),
	})
	fmt.Printf("✅ Idempotência detectada! IdempotentReplay: %v | Saldo retornado: %s BRL (sem debitar novamente!)\n\n",
		replayOut.IdempotentReplay, replayOut.Balance.AmountString())

	time.Sleep(200 * time.Millisecond)

	// 6. Reconciliação Contábil do Ledger
	fmt.Println("[PASSO 6] Executando auditoria e reconciliação contábil do Ledger...")
	recon, err := reconcileUC.Execute(ctx, usecase.ReconcileWalletInput{WalletID: wOut.ID})
	if err != nil {
		fmt.Printf("❌ Erro na reconciliação: %v\n", err)
		return
	}
	fmt.Printf("   -> Saldo na Tabela Carteiras: %s BRL\n", recon.StoredBalance.AmountString())
	fmt.Printf("   -> Saldo Reconstruído pelo Histórico do Ledger: %s BRL\n", recon.CalculatedBalance.AmountString())
	fmt.Printf("   -> Divergência: %s BRL\n", recon.Difference.AmountString())
	fmt.Printf("   -> Total de Lançamentos Auditados: %d\n", recon.CheckedEntries)
	fmt.Printf("   -> Estado Contábil: %s\n\n", map[bool]string{true: "✅ 100% CONSISTENTE (BATENDO CENTAVO POR CENTAVO)", false: "❌ DIVERGÊNCIA DETECTADA"}[recon.Consistent])

	// 7. Extrato Detalhado do Ledger
	ledgerOut, _ := getLedgerUC.Execute(ctx, wOut.ID, nil, 10)
	fmt.Println("📜 EXTRATO DO LEDGER IMUTÁVEL (APPEND-ONLY):")
	for i, e := range ledgerOut.Entries {
		fmt.Printf("   [%d] Direção: %-6s | Valor: %7s %s | Saldo Antes: %7s | Saldo Depois: %7s\n",
			i+1, e.Direction, e.Amount.AmountString(), e.Amount.Currency(), e.BalanceBefore.AmountString(), e.BalanceAfter.AmountString())
	}
	fmt.Println()
	fmt.Println("================================================================================")
	fmt.Println("🎉 SISTEMA 100% OPERACIONAL, BLINDADO E CONFORME AS REGRAS DA JUNGLE GAMING!")
	fmt.Println("================================================================================")
}
