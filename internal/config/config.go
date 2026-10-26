package config

import (
	"os"
	"strconv"
)

type Config struct {
	Port              int
	PostgresHost      string
	PostgresPort      int
	PostgresUser      string
	PostgresPass      string
	PostgresDB        string
	PostgresSSL       string
	SQSEndpoint       string
	SQSRegion         string
	SQSQueueURL       string
	SQSEventsQueueURL string
	JWTSecret         string
	JWTIssuer         string
}

func Load() *Config {
	return &Config{
		Port:              getEnvInt("PORT", 8080),
		PostgresHost:      getEnv("POSTGRES_HOST", "localhost"),
		PostgresPort:      getEnvInt("POSTGRES_PORT", 5432),
		PostgresUser:      getEnv("POSTGRES_USER", "postgres"),
		PostgresPass:      getEnv("POSTGRES_PASSWORD", "postgres"),
		PostgresDB:        getEnv("POSTGRES_DB", "jungle_wager"),
		PostgresSSL:       getEnv("POSTGRES_SSLMODE", "disable"),
		SQSEndpoint:       getEnv("SQS_ENDPOINT", "http://localhost:4566"),
		SQSRegion:         getEnv("SQS_REGION", "us-east-1"),
		SQSQueueURL:       getEnv("SQS_QUEUE_URL", "http://localhost:4566/000000000000/wager-transactions.fifo"),
		SQSEventsQueueURL: getEnv("SQS_EVENTS_QUEUE_URL", "http://localhost:4566/000000000000/wager-events.fifo"),
		JWTSecret:         getEnv("JWT_SECRET", "super-secret-keycloak-mock-secret-32-bytes!"),
		JWTIssuer:         getEnv("JWT_ISSUER", "http://localhost:8081/realms/jungle"),
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}
