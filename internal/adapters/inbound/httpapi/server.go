package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/application"
	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wagering"
)

type Readiness func(context.Context) error
type UseCases struct {
	OpenWallet       *application.OpenWallet
	GetWallet        *application.GetWallet
	ListLedger       *application.ListWalletLedger
	Reconcile        *application.ReconcileWallet
	SubmitWager      *application.SubmitWager
	GetWager         *application.GetWagerTransaction
	GetProviderWager *application.GetProviderWagerTransaction
}

type Server struct {
	auth    *Authenticator
	use     UseCases
	ready   Readiness
	logger  *slog.Logger
	handler http.Handler
}

func NewServer(auth *Authenticator, cases UseCases, ready Readiness, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{auth: auth, use: cases, ready: ready, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "live"})
	})
	mux.HandleFunc("GET /health/ready", s.handleReady)
	mux.HandleFunc("POST /wallets", s.handleOpenWallet)
	mux.HandleFunc("GET /wallets/{walletID}", s.handleGetWallet)
	mux.HandleFunc("GET /wallets/{walletID}/ledger", s.handleListLedger)
	mux.HandleFunc("POST /wallets/{walletID}/reconciliation", s.handleReconcile)
	mux.HandleFunc("POST /wagering/transactions", s.handleSubmitWager)
	mux.HandleFunc("GET /wagering/transactions/{transactionID}", s.handleGetWager)
	mux.HandleFunc("GET /providers/{providerID}/wagering/transactions/{externalTransactionID}", s.handleGetProviderWager)
	s.handler = s.recover(mux)
	return s
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.ready != nil {
		if err := s.ready(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}
func (s *Server) handleOpenWallet(w http.ResponseWriter, r *http.Request) {
	p, err := s.principal(r)
	if err != nil {
		s.writeError(w, err)
		return
	}
	if !p.Internal {
		s.writeError(w, application.ErrUnauthorized)
		return
	}
	var req struct {
		PlayerID       string   `json:"playerId"`
		InitialBalance moneyDTO `json:"initialBalance"`
	}
	if err = decodeBody(w, r, &req); err != nil {
		s.writeError(w, err)
		return
	}
	amount, err := shared.NewMoney(req.InitialBalance.Amount, req.InitialBalance.Currency)
	if err != nil {
		s.writeError(w, err)
		return
	}
	out, err := s.use.OpenWallet.Execute(r.Context(), application.OpenWalletInput{PlayerID: req.PlayerID, Initial: amount})
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": out.Wallet.ID, "playerId": out.Wallet.PlayerID, "balance": moneyResponse(out.Wallet.Balance), "version": out.Wallet.Version, "openingTransactionId": out.OpeningTransactionID})
}
func (s *Server) handleGetWallet(w http.ResponseWriter, r *http.Request) {
	p, err := s.principal(r)
	if err != nil {
		s.writeError(w, err)
		return
	}
	out, err := s.use.GetWallet.Execute(r.Context(), p, r.PathValue("walletID"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": out.ID, "playerId": out.PlayerID, "balance": map[string]string{"amount": out.Balance.String(), "currency": out.Currency}, "version": out.Version, "createdAt": out.CreatedAt, "updatedAt": out.UpdatedAt})
}
func (s *Server) handleListLedger(w http.ResponseWriter, r *http.Request) {
	p, err := s.principal(r)
	if err != nil {
		s.writeError(w, err)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			s.writeError(w, shared.Error("http.invalid_limit", shared.ErrInvalidArgument))
			return
		}
	}
	page, err := s.use.ListLedger.Execute(r.Context(), p, r.PathValue("walletID"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.writeError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, e := range page.Items {
		items = append(items, map[string]any{"id": e.TransactionID(), "walletId": e.WalletID(), "direction": e.Direction(), "money": moneyResponse(e.Amount()), "balanceBefore": moneyResponse(e.BalanceBefore()), "balanceAfter": moneyResponse(e.BalanceAfter()), "createdAt": e.CreatedAt()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": page.NextCursor})
}
func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	p, err := s.principal(r)
	if err != nil {
		s.writeError(w, err)
		return
	}
	out, err := s.use.Reconcile.Execute(r.Context(), p, r.PathValue("walletID"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"walletId": r.PathValue("walletID"), "storedBalance": moneyResponse(out.StoredBalance), "calculatedBalance": moneyResponse(out.CalculatedBalance), "difference": moneyResponse(out.Difference), "consistent": out.Consistent, "checkedEntries": out.Entries})
}

type moneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}
type wagerDTO struct {
	ProviderID                     string        `json:"providerId"`
	ExternalTransactionID          string        `json:"externalTransactionId"`
	PlayerID                       string        `json:"playerId"`
	WalletID                       string        `json:"walletId"`
	RoundID                        string        `json:"roundId"`
	GameID                         string        `json:"gameId"`
	Kind                           wagering.Kind `json:"kind"`
	Money                          moneyDTO      `json:"money"`
	ReferenceExternalTransactionID string        `json:"referenceExternalTransactionId,omitempty"`
}

func (s *Server) handleSubmitWager(w http.ResponseWriter, r *http.Request) {
	p, err := s.principal(r)
	if err != nil {
		s.writeError(w, err)
		return
	}
	if p.Internal || p.ProviderID == "" {
		s.writeError(w, application.ErrUnauthorized)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if strings.TrimSpace(key) == "" {
		s.writeError(w, shared.Error("http.idempotency_key_required", shared.ErrInvalidArgument))
		return
	}
	var req wagerDTO
	if err = decodeBody(w, r, &req); err != nil {
		s.writeError(w, err)
		return
	}
	if req.ProviderID != "" && req.ProviderID != p.ProviderID {
		s.writeError(w, application.ErrUnauthorized)
		return
	}
	money, err := shared.NewMoney(req.Money.Amount, req.Money.Currency)
	if err != nil {
		s.writeError(w, err)
		return
	}
	in := application.SubmitWagerInput{ExternalID: req.ExternalTransactionID, IdempotencyKey: key, PlayerID: req.PlayerID, WalletID: req.WalletID, RoundID: req.RoundID, GameID: req.GameID, Kind: req.Kind, Amount: money, ReferenceExternalID: req.ReferenceExternalTransactionID}
	hash, err := application.CanonicalPayloadHash(p.ProviderID, in)
	if err != nil {
		s.writeError(w, err)
		return
	}
	in.PayloadHash = hash
	out, err := s.use.SubmitWager.Execute(r.Context(), p, in)
	if err != nil {
		s.writeError(w, err)
		return
	}
	status := http.StatusOK
	if out.Pending {
		status = http.StatusAccepted
	} else if out.Transaction != nil && out.Transaction.Status() == wagering.Rejected {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, wagerResult(out))
}
func (s *Server) handleGetWager(w http.ResponseWriter, r *http.Request) {
	p, err := s.principal(r)
	if err != nil {
		s.writeError(w, err)
		return
	}
	tx, err := s.use.GetWager.Execute(r.Context(), p, r.PathValue("transactionID"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, transactionResponse(tx, false))
}
func (s *Server) handleGetProviderWager(w http.ResponseWriter, r *http.Request) {
	p, err := s.principal(r)
	if err != nil {
		s.writeError(w, err)
		return
	}
	if p.ProviderID != r.PathValue("providerID") {
		s.writeError(w, application.ErrUnauthorized)
		return
	}
	tx, err := s.use.GetProviderWager.Execute(r.Context(), p, r.PathValue("externalTransactionID"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, transactionResponse(tx, false))
}

func (s *Server) principal(r *http.Request) (application.Principal, error) {
	if s.auth == nil {
		return application.Principal{}, application.ErrUnauthorized
	}
	return s.auth.Principal(r.Context(), r.Header.Get("Authorization"))
}
func decodeBody(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return shared.Error("http.invalid_json", shared.ErrInvalidArgument)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return shared.Error("http.multiple_json_values", shared.ErrInvalidArgument)
	}
	return nil
}
func wagerResult(out application.WagerResult) map[string]any {
	result := transactionResponse(out.Transaction, out.Replayed)
	if out.Balance != nil {
		result["balance"] = moneyResponse(*out.Balance)
	}
	result["idempotentReplay"] = out.Replayed
	return result
}
func transactionResponse(t *wagering.WagerTransaction, replay bool) map[string]any {
	result := map[string]any{"transactionId": t.ID(), "status": t.Status(), "idempotentReplay": replay}
	if t.FailureCode() != "" {
		result["failureCode"] = t.FailureCode()
	}
	if b := t.ResultBalance(); b != nil {
		result["balance"] = moneyResponse(*b)
	}
	if t.Status() == wagering.PendingReference || t.Status() == wagering.Pending {
		result["pending"] = true
	}
	return result
}
func moneyResponse(m shared.Money) map[string]string {
	return map[string]string{"amount": m.String(), "currency": m.Currency()}
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (s *Server) writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	message := "request could not be completed"
	var domainErr *shared.DomainError
	switch {
	case errors.Is(err, application.ErrUnauthorized):
		status = http.StatusUnauthorized
		code = "unauthorized"
		message = "authentication or authorization failed"
	case errors.Is(err, application.ErrNotFound):
		status = http.StatusNotFound
		code = "not_found"
		message = "resource not found"
	case errors.Is(err, application.ErrIdempotencyConflict):
		status = http.StatusConflict
		code = "idempotency_conflict"
		message = "idempotency key was used with different content"
	case errors.Is(err, application.ErrConflict):
		status = http.StatusConflict
		code = "conflict"
		message = "resource conflict"
	case errors.Is(err, shared.ErrInvalidArgument) || errors.Is(err, shared.ErrInvalidMoney) || errors.Is(err, shared.ErrCurrencyMismatch) || errors.Is(err, shared.ErrInvalidOperation) || errors.Is(err, shared.ErrInvalidReference):
		status = http.StatusBadRequest
		code = "invalid_request"
		message = "request validation failed"
	case errors.Is(err, shared.ErrInsufficientFunds) || errors.Is(err, shared.ErrDuplicateReversal):
		status = http.StatusUnprocessableEntity
		code = "business_rejection"
		message = "operation rejected by business rules"
	}
	if errors.As(err, &domainErr) && domainErr.Code != "" {
		code = domainErr.Code
	}
	s.logger.Warn("http request failed", "error", err, "code", code)
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.logger.Error("panic recovered", "value", fmt.Sprint(v))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "internal_error", "message": "request could not be completed"})
			}
		}()
		if r.Header.Get("X-Correlation-ID") == "" {
			r.Header.Set("X-Correlation-ID", uuid.NewString())
		}
		next.ServeHTTP(w, r)
	})
}

var _ http.Handler = (*Server)(nil)
