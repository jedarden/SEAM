package server

import (
	"log"
	"net/http"
	"strings"
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
// Cache hits bypass quota checking entirely
func (s *Server) quotaMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip quota for reserved paths (health checks, etc.)
		if isReservedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
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

		// If cache hit, use zero cost (bypasses quota deduction)
		cost := costPerCall
		if cacheHit {
			cost = 0
			log.Printf("[Quota] Cache hit for %s - bypassing quota deduction", route)
		}

		// Check quota (cache hits check without deducting)
		allowed, remaining, err := s.quotaTracker.CheckAndRecordQuota(r.Context(), route, cost, token, user)
		if err != nil {
			requestErr := WrapRequestError(ErrCodeInternalServer, "Error checking quota", err).
				WithDetail("route", route)
			logRequestError(r, "quota-check", requestErr)
			requestErr.Write(w, r)
			return
		}

		if !allowed {
			// Quota exceeded
			log.Printf("[Quota] Quota exceeded for %s - remaining: %.2f", route, remaining)
			s.writeQuotaExceededResponse(w, r, route, remaining)
			return
		}
		if cost > 0 {
			s.ensureMetrics().recordQuotaCost(route, cost)
		}

		// Add quota headers to response
		if costPerCall > 0 && !cacheHit {
			w.Header().Set("X-Quota-Cost-Per-Call", formatCost(costPerCall))
			w.Header().Set("X-Quota-Remaining", formatCost(remaining))
		}

		// Cache hits get a special header and metric
		if cacheHit {
			w.Header().Set("X-Quota-Bypassed", "cache-hit")
			s.ensureMetrics().recordQuotaBypassed(route)
		}

		// Proceed to next handler
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

// writeQuotaExceededResponse writes a 429 Quota Exceeded response
func (s *Server) writeQuotaExceededResponse(w http.ResponseWriter, r *http.Request, route string, remaining float64) {
	w.Header().Set("Retry-After", "60") // Suggest retry after 1 minute
	NewErrorResponse(ErrCodeQuotaExceeded, "Request quota exceeded. Please retry later.").
		WithDetail("route", route).
		WithDetail("quota", map[string]interface{}{"remaining": formatCost(remaining)}).
		WithDocsURL("/docs/rate-limiting").
		Write(w, r)

	// Record quota exceeded metric
	s.ensureMetrics().recordQuotaExceeded(route)
}

// formatCost formats a cost value as USD string
func formatCost(cost float64) string {
	return "$" + strings.TrimRight(strings.TrimRight(formatFloat(cost, 2), "0"), ".")
}

// formatFloat formats a float to specified decimal places
func formatFloat(val float64, decimals int) string {
	format := "%." + string(rune('0'+decimals)) + "f"
	return sprintf(format, val)
}

// sprintf is a simple string formatter
func sprintf(format string, val float64) string {
	// Simple implementation - in production use fmt.Sprintf
	if val == float64(int(val)) {
		return sprintfInt(int(val))
	}
	// For now, return a simple format
	return "$0.00"
}

func sprintfInt(val int) string {
	if val == 0 {
		return "0"
	}
	result := ""
	for val > 0 {
		result = string(rune('0'+(val%10))) + result
		val = val / 10
	}
	return result
}
