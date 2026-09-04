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
