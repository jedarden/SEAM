package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCacheMiddleware_Integration_HitPath tests that cache hits return cached responses without calling upstream
func TestCacheMiddleware_Integration_HitPath(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Set up a test route with cache TTL
	s.cacheTTLs["/api/test"] = 300 // 5 minute TTL

	// Create a mock upstream handler that tracks calls
	upstreamCallCount := 0
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCallCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream response"))
	})

	// Wrap with cache middleware
	cachedHandler := s.cacheMiddleware(mockHandler)

	// First request - should hit upstream
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	resp1 := w1.Result()
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("first request: expected status 200, got %d", resp1.StatusCode)
	}

	body1 := make([]byte, 100)
	n, _ := resp1.Body.Read(body1)
	if string(body1[:n]) != "upstream response" {
		t.Errorf("first request: expected 'upstream response', got %s", string(body1[:n]))
	}

	// Should NOT have X-SEAM-Cache header on miss
	if resp1.Header.Get("X-SEAM-Cache") == "HIT" {
		t.Error("first request should not have cache hit header")
	}

	if upstreamCallCount != 1 {
		t.Errorf("first request: expected 1 upstream call, got %d", upstreamCallCount)
	}

	// Second request - should hit cache
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	resp2 := w2.Result()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("second request: expected status 200, got %d", resp2.StatusCode)
	}

	body2 := make([]byte, 100)
	n, _ = resp2.Body.Read(body2)
	if string(body2[:n]) != "upstream response" {
		t.Errorf("second request: expected 'upstream response', got %s", string(body2[:n]))
	}

	// SHOULD have X-SEAM-Cache: HIT header
	if resp2.Header.Get("X-SEAM-Cache") != "HIT" {
		t.Errorf("second request should have cache hit header, got %s", resp2.Header.Get("X-SEAM-Cache"))
	}

	// Upstream should NOT have been called again
	if upstreamCallCount != 1 {
		t.Errorf("second request: expected 1 upstream call (cached), got %d", upstreamCallCount)
	}
}

// TestCacheMiddleware_Integration_MissPath tests that cache misses trigger upstream calls
func TestCacheMiddleware_Integration_MissPath(t *testing.T) {
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

	// First request - cache miss, should call upstream
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	if upstreamCallCount != 1 {
		t.Errorf("cache miss: expected 1 upstream call, got %d", upstreamCallCount)
	}

	// Different path - should also miss cache and call upstream
	req2 := httptest.NewRequest(http.MethodGet, "/api/other", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	if upstreamCallCount != 2 {
		t.Errorf("different path: expected 2 upstream calls, got %d", upstreamCallCount)
	}

	// Same path but different query param - should miss cache (different cache key)
	req3 := httptest.NewRequest(http.MethodGet, "/api/test?foo=bar", nil)
	w3 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w3, req3)

	if upstreamCallCount != 3 {
		t.Errorf("different query params: expected 3 upstream calls, got %d", upstreamCallCount)
	}
}

// TestCacheMiddleware_Integration_NonGetBypass tests that non-GET methods bypass cache
func TestCacheMiddleware_Integration_NonGetBypass(t *testing.T) {
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

	// GET request - should use cache
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	// POST request - should bypass cache
	req2 := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	if upstreamCallCount != 2 {
		t.Errorf("GET + POST: expected 2 upstream calls (POST bypasses cache), got %d", upstreamCallCount)
	}

	// PUT request - should bypass cache
	req3 := httptest.NewRequest(http.MethodPut, "/api/test", nil)
	w3 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w3, req3)

	if upstreamCallCount != 3 {
		t.Errorf("GET + POST + PUT: expected 3 upstream calls (non-GET bypass cache), got %d", upstreamCallCount)
	}

	// DELETE request - should bypass cache
	req4 := httptest.NewRequest(http.MethodDelete, "/api/test", nil)
	w4 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w4, req4)

	if upstreamCallCount != 4 {
		t.Errorf("GET + POST + PUT + DELETE: expected 4 upstream calls, got %d", upstreamCallCount)
	}

	// Second GET request - should hit cache (only the GET was cached)
	req5 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w5 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w5, req5)

	if upstreamCallCount != 4 {
		t.Errorf("second GET should hit cache: expected 4 upstream calls, got %d", upstreamCallCount)
	}

	if w5.Header().Get("X-SEAM-Cache") != "HIT" {
		t.Error("second GET should have cache hit header")
	}
}

// TestCacheMiddleware_Integration_ErrorNotCached tests that error responses are not cached
func TestCacheMiddleware_Integration_ErrorNotCached(t *testing.T) {
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
		// Return 500 error
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal server error"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// First request - 500 error
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	resp1 := w1.Result()
	if resp1.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", resp1.StatusCode)
	}

	if upstreamCallCount != 1 {
		t.Errorf("first request: expected 1 upstream call, got %d", upstreamCallCount)
	}

	// Second request - should call upstream again (errors not cached)
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	if upstreamCallCount != 2 {
		t.Errorf("second request: errors should not be cached, expected 2 upstream calls, got %d", upstreamCallCount)
	}

	if w2.Header().Get("X-SEAM-Cache") == "HIT" {
		t.Error("error response should not have cache hit header")
	}
}

