package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// BrownoutScheduler is middleware that enforces brownout windows for deprecated routes.
// Per Phase 8.3: DORMANT on any route without a brownout block (fail-safe).
// Serves structured 410 Gone responses during active brownout windows.
//
// Runtime semantics (the contract brownout_wiring_test.go and
// brownout_middleware_test.go pin, and docs/notes/brownout-runtime-semantics.md
// is the authority on):
//
//   - Windows are compared as absolute instants. Each start/end is RFC 3339
//     carrying an explicit offset; a window written with a non-UTC offset is
//     exactly equal to its UTC rendering, and the gateway's own local
//     timezone never participates. A window written as
//     [2024-06-15T02:00:00+02:00, 2024-06-15T04:00:00+02:00] is exactly
//     [00:00Z, 02:00Z]: a request at 2024-06-15T01:30:00Z is inside it, and
//     so is a request at 2024-06-15T02:00:00Z (the end boundary is
//     inclusive). A window that does not parse is inert (never active), the
//     fail-safe direction — lint is the up-front gate.
//   - Boundaries are inclusive on both ends: [start, end].
//   - Multiple windows are evaluated as a union: if ANY window is active the
//     route serves 410. Lint rejects overlapping windows at fragment
//     validation, so union semantics is the belt-and-braces behavior for a
//     fragment that reached the gateway without linting — an overlap can only
//     extend an outage, never narrow one. When more than one window is active
//     at the same instant (adjacent inclusive boundaries), the FIRST active
//     window in array order names the bounds in the response body.
//   - Sunset never removes a route by itself: it is advisory (plan AP-08), so
//     past the last window — and a fortiori past sunset — the route serves
//     normally until a human merges the removal PR.
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
		if !ok {
			// No route match - DORMANT (fail-safe)
			next.ServeHTTP(w, r)
			return
		}
		bs.ServeForMatch(w, r, routeMatch, next)
	})
}

// ServeForMatch applies brownout semantics for a request whose route match
// has already been resolved. The caller-facing chain wires this through
// Server.brownoutMiddleware, which resolves the route itself because stage-4
// dispatch publishes the match only after every middleware has run.
func (bs *BrownoutScheduler) ServeForMatch(w http.ResponseWriter, r *http.Request, routeMatch *RouteMatch, next http.Handler) {
	if routeMatch == nil || routeMatch.Route.Deprecated == nil {
		// No deprecation info - DORMANT (fail-safe)
		next.ServeHTTP(w, r)
		return
	}

	deprecated := routeMatch.Route.Deprecated
	if len(deprecated.Brownouts) == 0 {
		// No brownout windows defined - DORMANT (fail-safe)
		next.ServeHTTP(w, r)
		return
	}

	// Check if current time is within any brownout window (union semantics:
	// first active window in array order wins).
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

	// Plan (Observability): every brownout 410 served is a logged event. This
	// is also the operator-facing record that a live caller appeared during a
	// window — the traffic the evaluator reads from the per-route-version
	// metric to cancel the retirement.
	log.Printf("[brownout] served 410 for route %s window [%s, %s]",
		route.ID(), window.Start, window.End)
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

// brownoutMiddleware is the caller-facing enforcement point for
// x-seam-deprecated brownout windows. It is the outermost of the caller
// chain's cache/quota/brownout trio (quota innermost, then cache, then
// brownout — see Server.Start), so a 410 served inside a window precedes
// caching and metering: browned-out traffic consumes no quota, the 410 is
// never itself cached, and a cached pre-window response cannot mask an
// active window.
//
// Dispatch publishes the authoritative route match into the request context
// only inside the caller mux (stage 4, via withRouteMatch) — after every
// middleware has run — so this handler resolves the route itself with a
// read-only lookup, exactly as LoopGuardMiddleware does. The lookup publishes
// nothing; stage 4 still re-matches and republishes the match with the
// credential resolver before anything can inject a secret.
func (s *Server) brownoutMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reserved paths (health, control plane) are never proxied routes, and
		// credential probes must not observe a gateway-side 410 as an upstream
		// failure — a window would otherwise mark a healthy credential
		// unhealthy for a decision the gateway itself made.
		if isReservedPath(r.URL.Path) || isProbeRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		scheduler := s.brownoutScheduler
		if scheduler == nil {
			// Server literals in tests may leave the field unset; a fresh
			// scheduler with the real clock is the same thing NewServer builds.
			scheduler = NewBrownoutScheduler()
		}

		routeMatch := s.routeTableHolder.MatchForBrownout(r)
		if routeMatch == nil {
			// No route match — DORMANT (fail-safe)
			next.ServeHTTP(w, r)
			return
		}
		scheduler.ServeForMatch(w, r, routeMatch, next)
	})
}
