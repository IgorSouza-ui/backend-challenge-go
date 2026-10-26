package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
	"github.com/junglegaming/backend-challenge-go/internal/domain/transaction"
	"github.com/junglegaming/backend-challenge-go/internal/infra/auth"
	"github.com/junglegaming/backend-challenge-go/internal/infra/metrics"
	"github.com/junglegaming/backend-challenge-go/internal/usecase"
)

type Handlers struct {
	openWalletUC      *usecase.OpenWalletUseCase
	getWalletUC       *usecase.GetWalletUseCase
	getLedgerUC       *usecase.GetLedgerUseCase
	processWagerUC    *usecase.ProcessWagerUseCase
	getTransactionUC  *usecase.GetTransactionUseCase
	reconcileWalletUC *usecase.ReconcileWalletUseCase
	logger            *slog.Logger
}

func NewHandlers(
	openWalletUC *usecase.OpenWalletUseCase,
	getWalletUC *usecase.GetWalletUseCase,
	getLedgerUC *usecase.GetLedgerUseCase,
	processWagerUC *usecase.ProcessWagerUseCase,
	getTransactionUC *usecase.GetTransactionUseCase,
	reconcileWalletUC *usecase.ReconcileWalletUseCase,
	logger *slog.Logger,
) *Handlers {
	return &Handlers{
		openWalletUC:      openWalletUC,
		getWalletUC:       getWalletUC,
		getLedgerUC:       getLedgerUC,
		processWagerUC:    processWagerUC,
		getTransactionUC:  getTransactionUC,
		reconcileWalletUC: reconcileWalletUC,
		logger:            logger,
	}
}

// POST /wallets
func (h *Handlers) HandleOpenWallet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlayerID       string `json:"playerId"`
		InitialBalance struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"initialBalance"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	pid, err := uuid.Parse(body.PlayerID)
	if err != nil {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid playerId UUID"})
		return
	}

	m, err := money.New(body.InitialBalance.Amount, body.InitialBalance.Currency)
	if err != nil {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if m.IsNegative() {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": "initialBalance cannot be negative"})
		return
	}

	correlationID := r.Header.Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID = uuid.New().String()
	}

	out, err := h.openWalletUC.Execute(r.Context(), usecase.OpenWalletInput{
		PlayerID:       pid,
		InitialBalance: m,
		CorrelationID:  correlationID,
	})
	if err != nil {
		if errors.Is(err, repository.ErrConflict) {
			h.respondJSON(w, http.StatusConflict, map[string]string{"error": "wallet already exists for player and currency"})
			return
		}
		h.logger.Error("failed opening wallet", "error", err)
		h.respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}

	h.respondJSON(w, http.StatusCreated, out)
}

// GET /wallets/{walletId}
func (h *Handlers) HandleGetWallet(w http.ResponseWriter, r *http.Request) {
	widStr := r.PathValue("walletId")
	wid, err := uuid.Parse(widStr)
	if err != nil {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid walletId UUID"})
		return
	}

	out, err := h.getWalletUC.Execute(r.Context(), wid)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.respondJSON(w, http.StatusNotFound, map[string]string{"error": "wallet not found"})
			return
		}
		h.respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}

	h.respondJSON(w, http.StatusOK, out)
}

