package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	TransactionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "wager_transactions_total",
		Help: "Total number of processed wager transactions partitioned by status, kind, and provider",
	}, []string{"status", "kind", "provider_id"})

	IdempotentDuplicatesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "wager_idempotent_duplicates_total",
		Help: "Total number of idempotent duplicate replays detected",
	}, []string{"source", "provider_id"})

	RetriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "wager_retries_total",
		Help: "Total number of retry attempts by component (outbox, reference_resolver, sqs_consumer)",
	}, []string{"component"})

	DLQMessagesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "wager_dlq_messages_total",
		Help: "Total number of messages routed to Dead Letter Queue or permanently failed",
	})

	ConcurrencyConflictsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "wager_concurrency_conflicts_total",
		Help: "Total number of concurrency or idempotency conflicts",
	})

	OutboxLagSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "wager_outbox_lag_seconds",
		Help: "Lag in seconds between outbox event creation and publication",
	})

	ProcessingDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "wager_processing_duration_seconds",
		Help:    "Latency of wager transaction processing in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"kind", "source"})

	ReconciliationDivergencesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "wager_reconciliation_divergences_total",
		Help: "Total number of wallet balance vs ledger reconciliation divergences detected",
	})
)
