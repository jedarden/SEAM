package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// BrownoutScheduler is middleware that enforces brownout windows for deprecated routes.
// Per Phase 8.3: DORMANT on any route without a brownout block (fail-safe).
// Serves structured 410 Gone responses during active brownout windows.
type BrownoutScheduler struct {
	// clock allows time-based tests to inject a fixed time
	clock func() time.Time
}

// NewBrownoutScheduler creates a new brownout scheduler middleware.
func NewBrownoutScheduler() *BrownoutScheduler {
	return &BrownoutScheduler{
		clock: time.Now,
	}
}

// Middleware returns an http.Handler that checks brownout windows.
// If the current time is within an active brownout window for the route,
// it returns a 410 Gone response with deprecation information.
func (bs *BrownoutScheduler) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract route match from context if available
		routeMatch, ok := r.Context().Value(routeMatchContextKey{}).(*RouteMatch)
		if !ok || routeMatch.Route.Deprecated == nil {
			// No deprecation info or no route match - DORMANT (fail-safe)
			next.ServeHTTP(w, r)
			return
		}

		deprecated := routeMatch.Route.Deprecated
		if len(deprecated.Brownouts) == 0 {
			// No brownout windows defined - DORMANT (fail-safe)
			next.ServeHTTP(w, r)
			return
		}

		// Check if current time is within any brownout window
		now := bs.clock()
		for _, window := range deprecated.Brownouts {
			if window.IsActiveAt(now) {
				// Active brownout - return 410 Gone
				bs.serveBrownoutResponse(w, r, &routeMatch.Route, &window)
				return
			}
		}

		// Not in a brownout window - proceed normally
		next.ServeHTTP(w, r)
	})
}

// serveBrownoutResponse serves a 410 Gone response during a brownout window.
// Per Phase 8.3: structured response naming the replacement and /changes.
func (bs *BrownoutScheduler) serveBrownoutResponse(w http.ResponseWriter, r *http.Request, route *RouteEntry, window *BrownoutWindow) {
	// Build error response
	response := map[string]interface{}{
		"error":   "gone",
		"message": "This route is deprecated and currently unavailable during a scheduled brownout window",
		"brownout": map[string]interface{}{
			"start": window.Start,
			"end":   window.End,
		},
		"deprecation": map[string]interface{}{
			"since": route.Deprecated.Since,
		},
	}

	// Add sunset if available
	if route.Deprecated.Sunset != "" {
		response["deprecation"].(map[string]interface{})["sunset"] = route.Deprecated.Sunset
	}

	// Add replacement information if available
	if route.Deprecated.ReplacementPath != "" {
		replacementInfo := map[string]interface{}{
			"path": route.Deprecated.ReplacementPath,
		}
		if route.Deprecated.ReplacementVersion != "" {
			replacementInfo["version"] = route.Deprecated.ReplacementVersion
		}
		response["replacement"] = replacementInfo
	}

	// Set headers
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-SEAM-Brownout", "active")

	// Add deprecation headers
	if route.Deprecated.Since != "" {
		w.Header().Set("Deprecation", fmt.Sprintf("since=%s", route.Deprecated.Since))
	}
	if route.Deprecated.Sunset != "" {
		w.Header().Set("Sunset", route.Deprecated.Sunset)
	}

	// Add Link header to /changes
	baseURL := getBaseURL(r)
	changesURL := fmt.Sprintf("%s/changes", baseURL)
	w.Header().Add("Link", fmt.Sprintf(`<%s>; rel="deprecation"`, changesURL))

	// If replacement exists, add link to it
	if route.Deprecated.ReplacementPath != "" {
		replacementURL := route.Deprecated.ReplacementPath
		if route.Deprecated.ReplacementVersion != "" {
			replacementURL = fmt.Sprintf("%s?version=%s", replacementURL, route.Deprecated.ReplacementVersion)
		}
		w.Header().Add("Link", fmt.Sprintf(`<%s>; rel="alternate"`, replacementURL))
	}

	w.WriteHeader(http.StatusGone)
	_ = json.NewEncoder(w).Encode(response)
}

// getBaseURL extracts the base URL from the request.
func getBaseURL(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	if forwardedProto := r.Header.Get("X-Forwarded-Proto"); forwardedProto != "" {
		scheme = forwardedProto
	}

	host := r.Host
	if forwardedHost := r.Header.Get("X-Forwarded-Host"); forwardedHost != "" {
		host = forwardedHost
	}

	return fmt.Sprintf("%s://%s", scheme, host)
}

// SetClock sets the clock function for testing purposes.
func (bs *BrownoutScheduler) SetClock(clock func() time.Time) {
	bs.clock = clock
}
