DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.wager_transactions) THEN
        RAISE EXCEPTION 'cannot roll back transaction guards after transaction history exists; use a forward migration';
    END IF;
END $$;
DROP TRIGGER wager_terminal_immutable ON app.wager_transactions;
DROP FUNCTION app.protect_terminal_wager();
DROP TRIGGER wager_reference_guard ON app.wager_transactions;
DROP FUNCTION app.validate_wager_reference();
DROP INDEX app.wager_one_processed_reversal_idx;
