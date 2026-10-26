package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/infra/auth"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func NewRouter(
	handlers *Handlers,
	jwtValidator *auth.JWTValidator,
	logger *slog.Logger,
) http.Handler {
	mux := http.NewServeMux()

	// Public Health Probes & Metrics
	mux.HandleFunc("GET /health/live", handlers.HandleHealthLive)
	mux.HandleFunc("GET /health/ready", handlers.HandleHealthReady)
	mux.Handle("GET /metrics", promhttp.Handler())

	// Protected Endpoints
	internalAuth := jwtValidator.Middleware("internal")
	providerAuth := jwtValidator.Middleware("")

	// Wallets (restricted to internal service role)
	mux.Handle("POST /wallets", internalAuth(http.HandlerFunc(handlers.HandleOpenWallet)))
	mux.Handle("GET /wallets/{walletId}", internalAuth(http.HandlerFunc(handlers.HandleGetWallet)))
	mux.Handle("GET /wallets/{walletId}/ledger", internalAuth(http.HandlerFunc(handlers.HandleGetLedger)))
	mux.Handle("POST /wallets/{walletId}/reconciliation", internalAuth(http.HandlerFunc(handlers.HandleReconcileWallet)))

	// Wagering Transactions
	mux.Handle("POST /wagering/transactions", providerAuth(http.HandlerFunc(handlers.HandleProcessWager)))
	mux.Handle("GET /wagering/transactions/{transactionId}", providerAuth(http.HandlerFunc(handlers.HandleGetTransactionByID)))
	mux.Handle("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", providerAuth(http.HandlerFunc(handlers.HandleGetTransactionByExternalID)))

	// Global logging & correlation ID middleware
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		corrID := r.Header.Get("X-Correlation-ID")
		if corrID == "" {
			corrID = uuid.New().String()
			r.Header.Set("X-Correlation-ID", corrID)
		}
		w.Header().Set("X-Correlation-ID", corrID)

		mux.ServeHTTP(w, r)

		logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"correlationId", corrID,
			"durationMs", time.Since(start).Milliseconds(),
		)
	})
}
