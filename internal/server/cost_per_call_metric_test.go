package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCostPerCallMetric_CacheHitBypass verifies that cache hits do NOT increment the cost-per-call metric
func TestCostPerCallMetric_CacheHitBypass(t *testing.T) {
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
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream response"))
	})

	// Wrap with cache middleware (outer) wrapping quota middleware (inner)
	quotaInner := s.quotaMiddleware(mockHandler)
	quotaHandler := s.cacheMiddleware(quotaInner)

	// Enable caching for this route
	s.cacheTTLs["/api/test"] = 300

	// First request - cache miss, should increment cost metric
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	quotaHandler.ServeHTTP(w1, req1)

	// Check metrics after cache miss
	reqMetrics1 := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics1 := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics1, reqMetrics1)

	resp1 := wMetrics1.Result()
	defer func() { _ = resp1.Body.Close() }()

	body1, _ := io.ReadAll(resp1.Body)
	bodyStr1 := string(body1)

	// Verify cost metric was incremented for cache miss
	if !strings.Contains(bodyStr1, `seam_quota_cost_total{route="/api/test"}`) &&
		!strings.Contains(bodyStr1, `seam_quota_cost_total{route="/api/test",`) {
		t.Error("expected seam_quota_cost_total metric for /api/test after cache miss")
	}

	// Extract the initial cost value (should be 0.10)
	initialCost := extractMetricValue(bodyStr1, "seam_quota_cost_total", "/api/test")
	if initialCost != 0.10 {
		t.Errorf("expected initial cost of $0.10, got $%.2f", initialCost)
	}

	// Make 5 consecutive cache hit requests
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		w := httptest.NewRecorder()
		quotaHandler.ServeHTTP(w, req)

		// Verify bypass header is present
		if w.Header().Get("X-Quota-Bypassed") != "cache-hit" {
			t.Errorf("request %d: should have quota bypass header (cache hit)", i+1)
		}
	}

	// Check metrics after cache hits
	reqMetrics2 := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics2 := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics2, reqMetrics2)

	resp2 := wMetrics2.Result()
	defer func() { _ = resp2.Body.Close() }()

	body2, _ := io.ReadAll(resp2.Body)
	bodyStr2 := string(body2)

	// Extract the final cost value
	finalCost := extractMetricValue(bodyStr2, "seam_quota_cost_total", "/api/test")

	// Verify cost metric was NOT incremented for cache hits
	// Should still be $0.10 (only the first call counted)
	if finalCost != 0.10 {
		t.Errorf("expected final cost of $0.10 (unchanged), got $%.2f", finalCost)
	}

	// Verify bypassed metric was incremented for cache hits
	if !strings.Contains(bodyStr2, `seam_quota_bypassed_total{route="/api/test"}`) &&
		!strings.Contains(bodyStr2, `seam_quota_bypassed_total{route="/api/test",`) {
		t.Error("expected seam_quota_bypassed_total metric for /api/test after cache hits")
	}
}

// TestCostPerCallMetric_CacheMissIncrements verifies that cache misses DO increment the cost-per-call metric
func TestCostPerCallMetric_CacheMissIncrements(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Configure quota for multiple routes
	paths := []string{"/api/test1", "/api/test2", "/api/test3"}
	for _, path := range paths {
		s.quotaTracker.SetQuota(path, QuotaConfig{
			Limit:  1.0,
			Window: 1 * time.Hour,
			Scope:  "per-route",
		})

		// Set different costs for each route
		s.quotaTracker.SetCostPerCall(path, 0.25) // $0.25 per call

		// Enable caching for this route
		s.cacheTTLs[path] = 300
	}

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream response"))
	})

	// Wrap with quota and cache middleware
	quotaInner := s.quotaMiddleware(mockHandler)
	quotaHandler := s.cacheMiddleware(quotaInner)

	// Make first requests for all routes (cache misses)
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		quotaHandler.ServeHTTP(w, req)

		// Verify NO bypass header (cache miss)
		if w.Header().Get("X-Quota-Bypassed") == "cache-hit" {
			t.Errorf("route %s: should NOT have bypass header on cache miss", path)
		}
	}

	// Check metrics after cache misses
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Verify cost metric was incremented for each route
	for _, path := range paths {
		cost := extractMetricValue(bodyStr, "seam_quota_cost_total", path)
		expectedCost := 0.25
		if cost != expectedCost {
			t.Errorf("route %s: expected cost metric of $%.2f, got $%.2f", path, expectedCost, cost)
		}
	}
}

