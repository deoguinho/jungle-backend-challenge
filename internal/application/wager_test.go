package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wagering"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

func TestMovementForKinds(t *testing.T) {
	money, err := shared.NewMoney("1.00", "USD")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(kind wagering.Kind, reference string) *wagering.WagerTransaction {
		t.Helper()
		amount := money
		externalID := "ext"
		if kind == wagering.Loss {
			amount, _ = shared.ZeroMoney("USD")
		}
		if kind == wagering.Bet && reference == "source" {
			externalID = "source"
			reference = ""
		}
		tx, e := wagering.NewExternal(wagering.ExternalParams{ID: "tx", ProviderID: "p", ExternalID: externalID, IdempotencyKey: "key", PayloadHash: "hash", PlayerID: "player", WalletID: "wallet", RoundID: "round", GameID: "game", Kind: kind, Amount: amount, ReferenceExternalID: reference, CreatedAt: time.Now().UTC()})
		if e != nil {
			t.Fatal(e)
		}
		return tx
	}
	tests := []struct {
		name    string
		kind    wagering.Kind
		source  *wagering.WagerTransaction
		want    wallet.Direction
		changes bool
	}{
		{"bet", wagering.Bet, nil, wallet.DebitDirection, true},
		{"win", wagering.Win, nil, wallet.CreditDirection, true},
		{"loss", wagering.Loss, nil, "", false},
		{"refund", wagering.Refund, nil, wallet.CreditDirection, true},
		{"rollback bet", wagering.Rollback, mk(wagering.Bet, "source"), wallet.CreditDirection, true},
		{"rollback win", wagering.Rollback, mk(wagering.Win, "source"), wallet.DebitDirection, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tx := mk(tc.kind, func() string {
				if tc.kind == wagering.Rollback {
					return "source"
				}
				if tc.kind == wagering.Refund {
					return "source"
				}
				return ""
			}())
			got, changes := movementFor(tx, tc.source)
			if got != tc.want || changes != tc.changes {
				t.Fatalf("got (%q,%v), want (%q,%v)", got, changes, tc.want, tc.changes)
			}
		})
	}
}

func TestSubmitWagerRejectsUnauthorizedBeforeDependencies(t *testing.T) {
	uc := NewSubmitWager(nil, nil, nil, nil, 0)
	_, err := uc.Execute(context.Background(), Principal{}, SubmitWagerInput{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}
