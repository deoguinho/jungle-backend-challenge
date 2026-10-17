DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.wager_transactions WHERE status = 'PENDING_REFERENCE') THEN
        RAISE EXCEPTION 'cannot remove reference retry schedule while pending references exist';
    END IF;
END $$;

DROP INDEX app.wager_reference_retry_idx;
DROP TRIGGER wager_reference_schedule ON app.wager_transactions;
DROP FUNCTION app.set_reference_schedule();
ALTER TABLE app.wager_transactions DROP CONSTRAINT wager_reference_schedule_ck;
ALTER TABLE app.wager_transactions
    DROP COLUMN reference_attempt_count,
    DROP COLUMN next_reference_attempt_at,
    DROP COLUMN reference_expires_at;
