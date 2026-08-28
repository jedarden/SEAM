package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestLoopGuardMiddleware_SkipsReservedPaths tests that reserved paths
// bypass loop guard checking.
func TestLoopGuardMiddleware_SkipsReservedPaths(t *testing.T) {
	config := &Config{
		MaxReplayableRequestBytes: 1024 * 1024,
	}
	server := &Server{
		config:            config,
		loopGuardRegistry: NewLoopGuardRegistry(),
	}

	// Create middleware
	middleware := server.LoopGuardMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	reservedPaths := []string{
		"/docs",
		"/docs/route",
		"/openapi.json",
		"/health/credentials",
		"/health/upstreams",
		"/config/status",
	}

	for _, path := range reservedPaths {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()

		middleware.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Reserved path %s should bypass loop guard, got status %d", path, w.Code)
		}
	}
}

// TestLoopGuardMiddleware_SkipsProbeRequests tests that probe requests
// bypass loop guard checking.
func TestLoopGuardMiddleware_SkipsProbeRequests(t *testing.T) {
	config := &Config{
		MaxReplayableRequestBytes: 1024 * 1024,
	}
	server := &Server{
		config:            config,
		loopGuardRegistry: NewLoopGuardRegistry(),
	}

	// Create middleware
	middleware := server.LoopGuardMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Create probe request
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-SEAM-Probe", "true")
	w := httptest.NewRecorder()

	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Probe request should bypass loop guard, got status %d", w.Code)
	}
}

// TestLoopGuardMiddleware_BlocksRepeatedFailures tests that repeated
// failing requests are blocked with 429.
func TestLoopGuardMiddleware_BlocksRepeatedFailures(t *testing.T) {
	config := &Config{
		MaxReplayableRequestBytes: 1024 * 1024,
		DryRun:                   false,
	}
	server := &Server{
		config:            config,
		loopGuardRegistry: NewLoopGuardRegistry(),
		routeTableHolder:  NewThreadSafeTableHolder(&RouteTable{}),
	}

	// Add a test route with loop guard config
	routeTable := &RouteTable{routes: []RouteEntry{{
		PathTemplate: "/api/test",
		Method:       "POST",
		APIVersion:   "v1",
		LoopGuardConfig: &LoopGuardConfig{
			MaxRepeats:     3,
			Window:         "1m",
			windowDuration: 1 * time.Minute,
		},
	}}}
	server.routeTableHolder = NewThreadSafeTableHolder(routeTable)

	// Create middleware with next handler that always fails
	callCount := 0
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusInternalServerError) // Simulate failure
	})

	middleware := server.LoopGuardMiddleware(nextHandler)

	// Make repeated failing requests
	for i := 0; i < 4; i++ {
		body := strings.NewReader(`{"test":"data"}`)
		req := httptest.NewRequest("POST", "/api/test", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		middleware.ServeHTTP(w, req)

		if i < 3 {
			// First 3 should be allowed (even though they fail)
			if w.Code != http.StatusInternalServerError {
				t.Errorf("Request %d should be allowed (fail), got status %d", i+1, w.Code)
			}
		} else {
			// 4th should be blocked by loop guard
			if w.Code != http.StatusTooManyRequests {
				t.Errorf("Request %d should be blocked by loop guard, got status %d", i+1, w.Code)
			}
			// Check for Retry-After header
			retryAfter := w.Header().Get("Retry-After")
			if retryAfter == "" {
				t.Errorf("Blocked request should have Retry-After header")
			}
		}
	}
}

