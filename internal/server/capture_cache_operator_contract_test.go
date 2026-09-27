package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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

// TestCacheEndpointsMethodEnforcement pins the method half of the cache
// pair's contract through the gated route: a wrong method is a 405 envelope
// whose message names the one accepted method, carrying no-store like every
// response on these routes. The capture pair's method errors are pinned in
// capture_endpoints_test.go; this covers the two routes that file omits.
func TestCacheEndpointsMethodEnforcement(t *testing.T) {
	s := New(&Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	})
	identity := identityWithScopes("seam:ops:read")

	for _, tc := range []struct {
		name           string
		method         string
		path           string
		allowedMessage string
	}{
		{name: "cache status rejects POST", method: http.MethodPost, path: "/_seam/cache/status", allowedMessage: "Only GET method is allowed"},
		{name: "cache cleanup rejects GET", method: http.MethodGet, path: "/_seam/cache/cleanup", allowedMessage: "Only POST method is allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := serveMuxWithIdentity(s.operatorMux, tc.method, tc.path, identity)
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, resp.StatusCode)
			}

			var envelope struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
				t.Fatalf("decode 405 envelope: %v", err)
			}
			if envelope.Error != "method_not_allowed" {
				t.Errorf("envelope error = %q, want method_not_allowed", envelope.Error)
			}
			if envelope.Message != tc.allowedMessage {
				t.Errorf("envelope message = %q, want %q", envelope.Message, tc.allowedMessage)
			}
			if got := resp.Header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("405 Cache-Control = %q, want no-store", got)
			}
		})
	}
}

// TestCaptureSaveFlushIsIdempotent pins the flush semantics the doc states:
// save is a snapshot rewrite, not a drain-and-append, so repeated saves
// neither duplicate nor drop entries; and the request contract is
// method-plus-path, so a request body is ignored rather than required or
// rejected.
func TestCaptureSaveFlushIsIdempotent(t *testing.T) {
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

	wrapped := s.captureMiddleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/once", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("capture setup request: expected %d, got %d", http.StatusNoContent, w.Code)
	}

	readCorpusEntries := func() []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(corpusDir, "corpus.json"))
		if err != nil {
			t.Fatalf("read corpus file: %v", err)
		}
		var corpus CorpusFile
		if err := json.Unmarshal(data, &corpus); err != nil {
			t.Fatalf("decode corpus file: %v", err)
		}
		encoded, err := json.Marshal(corpus.Entries)
		if err != nil {
			t.Fatalf("re-encode corpus entries: %v", err)
		}
		return encoded
	}

	var entriesAfterFirstSave []byte
	for i, wantEntries := range []int{1, 1} {
		// The body is deliberately non-empty: the handler must never read it.
		req := httptest.NewRequest(http.MethodPost, "/_seam/capture/save",
			strings.NewReader(`{"unexpected":"payload"}`))
		req = req.WithContext(contextWithIdentity(req.Context(), identity))
		recorder := httptest.NewRecorder()
		s.operatorMux.ServeHTTP(recorder, req)

		resp := recorder.Result()
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("save %d: expected status %d, got %d", i+1, http.StatusOK, resp.StatusCode)
		}

		var result struct {
			Status     string `json:"status"`
			EntryCount int    `json:"entry_count"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode save %d response: %v", i+1, err)
		}
		if result.Status != "saved" || result.EntryCount != wantEntries {
			t.Errorf("save %d response = %+v, want status=saved entry_count=%d",
				i+1, result, wantEntries)
		}

		entries := readCorpusEntries()
		if got := bytes.Count(entries, []byte(`"path"`)); got != wantEntries {
			t.Errorf("save %d: corpus holds %d entries, want %d", i+1, got, wantEntries)
		}
		if i == 0 {
			entriesAfterFirstSave = entries
		}
	}

	// Second snapshot must be byte-identical on the entry set: no duplication,
	// no loss, no reordering between flushes.
	if !bytes.Equal(entriesAfterFirstSave, readCorpusEntries()) {
		t.Errorf("second save changed the corpus entry set")
	}
}

// TestCacheCleanupIsIdempotent pins cleanup's idempotence: a second call
// immediately after the first finds nothing new expired and reports the
// identical cumulative counters, so cleanup is safe to retry.
func TestCacheCleanupIsIdempotent(t *testing.T) {
	s := New(&Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	})
	identity := identityWithScopes("seam:ops:read")

	s.cache.Set(CacheKey("GET /live"),
		&cachedResponse{StatusCode: http.StatusOK, Header: http.Header{}, Body: []byte("live")}, 300)
	s.cache.Set(CacheKey("GET /expired"),
		&cachedResponse{StatusCode: http.StatusOK, Header: http.Header{}, Body: []byte("stale")}, -1)

	cleanupOnce := func() struct {
		Status    string `json:"status"`
		Size      int    `json:"size"`
		Evictions int64  `json:"evictions"`
	} {
		t.Helper()
		resp := serveMuxWithIdentity(s.operatorMux, http.MethodPost, "/_seam/cache/cleanup", identity)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
		}
		var body struct {
			Status    string `json:"status"`
			Size      int    `json:"size"`
			Evictions int64  `json:"evictions"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode cleanup response: %v", err)
		}
		return body
	}

	first := cleanupOnce()
	if first.Status != "cleanup_complete" || first.Size != 1 || first.Evictions != 1 {
		t.Fatalf("first cleanup = %+v, want cleanup_complete size=1 evictions=1", first)
	}
	second := cleanupOnce()
	if second != first {
		t.Errorf("second cleanup = %+v, want identical counters from the first (%+v): "+
			"evictions are cumulative, not per-call", second, first)
	}
}

