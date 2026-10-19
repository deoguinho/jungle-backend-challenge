package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Registry is a small Prometheus text-format registry, avoiding a runtime
// dependency while keeping measurements safe across request and worker goroutines.
type Registry struct {
	mu        sync.RWMutex
	counters  map[string]uint64
	gauges    map[string]float64
	durations map[string]duration
}

type duration struct {
	count uint64
	sum   float64
}

func New() *Registry {
	return &Registry{
		counters: map[string]uint64{
			"jungle_http_requests_total": 0,
			"jungle_idempotency_replays_total": 0,
			"jungle_idempotency_conflicts_total": 0,
			"jungle_wager_errors_total": 0,
			"jungle_sqs_receive_failures_total": 0,
			"jungle_sqs_message_retries_total": 0,
			"jungle_reference_retry_failures_total": 0,
			"jungle_outbox_publish_retries_total": 0,
			"jungle_reconciliation_divergences_total": 0,
			"jungle_wager_results_total{status=\"PROCESSED\"}": 0,
			"jungle_wager_results_total{status=\"REJECTED\"}": 0,
			"jungle_wager_results_total{status=\"PENDING_REFERENCE\"}": 0,
			"jungle_wager_results_total{status=\"ERROR\"}": 0,
		},
		gauges: map[string]float64{"jungle_dlq_messages": 0, "jungle_outbox_oldest_pending_age_seconds": 0},
		durations: map[string]duration{"jungle_http_request_duration_seconds": {}, "jungle_wager_processing_duration_seconds": {}},
	}
}

var Default = New()

func (r *Registry) Inc(name string)           { r.Add(name, 1) }
func (r *Registry) Add(name string, n uint64) { r.mu.Lock(); r.counters[name] += n; r.mu.Unlock() }
func (r *Registry) Set(name string, value float64) {
	r.mu.Lock()
	r.gauges[name] = value
	r.mu.Unlock()
}
func (r *Registry) Observe(name string, elapsed time.Duration) {
	r.mu.Lock()
	d := r.durations[name]
	d.count++
	d.sum += elapsed.Seconds()
	r.durations[name] = d
	r.mu.Unlock()
}

// WagerResult implements application.WagerObserver without importing the application layer.
func (r *Registry) WagerResult(status string, replay bool, conflict bool, err error) {
	if status == "" {
		status = "ERROR"
	}
	r.Inc(fmt.Sprintf("jungle_wager_results_total{status=\"%s\"}", status))
	if replay {
		r.Inc("jungle_idempotency_replays_total")
	}
	if conflict {
		r.Inc("jungle_idempotency_conflicts_total")
	}
	if err != nil {
		r.Inc("jungle_wager_errors_total")
	}
}
func (r *Registry) ObserveWagerDuration(elapsed time.Duration) {
	r.Observe("jungle_wager_processing_duration_seconds", elapsed)
}

func (r *Registry) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	keys := make([]string, 0, len(r.counters)+len(r.gauges)+len(r.durations)*2)
	for k := range r.counters {
		keys = append(keys, k)
	}
	for k := range r.gauges {
		keys = append(keys, k)
	}
	for k := range r.durations {
		keys = append(keys, k+"_count", k+"_sum")
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v, ok := r.counters[k]; ok {
			_, _ = fmt.Fprintf(w, "%s %d\n", k, v)
			continue
		}
		if v, ok := r.gauges[k]; ok {
			_, _ = fmt.Fprintf(w, "%s %g\n", k, v)
			continue
		}
		if strings.HasSuffix(k, "_count") {
			_, _ = fmt.Fprintf(w, "%s %d\n", k, r.durations[strings.TrimSuffix(k, "_count")].count)
			continue
		}
		if strings.HasSuffix(k, "_sum") {
			_, _ = fmt.Fprintf(w, "%s %g\n", k, r.durations[strings.TrimSuffix(k, "_sum")].sum)
		}
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (r *Registry) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/metrics" {
			next.ServeHTTP(w, req)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, req)
		code := sw.status
		if code == 0 {
			code = http.StatusOK
		}
		r.Inc(fmt.Sprintf("jungle_http_requests_total{method=\"%s\",status=\"%d\"}", req.Method, code))
		r.Observe("jungle_http_request_duration_seconds", time.Since(start))
	})
}
