package server

import (
	"context"
	"net/http"
	"sync"
)

// LoopGuardRegistry manages loop guards for all routes.
// Per Phase 13.1: key is (route, hash) - strictly more conservative pre-Phase 7.
type LoopGuardRegistry struct {
	mu     sync.RWMutex
	guards map[string]*LoopGuard // routeID -> LoopGuard
}

// NewLoopGuardRegistry creates a new loop guard registry.
func NewLoopGuardRegistry() *LoopGuardRegistry {
	return &LoopGuardRegistry{
		guards: make(map[string]*LoopGuard),
	}
}

// GetOrCreateGuard gets or creates a loop guard for the given route.
func (r *LoopGuardRegistry) GetOrCreateGuard(routeID string, config LoopGuardConfig) (*LoopGuard, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Return existing guard if present
	if guard, ok := r.guards[routeID]; ok {
		return guard, nil
	}

	// Create new guard
	guard, err := NewLoopGuard(routeID, config)
	if err != nil {
		return nil, err
	}

	r.guards[routeID] = guard
	return guard, nil
}

// GetGuard gets a loop guard for the given route, returning nil if not found.
func (r *LoopGuardRegistry) GetGuard(routeID string) *LoopGuard {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.guards[routeID]
}

// Context key constants for storing loop guard check result in the request context.
const (
	loopGuardHashKey contextKey = iota
	loopGuardCheckedKey
)

// contextWithLoopGuardHash stores the loop guard hash in the request context.
func contextWithLoopGuardHash(ctx context.Context, hash string) context.Context {
	return context.WithValue(ctx, loopGuardHashKey, hash)
}

// loopGuardHashFromContext extracts the loop guard hash from the context.
func loopGuardHashFromContext(ctx context.Context) string {
	if hash, ok := ctx.Value(loopGuardHashKey).(string); ok {
		return hash
	}
	return ""
}

// contextWithLoopGuardChecked marks that loop guard has been checked.
func contextWithLoopGuardChecked(ctx context.Context, checked bool) context.Context {
	return context.WithValue(ctx, loopGuardCheckedKey, checked)
}

// loopGuardCheckedFromContext returns whether loop guard has been checked.
func loopGuardCheckedFromContext(ctx context.Context) bool {
	if checked, ok := ctx.Value(loopGuardCheckedKey).(bool); ok {
		return checked
	}
	return false
}

// LoopGuardMiddleware checks for repeated identical failing requests.
// Per Phase 13.1: dry-runs and probes never counted.
func (s *Server) LoopGuardMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip loop guard for:
		// 1. Reserved paths (health checks, control plane)
		// 2. Probe-originated requests (x-credential-probe)
		// 3. Dry-run mode
		if isReservedPath(r.URL.Path) || isProbeRequest(r) || s.config.DryRun {
			next.ServeHTTP(w, r)
			return
		}

		// Get route match from context
		routeMatch := getRouteMatchFromContext(r.Context())
		if routeMatch == nil {
			// No route match, skip loop guard
			next.ServeHTTP(w, r)
			return
		}

		// Check if route has loop guard configured
		route := routeMatch.Route
		if route.LoopGuardConfig == nil || s.loopGuardRegistry == nil {
			next.ServeHTTP(w, r)
			return
		}

		// Get or create loop guard for this route
		guard, err := s.loopGuardRegistry.GetOrCreateGuard(
			route.ID(),
			*route.LoopGuardConfig,
		)
		if err != nil {
			// Log error but allow request (fail-open for safety)
			log.Printf("[loop-guard] Failed to get/create guard for route %s: %v", route.ID(), err)
			next.ServeHTTP(w, r)
			return
		}

		// Compute canonical hash for this request
		hasher := NewRequestHasher(s.config.MaxReplayableRequestBytes)

		// Read replayable body if present (up to maxReplayableRequestBytes)
		var body []byte
		if r.Body != nil && r.Method != "GET" && r.Method != "HEAD" {
			var err error
			body, err = ReadReplayableBody(r, s.config.MaxReplayableRequestBytes)
			if err != nil {
				// Log error but continue with empty body
				log.Printf("[loop-guard] Failed to read request body: %v", err)
				body = nil
			}
		}

		// Compute hash
		hash := hasher.ComputeHash(
			r.Method,
			routeMatch.Route.PathTemplate,
			routeMatch.PathParams,
			r.URL.Query(),
			body,
		)

		// Store hash in context for later success/failure recording
		ctx := contextWithLoopGuardHash(r.Context(), hash)
		ctx = contextWithLoopGuardChecked(ctx, true)
		*r = r.WithContext(ctx)

		// Check if request should be allowed
		allowed, retryAfter, err := guard.CheckRequest(hash)
		if err != nil {
			// Log error but allow request (fail-open for safety)
			log.Printf("[loop-guard] Error checking request: %v", err)
			next.ServeHTTP(w, r)
			return
		}

		if !allowed {
			// Request blocked by loop guard - return 429
			// Get current failure count for this hash
			snapshot := guard.Snapshot()
			hashTracks := snapshot["hash_tracks"].(map[string]interface{})
			repeatCount := 0
			if track, ok := hashTracks[hash].(map[string]interface{}); ok {
				if count, ok := track["failure_count"].(int); ok {
					repeatCount = count
				}
			}

			errResp := NewLoopGuardErrorResponse(
				repeatCount,
				retryAfter,
				guard.RouteID(),
			)
			errResp.Write(w, r)

			// Add Retry-After header
			w.Header().Set("Retry-After", string(rune(retryAfter)))

			return
		}

		// Request allowed, continue to next handler
		next.ServeHTTP(w, r)
	})
}