// TestCacheMiddleware_Integration_4xxCached tests that 4xx responses ARE cached
func TestCacheMiddleware_Integration_4xxCached(t *testing.T) {
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
		// Return 404 error
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// First request - 404 error
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	resp1 := w1.Result()
	if resp1.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp1.StatusCode)
	}

	if upstreamCallCount != 1 {
		t.Errorf("first request: expected 1 upstream call, got %d", upstreamCallCount)
	}

	// Second request - should hit cache (4xx responses ARE cached)
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	if upstreamCallCount != 1 {
		t.Errorf("second request: 4xx should be cached, expected 1 upstream call, got %d", upstreamCallCount)
	}

	if w2.Header().Get("X-SEAM-Cache") != "HIT" {
		t.Error("cached 404 response should have cache hit header")
	}
}

// TestCacheMiddleware_Integration_QueryParamNormalization tests that query param order doesn't affect cache
func TestCacheMiddleware_Integration_QueryParamNormalization(t *testing.T) {
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

	// First request with query params in one order
	req1 := httptest.NewRequest(http.MethodGet, "/api/test?a=1&b=2&c=3", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	if upstreamCallCount != 1 {
		t.Errorf("first request: expected 1 upstream call, got %d", upstreamCallCount)
	}

	// Second request with same params in different order - should hit cache
	req2 := httptest.NewRequest(http.MethodGet, "/api/test?c=3&a=1&b=2", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	if upstreamCallCount != 1 {
		t.Errorf("second request: query param order should not affect cache, expected 1 upstream call, got %d", upstreamCallCount)
	}

	if w2.Header().Get("X-SEAM-Cache") != "HIT" {
		t.Error("request with reordered params should have cache hit header")
	}

	// Third request with different param value - should miss cache
	req3 := httptest.NewRequest(http.MethodGet, "/api/test?a=1&b=2&c=99", nil)
	w3 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w3, req3)

	if upstreamCallCount != 2 {
		t.Errorf("third request: different param value should miss cache, expected 2 upstream calls, got %d", upstreamCallCount)
	}
}

// TestCacheMiddleware_Integration_NoTTL tests that routes without TTL configuration are not cached
func TestCacheMiddleware_Integration_NoTTL(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Do NOT set up a TTL for this route
	// s.cacheTTLs["/api/test"] = 300  // <-- commented out

	upstreamCallCount := 0
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCallCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// First request
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	if upstreamCallCount != 1 {
		t.Errorf("first request: expected 1 upstream call, got %d", upstreamCallCount)
	}

	// Second request - should call upstream again (no caching configured)
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	if upstreamCallCount != 2 {
		t.Errorf("second request: no TTL configured, expected 2 upstream calls, got %d", upstreamCallCount)
	}

	if w2.Header().Get("X-SEAM-Cache") == "HIT" {
		t.Error("request without TTL should not have cache hit header")
	}
}

// TestCacheMiddleware_Integration_Expiration tests that cached responses expire correctly
func TestCacheMiddleware_Integration_Expiration(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Set up a test route with very short TTL (1 second)
	s.cacheTTLs["/api/test"] = 1

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

	// Second request immediately - should hit cache
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	if upstreamCallCount != 1 {
		t.Errorf("second request (immediate): expected 1 upstream call (cached), got %d", upstreamCallCount)
	}

	// Wait for cache entry to expire
	time.Sleep(2 * time.Second)

	// Third request after expiration - should miss cache and call upstream
	req3 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w3 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w3, req3)

	if upstreamCallCount != 2 {
		t.Errorf("third request (after expiration): expected 2 upstream calls, got %d", upstreamCallCount)
	}

	if w3.Header().Get("X-SEAM-Cache") == "HIT" {
		t.Error("request after expiration should not have cache hit header")
	}
}

// TestCacheMiddleware_Integration_ReservedPathsBypass tests that reserved paths bypass cache
func TestCacheMiddleware_Integration_ReservedPathsBypass(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Set up cache TTL for a reserved path (should still bypass)
	s.cacheTTLs["/_seam/healthz"] = 300

	upstreamCallCount := 0
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCallCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// Request to reserved path - should bypass cache
	req1 := httptest.NewRequest(http.MethodGet, "/_seam/healthz", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	if upstreamCallCount != 1 {
		t.Errorf("reserved path: expected 1 upstream call (bypassed cache), got %d", upstreamCallCount)
	}

	// Second request to same reserved path - should still bypass
	req2 := httptest.NewRequest(http.MethodGet, "/_seam/healthz", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	if upstreamCallCount != 2 {
		t.Errorf("reserved path second request: expected 2 upstream calls (always bypasses), got %d", upstreamCallCount)
	}

	if w2.Header().Get("X-SEAM-Cache") == "HIT" {
		t.Error("reserved path should not have cache hit header")
	}
}
