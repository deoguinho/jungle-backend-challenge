DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.wallet_ledger_entries) THEN
        RAISE EXCEPTION 'cannot roll back wallet ledger: financial entries exist; use a forward migration';
    END IF;
END $$;
DROP TABLE app.wallet_ledger_entries;
DROP FUNCTION app.reject_ledger_mutation();
