CREATE TABLE app.wager_transactions (
    id UUID PRIMARY KEY,
    source TEXT NOT NULL CHECK (source IN ('EXTERNAL', 'INTERNAL')),
    kind TEXT NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    provider_id TEXT,
    external_transaction_id TEXT,
    idempotency_key TEXT,
    payload_hash BYTEA,
    wallet_id UUID NOT NULL,
    player_id UUID NOT NULL,
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    round_id TEXT,
    game_id TEXT,
    amount app.money_amount NOT NULL CHECK (amount >= 0),
    reference_external_transaction_id TEXT,
    reference_transaction_id UUID REFERENCES app.wager_transactions(id) ON DELETE RESTRICT,
    failure_code TEXT,
    result_balance app.money_amount CHECK (result_balance IS NULL OR result_balance >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    CONSTRAINT wager_wallet_owner_fk FOREIGN KEY (wallet_id, player_id, currency)
      REFERENCES app.wallets (id, player_id, currency) ON DELETE RESTRICT,
    CONSTRAINT wager_source_kind_ck CHECK (
      (source = 'EXTERNAL' AND kind IN ('BET','WIN','LOSS','REFUND','ROLLBACK')) OR
      (source = 'INTERNAL' AND kind = 'OPENING')),
    CONSTRAINT wager_origin_metadata_ck CHECK (
      (source = 'EXTERNAL' AND provider_id IS NOT NULL AND btrim(provider_id) <> '' AND
       external_transaction_id IS NOT NULL AND btrim(external_transaction_id) <> '' AND
       idempotency_key IS NOT NULL AND btrim(idempotency_key) <> '' AND
       payload_hash IS NOT NULL AND octet_length(payload_hash) = 32 AND
       round_id IS NOT NULL AND btrim(round_id) <> '' AND game_id IS NOT NULL AND btrim(game_id) <> '') OR
      (source = 'INTERNAL' AND provider_id IS NULL AND external_transaction_id IS NULL AND
       idempotency_key IS NULL AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL)),
    CONSTRAINT wager_amount_ck CHECK (
      (kind = 'LOSS' AND amount = 0) OR (kind = 'OPENING' AND amount >= 0) OR
      (kind IN ('BET','WIN','REFUND','ROLLBACK') AND amount > 0)),
    CONSTRAINT wager_reference_ck CHECK (
      (kind IN ('REFUND','ROLLBACK') AND reference_external_transaction_id IS NOT NULL AND btrim(reference_external_transaction_id) <> '') OR
      (kind NOT IN ('REFUND','ROLLBACK') AND reference_external_transaction_id IS NULL)),
    CONSTRAINT wager_reference_resolution_ck CHECK (
      reference_transaction_id IS NULL OR reference_external_transaction_id IS NOT NULL),
    CONSTRAINT wager_completed_ck CHECK (
      (status IN ('PROCESSED','REJECTED','FAILED') AND completed_at IS NOT NULL) OR
      (status IN ('PENDING','PENDING_REFERENCE') AND completed_at IS NULL)),
    CONSTRAINT wager_id_wallet_uq UNIQUE (id, wallet_id)
);

CREATE UNIQUE INDEX wager_provider_external_id_uq ON app.wager_transactions(provider_id, external_transaction_id) WHERE source = 'EXTERNAL';
CREATE UNIQUE INDEX wager_provider_idempotency_uq ON app.wager_transactions(provider_id, idempotency_key) WHERE source = 'EXTERNAL';
CREATE UNIQUE INDEX wager_opening_wallet_uq ON app.wager_transactions(wallet_id) WHERE source = 'INTERNAL' AND kind = 'OPENING';
CREATE INDEX wager_wallet_created_idx ON app.wager_transactions(wallet_id, created_at DESC, id DESC);
CREATE INDEX wager_provider_created_idx ON app.wager_transactions(provider_id, created_at DESC, id DESC) WHERE source = 'EXTERNAL';
CREATE INDEX wager_status_updated_pending_idx ON app.wager_transactions(status, updated_at) WHERE status IN ('PENDING','PENDING_REFERENCE');
CREATE INDEX wager_reference_idx ON app.wager_transactions(reference_transaction_id) WHERE reference_transaction_id IS NOT NULL;
