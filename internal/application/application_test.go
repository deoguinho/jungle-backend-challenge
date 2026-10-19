package application

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wagering"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

type testClock struct{ now time.Time }

func (c testClock) Now() time.Time { return c.now }

type testIDs struct{ n int }

func (g *testIDs) NewID() string { g.n++; return fmt.Sprintf("id-%d", g.n) }

type memUOW struct {
	tx       *memTx
	forceErr error
}

func (u *memUOW) Within(_ context.Context, fn func(Tx) error) error {
	if u.forceErr != nil {
		return u.forceErr
	}
	return fn(u.tx)
}

type memTx struct {
	wallets *memWallets
	txs     *memTransactions
	ledger  *memLedger
	outbox  *memOutbox
	inbox   *memInbox
}

func (t *memTx) Wallets() WalletStore           { return t.wallets }
func (t *memTx) Transactions() TransactionStore { return t.txs }
func (t *memTx) Ledger() LedgerStore            { return t.ledger }
func (t *memTx) Outbox() OutboxStore            { return t.outbox }
func (t *memTx) Inbox() InboxStore              { return t.inbox }

type memInbox struct {
	entries map[string]struct {
		hash      []byte
		completed bool
	}
}

func (s *memInbox) Start(_ context.Context, consumer, messageID string, hash []byte) (bool, error) {
	key := consumer + ":" + messageID
	entry, ok := s.entries[key]
	if !ok {
		s.entries[key] = struct {
			hash      []byte
			completed bool
		}{hash: append([]byte(nil), hash...)}
		return false, nil
	}
	if !bytes.Equal(entry.hash, hash) {
		return false, ErrIdempotencyConflict
	}
	return entry.completed, nil
}

func (s *memInbox) Complete(_ context.Context, consumer, messageID, _, _ string) error {
	key := consumer + ":" + messageID
	entry, ok := s.entries[key]
	if !ok {
		return ErrNotFound
	}
	entry.completed = true
	s.entries[key] = entry
	return nil
}

type memWallets struct {
	byID                              map[string]*wallet.Wallet
	byPlayer                          map[string]*wallet.Wallet
	ledger                            *memLedger
	lookupErr, createErr, movementErr error
}

func (s *memWallets) Create(_ context.Context, w *wallet.Wallet) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.byID[w.ID()] = w
	s.byPlayer[w.PlayerID()+":"+w.Currency()] = w
	return nil
}
func (s *memWallets) ByID(_ context.Context, id string) (*wallet.Wallet, error) {
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	w := s.byID[id]
	if w == nil {
		return nil, ErrNotFound
	}
	return w, nil
}
func (s *memWallets) ByPlayerCurrency(_ context.Context, player, currency string) (*wallet.Wallet, error) {
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	w := s.byPlayer[player+":"+currency]
	if w == nil {
		return nil, ErrNotFound
	}
	return w, nil
}
func (s *memWallets) ApplyMovement(_ context.Context, walletID, transactionID string, direction wallet.Direction, amount shared.Money, at time.Time) (MovementResult, error) {
	if s.movementErr != nil {
		return MovementResult{}, s.movementErr
	}
	w, err := s.ByID(context.Background(), walletID)
	if err != nil {
		return MovementResult{}, err
	}
	var entry *wallet.LedgerEntry
	if direction == wallet.CreditDirection {
		entry, err = w.Credit(transactionID, amount, at)
	} else {
		entry, err = w.Debit(transactionID, amount, at)
	}
	if err != nil {
		return MovementResult{}, err
	}
	if s.ledger != nil {
		s.ledger.entries = append(s.ledger.entries, entry)
	}
	return MovementResult{Entry: entry, Balance: w.Balance(), Version: w.Version()}, nil
}

type memTransactions struct {
	byID                                                                 map[string]*wagering.WagerTransaction
	createErr, saveErr, byIDErr, byKeyErr, byExternalErr, byReferenceErr error
	createWinner                                                         *wagering.WagerTransaction
}

func (s *memTransactions) Create(_ context.Context, t *wagering.WagerTransaction) error {
	if s.createErr != nil {
		if s.createWinner != nil {
			s.byID[s.createWinner.ID()] = s.createWinner
		}
		return s.createErr
	}
	s.byID[t.ID()] = t
	return nil
}
func (s *memTransactions) Save(_ context.Context, t *wagering.WagerTransaction) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.byID[t.ID()] = t
	return nil
}
func (s *memTransactions) ByID(_ context.Context, id string) (*wagering.WagerTransaction, error) {
	if s.byIDErr != nil {
		return nil, s.byIDErr
	}
	t := s.byID[id]
	if t == nil {
		return nil, ErrNotFound
	}
	return t, nil
}
func (s *memTransactions) ByProviderExternal(_ context.Context, provider, id string) (*wagering.WagerTransaction, error) {
	if s.byExternalErr != nil {
		return nil, s.byExternalErr
	}
	for _, t := range s.byID {
		if t.ProviderID() == provider && t.ExternalID() == id {
			return t, nil
		}
	}
	return nil, ErrNotFound
}
func (s *memTransactions) ByProviderIdempotency(_ context.Context, provider, key string) (*wagering.WagerTransaction, error) {
	if s.byKeyErr != nil {
		return nil, s.byKeyErr
	}
	for _, t := range s.byID {
		if t.ProviderID() == provider && t.IdempotencyKey() == key {
			return t, nil
		}
	}
	return nil, ErrNotFound
}
func (s *memTransactions) ByReference(_ context.Context, provider, id string) (*wagering.WagerTransaction, error) {
	if s.byReferenceErr != nil {
		return nil, s.byReferenceErr
	}
	return s.ByProviderExternal(context.Background(), provider, id)
}

