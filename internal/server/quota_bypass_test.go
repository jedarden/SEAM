package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestQuotaBypass_CacheHit verifies that cache hits bypass quota deduction
func TestQuotaBypass_CacheHit(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Configure quota for the test route
	s.quotaTracker.SetQuota("/api/test", QuotaConfig{
		Limit:  1.0, // $1.00 limit
		Window: 1 * time.Hour,
		Scope:  "per-route",
	})

	// Set cost per call
	s.quotaTracker.SetCostPerCall("/api/test", 0.10) // $0.10 per call

	// Create a mock upstream handler
	upstreamCallCount := 0
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCallCount++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream response"))
	})

	// Wrap with cache middleware (outer) wrapping quota middleware (inner)
	// This matches the server's middleware chain:
	// cacheMiddleware -> quotaMiddleware -> handler
	quotaInner := s.quotaMiddleware(mockHandler)
	quotaHandler := s.cacheMiddleware(quotaInner)

	// Enable caching for this route
	s.cacheTTLs["/api/test"] = 300

	// First request - cache miss, should deduct quota
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	quotaHandler.ServeHTTP(w1, req1)

	if upstreamCallCount != 1 {
		t.Errorf("first request: expected 1 upstream call, got %d", upstreamCallCount)
	}

	// Verify quota was deducted (cost should be in response headers)
	if w1.Header().Get("X-Quota-Bypassed") == "cache-hit" {
		t.Error("first request should NOT have quota bypass header (cache miss)")
	}

	if w1.Header().Get("X-Quota-Cost-Per-Call") == "" {
		t.Error("first request should have quota cost header (cache miss)")
	}

	// Check quota status
	quotaStatus := s.quotaTracker.GetQuotaStatus()
	routeKey := "route:/api/test"
	if routeStatus, ok := quotaStatus[routeKey].(map[string]interface{}); ok {
		accumulated := routeStatus["accumulated"].(float64)
		if accumulated != 0.10 {
			t.Errorf("first request: expected accumulated quota of $0.10, got $%.2f", accumulated)
		}
	}

	// Second request - cache hit, should NOT deduct quota
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w2 := httptest.NewRecorder()
	quotaHandler.ServeHTTP(w2, req2)

	if upstreamCallCount != 1 {
		t.Errorf("second request: expected 1 upstream call (cache hit), got %d", upstreamCallCount)
	}

	// Verify quota bypass header is present
	if w2.Header().Get("X-Quota-Bypassed") != "cache-hit" {
		t.Error("second request should have quota bypass header (cache hit)")
	}

	// Verify NO quota cost header (because quota was bypassed)
	if w2.Header().Get("X-Quota-Cost-Per-Call") != "" {
		t.Error("second request should NOT have quota cost header (quota bypassed)")
	}

	// Check quota status - should still be $0.10 (not incremented)
	quotaStatus = s.quotaTracker.GetQuotaStatus()
	if routeStatus, ok := quotaStatus[routeKey].(map[string]interface{}); ok {
		accumulated := routeStatus["accumulated"].(float64)
		if accumulated != 0.10 {
			t.Errorf("second request: expected accumulated quota of $0.10 (unchanged), got $%.2f", accumulated)
		}
	}

	// Verify metrics
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Check that quota bypass metric was recorded
	if !strings.Contains(bodyStr, `seam_quota_bypassed_total{route="/api/test"}`) &&
		!strings.Contains(bodyStr, `seam_quota_bypassed_total{route="/api/test",`) {
		t.Error("expected seam_quota_bypassed_total metric for /api/test")
	}
}

// TestQuotaBypass_CacheMissDeducts verifies that cache misses still deduct quota
func TestQuotaBypass_CacheMissDeducts(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Configure quota for all test routes
	paths := []string{"/api/test", "/api/test2", "/api/test3"}
	for _, path := range paths {
		s.quotaTracker.SetQuota(path, QuotaConfig{
			Limit:  1.0, // $1.00 limit
			Window: 1 * time.Hour,
			Scope:  "per-route",
		})

		// Set cost per call for each route
		s.quotaTracker.SetCostPerCall(path, 0.25) // $0.25 per call

		// Enable caching for this route
		s.cacheTTLs[path] = 300
	}

	// Create a mock upstream handler
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream response"))
	})

	// Wrap with quota middleware first, then cache middleware
	quotaInner := s.quotaMiddleware(mockHandler)
	quotaHandler := s.cacheMiddleware(quotaInner)

	// Make 3 different requests (all cache misses due to different paths)
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		quotaHandler.ServeHTTP(w, req)

		// Verify NO quota bypass header
		if w.Header().Get("X-Quota-Bypassed") == "cache-hit" {
			t.Errorf("request to %s should NOT have quota bypass header (cache miss)", path)
		}
	}

	// Check quota status - should have deducted for all 3 requests (3 calls × $0.25 = $0.75)
	quotaStatus := s.quotaTracker.GetQuotaStatus()

	// Check each route's accumulated quota
	for _, path := range paths {
		routeKey := "route:" + path
		if routeStatus, ok := quotaStatus[routeKey].(map[string]interface{}); ok {
			accumulated := routeStatus["accumulated"].(float64)
			if accumulated != 0.25 {
				t.Errorf("route %s: expected accumulated quota of $0.25, got $%.2f", path, accumulated)
			}
		}
	}

	// Verify metrics were recorded
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Check that quota cost metrics were recorded for each route
	for _, path := range paths {
		if !strings.Contains(bodyStr, `seam_quota_cost_total{route="`+path+`"`) &&
			!strings.Contains(bodyStr, `seam_quota_cost_total{route="`+path+`,`) {
			t.Errorf("expected seam_quota_cost_total metric for route %s", path)
		}
	}
}

