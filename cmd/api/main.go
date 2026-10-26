package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
	"github.com/junglegaming/backend-challenge-go/internal/infra/auth"
	apphttp "github.com/junglegaming/backend-challenge-go/internal/infra/http"
	"github.com/junglegaming/backend-challenge-go/internal/infra/postgres"
	appsqs "github.com/junglegaming/backend-challenge-go/internal/infra/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/usecase"
	"github.com/junglegaming/backend-challenge-go/internal/worker"
	"go.uber.org/fx"
)

func main() {
	fx.New(
		fx.Provide(
			// Config & Logger
			config.Load,
			newLogger,

			// Auth
			newJWTValidator,

			// Postgres
			newPostgresPool,
			fx.Annotate(postgres.NewTxManager, fx.As(new(repository.DBTransactionManager))),
			fx.Annotate(postgres.NewWalletRepository, fx.As(new(repository.WalletRepository))),
			fx.Annotate(postgres.NewTransactionRepository, fx.As(new(repository.TransactionRepository))),
			fx.Annotate(postgres.NewLedgerRepository, fx.As(new(repository.LedgerRepository))),
			fx.Annotate(postgres.NewOutboxRepository, fx.As(new(repository.OutboxRepository))),
			fx.Annotate(postgres.NewInboxRepository, fx.As(new(repository.InboxRepository))),

			// SQS Client & Publisher
			newSQSClient,
			newSQSPublisher,

			// Use Cases
			usecase.NewOpenWalletUseCase,
			usecase.NewProcessWagerUseCase,
			usecase.NewReconcileWalletUseCase,
			usecase.NewGetWalletUseCase,
			usecase.NewGetLedgerUseCase,
			usecase.NewGetTransactionUseCase,

			// Workers
			worker.NewOutboxPublisher,
			worker.NewReferenceResolver,

			// SQS Consumer
			newSQSConsumer,

			// HTTP Handlers & Router
			apphttp.NewHandlers,
			apphttp.NewRouter,
		),
		fx.Invoke(registerLifecycle),
	).Run()
}

func newLogger() *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	return slog.New(handler)
}

func newJWTValidator(cfg *config.Config) *auth.JWTValidator {
	return auth.NewJWTValidator(cfg.JWTSecret, cfg.JWTIssuer)
}

func newPostgresPool(lc fx.Lifecycle, cfg *config.Config, logger *slog.Logger) (*pgxpool.Pool, error) {
	pgCfg := postgres.Config{
		Host:     cfg.PostgresHost,
		Port:     cfg.PostgresPort,
		User:     cfg.PostgresUser,
		Password: cfg.PostgresPass,
		Database: cfg.PostgresDB,
		SSLMode:  cfg.PostgresSSL,
	}

	// We allow lazy connection attempt so fx builds in unit tests or docker compose start
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := postgres.NewPool(ctx, pgCfg)
	if err != nil {
		logger.Warn("could not connect to postgres on startup, pool may be uninitialized", "error", err)
	} else {
		if sqlBytes, readErr := os.ReadFile("migrations/000001_init_schema.up.sql"); readErr == nil {
			if _, migErr := pool.Exec(ctx, string(sqlBytes)); migErr != nil {
				logger.Warn("auto-migration warning", "error", migErr)
			} else {
				logger.Info("database migrations verified successfully")
			}
		}
	}

	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			if pool != nil {
				logger.Info("closing postgres connection pool...")
				pool.Close()
			}
			return nil
		},
	})

	return pool, nil
}

func newSQSClient(cfg *config.Config) (appsqs.SQSClient, error) {
	awsConfig, err := awscfg.LoadDefaultConfig(context.Background(),
		awscfg.WithRegion(cfg.SQSRegion),
	)
	if err != nil {
		return nil, err
	}

	client := awssqs.NewFromConfig(awsConfig, func(o *awssqs.Options) {
		if cfg.SQSEndpoint != "" {
			o.BaseEndpoint = &cfg.SQSEndpoint
		}
	})

	return client, nil
}

func newSQSPublisher(client appsqs.SQSClient, cfg *config.Config) *appsqs.Publisher {
	return appsqs.NewPublisher(client, cfg.SQSEventsQueueURL)
}

func newSQSConsumer(
	client appsqs.SQSClient,
	cfg *config.Config,
	wagerUC *usecase.ProcessWagerUseCase,
	logger *slog.Logger,
) *appsqs.Consumer {
	consumerCfg := appsqs.ConsumerConfig{
		QueueURL:          cfg.SQSQueueURL,
		MaxMessages:       10,
		WaitTimeSeconds:   10,
		VisibilityTimeout: 30,
		Concurrency:       2,
	}
	return appsqs.NewConsumer(client, consumerCfg, wagerUC, logger)
}

func registerLifecycle(
	lc fx.Lifecycle,
	router http.Handler,
	cfg *config.Config,
	outboxWorker *worker.OutboxPublisher,
	refWorker *worker.ReferenceResolver,
	sqsConsumer *appsqs.Consumer,
	logger *slog.Logger,
) {
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			logger.Info("starting HTTP server", "port", cfg.Port)
			go func() {
				if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					logger.Error("HTTP server error", "error", err)
				}
			}()

			// Start background workers
			outboxWorker.Start(context.Background())
			refWorker.Start(context.Background())
			sqsConsumer.Start(context.Background())

			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("initiating graceful shutdown...")

			// 1. Stop HTTP listener
			shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				logger.Error("HTTP server shutdown error", "error", err)
			}

			// 2. Stop SQS consumer and workers
			_ = sqsConsumer.Stop(ctx)
			_ = outboxWorker.Stop(ctx)
			_ = refWorker.Stop(ctx)

			logger.Info("shutdown completed successfully")
			return nil
		},
	})
}
