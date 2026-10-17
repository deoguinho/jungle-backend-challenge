CREATE TABLE app.wallet_ledger_entries (
    id UUID PRIMARY KEY,
    wallet_id UUID NOT NULL REFERENCES app.wallets(id) ON DELETE RESTRICT,
    transaction_id UUID NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('DEBIT','CREDIT')),
    amount app.money_amount NOT NULL CHECK (amount > 0),
    balance_before app.money_amount NOT NULL CHECK (balance_before >= 0),
    balance_after app.money_amount NOT NULL CHECK (balance_after >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ledger_wallet_transaction_uq UNIQUE (wallet_id, transaction_id),
    CONSTRAINT ledger_transaction_wallet_fk FOREIGN KEY (transaction_id, wallet_id)
      REFERENCES app.wager_transactions(id, wallet_id) ON DELETE RESTRICT,
    CONSTRAINT ledger_balance_equation_ck CHECK (
      (direction = 'CREDIT' AND balance_after = balance_before + amount) OR
      (direction = 'DEBIT' AND balance_before >= amount AND balance_after = balance_before - amount))
);
CREATE INDEX ledger_wallet_created_idx ON app.wallet_ledger_entries(wallet_id, created_at, id);

CREATE FUNCTION app.reject_ledger_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet ledger is append-only' USING ERRCODE = '55000';
END $$;
CREATE TRIGGER wallet_ledger_immutable BEFORE UPDATE OR DELETE ON app.wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION app.reject_ledger_mutation();
