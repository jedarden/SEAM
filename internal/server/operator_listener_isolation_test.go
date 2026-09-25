package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The route registration (setupRoutes) puts capture, status, and health
// surfaces on the operator mux alone, and Start() serves that mux only from
// the listener bound to OperatorPort. The documentation promises that a
// caller-facing port cannot reach those endpoints while the operator port
// can, but no supplied workflow proved it over real sockets:
// operator_scope_test.go drives the scope middleware in isolation with
// httptest.NewRequest, and the control-plane verification's listener-split
// case only logs the expected route lists. These tests close the gap end to
// end: a real two-listener server, real HTTP over loopback, and both halves
// of the contract — the operator port serves every operator-only endpoint,
// and the caller port denies them without echoing operator payload.

// operatorOnlyEndpoint is one operator-only surface plus the body markers
// that identify the operator handler's payload. Markers are payload-value
// strings, never bare path fragments: the caller port's denial envelope
// echoes the request path (RouteNotFound embeds "path": "/health/upstreams"),
// so a path substring would false-positive as a leak. Each marker is shaped
// so the path echo cannot match it: `"upstreams":` needs a quote-and-colon
// the echo lacks, `"credentials"` needs an opening quote the `/health/`
// prefix displaces.
type operatorOnlyEndpoint struct {
	name            string
	method          string
	path            string
	operatorMarkers []string
}

// operatorOnlyEndpoints enumerates every route registered on the operator mux
// (setupRoutes, server.go) except the catch-all "/". When a new operator-only
// endpoint is registered there, add it here so the isolation contract follows
// it.
func operatorOnlyEndpoints() []operatorOnlyEndpoint {
	return []operatorOnlyEndpoint{
		{
			name:            "config-status",
			method:          http.MethodGet,
			path:            "/config/status",
			operatorMarkers: []string{`"operator_port"`},
		},
		{
			name:            "capture-status",
			method:          http.MethodGet,
			path:            "/_seam/capture/status",
			operatorMarkers: []string{`"corpus_dir"`, `"entry_count"`},
		},
		{
			name:   "capture-save",
			method: http.MethodPost,
			path:   "/_seam/capture/save",
			operatorMarkers: []string{
				`"status":"saved"`,
				`"entry_count"`,
			},
		},
		{
			name:            "cache-status",
			method:          http.MethodGet,
			path:            "/_seam/cache/status",
			operatorMarkers: []string{`"single_flight"`, `"hit_rate"`},
		},
		{
			name:            "cache-cleanup",
			method:          http.MethodPost,
			path:            "/_seam/cache/cleanup",
			operatorMarkers: []string{`"cleanup_complete"`},
		},
		{
			name:            "health-credentials",
			method:          http.MethodGet,
			path:            "/health/credentials",
			operatorMarkers: []string{`"credentials"`, `"circuit_breaker"`},
		},
		{
			name:            "health-upstreams",
			method:          http.MethodGet,
			path:            "/health/upstreams",
			operatorMarkers: []string{`"upstreams":`},
		},
		{
			name:            "metrics",
			method:          http.MethodGet,
			path:            "/_seam/metrics",
			operatorMarkers: []string{"# HELP"},
		},
	}
}

// isolationTestClient bounds every request so a listener that never came up
// fails the test with a timeout instead of hanging the suite.
var isolationTestClient = &http.Client{Timeout: 5 * time.Second}

// newListenerIsolationTestServer starts a real server on two loopback ports
// with capture enabled, so the capture endpoints exercise the real middleware
// rather than its not-enabled branches. The fixed loopback identity is what
// lets the operator port's scope-gated endpoints return 200: loopback is not
// a tailnet address, so production WhoIs resolution would default-deny every
// request this suite makes (see newLoopbackTestIdentityResolver).
func newListenerIsolationTestServer(t *testing.T) *Server {
	t.Helper()

	callerPort := getAvailablePort(t)
	operatorPort := getAvailablePort(t)

	cfg := &Config{
		CallerPort:     callerPort,
		OperatorPort:   operatorPort,
		BaseURL:        fmt.Sprintf("http://localhost:%d", callerPort),
		SpecDir:        "../../spec",
		CaptureEnabled: true,
		CorpusDir:      t.TempDir(),
		AllowlistFile:  newBaselineAllowlistFile(t),
	}

	s := New(cfg)
	s.identityResolver = newLoopbackTestIdentityResolver()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	// The isolation contract does not depend on OpenBao readiness; construct
	// it explicitly so an ambient OpenBao configuration cannot skew the
	// health endpoints this suite asserts on.
	s.setOpenBaoReady(true)

	return s
}

