package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// isCacheHit checks if the request context indicates a cache hit
func isCacheHit(r *http.Request) bool {
	if r == nil || r.Context() == nil {
		return false
	}
	cacheHit, ok := r.Context().Value(cacheHitKey).(bool)
	return ok && cacheHit
}

// quotaMiddleware enforces quota limits for cost-governed routes
// Phase 13.2: Quota is checked before dispatch, deducted AT DISPATCH time
// Cache hits bypass quota checking entirely
// Sentinel probes (/health/*) bypass quota checking entirely
func (s *Server) quotaMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip quota for reserved paths (health checks, etc.) - Phase 13.2
		if isReservedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Check for X-SEAM-Dry-Run header - Phase 13.2
		if r.Header.Get("X-SEAM-Dry-Run") == "1" {
			log.Printf("[Quota] Dry-run mode for %s - validation only, no quota charge", r.URL.Path)
			// Set dry-run context for downstream handlers
			ctx := contextWithDryRun(r.Context())
			r = r.WithContext(ctx)

			// For dry-run, we don't check quota - just validate and return
			s.writeDryRunValidationResponse(w, r, r.URL.Path)
			return
		}

		// Check if this is a cache hit from the context set by cache middleware
		cacheHit := isCacheHit(r)

		// Extract token and user from context (would be set by auth middleware)
		// For now, we'll extract from headers for demonstration
		token := r.Header.Get("X-Auth-Token")
		user := r.Header.Get("X-User-ID")

		// Get the route for cost lookup
		route := r.URL.Path

		// Get the cost per call for this route
		costPerCall := s.getCostPerCall(route)
		log.Printf("[Quota] Route %s has cost per call: $%.2f", route, costPerCall)

		// If cache hit, bypass quota entirely - Phase 13.2
		if cacheHit {
			log.Printf("[Quota] Cache hit for %s - bypassing quota check and deduction", route)
			w.Header().Set("X-Quota-Bypassed", "cache-hit")
			s.ensureMetrics().recordQuotaBypassed(route)
			next.ServeHTTP(w, r)
			return
		}

		// Phase 13.2: Check quota without deducting (deduction happens at dispatch)
		// Pass cost=0 to check quota without deducting
		allowed, remaining, err := s.quotaTracker.CheckQuotaOnly(r.Context(), route, costPerCall, token, user)
		if err != nil {
			requestErr := WrapRequestError(ErrCodeInternalServer, "Error checking quota", err).
				WithDetail("route", route)
			logRequestError(r, "quota-check", requestErr)
			requestErr.Write(w, r)
			return
		}

		if !allowed {
			// Phase 13.2: Return 402 Payment Required for quota exhaustion
			log.Printf("[Quota] Quota exceeded for %s - remaining: $%.2f", route, remaining)
			s.writeQuotaExceededResponse(w, r, route, remaining, costPerCall)
			return
		}

		// Phase 13.2: Store cost in context for deduction at dispatch time
		ctx := contextWithQuotaCost(r.Context(), costPerCall)
		r = r.WithContext(ctx)

		log.Printf("[Quota] Quota check passed for %s - cost will be deducted at dispatch", route)

		// Add X-SEAM-Budget-Remaining header to response (will be set by proxy after dispatch)
		// For now, set a placeholder - proxy will update with actual remaining after dispatch
		if costPerCall > 0 {
			w.Header().Set("X-SEAM-Budget-Remaining", formatBudgetRemaining(remaining-costPerCall, costPerCall, s.quotaTracker.GetWindowDuration()))
		}

		// Proceed to next handler (dispatch will deduct quota)
		next.ServeHTTP(w, r)
	})
}

// getCostPerCall retrieves the cost per call for a route
// Reads from the quota tracker, which is the canonical source of truth for cost configuration
func (s *Server) getCostPerCall(route string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// First check if we have a cost in the server's map (for backwards compatibility)
	if cost, exists := s.costPerCalls[route]; exists {
		return cost
	}

	// If not found, check the quota tracker's cost configuration
	// This allows tests to configure costs via quotaTracker.SetCostPerCall()
	return s.quotaTracker.GetCostPerCall(route)
}

// writeQuotaExceededResponse writes a 402 Payment Required response for quota exhaustion
// Phase 13.2: 402 is used for quota refusal, 429 stays with loop breaker
func (s *Server) writeQuotaExceededResponse(w http.ResponseWriter, r *http.Request, route string, remaining float64, costPerCall float64) {
	// Calculate window reset time
	windowDuration := s.quotaTracker.GetWindowDuration()
	resetTime := time.Now().Add(windowDuration)

	// Set Retry-After to seconds until window reset
	retryAfterSeconds := int(windowDuration.Seconds())
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSeconds))

	// Set X-SEAM-Budget-Remaining header
	w.Header().Set("X-SEAM-Budget-Remaining", formatBudgetRemaining(remaining, costPerCall, windowDuration))

	// Write 402 Payment Required response
	NewErrorResponse(ErrCodeQuotaExceeded, "Payment Required - quota exceeded").
		WithDetail("route", route).
		WithDetail("quota", map[string]interface{}{
			"remaining":   formatCost(remaining),
			"costPerCall": formatCost(costPerCall),
			"resetsIn":    fmt.Sprintf("%d seconds", retryAfterSeconds),
			"resetsAt":    resetTime.Format(time.RFC3339),
		}).
		WithDocsURL("/docs/quota").
		Write(w, r)

	// Record quota exceeded metric
	s.ensureMetrics().recordQuotaExceeded(route)
}

// writeDryRunValidationResponse writes a validation response for dry-run mode
// Phase 13.2: X-SEAM-Dry-Run: 1 = validation verdict at stage 7
func (s *Server) writeDryRunValidationResponse(w http.ResponseWriter, r *http.Request, route string) {
	// Get route match for validation
	routeTable := s.getRouteTable()
	if routeTable == nil {
		requestErr := WrapRequestError(ErrCodeInternalServer, "Route table not available", nil).
			WithDetail("route", route)
		logRequestError(r, "dry-run", requestErr)
		requestErr.Write(w, r)
		return
	}

	// Attempt to match route
	match := routeTable.Match(r)
	if match == nil {
		// Route not found - validation failure
		NewErrorResponse(ErrCodeRouteNotFound, "Route not found - validation failed").
			WithDetail("route", route).
			WithDetail("dryRun", true).
			Write(w, r)
		return
	}

	// Route found - validation success
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-SEAM-Dry-Run", "validated")
	w.WriteHeader(http.StatusOK)

	validation := map[string]interface{}{
		"status":   "validated",
		"route":    route,
		"method":   r.Method,
		"upstream": match.Route.UpstreamTarget,
		"message":  "Request would be accepted - dry-run validation passed",
	}

	// Add cost info if available
	costPerCall := s.getCostPerCall(route)
	if costPerCall > 0 {
		validation["costPerCall"] = formatCost(costPerCall)
		validation["quotaEnabled"] = true
	} else {
		validation["quotaEnabled"] = false
	}

	// Add breaker info if configured
	if match.Route.BreakerConfig != nil {
		validation["circuitBreaker"] = map[string]interface{}{
			"enabled":   match.Route.BreakerConfig.Enabled,
			"threshold": match.Route.BreakerConfig.Threshold,
		}
	}

	json.NewEncoder(w).Encode(validation)
	log.Printf("[Quota] Dry-run validation passed for %s", route)
}

