package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The four operator capture/cache routes are the capture workflow's flush
// point (capture/save) and the response-cache's operations surface (cache/*).
// docs/notes/control-plane-api-contracts.md pins the whole operator port —
// including /_seam/capture/* and /_seam/cache/* — as "gated on seam:ops:read
// by the operator scope middleware", and every handler response carries
// Cache-Control: no-store. operator_scope_test.go drives the middleware in
// isolation, which proves nothing about route wiring, so these tests go
// through the operator mux itself.

// captureCacheEndpoints enumerates the four routes under contract with the
// method each handler accepts.
func captureCacheEndpoints() []struct {
	name   string
	method string
	path   string
} {
	return []struct {
		name   string
		method string
		path   string
	}{
		{name: "capture save", method: http.MethodPost, path: "/_seam/capture/save"},
		{name: "capture status", method: http.MethodGet, path: "/_seam/capture/status"},
		{name: "cache status", method: http.MethodGet, path: "/_seam/cache/status"},
		{name: "cache cleanup", method: http.MethodPost, path: "/_seam/cache/cleanup"},
	}
}

// identityWithScopes returns a resolved identity carrying exactly the given
// scope claims.
func identityWithScopes(scopes ...string) *Identity {
	return &Identity{
		Resolved:     true,
		NodeName:     "capture-cache-scope-test",
		NodeKey:      "capture-cache-scope-test-key",
		Capabilities: scopes,
	}
}

// serveMuxWithIdentity drives one request through a mux with an identity in
// context, as stage 3 would leave it. A nil identity is itself a case under
// test: it is what the context looks like for a caller the identity stage
// could not resolve.
func serveMuxWithIdentity(mux http.Handler, method, path string, identity *Identity) *http.Response {
	req := httptest.NewRequest(method, path, nil)
	if identity != nil {
		req = req.WithContext(contextWithIdentity(req.Context(), identity))
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)
	return recorder.Result()
}

// TestCaptureCacheEndpointsRequireOpsReadScope pins the scope gate on the
// route registrations themselves: a caller with no identity and a caller
// resolved without the scope are both 403 with the required scope named and
// no-store on the denial, while the scope alone admits the caller to a 200
// that also carries no-store.
func TestCaptureCacheEndpointsRequireOpsReadScope(t *testing.T) {
	s := New(&Config{
		CallerPort:     8080,
		OperatorPort:   8081,
		BaseURL:        "http://localhost:8080",
		SpecDir:        "../../spec",
		CaptureEnabled: true,
		CorpusDir:      t.TempDir(),
	})

	for _, ep := range captureCacheEndpoints() {
		t.Run(ep.name, func(t *testing.T) {
			for _, identity := range []*Identity{nil, identityWithScopes("k8s-ro:get")} {
				resp := serveMuxWithIdentity(s.operatorMux, ep.method, ep.path, identity)
				defer func() { _ = resp.Body.Close() }()

				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("caller with identity %v: expected status %d, got %d",
						identity, http.StatusForbidden, resp.StatusCode)
				}
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("read denial body: %v", err)
				}
				if !strings.Contains(string(body), "seam:ops:read") {
					t.Errorf("denial must name the required scope, got: %s", body)
				}
				if got := resp.Header.Get("Cache-Control"); got != "no-store" {
					t.Errorf("denial Cache-Control = %q, want no-store", got)
				}
			}

			resp := serveMuxWithIdentity(s.operatorMux, ep.method, ep.path, identityWithScopes("seam:ops:read"))
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("caller with seam:ops:read: expected status %d, got %d",
					http.StatusOK, resp.StatusCode)
			}
			if got := resp.Header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("success Cache-Control = %q, want no-store", got)
			}
		})
	}
}

