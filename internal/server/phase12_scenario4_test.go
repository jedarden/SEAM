package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Phase 12.4 covers the 401 credential-refresh-and-retry path. The logic lives
// on *ReverseProxy (proxy.go handle401CredentialRefresh), not on *Server, and
// ReverseProxy is both constructible (NewReverseProxyWithConfig) and an
// http.Handler — so each scenario is driven end to end through ServeHTTP
// against an httptest upstream that only accepts the fresh credential.
//
// The one seam these tests cannot reach is RouteTable.secretClient: the
// credential resolver is injected per request via withRouteMatch, which is
// exactly the hook the tests substitute a counting stub into.
const (
	phase12StaleSecret = "stale-secret"
	phase12FreshSecret = "fresh-secret"
	phase12Route       = "/api/dispatch"
)

// phase12Upstream is a stub upstream that answers 401 for any credential other
// than the fresh one, recording every Authorization value and body it saw.
type phase12Upstream struct {
	mu        sync.Mutex
	fresh     string
	auth      []string
	bodies    []string
	always401 bool
}

func (u *phase12Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	auth := r.Header.Get("Authorization")
	u.mu.Lock()
	u.auth = append(u.auth, auth)
	u.bodies = append(u.bodies, string(body))
	always401 := u.always401
	u.mu.Unlock()

	if !always401 && auth == "Bearer "+u.fresh {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"stale credential"}`))
}

func (u *phase12Upstream) sawAuth() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.auth...)
}

func (u *phase12Upstream) sawBodies() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.bodies...)
}

// phase12Scenario wires one request through the proxy with a counting
// credential resolver. The resolver hands out the stale secret on its first
// call and the fresh secret thereafter, unless an override says otherwise.
func phase12Scenario(t *testing.T, maxReplayable int64, body []byte, resolve func(call int) ([]byte, error)) (*ReverseProxy, *httptest.ResponseRecorder, *http.Request, *int, *phase12Upstream) {
	t.Helper()

	upstream := &phase12Upstream{fresh: phase12FreshSecret}
	upstreamSrv := httptest.NewServer(upstream)
	t.Cleanup(upstreamSrv.Close)

	cfg := &ReverseProxyConfig{Client: upstreamSrv.Client()}
	if maxReplayable > 0 {
		cfg.MaxReplayableRequestBytes = maxReplayable
	}
	proxy, err := NewReverseProxyWithConfig(upstreamSrv.URL, cfg)
	if err != nil {
		t.Fatalf("NewReverseProxyWithConfig: %v", err)
	}

	calls := 0
	resolver := routeSecretResolver(func(_ context.Context, _ RouteEntry) ([]byte, error) {
		calls++
		return resolve(calls)
	})

	req := httptest.NewRequest(http.MethodPost, phase12Route, bytes.NewReader(body))
	req.Header.Set("X-Auth-Token", "test-token")
	req.Header.Set("X-User-ID", "test-user")

	route := RouteEntry{
		PathTemplate: phase12Route,
		Method:       http.MethodPost,
		VaultPath:    "secret/rs-manager/phase12/credential",
		InjectAs:     &InjectAs{Kind: InjectionBearer},
	}
	withRouteMatch(req, &RouteMatch{Route: route, PathParams: map[string]string{}}, resolver)

	rec := httptest.NewRecorder()
	return proxy, rec, req, &calls, upstream
}

// TestPhase12Scenario4_SuccessfulRetryWithReplayableBody covers the happy path:
// a replayable POST hits a 401 on the stale credential, the proxy invalidates
// and refetches, re-injects the fresh credential, and retries exactly once to a
// transparent 200.
func TestPhase12Scenario4_SuccessfulRetryWithReplayableBody(t *testing.T) {
	proxy, rec, req, calls, upstream := phase12Scenario(t, 0, []byte(`{"job":"run"}`),
		func(call int) ([]byte, error) {
			if call == 1 {
				return []byte(phase12StaleSecret), nil
			}
			return []byte(phase12FreshSecret), nil
		})

	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := *calls; got != 2 {
		t.Errorf("credential resolver calls = %d, want 2 (initial + refresh)", got)
	}
	auth := upstream.sawAuth()
	if len(auth) != 2 {
		t.Fatalf("upstream requests = %d, want 2 (initial + retry)", len(auth))
	}
	if auth[0] != "Bearer "+phase12StaleSecret {
		t.Errorf("first upstream credential = %q, want the stale secret", auth[0])
	}
	if auth[1] != "Bearer "+phase12FreshSecret {
		t.Errorf("retry upstream credential = %q, want the fresh secret", auth[1])
	}
	// The replayed body must survive the retry.
	if bodies := upstream.sawBodies(); len(bodies) != 2 || bodies[1] != `{"job":"run"}` {
		t.Errorf("retry body = %v, want the original body replayed", bodies)
	}
}

// TestPhase12Scenario4_UnreplayableBodyReturnsCredentialRefreshNotRetried
// covers a body over MaxReplayableRequestBytes: the credential is still
// invalidated and refetched, but the request is not retried and the caller gets
// the credential-refresh-not-retried envelope with a scrubbed upstream body.
func TestPhase12Scenario4_UnreplayableBodyReturnsCredentialRefreshNotRetried(t *testing.T) {
	const capBytes = 16
	bigBody := bytes.Repeat([]byte("x"), int(capBytes)*4)

	proxy, rec, req, calls, _ := phase12Scenario(t, capBytes, bigBody,
		func(call int) ([]byte, error) {
			if call == 1 {
				return []byte(phase12StaleSecret), nil
			}
			return []byte(phase12FreshSecret), nil
		})

	proxy.ServeHTTP(rec, req)

	if rec.Code != GetHTTPStatus(ErrCodeCredentialRefreshNotRetried) {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code,
			GetHTTPStatus(ErrCodeCredentialRefreshNotRetried), rec.Body.String())
	}
	if got := rec.Header().Get("X-SEAM-Credential-Refresh"); got != "succeeded" {
		t.Errorf("X-SEAM-Credential-Refresh = %q, want %q", got, "succeeded")
	}
	// Refresh happened; the request itself did not.
	if got := *calls; got != 2 {
		t.Errorf("credential resolver calls = %d, want 2 (refresh still happens)", got)
	}

	var envelope ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding envelope: %v\nbody: %s", err, rec.Body.String())
	}
	if envelope.Error != ErrCodeCredentialRefreshNotRetried {
		t.Errorf("error code = %q, want %q", envelope.Error, ErrCodeCredentialRefreshNotRetried)
	}
	if envelope.Details["upstream_status"] != float64(http.StatusUnauthorized) {
		t.Errorf("details.upstream_status = %v, want %d",
			envelope.Details["upstream_status"], http.StatusUnauthorized)
	}
}

// TestPhase12Scenario4_RefetchFailureDegradesToSecretStoreUnavailable covers a
// resolver that fails on the refresh: the caller gets secret-store-unavailable
// rather than a retry.
func TestPhase12Scenario4_RefetchFailureDegradesToSecretStoreUnavailable(t *testing.T) {
	proxy, rec, req, calls, _ := phase12Scenario(t, 0, []byte(`{"job":"run"}`),
		func(call int) ([]byte, error) {
			if call == 1 {
				return []byte(phase12StaleSecret), nil
			}
			return nil, io.ErrUnexpectedEOF
		})

	proxy.ServeHTTP(rec, req)

	if got := *calls; got != 2 {
		t.Errorf("credential resolver calls = %d, want 2 (the failing refresh is still attempted)", got)
	}
	if rec.Code != GetHTTPStatus(ErrCodeSecretStoreUnavailable) {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code,
			GetHTTPStatus(ErrCodeSecretStoreUnavailable), rec.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding envelope: %v\nbody: %s", err, rec.Body.String())
	}
	if envelope.Error != ErrCodeSecretStoreUnavailable {
		t.Errorf("error code = %q, want %q", envelope.Error, ErrCodeSecretStoreUnavailable)
	}
}

// TestPhase12Scenario4_NoInfiniteRetryLoops covers the loop guard: the retry
// itself gets a 401 and must NOT trigger a second refresh. The caller sees the
// upstream's own 401 and the upstream saw exactly two requests.
func TestPhase12Scenario4_NoInfiniteRetryLoops(t *testing.T) {
	proxy, rec, req, calls, upstream := phase12Scenario(t, 0, []byte(`{"job":"run"}`),
		func(call int) ([]byte, error) {
			// Every credential resolves to the same value the upstream rejects.
			return []byte(phase12StaleSecret), nil
		})

	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
	if got := *calls; got != 2 {
		t.Errorf("credential resolver calls = %d, want 2 (refresh must happen exactly once)", got)
	}
	if got := len(upstream.sawAuth()); got != 2 {
		t.Errorf("upstream requests = %d, want 2 (initial + one retry, no more)", got)
	}
}

// TestPhase12Scenario4_QuotaChargedTwiceOnRetryPath covers Phase 13.2
// dispatch-time accounting interacting with the retry: both the initial
// dispatch and the retry charge quota, because both reached the upstream.
func TestPhase12Scenario4_QuotaChargedTwiceOnRetryPath(t *testing.T) {
	proxy, rec, req, _, _ := phase12Scenario(t, 0, []byte(`{"job":"run"}`),
		func(call int) ([]byte, error) {
			if call == 1 {
				return []byte(phase12StaleSecret), nil
			}
			return []byte(phase12FreshSecret), nil
		})

	const cost = 0.25
	const limit = 10.00
	proxy.QuotaTracker = NewQuotaTracker()
	proxy.QuotaTracker.SetCostPerCall(phase12Route, cost)
	proxy.QuotaTracker.SetQuota(phase12Route, QuotaConfig{Limit: limit, Window: 0, Scope: "per-route"})
	*req = *req.WithContext(contextWithQuotaCost(req.Context(), cost))

	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	want := limit - 2*cost
	if got := proxy.QuotaTracker.GetRemaining(phase12Route, "test-token", "test-user"); got != want {
		t.Errorf("remaining = $%.2f, want $%.2f (charged once per dispatch)", got, want)
	}
	if hdr := rec.Header().Get("X-SEAM-Budget-Remaining"); hdr == "" {
		t.Error("X-SEAM-Budget-Remaining header missing on the retry response")
	}
}

// TestPhase12Scenario4_ProtocolUpgradeNotReplayed covers the guard in
// ServeHTTP that refuses credential injection into a protocol upgrade rather
// than buffering or replaying it.
func TestPhase12Scenario4_ProtocolUpgradeNotReplayed(t *testing.T) {
	proxy, rec, req, calls, upstream := phase12Scenario(t, 0, []byte(`{"job":"run"}`),
		func(call int) ([]byte, error) { return []byte(phase12FreshSecret), nil })

	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")

	proxy.ServeHTTP(rec, req)

	if rec.Code == http.StatusSwitchingProtocols {
		t.Fatalf("protocol upgrade was proxied with credential injection (status %d)", rec.Code)
	}
	// The request never reached the upstream: injection is refused first.
	if got := len(upstream.sawAuth()); got != 0 {
		t.Errorf("upstream requests = %d, want 0", got)
	}
	if got := *calls; got != 0 {
		t.Errorf("credential resolver calls = %d, want 0 (refused before resolving)", got)
	}
}
