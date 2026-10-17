package wallet

import (
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
)

var at = time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))

func money(v string) shared.Money {
	m, err := shared.NewMoney(v, "USD")
	if err != nil {
		panic(err)
	}
	return m
}

func TestNewWalletOpeningAndZero(t *testing.T) {
	w, entry, err := NewWallet("w1", "p1", money("10.00"), "opening1", at)
	if err != nil {
		t.Fatal(err)
	}
	if w.Balance().String() != "10.00" || w.Version() != 1 || entry == nil {
		t.Fatalf("wallet=%+v entry=%+v", w, entry)
	}
	if w.ID() != "w1" || w.PlayerID() != "p1" || w.Currency() != "USD" || !w.CreatedAt().Equal(at.UTC()) || !w.UpdatedAt().Equal(at.UTC()) {
		t.Fatal("wallet accessors returned incorrect state")
	}
	if entry.Direction() != CreditDirection || entry.BalanceBefore().String() != "0.00" || entry.BalanceAfter().String() != "10.00" {
		t.Fatal("incorrect opening ledger")
	}
	if entry.WalletID() != "w1" || entry.TransactionID() != "opening1" || entry.Amount().String() != "10.00" || !entry.CreatedAt().Equal(at.UTC()) || entry.WalletVersion() != 1 {
		t.Fatal("ledger accessors returned incorrect state")
	}
	z, entry, err := NewWallet("w2", "p1", money("0"), "", at)
	if err != nil || entry != nil || z.Version() != 1 {
		t.Fatalf("zero opening: %v", err)
	}
	if _, _, err := NewWallet("", "p1", money("1"), "tx", at); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("expected invalid identity: %v", err)
	}
	if _, _, err := NewWallet("w", "p", money("1"), "", at); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("expected opening id: %v", err)
	}
}

func TestCreditDebitAndInvariants(t *testing.T) {
	w, _, _ := NewWallet("w", "p", money("100.00"), "opening", at)
	e, err := w.Debit("bet", money("80.00"), at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if w.Balance().String() != "20.00" || w.Version() != 2 || e.BalanceBefore().String() != "100.00" || e.BalanceAfter().String() != "20.00" {
		t.Fatal("debit did not update wallet and ledger")
	}
	e, err = w.Credit("win", money("0.25"), at.Add(2*time.Second))
	if err != nil || e.Direction() != CreditDirection || w.Balance().String() != "20.25" || w.Version() != 3 {
		t.Fatalf("credit: %v", err)
	}
	if _, err = w.Debit("too-much", money("21"), at.Add(3*time.Second)); !errors.Is(err, shared.ErrInsufficientFunds) {
		t.Fatalf("expected insufficient funds: %v", err)
	}
	if w.Balance().String() != "20.25" || w.Version() != 3 {
		t.Fatal("failed debit mutated wallet")
	}
	if _, err = w.Credit("bad", money("0"), at.Add(3*time.Second)); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("expected invalid amount: %v", err)
	}
	if _, err = w.Credit("bad", mustMoney("1", "EUR"), at.Add(3*time.Second)); !errors.Is(err, shared.ErrCurrencyMismatch) {
		t.Fatalf("expected currency error: %v", err)
	}
}

func TestRehydrateWalletAndLedger(t *testing.T) {
	w, err := Rehydrate("w", "p", money("7.50"), 9, at, at.Add(time.Minute))
	if err != nil || w.Version() != 9 || w.Balance().String() != "7.50" {
		t.Fatalf("rehydrate: %v", err)
	}
	if _, err = Rehydrate("", "p", money("1"), 1, at, at); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("invalid rehydrate: %v", err)
	}
	if _, err = RehydrateLedgerEntry("w", "tx", CreditDirection, money("2"), money("1"), money("3"), at); err != nil {
		t.Fatal(err)
	}
	if _, err = RehydrateLedgerEntry("w", "tx", DebitDirection, money("2"), money("1"), money("9"), at); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("bad equation: %v", err)
	}
	if _, err = RehydrateLedgerEntry("w", "tx", Direction("OTHER"), money("2"), money("1"), money("3"), at); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("bad direction: %v", err)
	}
	if _, err = RehydrateLedgerEntry("w", "tx", DebitDirection, money("2"), money("1"), money("0"), at); !errors.Is(err, shared.ErrInvalidArgument) {
		t.Fatalf("debit overdrawn before: %v", err)
	}
}

func mustMoney(v, c string) shared.Money { m, _ := shared.NewMoney(v, c); return m }