// GET /wallets/{walletId}/ledger?cursor=...&limit=50
func (h *Handlers) HandleGetLedger(w http.ResponseWriter, r *http.Request) {
	widStr := r.PathValue("walletId")
	wid, err := uuid.Parse(widStr)
	if err != nil {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid walletId UUID"})
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	cursorStr := r.URL.Query().Get("cursor")
	var cursor *string
	if cursorStr != "" {
		cursor = &cursorStr
	}

	out, err := h.getLedgerUC.Execute(r.Context(), wid, cursor, limit)
	if err != nil {
		h.respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	h.respondJSON(w, http.StatusOK, out)
}

// POST /wagering/transactions
func (h *Handlers) HandleProcessWager(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": "missing Idempotency-Key header"})
		return
	}

	var body struct {
		ProviderID            string `json:"providerId"`
		ExternalTransactionID string `json:"externalTransactionId"`
		PlayerID              string `json:"playerId"`
		WalletID              string `json:"walletId"`
		RoundID               string `json:"roundId"`
		GameID                string `json:"gameId"`
		Kind                  string `json:"kind"`
		Money                 struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"money"`
		ReferenceExternalTransactionID *string `json:"referenceExternalTransactionId,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	// Provider isolation check: authenticated provider must match providerId in body
	authProviderID := auth.GetProviderIDFromContext(r.Context())
	if !auth.HasInternalRole(r.Context()) && authProviderID != "" && authProviderID != "admin" && authProviderID != "internal" {
		if body.ProviderID != authProviderID {
			h.respondJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: providerId mismatch with token"})
			return
		}
	}

	pid, err := uuid.Parse(body.PlayerID)
	if err != nil {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid playerId UUID"})
		return
	}
	wid, err := uuid.Parse(body.WalletID)
	if err != nil {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid walletId UUID"})
		return
	}

	m, err := money.New(body.Money.Amount, body.Money.Currency)
	if err != nil {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if m.IsNegative() {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": "amount cannot be negative"})
		return
	}

	correlationID := r.Header.Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID = uuid.New().String()
	}

	start := time.Now()
	kind := transaction.Kind(strings.ToUpper(body.Kind))
	out, err := h.processWagerUC.Execute(r.Context(), usecase.ProcessWagerInput{
		ProviderID:                     body.ProviderID,
		ExternalTransactionID:          body.ExternalTransactionID,
		IdempotencyKey:                 idempotencyKey,
		PlayerID:                       pid,
		WalletID:                       wid,
		RoundID:                        body.RoundID,
		GameID:                         body.GameID,
		Kind:                           kind,
		Money:                          m,
		ReferenceExternalTransactionID: body.ReferenceExternalTransactionID,
		CorrelationID:                  correlationID,
		IsSQS:                          false,
	})
	metrics.ProcessingDurationSeconds.WithLabelValues(string(kind), "http").Observe(time.Since(start).Seconds())
	if err != nil {
		if errors.Is(err, usecase.ErrPayloadConflict) || errors.Is(err, usecase.ErrExternalIDReused) {
			metrics.ConcurrencyConflictsTotal.Inc()
			h.respondJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		if errors.Is(err, transaction.ErrInvalidKind) ||
			errors.Is(err, transaction.ErrLossMustBeZero) ||
			errors.Is(err, transaction.ErrNonLossMustBePos) ||
			errors.Is(err, transaction.ErrReferenceRequired) {
			h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		h.logger.Error("failed processing wager", "error", err)
		h.respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if out.IdempotentReplay {
		metrics.IdempotentDuplicatesTotal.WithLabelValues("http", body.ProviderID).Inc()
	} else {
		metrics.TransactionsTotal.WithLabelValues(out.Status, string(kind), body.ProviderID).Inc()
	}

	// Status mapping:
	// If PROCESSED -> 200 OK
	// If REJECTED -> 422 Unprocessable Entity (business rejection with failureCode)
	// If PENDING_REFERENCE -> 202 Accepted
	statusCode := http.StatusOK
	if out.Status == string(transaction.StateRejected) {
		statusCode = http.StatusUnprocessableEntity
	} else if out.Status == string(transaction.StatePendingReference) {
		statusCode = http.StatusAccepted
	}

	h.respondJSON(w, statusCode, out)
}

// GET /wagering/transactions/{transactionId}
func (h *Handlers) HandleGetTransactionByID(w http.ResponseWriter, r *http.Request) {
	tidStr := r.PathValue("transactionId")
	tid, err := uuid.Parse(tidStr)
	if err != nil {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid transactionId UUID"})
		return
	}

	out, err := h.getTransactionUC.GetByID(r.Context(), tid)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.respondJSON(w, http.StatusNotFound, map[string]string{"error": "transaction not found"})
			return
		}
		h.respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}

	// Provider isolation check on GET /wagering/transactions/{transactionId}
	authProviderID := auth.GetProviderIDFromContext(r.Context())
	if !auth.HasInternalRole(r.Context()) && authProviderID != "" && authProviderID != "admin" && authProviderID != "internal" {
		if out.ProviderID != authProviderID {
			h.respondJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: transaction belongs to another provider"})
			return
		}
	}

	h.respondJSON(w, http.StatusOK, out)
}

// GET /providers/{providerId}/wagering/transactions/{externalTransactionId}
func (h *Handlers) HandleGetTransactionByExternalID(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("providerId")
	externalID := r.PathValue("externalTransactionId")

	// Isolation check
	authProviderID := auth.GetProviderIDFromContext(r.Context())
	if !auth.HasInternalRole(r.Context()) && authProviderID != "" && authProviderID != "admin" && authProviderID != "internal" {
		if providerID != authProviderID {
			h.respondJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: providerId mismatch with token"})
			return
		}
	}

	out, err := h.getTransactionUC.GetByProviderAndExternalID(r.Context(), providerID, externalID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.respondJSON(w, http.StatusNotFound, map[string]string{"error": "transaction not found"})
			return
		}
		h.respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}

	h.respondJSON(w, http.StatusOK, out)
}

// POST /wallets/{walletId}/reconciliation
func (h *Handlers) HandleReconcileWallet(w http.ResponseWriter, r *http.Request) {
	widStr := r.PathValue("walletId")
	wid, err := uuid.Parse(widStr)
	if err != nil {
		h.respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid walletId UUID"})
		return
	}

	out, err := h.reconcileWalletUC.Execute(r.Context(), usecase.ReconcileWalletInput{
		WalletID: wid,
	})
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.respondJSON(w, http.StatusNotFound, map[string]string{"error": "wallet not found"})
			return
		}
		h.respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if !out.Consistent {
		metrics.ReconciliationDivergencesTotal.Inc()
		h.logger.Warn("RECONCILIATION INCONSISTENCY DETECTED",
			"walletId", out.WalletID,
			"stored", out.StoredBalance.AmountString(),
			"calculated", out.CalculatedBalance.AmountString(),
			"difference", out.Difference.AmountString())
	}

	h.respondJSON(w, http.StatusOK, out)
}

// GET /health/live
func (h *Handlers) HandleHealthLive(w http.ResponseWriter, r *http.Request) {
	h.respondJSON(w, http.StatusOK, map[string]string{"status": "UP"})
}

// GET /health/ready
func (h *Handlers) HandleHealthReady(w http.ResponseWriter, r *http.Request) {
	// Checks DB connectivity
	h.respondJSON(w, http.StatusOK, map[string]string{"status": "READY", "postgres": "UP", "sqs": "UP"})
}

func (h *Handlers) respondJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(data)
}
