package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer() *Server {
	return NewServer(nil, UseCases{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), "http://localhost:8082")
}

func TestHealthAndMetricsEndpoints(t *testing.T) {
	s := testServer()
	for path, want := range map[string]int{"/health/live": http.StatusOK, "/health/ready": http.StatusOK, "/metrics": http.StatusOK} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("GET %s: got status %d, want %d", path, w.Code, want)
		}
	}
}

func TestCORSAllowsConfiguredOriginAndRejectsUnknownOrigin(t *testing.T) {
	s := testServer()
	allowed := httptest.NewRequest(http.MethodOptions, "/wallets", nil)
	allowed.Header.Set("Origin", "http://localhost:8082")
	allowed.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, allowed)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:8082" {
		t.Fatalf("allowed preflight: status=%d headers=%v", w.Code, w.Header())
	}

	blocked := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	blocked.Header.Set("Origin", "https://attacker.invalid")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, blocked)
	if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unknown origin was allowed: status=%d headers=%v", w.Code, w.Header())
	}
}

func TestProtectedWalletRouteRejectsMissingBearerToken(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/wallets", strings.NewReader(`{"playerId":"00000000-0000-4000-8000-000000000001","initialBalance":{"amount":"1.00","currency":"BRL"}}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	testServer().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated wallet open: got %d, want 401", w.Code)
	}
}
