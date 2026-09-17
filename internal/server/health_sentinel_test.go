package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newHealthSentinelTestServer(t *testing.T) *Server {
	t.Helper()
	s := New(&Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	})
	// /health/credentials is a reserved operator surface gated on
	// seam:ops:read, so stage 3 resolves the caller ahead of the handler; a
	// loopback caller resolves to none and is denied with 403. Present the
	// fixed test identity so each test reaches the sentinel it exercises.
	s.identityResolver = newLoopbackTestIdentityResolver()
	return s
}

func TestCredentialHealthSentinelIncludesCircuitBreakerState(t *testing.T) {
	s := newHealthSentinelTestServer(t)
	openedAt := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	s.CircuitBreakerStates().Set(CircuitBreakerStatus{
		Origin:              "https://upstream.example",
		State:               CircuitBreakerOpen,
		Enabled:             true,
		ConsecutiveFailures: 5,
		OpenedAt:            &openedAt,
		LastError:           "upstream request timed out",
		RetryAfterSeconds:   21,
		Source:              "caller",
	})

	req := httptest.NewRequest(http.MethodGet, "/health/credentials", nil)
	resp := httptest.NewRecorder()
	// Stage 3 sits outside the operator mux in production and is what puts the
	// identity the mux's scope gate reads into the request context.
	s.identityResolutionMiddleware(s.operatorMux).ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected health status 200, got %d", resp.Code)
	}
	var health CredentialHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if health.Status != "unhealthy" {
		t.Fatalf("expected open breaker to make credential health unhealthy, got %q", health.Status)
	}
	if health.CircuitBreaker.State != CircuitBreakerOpen {
		t.Fatalf("expected aggregate breaker state %q, got %q", CircuitBreakerOpen, health.CircuitBreaker.State)
	}
	if len(health.CircuitBreakers) != 1 {
		t.Fatalf("expected one breaker status, got %d", len(health.CircuitBreakers))
	}
	if health.CircuitBreakers[0].LastError != "upstream request timed out" {
		t.Fatalf("expected last breaker error in health response, got %q", health.CircuitBreakers[0].LastError)
	}
	if got := resp.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected no-store cache directive, got %q", got)
	}
}

func TestCredentialHealthSentinelCacheBypassIsFresh(t *testing.T) {
	s := newHealthSentinelTestServer(t)
	s.cacheTTLs["/health/credentials"] = 300
	// Stage 3 outside the mux, matching the production operator chain, so the
	// scope gate inside the mux sees the resolved test identity.
	handler := s.identityResolutionMiddleware(s.cacheMiddleware(s.operatorMux))

	s.CircuitBreakerStates().Set(CircuitBreakerStatus{
		Origin:  "https://upstream.example",
		State:   CircuitBreakerClosed,
		Enabled: true,
	})
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/health/credentials", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first health request: expected 200, got %d", first.Code)
	}
	firstStats := s.cache.Stats()

	// Change the live state between requests. A cached health response would
	// incorrectly continue to report the closed state.
	s.CircuitBreakerStates().Set(CircuitBreakerStatus{
		Origin:              "https://upstream.example",
		State:               CircuitBreakerOpen,
		Enabled:             true,
		ConsecutiveFailures: 5,
	})
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/health/credentials", nil))
	if second.Code != http.StatusOK {
		t.Fatalf("second health request: expected 200, got %d", second.Code)
	}

	var health CredentialHealthResponse
	if err := json.NewDecoder(second.Body).Decode(&health); err != nil {
		t.Fatalf("decode second health response: %v", err)
	}
	if health.CircuitBreaker.State != CircuitBreakerOpen {
		t.Fatalf("health response was stale or missing breaker state: got %q", health.CircuitBreaker.State)
	}
	stats := s.cache.Stats()
	if stats.Size != 0 {
		t.Fatalf("health sentinel response must not be stored in cache, size=%d", stats.Size)
	}
	if stats.Hits != firstStats.Hits || stats.Misses != firstStats.Misses {
		t.Fatalf("health sentinel must not affect cache hit/miss counters: before=%+v after=%+v", firstStats, stats)
	}
	if got := second.Header().Get("X-SEAM-Cache"); got != "" {
		t.Fatalf("health sentinel response must not include cache status, got %q", got)
	}
}

