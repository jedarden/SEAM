package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestSingleFlight_Coalescing tests that concurrent identical requests are coalesced
func TestSingleFlight_Coalescing(t *testing.T) {
	sf := NewSingleFlight()
	ctx := context.Background()
	key := CacheKey("test-key")

	// Track how many times the function is executed
	var executionCount int32
	var executionDelay time.Duration = 100 * time.Millisecond

	// Function that simulates an expensive upstream call
	executeFn := func(ctx context.Context) (*cachedResponse, error) {
		atomic.AddInt32(&executionCount, 1)
		time.Sleep(executionDelay) // Simulate slow upstream call
		return &cachedResponse{
			StatusCode: http.StatusOK,
			Body:       []byte("up response"),
		}, nil
	}

	// Launch 10 concurrent requests with the same key
	numRequests := 10
	results := make(chan *cachedResponse, numRequests)
	errors := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			result, err, _ := sf.Do(ctx, key, executeFn)
			if err != nil {
				errors <- err
				return
			}
			results <- result
		}()
	}

	// Collect all results
	var successfulResults int
	for i := 0; i < numRequests; i++ {
		select {
		case <-results:
			successfulResults++
		case err := <-errors:
			t.Fatalf("unexpected error: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for results")
		}
	}

	// All requests should have succeeded
	if successfulResults != numRequests {
		t.Errorf("expected %d successful results, got %d", numRequests, successfulResults)
	}

	// Only ONE execution should have occurred (coalescing)
	execCount := atomic.LoadInt32(&executionCount)
	if execCount != 1 {
		t.Errorf("expected 1 execution (coalesced), got %d", execCount)
	}

	// Verify single-flight stats
	// totalCalls counts only executions (not all Do() calls)
	stats := sf.Stats()
	if stats.TotalCalls != 1 {
		t.Errorf("expected 1 total call (execution), got %d", stats.TotalCalls)
	}
	if stats.DedupedCalls != int64(numRequests-1) {
		t.Errorf("expected %d deduped calls, got %d", numRequests-1, stats.DedupedCalls)
	}
	// Coalesce rate = dedupedCalls / totalCalls * 100 = 9 / 1 * 100 = 900%
	expectedCoalesceRate := (float64(numRequests-1) / float64(1)) * 100.0
	if stats.CoalesceRate < expectedCoalesceRate-1.0 || stats.CoalesceRate > expectedCoalesceRate+1.0 {
		t.Errorf("expected coalesce rate ~%.1f%%, got %.1f%%", expectedCoalesceRate, stats.CoalesceRate)
	}
}

// TestSingleFlight_TTLZeroDedup tests that TTL=0 mode only dedups without caching
func TestSingleFlight_TTLZeroDedup(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Set up a test route with TTL=0 (dedup only, no caching)
	s.cacheTTLs["/api/test"] = 0

	var upstreamCallCount int32
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCallCount, 1)
		time.Sleep(50 * time.Millisecond) // Simulate slow upstream
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// First request - should call upstream
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w1 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w1, req1)

	resp1 := w1.Result()
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("first request: expected status 200, got %d", resp1.StatusCode)
	}

	count1 := atomic.LoadInt32(&upstreamCallCount)
	if count1 != 1 {
		t.Errorf("first request: expected 1 upstream call, got %d", count1)
	}

	// Second request immediately after - with TTL=0, should call upstream again
	// (no caching, but concurrent requests would be deduped via single-flight)
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w2 := httptest.NewRecorder()
	cachedHandler.ServeHTTP(w2, req2)

	count2 := atomic.LoadInt32(&upstreamCallCount)
	if count2 != 2 {
		t.Errorf("second request (TTL=0, sequential): expected 2 upstream calls (no cache), got %d", count2)
	}

	if w2.Header().Get("X-SEAM-Cache") == "HIT" {
		t.Error("TTL=0 request should not have cache hit header")
	}
}

