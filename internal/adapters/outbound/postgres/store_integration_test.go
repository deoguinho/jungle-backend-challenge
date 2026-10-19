package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/application"
	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wagering"
)

// These tests require a disposable PostgreSQL database with migrations 001-009
// already applied. Set TEST_DATABASE_URL to opt in; no schema is dropped.
func integrationDB(t *testing.T) (*DB, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	db, err := New(ctx, url)
	if err != nil {
		t.Fatalf("connect to integration PostgreSQL: %v", err)
	}
	t.Cleanup(db.Close)
	return db, ctx
}

func independentIntegrationDB(t *testing.T, ctx context.Context) *DB {
	t.Helper()
	db, err := New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("open independent PostgreSQL pool: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

type integrationClock struct{ now time.Time }

func (c integrationClock) Now() time.Time { return c.now }

type integrationIDs struct{}

func (integrationIDs) NewID() string { return uuid.NewString() }

func openIntegrationWallet(t *testing.T, ctx context.Context, db *DB, balance string) (string, string) {
	t.Helper()
	playerID := uuid.NewString()
	amount, err := shared.NewMoney(balance, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	out, err := application.NewOpenWallet(db, integrationIDs{}, integrationClock{time.Now().UTC()}).Execute(ctx, application.OpenWalletInput{PlayerID: playerID, Initial: amount})
	if err != nil {
		t.Fatalf("open integration wallet: %v", err)
	}
	return out.Wallet.ID, playerID
}

func newIntegrationBet(t *testing.T, providerID, walletID, playerID, key, externalID, amount string) application.SubmitWagerInput {
	t.Helper()
	money, err := shared.NewMoney(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	in := application.SubmitWagerInput{ExternalID: externalID, IdempotencyKey: key, PlayerID: playerID, WalletID: walletID, RoundID: uuid.NewString(), GameID: "integration", Kind: wagering.Bet, Amount: money}
	in.PayloadHash, err = application.CanonicalPayloadHash(providerID, in)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestConcurrentWalletMovementsPreserveAtomicBalanceAndLedger(t *testing.T) {
	db, ctx := integrationDB(t)
	otherDB := independentIntegrationDB(t, ctx)
	walletID, playerID := openIntegrationWallet(t, ctx, db, "100.00")
	useCases := []*application.SubmitWager{
		application.NewSubmitWager(db, integrationIDs{}, integrationClock{time.Now().UTC()}, nil, 0),
		application.NewSubmitWager(otherDB, integrationIDs{}, integrationClock{time.Now().UTC()}, nil, 0),
	}
	providerID := uuid.NewString()

	var processed, rejected atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := newIntegrationBet(t, providerID, walletID, playerID, fmt.Sprintf("key-%d", i), fmt.Sprintf("external-%d", i), "80.00")
			result, err := useCases[i].Execute(ctx, application.Principal{ProviderID: providerID}, in)
			if err != nil {
				t.Errorf("submit concurrent bet %d: %v", i, err)
				return
			}
			if result.Transaction.Status() == wagering.Processed {
				processed.Add(1)
			} else if result.Transaction.Status() == wagering.Rejected && result.Transaction.FailureCode() == "INSUFFICIENT_FUNDS" {
				rejected.Add(1)
			} else {
				t.Errorf("unexpected concurrent bet result: status=%s failure=%s", result.Transaction.Status(), result.Transaction.FailureCode())
			}
		}(i)
	}
	wg.Wait()
	if processed.Load() != 1 || rejected.Load() != 1 {
		t.Fatalf("want one processed and one rejected bet; got processed=%d rejected=%d", processed.Load(), rejected.Load())
	}

	var balance string
	var version, ledgerCount int64
	if err := db.pool.QueryRow(ctx, `SELECT balance::text,version FROM app.wallets WHERE id=$1`, walletID).Scan(&balance, &version); err != nil {
		t.Fatal(err)
	}
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM app.wallet_ledger_entries WHERE wallet_id=$1`, walletID).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if balance != "20.00" || version != 2 || ledgerCount != 2 { // opening credit plus one bet debit
		t.Fatalf("lost update or inconsistent ledger: balance=%s version=%d ledger=%d", balance, version, ledgerCount)
	}
}

type processWager struct{ ProviderID, WalletID, PlayerID, Key, ExternalID, Amount, RoundID string }
type processInbox struct {
	Wager       processWager `json:"wager"`
	Consumer    string       `json:"consumer"`
	MessageID   string       `json:"messageId"`
	PayloadHash []byte       `json:"payloadHash"`
}

// Each invocation below runs in a separate test process with a fresh DB pool,
// proving that correctness does not depend on shared Go memory or one pool.
func TestThreeIndependentProcessesProtectWalletBalances(t *testing.T) {
	db, ctx := integrationDB(t)
	walletA, playerA := openIntegrationWallet(t, ctx, db, "100.00")
	walletB, playerB := openIntegrationWallet(t, ctx, db, "100.00")
	provider := uuid.NewString()
	inputs := []processWager{
		{provider, walletA, playerA, uuid.NewString(), uuid.NewString(), "80.00", uuid.NewString()},
		{provider, walletA, playerA, uuid.NewString(), uuid.NewString(), "80.00", uuid.NewString()},
		{provider, walletB, playerB, uuid.NewString(), uuid.NewString(), "5.00", uuid.NewString()},
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 0, len(inputs))
	for _, input := range inputs {
		payload, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		cmd := exec.Command(exe, "-test.run=^TestIndependentProcessWagerHelper$")
		cmd.Env = append(os.Environ(), "JUNGLE_PROCESS_WAGER="+string(payload))
		if err = cmd.Start(); err != nil {
			t.Fatalf("start independent process: %v", err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err = cmd.Wait(); err != nil {
			t.Fatalf("independent process failed: %v", err)
		}
	}
	var a, b string
	if err = db.pool.QueryRow(ctx, `SELECT balance::text FROM app.wallets WHERE id=$1`, walletA).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err = db.pool.QueryRow(ctx, `SELECT balance::text FROM app.wallets WHERE id=$1`, walletB).Scan(&b); err != nil {
		t.Fatal(err)
	}
	if a != "20.00" || b != "95.00" {
		t.Fatalf("independent processes lost updates: wallet A=%s wallet B=%s", a, b)
	}
}

func TestIndependentProcessWagerHelper(t *testing.T) {
	payload := os.Getenv("JUNGLE_PROCESS_WAGER")
	if payload == "" {
		return
	}
	var in processWager
	if err := json.Unmarshal([]byte(payload), &in); err != nil {
		t.Fatal(err)
	}
	db, ctx := integrationDB(t)
	amount, err := shared.NewMoney(in.Amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	request := application.SubmitWagerInput{ExternalID: in.ExternalID, IdempotencyKey: in.Key, PlayerID: in.PlayerID, WalletID: in.WalletID, RoundID: in.RoundID, GameID: "process-integration", Kind: wagering.Bet, Amount: amount}
	request.PayloadHash, err = application.CanonicalPayloadHash(in.ProviderID, request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.NewSubmitWager(db, integrationIDs{}, integrationClock{time.Now().UTC()}, nil, 0).Execute(ctx, application.Principal{ProviderID: in.ProviderID}, request)
	if err != nil {
		t.Fatal(err)
	}
}

func TestInboxRecoveryAcrossSeparateProcesses(t *testing.T) {
	db, ctx := integrationDB(t)
	walletID, playerID := openIntegrationWallet(t, ctx, db, "100.00")
	providerID, consumer, messageID := uuid.NewString(), "process-recovery", uuid.NewString()
	in := newIntegrationBet(t, providerID, walletID, playerID, uuid.NewString(), uuid.NewString(), "10.00")
	payloadHash := make([]byte, 32)
	payloadHash[0] = 7
	input := processInbox{Wager: processWager{providerID, walletID, playerID, in.IdempotencyKey, in.ExternalID, "10.00", in.RoundID}, Consumer: consumer, MessageID: messageID, PayloadHash: payloadHash}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		cmd := exec.Command(exe, "-test.run=^TestIndependentProcessInboxHelper$")
		cmd.Env = append(os.Environ(), "JUNGLE_PROCESS_INBOX="+string(encoded))
		if output, runErr := cmd.CombinedOutput(); runErr != nil {
			t.Fatalf("inbox process %d failed: %v: %s", attempt+1, runErr, output)
		}
	}
	var balance string
	var ledgerCount int64
	if err = db.pool.QueryRow(ctx, `SELECT balance::text FROM app.wallets WHERE id=$1`, walletID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if err = db.pool.QueryRow(ctx, `SELECT count(*) FROM app.wallet_ledger_entries WHERE wallet_id=$1`, walletID).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if balance != "90.00" || ledgerCount != 2 {
		t.Fatalf("cross-process SQS redelivery applied more than once: balance=%s ledger=%d", balance, ledgerCount)
	}
}

func TestIndependentProcessInboxHelper(t *testing.T) {
	payload := os.Getenv("JUNGLE_PROCESS_INBOX")
	if payload == "" {
		return
	}
	var in processInbox
	if err := json.Unmarshal([]byte(payload), &in); err != nil {
		t.Fatal(err)
	}
	db, ctx := integrationDB(t)
	amount, err := shared.NewMoney(in.Wager.Amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	submission := application.SubmitWagerInput{ExternalID: in.Wager.ExternalID, IdempotencyKey: in.Wager.Key, PlayerID: in.Wager.PlayerID, WalletID: in.Wager.WalletID, RoundID: in.Wager.RoundID, GameID: "integration", Kind: wagering.Bet, Amount: amount}
	submission.PayloadHash, err = application.CanonicalPayloadHash(in.Wager.ProviderID, submission)
	if err != nil {
		t.Fatal(err)
	}
	delivery := application.InboxDelivery{Consumer: in.Consumer, MessageID: in.MessageID, PayloadHash: in.PayloadHash}
	if _, err := application.NewSubmitWager(db, integrationIDs{}, integrationClock{time.Now().UTC()}, nil, 0).ConsumeWagerMessage(ctx, in.Wager.ProviderID, submission, delivery); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentDuplicateWagerReplaysDurableWinner(t *testing.T) {
	db, ctx := integrationDB(t)
	otherDB := independentIntegrationDB(t, ctx)
	walletID, playerID := openIntegrationWallet(t, ctx, db, "100.00")
	useCases := []*application.SubmitWager{
		application.NewSubmitWager(db, integrationIDs{}, integrationClock{time.Now().UTC()}, nil, 0),
		application.NewSubmitWager(otherDB, integrationIDs{}, integrationClock{time.Now().UTC()}, nil, 0),
	}
	providerID := uuid.NewString()
	in := newIntegrationBet(t, providerID, walletID, playerID, "shared-key", "shared-external", "10.00")
	const attempts = 50
	var wg sync.WaitGroup
	var firstErr atomic.Pointer[error]
	ids := make(chan string, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := useCases[i%len(useCases)].Execute(ctx, application.Principal{ProviderID: providerID}, in)
			if err != nil {
				copyErr := error(err)
				firstErr.CompareAndSwap(nil, &copyErr)
				return
			}
			ids <- result.Transaction.ID()
		}()
	}
	wg.Wait()
	close(ids)
	if errPtr := firstErr.Load(); errPtr != nil {
		t.Fatalf("duplicate submission failed: %v", *errPtr)
	}
	winner := ""
	for id := range ids {
		if winner == "" {
			winner = id
		} else if id != winner {
			t.Fatalf("concurrent callers observed different transactions: %s and %s", winner, id)
		}
	}
	var txCount, ledgerCount int64
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM app.wager_transactions WHERE provider_id=$1 AND idempotency_key=$2`, providerID, "shared-key").Scan(&txCount); err != nil {
		t.Fatal(err)
	}
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM app.wallet_ledger_entries WHERE wallet_id=$1`, walletID).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if txCount != 1 || ledgerCount != 2 { // opening credit plus one bet debit
		t.Fatalf("duplicate operation was applied more than once: transactions=%d ledger=%d", txCount, ledgerCount)
	}
}

func TestInboxDeduplicatesCompletedMessagesAndDetectsChangedPayload(t *testing.T) {
	db, ctx := integrationDB(t)
	consumer, messageID := "integration-consumer", uuid.NewString()
	hash := make([]byte, 32)
	hash[0] = 1
	start := func(payload []byte) (bool, error) {
		var done bool
		err := db.Within(ctx, func(tx application.Tx) error {
			var startErr error
			done, startErr = tx.Inbox().Start(ctx, consumer, messageID, payload)
			return startErr
		})
		return done, err
	}

	done, err := start(hash)
	if err != nil || done {
		t.Fatalf("first inbox delivery: done=%v err=%v", done, err)
	}
	if err = db.Within(ctx, func(tx application.Tx) error {
		return tx.Inbox().Complete(ctx, consumer, messageID, "", "")
	}); err != nil {
		t.Fatalf("complete inbox message: %v", err)
	}
	done, err = start(hash)
	if err != nil || !done {
		t.Fatalf("completed duplicate should be acknowledged: done=%v err=%v", done, err)
	}
	changed := append([]byte(nil), hash...)
	changed[0] = 2
	if _, err = start(changed); !errors.Is(err, application.ErrIdempotencyConflict) {
		t.Fatalf("changed payload should be rejected, got %v", err)
	}
}

func TestInboxRecoveryAfterDomainCommitReplaysWithoutSecondMovement(t *testing.T) {
	db, ctx := integrationDB(t)
	walletID, playerID := openIntegrationWallet(t, ctx, db, "100.00")
	providerID := uuid.NewString()
	in := newIntegrationBet(t, providerID, walletID, playerID, "recover-key", "recover-external", "10.00")
	payloadHash := make([]byte, 32)
	payloadHash[0] = 9
	consumer, messageID := "recovery-consumer", uuid.NewString()

	uc := application.NewSubmitWager(db, integrationIDs{}, integrationClock{time.Now().UTC()}, nil, 0)
	delivery := application.InboxDelivery{Consumer: consumer, MessageID: messageID, PayloadHash: payloadHash}
	first, err := uc.ConsumeWagerMessage(ctx, providerID, in, delivery)
	if err != nil {
		t.Fatalf("process message and commit inbox atomically: %v", err)
	}
	// Simulate a process crash after the database commit but before SQS ack.
	replay, err := uc.ConsumeWagerMessage(ctx, providerID, in, delivery)
	if err != nil || !replay.Replayed || replay.Transaction.ID() != first.Transaction.ID() {
		t.Fatalf("redelivery should replay durable result: result=%+v err=%v", replay, err)
	}
	done := false
	err = db.Within(ctx, func(tx application.Tx) error {
		var startErr error
		done, startErr = tx.Inbox().Start(ctx, consumer, messageID, payloadHash)
		return startErr
	})
	if err != nil || !done {
		t.Fatalf("committed inbox should acknowledge duplicate: done=%v err=%v", done, err)
	}
	var balance string
	var ledgerCount int64
	if err = db.pool.QueryRow(ctx, `SELECT balance::text FROM app.wallets WHERE id=$1`, walletID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if err = db.pool.QueryRow(ctx, `SELECT count(*) FROM app.wallet_ledger_entries WHERE wallet_id=$1`, walletID).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if balance != "90.00" || ledgerCount != 2 {
		t.Fatalf("recovery applied movement more than once: balance=%s ledger=%d", balance, ledgerCount)
	}
}

func TestOutboxRetryKeepsEventIdentityAndCompletes(t *testing.T) {
	db, ctx := integrationDB(t)
	openIntegrationWallet(t, ctx, db, "1.00") // opening emits durable outbox events
	workerA, workerB := uuid.NewString(), uuid.NewString()
	claimed, err := db.ClaimOutbox(ctx, workerA, 50)
	if err != nil || len(claimed) < 2 {
		t.Fatalf("claim opening events: count=%d err=%v", len(claimed), err)
	}
	if other, claimErr := db.ClaimOutbox(ctx, workerB, 50); claimErr != nil || len(other) != 0 {
		t.Fatalf("locked events should not be claimed twice: count=%d err=%v", len(other), claimErr)
	}
	first := claimed[0]
	if err = db.FinishOutbox(ctx, workerA, first.Event.ID, fmt.Errorf("temporary publish failure")); err != nil {
		t.Fatalf("record publish failure: %v", err)
	}
	for _, remaining := range claimed[1:] {
		if err = db.FinishOutbox(ctx, workerA, remaining.Event.ID, nil); err != nil {
			t.Fatalf("complete second event: %v", err)
		}
	}
	var eventID, lastError string
	var attempts int
	var published bool
	var nextAttempt time.Time
	if err = db.pool.QueryRow(ctx, `SELECT event_id::text,attempt_count,published_at IS NOT NULL,last_error,next_attempt_at FROM app.outbox_events WHERE event_id=$1`, first.Event.ID).Scan(&eventID, &attempts, &published, &lastError, &nextAttempt); err != nil {
		t.Fatalf("read scheduled outbox retry: %v", err)
	}
	if eventID != first.Event.ID || attempts != first.Attempt+1 || published || lastError != "temporary publish failure" || !nextAttempt.After(time.Now().UTC()) {
		t.Fatalf("retry must preserve event identity and schedule another attempt: event=%s attempts=%d published=%v next=%s error=%q", eventID, attempts, published, nextAttempt, lastError)
	}
}
