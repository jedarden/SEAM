package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type endpointMatrixRow struct {
	name          string
	path          string
	listener      string
	method        string
	scope         string
	successKeys   []string
	operatorMark  string
	callerMark    string
	wrongMethod   string
	underScopeMsg string
}

func controlPlaneEndpointMatrix() []endpointMatrixRow {
	return []endpointMatrixRow{
		{
			name: "config status", path: "/config/status", listener: "operator",
			method: http.MethodGet, scope: "seam:ops:read",
			successKeys:  []string{"config", "spec", "routes", "scrubbing", "corpus", "cache", "quota", "health"},
			operatorMark: `"operator_port"`, callerMark: `"operator_port"`,
			wrongMethod: http.MethodPost, underScopeMsg: "seam:ops:read",
		},
		{
			name: "capture save", path: "/_seam/capture/save", listener: "operator",
			method: http.MethodPost, scope: "seam:ops:read",
			successKeys: []string{"status", "entry_count"}, operatorMark: `"status":"saved"`,
			callerMark: `"status":"saved"`, wrongMethod: http.MethodGet, underScopeMsg: "seam:ops:read",
		},
		{
			name: "capture status", path: "/_seam/capture/status", listener: "operator",
			method: http.MethodGet, scope: "seam:ops:read",
			successKeys: []string{"enabled", "entry_count", "corpus_dir"}, operatorMark: `"entry_count"`,
			callerMark: `"entry_count"`, wrongMethod: http.MethodPost, underScopeMsg: "seam:ops:read",
		},
		{
			name: "cache status", path: "/_seam/cache/status", listener: "operator",
			method: http.MethodGet, scope: "seam:ops:read",
			successKeys:  []string{"enabled", "size", "hits", "misses", "evictions", "hit_rate", "routes_with_cache", "single_flight"},
			operatorMark: `"single_flight"`, callerMark: `"single_flight"`,
			wrongMethod: http.MethodPost, underScopeMsg: "seam:ops:read",
		},
		{
			name: "cache cleanup", path: "/_seam/cache/cleanup", listener: "operator",
			method: http.MethodPost, scope: "seam:ops:read",
			successKeys: []string{"status", "size", "evictions"}, operatorMark: `"status":"cleanup_complete"`,
			callerMark: `"status":"cleanup_complete"`, wrongMethod: http.MethodGet, underScopeMsg: "seam:ops:read",
		},
		{
			name: "credential health", path: "/health/credentials", listener: "operator",
			method: http.MethodGet, scope: "seam:ops:read",
			successKeys:  []string{"status", "timestamp", "credentials", "circuit_breaker"},
			operatorMark: `"circuit_breaker"`, callerMark: `"circuit_breaker"`,
			wrongMethod: http.MethodPost, underScopeMsg: "seam:ops:read",
		},
		{
			name: "upstream health", path: "/health/upstreams", listener: "operator",
			method: http.MethodGet, scope: "seam:ops:read",
			successKeys: []string{"timestamp", "upstreams", "route_table"}, operatorMark: `"upstreams":`,
			callerMark: `"upstreams":`, wrongMethod: http.MethodPost, underScopeMsg: "seam:ops:read",
		},
		{
			name: "whoami", path: "/whoami", listener: "caller", method: http.MethodGet,
			successKeys:  []string{"identity", "effective_scopes", "scope_version", "resolved"},
			operatorMark: `"effective_scopes"`, callerMark: `"effective_scopes"`,
			wrongMethod: http.MethodPost,
		},
		{
			name: "scopes", path: "/scopes", listener: "caller", method: http.MethodGet,
			successKeys:  []string{"scopes", "filtered", "total_scopes", "returned", "effective_count"},
			operatorMark: `"total_scopes"`, callerMark: `"total_scopes"`,
			wrongMethod: http.MethodPost,
		},
		{
			name: "ephemeral key", path: "/api/v1/tailscale/ephemeral-key", listener: "caller",
			method: http.MethodPost, scope: "seam:tailscale:key-create",
			successKeys:  []string{"key", "id", "expires", "description"},
			operatorMark: `"description"`, callerMark: `"description"`,
			wrongMethod: http.MethodGet, underScopeMsg: "seam:tailscale:key-create",
		},
	}
}

func newControlPlaneEndpointMatrixServer(t *testing.T) *Server {
	t.Helper()
	return New(&Config{
		CallerPort:     8080,
		OperatorPort:   8081,
		BaseURL:        "http://localhost:8080",
		SpecDir:        "../../spec",
		CaptureEnabled: true,
		CorpusDir:      t.TempDir(),
	})
}

