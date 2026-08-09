package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestQuotaEnforcement_CacheMissIntegration verifies that quota is enforced on cache misses
// and that cache hits bypass the enforcement entirely
func TestQuotaEnforcement_CacheMissIntegration(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Configure a global quota limit to test enforcement across multiple routes
	s.quotaTracker.SetQuota("", QuotaConfig{
		Limit:  0.30, // Only $0.30 global limit
		Window: 1 * time.Hour,
		Scope:  "global",
	})

	// Create a mock upstream handler
	upstreamCallCount := 0
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCallCount++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream response"))
	})

	// Wrap with quota and cache middleware (order must match production: cache → quota → handler)
	quotaHandler := s.quotaMiddleware(mockHandler)
	cachedHandler := s.cacheMiddleware(quotaHandler)

	// Set cost and enable caching for routes
	s.quotaTracker.SetCostPerCall("/api/test", 0.10)
	s.cacheTTLs["/api/test"] = 300
	s.quotaTracker.SetCostPerCall("/api/test2", 0.10)
	s.cacheTTLs["/api/test2"] = 300
	s.quotaTracker.SetCostPerCall("/api/test3", 0.10)
	s.cacheTTLs["/api/test3"] = 300

	// First request - cache miss, should succeed
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("first request: expected status 200, got %d", w1.Code)
	}

	if upstreamCallCount != 1 {
		t.Errorf("first request: expected 1 upstream call, got %d", upstreamCallCount)
	}

	// Second request - cache hit, should succeed without consuming quota
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("second request: expected status 200, got %d", w2.Code)
	}

	if upstreamCallCount != 1 {
		t.Errorf("second request: expected 1 upstream call (cache hit), got %d", upstreamCallCount)
	}

	// Third request - different path, cache miss, should succeed
	req3 := httptest.NewRequest(http.MethodGet, "/api/test2", nil)
	w3 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Errorf("third request: expected status 200, got %d", w3.Code)
	}

	// Fourth request - different path, cache miss, should be rate limited (global quota exceeded: $0.10 + $0.10 = $0.20 used, next $0.10 would exceed $0.30)
	req4 := httptest.NewRequest(http.MethodGet, "/api/test3", nil)
	w4 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w4, req4)

	if w4.Code != http.StatusTooManyRequests {
		t.Errorf("fourth request: expected status 429 (quota exceeded), got %d", w4.Code)
	}

	// Verify the quota exceeded response
	if w4.Header().Get("Content-Type") != "application/json" {
		t.Error("quota exceeded response should have Content-Type: application/json")
	}

	body := w4.Body.String()
	if !strings.Contains(body, "quota_exceeded") {
		t.Error("quota exceeded response should contain 'quota_exceeded' error")
	}

	// Verify metrics
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	metricsBody, _ := io.ReadAll(resp.Body)
	metricsStr := string(metricsBody)

	// Check that quota exceeded metric was recorded
	if !strings.Contains(metricsStr, `seam_quota_exceeded_total{route="/api/test3"}`) &&
		!strings.Contains(metricsStr, `seam_quota_exceeded_total{route="/api/test3",`) {
		t.Error("expected seam_quota_exceeded_total metric for /api/test3")
	}
}

// TestQuotaEnforcement_CacheHitsBypassEnforcement verifies that cache hits bypass quota enforcement
// even when quota would normally be exceeded
func TestQuotaEnforcement_CacheHitsBypassEnforcement(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Configure a very low global quota limit
	s.quotaTracker.SetQuota("", QuotaConfig{
		Limit:  0.10, // Only $0.10 global limit (1 call at $0.10)
		Window: 1 * time.Hour,
		Scope:  "global",
	})

	s.quotaTracker.SetCostPerCall("/api/test", 0.10)

	// Create a mock upstream handler
	upstreamCallCount := 0
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCallCount++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream response"))
	})

	// Wrap with quota and cache middleware (order must match production: cache → quota → handler)
	quotaHandler := s.quotaMiddleware(mockHandler)
	cachedHandler := s.cacheMiddleware(quotaHandler)

	// Enable caching
	s.cacheTTLs["/api/test"] = 300

	// First request - cache miss, consumes the entire quota
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("first request: expected status 200, got %d", w1.Code)
	}

	if upstreamCallCount != 1 {
		t.Errorf("first request: expected 1 upstream call, got %d", upstreamCallCount)
	}

	// Second request would normally exceed quota, but it's a cache hit so it should succeed
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("second request (cache hit): expected status 200, got %d - cache hits should bypass quota enforcement", w2.Code)
	}

	if w2.Header().Get("X-Quota-Bypassed") != "cache-hit" {
		t.Error("second request should have X-Quota-Bypassed header")
	}

	if upstreamCallCount != 1 {
		t.Errorf("second request: expected 1 upstream call (cache hit), got %d", upstreamCallCount)
	}

	// Third request - different path, cache miss, should be rate limited
	s.quotaTracker.SetCostPerCall("/api/test2", 0.10)
	s.cacheTTLs["/api/test2"] = 300
	req3 := httptest.NewRequest(http.MethodGet, "/api/test2", nil)
	w3 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w3, req3)

	if w3.Code != http.StatusTooManyRequests {
		t.Errorf("third request (cache miss, different route): expected status 429, got %d", w3.Code)
	}

	// Verify metrics
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	metricsBody, _ := io.ReadAll(resp.Body)
	metricsStr := string(metricsBody)

	// Check that quota bypass metric was recorded
	if !strings.Contains(metricsStr, `seam_quota_bypassed_total{route="/api/test"}`) &&
		!strings.Contains(metricsStr, `seam_quota_bypassed_total{route="/api/test",`) {
		t.Error("expected seam_quota_bypassed_total metric for /api/test")
	}

	// Check that quota exceeded metric was recorded for the second route
	if !strings.Contains(metricsStr, `seam_quota_exceeded_total{route="/api/test2"}`) &&
		!strings.Contains(metricsStr, `seam_quota_exceeded_total{route="/api/test2",`) {
		t.Error("expected seam_quota_exceeded_total metric for /api/test2")
	}
}

