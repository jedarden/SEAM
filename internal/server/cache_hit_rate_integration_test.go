package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCacheHitRateZeroTotals verifies that hit rate doesn't panic with zero totals
func TestCacheHitRateZeroTotals(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)
	s.cacheTTLs["/api/test"] = 300

	// Set up cache handler (not used in this test, but part of standard setup)
	_ = s.cacheMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream response"))
	}))

	// Get metrics before any requests
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics endpoint returned status %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Verify seam_cache_hit_rate exists and doesn't panic
	if !strings.Contains(bodyStr, "seam_cache_hit_rate") {
		t.Error("expected seam_cache_hit_rate metric")
	}
}

// TestCacheHitRateAllMisses verifies hit rate calculation with all misses
func TestCacheHitRateAllMisses(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)
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
		t.Errorf("expected 1 upstream call after miss, got %d", upstreamCallCount)
	}

	// Get metrics
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Verify hit rate is 0.0 (1 miss, 0 hits = 0%)
	if !strings.Contains(bodyStr, "seam_cache_hit_rate 0") {
		t.Errorf("expected hit rate 0.0 after 1 miss, got metrics:\n%s", bodyStr)
	}
}

// TestCacheHitRateFiftyPercent verifies hit rate calculation with 1 hit, 1 miss
func TestCacheHitRateFiftyPercent(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)
	s.cacheTTLs["/api/test"] = 300

	upstreamCallCount := 0
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCallCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// First request - miss
	req1 := httptest.NewRequest(http.MethodGet, "/api/test?foo=bar", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	// Second request - hit (same params)
	req2 := httptest.NewRequest(http.MethodGet, "/api/test?foo=bar", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	if upstreamCallCount != 1 {
		t.Errorf("expected 1 upstream call (1 hit, 1 miss), got %d", upstreamCallCount)
	}

	// Verify the second request was a cache hit
	if w2.Header().Get("X-SEAM-Cache") != "HIT" {
		t.Error("second request should have cache hit header")
	}

	// Get metrics
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Verify hit rate is 0.5 (1 hit, 1 miss = 50%)
	// Note: Prometheus gauges show floating point values
	if !strings.Contains(bodyStr, "seam_cache_hit_rate 0.5") &&
		!strings.Contains(bodyStr, "seam_cache_hit_rate 0.50") {
		t.Errorf("expected hit rate 0.5 after 1 hit, 1 miss, got metrics:\n%s", bodyStr)
	}
}

// TestCacheHitRateSeventyFivePercent verifies hit rate calculation with 3 hits, 1 miss
func TestCacheHitRateSeventyFivePercent(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)
	s.cacheTTLs["/api/test"] = 300

	upstreamCallCount := 0
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCallCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// First request - miss
	req1 := httptest.NewRequest(http.MethodGet, "/api/test?foo=bar", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	// Next three requests - all hits
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/test?foo=bar", nil)
		w := httptest.NewRecorder()
		cachedHandler.ServeHTTP(w, req)

		if w.Header().Get("X-SEAM-Cache") != "HIT" {
			t.Errorf("request %d should have cache hit header", i+2)
		}
	}

	if upstreamCallCount != 1 {
		t.Errorf("expected 1 upstream call (3 hits, 1 miss), got %d", upstreamCallCount)
	}

	// Get metrics
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Verify hit rate is 0.75 (3 hits, 1 miss = 75%)
	if !strings.Contains(bodyStr, "seam_cache_hit_rate 0.75") &&
		!strings.Contains(bodyStr, "seam_cache_hit_rate 0.7") {
		t.Errorf("expected hit rate 0.75 after 3 hits, 1 miss, got metrics:\n%s", bodyStr)
	}
}

