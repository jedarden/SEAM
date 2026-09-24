package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The served-alias contract these tests pin is documented in
// docs/design/cache-health-sentinel-integration.md ("Health Sentinel
// Endpoints"): /_seam/health is registered on the same handler as
// /_seam/healthz; neither health name is a reservedPaths exact entry — both
// ride the already-reserved /_seam/ prefix; both receive the reserved-path
// treatment (cache and quota middleware bypass); and a reload that
// quarantines every fragment takes the pod out of the Service via
// /_seam/readyz while both liveness names keep answering. Before these tests
// no named test pinned any of that, so a reserved-path or routing refactor
// could silently drop the alias or change its liveness semantics.

// newHealthAliasTestServer builds a server whose mux carries the production
// route registrations, with the allowlist readiness dependency neutralised
// the same way openbao_readiness_test.go does so the readiness assertions
// here isolate the route_table dependency this suite manipulates.
func newHealthAliasTestServer(t *testing.T) *Server {
	t.Helper()
	s := New(&Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	})
	s.allowlistEnforcer = nil
	return s
}

// TestSeamHealthAliasServesSameBodyAsHealthz pins the alias registration:
// /_seam/health is served by the same handler as /_seam/healthz, so both
// names answer 200 "OK" on the caller-facing mux and refuse non-GET
// identically. A routing refactor that drops the alias registration or moves
// it to a different handler fails here rather than only in production probe
// traffic.
func TestSeamHealthAliasServesSameBodyAsHealthz(t *testing.T) {
	s := newHealthAliasTestServer(t)

	serve := func(path, method string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		s.callerMux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}

	healthz := serve("/_seam/healthz", http.MethodGet)
	if healthz.Code != http.StatusOK {
		t.Fatalf("/_seam/healthz status = %d, want %d", healthz.Code, http.StatusOK)
	}
	if got := healthz.Body.String(); got != "OK" {
		t.Fatalf("/_seam/healthz body = %q, want %q", got, "OK")
	}

	alias := serve("/_seam/health", http.MethodGet)
	if alias.Code != http.StatusOK {
		t.Fatalf("/_seam/health status = %d, want %d", alias.Code, http.StatusOK)
	}
	if got := alias.Body.String(); got != healthz.Body.String() {
		t.Fatalf("/_seam/health body = %q, want the /_seam/healthz body %q", got, healthz.Body.String())
	}

	// Same handler means the same method semantics, not merely the same
	// happy path: a second registration on a different handler that answered
	// POST with 200 would satisfy a body-only check.
	if got := serve("/_seam/healthz", http.MethodPost).Code; got != http.StatusMethodNotAllowed {
		t.Errorf("/_seam/healthz POST status = %d, want %d", got, http.StatusMethodNotAllowed)
	}
	if got := serve("/_seam/health", http.MethodPost).Code; got != http.StatusMethodNotAllowed {
		t.Errorf("/_seam/health POST status = %d, want %d", got, http.StatusMethodNotAllowed)
	}
}

