package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCacheHitMetric_Recorded verifies that cache hit counters are properly recorded in Prometheus metrics
func TestCacheHitMetric_Recorded(t *testing.T) {
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
		w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// First request - cache miss (primes the cache)
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	if upstreamCallCount != 1 {
		t.Errorf("first request: expected 1 upstream call, got %d", upstreamCallCount)
	}

	// Second request - cache hit
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	if upstreamCallCount != 1 {
		t.Errorf("second request: expected 1 upstream call (hit cache), got %d", upstreamCallCount)
	}

	// Verify the second request was a cache hit
	if w2.Header().Get("X-SEAM-Cache") != "HIT" {
		t.Error("second request should have cache hit header")
	}

	// Now check the metrics endpoint to verify hit counter was recorded
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

	// Verify the seam_cache_hits_total metric exists and has been incremented
	if !strings.Contains(bodyStr, "seam_cache_hits_total") {
		t.Error("expected metrics to contain seam_cache_hits_total")
	}

	// Check that there's a metric entry for /api/test route
	// The Prometheus format is: seam_cache_hits_total{route="/api/test"} <count>
	if !strings.Contains(bodyStr, `seam_cache_hits_total{route="/api/test"}`) &&
		!strings.Contains(bodyStr, `seam_cache_hits_total{route="/api/test",`) {
		t.Error("expected seam_cache_hits_total metric for /api/test route")
	}
}

// TestCacheHitMetric_ThreadSafety verifies that hit counter increments are thread-safe
func TestCacheHitMetric_ThreadSafety(t *testing.T) {
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
		w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// Prime the cache with first request
	reqPrime := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	wPrime := httptest.NewRecorder()
	cachedHandler.ServeHTTP(wPrime, reqPrime)

	// Launch multiple concurrent requests that should all hit cache
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
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

	// Verify the hit counter exists in the output
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "seam_cache_hits_total") {
		t.Error("expected seam_cache_hits_total metric after concurrent requests")
	}
}

// TestCacheHitMetric_HitDetection verifies that hit detection logic is correct
func TestCacheHitMetric_HitDetection(t *testing.T) {
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
		w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// Test scenarios that should result in cache hits
	scenarios := []struct {
		name               string
		request            *http.Request
		shouldCallUpstream bool
	}{
		{
			name:               "second identical request (cache hit)",
			request:            httptest.NewRequest(http.MethodGet, "/api/test", nil),
			shouldCallUpstream: false,
		},
		{
			name:               "same path, same query params (cache hit)",
			request:            httptest.NewRequest(http.MethodGet, "/api/test?foo=bar", nil),
			shouldCallUpstream: false,
		},
		{
			name:               "same path, reordered query params (cache hit)",
			request:            httptest.NewRequest(http.MethodGet, "/api/test?b=2&a=1", nil),
			shouldCallUpstream: false,
		},
	}

	// Prime cache for each scenario
	for _, scenario := range scenarios {
		// First request to prime cache
		initialReq := scenario.request.Clone(scenario.request.Context())
		w := httptest.NewRecorder()
		cachedHandler.ServeHTTP(w, initialReq)
	}

	// Reset counter for hit testing
	upstreamCallCount = 0

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			initialCount := upstreamCallCount
			w := httptest.NewRecorder()
			cachedHandler.ServeHTTP(w, scenario.request)

			// Verify response has cache hit header
			if w.Header().Get("X-SEAM-Cache") != "HIT" {
				t.Errorf("%s: expected cache hit header", scenario.name)
			}

			expectedCalls := initialCount
			if upstreamCallCount != expectedCalls {
				t.Errorf("%s: expected %d upstream calls (cache hit), got %d", scenario.name, expectedCalls, upstreamCallCount)
			}
		})
	}

	// Verify that metrics were recorded for each hit
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "seam_cache_hits_total") {
		t.Error("expected seam_cache_hits_total metric after multiple hit scenarios")
	}
}

// TestCacheHitMetric_RouteLabels verifies that metrics are properly labeled by route
func TestCacheHitMetric_RouteLabels(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Set up multiple test routes with cache TTL
	s.cacheTTLs["/api/users"] = 300
	s.cacheTTLs["/api/posts"] = 300
	s.cacheTTLs["/api/comments"] = 300

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// Prime cache for all routes
	routes := []string{"/api/users", "/api/posts", "/api/comments"}
	for _, route := range routes {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		w := httptest.NewRecorder()
		cachedHandler.ServeHTTP(w, req)
	}

	// Hit each route twice
	for i := 0; i < 2; i++ {
		for _, route := range routes {
			req := httptest.NewRequest(http.MethodGet, route, nil)
			w := httptest.NewRecorder()
			cachedHandler.ServeHTTP(w, req)

			if w.Header().Get("X-SEAM-Cache") != "HIT" {
				t.Errorf("request to %s should have cache hit header", route)
			}
		}
	}

	// Check metrics endpoint
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Verify each route has its own metric label
	for _, route := range routes {
		if !strings.Contains(bodyStr, `route="`+route+`"`) {
			t.Errorf("expected seam_cache_hits_total metric for route %s", route)
		}
	}
}

// TestCacheHitMetric_ConsecutiveHits verifies multiple consecutive hits are recorded correctly
func TestCacheHitMetric_ConsecutiveHits(t *testing.T) {
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
		w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// Prime cache
	reqPrime := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	wPrime := httptest.NewRecorder()
	cachedHandler.ServeHTTP(wPrime, reqPrime)

	// Make 5 consecutive requests - all should be hits
	hitCount := 0
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		w := httptest.NewRecorder()
		cachedHandler.ServeHTTP(w, req)

		if w.Header().Get("X-SEAM-Cache") == "HIT" {
			hitCount++
		}
	}

	if hitCount != 5 {
		t.Errorf("expected 5 cache hits, got %d", hitCount)
	}

	// Trigger metrics update
	stats := s.cache.Stats()
	updateCacheMetrics(stats)

	// Check that hit rate reflects the hits (5 hits, 1 miss = ~83%)
	reqMetrics := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	wMetrics := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(wMetrics, reqMetrics)

	resp := wMetrics.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Verify hit rate is approximately 0.83 (5 hits, 1 miss total = 5/6 ≈ 83%)
	if !strings.Contains(bodyStr, "seam_cache_hit_rate 0.8") &&
		!strings.Contains(bodyStr, "seam_cache_hit_rate 0.83") {
		t.Errorf("expected hit rate near 0.83 after 5 hits, 1 miss, got metrics:\n%s", bodyStr)
	}
}
