CREATE TABLE app.inbox_messages (
    consumer_name TEXT NOT NULL CHECK (btrim(consumer_name) <> ''),
    message_id TEXT NOT NULL CHECK (btrim(message_id) <> ''),
    payload_hash BYTEA NOT NULL CHECK (octet_length(payload_hash) = 32),
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    transaction_id UUID REFERENCES app.wager_transactions(id) ON DELETE RESTRICT,
    failure_code TEXT,
    PRIMARY KEY (consumer_name, message_id)
);
CREATE INDEX inbox_completed_idx ON app.inbox_messages(completed_at) WHERE completed_at IS NOT NULL;
