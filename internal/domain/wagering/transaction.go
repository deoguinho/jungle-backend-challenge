package wagering

import (
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
)

type Kind string

const (
	Opening  Kind = "OPENING"
	Bet      Kind = "BET"
	Win      Kind = "WIN"
	Loss     Kind = "LOSS"
	Refund   Kind = "REFUND"
	Rollback Kind = "ROLLBACK"
)

type Status string

const (
	Pending          Status = "PENDING"
	PendingReference Status = "PENDING_REFERENCE"
	Processed        Status = "PROCESSED"
	Rejected         Status = "REJECTED"
	Failed           Status = "FAILED"
)

type WagerTransaction struct {
	id, providerID, externalID, idempotencyKey, payloadHash string
	playerID, walletID, roundID, gameID                     string
	kind                                                    Kind
	amount                                                  shared.Money
	referenceExternalID                                     string
	status                                                  Status
	failureCode                                             string
	resultBalance                                           *shared.Money
	createdAt, updatedAt                                    time.Time
	reversed                                                bool
}

type ExternalParams struct {
	ID, ProviderID, ExternalID, IdempotencyKey, PayloadHash string
	PlayerID, WalletID, RoundID, GameID                     string
	Kind                                                    Kind
	Amount                                                  shared.Money
	ReferenceExternalID                                     string
	CreatedAt                                               time.Time
}

func NewExternal(p ExternalParams) (*WagerTransaction, error) {
	if p.ID == "" || p.ProviderID == "" || p.ExternalID == "" || p.IdempotencyKey == "" || p.PayloadHash == "" || p.PlayerID == "" || p.WalletID == "" || p.RoundID == "" || p.GameID == "" || p.CreatedAt.IsZero() {
		return nil, shared.Error("wager.missing_required_field", shared.ErrInvalidArgument)
	}
	if p.Kind != Bet && p.Kind != Win && p.Kind != Loss && p.Kind != Refund && p.Kind != Rollback {
		return nil, shared.Error("wager.invalid_external_kind", shared.ErrInvalidOperation)
	}
	if p.Amount.Currency() == "" || p.Amount.IsNegative() || (p.Kind == Loss && !p.Amount.IsZero()) || (p.Kind != Loss && !p.Amount.IsPositive()) {
		return nil, shared.Error("wager.invalid_amount", shared.ErrInvalidMoney)
	}
	if ((p.Kind == Refund || p.Kind == Rollback) && p.ReferenceExternalID == "") || ((p.Kind == Bet || p.Kind == Loss) && p.ReferenceExternalID != "") {
		return nil, shared.Error("wager.invalid_reference_presence", shared.ErrInvalidReference)
	}
	t := &WagerTransaction{id: p.ID, providerID: p.ProviderID, externalID: p.ExternalID, idempotencyKey: p.IdempotencyKey, payloadHash: p.PayloadHash,
		playerID: p.PlayerID, walletID: p.WalletID, roundID: p.RoundID, gameID: p.GameID, kind: p.Kind, amount: p.Amount,
		referenceExternalID: p.ReferenceExternalID, status: Pending, createdAt: p.CreatedAt.UTC(), updatedAt: p.CreatedAt.UTC()}
	return t, nil
}

func NewOpening(id, playerID, walletID string, amount shared.Money, now time.Time) (*WagerTransaction, error) {
	if id == "" || playerID == "" || walletID == "" || now.IsZero() || amount.Currency() == "" || amount.IsNegative() {
		return nil, shared.Error("wager.invalid_opening", shared.ErrInvalidArgument)
	}
	return &WagerTransaction{id: id, playerID: playerID, walletID: walletID, kind: Opening, amount: amount, status: Processed,
		createdAt: now.UTC(), updatedAt: now.UTC()}, nil
}

// RehydrateOpening restores an already persisted OPENING transaction without
// replaying its original wallet movement or producing events.
func RehydrateOpening(id, playerID, walletID string, amount shared.Money, status Status, createdAt, updatedAt time.Time) (*WagerTransaction, error) {
	if id == "" || playerID == "" || walletID == "" || amount.Currency() == "" || amount.IsNegative() || status != Processed || createdAt.IsZero() || updatedAt.IsZero() || updatedAt.Before(createdAt) {
		return nil, shared.Error("wager.invalid_persisted_opening", shared.ErrInvalidArgument)
	}
	return &WagerTransaction{id: id, playerID: playerID, walletID: walletID, kind: Opening, amount: amount, status: Processed,
		createdAt: createdAt.UTC(), updatedAt: updatedAt.UTC()}, nil
}

// Rehydrate restores persisted state without replaying transitions or effects.
func Rehydrate(p ExternalParams, status Status, failureCode string, resultBalance *shared.Money, updatedAt time.Time) (*WagerTransaction, error) {
	t, err := NewExternal(p)
	if err != nil {
		return nil, err
	}
	if !knownStatus(status) || updatedAt.IsZero() || (status == Rejected || status == Failed) != (failureCode != "") {
		return nil, shared.Error("wager.invalid_persisted_state", shared.ErrInvalidArgument)
	}
	if (status == Processed) != (resultBalance != nil) {
		return nil, shared.Error("wager.invalid_result_snapshot", shared.ErrInvalidArgument)
	}
	if resultBalance != nil && resultBalance.Currency() != p.Amount.Currency() {
		return nil, shared.Error("wager.result_currency_mismatch", shared.ErrCurrencyMismatch)
	}
	t.status, t.failureCode, t.resultBalance, t.updatedAt = status, failureCode, cloneMoney(resultBalance), updatedAt.UTC()
	return t, nil
}

