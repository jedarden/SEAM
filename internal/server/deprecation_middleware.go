package server

import (
	"fmt"
	"net/http"
)

// DeprecationHeaders writes the x-seam-deprecated response-header set for a
// resolved route match:
//
//   - `Deprecation: since=<since>` — the since date verbatim (RFC 9745's
//     field; the schema stores a bare ISO date and it is emitted as written).
//   - `Sunset: <sunset>` — verbatim, only when the fragment declares one.
//   - `Link` headers: rel="deprecation" to <base>/docs/route (the served doc
//     for the route) and to <base>/changes, plus rel="alternate" to the
//     replacement path (with ?version= when declared) when one exists.
//
// The header NAMES are the unprefixed RFC 9745 / RFC 8594 fields. Some
// example fragments declare `X-Deprecation`/`X-Sunset` response headers in
// their documented responses; those describe the example API document, not
// the gateway's emission contract — see
// docs/notes/brownout-runtime-semantics.md ("Deprecation and Sunset
// response headers").
type DeprecationHeaders struct{}

// NewDeprecationHeaders creates a new deprecation headers middleware.
func NewDeprecationHeaders() *DeprecationHeaders {
	return &DeprecationHeaders{}
}

// Apply writes the deprecation header set for routeMatch onto w. It only
// mutates the response headers — it never writes the body or the status —
// so it must be called before the response is written, which is how both
// emission points below use it.
func (dh *DeprecationHeaders) Apply(w http.ResponseWriter, r *http.Request, routeMatch *RouteMatch) {
	deprecated := routeMatch.Route.Deprecated

	// Add Deprecation header with since date
	if deprecated.Since != "" {
		w.Header().Set("Deprecation", fmt.Sprintf("since=%s", deprecated.Since))
	}

	// Add Sunset header if sunset date is set
	if deprecated.Sunset != "" {
		w.Header().Set("Sunset", deprecated.Sunset)
	}

	// Add Link header to /docs/route
	baseURL := getBaseURL(r)
	docsRouteURL := fmt.Sprintf("%s/docs/route?path=%s&version=%s",
		baseURL,
		routeMatch.Route.PathTemplate,
		routeMatch.Route.APIVersion,
	)
	w.Header().Add("Link", fmt.Sprintf(`<%s>; rel="deprecation"`, docsRouteURL))

	// Add Link header to /changes
	changesURL := fmt.Sprintf("%s/changes", baseURL)
	w.Header().Add("Link", fmt.Sprintf(`<%s>; rel="deprecation"`, changesURL))

	// Add Link header to replacement if available
	if deprecated.ReplacementPath != "" {
		replacementURL := deprecated.ReplacementPath
		if deprecated.ReplacementVersion != "" {
			replacementURL = fmt.Sprintf("%s?version=%s", replacementURL, deprecated.ReplacementVersion)
		}
		w.Header().Add("Link", fmt.Sprintf(`<%s>; rel="alternate"; title="Replacement API"`, replacementURL))
	}
}

// Middleware returns an http.Handler that adds deprecation headers to
// responses based on a route match already published in the request context.
//
// This is the context-driven form, for handlers that run where stage-4
// dispatch has already published the match (inside the caller mux). The
// caller-facing chain does NOT use this form — stage 4 publishes the match
// only after every outer middleware has run — so the chain emits the same
// header set through the deprecated-route enforcement point instead
// (BrownoutScheduler.ServeForMatch applies it on every pass-through).
func (dh *DeprecationHeaders) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract route match from context if available
		routeMatch, ok := r.Context().Value(routeMatchContextKey{}).(*RouteMatch)
		if !ok || routeMatch == nil || routeMatch.Route.Deprecated == nil {
			// No deprecation info - proceed normally
			next.ServeHTTP(w, r)
			return
		}

		dh.Apply(w, r, routeMatch)

		// Proceed with the request
		next.ServeHTTP(w, r)
	})
}
