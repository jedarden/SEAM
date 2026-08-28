package server

import (
	"log"
	"net/http"
)

// identityResolutionMiddleware implements Stage 3 of the control-plane pipeline.
//
// This middleware resolves the caller's identity using Tailscale's WhoIs API
// and extracts scope claims from the Tailscale Grant's app capability field.
//
// Stage pipeline integration:
//   - Stage 1: Control-plane path detection (validationMiddleware)
//   - Stage 2: Header stripping (headerStrippingMiddleware)
//   - Stage 3: Identity resolution (THIS middleware) - resolves caller identity
//   - Stage 4: Route table lookup (validationMiddleware → route matching)
//   - Stage 5: Authorization (authorizationMiddleware) - enforces x-required-scope
//
// CRITICAL: Stages 3 and 5 must activate TOGETHER and never separately.
// - Stage 3 without Stage 5: Identity is resolved but never enforced (useless)
// - Stage 5 without Stage 3: Authorization checks with no identity (denies everyone)
//
// Before Phase 7: This middleware records "the reserved anonymous identity"
// and passes all requests through (ACL serves as security boundary).
//
// After Phase 7: This middleware performs real WhoIs resolution and
// default-deny for unresolved identities.
func (s *Server) identityResolutionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stage 3.1: Resolve caller identity from inbound connection
		identity, err := s.identityResolver.ResolveFromRequest(r)

		if err != nil {
			// Phase 7: Default-deny for unresolved identities
			// For now, log but allow (ACL is the security boundary)
			log.Printf("[Stage-3-Identity] Failed to resolve identity for %s: %v", r.RemoteAddr, err)

			// TODO: Phase 7 activation - return 403 for unresolved identities
			// NewErrorResponse(ErrCodeForbidden, "Identity resolution failed").Write(w, r)
			// return

			// Pre-Phase 7: Create anonymous identity
			identity = &Identity{
				NodeName:   r.RemoteAddr,
				Resolved:   false,
				NodeKey:    "anonymous",
				Tags:       []string{}, // No tags for anonymous
				Capabilities: []string{}, // No capabilities
			}
		}

		// Store resolved identity in request context for later stages
		ctx := contextWithIdentity(r.Context(), identity)
		*r = *r.WithContext(ctx)

		log.Printf("[Stage-3-Identity] Resolved caller: %s", identity.String())

		// Proceed to next handler
		next.ServeHTTP(w, r)
	})
}
