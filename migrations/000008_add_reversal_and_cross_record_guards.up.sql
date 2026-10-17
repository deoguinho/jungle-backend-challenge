-- One successful reversal per referenced transaction. A ROLLBACK may point to a REFUND,
-- allowing a compensating rollback of the refund while preventing two direct reversals.
CREATE UNIQUE INDEX wager_one_processed_reversal_idx
    ON app.wager_transactions(reference_transaction_id)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND','ROLLBACK') AND reference_transaction_id IS NOT NULL;

CREATE FUNCTION app.validate_wager_reference() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    target app.wager_transactions%ROWTYPE;
BEGIN
    IF NEW.reference_transaction_id IS NULL THEN
        RETURN NEW;
    END IF;

    SELECT * INTO target FROM app.wager_transactions
      WHERE id = NEW.reference_transaction_id FOR KEY SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'referenced transaction does not exist' USING ERRCODE = '23503';
    END IF;
    IF NEW.source <> 'EXTERNAL' OR target.source <> 'EXTERNAL'
       OR NEW.provider_id IS DISTINCT FROM target.provider_id
       OR NEW.player_id IS DISTINCT FROM target.player_id
       OR NEW.wallet_id IS DISTINCT FROM target.wallet_id
       OR NEW.currency IS DISTINCT FROM target.currency
       OR NEW.round_id IS DISTINCT FROM target.round_id
       OR NEW.reference_external_transaction_id IS DISTINCT FROM target.external_transaction_id THEN
        RAISE EXCEPTION 'referenced transaction must match provider, player, wallet, currency, and round'
          USING ERRCODE = '23514';
    END IF;
    IF NEW.kind = 'REFUND' AND target.kind <> 'BET' THEN
        RAISE EXCEPTION 'REFUND must reference a BET' USING ERRCODE = '23514';
    END IF;
    IF NEW.kind = 'ROLLBACK' AND target.kind NOT IN ('BET','WIN','REFUND') THEN
        RAISE EXCEPTION 'ROLLBACK must reference a BET, WIN, or REFUND' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER wager_reference_guard BEFORE INSERT OR UPDATE OF reference_transaction_id
    ON app.wager_transactions FOR EACH ROW EXECUTE FUNCTION app.validate_wager_reference();

CREATE FUNCTION app.protect_terminal_wager() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status IN ('PROCESSED','REJECTED','FAILED') THEN
        RAISE EXCEPTION 'terminal wager transaction is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER wager_terminal_immutable BEFORE UPDATE ON app.wager_transactions
    FOR EACH ROW EXECUTE FUNCTION app.protect_terminal_wager();