// TestCacheStatusResponseSchema pins the cache/status response shape as a
// closed key set, top level and single_flight: the fields an operator script
// may rely on, their types, and the enabled constant.
func TestCacheStatusResponseSchema(t *testing.T) {
	s := New(&Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	})
	s.cache.Set(CacheKey("GET /schema"),
		&cachedResponse{StatusCode: http.StatusOK, Header: http.Header{}, Body: []byte("x")}, 300)

	resp := serveMuxWithIdentity(s.operatorMux, http.MethodGet, "/_seam/cache/status",
		identityWithScopes("seam:ops:read"))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status response: %v", err)
	}

	wantTopLevel := []string{
		"enabled", "size", "hits", "misses", "evictions",
		"hit_rate", "routes_with_cache", "single_flight",
	}
	if len(status) != len(wantTopLevel) {
		t.Errorf("top-level key set changed: got %v, want exactly %v", keysOf(status), wantTopLevel)
	}
	for _, key := range wantTopLevel {
		if _, ok := status[key]; !ok {
			t.Errorf("missing top-level key %q", key)
		}
	}

	if enabled, ok := status["enabled"].(bool); !ok || !enabled {
		t.Errorf("enabled = %v, want the constant true", status["enabled"])
	}
	for _, key := range []string{"size", "hits", "misses", "evictions", "hit_rate", "routes_with_cache"} {
		if _, ok := status[key].(float64); !ok {
			t.Errorf("key %q = %T, want a JSON number", key, status[key])
		}
	}
	if size, _ := status["size"].(float64); int(size) != 1 {
		t.Errorf("size = %v, want the one seeded entry", status["size"])
	}

	singleFlight, ok := status["single_flight"].(map[string]interface{})
	if !ok {
		t.Fatalf("single_flight = %T, want an object", status["single_flight"])
	}
	wantSingleFlight := []string{"active_requests", "total_calls", "deduped_calls", "coalesce_rate"}
	if len(singleFlight) != len(wantSingleFlight) {
		t.Errorf("single_flight key set changed: got %v, want exactly %v",
			keysOf(singleFlight), wantSingleFlight)
	}
	for _, key := range wantSingleFlight {
		if _, ok := singleFlight[key].(float64); !ok {
			t.Errorf("single_flight.%s = %T, want a JSON number", key, singleFlight[key])
		}
	}
}

// keysOf returns a key set for a decoded JSON object, for failure messages.
func keysOf(object map[string]interface{}) []string {
	out := make([]string, 0, len(object))
	for key := range object {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