// TestQuotaBypass_ConsecutiveHits verifies multiple consecutive cache hits all bypass quota
func TestQuotaBypass_ConsecutiveHits(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Configure quota with a very low limit
	s.quotaTracker.SetQuota("/api/test", QuotaConfig{
		Limit:  0.20, // Only $0.20 limit (would allow 2 calls at $0.10 each)
		Window: 1 * time.Hour,
		Scope:  "per-route",
	})

	s.quotaTracker.SetCostPerCall("/api/test", 0.10)

	// Create a mock upstream handler
	upstreamCallCount := 0
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCallCount++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response"))
	})

	// Wrap with quota and cache middleware (correct order)
	quotaInner := s.quotaMiddleware(mockHandler)
	quotaHandler := s.cacheMiddleware(quotaInner)

	// Enable caching
	s.cacheTTLs["/api/test"] = 300

	// First request - cache miss
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	quotaHandler.ServeHTTP(w1, req1)

	// Make 10 consecutive requests - all should be cache hits
	bypassCount := 0
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		w := httptest.NewRecorder()
		quotaHandler.ServeHTTP(w, req)

		if w.Header().Get("X-Quota-Bypassed") == "cache-hit" {
			bypassCount++
		}
	}

	if bypassCount != 10 {
		t.Errorf("expected 10 quota bypasses, got %d", bypassCount)
	}

	if upstreamCallCount != 1 {
		t.Errorf("expected 1 upstream call total, got %d", upstreamCallCount)
	}

	// Verify quota is still at $0.10 (only the first call deducted)
	quotaStatus := s.quotaTracker.GetQuotaStatus()
	routeKey := "route:/api/test"
	if routeStatus, ok := quotaStatus[routeKey].(map[string]interface{}); ok {
		accumulated := routeStatus["accumulated"].(float64)
		if accumulated != 0.10 {
			t.Errorf("expected accumulated quota of $0.10, got $%.2f", accumulated)
		}
	}
}

// TestQuotaBypass_ContextPropagation verifies that cache hit status is properly propagated via context
func TestQuotaBypass_ContextPropagation(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Configure quota
	s.quotaTracker.SetQuota("/api/test", QuotaConfig{
		Limit:  1.0,
		Window: 1 * time.Hour,
		Scope:  "per-route",
	})

	s.quotaTracker.SetCostPerCall("/api/test", 0.10)

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response"))
	})

	// Wrap with quota and cache middleware (correct order)
	quotaInner := s.quotaMiddleware(mockHandler)
	quotaHandler := s.cacheMiddleware(quotaInner)

	// Enable caching
	s.cacheTTLs["/api/test"] = 300

	// Test that cache hit is correctly detected in quota middleware
	// First request - cache miss
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	quotaHandler.ServeHTTP(w1, req1)

	// Check context has cache hit set to false
	cacheHit1 := isCacheHit(req1)
	if cacheHit1 {
		t.Error("first request: cache hit in context should be false")
	}

	// Second request - cache hit
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w2 := httptest.NewRecorder()

	// We need to manually set up the context to test propagation
	// In real flow, cacheMiddleware sets this before calling quota middleware
	ctx := context.WithValue(req2.Context(), cacheHitKey, true)
	req2 = req2.WithContext(ctx)

	quotaHandler.ServeHTTP(w2, req2)

	// Verify quota middleware detected the cache hit
	if w2.Header().Get("X-Quota-Bypassed") != "cache-hit" {
		t.Error("expected X-Quota-Bypassed header when cache hit is in context")
	}
}

// TestQuotaBypass_MultipleRoutes verifies quota bypass works correctly across multiple routes
func TestQuotaBypass_MultipleRoutes(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Configure multiple routes
	routes := []string{"/api/users", "/api/posts", "/api/comments"}
	for _, route := range routes {
		s.quotaTracker.SetQuota(route, QuotaConfig{
			Limit:  1.0,
			Window: 1 * time.Hour,
			Scope:  "per-route",
		})
		s.quotaTracker.SetCostPerCall(route, 0.15)
		s.cacheTTLs[route] = 300
	}

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response"))
	})

	// Wrap with quota and cache middleware (correct order)
	quotaInner := s.quotaMiddleware(mockHandler)
	quotaHandler := s.cacheMiddleware(quotaInner)

	// Prime cache for all routes (cache misses)
	for _, route := range routes {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		w := httptest.NewRecorder()
		quotaHandler.ServeHTTP(w, req)

		if w.Header().Get("X-Quota-Bypassed") == "cache-hit" {
			t.Errorf("route %s: first request should NOT bypass quota", route)
		}
	}

	// Hit each route 5 times (all cache hits)
	for i := 0; i < 5; i++ {
		for _, route := range routes {
			req := httptest.NewRequest(http.MethodGet, route, nil)
			w := httptest.NewRecorder()
			quotaHandler.ServeHTTP(w, req)

			if w.Header().Get("X-Quota-Bypassed") != "cache-hit" {
				t.Errorf("route %s, request %d: should bypass quota (cache hit)", route, i+1)
			}
		}
	}

	// Verify all routes have exactly $0.15 accumulated (only first call counted)
	quotaStatus := s.quotaTracker.GetQuotaStatus()
	for _, route := range routes {
		routeKey := "route:" + route
		if routeStatus, ok := quotaStatus[routeKey].(map[string]interface{}); ok {
			accumulated := routeStatus["accumulated"].(float64)
			if accumulated != 0.15 {
				t.Errorf("route %s: expected $0.15 accumulated, got $%.2f", route, accumulated)
			}
		}
	}
}
