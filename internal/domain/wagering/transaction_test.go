package wagering

import (
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
)

var when = time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

func cash(v string) shared.Money {
	m, err := shared.NewMoney(v, "USD")
	if err != nil {
		panic(err)
	}
	return m
}
func params(kind Kind, amount string) ExternalParams {
	return ExternalParams{ID: "id", ProviderID: "prov", ExternalID: "ext", IdempotencyKey: "key", PayloadHash: "hash", PlayerID: "player", WalletID: "wallet", RoundID: "round", GameID: "game", Kind: kind, Amount: cash(amount), CreatedAt: when}
}

func TestNewExternalValidatesOperationAndFields(t *testing.T) {
	for _, p := range []ExternalParams{params(Opening, "1"), params(Bet, "0"), params(Loss, "1")} {
		if _, err := NewExternal(p); err == nil {
			t.Fatalf("expected invalid operation for %+v", p)
		}
	}
	p := params(Refund, "2")
	if _, err := NewExternal(p); !errors.Is(err, shared.ErrInvalidReference) {
		t.Fatalf("refund reference required: %v", err)
	}
	p = params(Bet, "2")
	p.ProviderID = ""
	if _, err := NewExternal(p); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("required fields: %v", err)
	}
	p = params(Bet, "2")
	p.ReferenceExternalID = "ref"
	if _, err := NewExternal(p); !errors.Is(err, shared.ErrInvalidReference) {
		t.Fatalf("bet reference forbidden: %v", err)
	}
	p = params(Loss, "0")
	tx, err := NewExternal(p)
	if err != nil || tx.Status() != Pending || tx.Amount().String() != "0.00" {
		t.Fatalf("valid loss: %v", err)
	}
	if tx.ID() != "id" || tx.ProviderID() != "prov" || tx.ExternalID() != "ext" || tx.IdempotencyKey() != "key" || tx.PayloadHash() != "hash" || tx.PlayerID() != "player" || tx.WalletID() != "wallet" || tx.RoundID() != "round" || tx.GameID() != "game" || tx.Kind() != Loss || tx.ReferenceExternalID() != "" || tx.FailureCode() != "" || tx.ResultBalance() != nil || !tx.CreatedAt().Equal(when) || !tx.UpdatedAt().Equal(when) {
		t.Fatal("transaction accessors returned incorrect state")
	}
}

func TestTransactionTransitionsAreTerminalAndMonotonic(t *testing.T) {
	tx, _ := NewExternal(params(Bet, "5"))
	if err := tx.MarkProcessed(ptr(cash("10")), when.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if tx.Status() != Processed || tx.ResultBalance().String() != "10.00" {
		t.Fatal("processed state not stored")
	}
	if err := tx.Reject("again", when.Add(2*time.Second)); !errors.Is(err, shared.ErrTerminalTransaction) {
		t.Fatalf("terminal changed: %v", err)
	}
	other, _ := NewExternal(params(Bet, "5"))
	if err := other.MarkPendingReference(when.Add(time.Second)); !errors.Is(err, shared.ErrInvalidReference) {
		t.Fatalf("bet cannot pend on reference: %v", err)
	}
	if err := other.Reject("bad", when.Add(-time.Second)); !errors.Is(err, shared.ErrInvalidTransition) {
		t.Fatalf("time regression: %v", err)
	}
	if err := other.Reject("", when.Add(time.Second)); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("missing code: %v", err)
	}
	if err := other.Reject("bad", when.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := other.Fail("late", when.Add(2*time.Second)); !errors.Is(err, shared.ErrTerminalTransaction) {
		t.Fatalf("failed terminal changed: %v", err)
	}
}

func TestPendingReferenceThenProcessAndValidation(t *testing.T) {
	p := params(Refund, "5")
	p.ReferenceExternalID = "bet-ext"
	tx, err := NewExternal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.MarkPendingReference(when.Add(time.Second)); err != nil || tx.Status() != PendingReference {
		t.Fatalf("pending reference: %v", err)
	}
	if err = tx.MarkProcessed(nil, when.Add(2*time.Second)); !errors.Is(err, shared.ErrInvalidMoney) {
		t.Fatalf("missing snapshot: %v", err)
	}
	if err = tx.MarkProcessed(ptr(cash("9")), when.Add(2*time.Second)); err != nil || tx.Status() != Processed {
		t.Fatalf("process: %v", err)
	}
	if !tx.UpdatedAt().Equal(when.Add(2 * time.Second)) {
		t.Fatal("updated timestamp was not stored")
	}
	if err = tx.MarkProcessed(ptr(cash("9")), time.Time{}); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("zero transition time: %v", err)
	}
	if err = tx.MarkPendingReference(when.Add(3 * time.Second)); !errors.Is(err, shared.ErrTerminalTransaction) {
		t.Fatalf("terminal pending transition: %v", err)
	}
	var nilTx *WagerTransaction
	if err = nilTx.MarkProcessed(ptr(cash("1")), when); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("nil transaction transition: %v", err)
	}
}

