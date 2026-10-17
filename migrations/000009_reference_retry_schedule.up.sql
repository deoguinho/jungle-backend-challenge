ALTER TABLE app.wager_transactions
    ADD COLUMN reference_attempt_count INTEGER,
    ADD COLUMN next_reference_attempt_at TIMESTAMPTZ,
    ADD COLUMN reference_expires_at TIMESTAMPTZ;

UPDATE app.wager_transactions
SET reference_attempt_count = 0,
    next_reference_attempt_at = now() + interval '1 second',
    reference_expires_at = created_at + interval '24 hours'
WHERE status = 'PENDING_REFERENCE';

ALTER TABLE app.wager_transactions
    ADD CONSTRAINT wager_reference_schedule_ck CHECK (
        (status = 'PENDING_REFERENCE' AND reference_attempt_count IS NOT NULL AND reference_attempt_count >= 0
         AND next_reference_attempt_at IS NOT NULL AND reference_expires_at IS NOT NULL
         AND reference_expires_at > created_at)
        OR
        (status <> 'PENDING_REFERENCE' AND reference_attempt_count IS NULL
         AND next_reference_attempt_at IS NULL AND reference_expires_at IS NULL)
    );

CREATE FUNCTION app.set_reference_schedule() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status = 'PENDING_REFERENCE' AND OLD.status <> 'PENDING_REFERENCE' THEN
        NEW.reference_attempt_count := 0;
        NEW.next_reference_attempt_at := now() + interval '1 second';
        NEW.reference_expires_at := now() + interval '24 hours';
    ELSIF OLD.status = 'PENDING_REFERENCE' AND NEW.status <> 'PENDING_REFERENCE' THEN
        NEW.reference_attempt_count := NULL;
        NEW.next_reference_attempt_at := NULL;
        NEW.reference_expires_at := NULL;
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER wager_reference_schedule BEFORE UPDATE OF status ON app.wager_transactions
    FOR EACH ROW EXECUTE FUNCTION app.set_reference_schedule();

CREATE INDEX wager_reference_retry_idx
    ON app.wager_transactions(next_reference_attempt_at, reference_expires_at)
    WHERE status = 'PENDING_REFERENCE';
