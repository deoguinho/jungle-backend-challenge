CREATE TABLE app.outbox_events (
    event_id UUID PRIMARY KEY,
    aggregate_id UUID NOT NULL,
    event_type TEXT NOT NULL CHECK (btrim(event_type) <> ''),
    event_version INTEGER NOT NULL CHECK (event_version > 0),
    correlation_id UUID NOT NULL,
    causation_id UUID,
    occurred_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    locked_at TIMESTAMPTZ,
    locked_by TEXT,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT outbox_lease_pair_ck CHECK ((locked_at IS NULL) = (locked_by IS NULL))
);
CREATE INDEX outbox_pending_idx ON app.outbox_events(next_attempt_at, created_at) WHERE published_at IS NULL;
CREATE INDEX outbox_aggregate_idx ON app.outbox_events(aggregate_id, occurred_at, event_id);

CREATE FUNCTION app.protect_outbox_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.event_id, NEW.aggregate_id, NEW.event_type, NEW.event_version, NEW.correlation_id,
        NEW.causation_id, NEW.occurred_at, NEW.payload, NEW.created_at)
       IS DISTINCT FROM
       (OLD.event_id, OLD.aggregate_id, OLD.event_type, OLD.event_version, OLD.correlation_id,
        OLD.causation_id, OLD.occurred_at, OLD.payload, OLD.created_at) THEN
        RAISE EXCEPTION 'outbox event identity and payload are immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER outbox_event_immutable BEFORE UPDATE ON app.outbox_events
    FOR EACH ROW EXECUTE FUNCTION app.protect_outbox_event();
