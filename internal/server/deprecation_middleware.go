package server

import (
	"fmt"
	"net/http"
)

// DeprecationHeaders is middleware that adds deprecation headers to responses.
// Per Phase 8.3: emits Deprecation and Sunset headers based on x-seam-deprecated.
// Adds Link rel=deprecation pointing to /docs/route and /changes.
type DeprecationHeaders struct{}

// NewDeprecationHeaders creates a new deprecation headers middleware.
func NewDeprecationHeaders() *DeprecationHeaders {
	return &DeprecationHeaders{}
}

// Middleware returns an http.Handler that adds deprecation headers.
func (dh *DeprecationHeaders) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract route match from context if available
		routeMatch, ok := r.Context().Value(routeMatchContextKey{}).(*RouteMatch)
		if !ok || routeMatch.Route.Deprecated == nil {
			// No deprecation info - proceed normally
			next.ServeHTTP(w, r)
			return
		}

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

		// Proceed with the request
		next.ServeHTTP(w, r)
	})
}
