package application

import (
	"context"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wagering"
)

type ExpirePendingReference struct {
	uow   UnitOfWork
	ids   IDGenerator
	clock Clock
}

func NewExpirePendingReference(uow UnitOfWork, ids IDGenerator, clock Clock) *ExpirePendingReference {
	return &ExpirePendingReference{uow: uow, ids: ids, clock: clock}
}
func (uc *ExpirePendingReference) Execute(ctx context.Context, providerID, key string) error {
	return uc.uow.Within(ctx, func(tx Tx) error {
		t, err := tx.Transactions().ByProviderIdempotency(ctx, providerID, key)
		if err != nil {
			return err
		}
		if t.Status() != wagering.PendingReference {
			return nil
		}
		now := uc.clock.Now().UTC()
		if err = t.Reject("REFERENCE_NOT_FOUND", now); err != nil {
			return err
		}
		if err = tx.Transactions().Save(ctx, t); err != nil {
			return err
		}
		return tx.Outbox().Append(ctx, []Event{{ID: uc.ids.NewID(), Type: "WagerTransactionRejected", AggregateID: t.ID(), CorrelationID: t.ID(), OccurredAt: now, Data: wagerEventData(t)}})
	})
}