func TestReversalRules(t *testing.T) {
	source, _ := NewExternal(params(Bet, "5"))
	_ = source.MarkProcessed(ptr(cash("5")), when.Add(time.Second))
	p := params(Refund, "5")
	p.ReferenceExternalID = "ext"
	refund, _ := NewExternal(p)
	if err := ValidateReversal(refund, source); err != nil {
		t.Fatal(err)
	}
	if err := source.MarkReversed(when.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReversal(refund, source); !errors.Is(err, shared.ErrDuplicateReversal) {
		t.Fatalf("duplicate accepted: %v", err)
	}
	if err := source.MarkReversed(when.Add(3 * time.Second)); !errors.Is(err, shared.ErrDuplicateReversal) {
		t.Fatalf("second reversal mark accepted: %v", err)
	}
	rollbackParams := params(Rollback, "4")
	rollbackParams.ReferenceExternalID = "ext"
	rb, _ := NewExternal(rollbackParams)
	if err := ValidateReversal(rb, source); !errors.Is(err, shared.ErrInvalidReference) {
		t.Fatalf("partial reversal accepted: %v", err)
	}
	win, _ := NewExternal(params(Win, "5"))
	_ = win.MarkProcessed(ptr(cash("5")), when.Add(time.Second))
	rollbackParams.Amount = cash("5")
	rollbackParams.ReferenceExternalID = "different"
	rb, _ = NewExternal(rollbackParams)
	if err := ValidateReversal(rb, win); !errors.Is(err, shared.ErrInvalidReference) {
		t.Fatalf("context mismatch accepted: %v", err)
	}
	other := params(Refund, "5")
	other.ReferenceExternalID = "ext"
	invalidRefund, _ := NewExternal(other)
	if err := ValidateReversal(invalidRefund, win); !errors.Is(err, shared.ErrInvalidReference) {
		t.Fatalf("refund against win accepted: %v", err)
	}
	loss, _ := NewExternal(params(Loss, "0"))
	_ = loss.MarkProcessed(ptr(cash("5")), when.Add(time.Second))
	if err := ValidateReversal(rb, loss); !errors.Is(err, shared.ErrInvalidReference) {
		t.Fatalf("rollback against loss accepted: %v", err)
	}
	if err := ValidateReversal(nil, source); !errors.Is(err, shared.ErrInvalidReference) {
		t.Fatalf("nil reversal accepted: %v", err)
	}
	pendingSource, _ := NewExternal(params(Bet, "5"))
	if err := ValidateReversal(refund, pendingSource); !errors.Is(err, shared.ErrInvalidReference) {
		t.Fatalf("pending source accepted: %v", err)
	}
	if err := pendingSource.MarkReversed(when); !errors.Is(err, shared.ErrInvalidReference) {
		t.Fatalf("pending source marked reversed: %v", err)
	}
	if err := win.MarkReversed(time.Time{}); !errors.Is(err, shared.ErrInvalidReference) {
		t.Fatalf("zero reversal timestamp: %v", err)
	}
}

func TestOpeningAndRehydrate(t *testing.T) {
	opening, err := NewOpening("open", "player", "wallet", cash("20"), when)
	if err != nil || opening.Status() != Processed || opening.Kind() != Opening {
		t.Fatalf("opening: %v", err)
	}
	opening, err = RehydrateOpening("open", "player", "wallet", cash("20"), Processed, when, when.Add(time.Second))
	if err != nil || opening.Amount().String() != "20.00" || opening.ResultBalance() != nil {
		t.Fatalf("rehydrate opening: %v", err)
	}
	if _, err := RehydrateOpening("open", "player", "wallet", cash("20"), Pending, when, when); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("nonterminal opening: %v", err)
	}
	if _, err := RehydrateOpening("open", "player", "wallet", cash("20"), Processed, when, when.Add(-time.Second)); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("opening time regression: %v", err)
	}
	p := params(Bet, "3")
	if _, err := Rehydrate(p, Pending, "", nil, when.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := Rehydrate(p, Processed, "", nil, when.Add(time.Second)); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("processed without result: %v", err)
	}
	if _, err := Rehydrate(p, Rejected, "", nil, when); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("rejected without code: %v", err)
	}
	if _, err := Rehydrate(p, Status("WHAT"), "", nil, when); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("unknown status: %v", err)
	}
	if _, err := Rehydrate(p, Pending, "failure", nil, when); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("pending with failure code: %v", err)
	}
	if _, err := Rehydrate(p, Rejected, "rejected", nil, when); err != nil {
		t.Fatalf("rehydrate rejected: %v", err)
	}
	if _, err := Rehydrate(p, Failed, "failed", nil, when); err != nil {
		t.Fatalf("rehydrate failed: %v", err)
	}
	if _, err := Rehydrate(p, Pending, "", nil, time.Time{}); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("zero persisted timestamp: %v", err)
	}
	if _, err := Rehydrate(p, Pending, "", ptr(cash("1")), when); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("pending with result: %v", err)
	}
	if _, err := Rehydrate(p, Processed, "", ptr(mustCash("1", "EUR")), when); !errors.Is(err, shared.ErrCurrencyMismatch) {
		t.Fatalf("result currency mismatch: %v", err)
	}
	if _, err := NewOpening("", "player", "wallet", cash("1"), when); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("invalid opening identity: %v", err)
	}
	if _, err := NewOpening("id", "player", "wallet", cash("1"), time.Time{}); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("invalid opening time: %v", err)
	}
}

func ptr(m shared.Money) *shared.Money { return &m }

func mustCash(v, currency string) shared.Money { m, _ := shared.NewMoney(v, currency); return m }