// TestLoopGuardMiddleware_SuccessClearsCounter tests that a successful
// response clears the failure counter.
func TestLoopGuardMiddleware_SuccessClearsCounter(t *testing.T) {
	config := &Config{
		MaxReplayableRequestBytes: 1024 * 1024,
		DryRun:                   false,
	}
	server := &Server{
		config:            config,
		loopGuardRegistry: NewLoopGuardRegistry(),
		routeTableHolder:  NewThreadSafeTableHolder(&RouteTable{}),
	}

	// Add a test route with loop guard config
	routeTable := &RouteTable{routes: []RouteEntry{{
		PathTemplate: "/api/test",
		Method:       "POST",
		APIVersion:   "v1",
		LoopGuardConfig: &LoopGuardConfig{
			MaxRepeats:     3,
			Window:         "1m",
			windowDuration: 1 * time.Minute,
		},
	}}}
	server.routeTableHolder = NewThreadSafeTableHolder(routeTable)

	// Track if success was recorded
	successRecorded := false

	// Create middleware with next handler that succeeds after failures
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if this is after some failures
		if successRecorded {
			w.WriteHeader(http.StatusOK) // Success
		} else {
			w.WriteHeader(http.StatusInternalServerError) // Failure
		}
	})

	middleware := server.LoopGuardMiddleware(nextHandler)

	// Make 2 failing requests
	for i := 0; i < 2; i++ {
		body := strings.NewReader(`{"test":"data"}`)
		req := httptest.NewRequest("POST", "/api/test", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		middleware.ServeHTTP(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Errorf("Request %d should fail, got status %d", i+1, w.Code)
		}
	}

	// Make a successful request
	successRecorded = true
	body := strings.NewReader(`{"test":"data"}`)
	req := httptest.NewRequest("POST", "/api/test", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Success request should return 200, got status %d", w.Code)
	}

	// Make more failing requests - should not be blocked immediately
	// because success cleared the counter
	successRecorded = false
	for i := 0; i < 3; i++ {
		body = strings.NewReader(`{"test":"data"}`)
		req = httptest.NewRequest("POST", "/api/test", body)
		req.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()

		middleware.ServeHTTP(w, req)

		// Should not be blocked yet (need 4 failures after success)
		if w.Code == http.StatusTooManyRequests {
			t.Errorf("Request after success should not be blocked yet, got status %d", w.Code)
		}
	}
}

// TestLoopGuardMiddleware_DifferentRequestsIndependent tests that
// different request hashes are tracked independently.
func TestLoopGuardMiddleware_DifferentRequestsIndependent(t *testing.T) {
	config := &Config{
		MaxReplayableRequestBytes: 1024 * 1024,
		DryRun:                   false,
	}
	server := &Server{
		config:            config,
		loopGuardRegistry: NewLoopGuardRegistry(),
		routeTableHolder:  NewThreadSafeTableHolder(&RouteTable{}),
	}

	// Add a test route with loop guard config
	routeTable := &RouteTable{routes: []RouteEntry{{
		PathTemplate: "/api/test",
		Method:       "POST",
		APIVersion:   "v1",
		LoopGuardConfig: &LoopGuardConfig{
			MaxRepeats:     3,
			Window:         "1m",
			windowDuration: 1 * time.Minute,
		},
	}}}
	server.routeTableHolder = NewThreadSafeTableHolder(routeTable)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	middleware := server.LoopGuardMiddleware(nextHandler)

	// Block hash1 (4 failures)
	for i := 0; i < 4; i++ {
		body := strings.NewReader(`{"id":"123"}`)
		req := httptest.NewRequest("POST", "/api/test?id=123", body)
		w := httptest.NewRecorder()
		middleware.ServeHTTP(w, req)
	}

	// hash1 should be blocked
	body := strings.NewReader(`{"id":"123"}`)
	req := httptest.NewRequest("POST", "/api/test?id=123", body)
	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("hash1 should be blocked, got status %d", w.Code)
	}

	// hash2 should still work (different query param)
	body = strings.NewReader(`{"id":"456"}`)
	req = httptest.NewRequest("POST", "/api/test?id=456", body)
	w = httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code == http.StatusTooManyRequests {
		t.Errorf("hash2 should not be blocked, got status %d", w.Code)
	}
}

