package wallet

import (
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
)

// LedgerEntry is immutable after construction.
type LedgerEntry struct {
	walletID, transactionID             string
	direction                           Direction
	amount, balanceBefore, balanceAfter shared.Money
	createdAt                           time.Time
	version                             int64
}

func newLedgerEntry(walletID, transactionID string, direction Direction, amount, before, after shared.Money, at time.Time, version int64) *LedgerEntry {
	return &LedgerEntry{walletID: walletID, transactionID: transactionID, direction: direction, amount: amount, balanceBefore: before, balanceAfter: after, createdAt: at.UTC(), version: version}
}

func RehydrateLedgerEntry(walletID, transactionID string, direction Direction, amount, before, after shared.Money, at time.Time) (*LedgerEntry, error) {
	if walletID == "" || transactionID == "" || at.IsZero() || !amount.IsPositive() || before.IsNegative() || after.IsNegative() || amount.Currency() != before.Currency() || amount.Currency() != after.Currency() {
		return nil, shared.Error("ledger.invalid_entry", shared.ErrInvalidArgument)
	}
	var expected shared.Money
	var err error
	switch direction {
	case CreditDirection:
		expected, err = before.Add(amount)
	case DebitDirection:
		if before.Amount().LessThan(amount.Amount()) {
			return nil, shared.Error("ledger.insufficient_before_balance", shared.ErrInvalidArgument)
		}
		expected, err = before.Subtract(amount)
	default:
		return nil, shared.Error("ledger.invalid_direction", shared.ErrInvalidArgument)
	}
	if err != nil || expected.Amount().Cmp(after.Amount()) != 0 {
		return nil, shared.Error("ledger.balance_equation_mismatch", shared.ErrInvalidArgument)
	}
	return newLedgerEntry(walletID, transactionID, direction, amount, before, after, at, 0), nil
}

func (e *LedgerEntry) WalletID() string            { return e.walletID }
func (e *LedgerEntry) TransactionID() string       { return e.transactionID }
func (e *LedgerEntry) Direction() Direction        { return e.direction }
func (e *LedgerEntry) Amount() shared.Money        { return e.amount }
func (e *LedgerEntry) BalanceBefore() shared.Money { return e.balanceBefore }
func (e *LedgerEntry) BalanceAfter() shared.Money  { return e.balanceAfter }
func (e *LedgerEntry) CreatedAt() time.Time        { return e.createdAt }
func (e *LedgerEntry) WalletVersion() int64        { return e.version }
