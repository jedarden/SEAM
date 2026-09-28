package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Cross-cutting health-sentinel isolation. Each sentinel already carries a
// per-endpoint contract suite — health_alias_test.go (the liveness names),
// readyz_contract_test.go and openbao_readiness_test.go (readiness),
// credentials_health_contract_test.go and health_sentinel_test.go (credential
// health), health_upstreams_state_test.go (upstream health) — and
// cache_quota_health_interaction_test.go pins the reserved-path cache/quota
// bypass. This file pins the five sentinel names against each other and
// against the two listeners, per the plan's listener split (docs/plan/plan.md:
// the caller port carries /_seam/healthz and /_seam/readyz, the operator port
// carries /health/*) and docs/design/cache-health-sentinel-integration.md
// ("Health Sentinel Endpoints"):
//
//   - listener isolation in both directions: a sentinel answers 200 only on
//     its own listener, and the other listener refuses with 404 without ever
//     echoing that sentinel's payload;
//   - the seam:ops:read registration gate on /health/upstreams, including the
//     deny half its allow-half suite does not cover;
//   - Cache-Control: no-store on every readiness verdict, both the 200 and the
//     503, matching the other three sentinels;
//   - real-handler cache and quota bypass for /_seam/readyz through the
//     production caller chain — the liveness names are pinned in
//     health_alias_test.go and the operator pair in
//     TestHealthSentinelConfiguredTTLAndCostStayBypassed.

// newSentinelIsolationTestServer arms the shared health-sentinel fixture with
// the allowlist readiness dependency neutralised the same way
// newHealthAliasTestServer does, so the readiness legs of these tests isolate
// the route_table dependency they manipulate.
func newSentinelIsolationTestServer(t *testing.T) *Server {
	t.Helper()
	s := newHealthSentinelTestServer(t)
	s.allowlistEnforcer = nil
	return s
}

// operatorSentinelPayloadFragments are body fragments from the two operator
// sentinels' JSON. No listener that refuses a sentinel may echo any of them.
var operatorSentinelPayloadFragments = []string{
	`"circuit_breaker"`, `"credentials"`, `"route_table"`, `"upstreams"`,
}

// TestHealthSentinelListenerIsolation walks the whole sentinel matrix in one
// place: each name is served by its owning listener with its own body shape
// and the no-store directive, and the other listener answers 404 without
// leaking the serving listener's payload. A routing refactor that registers a
// sentinel on the wrong mux — or a caller-mux dispatch change that starts
// resolving an operator sentinel path — fails here, in both directions.
func TestHealthSentinelListenerIsolation(t *testing.T) {
	s := newSentinelIsolationTestServer(t)

	sentinels := []struct {
		path string
		// own and other are the two listeners composed as production composes
		// them: stage 3 outside the mux. The caller trio's own listener is the
		// bare caller mux because stage 3 steps aside for the probe paths.
		own       http.Handler
		other     http.Handler
		ownBody   string // fragment the owning listener's body must carry
		neverEcho []string
	}{
		{
			path:      "/_seam/health",
			own:       s.callerMux,
			other:     s.identityResolutionMiddleware(s.operatorMux),
			ownBody:   "OK",
			neverEcho: operatorSentinelPayloadFragments,
		},
		{
			path:      "/_seam/healthz",
			own:       s.callerMux,
			other:     s.identityResolutionMiddleware(s.operatorMux),
			ownBody:   "OK",
			neverEcho: operatorSentinelPayloadFragments,
		},
		{
			path:      "/_seam/readyz",
			own:       s.callerMux,
			other:     s.identityResolutionMiddleware(s.operatorMux),
			ownBody:   `"ready":true`,
			neverEcho: operatorSentinelPayloadFragments,
		},
		{
			path: "/health/credentials",
			own:  s.identityResolutionMiddleware(s.operatorMux),
			// A caller reaching the caller listener is resolved exactly as any
			// other caller request, then finds no route: the spec loader
			// reserves /health/* against fragment claims.
			other:     s.identityResolutionMiddleware(s.callerMux),
			ownBody:   `"status":"healthy"`,
			neverEcho: operatorSentinelPayloadFragments,
		},
		{
			path:      "/health/upstreams",
			own:       s.identityResolutionMiddleware(s.operatorMux),
			other:     s.identityResolutionMiddleware(s.callerMux),
			ownBody:   `"upstreams":`,
			neverEcho: operatorSentinelPayloadFragments,
		},
	}

	for _, tt := range sentinels {
		t.Run("served_on_own_listener_"+sanitizePath(tt.path), func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.own.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s on its own listener: status = %d, want 200 (body: %s)", tt.path, rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); !strings.Contains(got, tt.ownBody) {
				t.Errorf("%s body = %q, want it to carry %q", tt.path, got, tt.ownBody)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s Cache-Control = %q, want no-store on every sentinel verdict", tt.path, got)
			}
		})

		t.Run("refused_on_other_listener_"+sanitizePath(tt.path), func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.other.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s on the other listener: status = %d, want 404 (body: %s)", tt.path, rec.Code, rec.Body.String())
			}
			for _, fragment := range tt.neverEcho {
				if strings.Contains(rec.Body.String(), fragment) {
					t.Errorf("%s refused on the other listener but the 404 echoes %q: %s",
						tt.path, fragment, rec.Body.String())
				}
			}
		})
	}
}