// requestListener issues one real HTTP request against one listener and
// returns the status code and body.
func requestListener(t *testing.T, port int, method, path string) (int, string) {
	t.Helper()

	req, err := http.NewRequest(method, fmt.Sprintf("http://localhost:%d%s", port, path), nil)
	if err != nil {
		t.Fatalf("Failed to build request for %s %s: %v", method, path, err)
	}
	resp, err := isolationTestClient.Do(req)
	if err != nil {
		t.Fatalf("Request %s %s against port %d failed: %v", method, path, port, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body for %s %s: %v", method, path, err)
	}
	return resp.StatusCode, string(body)
}

// TestOperatorListenerIsolationEndToEnd proves both halves of the listener
// contract for every operator-only endpoint: the operator port serves it with
// its operator payload, and the caller port denies it without leaking that
// payload into the denial body.
func TestOperatorListenerIsolationEndToEnd(t *testing.T) {
	s := newListenerIsolationTestServer(t)

	for _, ep := range operatorOnlyEndpoints() {
		t.Run(ep.name, func(t *testing.T) {
			// Operator port: the endpoint must be served, and the response
			// must be the operator handler's own payload.
			opStatus, opBody := requestListener(t, s.config.OperatorPort, ep.method, ep.path)
			if opStatus != http.StatusOK {
				t.Fatalf("Operator port must serve %s %s with 200, got %d: %s",
					ep.method, ep.path, opStatus, opBody)
			}
			for _, marker := range ep.operatorMarkers {
				if !strings.Contains(opBody, marker) {
					t.Errorf("Operator port response for %s is missing marker %s; body: %s",
						ep.path, marker, opBody)
				}
			}

			// Caller port: the same path must be denied — any 4xx/5xx counts,
			// because two different denials are correct here depending on how
			// the path reaches dispatch. Undocumented operator paths (the
			// /_seam/capture and /_seam/cache surfaces, /health/*) fall
			// through the caller mux to dispatch, find no route, and 404.
			// Documented ones (/config/status, /_seam/metrics are declared in
			// spec/openapi.yaml) match the route table with no upstream
			// target and 503 no_upstream_configured. What must never happen
			// is a served response, so anything below 400 fails — a 3xx would
			// redirect the caller to operator content, which is exposure too.
			callerStatus, callerBody := requestListener(t, s.config.CallerPort, ep.method, ep.path)
			if callerStatus < 400 {
				t.Errorf("Caller port must deny %s %s, got %d: %s",
					ep.method, ep.path, callerStatus, callerBody)
			}

			// The denial must not carry the operator payload. A caller port
			// that ever proxies, caches, or echoes operator state fails here
			// even if the status code alone looked benign.
			for _, marker := range ep.operatorMarkers {
				if strings.Contains(callerBody, marker) {
					t.Errorf("Caller port denial for %s leaks operator marker %s; body: %s",
						ep.path, marker, callerBody)
				}
			}
		})
	}
}

// TestCallerListenerServesOwnEndpoints is the positive control for the
// isolation suite: in the same server run where every operator-only path is
// denied on the caller port, the caller listener still serves its own
// surfaces. Without this, a caller listener that failed to come up at all
// would pass the denial checks for the wrong reason.
func TestCallerListenerServesOwnEndpoints(t *testing.T) {
	s := newListenerIsolationTestServer(t)

	t.Run("healthz", func(t *testing.T) {
		status, body := requestListener(t, s.config.CallerPort, http.MethodGet, "/_seam/healthz")
		if status != http.StatusOK {
			t.Fatalf("Caller port must serve /_seam/healthz with 200, got %d: %s", status, body)
		}
		if strings.TrimSpace(body) != "OK" {
			t.Errorf("Expected healthz body 'OK', got %q", body)
		}
	})

	// The served alias rides the same caller listener as /_seam/healthz. The
	// mux-level alias tests pass even if the listener were rewired to serve a
	// mux that never received the alias registration, so pin the alias on the
	// real socket too, including the handler's own no-store directive.
	t.Run("health-alias", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet,
			fmt.Sprintf("http://localhost:%d/_seam/health", s.config.CallerPort), nil)
		if err != nil {
			t.Fatalf("Failed to build request for /_seam/health: %v", err)
		}
		resp, err := isolationTestClient.Do(req)
		if err != nil {
			t.Fatalf("Request GET /_seam/health against port %d failed: %v", s.config.CallerPort, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("Failed to read response body for /_seam/health: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Caller port must serve /_seam/health with 200, got %d: %s", resp.StatusCode, body)
		}
		if strings.TrimSpace(string(body)) != "OK" {
			t.Errorf("Expected health alias body 'OK', got %q", body)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("/_seam/health Cache-Control = %q, want no-store", got)
		}
	})

	// readyz gates on real readiness (allowlist + OpenBao), so pinning it
	// proves the caller listener routes through the full readiness chain
	// and not just a liveness stub.
	t.Run("readyz", func(t *testing.T) {
		status, body := requestListener(t, s.config.CallerPort, http.MethodGet, "/_seam/readyz")
		if status != http.StatusOK {
			t.Fatalf("Caller port must serve /_seam/readyz with 200 once OpenBao is ready, got %d: %s", status, body)
		}
		if !strings.Contains(body, `"ready":true`) {
			t.Errorf("Expected readyz body to report readiness, got %q", body)
		}
	})

	t.Run("openapi", func(t *testing.T) {
		status, body := requestListener(t, s.config.CallerPort, http.MethodGet, "/openapi.json")
		if status != http.StatusOK {
			t.Fatalf("Caller port must serve /openapi.json with 200, got %d: %s", status, body)
		}
		if !strings.Contains(body, `"openapi"`) {
			t.Errorf("Expected openapi.json body to carry the spec, got: %s", body)
		}
	})
}
