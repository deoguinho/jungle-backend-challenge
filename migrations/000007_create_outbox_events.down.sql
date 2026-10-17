DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.outbox_events) THEN
        RAISE EXCEPTION 'cannot roll back outbox: durable events or delivery history exists; use a forward migration';
    END IF;
END $$;
DROP TABLE app.outbox_events;
DROP FUNCTION app.protect_outbox_event();
