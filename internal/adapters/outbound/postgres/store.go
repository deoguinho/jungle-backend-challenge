package postgres

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/application"
	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wagering"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

type DB struct{ pool *pgxpool.Pool }

func New(ctx context.Context, databaseURL string) (*DB, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL URL: %w", err)
	}
	config.MaxConns = 20
	config.MinConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return &DB{pool: pool}, nil
}

func (d *DB) Close()                         { d.pool.Close() }
func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

func (d *DB) OldestPendingOutboxAge(ctx context.Context) (float64, error) {
	var seconds float64
	err := d.pool.QueryRow(ctx, `SELECT COALESCE(EXTRACT(EPOCH FROM now()-min(created_at)),0)::float8 FROM app.outbox_events WHERE published_at IS NULL`).Scan(&seconds)
	return seconds, translateError(err)
}

type PendingReference struct {
	ProviderID, IdempotencyKey string
	Expired                    bool
}

func (d *DB) ListPendingReferences(ctx context.Context, limit int) ([]PendingReference, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := d.pool.Query(ctx, `SELECT provider_id,idempotency_key,reference_expires_at<=now() FROM app.wager_transactions WHERE status='PENDING_REFERENCE' AND next_reference_attempt_at<=now() ORDER BY next_reference_attempt_at LIMIT $1`, limit)
	if err != nil {
		return nil, translateError(err)
	}
	defer rows.Close()
	out := make([]PendingReference, 0)
	for rows.Next() {
		var item PendingReference
		if err := rows.Scan(&item.ProviderID, &item.IdempotencyKey, &item.Expired); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (d *DB) DeferPendingReference(ctx context.Context, provider, key string) error {
	_, err := d.pool.Exec(ctx, `UPDATE app.wager_transactions SET reference_attempt_count=reference_attempt_count+1,next_reference_attempt_at=LEAST(reference_expires_at,now()+LEAST(interval '1 hour',interval '1 second'*power(2,LEAST(reference_attempt_count,12)))) WHERE provider_id=$1 AND idempotency_key=$2 AND status='PENDING_REFERENCE'`, provider, key)
	return translateError(err)
}

type OutboxRecord struct {
	Event   application.Event
	Attempt int
}

func (d *DB) ClaimOutbox(ctx context.Context, worker string, limit int) ([]OutboxRecord, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `WITH picked AS (SELECT event_id FROM app.outbox_events WHERE published_at IS NULL AND next_attempt_at<=now() AND (locked_at IS NULL OR locked_at<now()-interval '2 minutes') ORDER BY next_attempt_at,created_at FOR UPDATE SKIP LOCKED LIMIT $1) UPDATE app.outbox_events o SET locked_at=now(),locked_by=$2 FROM picked WHERE o.event_id=picked.event_id RETURNING o.event_id::text,o.aggregate_id::text,o.event_type,o.correlation_id::text,o.occurred_at,o.payload,o.attempt_count`, limit, worker)
	if err != nil {
		return nil, translateError(err)
	}
	out := make([]OutboxRecord, 0)
	for rows.Next() {
		var rec OutboxRecord
		var raw []byte
		if err := rows.Scan(&rec.Event.ID, &rec.Event.AggregateID, &rec.Event.Type, &rec.Event.CorrelationID, &rec.Event.OccurredAt, &raw, &rec.Attempt); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(raw, &rec.Event.Data); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
func (d *DB) FinishOutbox(ctx context.Context, worker, eventID string, publishErr error) error {
	if publishErr == nil {
		_, err := d.pool.Exec(ctx, `UPDATE app.outbox_events SET published_at=now(),locked_at=NULL,locked_by=NULL,last_error=NULL WHERE event_id=$1 AND locked_by=$2 AND published_at IS NULL`, eventID, worker)
		return translateError(err)
	}
	message := publishErr.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	_, err := d.pool.Exec(ctx, `UPDATE app.outbox_events SET attempt_count=attempt_count+1,next_attempt_at=now()+LEAST(interval '1 hour',interval '1 second'*power(2,LEAST(attempt_count,12))),locked_at=NULL,locked_by=NULL,last_error=$3 WHERE event_id=$1 AND locked_by=$2 AND published_at IS NULL`, eventID, worker, message)
	return translateError(err)
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var x byte
	for i := range a {
		x |= a[i] ^ b[i]
	}
	return x == 0
}

func (d *DB) Within(ctx context.Context, fn func(application.Tx) error) error {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin database transaction: %w", err)
	}
	wrapped := &unitTx{tx: tx}
	if err := fn(wrapped); err != nil {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("rollback: %w", rollbackErr))
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit database transaction: %w", err)
	}
	return nil
}

type unitTx struct{ tx pgx.Tx }

func (t *unitTx) Wallets() application.WalletStore           { return walletStore{tx: t.tx} }
func (t *unitTx) Transactions() application.TransactionStore { return transactionStore{tx: t.tx} }
func (t *unitTx) Ledger() application.LedgerStore            { return ledgerStore{tx: t.tx} }
func (t *unitTx) Outbox() application.OutboxStore            { return outboxStore{tx: t.tx} }
func (t *unitTx) Inbox() application.InboxStore              { return inboxStore{tx: t.tx} }

type inboxStore struct{ tx pgx.Tx }

func (s inboxStore) Start(ctx context.Context, consumer, messageID string, payloadHash []byte) (bool, error) {
	_, err := s.tx.Exec(ctx, `INSERT INTO app.inbox_messages(consumer_name,message_id,payload_hash) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, consumer, messageID, payloadHash)
	if err != nil {
		return false, translateError(err)
	}
	var stored []byte
	var completed *time.Time
	if err = s.tx.QueryRow(ctx, `SELECT payload_hash,completed_at FROM app.inbox_messages WHERE consumer_name=$1 AND message_id=$2 FOR UPDATE`, consumer, messageID).Scan(&stored, &completed); err != nil {
		return false, translateError(err)
	}
	if !equalBytes(stored, payloadHash) {
		return false, application.ErrIdempotencyConflict
	}
	return completed != nil, nil
}

func (s inboxStore) Complete(ctx context.Context, consumer, messageID, transactionID, failureCode string) error {
	var txID any
	if transactionID != "" {
		txID = transactionID
	}
	var failure any
	if failureCode != "" {
		failure = failureCode
	}
	_, err := s.tx.Exec(ctx, `UPDATE app.inbox_messages SET completed_at=now(),transaction_id=$3,failure_code=$4 WHERE consumer_name=$1 AND message_id=$2 AND completed_at IS NULL`, consumer, messageID, txID, failure)
	return translateError(err)
}

type walletStore struct{ tx pgx.Tx }

func (s walletStore) Create(ctx context.Context, w *wallet.Wallet) error {
	_, err := s.tx.Exec(ctx, `INSERT INTO app.wallets(id,player_id,currency,balance,version,created_at,updated_at) VALUES($1,$2,$3,$4::numeric,$5,$6,$7)`, w.ID(), w.PlayerID(), w.Currency(), w.Balance().String(), w.Version(), w.CreatedAt(), w.UpdatedAt())
	return translateError(err)
}

func (s walletStore) ByID(ctx context.Context, id string) (*wallet.Wallet, error) {
	return loadWallet(ctx, s.tx, `SELECT id::text,player_id::text,currency,balance::text,version,created_at,updated_at FROM app.wallets WHERE id=$1`, id)
}

func (s walletStore) ByPlayerCurrency(ctx context.Context, playerID, currency string) (*wallet.Wallet, error) {
	return loadWallet(ctx, s.tx, `SELECT id::text,player_id::text,currency,balance::text,version,created_at,updated_at FROM app.wallets WHERE player_id=$1 AND currency=$2`, playerID, strings.ToUpper(currency))
}

func (s walletStore) ApplyMovement(ctx context.Context, walletID, transactionID string, direction wallet.Direction, amount shared.Money, at time.Time) (application.MovementResult, error) {
	if direction != wallet.CreditDirection && direction != wallet.DebitDirection {
		return application.MovementResult{}, shared.Error("wallet.invalid_direction", shared.ErrInvalidArgument)
	}
	delta := amount.Amount()
	if direction == wallet.DebitDirection {
		delta = delta.Neg()
	}
	var beforeText, afterText, currency string
	var version int64
	err := s.tx.QueryRow(ctx, `UPDATE app.wallets SET balance=balance+$1::numeric, version=version+1, updated_at=$2 WHERE id=$3 AND balance+$1::numeric>=0 RETURNING (balance-$1::numeric)::text,balance::text,currency,version`, delta.String(), at.UTC(), walletID).Scan(&beforeText, &afterText, &currency, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.MovementResult{}, shared.ErrInsufficientFunds
	}
	if err != nil {
		return application.MovementResult{}, translateError(err)
	}
	before, err := shared.NewMoney(beforeText, strings.TrimSpace(currency))
	if err != nil {
		return application.MovementResult{}, err
	}
	after, err := shared.NewMoney(afterText, strings.TrimSpace(currency))
	if err != nil {
		return application.MovementResult{}, err
	}
	entryID := uuid.NewString()
	_, err = s.tx.Exec(ctx, `INSERT INTO app.wallet_ledger_entries(id,wallet_id,transaction_id,direction,amount,balance_before,balance_after,created_at) VALUES($1,$2,$3,$4,$5::numeric,$6::numeric,$7::numeric,$8)`, entryID, walletID, transactionID, string(direction), amount.String(), before.String(), after.String(), at.UTC())
	if err != nil {
		return application.MovementResult{}, translateError(err)
	}
	entry, err := wallet.RehydrateLedgerEntry(walletID, transactionID, direction, amount, before, after, at)
	if err != nil {
		return application.MovementResult{}, err
	}
	return application.MovementResult{Entry: entry, Balance: after, Version: version}, nil
}

type rowScanner interface{ Scan(...any) error }

func loadWallet(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, query string, args ...any) (*wallet.Wallet, error) {
	var id, player, currency, balance string
	var version int64
	var created, updated time.Time
	if err := q.QueryRow(ctx, query, args...).Scan(&id, &player, &currency, &balance, &version, &created, &updated); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrNotFound
		}
		return nil, translateError(err)
	}
	m, err := shared.NewMoney(balance, strings.TrimSpace(currency))
	if err != nil {
		return nil, err
	}
	return wallet.Rehydrate(id, player, m, version, created, updated)
}

type transactionStore struct{ tx pgx.Tx }

const wagerColumns = `id::text,source,kind,status,provider_id,external_transaction_id,idempotency_key,payload_hash,player_id::text,wallet_id::text,round_id,game_id,amount::text,currency,reference_external_transaction_id,failure_code,result_balance::text,created_at,updated_at`

func (s transactionStore) Create(ctx context.Context, t *wagering.WagerTransaction) error {
	source := "EXTERNAL"
	if t.Kind() == wagering.Opening {
		source = "INTERNAL"
	}
	var hash any
	if t.PayloadHash() != "" {
		b, err := hex.DecodeString(t.PayloadHash())
		if err != nil || len(b) != 32 {
			return shared.Error("wager.invalid_payload_hash", shared.ErrInvalidArgument)
		}
		hash = b
	}
	var provider, external, key, round, game, reference any
	if source == "EXTERNAL" {
		provider = t.ProviderID()
		external = t.ExternalID()
		key = t.IdempotencyKey()
		round = t.RoundID()
		game = t.GameID()
		if t.ReferenceExternalID() != "" {
			reference = t.ReferenceExternalID()
		}
	}
	var failure any
	if t.FailureCode() != "" {
		failure = t.FailureCode()
	}
	var result any
	if balance := t.ResultBalance(); balance != nil {
		result = balance.String()
	}
	completed := any(nil)
	if terminal(t.Status()) {
		completed = t.UpdatedAt()
	}
	_, err := s.tx.Exec(ctx, `INSERT INTO app.wager_transactions(id,source,kind,status,provider_id,external_transaction_id,idempotency_key,payload_hash,wallet_id,player_id,currency,round_id,game_id,amount,reference_external_transaction_id,failure_code,result_balance,created_at,updated_at,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::numeric,$15,$16,$17::numeric,$18,$19,$20)`, t.ID(), source, string(t.Kind()), string(t.Status()), provider, external, key, hash, t.WalletID(), t.PlayerID(), t.Amount().Currency(), round, game, t.Amount().String(), reference, failure, result, t.CreatedAt(), t.UpdatedAt(), completed)
	if err != nil {
		return translateError(err)
	}
	if source == "EXTERNAL" {
		return s.upsertIdempotency(ctx, t)
	}
	return nil
}

func (s transactionStore) upsertIdempotency(ctx context.Context, t *wagering.WagerTransaction) error {
	hash, err := hex.DecodeString(t.PayloadHash())
	if err != nil {
		return err
	}
	snapshot, err := json.Marshal(transactionSnapshot(t))
	if err != nil {
		return err
	}
	_, err = s.tx.Exec(ctx, `INSERT INTO app.idempotency_records(id,provider_id,idempotency_key,payload_hash,transaction_id,response_snapshot) VALUES($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT(provider_id,idempotency_key) DO UPDATE SET payload_hash=EXCLUDED.payload_hash,transaction_id=EXCLUDED.transaction_id,response_snapshot=EXCLUDED.response_snapshot,updated_at=now()`, uuid.NewString(), t.ProviderID(), t.IdempotencyKey(), hash, t.ID(), snapshot)
	return translateError(err)
}
func transactionSnapshot(t *wagering.WagerTransaction) map[string]any {
	out := map[string]any{"transactionId": t.ID(), "status": t.Status(), "failureCode": t.FailureCode()}
	if b := t.ResultBalance(); b != nil {
		out["balance"] = map[string]string{"amount": b.String(), "currency": b.Currency()}
	}
	return out
}

func (s transactionStore) Save(ctx context.Context, t *wagering.WagerTransaction) error {
	var failure any
	if t.FailureCode() != "" {
		failure = t.FailureCode()
	}
	var result any
	if b := t.ResultBalance(); b != nil {
		result = b.String()
	}
	completed := any(nil)
	if terminal(t.Status()) {
		completed = t.UpdatedAt()
	}
	var refID any
	if t.ReferenceExternalID() != "" {
		var resolved string
		err := s.tx.QueryRow(ctx, `SELECT id FROM app.wager_transactions WHERE source='EXTERNAL' AND provider_id=$1 AND external_transaction_id=$2`, t.ProviderID(), t.ReferenceExternalID()).Scan(&resolved)
		if err == nil {
			refID = resolved
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return translateError(err)
		}
	}
	command, err := s.tx.Exec(ctx, `UPDATE app.wager_transactions SET status=$2,failure_code=$3,result_balance=$4::numeric,reference_transaction_id=COALESCE($5::uuid,reference_transaction_id),updated_at=$6,completed_at=$7 WHERE id=$1 AND status IN ('PENDING','PENDING_REFERENCE')`, t.ID(), string(t.Status()), failure, result, refID, t.UpdatedAt(), completed)
	if err != nil {
		return translateError(err)
	}
	if command.RowsAffected() == 0 {
		return application.ErrConflict
	}
	if t.ProviderID() != "" {
		return s.upsertIdempotency(ctx, t)
	}
	return nil
}

func (s transactionStore) ByID(ctx context.Context, id string) (*wagering.WagerTransaction, error) {
	return s.load(ctx, `SELECT `+wagerColumns+` FROM app.wager_transactions WHERE id=$1`, id)
}
func (s transactionStore) ByProviderExternal(ctx context.Context, provider, id string) (*wagering.WagerTransaction, error) {
	return s.load(ctx, `SELECT `+wagerColumns+` FROM app.wager_transactions WHERE source='EXTERNAL' AND provider_id=$1 AND external_transaction_id=$2`, provider, id)
}
func (s transactionStore) ByProviderIdempotency(ctx context.Context, provider, key string) (*wagering.WagerTransaction, error) {
	return s.load(ctx, `SELECT `+wagerColumns+` FROM app.wager_transactions WHERE source='EXTERNAL' AND provider_id=$1 AND idempotency_key=$2`, provider, key)
}
func (s transactionStore) ByReference(ctx context.Context, provider, externalID string) (*wagering.WagerTransaction, error) {
	return s.ByProviderExternal(ctx, provider, externalID)
}
func (s transactionStore) load(ctx context.Context, q string, args ...any) (*wagering.WagerTransaction, error) {
	var id, source, kind, status string
	var provider, external, key, round, game, refID, failure, result *string
	var hash []byte
	var player, walletID, amount, currency string
	var created, updated time.Time
	err := s.tx.QueryRow(ctx, q, args...).Scan(&id, &source, &kind, &status, &provider, &external, &key, &hash, &player, &walletID, &round, &game, &amount, &currency, &refID, &failure, &result, &created, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrNotFound
	}
	if err != nil {
		return nil, translateError(err)
	}
	parsed, err := shared.NewMoney(amount, strings.TrimSpace(currency))
	if err != nil {
		return nil, err
	}
	params := wagering.ExternalParams{ID: id, ProviderID: deref(provider), ExternalID: deref(external), IdempotencyKey: deref(key), PayloadHash: hex.EncodeToString(hash), PlayerID: player, WalletID: walletID, RoundID: deref(round), GameID: deref(game), Kind: wagering.Kind(kind), Amount: parsed, ReferenceExternalID: deref(refID), CreatedAt: created}
	if source == "INTERNAL" {
		return wagering.RehydrateOpening(id, player, walletID, parsed, wagering.Status(status), created, updated)
	}
	var bal *shared.Money
	if result != nil {
		v, e := shared.NewMoney(*result, parsed.Currency())
		if e != nil {
			return nil, e
		}
		bal = &v
	}
	return wagering.Rehydrate(params, wagering.Status(status), deref(failure), bal, updated)
}

type ledgerStore struct{ tx pgx.Tx }

func (s ledgerStore) Append(ctx context.Context, e *wallet.LedgerEntry) error {
	_, err := s.tx.Exec(ctx, `INSERT INTO app.wallet_ledger_entries(id,wallet_id,transaction_id,direction,amount,balance_before,balance_after,created_at) VALUES($1,$2,$3,$4,$5::numeric,$6::numeric,$7::numeric,$8)`, uuid.NewString(), e.WalletID(), e.TransactionID(), string(e.Direction()), e.Amount().String(), e.BalanceBefore().String(), e.BalanceAfter().String(), e.CreatedAt())
	return translateError(err)
}
func (s ledgerStore) List(ctx context.Context, walletID, cursor string, limit int) (application.LedgerPage, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	query := `SELECT id::text,transaction_id::text,direction,amount::text,balance_before::text,balance_after::text,created_at FROM app.wallet_ledger_entries WHERE wallet_id=$1`
	args := []any{walletID}
	if cursor != "" {
		at, id, err := decodeCursor(cursor)
		if err != nil {
			return application.LedgerPage{}, shared.Error("ledger.invalid_cursor", shared.ErrInvalidArgument)
		}
		query += ` AND (created_at,id)>($2,$3)`
		args = append(args, at, id)
	}
	query += fmt.Sprintf(` ORDER BY created_at,id LIMIT $%d`, len(args)+1)
	args = append(args, limit+1)
	rows, err := s.tx.Query(ctx, query, args...)
	if err != nil {
		return application.LedgerPage{}, translateError(err)
	}
	defer rows.Close()
	currency, err := s.walletCurrency(ctx, walletID)
	if err != nil {
		return application.LedgerPage{}, err
	}
	entries := make([]*wallet.LedgerEntry, 0, limit+1)
	entryIDs := make([]string, 0, limit+1)
	for rows.Next() {
		var entryID, txID, dir, amount, before, after string
		var at time.Time
		if err := rows.Scan(&entryID, &txID, &dir, &amount, &before, &after, &at); err != nil {
			return application.LedgerPage{}, err
		}
		a, err := shared.NewMoney(amount, currency)
		if err != nil {
			return application.LedgerPage{}, err
		}
		b, err := shared.NewMoney(before, currency)
		if err != nil {
			return application.LedgerPage{}, err
		}
		c, err := shared.NewMoney(after, currency)
		if err != nil {
			return application.LedgerPage{}, err
		}
		entry, err := wallet.RehydrateLedgerEntry(walletID, txID, wallet.Direction(dir), a, b, c, at)
		if err != nil {
			return application.LedgerPage{}, err
		}
		entries = append(entries, entry)
		entryIDs = append(entryIDs, entryID)
	}
	if err := rows.Err(); err != nil {
		return application.LedgerPage{}, err
	}
	page := application.LedgerPage{}
	if len(entries) > limit {
		entries = entries[:limit]
		last := entries[len(entries)-1]
		page.NextCursor = encodeCursor(last.CreatedAt(), entryIDs[limit-1])
	}
	page.Items = entries
	return page, nil
}
func (s ledgerStore) walletCurrency(ctx context.Context, id string) (string, error) {
	var cur string
	if err := s.tx.QueryRow(ctx, `SELECT currency FROM app.wallets WHERE id=$1`, id).Scan(&cur); err != nil {
		return "", translateError(err)
	}
	return strings.TrimSpace(cur), nil
}
func (s ledgerStore) Reconcile(ctx context.Context, walletID, currency string) (shared.Money, shared.Money, int64, error) {
	var storedText, sum string
	var count int64
	err := s.tx.QueryRow(ctx, `SELECT w.balance::text,COALESCE(SUM(CASE e.direction WHEN 'CREDIT' THEN e.amount ELSE -e.amount END),0)::text,COUNT(e.id) FROM app.wallets w LEFT JOIN app.wallet_ledger_entries e ON e.wallet_id=w.id WHERE w.id=$1 GROUP BY w.balance`, walletID).Scan(&storedText, &sum, &count)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return shared.Money{}, shared.Money{}, 0, application.ErrNotFound
		}
		return shared.Money{}, shared.Money{}, 0, translateError(err)
	}
	stored, err := shared.NewMoney(storedText, currency)
	if err != nil {
		return shared.Money{}, shared.Money{}, 0, err
	}
	calculated, err := shared.NewMoney(sum, currency)
	return stored, calculated, count, err
}

type outboxStore struct{ tx pgx.Tx }

func (s outboxStore) Append(ctx context.Context, events []application.Event) error {
	for _, e := range events {
		payload, err := json.Marshal(e.Data)
		if err != nil {
			return fmt.Errorf("marshal event %s: %w", e.Type, err)
		}
		_, err = s.tx.Exec(ctx, `INSERT INTO app.outbox_events(event_id,aggregate_id,event_type,event_version,correlation_id,occurred_at,payload) VALUES($1,$2,$3,1,$4,$5,$6::jsonb)`, e.ID, e.AggregateID, e.Type, e.CorrelationID, e.OccurredAt, payload)
		if err != nil {
			return translateError(err)
		}
	}
	return nil
}

func translateError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			if pgErr.ConstraintName == "wager_one_processed_reversal_idx" {
				return fmt.Errorf("%w: %s", shared.ErrDuplicateReversal, pgErr.ConstraintName)
			}
			return fmt.Errorf("%w: %s", application.ErrConflict, pgErr.ConstraintName)
		case "23503", "23514", "23502", "22P02":
			return fmt.Errorf("%w: %s", shared.ErrInvalidArgument, pgErr.Message)
		case "55000":
			return fmt.Errorf("%w: %s", shared.ErrInvalidTransition, pgErr.Message)
		}
	}
	return err
}
func terminal(s wagering.Status) bool {
	return s == wagering.Processed || s == wagering.Rejected || s == wagering.Failed
}
func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func encodeCursor(t time.Time, id string) string {
	data, _ := json.Marshal([]string{t.UTC().Format(time.RFC3339Nano), id})
	return base64.RawURLEncoding.EncodeToString(data)
}
func decodeCursor(s string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", err
	}
	var values []string
	if err = json.Unmarshal(raw, &values); err != nil || len(values) != 2 {
		return time.Time{}, "", errors.New("invalid cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, values[0])
	return at, values[1], err
}
