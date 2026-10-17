DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.inbox_messages) THEN
        RAISE EXCEPTION 'cannot roll back inbox: deduplication and recovery history exists; use a forward migration';
    END IF;
END $$;
DROP TABLE app.inbox_messages;