// TestQuotaEnforcement_GlobalScope verifies quota enforcement with global scope
func TestQuotaEnforcement_GlobalScope(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Configure global quota limit
	s.quotaTracker.SetQuota("", QuotaConfig{
		Limit:  0.50, // $0.50 global limit
		Window: 1 * time.Hour,
		Scope:  "global",
	})

	// Set cost for multiple routes
	routes := []string{"/api/a", "/api/b", "/api/c"}
	for _, route := range routes {
		s.quotaTracker.SetCostPerCall(route, 0.20)
		s.cacheTTLs[route] = 300
	}

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response"))
	})

	// Wrap with quota and cache middleware (order must match production: cache → quota → handler)
	quotaHandler := s.quotaMiddleware(mockHandler)
	cachedHandler := s.cacheMiddleware(quotaHandler)

	// First two requests should succeed (total $0.40)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, routes[i], nil)
		w := httptest.NewRecorder()
		cachedHandler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected status 200, got %d", i+1, w.Code)
		}
	}

	// Third request should be rate limited (would exceed global quota of $0.50)
	req3 := httptest.NewRequest(http.MethodGet, routes[2], nil)
	w3 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w3, req3)

	if w3.Code != http.StatusTooManyRequests {
		t.Errorf("third request: expected status 429 (global quota exceeded), got %d", w3.Code)
	}

	// Now hit the cache for the first two routes - should succeed (bypass quota)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, routes[i], nil)
		w := httptest.NewRecorder()
		cachedHandler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("cache hit request %d: expected status 200, got %d - cache hits should bypass quota", i+1, w.Code)
		}

		if w.Header().Get("X-Quota-Bypassed") != "cache-hit" {
			t.Errorf("cache hit request %d: should have X-Quota-Bypassed header", i+1)
		}
	}
}

// TestQuotaEnforcement_ResponseHeaders verifies that correct headers are set for quota scenarios
func TestQuotaEnforcement_ResponseHeaders(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	s.quotaTracker.SetQuota("/api/test", QuotaConfig{
		Limit:  1.0,
		Window: 1 * time.Hour,
		Scope:  "per-route",
	})

	s.quotaTracker.SetCostPerCall("/api/test", 0.25)

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response"))
	})

	// Wrap with quota and cache middleware (order must match production: cache → quota → handler)
	quotaHandler := s.quotaMiddleware(mockHandler)
	cachedHandler := s.cacheMiddleware(quotaHandler)

	// Enable caching
	s.cacheTTLs["/api/test"] = 300

	// First request - cache miss
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	// Verify cache miss headers
	if w1.Header().Get("X-SEAM-Cache") == "HIT" {
		t.Error("first request should NOT have X-SEAM-Cache: HIT header")
	}

	if w1.Header().Get("X-Quota-Bypassed") == "cache-hit" {
		t.Error("first request should NOT have X-Quota-Bypassed header")
	}

	if w1.Header().Get("X-Quota-Cost-Per-Call") == "" {
		t.Error("first request should have X-Quota-Cost-Per-Call header")
	}

	if w1.Header().Get("X-Quota-Remaining") == "" {
		t.Error("first request should have X-Quota-Remaining header")
	}

	// Second request - cache hit
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	// Verify cache hit headers
	if w2.Header().Get("X-SEAM-Cache") != "HIT" {
		t.Error("second request should have X-SEAM-Cache: HIT header")
	}

	if w2.Header().Get("X-Quota-Bypassed") != "cache-hit" {
		t.Error("second request should have X-Quota-Bypassed: cache-hit header")
	}

	if w2.Header().Get("X-Quota-Cost-Per-Call") != "" {
		t.Error("second request should NOT have X-Quota-Cost-Per-Call header (quota bypassed)")
	}

	if w2.Header().Get("X-Quota-Remaining") != "" {
		t.Error("second request should NOT have X-Quota-Remaining header (quota bypassed)")
	}
}

// TestQuotaEnforcement_ReservedPaths verifies that reserved paths bypass quota checks
func TestQuotaEnforcement_ReservedPaths(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Configure a very low global quota
	s.quotaTracker.SetQuota("", QuotaConfig{
		Limit:  0.01,
		Window: 1 * time.Hour,
		Scope:  "global",
	})

	// Set a cost for the non-reserved path so it exceeds the quota
	s.quotaTracker.SetCostPerCall("/api/test", 0.10)

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response"))
	})

	// Wrap with quota middleware only (no cache)
	quotaHandler := s.quotaMiddleware(mockHandler)

	// Test reserved paths - should all succeed
	reservedPaths := []string{
		"/_seam/health",
		"/_seam/healthz",
		"/_seam/readyz",
		"/openapi.json",
		"/docs",
		"/docs/route",
		"/health/credentials",
		"/health/upstreams",
	}

	for _, path := range reservedPaths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		quotaHandler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("reserved path %s: expected status 200, got %d - reserved paths should bypass quota", path, w.Code)
		}
	}

	// Test non-reserved path - should be rate limited
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w := httptest.NewRecorder()
	quotaHandler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("non-reserved path: expected status 429, got %d", w.Code)
	}
}
