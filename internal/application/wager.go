package application

import (
	"context"
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wagering"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

type SubmitWagerInput struct {
	ExternalID, IdempotencyKey, PayloadHash, PlayerID, WalletID, RoundID, GameID string
	Kind                                                                         wagering.Kind
	Amount                                                                       shared.Money
	ReferenceExternalID                                                          string
}
type WagerResult struct {
	Transaction       *wagering.WagerTransaction
	Balance           *shared.Money
	Replayed, Pending bool
}

type SubmitWager struct {
	uow      UnitOfWork
	ids      IDGenerator
	clock    Clock
	cache    IdempotencyCache
	cacheTTL time.Duration
	observer WagerObserver
}

// ConsumeWagerTransaction is the worker entry point. The adapter must supply
// the provider identity from the trusted message envelope, never an untrusted
// payload field, and acknowledge only after Execute returns durably.
func (uc *SubmitWager) ConsumeWagerTransaction(ctx context.Context, providerID string, in SubmitWagerInput) (WagerResult, error) {
	return uc.Execute(ctx, Principal{ProviderID: providerID, PlayerID: in.PlayerID, Internal: true}, in)
}

type InboxDelivery struct {
	Consumer, MessageID string
	PayloadHash         []byte
}

// ConsumeWagerMessage atomically records inbox completion with the durable
// wager result. The caller acknowledges SQS only after this method commits.
func (uc *SubmitWager) ConsumeWagerMessage(ctx context.Context, providerID string, in SubmitWagerInput, delivery InboxDelivery) (WagerResult, error) {
	if delivery.Consumer == "" || delivery.MessageID == "" || len(delivery.PayloadHash) != 32 {
		return WagerResult{}, shared.Error("wager.inbox.invalid_delivery", shared.ErrInvalidArgument)
	}
	return uc.execute(ctx, Principal{ProviderID: providerID, PlayerID: in.PlayerID, Internal: true}, in, &delivery)
}

func NewSubmitWager(uow UnitOfWork, ids IDGenerator, clock Clock, cache IdempotencyCache, cacheTTL time.Duration, observers ...WagerObserver) *SubmitWager {
	uc := &SubmitWager{uow: uow, ids: ids, clock: clock, cache: cache, cacheTTL: cacheTTL}
	if len(observers) > 0 {
		uc.observer = observers[0]
	}
	return uc
}

func (uc *SubmitWager) Execute(ctx context.Context, principal Principal, in SubmitWagerInput) (WagerResult, error) {
	return uc.execute(ctx, principal, in, nil)
}

func (uc *SubmitWager) execute(ctx context.Context, principal Principal, in SubmitWagerInput, delivery *InboxDelivery) (out WagerResult, err error) {
	started := time.Now()
	defer func() {
		if uc.observer != nil {
			status := "ERROR"
			if out.Transaction != nil {
				status = string(out.Transaction.Status())
			}
			uc.observer.WagerResult(status, out.Replayed, errors.Is(err, ErrIdempotencyConflict), err)
			uc.observer.ObserveWagerDuration(time.Since(started))
		}
	}()
	if !principal.Internal {
		if principal.ProviderID == "" || (principal.PlayerID != "" && in.PlayerID != "" && in.PlayerID != principal.PlayerID) {
			return out, ErrUnauthorized
		}
		if principal.PlayerID != "" {
			in.PlayerID = principal.PlayerID
		}
	}
	if principal.ProviderID == "" || in.PlayerID == "" || in.ExternalID == "" || in.IdempotencyKey == "" || in.PayloadHash == "" || in.WalletID == "" {
		return out, shared.Error("wager.submit.invalid_input", shared.ErrInvalidArgument)
	}
	now := uc.clock.Now().UTC()
	p := wagering.ExternalParams{ID: uc.ids.NewID(), ProviderID: principal.ProviderID, ExternalID: in.ExternalID, IdempotencyKey: in.IdempotencyKey, PayloadHash: in.PayloadHash, PlayerID: in.PlayerID, WalletID: in.WalletID, RoundID: in.RoundID, GameID: in.GameID, Kind: in.Kind, Amount: in.Amount, ReferenceExternalID: in.ReferenceExternalID, CreatedAt: now}
	var cached CachedIdempotency
	cacheHit := false
	cacheKey := principal.ProviderID + ":" + in.IdempotencyKey
	if uc.cache != nil {
		cached, cacheHit, _ = uc.cache.Get(ctx, cacheKey)
	}
	err = uc.uow.Within(ctx, func(tx Tx) error {
		if delivery != nil {
			done, inboxErr := tx.Inbox().Start(ctx, delivery.Consumer, delivery.MessageID, delivery.PayloadHash)
			if inboxErr != nil {
				return inboxErr
			}
			if done {
				existing, lookupErr := tx.Transactions().ByProviderIdempotency(ctx, principal.ProviderID, in.IdempotencyKey)
				if lookupErr != nil {
					return lookupErr
				}
				if existing.PayloadHash() != in.PayloadHash {
					return ErrIdempotencyConflict
				}
				out = WagerResult{Transaction: existing, Balance: existing.ResultBalance(), Replayed: true, Pending: existing.Status() == wagering.Pending || existing.Status() == wagering.PendingReference}
				return nil
			}
		}
		operationErr := func() error {
			if cacheHit && cached.TransactionID != "" {
				if candidate, cacheErr := tx.Transactions().ByID(ctx, cached.TransactionID); cacheErr == nil && candidate.ProviderID() == principal.ProviderID && candidate.IdempotencyKey() == in.IdempotencyKey {
					if candidate.PayloadHash() != in.PayloadHash {
						return ErrIdempotencyConflict
					}
					out = WagerResult{Transaction: candidate, Balance: candidate.ResultBalance(), Replayed: true, Pending: candidate.Status() == wagering.Pending || candidate.Status() == wagering.PendingReference}
					return nil
				}
			}
			// PostgreSQL lookup is authoritative, including when Redis hits or fails.
			existing, e := tx.Transactions().ByProviderIdempotency(ctx, principal.ProviderID, in.IdempotencyKey)
			if e == nil {
				if existing.PayloadHash() != in.PayloadHash {
					return ErrIdempotencyConflict
				}
				out = WagerResult{Transaction: existing, Balance: existing.ResultBalance(), Replayed: true, Pending: existing.Status() == wagering.Pending || existing.Status() == wagering.PendingReference}
				return nil
			}
			if !errors.Is(e, ErrNotFound) {
				return e
			}
			_, e = tx.Transactions().ByProviderExternal(ctx, principal.ProviderID, in.ExternalID)
			if e == nil {
				return ErrConflict
			}
			if !errors.Is(e, ErrNotFound) {
				return e
			}
			w, e := tx.Wallets().ByID(ctx, in.WalletID)
			if e != nil {
				return e
			}
			if w.PlayerID() != in.PlayerID || w.Currency() != in.Amount.Currency() {
				return shared.Error("wager.wallet_context_mismatch", shared.ErrInvalidArgument)
			}
			t, e := wagering.NewExternal(p)
			if e != nil {
				return e
			}
			if e = tx.Transactions().Create(ctx, t); e != nil {
				return e
			}
			var source *wagering.WagerTransaction
			if t.Kind() == wagering.Refund || t.Kind() == wagering.Rollback {
				source, e = tx.Transactions().ByReference(ctx, principal.ProviderID, t.ReferenceExternalID())
				if errors.Is(e, ErrNotFound) {
					if e = t.MarkPendingReference(now); e != nil {
						return e
					}
					if e = tx.Transactions().Save(ctx, t); e != nil {
						return e
					}
					if e = tx.Outbox().Append(ctx, []Event{{ID: uc.ids.NewID(), Type: "WagerTransactionPendingReference", AggregateID: t.ID(), CorrelationID: t.ID(), OccurredAt: now, Data: wagerEventData(t)}}); e != nil {
						return e
					}
					out = WagerResult{Transaction: t, Pending: true}
					return nil
				}
				if e != nil {
					return e
				}
				if e = wagering.ValidateReversal(t, source); e != nil {
					if re := t.Reject("REFERENCE_INVALID", now); re != nil {
						return re
					}
					if re := tx.Transactions().Save(ctx, t); re != nil {
						return re
					}
					if re := tx.Outbox().Append(ctx, []Event{{ID: uc.ids.NewID(), Type: "WagerTransactionRejected", AggregateID: t.ID(), CorrelationID: t.ID(), OccurredAt: now, Data: wagerEventData(t)}}); re != nil {
						return re
					}
					out = WagerResult{Transaction: t}
					return nil
				}
			}
			direction, changes := movementFor(t, source)
			var final *shared.Money
			if changes {
				m, e := tx.Wallets().ApplyMovement(ctx, w.ID(), t.ID(), direction, t.Amount(), now)
				if errors.Is(e, shared.ErrInsufficientFunds) {
					failureCode := "INSUFFICIENT_FUNDS"
					if t.Kind() == wagering.Rollback {
						failureCode = "REVERSAL_INSUFFICIENT_FUNDS"
					}
					if re := t.Reject(failureCode, now); re != nil {
						return re
					}
					if re := tx.Transactions().Save(ctx, t); re != nil {
						return re
					}
					if re := tx.Outbox().Append(ctx, []Event{{ID: uc.ids.NewID(), Type: "WagerTransactionRejected", AggregateID: t.ID(), CorrelationID: t.ID(), OccurredAt: now, Data: wagerEventData(t)}}); re != nil {
						return re
					}
					out = WagerResult{Transaction: t}
					return nil
				}
				if e != nil {
					return e
				}
				final = &m.Balance
				if e = tx.Outbox().Append(ctx, []Event{{ID: uc.ids.NewID(), Type: "WalletBalanceChanged", AggregateID: w.ID(), CorrelationID: t.ID(), OccurredAt: now, Data: walletBalanceEventData(m.Entry, m.Version)}}); e != nil {
					return e
				}
			} else {
				b := w.Balance()
				final = &b
			}
			if e = t.MarkProcessed(final, now); e != nil {
				return e
			}
			if e = tx.Transactions().Save(ctx, t); e != nil {
				return e
			}
			if e = tx.Outbox().Append(ctx, []Event{{ID: uc.ids.NewID(), Type: "WagerTransactionProcessed", AggregateID: t.ID(), CorrelationID: t.ID(), OccurredAt: now, Data: wagerEventData(t)}}); e != nil {
				return e
			}
			out = WagerResult{Transaction: t, Balance: final}
			return nil
		}()
		if operationErr != nil {
			return operationErr
		}
		if delivery != nil && out.Transaction != nil {
			return tx.Inbox().Complete(ctx, delivery.Consumer, delivery.MessageID, out.Transaction.ID(), out.Transaction.FailureCode())
		}
		return nil
	})
	// A concurrent request with the same idempotency key can commit after this
	// request's initial read but before its unique insert. Resolve that race by
	// reading the winner's durable result in a fresh transaction.
	if errors.Is(err, ErrConflict) {
		var existing *wagering.WagerTransaction
		lookupErr := uc.uow.Within(ctx, func(tx Tx) error {
			if delivery != nil {
				if _, startErr := tx.Inbox().Start(ctx, delivery.Consumer, delivery.MessageID, delivery.PayloadHash); startErr != nil {
					return startErr
				}
			}
			var readErr error
			existing, readErr = tx.Transactions().ByProviderIdempotency(ctx, principal.ProviderID, in.IdempotencyKey)
			if readErr != nil {
				return readErr
			}
			if existing.PayloadHash() != in.PayloadHash {
				return ErrIdempotencyConflict
			}
			if delivery != nil {
				return tx.Inbox().Complete(ctx, delivery.Consumer, delivery.MessageID, existing.ID(), existing.FailureCode())
			}
			return nil
		})
		if lookupErr == nil {
			out = WagerResult{Transaction: existing, Balance: existing.ResultBalance(), Replayed: true, Pending: existing.Status() == wagering.Pending || existing.Status() == wagering.PendingReference}
			err = nil
		}
	}
	if err == nil && uc.cache != nil && out.Transaction != nil && uc.cacheTTL > 0 {
		_ = uc.cache.Put(ctx, cacheKey, CachedIdempotency{TransactionID: out.Transaction.ID(), PayloadHash: in.PayloadHash}, uc.cacheTTL)
	}
	return out, err
}

func movementFor(t *wagering.WagerTransaction, source *wagering.WagerTransaction) (wallet.Direction, bool) {
	switch t.Kind() {
	case wagering.Bet:
		return wallet.DebitDirection, true
	case wagering.Win, wagering.Refund:
		return wallet.CreditDirection, true
	case wagering.Loss:
		return "", false
	case wagering.Rollback:
		if source != nil && (source.Kind() == wagering.Bet) {
			return wallet.CreditDirection, true
		}
		return wallet.DebitDirection, true
	}
	return "", false
}

// RetryPendingReference resumes a durable pending reversal after a reference arrives.
type RetryPendingReference struct{ submit *SubmitWager }

func NewRetryPendingReference(submit *SubmitWager) *RetryPendingReference {
	return &RetryPendingReference{submit: submit}
}
func (uc *RetryPendingReference) Execute(ctx context.Context, providerID, idempotencyKey string) (WagerResult, error) {
	if providerID == "" || idempotencyKey == "" {
		return WagerResult{}, shared.Error("wager.retry.invalid_input", shared.ErrInvalidArgument)
	}
	var out WagerResult
	err := uc.submit.uow.Within(ctx, func(tx Tx) error {
		t, e := tx.Transactions().ByProviderIdempotency(ctx, providerID, idempotencyKey)
		if e != nil {
			return e
		}
		if t.Status() != wagering.PendingReference {
			return shared.Error("wager.retry.not_pending", shared.ErrInvalidTransition)
		}
		now := uc.submit.clock.Now().UTC()
		source, e := tx.Transactions().ByReference(ctx, providerID, t.ReferenceExternalID())
		if errors.Is(e, ErrNotFound) {
			out = WagerResult{Transaction: t, Pending: true}
			return nil
		}
		if e != nil {
			return e
		}
		if e = wagering.ValidateReversal(t, source); e != nil {
			if e = t.Reject("REFERENCE_INVALID", now); e != nil {
				return e
			}
			if e = tx.Transactions().Save(ctx, t); e != nil {
				return e
			}
			if e = tx.Outbox().Append(ctx, []Event{{ID: uc.submit.ids.NewID(), Type: "WagerTransactionRejected", AggregateID: t.ID(), CorrelationID: t.ID(), OccurredAt: now, Data: wagerEventData(t)}}); e != nil {
				return e
			}
			out = WagerResult{Transaction: t}
			return nil
		}
		w, e := tx.Wallets().ByID(ctx, t.WalletID())
		if e != nil {
			return e
		}
		direction, _ := movementFor(t, source)
		movement, e := tx.Wallets().ApplyMovement(ctx, w.ID(), t.ID(), direction, t.Amount(), now)
		if errors.Is(e, shared.ErrInsufficientFunds) {
			failureCode := "INSUFFICIENT_FUNDS"
			if t.Kind() == wagering.Rollback {
				failureCode = "REVERSAL_INSUFFICIENT_FUNDS"
			}
			if e = t.Reject(failureCode, now); e != nil {
				return e
			}
			if e = tx.Transactions().Save(ctx, t); e != nil {
				return e
			}
			if e = tx.Outbox().Append(ctx, []Event{{ID: uc.submit.ids.NewID(), Type: "WagerTransactionRejected", AggregateID: t.ID(), CorrelationID: t.ID(), OccurredAt: now, Data: wagerEventData(t)}}); e != nil {
				return e
			}
			out = WagerResult{Transaction: t}
			return nil
		}
		if e != nil {
			return e
		}
		balance := movement.Balance
		if e = t.MarkProcessed(&balance, now); e != nil {
			return e
		}
		if e = tx.Transactions().Save(ctx, t); e != nil {
			return e
		}
		if e = tx.Outbox().Append(ctx, []Event{{ID: uc.submit.ids.NewID(), Type: "WalletBalanceChanged", AggregateID: w.ID(), CorrelationID: t.ID(), OccurredAt: now, Data: walletBalanceEventData(movement.Entry, movement.Version)}, {ID: uc.submit.ids.NewID(), Type: "WagerTransactionProcessed", AggregateID: t.ID(), CorrelationID: t.ID(), OccurredAt: now, Data: wagerEventData(t)}}); e != nil {
			return e
		}
		out = WagerResult{Transaction: t, Balance: &balance}
		return nil
	})
	if err != nil {
		return WagerResult{}, err
	}
	return out, nil
}

type GetWagerTransaction struct{ uow UnitOfWork }

func NewGetWagerTransaction(uow UnitOfWork) *GetWagerTransaction {
	return &GetWagerTransaction{uow: uow}
}
func (uc *GetWagerTransaction) Execute(ctx context.Context, p Principal, id string) (*wagering.WagerTransaction, error) {
	if id == "" {
		return nil, shared.Error("wager.get.invalid_id", shared.ErrInvalidArgument)
	}
	var out *wagering.WagerTransaction
	err := uc.uow.Within(ctx, func(tx Tx) error {
		t, e := tx.Transactions().ByID(ctx, id)
		if e != nil {
			return e
		}
		if !p.Internal && (p.ProviderID == "" || t.ProviderID() != p.ProviderID) {
			return ErrUnauthorized
		}
		out = t
		return nil
	})
	return out, err
}

type GetProviderWagerTransaction struct{ uow UnitOfWork }

func NewGetProviderWagerTransaction(uow UnitOfWork) *GetProviderWagerTransaction {
	return &GetProviderWagerTransaction{uow: uow}
}
func (uc *GetProviderWagerTransaction) Execute(ctx context.Context, p Principal, externalID string) (*wagering.WagerTransaction, error) {
	if p.ProviderID == "" || externalID == "" {
		return nil, shared.Error("wager.get_provider.invalid_input", shared.ErrInvalidArgument)
	}
	var out *wagering.WagerTransaction
	err := uc.uow.Within(ctx, func(tx Tx) error {
		var e error
		out, e = tx.Transactions().ByProviderExternal(ctx, p.ProviderID, externalID)
		return e
	})
	return out, err
}