func (t *WagerTransaction) MarkPendingReference(now time.Time) error {
	if err := t.canTransition(now); err != nil {
		return err
	}
	if t.kind != Refund && t.kind != Rollback {
		return shared.Error("wager.reference_not_applicable", shared.ErrInvalidReference)
	}
	t.status, t.updatedAt = PendingReference, now.UTC()
	return nil
}

func (t *WagerTransaction) MarkProcessed(resultBalance *shared.Money, now time.Time) error {
	if err := t.canTransition(now); err != nil {
		return err
	}
	if resultBalance == nil || resultBalance.Currency() != t.amount.Currency() || resultBalance.IsNegative() {
		return shared.Error("wager.invalid_result_balance", shared.ErrInvalidMoney)
	}
	t.status, t.resultBalance, t.updatedAt = Processed, cloneMoney(resultBalance), now.UTC()
	return nil
}

func (t *WagerTransaction) Reject(code string, now time.Time) error {
	return t.finish(Rejected, code, now)
}
func (t *WagerTransaction) Fail(code string, now time.Time) error { return t.finish(Failed, code, now) }

func (t *WagerTransaction) finish(status Status, code string, now time.Time) error {
	if err := t.canTransition(now); err != nil {
		return err
	}
	if code == "" {
		return shared.Error("wager.missing_failure_code", shared.ErrInvalidArgument)
	}
	t.status, t.failureCode, t.updatedAt = status, code, now.UTC()
	return nil
}

func (t *WagerTransaction) canTransition(now time.Time) error {
	if t == nil || now.IsZero() {
		return shared.Error("wager.invalid_transition_time", shared.ErrInvalidArgument)
	}
	if t.status == Processed || t.status == Rejected || t.status == Failed {
		return shared.Error("wager.terminal", shared.ErrTerminalTransaction)
	}
	if !now.Before(t.updatedAt) {
		return nil
	}
	return shared.Error("wager.time_regression", shared.ErrInvalidTransition)
}

func (t *WagerTransaction) ID() string                   { return t.id }
func (t *WagerTransaction) ProviderID() string           { return t.providerID }
func (t *WagerTransaction) ExternalID() string           { return t.externalID }
func (t *WagerTransaction) IdempotencyKey() string       { return t.idempotencyKey }
func (t *WagerTransaction) PayloadHash() string          { return t.payloadHash }
func (t *WagerTransaction) PlayerID() string             { return t.playerID }
func (t *WagerTransaction) WalletID() string             { return t.walletID }
func (t *WagerTransaction) RoundID() string              { return t.roundID }
func (t *WagerTransaction) GameID() string               { return t.gameID }
func (t *WagerTransaction) Kind() Kind                   { return t.kind }
func (t *WagerTransaction) Amount() shared.Money         { return t.amount }
func (t *WagerTransaction) ReferenceExternalID() string  { return t.referenceExternalID }
func (t *WagerTransaction) Status() Status               { return t.status }
func (t *WagerTransaction) FailureCode() string          { return t.failureCode }
func (t *WagerTransaction) ResultBalance() *shared.Money { return cloneMoney(t.resultBalance) }
func (t *WagerTransaction) CreatedAt() time.Time         { return t.createdAt }
func (t *WagerTransaction) UpdatedAt() time.Time         { return t.updatedAt }

func (t *WagerTransaction) MarkReversed(now time.Time) error {
	if t == nil || t.status != Processed || (t.kind != Bet && t.kind != Win && t.kind != Refund) || now.IsZero() {
		return shared.Error("wager.source_not_reversible", shared.ErrInvalidReference)
	}
	if t.reversed {
		return shared.Error("wager.duplicate_reversal", shared.ErrDuplicateReversal)
	}
	t.reversed = true
	return nil
}

// ValidateReversal enforces full reversals and shared transaction context.
func ValidateReversal(reversal, source *WagerTransaction) error {
	if reversal == nil || source == nil || reversal.referenceExternalID != source.externalID || reversal.providerID != source.providerID || reversal.playerID != source.playerID || reversal.walletID != source.walletID || reversal.roundID != source.roundID || reversal.amount.Currency() != source.amount.Currency() {
		return shared.Error("wager.reference_context_mismatch", shared.ErrInvalidReference)
	}
	if source.status != Processed {
		return shared.Error("wager.reference_not_processed", shared.ErrInvalidReference)
	}
	if reversal.kind == Refund && source.kind != Bet {
		return shared.Error("wager.refund_requires_bet", shared.ErrInvalidReference)
	}
	if reversal.kind == Rollback && source.kind != Bet && source.kind != Win && source.kind != Refund {
		return shared.Error("wager.rollback_source_invalid", shared.ErrInvalidReference)
	}
	if reversal.amount.Amount().Cmp(source.amount.Amount()) != 0 {
		return shared.Error("wager.partial_reversal_unsupported", shared.ErrInvalidReference)
	}
	if source.reversed {
		return shared.Error("wager.duplicate_reversal", shared.ErrDuplicateReversal)
	}
	return nil
}

func knownStatus(s Status) bool {
	return s == Pending || s == PendingReference || s == Processed || s == Rejected || s == Failed
}
func cloneMoney(m *shared.Money) *shared.Money {
	if m == nil {
		return nil
	}
	v := *m
	return &v
}
