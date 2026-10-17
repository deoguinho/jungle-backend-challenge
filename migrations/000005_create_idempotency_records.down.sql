DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.idempotency_records) THEN
        RAISE EXCEPTION 'cannot roll back idempotency records: durable replay history exists; use a forward migration';
    END IF;
END $$;
DROP TABLE app.idempotency_records;
