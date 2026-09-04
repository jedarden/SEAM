package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// setResolveOverride installs fn as the identity source for every subsequent
// Resolve call, replacing Tailscale resolution in its entirety. It lives in a
// _test.go file rather than beside the field it arms so that a production build
// of this package has no way to swap out WhoIs resolution — only a test binary
// does.
func (ir *IdentityResolver) setResolveOverride(fn func(remoteAddr string) (*Identity, error)) {
	ir.mu.Lock()
	defer ir.mu.Unlock()
	ir.resolveOverride = fn
}

// newLoopbackTestIdentityResolver returns a resolver that resolves every caller
// to one fixed, fully-resolved tailnet-node identity.
//
// The capture/proxy suites drive a real listener over loopback, and loopback is
// not a tailnet address, so the production WhoIs path would default-deny every
// request they make. Those suites exercise identity-gated surfaces on purpose —
// /docs, /openapi.json, the operator endpoints and proxied routes — so they
// present this identity rather than asserting the deny. The probe paths need
// nothing here: stage 3 steps aside for them in production too
// (identityExemptPaths).
func newLoopbackTestIdentityResolver() *IdentityResolver {
	resolver := NewIdentityResolver()
	// Same capability set the development test mode grants (identity.go), so a
	// suite sees the scopes an operator-tier endpoint such as /config/status
	// requires (seam:ops:read).
	resolver.setResolveOverride(func(remoteAddr string) (*Identity, error) {
		return &Identity{
			Resolved:     true,
			NodeName:     "capture-proxy-test",
			NodeKey:      "capture-proxy-test-node-key",
			User:         "capture-proxy-test@example.com",
			Tags:         []string{"tag:needle-worker"},
			Capabilities: []string{"k8s-ro:get", "argocd:read", "config:read", "seam:ops:read", "seam:scopes:read-all"},
		}, nil
	})
	return resolver
}

// TestIdentityResolutionExemptsInfraProbePaths pins both halves of stage 3's
// treatment of the infrastructure endpoints: a probe or metrics scrape from an
// address with no tailnet identity still reaches its handler, and a
// scope-filtered control-plane path from that same address is still
// default-denied. Exempting the probes must not widen into exempting the
// control plane, whose endpoints are reduced to the caller's own grants.
func TestIdentityResolutionExemptsInfraProbePaths(t *testing.T) {
	s := &Server{identityResolver: NewIdentityResolver()}

	reached := false
	middleware := s.identityResolutionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	// A loopback caller is exactly what kubelet and the scraper look like:
	// reachable, and resolvable to no identity at all. /_seam/health is the
	// legacy alias bound to the same handler as /_seam/healthz and is exempted
	// alongside it by cloudflareJWTMiddleware too.
	for _, path := range []string{"/_seam/health", "/_seam/healthz", "/_seam/readyz", "/_seam/metrics"} {
		reached = false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "127.0.0.1:51000"

		rec := httptest.NewRecorder()
		middleware.ServeHTTP(rec, req)

		if !reached {
			t.Errorf("%s: handler not reached; identity resolution denied a probe with no identity to resolve", path)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("%s: expected status 200, got %d", path, rec.Code)
		}
	}

	// The same caller on a caller-facing control-plane endpoint is still
	// denied — /docs is scope-filtered to the identity stage 3 resolves.
	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	req.RemoteAddr = "127.0.0.1:51000"

	reached = false
	rec := httptest.NewRecorder()
	middleware.ServeHTTP(rec, req)

	if reached {
		t.Error("/docs: handler reached despite an unresolvable caller; default-deny is no longer active")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("/docs: expected status 403, got %d", rec.Code)
	}
}