func TestCredentialHealthSentinelReportsDegradedForHalfOpenBreaker(t *testing.T) {
	s := newHealthSentinelTestServer(t)
	// Origins are ordered so the half-open record sorts before the open one:
	// the status walk must still report unhealthy once it reaches the open
	// breaker instead of stopping at the degraded it already recorded.
	s.CircuitBreakerStates().Set(CircuitBreakerStatus{
		Origin:              "https://a-half-open.example",
		State:               CircuitBreakerHalfOpen,
		Enabled:             true,
		ConsecutiveFailures: 2,
	})
	s.CircuitBreakerStates().Set(CircuitBreakerStatus{
		Origin:              "https://z-open.example",
		State:               CircuitBreakerOpen,
		Enabled:             true,
		ConsecutiveFailures: 7,
	})

	resp := httptest.NewRecorder()
	s.identityResolutionMiddleware(s.operatorMux).ServeHTTP(resp,
		httptest.NewRequest(http.MethodGet, "/health/credentials", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected health status 200, got %d", resp.Code)
	}
	var health CredentialHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if health.Status != "unhealthy" {
		t.Fatalf("expected open breaker to outrank half-open as unhealthy, got %q", health.Status)
	}
	if health.CircuitBreaker.State != CircuitBreakerOpen {
		t.Fatalf("expected aggregate breaker state %q, got %q", CircuitBreakerOpen, health.CircuitBreaker.State)
	}

	// Without the open breaker the same half-open record degrades the status
	// rather than leaving the endpoint healthy.
	s.CircuitBreakerStates().Remove("https://z-open.example")
	degraded := httptest.NewRecorder()
	s.identityResolutionMiddleware(s.operatorMux).ServeHTTP(degraded,
		httptest.NewRequest(http.MethodGet, "/health/credentials", nil))
	if degraded.Code != http.StatusOK {
		t.Fatalf("expected health status 200, got %d", degraded.Code)
	}
	var halfOpen CredentialHealthResponse
	if err := json.NewDecoder(degraded.Body).Decode(&halfOpen); err != nil {
		t.Fatalf("decode half-open health response: %v", err)
	}
	if halfOpen.Status != "degraded" {
		t.Fatalf("expected half-open breaker to make credential health degraded, got %q", halfOpen.Status)
	}
	if halfOpen.CircuitBreaker.State != CircuitBreakerHalfOpen {
		t.Fatalf("expected aggregate breaker state %q, got %q", CircuitBreakerHalfOpen, halfOpen.CircuitBreaker.State)
	}
}

func TestCredentialHealthSentinelResponseCarriesNoCredentialValues(t *testing.T) {
	s := newHealthSentinelTestServer(t)
	s.CircuitBreakerStates().Set(CircuitBreakerStatus{
		Origin:              "https://upstream.example",
		State:               CircuitBreakerOpen,
		Enabled:             true,
		ConsecutiveFailures: 4,
		LastError:           "upstream request timed out",
		RetryAfterSeconds:   30,
		Source:              "caller",
	})

	resp := httptest.NewRecorder()
	s.identityResolutionMiddleware(s.operatorMux).ServeHTTP(resp,
		httptest.NewRequest(http.MethodGet, "/health/credentials", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected health status 200, got %d", resp.Code)
	}

	// The sentinel's only input is breaker state, so the response schema is
	// closed: every object key the body contains must come from the
	// documented set. A field added for a credential value, its vault path or
	// any other secret-bearing metadata fails this pin.
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	allowed := map[string]bool{
		"status": true, "timestamp": true, "credentials": true, "available": true,
		"last_refresh": true, "circuit_breaker": true, "circuit_breakers": true,
		"enabled": true, "state": true, "consecutive_failures": true,
		"opened_at": true, "last_error": true, "retry_after_seconds": true,
		"origin": true, "source": true,
	}
	var seen []string
	var walk func(prefix string, value map[string]any)
	walk = func(prefix string, value map[string]any) {
		for key, child := range value {
			seen = append(seen, prefix+key)
			if !allowed[key] {
				t.Errorf("unexpected key %q in credential health response (only documented fields may appear)", prefix+key)
			}
			if nested, ok := child.(map[string]any); ok {
				walk(prefix+key+".", nested)
			}
		}
	}
	walk("", body)

	credentials, ok := body["credentials"].(map[string]any)
	if !ok {
		t.Fatalf("expected credentials object, got %T", body["credentials"])
	}
	for key := range credentials {
		if key != "available" && key != "last_refresh" {
			t.Errorf("credentials object carries %q; only availability metadata is permitted", key)
		}
	}
	if _, leaked := credentials["value"]; leaked {
		t.Error("credentials object must never carry a credential value")
	}
}

// TestCredentialHealthSentinelStatusMappingOverBreakerLifecycle pins the
// documented status mapping across every breaker state in one live sequence:
// a closed (or absent) breaker is healthy, an open breaker is unhealthy, and
// a breaker that recovers back to closed returns the endpoint to healthy. The
// endpoint stays HTTP 200 with Cache-Control: no-store at every observation,
// so a monitor can alert on the JSON status alone and never polls a stale
// cached verdict.
func TestCredentialHealthSentinelStatusMappingOverBreakerLifecycle(t *testing.T) {
	s := newHealthSentinelTestServer(t)
	observe := func() CredentialHealthResponse {
		t.Helper()
		resp := httptest.NewRecorder()
		s.identityResolutionMiddleware(s.operatorMux).ServeHTTP(resp,
			httptest.NewRequest(http.MethodGet, "/health/credentials", nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("expected health status 200, got %d", resp.Code)
		}
		if got := resp.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("expected no-store cache directive, got %q", got)
		}
		var health CredentialHealthResponse
		if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
			t.Fatalf("decode health response: %v", err)
		}
		return health
	}

	// A fresh registry has published nothing: the sentinel reports the
	// default healthy shape rather than an error, and omits the per-origin
	// list entirely.
	empty := observe()
	if empty.Status != "healthy" {
		t.Fatalf("expected empty registry to be healthy, got %q", empty.Status)
	}
	if !empty.Credentials.Available {
		t.Error("expected credentials.available true on the default healthy shape")
	}
	if empty.CircuitBreaker.Enabled {
		t.Error("expected circuit_breaker.enabled false when no breaker published state")
	}
	if empty.CircuitBreaker.State != CircuitBreakerClosed {
		t.Fatalf("expected aggregate state closed when no breaker published state, got %q", empty.CircuitBreaker.State)
	}
	if len(empty.CircuitBreakers) != 0 {
		t.Fatalf("expected no per-origin entries when no breaker published state, got %d", len(empty.CircuitBreakers))
	}

	// A closed breaker is the documented "normal operation" state: healthy.
	s.CircuitBreakerStates().Set(CircuitBreakerStatus{
		Origin:  "https://upstream.example",
		State:   CircuitBreakerClosed,
		Enabled: true,
	})
	closed := observe()
	if closed.Status != "healthy" {
		t.Fatalf("expected closed breaker to leave credential health healthy, got %q", closed.Status)
	}
	if closed.CircuitBreaker.State != CircuitBreakerClosed {
		t.Fatalf("expected aggregate breaker state %q, got %q", CircuitBreakerClosed, closed.CircuitBreaker.State)
	}
	if !closed.CircuitBreaker.Enabled {
		t.Error("expected circuit_breaker.enabled true once a breaker published state")
	}

	// The same origin tripping open flips the sentinel to unhealthy...
	s.CircuitBreakerStates().Set(CircuitBreakerStatus{
		Origin:              "https://upstream.example",
		State:               CircuitBreakerOpen,
		Enabled:             true,
		ConsecutiveFailures: 3,
	})
	open := observe()
	if open.Status != "unhealthy" {
		t.Fatalf("expected open breaker to make credential health unhealthy, got %q", open.Status)
	}

	// ...and its recovery back to closed restores healthy, so the endpoint
	// tracks the live registry in both directions instead of latching the
	// first failure it saw.
	s.CircuitBreakerStates().Set(CircuitBreakerStatus{
		Origin:  "https://upstream.example",
		State:   CircuitBreakerClosed,
		Enabled: true,
	})
	recovered := observe()
	if recovered.Status != "healthy" {
		t.Fatalf("expected recovered breaker to restore credential health to healthy, got %q", recovered.Status)
	}
	if recovered.CircuitBreaker.State != CircuitBreakerClosed {
		t.Fatalf("expected aggregate breaker state %q after recovery, got %q", CircuitBreakerClosed, recovered.CircuitBreaker.State)
	}
	if recovered.CircuitBreaker.ConsecutiveFailures != 0 {
		t.Fatalf("expected recovered breaker to report zero consecutive failures, got %d", recovered.CircuitBreaker.ConsecutiveFailures)
	}
}

func TestCredentialHealthSentinelRejectsNonGet(t *testing.T) {
	s := newHealthSentinelTestServer(t)
	resp := httptest.NewRecorder()
	// Same stage-3-outside-the-mux composition; the 405 lives behind the scope
	// gate that reads the identity stage 3 resolves.
	s.identityResolutionMiddleware(s.operatorMux).ServeHTTP(resp,
		httptest.NewRequest(http.MethodPost, "/health/credentials", nil))
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected POST health request to return 405, got %d", resp.Code)
	}
}
