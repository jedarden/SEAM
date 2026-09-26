package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/tailscale"
)

// The /api/v1/tailscale/ephemeral-key endpoint contract (Phase 7): a reserved
// caller-listener control-plane path that turns the caller's resolved identity
// plus the seam:tailscale:key-create scope into a Tailscale ephemeral auth key
// for a NEEDLE worker, fronting the internal/tailscale client's cache and
// hold-down behaviour. These tests drive the full caller route table, not the
// handler in isolation, so the registration itself is part of what is pinned.

// ephemeralKeyIdentity returns a resolved caller identity carrying the given
// capability scopes, shaped as the identity middleware resolves one.
func ephemeralKeyIdentity(capabilities ...string) *Identity {
	return &Identity{
		NodeKey:      "nodekey:ephemeral-key-test",
		NodeName:     "needle-worker.test.ts.net",
		Capabilities: capabilities,
		Resolved:     true,
	}
}

// newEphemeralKeyTestServer builds a Server with the caller and operator route
// tables registered and, when baseURL is non-empty, a Tailscale client pointed
// at that upstream. An empty baseURL leaves tailscaleClient nil, which is the
// not-configured deployment shape the handler must refuse gracefully.
func newEphemeralKeyTestServer(t *testing.T, baseURL string) *Server {
	t.Helper()
	s := &Server{
		scopeVersionCache: NewScopeVersionCache(),
		callerMux:         http.NewServeMux(),
		operatorMux:       http.NewServeMux(),
	}
	if baseURL != "" {
		client, err := tailscale.New(tailscale.Config{
			APIKey:  "test-api-key",
			Tailnet: "test-tailnet",
			BaseURL: baseURL,
		})
		if err != nil {
			t.Fatalf("tailscale.New: %v", err)
		}
		s.tailscaleClient = client
	}
	s.setupRoutes()
	return s
}

// dispatchEphemeralKey sends a request to the endpoint through the caller mux
// with the caller identity injected, as the identity middleware would.
func dispatchEphemeralKey(s *Server, identity *Identity, method, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/tailscale/ephemeral-key", strings.NewReader(body))
	if identity != nil {
		req = req.WithContext(contextWithIdentity(req.Context(), identity))
	}
	rec := httptest.NewRecorder()
	s.callerMux.ServeHTTP(rec, req)
	return rec
}

// decodeEphemeralKeyError decodes a structured ErrorResponse envelope.
func decodeEphemeralKeyError(t *testing.T, rec *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	var env ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v; body: %s", err, rec.Body.String())
	}
	return env
}

// newEphemeralKeyFakeUpstream stands in for the Tailscale API. It answers key
// creation with a fixed key and records every call plus the last create
// request, so tests can assert both the proxying contract and how many times
// the upstream was actually hit.
func newEphemeralKeyFakeUpstream(t *testing.T, workerID string) (*httptest.Server, *ephemeralKeyFakeUpstreamState) {
	t.Helper()
	expires := time.Date(2026, 12, 25, 12, 0, 0, 0, time.UTC)
	state := &ephemeralKeyFakeUpstreamState{expires: expires}
	state.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.calls++
		state.lastAuth = r.Header.Get("Authorization")
		var req tailscale.CreateKeyRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		state.lastCreate = req
		state.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tailscale.Key{
			ID:          "key-1234",
			Key:         "tskey-ephemeral-test",
			KeyType:     "EPHEMERAL",
			Description: "NEEDLE worker: " + workerID,
			Expires:     expires,
		})
	}))
	t.Cleanup(state.server.Close)
	return state.server, state
}

type ephemeralKeyFakeUpstreamState struct {
	server     *httptest.Server
	mu         sync.Mutex
	calls      int
	lastAuth   string
	lastCreate tailscale.CreateKeyRequest
	expires    time.Time
}

func (s *ephemeralKeyFakeUpstreamState) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *ephemeralKeyFakeUpstreamState) lastRequest() (auth string, create tailscale.CreateKeyRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAuth, s.lastCreate
}

