DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'app') THEN
        RAISE EXCEPTION 'cannot drop app schema: it still contains tables; roll back dependent migrations first';
    END IF;
END $$;
DROP DOMAIN app.money_amount;
DROP SCHEMA app;