// TestHealthUpstreamsOperatorScopeGate completes the /health/upstreams half of
// the operator-scope contract. The allow half is pinned by
// TestOperatorScopeMiddleware_HealthUpstreams against a bare middleware
// instance; this test drives the real registration through the production
// operator chain, so a registration that drops the scope wrapper fails here.
// A resolved caller carrying ordinary proxy scopes but not seam:ops:read is
// 403'd with the required scope named and no sentinel payload echoed; the
// allow half then answers through the same composition, proving the denial is
// the gate and not a broken registration.
func TestHealthUpstreamsOperatorScopeGate(t *testing.T) {
	s := newSentinelIsolationTestServer(t)

	// A fully-resolved caller without the operator scope.
	s.identityResolver.setResolveOverride(func(remoteAddr string) (*Identity, error) {
		return &Identity{
			Resolved:     true,
			NodeName:     "plain-caller",
			NodeKey:      "plain-caller-node-key",
			User:         "caller@example.com",
			Capabilities: []string{"k8s-ro:get"},
		}, nil
	})

	denied := httptest.NewRecorder()
	s.identityResolutionMiddleware(s.operatorMux).ServeHTTP(denied,
		httptest.NewRequest(http.MethodGet, "/health/upstreams", nil))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("unscoped caller status = %d, want 403 (body: %s)", denied.Code, denied.Body.String())
	}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(denied.Body).Decode(&body); err != nil {
		t.Fatalf("decode 403 body %q: %v", denied.Body.String(), err)
	}
	if body.Error != "forbidden" {
		t.Errorf("403 error = %q, want forbidden", body.Error)
	}
	if !strings.Contains(body.Message, "seam:ops:read") {
		t.Errorf("403 message = %q, want it to name the required scope", body.Message)
	}
	for _, fragment := range []string{`"route_table"`, `"last_2xx"`} {
		if strings.Contains(denied.Body.String(), fragment) {
			t.Errorf("403 echoes operator payload %q: %s", fragment, denied.Body.String())
		}
	}

	// The same identity with the scope admitted: the denial above is the scope
	// gate, not an unreachable handler.
	s.identityResolver.setResolveOverride(func(remoteAddr string) (*Identity, error) {
		return &Identity{
			Resolved:     true,
			NodeName:     "operator-caller",
			NodeKey:      "operator-caller-node-key",
			User:         "operator@example.com",
			Capabilities: []string{"k8s-ro:get", "seam:ops:read"},
		}, nil
	})
	allowed := httptest.NewRecorder()
	s.identityResolutionMiddleware(s.operatorMux).ServeHTTP(allowed,
		httptest.NewRequest(http.MethodGet, "/health/upstreams", nil))
	if allowed.Code != http.StatusOK {
		t.Fatalf("scoped caller status = %d, want 200 (body: %s)", allowed.Code, allowed.Body.String())
	}
	if got := allowed.Body.String(); !strings.Contains(got, `"upstreams":`) {
		t.Errorf("scoped caller body = %q, want the sentinel payload", got)
	}
}

// TestReadyzVerdictCarriesNoStore pins the readiness handler's own no-store
// directive on both verdicts it can emit. The other three sentinels carry the
// header for the same reason: the verdict is re-evaluated on every request, so
// a cached answer would be wrong by construction. The 503 leg matters as much
// as the 200 — a probe consumer or intermediary seeing the not-ready verdict
// must not be able to hold it any longer than the ready one.
func TestReadyzVerdictCarriesNoStore(t *testing.T) {
	t.Run("ready_200", func(t *testing.T) {
		s := newSentinelIsolationTestServer(t)

		rec := httptest.NewRecorder()
		s.callerMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_seam/readyz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("readyz status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("readyz 200 Cache-Control = %q, want no-store", got)
		}
	})

	t.Run("not_ready_503", func(t *testing.T) {
		s := newSentinelIsolationTestServer(t)
		if err := s.routeTableHolder.Swap(NewRouteTable(nil)); err != nil {
			t.Fatalf("install quarantined-everything route table: %v", err)
		}

		rec := httptest.NewRecorder()
		s.callerMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_seam/readyz", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("readyz status with an empty route table = %d, want 503 (body: %s)", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("readyz 503 Cache-Control = %q, want no-store", got)
		}
	})
}

