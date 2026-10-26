# =============================================================================
# Stage 1: Build
# =============================================================================
FROM golang:alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/wager-service ./cmd/api

# =============================================================================
# Stage 2: Runtime
# =============================================================================
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata curl

WORKDIR /app

COPY --from=builder /bin/wager-service /app/wager-service
COPY --from=builder /app/migrations /app/migrations

EXPOSE 8080

ENTRYPOINT ["/app/wager-service"]