// TestCacheHitRateAllHits verifies hit rate calculation with all hits
func TestCacheHitRateAllHits(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)
	s.cacheTTLs["/api/test"] = 300

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// First request primes the cache (miss)
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	// Next 10 requests should all hit
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		w := httptest.NewRecorder()
		cachedHandler.ServeHTTP(w, req)

		if w.Header().Get("X-SEAM-Cache") != "HIT" {
			t.Errorf("request %d should have cache hit header", i+2)
		}
	}

	// Get metrics
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Verify hit rate is close to 1.0 (10 hits, 1 miss ≈ 91%)
	if !strings.Contains(bodyStr, "seam_cache_hit_rate 0.9") &&
		!strings.Contains(bodyStr, "seam_cache_hit_rate 0.91") &&
		!strings.Contains(bodyStr, "seam_cache_hit_rate 0.92") &&
		!strings.Contains(bodyStr, "seam_cache_hit_rate 90") &&
		!strings.Contains(bodyStr, "seam_cache_hit_rate 91") &&
		!strings.Contains(bodyStr, "seam_cache_hit_rate 92") {
		t.Errorf("expected hit rate near 0.91 after 10 hits, 1 miss, got metrics:\n%s", bodyStr)
	}
}

// TestCacheHitRateConcurrent verifies concurrent requests don't corrupt hit rate calculation
func TestCacheHitRateConcurrent(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)
	s.cacheTTLs["/api/test"] = 300

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// Launch concurrent cache requests
	done := make(chan bool, 20)
	for i := 0; i < 20; i++ {
		go func(idx int) {
			// Alternate between two paths to get both hits and misses
			path := "/api/test"
			if idx%2 == 1 {
				path = "/api/test?variant=1"
			}

			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			cachedHandler.ServeHTTP(w, req)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 20; i++ {
		<-done
	}

	// Get metrics - should not panic or produce invalid values
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics endpoint failed after concurrent load: %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Verify hit rate is in valid range [0, 1]
	if !strings.Contains(bodyStr, "seam_cache_hit_rate") {
		t.Error("expected seam_cache_hit_rate metric after concurrent requests")
	}

	// Verify the metric value is present and reasonable
	// Hit rate should be between 0 and 1
	lines := strings.Split(bodyStr, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "seam_cache_hit_rate") {
			// Extract value (format: "seam_cache_hit_rate 0.5")
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				var hitRate float64
				_, err := fmt.Sscanf(parts[1], "%f", &hitRate)
				if err != nil {
					t.Errorf("failed to parse hit rate value: %v", err)
				}
				if hitRate < 0 || hitRate > 1 {
					t.Errorf("hit rate out of valid range [0, 1]: %f", hitRate)
				}
			}
		}
	}
}

// TestCacheHitRateStatusEndpoint verifies that the cache status endpoint returns accurate hit rate
func TestCacheHitRateStatusEndpoint(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)
	s.cacheTTLs["/api/test"] = 300

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// Make 3 requests: 1 miss, then 2 hits
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	req3 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w3 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w3, req3)

	// Query cache status endpoint
	reqStatus := httptest.NewRequest(http.MethodGet, "/_seam/cache/status", nil)
	wStatus := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wStatus, reqStatus)

	resp := wStatus.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cache status endpoint returned status %d", resp.StatusCode)
	}

	// Parse JSON response
	body, _ := io.ReadAll(resp.Body)
	var status map[string]interface{}
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("failed to parse status JSON: %v", err)
	}

	// Verify hit_rate field exists
	hitRate, ok := status["hit_rate"]
	if !ok {
		t.Error("cache status should include hit_rate field")
	}

	// Verify hit rate is approximately 0.67 (2 hits, 1 miss = 66.7%)
	hitRateFloat, ok := hitRate.(float64)
	if !ok {
		t.Error("hit_rate should be a float64")
	}

	// Allow small floating point errors
	expectedHitRate := 2.0 / 3.0 // 0.666...
	if hitRateFloat < expectedHitRate-0.01 || hitRateFloat > expectedHitRate+0.01 {
		t.Errorf("hit rate mismatch: expected ~%f, got %f", expectedHitRate, hitRateFloat)
	}

	// Verify other fields
	if hits, ok := status["hits"].(float64); !ok || int(hits) != 2 {
		t.Errorf("expected 2 hits, got %v", status["hits"])
	}

	if misses, ok := status["misses"].(float64); !ok || int(misses) != 1 {
		t.Errorf("expected 1 miss, got %v", status["misses"])
	}
}
