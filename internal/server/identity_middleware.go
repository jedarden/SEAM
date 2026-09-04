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
// Phase 7 activated: This middleware performs real WhoIs resolution and
// default-deny for unresolved identities. The infrastructure endpoints in
// identityExemptPaths are the one exception, and it is not a reservation of the
// control-plane branch: the branch still runs stage 3 for every other
// control-plane path, because /whoami, /scopes, /openapi.json and /docs are
// scope-filtered to the identity it resolves.
func (s *Server) identityResolutionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// kubelet's liveness and readiness probes and the VictoriaMetrics
		// scrape reach these paths at the pod IP on the pod network, which
		// never traverses the tailnet (plan.md, "The ACL governs tailnet
		// ingress and does not govern the in-cluster pod network"), so there is
		// no identity to resolve for them by construction. Default-denying an
		// unresolvable caller here would fail every probe and blank the metrics
		// series for a pod whose proxy surface is otherwise fine.
		if identityExemptPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		// Stage 3.1: Resolve caller identity from inbound connection
		identity, err := s.identityResolver.ResolveFromRequest(r)

		if err != nil {
			// Phase 7: Default-deny for unresolved identities
			log.Printf("[Stage-3-Identity] Failed to resolve identity for %s: %v", r.RemoteAddr, err)

			// Phase 7 activated: deny requests with unresolved identity
			NewErrorResponse(ErrCodeForbidden, "Identity resolution failed").Write(w, r)
			return
		}

		// Store resolved identity in request context for later stages
		ctx := contextWithIdentity(r.Context(), identity)
		*r = *r.WithContext(ctx)

		log.Printf("[Stage-3-Identity] Resolved caller: %s", identity.String())

		// Proceed to next handler
		next.ServeHTTP(w, r)
	})
}

// identityExemptPaths are the endpoints that arrive with no caller identity by
// construction. kubelet's liveness and readiness probes and the VictoriaMetrics
// scrape reach /_seam/healthz and /_seam/readyz on the caller port and
// /_seam/metrics on the operator port at the pod IP on the pod network, which
// never traverses the tailnet (plan.md, "The ACL governs tailnet ingress and
// does not govern the in-cluster pod network") — so there is no WhoIs answer to
// resolve for them, and no ACL construction could put one there. Stage 3 steps
// aside rather than default-denying, because refusing an unresolvable caller on
// these paths fails every probe and blanks the metrics series for a pod whose
// proxy surface is otherwise fine.
//
// This is the same exemption cloudflareJWTMiddleware already makes for the two
// probe paths (plus the /_seam/health alias registered to the same handler), so
// a probe that survives the outer layer no longer dies at stage 3. It is not a
// reservation of the control-plane branch: every other reserved path still runs
// stage 3, because /whoami, /scopes, /openapi.json and /docs are scope-filtered
// to the identity it resolves, and none of these four discloses anything a
// scope check would withhold.
var identityExemptPaths = map[string]bool{
	"/_seam/health":  true,
	"/_seam/healthz": true,
	"/_seam/readyz":  true,
	"/_seam/metrics": true,
}