func TestEphemeralKeyEndpointRequiresKeyCreateScope(t *testing.T) {
	upstream, state := newEphemeralKeyFakeUpstream(t, "needle-alpha")
	s := newEphemeralKeyTestServer(t, upstream.URL)
	body := `{"worker_id":"needle-alpha"}`

	t.Run("nil identity is refused", func(t *testing.T) {
		rec := dispatchEphemeralKey(s, nil, http.MethodPost, body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusForbidden, rec.Body.String())
		}
		env := decodeEphemeralKeyError(t, rec)
		if env.Error != ErrCodeForbidden {
			t.Errorf("error = %q, want %q", env.Error, ErrCodeForbidden)
		}
		if got, ok := env.Details["required_scope"].(string); !ok || got != "seam:tailscale:key-create" {
			t.Errorf("details.required_scope = %v, want seam:tailscale:key-create", env.Details["required_scope"])
		}
		if state.callCount() != 0 {
			t.Errorf("upstream hit %d times on refusal, want 0", state.callCount())
		}
	})

	t.Run("identity without the scope is refused", func(t *testing.T) {
		rec := dispatchEphemeralKey(s, ephemeralKeyIdentity("seam:read", "seam:write"), http.MethodPost, body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusForbidden, rec.Body.String())
		}
		env := decodeEphemeralKeyError(t, rec)
		if env.Error != ErrCodeForbidden {
			t.Errorf("error = %q, want %q", env.Error, ErrCodeForbidden)
		}
		if state.callCount() != 0 {
			t.Errorf("upstream hit %d times on refusal, want 0", state.callCount())
		}
	})

	t.Run("scope match normalizes case and surrounding whitespace", func(t *testing.T) {
		rec := dispatchEphemeralKey(s,
			ephemeralKeyIdentity(" SEAM:Tailscale:Key-Create "), http.MethodPost, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
	})
}

func TestEphemeralKeyEndpointIssuanceResponseShape(t *testing.T) {
	upstream, state := newEphemeralKeyFakeUpstream(t, "needle-alpha")
	s := newEphemeralKeyTestServer(t, upstream.URL)
	identity := ephemeralKeyIdentity("seam:tailscale:key-create")

	rec := dispatchEphemeralKey(s, identity, http.MethodPost, `{"worker_id":"needle-alpha"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var issued struct {
		Key         string `json:"key"`
		ID          string `json:"id"`
		Expires     string `json:"expires"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&issued); err != nil {
		t.Fatalf("decode issuance: %v; body: %s", err, rec.Body.String())
	}
	if issued.Key != "tskey-ephemeral-test" {
		t.Errorf("key = %q, want tskey-ephemeral-test", issued.Key)
	}
	if issued.ID != "key-1234" {
		t.Errorf("id = %q, want key-1234", issued.ID)
	}
	if issued.Description != "NEEDLE worker: needle-alpha" {
		t.Errorf("description = %q, want %q", issued.Description, "NEEDLE worker: needle-alpha")
	}
	expires, err := time.Parse(time.RFC3339, issued.Expires)
	if err != nil {
		t.Fatalf("expires %q is not RFC3339: %v", issued.Expires, err)
	}
	if !expires.Equal(state.expires) {
		t.Errorf("expires = %v, want %v", expires, state.expires)
	}

	// The issuance is backed by a real create call with the Phase 7 key
	// capabilities and the worker-scoped description.
	auth, create := state.lastRequest()
	if auth != "Bearer test-api-key" {
		t.Errorf("upstream Authorization = %q, want Bearer test-api-key", auth)
	}
	if !create.Capabilities.Devices.Create.Ephemeral {
		t.Error("upstream create.ephemeral = false, want true")
	}
	if !create.Capabilities.Devices.Create.Preauthorized {
		t.Error("upstream create.preauthorized = false, want true")
	}
	if got := create.Capabilities.Devices.Create.Tags; len(got) != 1 || got[0] != "tag:needle-worker" {
		t.Errorf("upstream create.tags = %v, want [tag:needle-worker]", got)
	}
	if want := int64((90 * 24 * time.Hour).Seconds()); create.ExpirySeconds != want {
		t.Errorf("upstream expirySeconds = %d, want %d", create.ExpirySeconds, want)
	}
	if create.Description != "NEEDLE worker: needle-alpha" {
		t.Errorf("upstream description = %q, want %q", create.Description, "NEEDLE worker: needle-alpha")
	}

	// A repeat request for the same worker within the cache TTL is served from
	// the client cache without a second upstream call.
	again := dispatchEphemeralKey(s, identity, http.MethodPost, `{"worker_id":"needle-alpha"}`)
	if again.Code != http.StatusOK {
		t.Fatalf("repeat status = %d, want %d; body: %s", again.Code, http.StatusOK, again.Body.String())
	}
	var reissued struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.NewDecoder(again.Body).Decode(&reissued); err != nil {
		t.Fatalf("decode repeat issuance: %v", err)
	}
	if reissued.ID != issued.ID || reissued.Key != issued.Key {
		t.Errorf("repeat issuance = %+v, want the cached key %+v", reissued, issued)
	}
	if got := state.callCount(); got != 1 {
		t.Errorf("upstream hit %d times after repeat request, want 1 (cache)", got)
	}
}

