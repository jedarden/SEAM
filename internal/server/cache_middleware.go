package server

import (
	"bytes"
	"context"
	"net/http"
	"time"
)

// Context key for cache hit status
type contextKey string

const cacheHitKey contextKey = "cacheHit"

// cacheMiddleware creates middleware that caches GET responses and provides single-flight coalescing
// Only applies to GET requests; other methods pass through unchanged
func (s *Server) cacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip caching for non-GET requests
		if !ShouldUseCache(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Skip caching for reserved paths (control plane endpoints)
		// Health Sentinel Integration:
		// Health sentinel probes (/_seam/health, /health/*) and other control-plane
		// endpoints bypass caching entirely. This ensures:
		//   - Health checks always execute fresh (no stale cached responses)
		//   - Control plane responses are never cached (cache pollution prevention)
		//   - No cache metrics are recorded for infrastructure traffic
		if isReservedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Generate cache key
		cacheKey := GenerateCacheKey(r.Method, r.URL.Path, r.URL.Query())

		// Try to get from cache
		if cachedResponse, found := s.cache.Get(cacheKey); found {
			// Cache hit - set context before serving response
			ctx := context.WithValue(r.Context(), cacheHitKey, true)
			r = r.WithContext(ctx)
			// Serve cached response with updated request
			s.serveCachedResponse(w, r, cachedResponse, true)
			return
		}

		// Cache miss - use single-flight to coalesce concurrent identical requests
		ttl := s.getRouteCacheTTL(r.URL.Path)

		// Execute request via single-flight
		result, err, _ := s.singleFlight.Do(r.Context(), cacheKey, func(ctx context.Context) (*cachedResponse, error) {
			// This function executes only once per cache key, even if multiple goroutines call Do() concurrently
			return s.executeAndCacheRequest(ctx, next, w, r, cacheKey, ttl)
		})

		// Handle the result
		if err != nil {
			// Check if it's a context error (cancellation/timeout)
			if ctxErr := ctxError(err); ctxErr != nil {
				// Request was cancelled - just return
				http.Error(w, "Request cancelled", http.StatusServiceUnavailable)
				return
			}
			// Some other error occurred
			http.Error(w, "Upstream request failed", http.StatusBadGateway)
			return
		}

		// Serve the response (fresh from single-flight, not an actual cache hit)
		if result != nil {
			// Cache miss - set context before serving response
			ctx := context.WithValue(r.Context(), cacheHitKey, false)
			r = r.WithContext(ctx)
			s.serveCachedResponse(w, r, result, false)
		}
	})
}

// executeAndCacheRequest executes the upstream request and caches the result if applicable
// This is called within the single-flight context, ensuring only one upstream call per key
func (s *Server) executeAndCacheRequest(ctx context.Context, next http.Handler, w http.ResponseWriter, r *http.Request, cacheKey CacheKey, ttl int) (*cachedResponse, error) {
	// Create a response recorder to capture the response
	recorder := &cacheResponseRecorder{ResponseWriter: w}

	// Create a new request with the context from single-flight
	// This ensures context cancellation propagates to the upstream handler
	reqWithContext := r.WithContext(ctx)

	// Execute the upstream request
	next.ServeHTTP(recorder, reqWithContext)

	// Check if context was cancelled during execution
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Only cache if the upstream handler completed successfully
	if recorder.statusCode > 0 && recorder.statusCode < 600 {
		cachedResp := &cachedResponse{
			StatusCode: recorder.statusCode,
			Header:     recorder.Header().Clone(),
			Body:       recorder.body.Bytes(),
		}

		// Only cache if TTL > 0 (TTL=0 means dedup only, no caching)
		if ttl > 0 {
			s.cache.Set(cacheKey, cachedResp, ttl)
		}

		return cachedResp, nil
	}

	return nil, nil
}

// ctxError checks if an error is a context error
func ctxError(err error) error {
	if err == nil {
		return nil
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return err
	}
	return nil
}

// serveCachedResponse writes a response to the client
// isActualHit indicates whether this response came from the cache (true) or was freshly executed (false)
func (s *Server) serveCachedResponse(w http.ResponseWriter, r *http.Request, cached *cachedResponse, isActualHit bool) {
	// Copy headers
	for k, v := range cached.Header {
		w.Header()[k] = v
	}

	// Add cache status header only if this is an actual cache hit
	if isActualHit {
		w.Header().Set("X-SEAM-Cache", "HIT")
		w.Header().Set("X-Quota-Bypassed", "cache-hit")
		// Remove quota cost headers for cache hits (they don't apply since quota was bypassed)
		w.Header().Del("X-Quota-Cost-Per-Call")
		w.Header().Del("X-Quota-Remaining")
		// Record metrics for cache hit
		recordCacheHit(r.URL.Path)
		// Record quota bypass
		recordQuotaBypassed(r.URL.Path)
	} else {
		// Record metrics for cache miss (fresh execution)
		recordCacheMiss(r.URL.Path)
	}

	// Write status code and body
	w.WriteHeader(cached.StatusCode)
	w.Write(cached.Body)
}

// getRouteCacheTTL returns the cache TTL for a given route path
// Returns 0 if the route has no caching configured
func (s *Server) getRouteCacheTTL(path string) int {
	// Check if we have a TTL configured for this path
	if ttl, exists := s.cacheTTLs[path]; exists {
		return ttl
	}

	// No caching configured for this route
	return 0
}

// cacheResponseRecorder captures the response status code and body for caching
type cacheResponseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
	written    bool
}

// WriteHeader captures the status code without writing to underlying
func (r *cacheResponseRecorder) WriteHeader(statusCode int) {
	if !r.written {
		r.statusCode = statusCode
		r.written = true
		// Don't write to underlying - we'll serve the response once via serveCachedResponse
	}
}

// Write captures the response body without writing to underlying
func (r *cacheResponseRecorder) Write(b []byte) (int, error) {
	if !r.written {
		r.statusCode = http.StatusOK // Default if WriteHeader not called
		r.written = true
	}
	r.body.Write(b)
	return len(b), nil // Only write to buffer, not underlying
}

// startCacheCleanup starts a background goroutine to periodically clean expired entries and update metrics
func (s *Server) startCacheCleanup() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		// Initialize metrics on startup
		stats := s.cache.Stats()
		updateCacheMetrics(stats)

		for range ticker.C {
			s.cache.Cleanup()
			// Update cache metrics (size, evictions, hit rate)
			stats := s.cache.Stats()
			updateCacheMetrics(stats)
		}
	}()
}