// RecordLoopGuardSuccess records a successful response for loop guard tracking.
// Per Phase 13.1: A 2xx on the same hash clears that hash's counter.
func (s *Server) RecordLoopGuardSuccess(w http.ResponseWriter, r *http.Request) {
	if s.loopGuardRegistry == nil {
		return
	}

	// Skip if loop guard wasn't checked for this request
	if !loopGuardCheckedFromContext(r.Context()) {
		return
	}

	// Get hash from context
	hash := loopGuardHashFromContext(r.Context())
	if hash == "" {
		return
	}

	// Get route match
	routeMatch := getRouteMatchFromContext(r.Context())
	if routeMatch == nil {
		return
	}

	// Get guard for this route
	guard := s.loopGuardRegistry.GetGuard(routeMatch.Route.ID())
	if guard == nil {
		return
	}

	// Record success
	guard.RecordSuccess(hash)
}

// RecordLoopGuardFailure records a failed response for loop guard tracking.
func (s *Server) RecordLoopGuardFailure(w http.ResponseWriter, r *http.Request) {
	if s.loopGuardRegistry == nil {
		return
	}

	// Skip if loop guard wasn't checked for this request
	if !loopGuardCheckedFromContext(r.Context()) {
		return
	}

	// Get hash from context
	hash := loopGuardHashFromContext(r.Context())
	if hash == "" {
		return
	}

	// Get route match
	routeMatch := getRouteMatchFromContext(r.Context())
	if routeMatch == nil {
		return
	}

	// Get guard for this route
	guard := s.loopGuardRegistry.GetGuard(routeMatch.Route.ID())
	if guard == nil {
		return
	}

	// Record failure
	guard.RecordFailure(hash)
}

// isProbeRequest detects if this is a probe-originated request.
// Per Phase 13.1: dry-runs and probes never counted.
func isProbeRequest(r *http.Request) bool {
	// Check for probe marker header
	return r.Header.Get("X-SEAM-Probe") == "true"
}

// Helper function to get route match from context
func getRouteMatchFromContext(ctx context.Context) *RouteMatch {
	// This would be set by the route matching middleware
	// For now, we'll need to add this to the context in the routing layer
	return nil
}

// ID returns a stable identifier for this route.
func (re RouteEntry) ID() string {
	// Route ID is: method:apiVersion:pathTemplate
	// This provides a stable key for loop guard tracking
	return re.Method + ":" + re.APIVersion + ":" + re.PathTemplate
}
