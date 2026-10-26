DROP TRIGGER IF EXISTS trg_protect_wallet_ledger ON wallet_ledger;
DROP FUNCTION IF EXISTS prevent_ledger_modification();
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS inbox_messages;
DROP TABLE IF EXISTS wallet_ledger;
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;