// TestCostPerCallMetric_MixedHitsAndMisses verifies correct behavior with mixed cache hits and misses
func TestCostPerCallMetric_MixedHitsAndMisses(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Create multiple routes to simulate mixed hits/misses
	routes := []string{"/api/mixed1", "/api/mixed2"}
	for _, route := range routes {
		s.quotaTracker.SetQuota(route, QuotaConfig{
			Limit:  2.0,
			Window: 1 * time.Hour,
			Scope:  "per-route",
		})

		s.quotaTracker.SetCostPerCall(route, 0.20) // $0.20 per call
		s.cacheTTLs[route] = 300
	}

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream response"))
	})

	quotaInner := s.quotaMiddleware(mockHandler)
	quotaHandler := s.cacheMiddleware(quotaInner)

	// First request to /api/mixed1 - cache miss
	req1 := httptest.NewRequest(http.MethodGet, "/api/mixed1", nil)
	w1 := httptest.NewRecorder()
	quotaHandler.ServeHTTP(w1, req1)

	if w1.Header().Get("X-Quota-Bypassed") == "cache-hit" {
		t.Error("first request to /api/mixed1 should NOT have bypass header (cache miss)")
	}

	// Second request to /api/mixed1 - cache hit
	req2 := httptest.NewRequest(http.MethodGet, "/api/mixed1", nil)
	w2 := httptest.NewRecorder()
	quotaHandler.ServeHTTP(w2, req2)

	if w2.Header().Get("X-Quota-Bypassed") != "cache-hit" {
		t.Error("second request to /api/mixed1 should have bypass header (cache hit)")
	}

	// Third request to /api/mixed2 - cache miss (different route)
	req3 := httptest.NewRequest(http.MethodGet, "/api/mixed2", nil)
	w3 := httptest.NewRecorder()
	quotaHandler.ServeHTTP(w3, req3)

	if w3.Header().Get("X-Quota-Bypassed") == "cache-hit" {
		t.Error("first request to /api/mixed2 should NOT have bypass header (cache miss)")
	}

	// Fourth request to /api/mixed2 - cache hit
	req4 := httptest.NewRequest(http.MethodGet, "/api/mixed2", nil)
	w4 := httptest.NewRecorder()
	quotaHandler.ServeHTTP(w4, req4)

	if w4.Header().Get("X-Quota-Bypassed") != "cache-hit" {
		t.Error("second request to /api/mixed2 should have bypass header (cache hit)")
	}

	// Check metrics
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// We had 2 cache misses (first request to each route), so:
	// /api/mixed1: $0.20 (1 miss)
	// /api/mixed2: $0.20 (1 miss)
	mixed1Cost := extractMetricValue(bodyStr, "seam_quota_cost_total", "/api/mixed1")
	mixed2Cost := extractMetricValue(bodyStr, "seam_quota_cost_total", "/api/mixed2")

	expectedRouteCost := 0.20
	if mixed1Cost != expectedRouteCost {
		t.Errorf("route /api/mixed1: expected cost of $%.2f (1 miss), got $%.2f", expectedRouteCost, mixed1Cost)
	}

	if mixed2Cost != expectedRouteCost {
		t.Errorf("route /api/mixed2: expected cost of $%.2f (1 miss), got $%.2f", expectedRouteCost, mixed2Cost)
	}

	// Verify bypassed metrics were incremented for both routes
	if !strings.Contains(bodyStr, `seam_quota_bypassed_total{route="/api/mixed1"}`) &&
		!strings.Contains(bodyStr, `seam_quota_bypassed_total{route="/api/mixed1",`) {
		t.Error("expected seam_quota_bypassed_total metric for /api/mixed1")
	}

	if !strings.Contains(bodyStr, `seam_quota_bypassed_total{route="/api/mixed2"}`) &&
		!strings.Contains(bodyStr, `seam_quota_bypassed_total{route="/api/mixed2",`) {
		t.Error("expected seam_quota_bypassed_total metric for /api/mixed2")
	}
}

// extractMetricValue extracts the value of a Prometheus metric for a specific route
// Returns the metric value as a float64
func extractMetricValue(metricsBody, metricName, route string) float64 {
	// Prometheus format: metric_name{route="/path"} value
	// We need to find the line containing this metric and route, then extract the value

	lines := strings.Split(metricsBody, "\n")
	for _, line := range lines {
		// Check if line contains our metric and route
		if strings.Contains(line, metricName+"{") || strings.Contains(line, metricName+" ") {
			// Check if it's for our route
			if strings.Contains(line, `route="`+route+`"`) || strings.Contains(line, `route="/`+route+`"`) {
				// Extract the value (last space-separated token)
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					var value float64
					_, err := fmt.Sscanf(parts[len(parts)-1], "%f", &value)
					if err == nil {
						return value
					}
				}
			}
		}
	}
	return 0
}
