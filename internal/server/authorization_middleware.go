package server

import (
	"fmt"
	"log"
	"net/http"
	"strings"
)

// authorizationMiddleware implements Stage 5 of the control-plane pipeline.
//
// This middleware enforces x-required-scope from the route fragment.
// It checks whether the resolved identity has the required scope claims.
//
// Stage pipeline integration:
//   - Stage 3: Identity resolution (identityResolutionMiddleware)
//   - Stage 4: Route table lookup (validationMiddleware → route matching)
//   - Stage 5: Authorization (THIS middleware) - enforces x-required-scope
//   - Stage 6+: Spec validation, dry-run, transforms, guards, secret injection, dispatch
//
// CRITICAL: Stages 3 and 5 must activate TOGETHER and never separately.
// - Stage 3 without Stage 5: Identity is resolved but never enforced (useless)
// - Stage 5 without Stage 3: Authorization checks with no identity (denies everyone)
//
// Phase 7 activated: This middleware enforces x-required-scope with default-deny.
func (s *Server) authorizationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Stage 5.1: Get resolved identity from Stage 3
		identity := identityFromContext(ctx)
		if identity == nil {
			// This should never happen if Stage 3 ran
			log.Printf("[Stage-5-Authorization] No identity in context - allowing (should not happen)")
			next.ServeHTTP(w, r)
			return
		}

		// Stage 5.2: Get route match to check x-required-scope
		routeMatch := routeMatchFromRequest(r)
		if routeMatch == nil {
			// No route match - this is likely a control-plane path
			// Control-plane paths bypass authorization via reserved path check
			next.ServeHTTP(w, r)
			return
		}

		// Stage 5.3: Check if route has x-required-scope
		requiredScopes := routeMatch.Route.RequiredScopes
		if len(requiredScopes) == 0 {
			// No scope requirement - allow
			log.Printf("[Stage-5-Authorization] Route %s has no scope requirement - allowing", routeMatch.Route.PathTemplate)
			next.ServeHTTP(w, r)
			return
		}

		// Stage 5.4: Check if identity has any of the required scopes
		// Phase 7: Default-deny for unresolved identities
		if !identity.Resolved {
			log.Printf("[Stage-5-Authorization] Identity not resolved for route %s requiring scopes %v - denying (Phase 7)",
				routeMatch.Route.PathTemplate, requiredScopes)

			// Phase 7 activated: deny requests with unresolved identity
			NewErrorResponse(ErrCodeForbidden, "Identity resolution failed - cannot authorize scoped route").Write(w, r)
			return
		}

		// Stage 5.5: Check scope intersection
		// Identity needs at least one of the required scopes
		hasScope := false
		for _, requiredScope := range requiredScopes {
			if identity.HasScope(requiredScope) {
				log.Printf("[Stage-5-Authorization] Identity has required scope %s - allowing", requiredScope)
				hasScope = true
				break
			}
		}

		if !hasScope {
			log.Printf("[Stage-5-Authorization] Identity lacks required scopes %v (has: %v) - denying (Phase 7)",
				requiredScopes, identity.Capabilities)

			// Phase 7 activated: deny requests with insufficient scope
			NewErrorResponse(ErrCodeForbidden, fmt.Sprintf("Route requires one of scopes: %v", requiredScopes)).Write(w, r)
			return
		}

		// Proceed to next handler
		next.ServeHTTP(w, r)
	})
}

// normalizeScope normalizes a scope string for comparison
func normalizeScope(scope string) string {
	return strings.ToLower(strings.TrimSpace(scope))
}

// hasAnyScope checks if an identity has any of the required scopes
func hasAnyScope(identity *Identity, requiredScopes []string) bool {
	if identity == nil || len(requiredScopes) == 0 {
		return false
	}

	// If identity has no capabilities, it can't match any scope
	if len(identity.Capabilities) == 0 {
		return false
	}

	for _, required := range requiredScopes {
		normalizedRequired := normalizeScope(required)
		for _, capability := range identity.Capabilities {
			if normalizeScope(capability) == normalizedRequired {
				return true
			}
		}
	}

	return false
}