// TestCaptureCacheStatusReflectsCaptureState pins the status half of the
// capture contract through the gated route: enabled mirrors the middleware's
// enabled flag, and entry_count counts the captured-but-not-yet-flushed
// entries the save endpoint is the flush point for.
func TestCaptureCacheStatusReflectsCaptureState(t *testing.T) {
	corpusDir := t.TempDir()
	s := New(&Config{
		CallerPort:     8080,
		OperatorPort:   8081,
		BaseURL:        "http://localhost:8080",
		SpecDir:        "../../spec",
		CaptureEnabled: true,
		CorpusDir:      corpusDir,
	})
	identity := identityWithScopes("seam:ops:read")

	t.Run("disabled capture reports enabled false", func(t *testing.T) {
		disabled := New(&Config{
			CallerPort:   8080,
			OperatorPort: 8081,
			BaseURL:      "http://localhost:8080",
			SpecDir:      "../../spec",
		})
		resp := serveMuxWithIdentity(disabled.operatorMux, http.MethodGet, "/_seam/capture/status", identity)
		defer func() { _ = resp.Body.Close() }()

		var status struct {
			Enabled    bool `json:"enabled"`
			EntryCount int  `json:"entry_count"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if status.Enabled || status.EntryCount != 0 {
			t.Errorf("disabled capture status = %+v, want enabled=false entry_count=0", status)
		}
	})

	t.Run("pending entries counted before flush", func(t *testing.T) {
		wrapped := s.captureMiddleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		w := httptest.NewRecorder()
		wrapped.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/pending", nil))
		if w.Code != http.StatusNoContent {
			t.Fatalf("capture setup request: expected %d, got %d", http.StatusNoContent, w.Code)
		}

		resp := serveMuxWithIdentity(s.operatorMux, http.MethodGet, "/_seam/capture/status", identity)
		defer func() { _ = resp.Body.Close() }()

		var status struct {
			Enabled    bool `json:"enabled"`
			EntryCount int  `json:"entry_count"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if !status.Enabled || status.EntryCount != 1 {
			t.Errorf("status = %+v, want enabled=true with the one pending entry", status)
		}
	})
}

// TestCacheCleanupReportsEvictions pins the cleanup contract: an expired
// entry seeded into the server's own cache is evicted, the response counts
// the eviction and the surviving size, and cache/status reports the same
// counter afterwards.
func TestCacheCleanupReportsEvictions(t *testing.T) {
	s := New(&Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	})
	identity := identityWithScopes("seam:ops:read")

	live := &cachedResponse{StatusCode: http.StatusOK, Header: http.Header{}, Body: []byte("live")}
	expired := &cachedResponse{StatusCode: http.StatusOK, Header: http.Header{}, Body: []byte("stale")}
	s.cache.Set(CacheKey("GET /live"), live, 300)
	s.cache.Set(CacheKey("GET /expired"), expired, -1)

	resp := serveMuxWithIdentity(s.operatorMux, http.MethodPost, "/_seam/cache/cleanup", identity)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	var cleanup struct {
		Status    string `json:"status"`
		Size      int    `json:"size"`
		Evictions int64  `json:"evictions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cleanup); err != nil {
		t.Fatalf("decode cleanup response: %v", err)
	}
	if cleanup.Status != "cleanup_complete" {
		t.Errorf("cleanup status = %q, want cleanup_complete", cleanup.Status)
	}
	if cleanup.Size != 1 {
		t.Errorf("size after cleanup = %d, want the one live entry", cleanup.Size)
	}
	if cleanup.Evictions != 1 {
		t.Errorf("evictions = %d, want the one expired entry", cleanup.Evictions)
	}

	statusResp := serveMuxWithIdentity(s.operatorMux, http.MethodGet, "/_seam/cache/status", identity)
	defer func() { _ = statusResp.Body.Close() }()

	var status struct {
		Size      int   `json:"size"`
		Evictions int64 `json:"evictions"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if status.Size != 1 || status.Evictions != 1 {
		t.Errorf("status after cleanup = %+v, want size=1 evictions=1", status)
	}
}
