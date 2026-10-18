package application

import (
	"context"
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wagering"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

var (
	ErrNotFound            = errors.New("application: resource not found")
	ErrConflict            = errors.New("application: conflicting resource")
	ErrUnauthorized        = errors.New("application: unauthorized")
	ErrIdempotencyConflict = errors.New("application: idempotency key reused with different payload")
)

// UnitOfWork defines the atomic boundary. Adapters must commit all writes in
// the callback or roll them all back.
type UnitOfWork interface {
	Within(context.Context, func(Tx) error) error
}

type Tx interface {
	Wallets() WalletStore
	Transactions() TransactionStore
	Ledger() LedgerStore
	Outbox() OutboxStore
	Inbox() InboxStore
}

type WalletStore interface {
	Create(context.Context, *wallet.Wallet) error
	ByID(context.Context, string) (*wallet.Wallet, error)
	ByPlayerCurrency(context.Context, string, string) (*wallet.Wallet, error)
	// ApplyMovement atomically changes the persisted balance by amount using a
	// decimal string/NUMERIC delta, increments the version, and appends ledger.
	ApplyMovement(context.Context, string, string, wallet.Direction, shared.Money, time.Time) (MovementResult, error)
}

type MovementResult struct {
	Entry   *wallet.LedgerEntry
	Balance shared.Money
	Version int64
}

type TransactionStore interface {
	Create(context.Context, *wagering.WagerTransaction) error
	Save(context.Context, *wagering.WagerTransaction) error
	ByID(context.Context, string) (*wagering.WagerTransaction, error)
	ByProviderExternal(context.Context, string, string) (*wagering.WagerTransaction, error)
	ByProviderIdempotency(context.Context, string, string) (*wagering.WagerTransaction, error)
	ByReference(context.Context, string, string) (*wagering.WagerTransaction, error)
}

type LedgerStore interface {
	Append(context.Context, *wallet.LedgerEntry) error
	List(context.Context, string, string, int) (LedgerPage, error)
	Reconcile(context.Context, string, string) (stored, calculated shared.Money, entries int64, err error)
}

type OutboxStore interface {
	Append(context.Context, []Event) error
}

// InboxStore participates in the same unit of work as financial processing so
// a message is complete only with its durable application result.
type InboxStore interface {
	Start(context.Context, string, string, []byte) (completed bool, err error)
	Complete(context.Context, string, string, string, string) error
}

// IdempotencyCache is optional acceleration only. A miss/error never changes
// the authoritative PostgreSQL lookup outcome.
type IdempotencyCache interface {
	Get(context.Context, string) (CachedIdempotency, bool, error)
	Put(context.Context, string, CachedIdempotency, time.Duration) error
}

// WagerObserver reports application outcomes without coupling use cases to a metrics vendor.
type WagerObserver interface {
	WagerResult(status string, replay bool, conflict bool, err error)
	ObserveWagerDuration(elapsed time.Duration)
}
type CachedIdempotency struct{ TransactionID, PayloadHash string }

type Event struct {
	ID, Type, AggregateID, CorrelationID string
	OccurredAt                           time.Time
	Data                                 any
}

type IDGenerator interface{ NewID() string }
type Clock interface{ Now() time.Time }

type Principal struct {
	PlayerID, ProviderID string
	Internal             bool
}

type WalletView struct {
	ID, PlayerID, Currency string
	Balance                shared.Money
	Version                int64
	CreatedAt, UpdatedAt   time.Time
}
type LedgerPage struct {
	Items      []*wallet.LedgerEntry
	NextCursor string
}
type Reconciliation struct {
	StoredBalance, CalculatedBalance, Difference shared.Money
	Consistent                                   bool
	Entries                                      int64
}

func walletView(w *wallet.Wallet) WalletView {
	return WalletView{ID: w.ID(), PlayerID: w.PlayerID(), Currency: w.Currency(), Balance: w.Balance(), Version: w.Version(), CreatedAt: w.CreatedAt(), UpdatedAt: w.UpdatedAt()}
}
