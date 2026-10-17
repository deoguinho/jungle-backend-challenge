package wallet

import (
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
)

type Direction string

const (
	CreditDirection Direction = "CREDIT"
	DebitDirection  Direction = "DEBIT"
)

// Wallet protects its balance and version; callers receive copies of value objects.
type Wallet struct {
	id, playerID         string
	currency             string
	balance              shared.Money
	version              int64
	createdAt, updatedAt time.Time
}

func NewWallet(id, playerID string, initial shared.Money, openingTransactionID string, now time.Time) (*Wallet, *LedgerEntry, error) {
	if id == "" || playerID == "" || now.IsZero() {
		return nil, nil, shared.Error("wallet.invalid_identity", shared.ErrInvalidArgument)
	}
	if initial.IsNegative() {
		return nil, nil, shared.Error("wallet.negative_initial_balance", shared.ErrInvalidMoney)
	}
	w := &Wallet{id: id, playerID: playerID, currency: initial.Currency(), balance: initial, version: 1, createdAt: now.UTC(), updatedAt: now.UTC()}
	if initial.IsZero() {
		return w, nil, nil
	}
	if openingTransactionID == "" {
		return nil, nil, shared.Error("wallet.missing_opening_transaction", shared.ErrInvalidArgument)
	}
	zero, _ := shared.ZeroMoney(initial.Currency())
	return w, newLedgerEntry(id, openingTransactionID, CreditDirection, initial, zero, initial, now, 1), nil
}

// Rehydrate restores persisted state without creating a movement or ledger entry.
func Rehydrate(id, playerID string, balance shared.Money, version int64, createdAt, updatedAt time.Time) (*Wallet, error) {
	if id == "" || playerID == "" || balance.Currency() == "" || balance.IsNegative() || version < 1 || createdAt.IsZero() || updatedAt.IsZero() {
		return nil, shared.Error("wallet.invalid_persisted_state", shared.ErrInvalidArgument)
	}
	return &Wallet{id: id, playerID: playerID, currency: balance.Currency(), balance: balance, version: version, createdAt: createdAt.UTC(), updatedAt: updatedAt.UTC()}, nil
}

func (w *Wallet) ID() string            { return w.id }
func (w *Wallet) PlayerID() string      { return w.playerID }
func (w *Wallet) Currency() string      { return w.currency }
func (w *Wallet) Balance() shared.Money { return w.balance }
func (w *Wallet) Version() int64        { return w.version }
func (w *Wallet) CreatedAt() time.Time  { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time  { return w.updatedAt }

func (w *Wallet) Credit(transactionID string, amount shared.Money, now time.Time) (*LedgerEntry, error) {
	return w.apply(transactionID, amount, CreditDirection, now)
}

func (w *Wallet) Debit(transactionID string, amount shared.Money, now time.Time) (*LedgerEntry, error) {
	return w.apply(transactionID, amount, DebitDirection, now)
}

func (w *Wallet) apply(transactionID string, amount shared.Money, direction Direction, now time.Time) (*LedgerEntry, error) {
	if w == nil || transactionID == "" || now.IsZero() || !amount.IsPositive() {
		return nil, shared.Error("wallet.invalid_movement", shared.ErrInvalidArgument)
	}
	if amount.Currency() != w.currency {
		return nil, shared.Error("wallet.currency_mismatch", shared.ErrCurrencyMismatch)
	}
	var next shared.Money
	var err error
	if direction == CreditDirection {
		next, err = w.balance.Add(amount)
	} else {
		next, err = w.balance.Subtract(amount)
	}
	if err != nil {
		return nil, err
	}
	if next.IsNegative() {
		return nil, shared.Error("wallet.insufficient_funds", shared.ErrInsufficientFunds)
	}
	before := w.balance
	w.balance = next
	w.version++
	w.updatedAt = now.UTC()
	return newLedgerEntry(w.id, transactionID, direction, amount, before, next, now, w.version), nil
}