type memLedger struct {
	entries                    []*wallet.LedgerEntry
	page                       LedgerPage
	listErr, sumErr, appendErr error
	stored, sum                shared.Money
	count                      int64
}

func (s *memLedger) Append(_ context.Context, e *wallet.LedgerEntry) error {
	if s.appendErr != nil {
		return s.appendErr
	}
	s.entries = append(s.entries, e)
	return nil
}
func (s *memLedger) List(context.Context, string, string, int) (LedgerPage, error) {
	return s.page, s.listErr
}
func (s *memLedger) Reconcile(context.Context, string, string) (shared.Money, shared.Money, int64, error) {
	return s.stored, s.sum, s.count, s.sumErr
}

type memOutbox struct {
	events []Event
	err    error
}

func (s *memOutbox) Append(_ context.Context, events []Event) error {
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, events...)
	return nil
}

type memCache struct {
	value          CachedIdempotency
	hit            bool
	getErr, putErr error
	puts           int
}

func (c *memCache) Get(context.Context, string) (CachedIdempotency, bool, error) {
	return c.value, c.hit, c.getErr
}
func (c *memCache) Put(context.Context, string, CachedIdempotency, time.Duration) error {
	c.puts++
	return c.putErr
}

func newFixture(t *testing.T, balance string) (*memUOW, *testIDs, testClock, shared.Money) {
	t.Helper()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	money, err := shared.NewMoney(balance, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	w, _, err := wallet.NewWallet("wallet-1", "player-1", money, "opening-1", now)
	if err != nil {
		t.Fatal(err)
	}
	ledger := &memLedger{}
	ws := &memWallets{byID: map[string]*wallet.Wallet{"wallet-1": w}, byPlayer: map[string]*wallet.Wallet{"player-1:BRL": w}, ledger: ledger}
	tx := &memTx{wallets: ws, txs: &memTransactions{byID: map[string]*wagering.WagerTransaction{}}, ledger: ledger, outbox: &memOutbox{}, inbox: &memInbox{entries: map[string]struct {
		hash      []byte
		completed bool
	}{}}}
	return &memUOW{tx: tx}, &testIDs{}, testClock{now}, money
}
func mustMoney(t *testing.T, amount, currency string) shared.Money {
	t.Helper()
	m, e := shared.NewMoney(amount, currency)
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func input(t *testing.T, kind wagering.Kind, amount string) SubmitWagerInput {
	t.Helper()
	m := mustMoney(t, amount, "BRL")
	return SubmitWagerInput{ExternalID: "external-1", IdempotencyKey: "key-1", PayloadHash: "hash-1", PlayerID: "player-1", WalletID: "wallet-1", RoundID: "round-1", GameID: "game-1", Kind: kind, Amount: m}
}
func sourceTx(t *testing.T, now time.Time, kind wagering.Kind, amount string, status wagering.Status) *wagering.WagerTransaction {
	t.Helper()
	m := mustMoney(t, amount, "BRL")
	p := wagering.ExternalParams{ID: "source-id", ProviderID: "provider-1", ExternalID: "source-external", IdempotencyKey: "source-key", PayloadHash: "source-hash", PlayerID: "player-1", WalletID: "wallet-1", RoundID: "round-1", GameID: "game-1", Kind: kind, Amount: m, CreatedAt: now}
	tx, e := wagering.NewExternal(p)
	if e != nil {
		t.Fatal(e)
	}
	if status == wagering.Processed {
		balance := mustMoney(t, "100.00", "BRL")
		if e = tx.MarkProcessed(&balance, now.Add(time.Second)); e != nil {
			t.Fatal(e)
		}
	}
	return tx
}
func addPending(t *testing.T, u *memUOW, now time.Time, kind wagering.Kind, amount, key string) *wagering.WagerTransaction {
	t.Helper()
	in := input(t, kind, amount)
	in.ExternalID = "reversal-" + key
	in.IdempotencyKey = key
	in.ReferenceExternalID = "source-external"
	params := wagering.ExternalParams{ID: "reversal-id-" + key, ProviderID: "provider-1", ExternalID: in.ExternalID, IdempotencyKey: in.IdempotencyKey, PayloadHash: in.PayloadHash, PlayerID: in.PlayerID, WalletID: in.WalletID, RoundID: in.RoundID, GameID: in.GameID, Kind: kind, Amount: in.Amount, ReferenceExternalID: in.ReferenceExternalID, CreatedAt: now}
	tx, e := wagering.NewExternal(params)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.MarkPendingReference(now); e != nil {
		t.Fatal(e)
	}
	u.tx.txs.byID[tx.ID()] = tx
	return tx
}

func TestOpenWalletPositiveZeroAndFailures(t *testing.T) {
	ctx := context.Background()
	u, ids, clock, _ := newFixture(t, "1.00")
	u.tx.wallets.byID = map[string]*wallet.Wallet{}
	u.tx.wallets.byPlayer = map[string]*wallet.Wallet{}
	uc := NewOpenWallet(u, ids, clock)
	out, err := uc.Execute(ctx, OpenWalletInput{PlayerID: "new-player", Initial: mustMoney(t, "25.00", "BRL")})
	if err != nil {
		t.Fatal(err)
	}
	if out.OpeningTransactionID == "" || out.Wallet.Balance.String() != "25.00" || len(u.tx.ledger.entries) != 1 || len(u.tx.outbox.events) != 2 {
		t.Fatalf("unexpected opening output: %+v", out)
	}
	zero, err := uc.Execute(ctx, OpenWalletInput{PlayerID: "zero-player", Initial: mustMoney(t, "0.00", "BRL")})
	if err != nil {
		t.Fatal(err)
	}
	if zero.OpeningTransactionID != "" || len(u.tx.ledger.entries) != 1 {
		t.Fatalf("zero opening created movement: %+v", zero)
	}
	if _, err = uc.Execute(ctx, OpenWalletInput{PlayerID: "new-player", Initial: mustMoney(t, "1.00", "BRL")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate player/currency conflict, got %v", err)
	}
	if _, err = uc.Execute(ctx, OpenWalletInput{Initial: mustMoney(t, "1.00", "BRL")}); err == nil {
		t.Fatal("expected invalid player input")
	}
	u.tx.wallets.lookupErr = errors.New("read failed")
	if _, err = uc.Execute(ctx, OpenWalletInput{PlayerID: "other", Initial: mustMoney(t, "1.00", "BRL")}); err == nil {
		t.Fatal("expected lookup error")
	}
	u.tx.wallets.lookupErr = nil
	u.tx.wallets.createErr = errors.New("write failed")
	if _, err = uc.Execute(ctx, OpenWalletInput{PlayerID: "other", Initial: mustMoney(t, "1.00", "BRL")}); err == nil {
		t.Fatal("expected create error")
	}
}

func TestWalletReadLedgerAndReconciliationUseCases(t *testing.T) {
	ctx := context.Background()
	u, _, _, balance := newFixture(t, "10.00")
	player := Principal{PlayerID: "player-1"}
	other := Principal{PlayerID: "another"}
	internal := Principal{Internal: true}
	get := NewGetWallet(u)
	if _, e := get.Execute(ctx, player, ""); e == nil {
		t.Fatal("expected empty id error")
	}
	if _, e := get.Execute(ctx, other, "wallet-1"); !errors.Is(e, ErrUnauthorized) {
		t.Fatalf("expected owner check, got %v", e)
	}
	view, e := get.Execute(ctx, internal, "wallet-1")
	if e != nil || view.Balance.String() != balance.String() {
		t.Fatalf("get wallet: %+v %v", view, e)
	}
	u.tx.wallets.lookupErr = errors.New("wallet read failed")
	if _, e = get.Execute(ctx, internal, "wallet-1"); e == nil {
		t.Fatal("expected wallet lookup error")
	}
	u.tx.wallets.lookupErr = nil
	list := NewListWalletLedger(u)
	if _, e = list.Execute(ctx, player, "", "", 1); e == nil {
		t.Fatal("expected invalid list input")
	}
	u.tx.ledger.page = LedgerPage{NextCursor: "next"}
	page, e := list.Execute(ctx, player, "wallet-1", "", 25)
	if e != nil || page.NextCursor != "next" {
		t.Fatalf("list ledger: %+v %v", page, e)
	}
	if _, e = list.Execute(ctx, other, "wallet-1", "", 25); !errors.Is(e, ErrUnauthorized) {
		t.Fatalf("expected ledger owner check: %v", e)
	}
	u.tx.ledger.listErr = errors.New("list failed")
	if _, e = list.Execute(ctx, player, "wallet-1", "", 25); e == nil {
		t.Fatal("expected ledger read error")
	}
	u.tx.ledger.listErr = nil
	reconcile := NewReconcileWallet(u)
	u.tx.ledger.stored = balance
	u.tx.ledger.sum = balance
	u.tx.ledger.count = 4
	r, e := reconcile.Execute(ctx, internal, "wallet-1")
	if e != nil || !r.Consistent || r.Entries != 4 {
		t.Fatalf("reconcile equal: %+v %v", r, e)
	}
	u.tx.ledger.sum = mustMoney(t, "8.00", "BRL")
	u.tx.ledger.stored = mustMoney(t, "10.00", "BRL")
	r, e = reconcile.Execute(ctx, player, "wallet-1")
	if e != nil || r.Consistent || r.Difference.String() != "2.00" {
		t.Fatalf("reconcile mismatch: %+v %v", r, e)
	}
	if _, e = reconcile.Execute(ctx, other, "wallet-1"); !errors.Is(e, ErrUnauthorized) {
		t.Fatalf("expected reconciliation owner check: %v", e)
	}
	u.tx.ledger.sumErr = errors.New("sum failed")
	if _, e = reconcile.Execute(ctx, internal, "wallet-1"); e == nil {
		t.Fatal("expected sum error")
	}
	u.tx.ledger.sumErr = nil
	u.tx.ledger.sum = mustMoney(t, "1.00", "USD")
	u.tx.ledger.stored = balance
	if _, e = reconcile.Execute(ctx, internal, "wallet-1"); e == nil {
		t.Fatal("expected currency mismatch from ledger")
	}
}

func TestCanonicalPayloadHashNormalizesAndDistinguishesBusinessFields(t *testing.T) {
	in := input(t, wagering.Bet, "1.0")
	a, e := CanonicalPayloadHash("provider-1", in)
	if e != nil {
		t.Fatal(e)
	}
	in.Amount = mustMoney(t, "1.00", "BRL")
	b, e := CanonicalPayloadHash("provider-1", in)
	if e != nil || a != b {
		t.Fatalf("equivalent money must hash equally: %s %s %v", a, b, e)
	}
	in.ExternalID = "another"
	c, e := CanonicalPayloadHash("provider-1", in)
	if e != nil || a == c {
		t.Fatalf("changed business value must change hash: %s %s %v", a, c, e)
	}
}

func TestMovementForOpeningHasNoExternalWalletMovement(t *testing.T) {
	amount := mustMoney(t, "1.00", "BRL")
	opening, err := wagering.NewOpening("opening", "player-1", "wallet-1", amount, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	direction, changes := movementFor(opening, nil)
	if direction != "" || changes {
		t.Fatalf("opening should not create an external movement, got %q %v", direction, changes)
	}
}

func TestSubmitWagerProcessesReplayCacheAndBusinessOutcomes(t *testing.T) {
	ctx := context.Background()
	u, ids, clock, _ := newFixture(t, "100.00")
	cache := &memCache{}
	uc := NewSubmitWager(u, ids, clock, cache, time.Hour)
	principal := Principal{ProviderID: "provider-1"}
	out, e := uc.Execute(ctx, principal, input(t, wagering.Bet, "25.00"))
	if e != nil || out.Transaction.Status() != wagering.Processed || out.Balance.String() != "75.00" {
		t.Fatalf("processed wager: %+v %v", out, e)
	}
	if len(u.tx.ledger.entries) != 1 || cache.puts != 1 {
		t.Fatalf("movement/cache not recorded: ledger=%d puts=%d", len(u.tx.ledger.entries), cache.puts)
	}
	replay, e := uc.Execute(ctx, principal, input(t, wagering.Bet, "25.00"))
	if e != nil || !replay.Replayed || replay.Balance.String() != "75.00" {
		t.Fatalf("database replay: %+v %v", replay, e)
	}
	cache.hit = true
	cache.value = CachedIdempotency{TransactionID: out.Transaction.ID(), PayloadHash: "hash-1"}
	replay, e = uc.Execute(ctx, principal, input(t, wagering.Bet, "25.00"))
	if e != nil || !replay.Replayed {
		t.Fatalf("cache replay: %+v %v", replay, e)
	}
	cache.value.PayloadHash = "different"
	conflict := input(t, wagering.Bet, "25.00")
	conflict.PayloadHash = "changed"
	if _, e = uc.Execute(ctx, principal, conflict); !errors.Is(e, ErrIdempotencyConflict) {
		t.Fatalf("expected cache payload conflict, got %v", e)
	}
	cache.hit = false
	if _, e = uc.Execute(ctx, principal, conflict); !errors.Is(e, ErrIdempotencyConflict) {
		t.Fatalf("expected DB payload conflict, got %v", e)
	}
	duplicate := input(t, wagering.Bet, "25.00")
	duplicate.IdempotencyKey = "new-key"
	duplicate.PayloadHash = "new-hash"
	if _, e = uc.Execute(ctx, principal, duplicate); !errors.Is(e, ErrConflict) {
		t.Fatalf("expected external id conflict, got %v", e)
	}
	if got := len(u.tx.ledger.entries); got != 1 {
		t.Fatalf("replays changed ledger: %d", got)
	}
}

type recordingWagerObserver struct {
	statuses                     []string
	replays, conflicts, failures int
	durations                    []time.Duration
}

func (o *recordingWagerObserver) WagerResult(status string, replay bool, conflict bool, err error) {
	o.statuses = append(o.statuses, status)
	if replay {
		o.replays++
	}
	if conflict {
		o.conflicts++
	}
	if err != nil {
		o.failures++
	}
}
func (o *recordingWagerObserver) ObserveWagerDuration(d time.Duration) {
	o.durations = append(o.durations, d)
}

func TestSubmitWagerReportsMetricsObserverOutcomes(t *testing.T) {
	ctx := context.Background()
	u, ids, clock, _ := newFixture(t, "100.00")
	observer := &recordingWagerObserver{}
	uc := NewSubmitWager(u, ids, clock, nil, 0, observer)
	in := input(t, wagering.Bet, "5.00")
	principal := Principal{ProviderID: "provider-1"}
	if _, err := uc.Execute(ctx, principal, in); err != nil {
		t.Fatal(err)
	}
	if out, err := uc.Execute(ctx, principal, in); err != nil || !out.Replayed {
		t.Fatalf("replay: out=%+v err=%v", out, err)
	}
	changed := in
	changed.PayloadHash = "changed-payload"
	if _, err := uc.Execute(ctx, principal, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
	if len(observer.statuses) != 3 || observer.replays != 1 || observer.conflicts != 1 || observer.failures != 1 || len(observer.durations) != 3 {
		t.Fatalf("unexpected observer measurements: %+v", observer)
	}
}

func TestConsumeWagerMessageCommitsInboxWithDurableResult(t *testing.T) {
	ctx := context.Background()
	u, ids, clock, _ := newFixture(t, "20.00")
	in := input(t, wagering.Bet, "5.00")
	hash := make([]byte, 32)
	hash[0] = 7
	delivery := InboxDelivery{Consumer: "wager-consumer", MessageID: "message-1", PayloadHash: hash}
	uc := NewSubmitWager(u, ids, clock, nil, 0)

	first, err := uc.ConsumeWagerMessage(ctx, "provider-1", in, delivery)
	if err != nil || first.Transaction.Status() != wagering.Processed || first.Balance.String() != "15.00" {
		t.Fatalf("first message result: %+v %v", first, err)
	}
	replay, err := uc.ConsumeWagerMessage(ctx, "provider-1", in, delivery)
	if err != nil || !replay.Replayed || replay.Transaction.ID() != first.Transaction.ID() {
		t.Fatalf("redelivery should replay completed message: %+v %v", replay, err)
	}
	if len(u.tx.ledger.entries) != 1 {
		t.Fatalf("message replay moved balance more than once: ledger=%d", len(u.tx.ledger.entries))
	}
	changed := delivery
	changed.PayloadHash = append([]byte(nil), hash...)
	changed.PayloadHash[0]++
	if _, err = uc.ConsumeWagerMessage(ctx, "provider-1", in, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed inbox payload should conflict: %v", err)
	}
}

func TestSubmitWagerPendingRejectedAndInvalidFlows(t *testing.T) {
	ctx := context.Background()
	u, ids, clock, _ := newFixture(t, "5.00")
	uc := NewSubmitWager(u, ids, clock, nil, 0)
	p := Principal{ProviderID: "provider-1"}
	pending := input(t, wagering.Refund, "5.00")
	pending.ReferenceExternalID = "missing-ref"
	out, e := uc.Execute(ctx, p, pending)
	if e != nil || !out.Pending || out.Transaction.Status() != wagering.PendingReference {
		t.Fatalf("expected pending reference: %+v %v", out, e)
	}
	badRef := input(t, wagering.Refund, "5.00")
	badRef.ExternalID = "refund-invalid"
	badRef.IdempotencyKey = "refund-invalid-key"
	badRef.PayloadHash = "refund-invalid-hash"
	badRef.ReferenceExternalID = "source-external"
	u.tx.txs.byID["source-id"] = sourceTx(t, clock.now, wagering.Win, "5.00", wagering.Processed)
	out, e = uc.Execute(ctx, p, badRef)
	if e != nil || out.Transaction.Status() != wagering.Rejected || out.Transaction.FailureCode() != "REFERENCE_INVALID" {
		t.Fatalf("expected invalid reference rejection: %+v %v", out, e)
	}
	insufficient := input(t, wagering.Bet, "10.00")
	insufficient.ExternalID = "bet-too-large"
	insufficient.IdempotencyKey = "bet-too-large-key"
	insufficient.PayloadHash = "too-large-hash"
	out, e = uc.Execute(ctx, p, insufficient)
	if e != nil || out.Transaction.FailureCode() != "INSUFFICIENT_FUNDS" || len(u.tx.ledger.entries) != 0 {
		t.Fatalf("expected insufficient funds result: %+v %v", out, e)
	}
	loss := input(t, wagering.Loss, "0.00")
	loss.ExternalID = "loss"
	loss.IdempotencyKey = "loss-key"
	loss.PayloadHash = "loss-hash"
	out, e = uc.Execute(ctx, p, loss)
	if e != nil || out.Transaction.Status() != wagering.Processed || out.Balance.String() != "5.00" || len(u.tx.ledger.entries) != 0 {
		t.Fatalf("loss should not move balance: %+v %v", out, e)
	}
	if _, e = uc.Execute(ctx, Principal{}, input(t, wagering.Bet, "1.00")); !errors.Is(e, ErrUnauthorized) {
		t.Fatalf("expected unauthorized principal: %v", e)
	}
	if _, e = uc.Execute(ctx, Principal{ProviderID: "provider-1", PlayerID: "other"}, input(t, wagering.Bet, "1.00")); !errors.Is(e, ErrUnauthorized) {
		t.Fatalf("expected player authorization error: %v", e)
	}
	invalid := input(t, wagering.Bet, "1.00")
	invalid.PayloadHash = ""
	if _, e = uc.Execute(ctx, p, invalid); e == nil {
		t.Fatal("expected invalid input")
	}
	wrongWallet := input(t, wagering.Bet, "1.00")
	wrongWallet.PlayerID = "other"
	wrongWallet.ExternalID = "other-player"
	wrongWallet.IdempotencyKey = "other-player-key"
	wrongWallet.PayloadHash = "other-hash"
	if _, e = uc.Execute(ctx, p, wrongWallet); e == nil {
		t.Fatal("expected wallet context mismatch")
	}
	cache := &memCache{getErr: errors.New("redis down"), putErr: errors.New("redis down")}
	fallback := NewSubmitWager(u, ids, clock, cache, time.Hour)
	newBet := input(t, wagering.Bet, "1.00")
	newBet.ExternalID = "cache-fallback"
	newBet.IdempotencyKey = "cache-fallback"
	newBet.PayloadHash = "cache-fallback"
	if _, e = fallback.Execute(ctx, p, newBet); e != nil {
		t.Fatalf("cache failure must fall back to DB: %v", e)
	}
}

func TestSubmitWagerReversalsAndUseCaseFailures(t *testing.T) {
	ctx := context.Background()
	p := Principal{ProviderID: "provider-1"}
	t.Run("refund of processed bet", func(t *testing.T) {
		u, ids, clock, _ := newFixture(t, "10.00")
		source := sourceTx(t, clock.now, wagering.Bet, "5.00", wagering.Processed)
		u.tx.txs.byID[source.ID()] = source
		in := input(t, wagering.Refund, "5.00")
		in.ExternalID, in.IdempotencyKey, in.PayloadHash, in.ReferenceExternalID = "refund", "refund-key", "refund-hash", source.ExternalID()
		out, err := NewSubmitWager(u, ids, clock, nil, 0).Execute(ctx, p, in)
		if err != nil || out.Transaction.Status() != wagering.Processed || out.Balance.String() != "15.00" {
			t.Fatalf("refund result: %+v %v", out, err)
		}
	})
	t.Run("duplicate reversal rejected", func(t *testing.T) {
		u, ids, clock, _ := newFixture(t, "10.00")
		source := sourceTx(t, clock.now, wagering.Bet, "5.00", wagering.Processed)
		if err := source.MarkReversed(clock.now.Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		u.tx.txs.byID[source.ID()] = source
		in := input(t, wagering.Refund, "5.00")
		in.ExternalID, in.IdempotencyKey, in.PayloadHash, in.ReferenceExternalID = "refund", "refund-key", "refund-hash", source.ExternalID()
		out, err := NewSubmitWager(u, ids, clock, nil, 0).Execute(ctx, p, in)
		if err != nil || out.Transaction.FailureCode() != "REFERENCE_INVALID" {
			t.Fatalf("duplicate reversal result: %+v %v", out, err)
		}
	})
	t.Run("context mismatch is rejected before create", func(t *testing.T) {
		u, ids, clock, _ := newFixture(t, "10.00")
		in := input(t, wagering.Bet, "1.00")
		in.ExternalID, in.IdempotencyKey, in.PayloadHash = "bad", "bad-key", "bad-hash"
		// A valid Money in another currency reaches the wallet context check.
		in.Amount = mustMoney(t, "1.00", "USD")
		if _, err := NewSubmitWager(u, ids, clock, nil, 0).Execute(ctx, p, in); err == nil {
			t.Fatal("expected currency mismatch")
		}
	})
	tests := []struct {
		name      string
		configure func(*memUOW)
		in        SubmitWagerInput
	}{
		{"idempotency lookup error", func(u *memUOW) { u.tx.txs.byKeyErr = errors.New("db") }, input(t, wagering.Bet, "1.00")},
		{"external lookup error", func(u *memUOW) { u.tx.txs.byExternalErr = errors.New("db") }, input(t, wagering.Bet, "1.00")},
		{"wallet lookup error", func(u *memUOW) { u.tx.wallets.lookupErr = errors.New("db") }, input(t, wagering.Bet, "1.00")},
		{"create error", func(u *memUOW) { u.tx.txs.createErr = errors.New("db") }, input(t, wagering.Bet, "1.00")},
		{"movement error", func(u *memUOW) { u.tx.wallets.movementErr = errors.New("db") }, input(t, wagering.Bet, "1.00")},
		{"save error", func(u *memUOW) { u.tx.txs.saveErr = errors.New("db") }, input(t, wagering.Loss, "0.00")},
		{"outbox error", func(u *memUOW) { u.tx.outbox.err = errors.New("db") }, input(t, wagering.Loss, "0.00")},
		{"reference lookup error", func(u *memUOW) { u.tx.txs.byReferenceErr = errors.New("db") }, func() SubmitWagerInput {
			x := input(t, wagering.Refund, "1.00")
			x.ReferenceExternalID = "source-external"
			return x
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, ids, clock, _ := newFixture(t, "10.00")
			tc.configure(u)
			tc.in.ExternalID = "id-" + tc.name
			tc.in.IdempotencyKey = "key-" + tc.name
			tc.in.PayloadHash = "hash-" + tc.name
			if _, err := NewSubmitWager(u, ids, clock, nil, 0).Execute(ctx, p, tc.in); err == nil {
				t.Fatal("expected port error")
			}
		})
	}
	t.Run("cache hit whose row is unavailable falls back to authoritative key query", func(t *testing.T) {
		u, ids, clock, _ := newFixture(t, "10.00")
		cache := &memCache{hit: true, value: CachedIdempotency{TransactionID: "missing"}}
		in := input(t, wagering.Loss, "0.00")
		out, err := NewSubmitWager(u, ids, clock, cache, time.Hour).Execute(ctx, p, in)
		if err != nil || out.Transaction.Status() != wagering.Processed {
			t.Fatalf("fallback result: %+v %v", out, err)
		}
	})
	t.Run("concurrent insert resolves to durable winner", func(t *testing.T) {
		u, ids, clock, _ := newFixture(t, "10.00")
		in := input(t, wagering.Loss, "0.00")
		winner, err := wagering.NewExternal(wagering.ExternalParams{ID: "winner", ProviderID: "provider-1", ExternalID: in.ExternalID, IdempotencyKey: in.IdempotencyKey, PayloadHash: in.PayloadHash, PlayerID: in.PlayerID, WalletID: in.WalletID, RoundID: in.RoundID, GameID: in.GameID, Kind: in.Kind, Amount: in.Amount, CreatedAt: clock.now})
		if err != nil {
			t.Fatal(err)
		}
		if err = winner.MarkProcessed(&in.Amount, clock.now); err != nil {
			t.Fatal(err)
		}
		u.tx.txs.createErr = ErrConflict
		u.tx.txs.createWinner = winner
		out, err := NewSubmitWager(u, ids, clock, nil, 0).Execute(ctx, p, in)
		if err != nil || !out.Replayed || out.Transaction.ID() != "winner" {
			t.Fatalf("concurrent winner result: %+v %v", out, err)
		}
	})
	t.Run("worker entry point uses trusted provider", func(t *testing.T) {
		u, ids, clock, _ := newFixture(t, "10.00")
		in := input(t, wagering.Loss, "0.00")
		in.PlayerID = "player-1"
		hash, err := CanonicalPayloadHash("provider-worker", in)
		if err != nil {
			t.Fatal(err)
		}
		in.PayloadHash = hash
		out, err := NewSubmitWager(u, ids, clock, nil, 0).ConsumeWagerTransaction(ctx, "provider-worker", in)
		if err != nil || out.Transaction.ProviderID() != "provider-worker" {
			t.Fatalf("worker submit: %+v %v", out, err)
		}
	})
}

func TestApplicationPortErrorsAndExpiryErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("open wallet writes transaction ledger and outbox errors", func(t *testing.T) {
		for _, stage := range []string{"transaction", "ledger", "outbox"} {
			t.Run(stage, func(t *testing.T) {
				u, ids, clock, _ := newFixture(t, "1.00")
				u.tx.wallets.byID = map[string]*wallet.Wallet{}
				u.tx.wallets.byPlayer = map[string]*wallet.Wallet{}
				failure := errors.New("write failed")
				switch stage {
				case "transaction":
					u.tx.txs.createErr = failure
				case "ledger":
					u.tx.ledger.appendErr = failure
				case "outbox":
					u.tx.outbox.err = failure
				}
				_, err := NewOpenWallet(u, ids, clock).Execute(ctx, OpenWalletInput{PlayerID: "fresh", Initial: mustMoney(t, "2.00", "BRL")})
				if err == nil {
					t.Fatal("expected write failure")
				}
			})
		}
	})
	t.Run("expiry save and outbox failure", func(t *testing.T) {
		for _, stage := range []string{"save", "outbox"} {
			t.Run(stage, func(t *testing.T) {
				u, ids, clock, _ := newFixture(t, "1.00")
				_ = addPending(t, u, clock.now, wagering.Refund, "1.00", "expire-key")
				failure := errors.New("write failed")
				if stage == "save" {
					u.tx.txs.saveErr = failure
				} else {
					u.tx.outbox.err = failure
				}
				err := NewExpirePendingReference(u, ids, clock).Execute(ctx, "provider-1", "expire-key")
				if err == nil {
					t.Fatal("expected persistence error")
				}
			})
		}
	})
	u, ids, clock, _ := newFixture(t, "1.00")
	if _, err := NewOpenWallet(u, ids, clock).Execute(ctx, OpenWalletInput{PlayerID: "p"}); err == nil {
		t.Fatal("expected empty money input error")
	}
}

func TestRetryAndExpirePendingReference(t *testing.T) {
	ctx := context.Background()
	u, ids, clock, _ := newFixture(t, "100.00")
	submit := NewSubmitWager(u, ids, clock, nil, 0)
	retry := NewRetryPendingReference(submit)
	if _, e := retry.Execute(ctx, "", "key"); e == nil {
		t.Fatal("expected invalid retry input")
	}
	_ = addPending(t, u, clock.now, wagering.Refund, "20.00", "reversal-key")
	result, e := retry.Execute(ctx, "provider-1", "reversal-key")
	if e != nil || !result.Pending {
		t.Fatalf("missing source should remain pending: %+v %v", result, e)
	}
	if _, e := retry.Execute(ctx, "provider-1", "missing-key"); !errors.Is(e, ErrNotFound) {
		t.Fatalf("expected missing transaction error, got %v", e)
	}
	u.tx.txs.byID["source-id"] = sourceTx(t, clock.now, wagering.Bet, "20.00", wagering.Processed)
	result, e = retry.Execute(ctx, "provider-1", "reversal-key")
	if e != nil || result.Transaction.Status() != wagering.Processed || result.Balance.String() != "120.00" {
		t.Fatalf("reference retry processing: %+v %v", result, e)
	}
	if _, e = retry.Execute(ctx, "provider-1", "reversal-key"); e == nil {
		t.Fatal("terminal transaction must not be retried")
	}

	_ = addPending(t, u, clock.now, wagering.Rollback, "20.00", "expire-key")
	expire := NewExpirePendingReference(u, ids, clock)
	if e = expire.Execute(ctx, "provider-1", "missing-key"); !errors.Is(e, ErrNotFound) {
		t.Fatalf("expected expire lookup error: %v", e)
	}
	if e = expire.Execute(ctx, "provider-1", "reversal-key"); e != nil {
		t.Fatalf("terminal expiration should be a no-op: %v", e)
	}
	if e = expire.Execute(ctx, "provider-1", "expire-key"); e != nil {
		t.Fatal(e)
	}
	expired, e := u.tx.txs.ByProviderIdempotency(ctx, "provider-1", "expire-key")
	if e != nil || expired.Status() != wagering.Rejected || expired.FailureCode() != "REFERENCE_NOT_FOUND" {
		t.Fatalf("pending expiry should reject durably: %+v %v", expired, e)
	}
}

func TestRetryPendingReferenceErrorsAndTerminalOutcomes(t *testing.T) {
	ctx := context.Background()
	newPending := func(t *testing.T, sourceKind wagering.Kind, sourceAmount string, walletBalance string) (*memUOW, *SubmitWager, testClock) {
		u, ids, clock, _ := newFixture(t, walletBalance)
		_ = addPending(t, u, clock.now, wagering.Refund, sourceAmount, "retry-key")
		u.tx.txs.byID["source-id"] = sourceTx(t, clock.now, sourceKind, sourceAmount, wagering.Processed)
		return u, NewSubmitWager(u, ids, clock, nil, 0), clock
	}
	t.Run("lookup errors and non pending", func(t *testing.T) {
		u, submit, _ := newPending(t, wagering.Bet, "5.00", "10.00")
		retry := NewRetryPendingReference(submit)
		u.tx.txs.byKeyErr = errors.New("lookup")
		if _, err := retry.Execute(ctx, "provider-1", "retry-key"); err == nil {
			t.Fatal("expected lookup error")
		}
		u.tx.txs.byKeyErr = nil
		if _, err := retry.Execute(ctx, "provider-1", "source-key"); err == nil {
			t.Fatal("expected non pending error")
		}
	})
	t.Run("reference lookup error", func(t *testing.T) {
		u, submit, _ := newPending(t, wagering.Bet, "5.00", "10.00")
		u.tx.txs.byReferenceErr = errors.New("reference lookup")
		if _, err := NewRetryPendingReference(submit).Execute(ctx, "provider-1", "retry-key"); err == nil {
			t.Fatal("expected reference lookup error")
		}
	})
	t.Run("invalid reference rejects and emits outbox", func(t *testing.T) {
		u, submit, _ := newPending(t, wagering.Win, "5.00", "10.00")
		result, err := NewRetryPendingReference(submit).Execute(ctx, "provider-1", "retry-key")
		if err != nil || result.Transaction.Status() != wagering.Rejected || result.Transaction.FailureCode() != "REFERENCE_INVALID" || len(u.tx.outbox.events) != 1 {
			t.Fatalf("invalid reference result: %+v %v", result, err)
		}
	})
	t.Run("invalid reference save and outbox errors", func(t *testing.T) {
		for _, stage := range []string{"save", "outbox"} {
			t.Run(stage, func(t *testing.T) {
				u, submit, _ := newPending(t, wagering.Win, "5.00", "10.00")
				if stage == "save" {
					u.tx.txs.saveErr = errors.New("save")
				} else {
					u.tx.outbox.err = errors.New("outbox")
				}
				if _, err := NewRetryPendingReference(submit).Execute(ctx, "provider-1", "retry-key"); err == nil {
					t.Fatal("expected persistence error")
				}
			})
		}
	})
	t.Run("wallet lookup error", func(t *testing.T) {
		u, submit, _ := newPending(t, wagering.Bet, "5.00", "10.00")
		u.tx.wallets.lookupErr = errors.New("wallet")
		if _, err := NewRetryPendingReference(submit).Execute(ctx, "provider-1", "retry-key"); err == nil {
			t.Fatal("expected wallet error")
		}
	})
	t.Run("insufficient rollback balance rejects with reversal code", func(t *testing.T) {
		u, ids, clock, _ := newFixture(t, "10.00")
		_ = addPending(t, u, clock.now, wagering.Rollback, "20.00", "retry-key")
		u.tx.txs.byID["source-id"] = sourceTx(t, clock.now, wagering.Win, "20.00", wagering.Processed)
		result, err := NewRetryPendingReference(NewSubmitWager(u, ids, clock, nil, 0)).Execute(ctx, "provider-1", "retry-key")
		if err != nil || result.Transaction.FailureCode() != "REVERSAL_INSUFFICIENT_FUNDS" {
			t.Fatalf("reversal rejection: %+v %v", result, err)
		}
	})
	t.Run("movement, save and outbox errors", func(t *testing.T) {
		for _, stage := range []string{"movement", "save", "outbox"} {
			t.Run(stage, func(t *testing.T) {
				u, submit, _ := newPending(t, wagering.Bet, "5.00", "10.00")
				if stage == "movement" {
					u.tx.wallets.movementErr = errors.New(stage)
				}
				if stage == "save" {
					u.tx.txs.saveErr = errors.New(stage)
				}
				if stage == "outbox" {
					u.tx.outbox.err = errors.New(stage)
				}
				if _, err := NewRetryPendingReference(submit).Execute(ctx, "provider-1", "retry-key"); err == nil {
					t.Fatal("expected retry error")
				}
			})
		}
	})
}

func TestReadWagerAuthorizationAndEventPayloads(t *testing.T) {
	ctx := context.Background()
	u, _, clock, _ := newFixture(t, "10.00")
	source := sourceTx(t, clock.now, wagering.Bet, "1.00", wagering.Processed)
	u.tx.txs.byID[source.ID()] = source
	get := NewGetWagerTransaction(u)
	if _, e := get.Execute(ctx, Principal{}, ""); e == nil {
		t.Fatal("expected empty transaction ID error")
	}
	if _, e := get.Execute(ctx, Principal{ProviderID: "other"}, source.ID()); !errors.Is(e, ErrUnauthorized) {
		t.Fatalf("expected provider isolation: %v", e)
	}
	if _, e := get.Execute(ctx, Principal{ProviderID: "provider-1"}, source.ID()); e != nil {
		t.Fatal(e)
	}
	providerGet := NewGetProviderWagerTransaction(u)
	if _, e := providerGet.Execute(ctx, Principal{}, "external"); e == nil {
		t.Fatal("expected invalid provider query")
	}
	if _, e := providerGet.Execute(ctx, Principal{ProviderID: "provider-1"}, "source-external"); e != nil {
		t.Fatal(e)
	}
	data := wagerEventData(source)
	if data["transactionId"] != source.ID() || data["status"] != wagering.Processed {
		t.Fatalf("unexpected transaction event data: %#v", data)
	}
	entry, err := wallet.RehydrateLedgerEntry("wallet-1", "source-id", wallet.CreditDirection, mustMoney(t, "1.00", "BRL"), mustMoney(t, "0.00", "BRL"), mustMoney(t, "1.00", "BRL"), clock.now)
	if err != nil {
		t.Fatal(err)
	}
	balanceData := walletBalanceEventData(entry, 2)
	if balanceData["walletVersion"] != int64(2) || balanceData["transactionId"] != "source-id" {
		t.Fatalf("unexpected wallet event data: %#v", balanceData)
	}
}