// TestSingleFlight_ConcurrentTTLZero tests concurrent requests with TTL=0
func TestSingleFlight_ConcurrentTTLZero(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Set up a test route with TTL=0 (dedup only, no caching)
	s.cacheTTLs["/api/test"] = 0

	var upstreamCallCount int32
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCallCount, 1)
		time.Sleep(100 * time.Millisecond) // Simulate slow upstream
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream response"))
	})

	cachedHandler := s.cacheMiddleware(mockHandler)

	// Launch 5 concurrent requests with the same key
	numRequests := 5
	done := make(chan bool, numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			w := httptest.NewRecorder()
			cachedHandler.ServeHTTP(w, req)
			done <- true
		}()
	}

	// Wait for all requests to complete
	for i := 0; i < numRequests; i++ {
		select {
		case <-done:
			// OK
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for concurrent requests")
		}
	}

	// With TTL=0 and concurrent requests, single-flight should coalesce to 1 call
	finalCount := atomic.LoadInt32(&upstreamCallCount)
	if finalCount != 1 {
		t.Errorf("concurrent TTL=0 requests: expected 1 upstream call (single-flight dedup), got %d", finalCount)
	}

	// Verify single-flight stats
	sfStats := s.singleFlight.Stats()
	// totalCalls counts executions (should be 1 for the single coalesced call)
	if sfStats.TotalCalls < 1 {
		t.Errorf("expected at least 1 total single-flight execution, got %d", sfStats.TotalCalls)
	}
	// dedupedCalls counts waiters (should be at least 4 for the 4 deduped requests)
	if sfStats.DedupedCalls < int64(numRequests-1) {
		t.Errorf("expected at least %d deduped calls, got %d", numRequests-1, sfStats.DedupedCalls)
	}
}

// TestSingleFlight_ContextCancellation tests that context cancellation works correctly
func TestSingleFlight_ContextCancellation(t *testing.T) {
	sf := NewSingleFlight()

	// Function that takes a long time
	executeFn := func(ctx context.Context) (*cachedResponse, error) {
		select {
		case <-time.After(5 * time.Second):
			return &cachedResponse{StatusCode: http.StatusOK}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Create a context with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	key := CacheKey("test-key")

	// This should timeout
	_, err, _ := sf.Do(ctx, key, executeFn)
	if err == nil {
		t.Error("expected timeout error, got nil")
	} else if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}

	// Verify the in-flight request was cleaned up
	stats := sf.Stats()
	if stats.ActiveRequests != 0 {
		t.Errorf("expected 0 active requests after cancellation, got %d", stats.ActiveRequests)
	}
}

// TestSingleFlight_MultipleKeys tests that different keys are handled independently
func TestSingleFlight_MultipleKeys(t *testing.T) {
	sf := NewSingleFlight()
	ctx := context.Background()

	var executionCount int32
	executeFn := func(ctx context.Context) (*cachedResponse, error) {
		atomic.AddInt32(&executionCount, 1)
		time.Sleep(50 * time.Millisecond)
		return &cachedResponse{StatusCode: http.StatusOK}, nil
	}

	// Launch concurrent requests with different keys
	keys := []CacheKey{"key1", "key2", "key3"}
	done := make(chan bool, len(keys)*3) // 3 requests per key

	for _, key := range keys {
		for i := 0; i < 3; i++ {
			go func(k CacheKey) {
				sf.Do(ctx, k, executeFn)
				done <- true
			}(key)
		}
	}

	// Wait for completion
	for i := 0; i < len(keys)*3; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout")
		}
	}

	// Should have 3 executions total (one per key)
	execCount := atomic.LoadInt32(&executionCount)
	if execCount != 3 {
		t.Errorf("expected 3 executions (one per key), got %d", execCount)
	}

	// Verify stats
	stats := sf.Stats()
	// totalCalls counts executions (3 keys = 3 executions)
	if stats.TotalCalls != 3 {
		t.Errorf("expected 3 total executions, got %d", stats.TotalCalls)
	}
	// dedupedCalls counts waiters (2 per key * 3 keys = 6)
	if stats.DedupedCalls != 6 {
		t.Errorf("expected 6 deduped calls, got %d", stats.DedupedCalls)
	}
}
