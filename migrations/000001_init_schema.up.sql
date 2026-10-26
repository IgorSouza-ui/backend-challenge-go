-- =============================================================================
-- Migration: 000001_init_schema.up.sql
-- Description: Core Distributed Betting & Wallet Ledger Tables
-- =============================================================================

CREATE TABLE IF NOT EXISTS wallets (
    id UUID PRIMARY KEY,
    player_id UUID NOT NULL,
    currency VARCHAR(3) NOT NULL,
    balance BIGINT NOT NULL CHECK (balance >= 0),
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_wallets_player_currency UNIQUE (player_id, currency)
);

CREATE TABLE IF NOT EXISTS wager_transactions (
    id UUID PRIMARY KEY,
    provider_id VARCHAR(100) NOT NULL,
    external_transaction_id VARCHAR(255),
    idempotency_key VARCHAR(255),
    payload_hash VARCHAR(64),
    wallet_id UUID NOT NULL REFERENCES wallets(id),
    player_id UUID NOT NULL,
    round_id VARCHAR(255),
    game_id VARCHAR(255),
    kind VARCHAR(20) NOT NULL,
    amount BIGINT NOT NULL,
    currency VARCHAR(3) NOT NULL,
    reference_external_transaction_id VARCHAR(255),
    resolved_internal_reference_id UUID,
    state VARCHAR(30) NOT NULL,
    failure_code VARCHAR(50),
    balance_at_process BIGINT,
    retry_count INT NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_transactions_opening_wallet
    ON wager_transactions(wallet_id)
    WHERE kind = 'OPENING';

CREATE UNIQUE INDEX IF NOT EXISTS uq_transactions_provider_ext
    ON wager_transactions(provider_id, external_transaction_id)
    WHERE kind != 'OPENING';

CREATE UNIQUE INDEX IF NOT EXISTS uq_transactions_provider_idempotency
    ON wager_transactions(provider_id, idempotency_key)
    WHERE kind != 'OPENING';

CREATE UNIQUE INDEX IF NOT EXISTS uq_transactions_processed_reversal
    ON wager_transactions(resolved_internal_reference_id)
    WHERE kind IN ('REFUND', 'ROLLBACK') AND state = 'PROCESSED';

CREATE INDEX IF NOT EXISTS idx_transactions_pending_ref
    ON wager_transactions(state, next_retry_at)
    WHERE state = 'PENDING_REFERENCE';

CREATE TABLE IF NOT EXISTS wallet_ledger (
    id UUID PRIMARY KEY,
    wallet_id UUID NOT NULL REFERENCES wallets(id),
    transaction_id UUID NOT NULL REFERENCES wager_transactions(id),
    direction VARCHAR(10) NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount BIGINT NOT NULL CHECK (amount > 0),
    currency VARCHAR(3) NOT NULL,
    balance_before BIGINT NOT NULL,
    balance_after BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_wallet_ledger_wallet_tx UNIQUE (wallet_id, transaction_id)
);

CREATE INDEX IF NOT EXISTS idx_wallet_ledger_wallet_created ON wallet_ledger(wallet_id, created_at DESC, id DESC);

-- Trigger to guarantee append-only ledger (blocks any UPDATE or DELETE)
CREATE OR REPLACE FUNCTION prevent_ledger_modification()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'Ledger entries are strictly immutable and cannot be updated or deleted';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_protect_wallet_ledger ON wallet_ledger;
CREATE TRIGGER trg_protect_wallet_ledger
BEFORE UPDATE OR DELETE ON wallet_ledger
FOR EACH ROW
EXECUTE FUNCTION prevent_ledger_modification();

-- Inbox table for idempotent message consumption
CREATE TABLE IF NOT EXISTS inbox_messages (
    id UUID PRIMARY KEY,
    consumer_name VARCHAR(100) NOT NULL,
    message_id VARCHAR(255) NOT NULL,
    payload_hash VARCHAR(64) NOT NULL,
    status VARCHAR(20) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ,
    CONSTRAINT uq_inbox_consumer_msg UNIQUE (consumer_name, message_id)
);

-- Outbox table for atomic event publishing
CREATE TABLE IF NOT EXISTS outbox_events (
    id UUID PRIMARY KEY,
    aggregate_type VARCHAR(50) NOT NULL,
    aggregate_id UUID NOT NULL,
    event_type VARCHAR(50) NOT NULL,
    payload JSONB NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    retry_count INT NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_outbox_pending ON outbox_events(status, next_retry_at) WHERE status = 'PENDING';
