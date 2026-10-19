package metrics

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWagerMeasurementsAreExportedInPrometheusFormat(t *testing.T) {
	r := New()
	r.WagerResult("", false, false, nil)
	r.WagerResult("PROCESSED", false, false, nil)
	r.WagerResult("PROCESSED", true, false, nil)
	r.WagerResult("ERROR", false, true, errors.New("conflict"))
	r.ObserveWagerDuration(15 * time.Millisecond)
	r.Inc("jungle_sqs_message_retries_total")
	r.Set("jungle_dlq_messages", 2)
	r.Set("jungle_outbox_oldest_pending_age_seconds", 4.5)
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()
	for _, sample := range []string{
		`jungle_wager_results_total{status="PROCESSED"} 2`,
		"jungle_idempotency_replays_total 1",
		"jungle_idempotency_conflicts_total 1",
		"jungle_wager_errors_total 1",
		"jungle_wager_processing_duration_seconds_count 1",
		"jungle_sqs_message_retries_total 1",
		"jungle_dlq_messages 2",
		"jungle_outbox_oldest_pending_age_seconds 4.5",
	} {
		if !strings.Contains(body, sample) {
			t.Errorf("metrics output missing sample %q:\n%s", sample, body)
		}
	}
}

func TestMiddlewareRecordsRequestStatusAndDuration(t *testing.T) {
	r := New()
	wrapped := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(202) }))
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/wagering/transactions", nil))
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(response.Body.String(), `jungle_http_requests_total{method="POST",status="202"} 1`) || !strings.Contains(response.Body.String(), "jungle_http_request_duration_seconds_count 1") {
		t.Fatalf("request metrics not recorded: %s", response.Body.String())
	}
}

func TestMiddlewareCountsImplicitStatusAndSkipsScrapeRequests(t *testing.T) {
	r := New()
	wrapped := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, httptest.NewRequest("GET", "/health/live", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("implicit response status: got %d", w.Code)
	}
	scrapeRegistry := New()
	w = httptest.NewRecorder()
	scrapeRegistry.Middleware(scrapeRegistry).ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(w.Body.String(), `method="GET",status="200"`) {
		t.Fatalf("metrics scrape was counted as an application request: %s", w.Body.String())
	}
}
