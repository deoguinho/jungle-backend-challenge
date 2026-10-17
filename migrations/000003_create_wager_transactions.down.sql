DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.wager_transactions) THEN
        RAISE EXCEPTION 'cannot roll back wager_transactions: financial transaction history exists; use a forward migration';
    END IF;
END $$;
DROP TABLE app.wager_transactions;