func decodeMatrixJSON(t *testing.T, body io.Reader) map[string]interface{} {
	t.Helper()
	var decoded map[string]interface{}
	if err := json.NewDecoder(body).Decode(&decoded); err != nil {
		t.Fatalf("decode JSON response: %v", err)
	}
	return decoded
}

func assertMatrixJSONResponse(t *testing.T, response *http.Response, wantStatus int, wantKeys []string) map[string]interface{} {
	t.Helper()
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != wantStatus {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want %d; body: %s", response.StatusCode, wantStatus, body)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	if cacheControl := response.Header.Get("Cache-Control"); cacheControl != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cacheControl)
	}
	body := decodeMatrixJSON(t, response.Body)
	for _, key := range wantKeys {
		if _, ok := body[key]; !ok {
			t.Errorf("response is missing documented key %q: %v", key, body)
		}
	}
	return body
}

// TestPublishedControlPlaneEndpointMatrix makes the markdown publication a
// checked artifact. It is intentionally small: wire-level assertions remain
// in the tests below, while this catches a matrix that silently drops a row or
// one of the contract columns.
func TestPublishedControlPlaneEndpointMatrix(t *testing.T) {
	data, err := os.ReadFile("../../docs/notes/control-plane-endpoint-matrix.md")
	if err != nil {
		t.Fatalf("read published endpoint matrix: %v", err)
	}
	doc := string(data)
	for _, heading := range []string{
		"| Endpoint | Listener | Method | Authorization | Status codes | Successful response shape | Cache policy |",
		"## Listener and authorization rules",
		"## Cache policy",
	} {
		if !strings.Contains(doc, heading) {
			t.Errorf("published matrix is missing %q", heading)
		}
	}
	for _, row := range controlPlaneEndpointMatrix() {
		needle := "| `" + row.path + "` | "
		lineFound := false
		for _, line := range strings.Split(doc, "\n") {
			if strings.HasPrefix(line, needle) {
				lineFound = true
				for _, field := range []string{"| " + row.listener + " |", "| `" + row.method + "` |", row.scope} {
					if !strings.Contains(line, field) {
						t.Errorf("matrix row %s is missing %q: %s", row.path, field, line)
					}
				}
				break
			}
		}
		if !lineFound {
			t.Errorf("published matrix is missing endpoint %s", row.path)
		}
	}
}