// TestSeamHealthAliasReceivesReservedPathTreatment pins the reserved-path
// treatment the doc promises for both health names: even with a cache TTL
// and a per-call cost configured for each path, requests through the
// production caller-chain order (cache middleware over quota middleware over
// the caller mux) bypass the cache and quota enforcement entirely — 200
// "OK", no cache or quota headers, and no cache interaction. It also pins
// the documented mechanism: neither health name is a reservedPaths exact
// entry, and the /_seam/ prefix reservation is what covers them.
func TestSeamHealthAliasReceivesReservedPathTreatment(t *testing.T) {
	s := newHealthAliasTestServer(t)

	// The grandfathered exact enumeration never contained either health
	// name, and the doc records that absence as deliberate. An exact entry
	// would still pass every behavioral assertion below, so assert the
	// documented mechanism directly: absent from the map, reserved via the
	// prefix walk.
	for _, path := range []string{"/_seam/health", "/_seam/healthz"} {
		if reservedPaths.exact[path] {
			t.Errorf("%s is a reservedPaths exact entry; the doc records both health names as deliberately absent (they ride the reserved /_seam/ prefix)", path)
		}
		if !isReservedPath(path) {
			t.Errorf("isReservedPath(%q) = false, want true via the reserved /_seam/ prefix", path)
		}
	}

	// Configure each health name so the bypass is observable: a TTL means an
	// unreserved GET would be cached, and a cost far above the quota limit
	// means an unreserved GET would be refused.
	s.cacheTTLs["/_seam/health"] = 300
	s.cacheTTLs["/_seam/healthz"] = 300
	s.quotaTracker.SetQuota("", QuotaConfig{
		Limit:  0.01,
		Window: 1 * time.Hour,
		Scope:  "global",
	})
	s.quotaTracker.SetCostPerCall("/_seam/health", 1.0)
	s.quotaTracker.SetCostPerCall("/_seam/healthz", 1.0)
	s.quotaTracker.SetCostPerCall("/quota-sanity-nonreserved", 1.0)

	// The same quota configuration does refuse a non-reserved path, so the
	// 200s below prove a bypass rather than a quota layer that never bites.
	quotaOnly := s.quotaMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	sanity := httptest.NewRecorder()
	quotaOnly.ServeHTTP(sanity, httptest.NewRequest(http.MethodGet, "/quota-sanity-nonreserved", nil))
	if sanity.Code != http.StatusTooManyRequests {
		t.Fatalf("quota sanity: unreserved path with cost 1.0 against limit 0.01: status = %d, want %d (the bypass assertions are vacuous otherwise)", sanity.Code, http.StatusTooManyRequests)
	}

	// Production order from Start: quota middleware innermost, cache
	// middleware directly over it.
	handler := s.cacheMiddleware(s.quotaMiddleware(s.callerMux))
	before := s.cache.Stats()
	for _, path := range []string{"/_seam/health", "/_seam/healthz", "/_seam/health"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s through the caller chain: status = %d, want %d (reserved paths bypass cache and quota)", path, rec.Code, http.StatusOK)
			continue
		}
		if got := rec.Body.String(); got != "OK" {
			t.Errorf("%s through the caller chain: body = %q, want %q", path, got, "OK")
		}
		if got := rec.Header().Get("X-SEAM-Cache"); got != "" {
			t.Errorf("%s: X-SEAM-Cache = %q, want no cache header on a reserved path", path, got)
		}
		if got := rec.Header().Get("X-Quota-Cost-Per-Call"); got != "" {
			t.Errorf("%s: X-Quota-Cost-Per-Call = %q, want no quota header on a reserved path", path, got)
		}
	}

	after := s.cache.Stats()
	if after.Size != 0 {
		t.Errorf("cache size = %d after reserved-path requests, want 0 (health responses are never stored)", after.Size)
	}
	if after.Hits != before.Hits || after.Misses != before.Misses {
		t.Errorf("cache hit/miss counters moved on reserved-path requests: before=%+v after=%+v", before, after)
	}
}

// TestHealthzAndAliasAnswerWhileReadyzIs503 pins the liveness/readiness
// separation the doc promises: a reload that quarantines every fragment
// installs a route table with no routes, /_seam/readyz then answers 503 and
// names the unmet route_table dependency (the pod leaves the Service), while
// /_seam/healthz and /_seam/health keep answering 200 "OK" so the pod does
// not also leave the restart path.
func TestHealthzAndAliasAnswerWhileReadyzIs503(t *testing.T) {
	s := newHealthAliasTestServer(t)

	if err := s.routeTableHolder.Swap(NewRouteTable(nil)); err != nil {
		t.Fatalf("install quarantined-everything route table: %v", err)
	}

	ready := httptest.NewRecorder()
	s.callerMux.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/_seam/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("/_seam/readyz with an empty route table: status = %d, want %d", ready.Code, http.StatusServiceUnavailable)
	}
	var checks map[string]bool
	if err := json.NewDecoder(ready.Body).Decode(&checks); err != nil {
		t.Fatalf("decode readyz body: %v", err)
	}
	if checks["route_table"] {
		t.Error("/_seam/readyz reports route_table satisfied with an empty route table")
	}
	if checks["ready"] {
		t.Error("/_seam/readyz reports ready with an empty route table")
	}

	for _, path := range []string{"/_seam/health", "/_seam/healthz"} {
		rec := httptest.NewRecorder()
		s.callerMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s while readyz is 503: status = %d, want %d (liveness stays independent of readiness)", path, rec.Code, http.StatusOK)
			continue
		}
		if got := rec.Body.String(); got != "OK" {
			t.Errorf("%s while readyz is 503: body = %q, want %q", path, got, "OK")
		}
	}
}
