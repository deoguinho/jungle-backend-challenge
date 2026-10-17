DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.wallets) THEN
        RAISE EXCEPTION 'cannot roll back wallets: financial wallet data exists; use a forward migration';
    END IF;
END $$;
DROP TABLE app.wallets;
