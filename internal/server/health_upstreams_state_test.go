package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHealthUpstreamsHealthyAndDegradedResponses completes the
// "credential-free healthy and degraded responses from both health endpoints"
// contract for the /health/upstreams half — the /health/credentials half is
// pinned by TestCredentialsHealthStateMatrix and
// TestCredentialHealthSentinelResponseCarriesNoCredentialValues. One live
// response carries both shapes: an upstream whose last-2xx succeeded under a
// closed breaker renders healthy, and an upstream with attempts but no
// success under an open short-retry breaker renders the degraded verdict the
// endpoint exists to surface — still flagged healthy (a sub-300s retry-after
// is transient, not permanent), with the open breaker and failed attempts
// reported for the operator to act on. The whole body is walked against a
// closed key set so a credential value, its vault path or any other
// secret-bearing field fails the pin, mirroring the /health/credentials walk.
func TestHealthUpstreamsHealthyAndDegradedResponses(t *testing.T) {
	s := newRouteTableHealthTestServer()

	// Healthy: a recorded 2xx this restart, breaker closed.
	s.last2xxTracker.RecordAttempt("", "https://healthy.example")
	s.last2xxTracker.RecordSuccess("", "https://healthy.example", "")
	s.CircuitBreakerStates().Set(CircuitBreakerStatus{
		Origin:  "https://healthy.example",
		State:   CircuitBreakerClosed,
		Enabled: true,
	})

	// Degraded: attempts but no success since restart under an open breaker
	// whose retry-after is below the permanent-failure threshold.
	s.last2xxTracker.RecordAttempt("", "https://degraded.example")
	s.last2xxTracker.RecordAttempt("", "https://degraded.example")
	s.last2xxTracker.RecordError("", "https://degraded.example", "upstream request timed out")
	s.CircuitBreakerStates().Set(CircuitBreakerStatus{
		Origin:              "https://degraded.example",
		State:               CircuitBreakerOpen,
		Enabled:             true,
		ConsecutiveFailures: 3,
		LastError:           "upstream request timed out",
		RetryAfterSeconds:   30, // <= 300: transient, so the entry stays flagged healthy
		Source:              "caller",
	})

	rec := httptest.NewRecorder()
	s.healthUpstreamsHandler(rec, httptest.NewRequest(http.MethodGet, "/health/upstreams", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	// The handler owns the no-store directive pinned for sentinel traffic
	// through the middleware chain elsewhere; assert it at the source.
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Closed schema: every key the body contains must come from the
	// documented set. A field added for a credential value, its vault path or
	// any other secret-bearing metadata fails this walk.
	allowed := map[string]bool{
		"timestamp": true, "upstreams": true, "route_table": true,
		"upstream": true, "last_2xx": true, "circuit_breaker": true, "healthy": true,
		"path": true, "state": true, "last_attempt_at": true, "last_success_at": true,
		"attempts_since_last_success": true, "last_error": true, "source": true,
		"origin": true, "enabled": true, "consecutive_failures": true,
		"opened_at": true, "retry_after_seconds": true,
		"fragment_mode": true, "fragments_loaded": true, "fragments_quarantined": true,
		"routes": true, "last_loaded": true, "hot_reload": true,
		"in_progress": true, "reload_count": true, "failure_count": true,
		"last_reload_time": true,
	}
	var walk func(prefix string, value map[string]any)
	walk = func(prefix string, value map[string]any) {
		for key, child := range value {
			if !allowed[key] {
				t.Errorf("unexpected key %q in upstream health response (only documented fields may appear)", prefix+key)
			}
			for _, banned := range []string{"credential", "token", "secret", "password"} {
				if strings.Contains(strings.ToLower(key), banned) {
					t.Errorf("key %q must never appear in an upstream health response", prefix+key)
				}
			}
			if nested, ok := child.(map[string]any); ok {
				walk(prefix+key+".", nested)
			}
		}
	}
	walk("", body)

	upstreams, ok := body["upstreams"].([]any)
	if !ok {
		t.Fatalf("expected upstreams array, got %T", body["upstreams"])
	}
	if len(upstreams) != 2 {
		t.Fatalf("expected 2 upstream entries, got %d: %s", len(upstreams), rec.Body.String())
	}
	byOrigin := make(map[string]map[string]any, len(upstreams))
	for _, raw := range upstreams {
		entry, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("upstream entry is %T, want object", raw)
		}
		origin, _ := entry["upstream"].(string)
		byOrigin[origin] = entry
	}

	// The healthy shape: success recorded, breaker closed, flagged healthy.
	healthy, ok := byOrigin["https://healthy.example"]
	if !ok {
		t.Fatal("the healthy upstream is missing from the response")
	}
	if healthy["healthy"] != true {
		t.Errorf("healthy upstream: healthy = %v, want true", healthy["healthy"])
	}
	last2xx, ok := healthy["last_2xx"].(map[string]any)
	if !ok {
		t.Fatalf("healthy upstream: last_2xx = %v, want object", healthy["last_2xx"])
	}
	if last2xx["state"] != string(Last2xxSucceeded) {
		t.Errorf("healthy upstream: last_2xx.state = %v, want %q", last2xx["state"], Last2xxSucceeded)
	}
	if got := last2xx["attempts_since_last_success"]; got != float64(0) {
		t.Errorf("healthy upstream: attempts_since_last_success = %v, want 0", got)
	}
	breaker, ok := healthy["circuit_breaker"].(map[string]any)
	if !ok {
		t.Fatalf("healthy upstream: circuit_breaker = %v, want object", healthy["circuit_breaker"])
	}
	if breaker["state"] != string(CircuitBreakerClosed) {
		t.Errorf("healthy upstream: circuit_breaker.state = %v, want %q", breaker["state"], CircuitBreakerClosed)
	}

	// The degraded shape: no success since restart under an open breaker, but
	// flagged healthy because the retry-after is below the permanent
	// threshold — the open breaker and the failed attempts are the operator's
	// signal, not a health flag flip.
	degraded, ok := byOrigin["https://degraded.example"]
	if !ok {
		t.Fatal("the degraded upstream is missing from the response")
	}
	if degraded["healthy"] != true {
		t.Errorf("degraded upstream (retry-after 30s): healthy = %v, want true (transient, not permanent)", degraded["healthy"])
	}
	last2xx, ok = degraded["last_2xx"].(map[string]any)
	if !ok {
		t.Fatalf("degraded upstream: last_2xx = %v, want object", degraded["last_2xx"])
	}
	if last2xx["state"] != string(Last2xxNoSuccess) {
		t.Errorf("degraded upstream: last_2xx.state = %v, want %q", last2xx["state"], Last2xxNoSuccess)
	}
	if got := last2xx["attempts_since_last_success"]; got != float64(2) {
		t.Errorf("degraded upstream: attempts_since_last_success = %v, want 2", got)
	}
	if got := last2xx["last_error"]; got != "upstream request timed out" {
		t.Errorf("degraded upstream: last_2xx.last_error = %v, want the recorded error", got)
	}
	breaker, ok = degraded["circuit_breaker"].(map[string]any)
	if !ok {
		t.Fatalf("degraded upstream: circuit_breaker = %v, want object", degraded["circuit_breaker"])
	}
	if breaker["state"] != string(CircuitBreakerOpen) {
		t.Errorf("degraded upstream: circuit_breaker.state = %v, want %q", breaker["state"], CircuitBreakerOpen)
	}
	if got := breaker["consecutive_failures"]; got != float64(3) {
		t.Errorf("degraded upstream: consecutive_failures = %v, want 3", got)
	}
	if got := breaker["retry_after_seconds"]; got != float64(30) {
		t.Errorf("degraded upstream: retry_after_seconds = %v, want 30", got)
	}
}