// TestLoopGuardMiddleware_ResponseIncludesDetails tests that the 429
// response includes proper error details.
func TestLoopGuardMiddleware_ResponseIncludesDetails(t *testing.T) {
	config := &Config{
		MaxReplayableRequestBytes: 1024 * 1024,
		DryRun:                   false,
	}
	server := &Server{
		config:            config,
		loopGuardRegistry: NewLoopGuardRegistry(),
		routeTableHolder:  NewThreadSafeTableHolder(&RouteTable{}),
	}

	// Add a test route with loop guard config
	routeTable := &RouteTable{routes: []RouteEntry{{
		PathTemplate: "/api/test",
		Method:       "POST",
		APIVersion:   "v1",
		LoopGuardConfig: &LoopGuardConfig{
			MaxRepeats:     3,
			Window:         "1m",
			windowDuration: 1 * time.Minute,
		},
	}}}
	server.routeTableHolder = NewThreadSafeTableHolder(routeTable)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	middleware := server.LoopGuardMiddleware(nextHandler)

	// Block the route
	for i := 0; i < 4; i++ {
		body := strings.NewReader(`{"test":"data"}`)
		req := httptest.NewRequest("POST", "/api/test", body)
		w := httptest.NewRecorder()
		middleware.ServeHTTP(w, req)
	}

	// Make a blocked request
	body := strings.NewReader(`{"test":"data"}`)
	req := httptest.NewRequest("POST", "/api/test", body)
	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	// Check response
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Expected 429 status, got %d", w.Code)
	}

	// Check content type
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected application/json content type, got %s", contentType)
	}

	// Check Retry-After header
	retryAfter := w.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Errorf("Expected Retry-After header")
	}

	// Check response body contains expected fields
	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, "loop_guard_exceeded") {
		t.Errorf("Response should contain error code loop_guard_exceeded")
	}
	if !strings.Contains(bodyStr, "retry_after") {
		t.Errorf("Response should contain retry_after detail")
	}
	if !strings.Contains(bodyStr, "/docs/route") {
		t.Errorf("Response should contain /docs/route pointer")
	}
}

// TestLoopGuardMiddleware_QueryParamsInHash tests that query parameters
// are included in the hash.
func TestLoopGuardMiddleware_QueryParamsInHash(t *testing.T) {
	config := &Config{
		MaxReplayableRequestBytes: 1024 * 1024,
		DryRun:                   false,
	}
	server := &Server{
		config:            config,
		loopGuardRegistry: NewLoopGuardRegistry(),
		routeTableHolder:  NewThreadSafeTableHolder(&RouteTable{}),
	}

	// Add a test route with loop guard config
	routeTable := &RouteTable{routes: []RouteEntry{{
		PathTemplate: "/api/search",
		Method:       "GET",
		APIVersion:   "v1",
		LoopGuardConfig: &LoopGuardConfig{
			MaxRepeats:     3,
			Window:         "1m",
			windowDuration: 1 * time.Minute,
		},
	}}}
	server.routeTableHolder = NewThreadSafeTableHolder(routeTable)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	middleware := server.LoopGuardMiddleware(nextHandler)

	// Block requests with query param foo=bar
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest("GET", "/api/search?foo=bar", nil)
		w := httptest.NewRecorder()
		middleware.ServeHTTP(w, req)
	}

	// Request with foo=bar should be blocked
	req := httptest.NewRequest("GET", "/api/search?foo=bar", nil)
	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Request with foo=bar should be blocked, got status %d", w.Code)
	}

	// Request with different query param should not be blocked
	req = httptest.NewRequest("GET", "/api/search?foo=baz", nil)
	w = httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code == http.StatusTooManyRequests {
		t.Errorf("Request with foo=baz should not be blocked, got status %d", w.Code)
	}
}

// TestLoopGuardMiddleware_PathParamsInHash tests that path parameters
// are included in the hash.
func TestLoopGuardMiddleware_PathParamsInHash(t *testing.T) {
	config := &Config{
		MaxReplayableRequestBytes: 1024 * 1024,
		DryRun:                   false,
	}
	server := &Server{
		config:            config,
		loopGuardRegistry: NewLoopGuardRegistry(),
		routeTableHolder:  NewThreadSafeTableHolder(&RouteTable{}),
	}

	// Add a test route with loop guard config
	routeTable := &RouteTable{routes: []RouteEntry{{
		PathTemplate: "/api/users/{id}",
		Method:       "GET",
		APIVersion:   "v1",
		LoopGuardConfig: &LoopGuardConfig{
			MaxRepeats:     3,
			Window:         "1m",
			windowDuration: 1 * time.Minute,
		},
	}}}
	server.routeTableHolder = NewThreadSafeTableHolder(routeTable)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	middleware := server.LoopGuardMiddleware(nextHandler)

	// Note: This test would require proper route matching and path parameter
	// extraction to be implemented. For now, we test that the hash computation
	// includes path parameters correctly.

	hasher := NewRequestHasher(1024 * 1024)

	// Test that different path params produce different hashes
	hash1 := hasher.ComputeHash("GET", "/api/users/{id}", map[string]string{"id": "123"}, url.Values{}, nil)
	hash2 := hasher.ComputeHash("GET", "/api/users/{id}", map[string]string{"id": "456"}, url.Values{}, nil)

	if hash1 == hash2 {
		t.Errorf("Hashes with different path params should differ")
	}
}
