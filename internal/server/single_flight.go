package server

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// inFlightRequest represents an in-flight request being processed
type inFlightRequest struct {
	result   *cachedResponse
	err      error
	waiters  chan struct{} // Channel for goroutines waiting on this request
	once     sync.Once     // Ensures result is broadcast only once
	done     bool          // Whether the request has completed
	cancelFn context.CancelFunc // Function to cancel the in-flight request
}

// SingleFlight manages in-flight request coalescing
// Multiple concurrent requests with the same key will share a single upstream call
type SingleFlight struct {
	mu        sync.Mutex
	inFlight  map[CacheKey]*inFlightRequest

	// Metrics
	totalCalls   int64
	dedupedCalls int64
	activeCount  int64
}

// NewSingleFlight creates a new single-flight manager
func NewSingleFlight() *SingleFlight {
	return &SingleFlight{
		inFlight: make(map[CacheKey]*inFlightRequest),
	}
}

// Do executes the given function if no request with the same key is already in flight.
// If a request is already in flight, it waits for that request to complete and returns the same result.
// Returns the cached response, any error that occurred, and a boolean indicating if this was the caller
// that executed the function (true) or waited for another caller's result (false).
func (s *SingleFlight) Do(ctx context.Context, key CacheKey, fn func(ctx context.Context) (*cachedResponse, error)) (*cachedResponse, error, bool) {
	// Fast path: try to acquire lock
	s.mu.Lock()

	// Check if there's already an in-flight request for this key
	if existing, found := s.inFlight[key]; found {
		// We found an in-flight request - increment dedup counter
		atomicAddInt64(&s.dedupedCalls, 1)
		s.mu.Unlock()

		// Wait for the existing request to complete
		return s.waitForResult(ctx, existing, key)
	}

	// No in-flight request - we'll be the one to execute it
	atomicAddInt64(&s.totalCalls, 1)
	atomicAddInt64(&s.activeCount, 1)

	// Create a new in-flight request
	reqCtx, cancelFn := context.WithCancel(ctx)
	req := &inFlightRequest{
		waiters:  make(chan struct{}),
		cancelFn: cancelFn,
	}

	// Register this request
	s.inFlight[key] = req
	s.mu.Unlock()

	// Execute the function
	defer func() {
		// Clean up the in-flight entry
		s.mu.Lock()
		delete(s.inFlight, key)
		atomicAddInt64(&s.activeCount, -1)
		s.mu.Unlock()

		// Broadcast completion to all waiters
		close(req.waiters)
	}()

	result, err := fn(reqCtx)

	// Store the result (once only)
	req.result = result
	req.err = err
	req.done = true

	return result, err, true
}

// waitForResult waits for an in-flight request to complete and returns its result
func (s *SingleFlight) waitForResult(ctx context.Context, req *inFlightRequest, key CacheKey) (*cachedResponse, error, bool) {
	// Create a timer for context cancellation
	var timer *time.Timer
	var timeout <-chan time.Time

	if dl, ok := ctx.Deadline(); ok {
		delay := time.Until(dl)
		if delay <= 0 {
			// Context already expired
			return nil, ctx.Err(), false
		}
		timer = time.NewTimer(delay)
		defer timer.Stop()
		timeout = timer.C
	}

	// Wait for the request to complete or context to be cancelled
	select {
	case <-req.waiters:
		// Request completed successfully
		return req.result, req.err, false
	case <-timeout:
		// Context deadline exceeded
		return nil, ctx.Err(), false
	case <-ctx.Done():
		// Context was cancelled
		return nil, ctx.Err(), false
	}
}

// Cancel cancels an in-flight request by key
// This is useful when a request needs to be aborted (e.g., client disconnect)
func (s *SingleFlight) Cancel(key CacheKey) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req, found := s.inFlight[key]; found {
		// Cancel the request context
		if req.cancelFn != nil {
			req.cancelFn()
		}
	}
}

// GetActiveCount returns the current number of in-flight requests
func (s *SingleFlight) GetActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inFlight)
}

// Stats returns single-flight statistics
func (s *SingleFlight) Stats() SingleFlightStats {
	totalCalls := atomicLoadInt64(&s.totalCalls)
	dedupedCalls := atomicLoadInt64(&s.dedupedCalls)

	return SingleFlightStats{
		ActiveRequests: s.GetActiveCount(),
		TotalCalls:     totalCalls,
		DedupedCalls:   dedupedCalls,
		CoalesceRate:   calculateCoalesceRate(totalCalls, dedupedCalls),
	}
}

// SingleFlightStats holds single-flight statistics
type SingleFlightStats struct {
	ActiveRequests int    // Current number of in-flight requests
	TotalCalls     int64  // Total number of calls to Do()
	DedupedCalls   int64  // Number of calls that were deduped (waited for another)
	CoalesceRate   float64 // Percentage of calls that were coalesced
}

// calculateCoalesceRate computes the coalescing rate as a percentage
// This is the percentage of total calls that were saved by coalescing
func calculateCoalesceRate(totalCalls, dedupedCalls int64) float64 {
	if totalCalls == 0 {
		return 0.0
	}
	return (float64(dedupedCalls) / float64(totalCalls)) * 100.0
}

// Helper functions for atomic operations
func atomicAddInt64(addr *int64, delta int64) int64 {
	return atomic.AddInt64(addr, delta)
}

func atomicLoadInt64(addr *int64) int64 {
	return atomic.LoadInt64(addr)
}