// TestReadyzReservedPathBypassWithRealHandler extends the configured-TTL
// bypass pin to readiness with its real handler through the production caller
// chain. With a cache TTL and a per-call cost far above the quota limit set
// for /_seam/readyz itself, every request executes the handler fresh — the
// verdict flips from 200 ready to 503 not-ready between two requests and the
// second answer tracks the flip, so nothing was replayed from a cache entry —
// while the same configuration demonstrably caches a non-reserved path and
// refuses a non-reserved path, so the bypass assertions are not vacuous.
func TestReadyzReservedPathBypassWithRealHandler(t *testing.T) {
	s := newSentinelIsolationTestServer(t)

	s.cacheTTLs["/_seam/readyz"] = 300
	s.quotaTracker.SetQuota("", QuotaConfig{
		Limit:  0.01,
		Window: 1 * time.Hour,
		Scope:  "global",
	})
	s.quotaTracker.SetCostPerCall("/_seam/readyz", 1.0)
	// The sanity twins: an unreserved path under the same quota configuration
	// is refused, and an unreserved path under the same cache configuration is
	// cached and served as a HIT.
	s.quotaTracker.SetCostPerCall("/quota-sanity-nonreserved", 1.0)
	s.cacheTTLs["/cache-sanity-nonreserved"] = 300

	// Production caller-chain order: cache middleware directly over quota
	// middleware over the caller mux.
	chained := s.cacheMiddleware(s.quotaMiddleware(s.callerMux))

	before := s.cache.Stats()

	first := httptest.NewRecorder()
	chained.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/_seam/readyz", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first readyz through the caller chain: status = %d, want 200 (body: %s)", first.Code, first.Body.String())
	}
	assertBypassedReadyzResponse(t, first, "first readyz")

	// Flip the readiness state between requests: a cached verdict would keep
	// answering 200 ready=true.
	if err := s.routeTableHolder.Swap(NewRouteTable(nil)); err != nil {
		t.Fatalf("install quarantined-everything route table: %v", err)
	}
	second := httptest.NewRecorder()
	chained.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/_seam/readyz", nil))
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second readyz after the route table emptied: status = %d, want 503 (a cached response would replay 200; body: %s)", second.Code, second.Body.String())
	}
	assertBypassedReadyzResponse(t, second, "second readyz")
	var checks map[string]bool
	if err := json.NewDecoder(second.Body).Decode(&checks); err != nil {
		t.Fatalf("decode second readyz body %q: %v", second.Body.String(), err)
	}
	if checks["route_table"] || checks["ready"] {
		t.Errorf("second readyz body = %v, want the not-ready verdict tracking the live route table", checks)
	}

	after := s.cache.Stats()
	if after.Size != 0 {
		t.Errorf("cache size = %d after readyz requests, want 0 (readiness verdicts are never stored)", after.Size)
	}
	if after.Hits != before.Hits || after.Misses != before.Misses {
		t.Errorf("cache hit/miss counters moved on readyz requests: before=%+v after=%+v", before, after)
	}
	if got := interactionAccumulated(s, "/_seam/readyz"); got != 0 {
		t.Errorf("reserved path /_seam/readyz: expected $0 accumulated quota, got $%.2f", got)
	}

	// Quota sanity: the identical quota configuration refuses a non-reserved
	// path, so the 200/503s above prove a bypass rather than a quota layer
	// that never engages.
	quotaSanity := httptest.NewRecorder()
	chained.ServeHTTP(quotaSanity, httptest.NewRequest(http.MethodGet, "/quota-sanity-nonreserved", nil))
	if quotaSanity.Code != http.StatusTooManyRequests {
		t.Errorf("quota sanity: unreserved path with cost 1.0 against limit 0.01: status = %d, want %d",
			quotaSanity.Code, http.StatusTooManyRequests)
	}

	// Cache sanity: the identical cache configuration stores and replays a
	// non-reserved path, so the absent cache interaction above is the
	// reserved-path gate and not a broken cache.
	cacheSanityFirst := httptest.NewRecorder()
	chained.ServeHTTP(cacheSanityFirst, httptest.NewRequest(http.MethodGet, "/cache-sanity-nonreserved", nil))
	cacheSanitySecond := httptest.NewRecorder()
	chained.ServeHTTP(cacheSanitySecond, httptest.NewRequest(http.MethodGet, "/cache-sanity-nonreserved", nil))
	if cacheSanitySecond.Header().Get("X-SEAM-Cache") != "HIT" {
		t.Errorf("cache sanity: second non-reserved request X-SEAM-Cache = %q, want HIT (the cache never engaged, so the bypass assertions are vacuous)",
			cacheSanitySecond.Header().Get("X-SEAM-Cache"))
	}
}

// assertBypassedReadyzResponse pins the per-response half of the readiness
// bypass: no cache or quota header of any kind, and the handler's own
// no-store directive.
func assertBypassedReadyzResponse(t *testing.T, rec *httptest.ResponseRecorder, scenario string) {
	t.Helper()
	interactionAssertNoBypassHeaders(t, rec, scenario)
	for _, header := range []string{"X-Quota-Cost-Per-Call", "X-Quota-Remaining", "X-SEAM-Budget-Remaining"} {
		if got := rec.Header().Get(header); got != "" {
			t.Errorf("%s: expected no %s header, got %q", scenario, header, got)
		}
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("%s: Cache-Control = %q, want the handler's own no-store", scenario, got)
	}
}

// sanitizePath turns a request path into a subtest name fragment.
func sanitizePath(path string) string {
	return strings.ReplaceAll(strings.Trim(path, "/"), "/", "_")
}