func TestEphemeralKeyEndpointHoldDownAfterUpstreamFailure(t *testing.T) {
	// Mirrors TestCreateEphemeralKeyHoldDown in internal/tailscale at the
	// endpoint boundary: a failed create enters the hold-down window, and the
	// next request is refused without touching the upstream again.
	var calls int
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Rate limit exceeded"})
	}))
	defer upstream.Close()

	s := newEphemeralKeyTestServer(t, upstream.URL)
	identity := ephemeralKeyIdentity("seam:tailscale:key-create")
	body := `{"worker_id":"needle-alpha"}`

	first := dispatchEphemeralKey(s, identity, http.MethodPost, body)
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first status = %d, want %d; body: %s", first.Code, http.StatusInternalServerError, first.Body.String())
	}
	env := decodeEphemeralKeyError(t, first)
	if env.Error != ErrCodeInternalServer {
		t.Errorf("first error = %q, want %q", env.Error, ErrCodeInternalServer)
	}
	mu.Lock()
	hits := calls
	mu.Unlock()
	if hits != 1 {
		t.Fatalf("upstream hit %d times after first request, want 1", hits)
	}

	second := dispatchEphemeralKey(s, identity, http.MethodPost, body)
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, want %d; body: %s", second.Code, http.StatusServiceUnavailable, second.Body.String())
	}
	env = decodeEphemeralKeyError(t, second)
	if env.Error != ErrCodeServiceUnavailable {
		t.Errorf("second error = %q, want %q", env.Error, ErrCodeServiceUnavailable)
	}
	if got, ok := env.Details["retry_after"].(string); !ok || got != "30s" {
		t.Errorf("details.retry_after = %v, want 30s", env.Details["retry_after"])
	}
	mu.Lock()
	hits = calls
	mu.Unlock()
	if hits != 1 {
		t.Errorf("upstream hit %d times during hold-down, want still 1", hits)
	}
}

func TestEphemeralKeyEndpointRefusalEnvelope(t *testing.T) {
	s := newEphemeralKeyTestServer(t, "")
	identity := ephemeralKeyIdentity("seam:tailscale:key-create")

	t.Run("non-POST method", func(t *testing.T) {
		rec := dispatchEphemeralKey(s, identity, http.MethodGet, "")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusMethodNotAllowed, rec.Body.String())
		}
		env := decodeEphemeralKeyError(t, rec)
		if env.Error != ErrCodeMethodNotAllowed {
			t.Errorf("error = %q, want %q", env.Error, ErrCodeMethodNotAllowed)
		}
	})

	t.Run("malformed request body", func(t *testing.T) {
		rec := dispatchEphemeralKey(s, identity, http.MethodPost, `{"worker_id":`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}
		env := decodeEphemeralKeyError(t, rec)
		if env.Error != ErrCodeBadRequest {
			t.Errorf("error = %q, want %q", env.Error, ErrCodeBadRequest)
		}
	})

	t.Run("missing worker_id", func(t *testing.T) {
		rec := dispatchEphemeralKey(s, identity, http.MethodPost, `{}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}
		env := decodeEphemeralKeyError(t, rec)
		if env.Error != ErrCodeBadRequest {
			t.Errorf("error = %q, want %q", env.Error, ErrCodeBadRequest)
		}
		if got, ok := env.Details["field"].(string); !ok || got != "worker_id" {
			t.Errorf("details.field = %v, want worker_id", env.Details["field"])
		}
	})

	t.Run("unconfigured Tailscale client", func(t *testing.T) {
		rec := dispatchEphemeralKey(s, identity, http.MethodPost, `{"worker_id":"needle-alpha"}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
		}
		env := decodeEphemeralKeyError(t, rec)
		if env.Error != ErrCodeServiceUnavailable {
			t.Errorf("error = %q, want %q", env.Error, ErrCodeServiceUnavailable)
		}
	})
}
