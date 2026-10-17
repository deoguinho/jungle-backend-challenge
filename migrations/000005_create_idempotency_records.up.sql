CREATE TABLE app.idempotency_records (
    id UUID PRIMARY KEY,
    provider_id TEXT NOT NULL CHECK (btrim(provider_id) <> ''),
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    payload_hash BYTEA NOT NULL CHECK (octet_length(payload_hash) = 32),
    transaction_id UUID NOT NULL UNIQUE REFERENCES app.wager_transactions(id) ON DELETE RESTRICT,
    response_snapshot JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT idempotency_provider_key_uq UNIQUE (provider_id, idempotency_key)
);