// TestControlPlaneEndpointMatrixSuccessShapes drives every requested endpoint
// on its owning mux with an authorized identity and checks the documented
// status, JSON shape, and freshness header.
func TestControlPlaneEndpointMatrixSuccessShapes(t *testing.T) {
	s := newControlPlaneEndpointMatrixServer(t)
	ops := identityWithScopes("seam:ops:read")
	caller := identityWithScopes("seam:read", "seam:scopes:read-all")

	for _, row := range controlPlaneEndpointMatrix() {
		row := row
		t.Run(row.name, func(t *testing.T) {
			if row.path == "/api/v1/tailscale/ephemeral-key" {
				upstream, _ := newEphemeralKeyFakeUpstream(t, "matrix-worker")
				e := newEphemeralKeyTestServer(t, upstream.URL)
				rec := dispatchEphemeralKey(e, ephemeralKeyIdentity(row.scope), row.method, `{"worker_id":"matrix-worker"}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
				}
				if got := rec.Header().Get("Cache-Control"); got != "no-store" {
					t.Errorf("Cache-Control = %q, want no-store", got)
				}
				body := decodeMatrixJSON(t, rec.Body)
				for _, key := range row.successKeys {
					if _, ok := body[key]; !ok {
						t.Errorf("response is missing documented key %q: %v", key, body)
					}
				}
				return
			}

			identity := ops
			mux := http.Handler(s.operatorMux)
			if row.listener == "caller" {
				identity = caller
				mux = s.callerMux
			}
			response := serveMuxWithIdentity(mux, row.method, row.path, identity)
			assertMatrixJSONResponse(t, response, http.StatusOK, row.successKeys)
		})
	}
}

// TestControlPlaneEndpointMatrixMethodsAndScopeDenials covers the two common
// negative dimensions of the matrix. Authorization is checked before method
// dispatch on operator routes, so a caller without the required scope gets
// 403 even when it also uses the wrong method.
func TestControlPlaneEndpointMatrixMethodsAndScopeDenials(t *testing.T) {
	s := newControlPlaneEndpointMatrixServer(t)
	for _, row := range controlPlaneEndpointMatrix() {
		row := row
		t.Run(row.name+" wrong method", func(t *testing.T) {
			identity := identityWithScopes("seam:ops:read")
			mux := http.Handler(s.operatorMux)
			if row.listener == "caller" {
				identity = identityWithScopes("seam:scopes:read-all", "seam:tailscale:key-create")
				mux = s.callerMux
			}
			response := serveMuxWithIdentity(mux, row.wrongMethod, row.path, identity)
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405; body: %s", response.StatusCode, response.Body)
			}
			if got := response.Header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("405 Cache-Control = %q, want no-store", got)
			}
		})

		if row.scope == "" {
			continue
		}
		t.Run(row.name+" under scoped", func(t *testing.T) {
			identity := identityWithScopes("seam:read")
			mux := http.Handler(s.operatorMux)
			if row.listener == "caller" {
				mux = s.callerMux
			}
			path := row.path
			if row.path == "/scopes" {
				path += "?all=1"
			}
			response := serveMuxWithIdentity(mux, row.method, path, identity)
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body: %s", response.StatusCode, response.Body)
			}
			contents, _ := io.ReadAll(response.Body)
			if !strings.Contains(string(contents), row.scope) {
				t.Errorf("denial body %q does not name required scope %q", contents, row.scope)
			}
			if got := response.Header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("403 Cache-Control = %q, want no-store", got)
			}
		})
	}
}

// TestControlPlaneEndpointMatrixListenerIsolation proves that the two muxes
// cannot be used interchangeably. It checks the response body as well as the
// status so a wrong-listener registration cannot leak an operator payload.
func TestControlPlaneEndpointMatrixListenerIsolation(t *testing.T) {
	s := newControlPlaneEndpointMatrixServer(t)
	identity := identityWithScopes("seam:ops:read", "seam:scopes:read-all", "seam:tailscale:key-create")
	for _, row := range controlPlaneEndpointMatrix() {
		row := row
		t.Run(row.name, func(t *testing.T) {
			if row.listener == "operator" {
				response := serveMuxWithIdentity(s.callerMux, row.method, row.path, nil)
				defer func() { _ = response.Body.Close() }()
				wantStatus := http.StatusNotFound
				if row.path == "/config/status" {
					wantStatus = http.StatusServiceUnavailable
				}
				if response.StatusCode != wantStatus {
					t.Fatalf("caller mux status = %d, want %d; body: %s", response.StatusCode, wantStatus, response.Body)
				}
				contents, _ := io.ReadAll(response.Body)
				if strings.Contains(string(contents), row.operatorMark) {
					t.Errorf("caller-mux denial leaked operator marker %q: %s", row.operatorMark, contents)
				}
				return
			}

			response := serveMuxWithIdentity(s.operatorMux, row.method, row.path, identity)
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("operator mux status = %d, want 404; body: %s", response.StatusCode, response.Body)
			}
			contents, _ := io.ReadAll(response.Body)
			if strings.Contains(string(contents), row.callerMark) {
				t.Errorf("operator-mux denial leaked caller marker %q: %s", row.callerMark, contents)
			}
		})
	}
}

// TestControlPlaneEndpointMatrixUsesReservedCacheBypass verifies the common
// cache rule directly for the caller-owned paths. Two different identities
// must receive two fresh /whoami bodies even when a TTL is configured, and
// the cache must record neither a hit nor a miss.
func TestControlPlaneEndpointMatrixUsesReservedCacheBypass(t *testing.T) {
	s := newControlPlaneEndpointMatrixServer(t)
	for _, row := range controlPlaneEndpointMatrix() {
		if !isReservedPath(row.path) {
			t.Errorf("matrix path %s is not reserved", row.path)
		}
	}

	s.cacheTTLs["/whoami"] = 300
	cachedChain := s.cacheMiddleware(s.callerMux)
	for _, identity := range []*Identity{
		identityWithScopes("scope:first"),
		identityWithScopes("scope:second"),
	} {
		identity.NodeName = identity.Capabilities[0]
		req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
		req = req.WithContext(contextWithIdentity(req.Context(), identity))
		response := httptest.NewRecorder()
		cachedChain.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("/whoami status = %d, want 200; body: %s", response.Code, response.Body)
		}
		if got := response.Header().Get("X-SEAM-Cache"); got != "" {
			t.Errorf("/whoami X-SEAM-Cache = %q, want absent", got)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("/whoami Cache-Control = %q, want no-store", got)
		}
		body := decodeMatrixJSON(t, response.Result().Body)
		identityBody, ok := body["identity"].(map[string]interface{})
		if !ok || identityBody["node_name"] != identity.NodeName {
			t.Errorf("fresh /whoami body identity = %v, want node_name=%q", body["identity"], identity.NodeName)
		}
	}
	stats := s.cache.Stats()
	if stats.Hits != 0 || stats.Misses != 0 || stats.Size != 0 {
		t.Errorf("reserved /whoami traffic touched cache: %+v", stats)
	}
}
