package application

import (
	"context"
	"errors"

	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wagering"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

type OpenWalletInput struct {
	PlayerID string
	Initial  shared.Money
}
type OpenWalletOutput struct {
	Wallet               WalletView
	OpeningTransactionID string
}

type OpenWallet struct {
	uow   UnitOfWork
	ids   IDGenerator
	clock Clock
}

func NewOpenWallet(uow UnitOfWork, ids IDGenerator, clock Clock) *OpenWallet {
	return &OpenWallet{uow: uow, ids: ids, clock: clock}
}

func (uc *OpenWallet) Execute(ctx context.Context, in OpenWalletInput) (out OpenWalletOutput, err error) {
	if in.PlayerID == "" || in.Initial.Currency() == "" || in.Initial.IsNegative() {
		return out, shared.Error("wallet.open.invalid_input", shared.ErrInvalidArgument)
	}
	now := uc.clock.Now().UTC()
	err = uc.uow.Within(ctx, func(tx Tx) error {
		_, lookupErr := tx.Wallets().ByPlayerCurrency(ctx, in.PlayerID, in.Initial.Currency())
		if lookupErr == nil {
			return ErrConflict
		}
		if !errors.Is(lookupErr, ErrNotFound) {
			return lookupErr
		}
		walletID := uc.ids.NewID()
		openingID := ""
		if in.Initial.IsPositive() {
			openingID = uc.ids.NewID()
		}
		w, entry, createErr := wallet.NewWallet(walletID, in.PlayerID, in.Initial, openingID, now)
		if createErr != nil {
			return createErr
		}
		if createErr = tx.Wallets().Create(ctx, w); createErr != nil {
			return createErr
		}
		if openingID != "" {
			opening, createErr := wagering.NewOpening(openingID, in.PlayerID, walletID, in.Initial, now)
			if createErr != nil {
				return createErr
			}
			if createErr = tx.Transactions().Create(ctx, opening); createErr != nil {
				return createErr
			}
			if createErr = tx.Ledger().Append(ctx, entry); createErr != nil {
				return createErr
			}
			if createErr = tx.Outbox().Append(ctx, []Event{{ID: uc.ids.NewID(), Type: "WagerTransactionProcessed", AggregateID: openingID, CorrelationID: openingID, OccurredAt: now, Data: wagerEventData(opening)}, {ID: uc.ids.NewID(), Type: "WalletBalanceChanged", AggregateID: walletID, CorrelationID: openingID, OccurredAt: now, Data: walletBalanceEventData(entry, 1)}}); createErr != nil {
				return createErr
			}
		}
		out = OpenWalletOutput{Wallet: walletView(w), OpeningTransactionID: openingID}
		return nil
	})
	return out, err
}

type GetWallet struct{ uow UnitOfWork }

func NewGetWallet(uow UnitOfWork) *GetWallet { return &GetWallet{uow: uow} }
func (uc *GetWallet) Execute(ctx context.Context, p Principal, id string) (WalletView, error) {
	if id == "" {
		return WalletView{}, shared.Error("wallet.get.invalid_id", shared.ErrInvalidArgument)
	}
	var out WalletView
	err := uc.uow.Within(ctx, func(tx Tx) error {
		w, e := tx.Wallets().ByID(ctx, id)
		if e != nil {
			return e
		}
		if !p.Internal && (p.PlayerID == "" || p.PlayerID != w.PlayerID()) {
			return ErrUnauthorized
		}
		out = walletView(w)
		return nil
	})
	return out, err
}

type ListWalletLedger struct{ uow UnitOfWork }

func NewListWalletLedger(uow UnitOfWork) *ListWalletLedger { return &ListWalletLedger{uow: uow} }
func (uc *ListWalletLedger) Execute(ctx context.Context, p Principal, walletID, cursor string, limit int) (LedgerPage, error) {
	if walletID == "" || limit < 1 || limit > 500 {
		return LedgerPage{}, shared.Error("ledger.list.invalid_input", shared.ErrInvalidArgument)
	}
	var page LedgerPage
	err := uc.uow.Within(ctx, func(tx Tx) error {
		w, e := tx.Wallets().ByID(ctx, walletID)
		if e != nil {
			return e
		}
		if !p.Internal && (p.PlayerID == "" || p.PlayerID != w.PlayerID()) {
			return ErrUnauthorized
		}
		page, e = tx.Ledger().List(ctx, walletID, cursor, limit)
		return e
	})
	return page, err
}

type ReconcileWallet struct{ uow UnitOfWork }

func NewReconcileWallet(uow UnitOfWork) *ReconcileWallet { return &ReconcileWallet{uow: uow} }
func (uc *ReconcileWallet) Execute(ctx context.Context, p Principal, walletID string) (Reconciliation, error) {
	var out Reconciliation
	err := uc.uow.Within(ctx, func(tx Tx) error {
		w, e := tx.Wallets().ByID(ctx, walletID)
		if e != nil {
			return e
		}
		if !p.Internal && (p.PlayerID == "" || p.PlayerID != w.PlayerID()) {
			return ErrUnauthorized
		}
		calculated, count, e := tx.Ledger().Sum(ctx, walletID, w.Currency())
		if e != nil {
			return e
		}
		difference, e := w.Balance().Subtract(calculated)
		if e != nil {
			return e
		}
		out = Reconciliation{StoredBalance: w.Balance(), CalculatedBalance: calculated, Difference: difference, Consistent: difference.IsZero(), Entries: count}
		return nil
	})
	return out, err
}
