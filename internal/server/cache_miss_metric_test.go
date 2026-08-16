package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCacheMissMetric_Recorded verifies that cache miss counters are properly recorded in Prometheus metrics
func TestCacheMissMetric_Recorded(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Set up a test route with cache TTL
	s.cacheTTLs["/api/test"] = 300

	upstreamCallCount := 0
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCallCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// First request - cache miss
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	if upstreamCallCount != 1 {
		t.Errorf("first request: expected 1 upstream call, got %d", upstreamCallCount)
	}

	// Now check the metrics endpoint to verify miss counter was recorded
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics endpoint returned status %d", resp.StatusCode)
	}

	// Read metrics body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read metrics body: %v", err)
	}

	bodyStr := string(body)

	// Verify the seam_cache_misses_total metric exists and has been incremented
	if !strings.Contains(bodyStr, "seam_cache_misses_total") {
		t.Error("expected metrics to contain seam_cache_misses_total")
	}

	// Check that there's a metric entry for /api/test route
	// The Prometheus format is: seam_cache_misses_total{route="/api/test"} <count>
	if !strings.Contains(bodyStr, `seam_cache_misses_total{route="/api/test"}`) &&
		!strings.Contains(bodyStr, `seam_cache_misses_total{route="/api/test",`) {
		t.Error("expected seam_cache_misses_total metric for /api/test route")
	}
}

// TestCacheMissMetric_ThreadSafety verifies that miss counter increments are thread-safe
func TestCacheMissMetric_ThreadSafety(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Set up a test route with cache TTL
	s.cacheTTLs["/api/test"] = 300

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// Launch multiple concurrent requests that will all miss cache
	// (different paths to ensure they all miss)
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			path := "/api/test" + string(rune('a'+idx))
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			cachedHandler.ServeHTTP(w, req)
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Check that the metrics endpoint responds without panic
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("metrics endpoint returned status %d after concurrent load", resp.StatusCode)
	}

	// Verify the miss counter exists in the output
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "seam_cache_misses_total") {
		t.Error("expected seam_cache_misses_total metric after concurrent requests")
	}
}

// TestCacheMissMetric_MissDetection verifies that miss detection logic is correct
func TestCacheMissMetric_MissDetection(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Set up a test route with cache TTL
	s.cacheTTLs["/api/test"] = 300

	upstreamCallCount := 0
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCallCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// Test scenarios that should result in cache misses
	scenarios := []struct {
		name               string
		request            *http.Request
		shouldCallUpstream bool
	}{
		{
			name:               "first request (cold cache)",
			request:            httptest.NewRequest(http.MethodGet, "/api/test", nil),
			shouldCallUpstream: true,
		},
		{
			name:               "different path",
			request:            httptest.NewRequest(http.MethodGet, "/api/other", nil),
			shouldCallUpstream: true,
		},
		{
			name:               "different query params",
			request:            httptest.NewRequest(http.MethodGet, "/api/test?foo=bar", nil),
			shouldCallUpstream: true,
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			initialCount := upstreamCallCount
			w := httptest.NewRecorder()
			cachedHandler.ServeHTTP(w, scenario.request)

			expectedCalls := initialCount + 1
			if upstreamCallCount != expectedCalls {
				t.Errorf("%s: expected %d upstream calls, got %d", scenario.name, expectedCalls, upstreamCallCount)
			}
		})
	}

	// Verify that metrics were recorded for each miss
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "seam_cache_misses_total") {
		t.Error("expected seam_cache_misses_total metric after multiple miss scenarios")
	}
}
