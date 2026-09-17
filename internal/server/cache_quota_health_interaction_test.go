package server

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This file pins the documented interactions between the response cache, the
// quota middleware and the health-sentinel reserved-path gate. The behaviour
// under test is specified in docs/design/cache-health-sentinel-integration.md:
//
//   - Reserved paths (health sentinel / control plane) bypass cache AND quota,
//     and record no cache or quota metrics for that traffic.
//   - A route with TTL=0 gets single-flight deduplication only: concurrent
//     identical requests coalesce into one upstream call, but nothing is
//     cached, so sequential requests each reach upstream and each pay quota.
//   - A successful cache hit bypasses quota: the caller sees
//     X-SEAM-Cache: HIT and X-Quota-Bypassed: cache-hit, the quota cost
//     headers captured on the miss are stripped, and the bypass events are
//     counted in seam_cache_hits_total / seam_quota_bypassed_total.
//   - Non-GET requests bypass the cache but are still quota-charged, and their
//     responses never poison the GET cache entry.

// interactionChain wires the mock upstream behind the production middleware
// order (cache outer, quota inner) used by server.go.
func interactionChain(s *Server, upstream http.HandlerFunc) http.Handler {
	return s.cacheMiddleware(s.quotaMiddleware(upstream))
}

// interactionScrapeMetrics fetches the Prometheus exposition from the
// operator mux and returns it as a string.
func interactionScrapeMetrics(t *testing.T, s *Server) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	w := httptest.NewRecorder()
	s.operatorMux.ServeHTTP(w, req)
	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read metrics body: %v", err)
	}
	return string(body)
}

// interactionMetricValue parses the numeric value of the metric sample for
// family+route (e.g. seam_cache_hits_total{route="/api/x",...} 3). It returns
// math.NaN() when no sample exists for that family+route pair.
func interactionMetricValue(body, family, route string) float64 {
	prefix := family + `{route="` + route + `"`
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		valuePart := line[strings.LastIndex(line, " ")+1:]
		value, err := strconv.ParseFloat(valuePart, 64)
		if err != nil {
			return math.NaN()
		}
		return value
	}
	return math.NaN()
}

// interactionMetricValueEquals asserts the metric sample exists and carries
// exactly the wanted value.
func interactionMetricValueEquals(t *testing.T, body, family, route string, want float64) {
	t.Helper()
	got := interactionMetricValue(body, family, route)
	if math.IsNaN(got) {
		t.Errorf("expected metric %s{route=%q} to exist with value %v, but no sample was present", family, route, want)
		return
	}
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("metric %s{route=%q}: expected %v, got %v", family, route, want, got)
	}
}

// interactionMetricAbsent asserts no sample exists for family+route.
func interactionMetricAbsent(t *testing.T, body, family, route string) {
	t.Helper()
	if got := interactionMetricValue(body, family, route); !math.IsNaN(got) {
		t.Errorf("expected no %s{route=%q} sample, got value %v", family, route, got)
	}
}

// interactionAccumulated returns the quota accumulated for a route, treating a
// missing tracker entry as 0.
func interactionAccumulated(s *Server, route string) float64 {
	status := s.quotaTracker.GetQuotaStatus()
	if routeStatus, ok := status["route:"+route].(map[string]interface{}); ok {
		if accumulated, ok := routeStatus["accumulated"].(float64); ok {
			return accumulated
		}
	}
	return 0
}

// interactionAssertNoBypassHeaders asserts a response carries none of the
// bypass marker headers documented for bypass events. Quota cost headers are
// deliberately NOT checked here: a charged cache miss legitimately carries
// them.
func interactionAssertNoBypassHeaders(t *testing.T, w *httptest.ResponseRecorder, scenario string) {
	t.Helper()
	for _, header := range []string{"X-SEAM-Cache", "X-Quota-Bypassed"} {
		if got := w.Header().Get(header); got != "" {
			t.Errorf("%s: expected no %s bypass header, got %q", scenario, header, got)
		}
	}
}

// TestReservedPathInteraction_BypassesCacheQuotaAndMetrics verifies the
// documented reserved-path behaviour through the full production chain: with
// cache TTLs, quotas and per-call costs deliberately configured FOR the
// reserved paths themselves, probe traffic must still always execute fresh,
// never consume quota, never produce bypass headers, and never record cache or
// quota metrics.
func TestReservedPathInteraction_BypassesCacheQuotaAndMetrics(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Reserved paths from every documented family: /_seam/ exact+prefix,
	// /health/ prefix and exact sentinel endpoints, /config/ prefix.
	reserved := []string{
		"/_seam/health",
		"/_seam/healthz",
		"/_seam/readyz",
		"/health/credentials",
		"/health/upstreams",
		"/health/deep/nested",
		"/config/status",
	}

	upstreamCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "fresh-%d", upstreamCalls)
	})

	// Configure cache TTL, quota and per-call cost FOR each reserved path.
	// The bypass must hold even when the configuration invites caching and
	// charging.
	for _, path := range reserved {
		s.cacheTTLs[path] = 300
		s.quotaTracker.SetQuota(path, QuotaConfig{
			Limit:  1.0,
			Window: 1 * time.Hour,
			Scope:  "per-route",
		})
		s.quotaTracker.SetCostPerCall(path, 0.10)
	}

	chained := interactionChain(s, handler)

	for _, path := range reserved {
		for i := 1; i <= 2; i++ {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			chained.ServeHTTP(w, req)

			scenario := fmt.Sprintf("%s request #%d", path, i)
			if w.Code != http.StatusOK {
				t.Errorf("%s: expected 200 (reserved paths must never be refused), got %d", scenario, w.Code)
			}
			// Always fresh: the upstream handler ran for this request, so the
			// body carries the current call counter.
			if got := w.Body.String(); got != fmt.Sprintf("fresh-%d", upstreamCalls) {
				t.Errorf("%s: expected a fresh upstream body, got %q", scenario, got)
			}
			interactionAssertNoBypassHeaders(t, w, scenario)
			// Documented reserved-path contract: "Response headers: No
			// X-Quota-* headers".
			for _, header := range []string{"X-Quota-Cost-Per-Call", "X-Quota-Remaining", "X-SEAM-Budget-Remaining"} {
				if got := w.Header().Get(header); got != "" {
					t.Errorf("%s: expected no %s header on reserved-path traffic, got %q", scenario, header, got)
				}
			}
		}
	}

	// Every request reached upstream: nothing was cached for reserved paths.
	if upstreamCalls != 2*len(reserved) {
		t.Errorf("expected %d upstream calls (always fresh), got %d", 2*len(reserved), upstreamCalls)
	}

	// No quota was consumed for any reserved route.
	for _, path := range reserved {
		if got := interactionAccumulated(s, path); got != 0 {
			t.Errorf("reserved path %s: expected $0 accumulated quota, got $%.2f", path, got)
		}
	}

	// The documented "No metrics recorded" row: no cache or quota samples for
	// any reserved route.
	body := interactionScrapeMetrics(t, s)
	for _, path := range reserved {
		for _, family := range []string{
			"seam_cache_hits_total",
			"seam_cache_misses_total",
			"seam_quota_cost_total",
			"seam_quota_bypassed_total",
			"seam_quota_exceeded_total",
		} {
			interactionMetricAbsent(t, body, family, path)
		}
	}
}

// TestTTLZeroInteraction_DedupOnlyNoCaching verifies the documented TTL=0
// decision-tree branch: single-flight coalescing is available, but nothing is
// cached, so sequential requests each execute upstream and each pay quota. A
// TTL>0 route configured in the same server is contrasted to show the
// difference is the route TTL, not global behaviour.
func TestTTLZeroInteraction_DedupOnlyNoCaching(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	upstreamCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "call-%d", upstreamCalls)
	})

	for _, route := range []string{"/api/ttl0", "/api/ttl300"} {
		s.quotaTracker.SetQuota(route, QuotaConfig{
			Limit:  10.0,
			Window: 1 * time.Hour,
			Scope:  "per-route",
		})
		s.quotaTracker.SetCostPerCall(route, 0.10)
	}
	s.cacheTTLs["/api/ttl0"] = 0     // explicit TTL=0: dedup only, no caching
	s.cacheTTLs["/api/ttl300"] = 300 // normal caching for contrast

	chained := interactionChain(s, handler)

	// Three sequential requests on the TTL=0 route: all three reach upstream
	// and all three are charged.
	for i := 1; i <= 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/ttl0", nil)
		w := httptest.NewRecorder()
		chained.ServeHTTP(w, req)

		scenario := fmt.Sprintf("ttl0 request #%d", i)
		if w.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", scenario, w.Code)
		}
		if got := w.Body.String(); got != fmt.Sprintf("call-%d", i) {
			t.Errorf("%s: expected fresh upstream body %q, got %q (TTL=0 must not cache)", scenario, fmt.Sprintf("call-%d", i), got)
		}
		interactionAssertNoBypassHeaders(t, w, scenario)
		if w.Header().Get("X-Quota-Cost-Per-Call") == "" {
			t.Errorf("%s: expected quota cost header (charged at admission)", scenario)
		}
	}

	if got := interactionAccumulated(s, "/api/ttl0"); math.Abs(got-0.30) > 1e-9 {
		t.Errorf("ttl0 route: expected $0.30 accumulated (3 charged misses), got $%.2f", got)
	}

	// Contrast: the TTL>0 route caches, so its second request neither reaches
	// upstream nor pays.
	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/ttl300", nil)
		w := httptest.NewRecorder()
		chained.ServeHTTP(w, req)
		if i == 2 && w.Header().Get("X-SEAM-Cache") != "HIT" {
			t.Error("ttl300 request #2: expected cache HIT")
		}
	}
	if upstreamCalls != 4 { // 3 ttl0 + 1 ttl300 (second ttl300 served from cache)
		t.Errorf("expected 4 upstream calls total, got %d", upstreamCalls)
	}
	if got := interactionAccumulated(s, "/api/ttl300"); math.Abs(got-0.10) > 1e-9 {
		t.Errorf("ttl300 route: expected $0.10 accumulated (second call bypassed quota), got $%.2f", got)
	}

	// Metrics agree: misses were recorded per request on ttl0, and hits only
	// on ttl300.
	body := interactionScrapeMetrics(t, s)
	interactionMetricValueEquals(t, body, "seam_cache_misses_total", "/api/ttl0", 3)
	interactionMetricAbsent(t, body, "seam_cache_hits_total", "/api/ttl0")
	interactionMetricValueEquals(t, body, "seam_cache_hits_total", "/api/ttl300", 1)
	interactionMetricValueEquals(t, body, "seam_quota_bypassed_total", "/api/ttl300", 1)
	interactionMetricValueEquals(t, body, "seam_quota_cost_total", "/api/ttl0", 0.30)
}

// TestTTLZeroInteraction_ConcurrentRequestsCoalesce verifies that with TTL=0
// concurrent identical requests share a single upstream execution and a single
// quota charge, while a later sequential request proves nothing was cached.
func TestTTLZeroInteraction_ConcurrentRequestsCoalesce(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	route := "/api/ttl0-concurrent"
	s.quotaTracker.SetQuota(route, QuotaConfig{
		Limit:  10.0,
		Window: 1 * time.Hour,
		Scope:  "per-route",
	})
	s.quotaTracker.SetCostPerCall(route, 0.10)
	s.cacheTTLs[route] = 0

	const waiters = 24

	var upstreamCalls int32
	entered := make(chan struct{})
	var enteredOnce sync.Once
	release := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The single-flight leader is the first to reach the upstream handler;
		// the post-coalescing sequential request is the second, so the enter
		// signal is once-only. Only the leader parks: it holds the
		// single-flight window open until the test has counted the waiters.
		n := atomic.AddInt32(&upstreamCalls, 1)
		enteredOnce.Do(func() { close(entered) })
		if n == 1 {
			<-release
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("coalesced"))
	})

	chained := interactionChain(s, handler)

	// Leader request: parks inside the upstream handler, holding the
	// single-flight slot for the cache key.
	leaderDone := make(chan error, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		w := httptest.NewRecorder()
		chained.ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Body.String() != "coalesced" {
			leaderDone <- fmt.Errorf("leader: got status %d body %q", w.Code, w.Body.String())
			return
		}
		leaderDone <- nil
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("leader request never reached the upstream handler")
	}

	// Waiters: all must dedupe onto the leader's in-flight request.
	type waiterResult struct {
		code int
		body string
	}
	waiterResults := make([]waiterResult, waiters)
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, route, nil)
			w := httptest.NewRecorder()
			chained.ServeHTTP(w, req)
			waiterResults[i] = waiterResult{code: w.Code, body: w.Body.String()}
		}(i)
	}

	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&s.singleFlight.dedupedCalls) < waiters && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&s.singleFlight.dedupedCalls); got < waiters {
		t.Fatalf("expected %d deduped waiters, counted %d", waiters, got)
	}

	close(release)
	wg.Wait()
	if err := <-leaderDone; err != nil {
		t.Fatal(err)
	}

	for i, res := range waiterResults {
		if res.code != http.StatusOK || res.body != "coalesced" {
			t.Errorf("waiter %d: expected 200 %q, got %d %q", i, "coalesced", res.code, res.body)
		}
	}
	if got := atomic.LoadInt32(&upstreamCalls); got != 1 {
		t.Errorf("expected 1 upstream call for %d concurrent requests, got %d", waiters+1, got)
	}
	// Exactly one quota charge: waiters never run the quota middleware.
	if got := interactionAccumulated(s, route); math.Abs(got-0.10) > 1e-9 {
		t.Errorf("expected $0.10 accumulated (single charge for the coalesced group), got $%.2f", got)
	}
	if got := atomic.LoadInt64(&s.singleFlight.totalCalls); got != 1 {
		t.Errorf("expected 1 single-flight execution, got %d", got)
	}

	// TTL=0 stored nothing: a later sequential request executes upstream again
	// and pays again.
	req := httptest.NewRequest(http.MethodGet, route, nil)
	w := httptest.NewRecorder()
	chained.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("post-coalescing request: expected 200, got %d", w.Code)
	}
	if w.Header().Get("X-SEAM-Cache") == "HIT" {
		t.Error("post-coalescing request: TTL=0 must not cache, so no HIT is possible")
	}
	if got := atomic.LoadInt32(&upstreamCalls); got != 2 {
		t.Errorf("post-coalescing request: expected 2 upstream calls total, got %d", got)
	}
	if got := interactionAccumulated(s, route); math.Abs(got-0.20) > 1e-9 {
		t.Errorf("post-coalescing request: expected $0.20 accumulated (second charge), got $%.2f", got)
	}
}

// TestCacheHitBypassInteraction_MetricsAndHeaders verifies the documented
// observability contract of a successful cache-hit quota bypass: the response
// carries X-SEAM-Cache: HIT plus X-Quota-Bypassed: cache-hit, the quota cost
// headers captured when the response was cached are stripped, and the bypass
// events show up in seam_cache_hits_total and seam_quota_bypassed_total while
// the charged cost and the miss counter stay frozen.
func TestCacheHitBypassInteraction_MetricsAndHeaders(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	route := "/api/bypass"
	s.quotaTracker.SetQuota(route, QuotaConfig{
		Limit:  1.0,
		Window: 1 * time.Hour,
		Scope:  "per-route",
	})
	s.quotaTracker.SetCostPerCall(route, 0.10)
	s.cacheTTLs[route] = 300

	upstreamCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	chained := interactionChain(s, handler)

	// Cache miss: charged, with the documented quota cost headers.
	req := httptest.NewRequest(http.MethodGet, route, nil)
	w := httptest.NewRecorder()
	chained.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("miss: expected 200, got %d", w.Code)
	}
	if w.Header().Get("X-Quota-Cost-Per-Call") == "" || w.Header().Get("X-Quota-Remaining") == "" {
		t.Fatal("miss: expected X-Quota-Cost-Per-Call and X-Quota-Remaining headers")
	}

	// Cache hit: bypass headers present, cost headers stripped even though the
	// cached response was stored WITH them.
	req = httptest.NewRequest(http.MethodGet, route, nil)
	w = httptest.NewRecorder()
	chained.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("hit: expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("X-SEAM-Cache"); got != "HIT" {
		t.Errorf("hit: expected X-SEAM-Cache: HIT, got %q", got)
	}
	if got := w.Header().Get("X-Quota-Bypassed"); got != "cache-hit" {
		t.Errorf("hit: expected X-Quota-Bypassed: cache-hit, got %q", got)
	}
	for _, header := range []string{"X-Quota-Cost-Per-Call", "X-Quota-Remaining"} {
		if got := w.Header().Get(header); got != "" {
			t.Errorf("hit: expected %s stripped from the cached response, got %q", header, got)
		}
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("hit: expected cached Content-Type to be replayed, got %q", got)
	}

	// Three more hits: every one bypasses, nothing new is charged.
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		w := httptest.NewRecorder()
		chained.ServeHTTP(w, req)
		if w.Header().Get("X-Quota-Bypassed") != "cache-hit" {
			t.Errorf("hit #%d: expected X-Quota-Bypassed: cache-hit", i+2)
		}
	}

	if upstreamCalls != 1 {
		t.Errorf("expected 1 upstream call total (1 miss, 4 hits), got %d", upstreamCalls)
	}
	if got := interactionAccumulated(s, route); math.Abs(got-0.10) > 1e-9 {
		t.Errorf("expected $0.10 accumulated (hits bypass deduction), got $%.2f", got)
	}

	// The documented metric pairs for bypass events.
	body := interactionScrapeMetrics(t, s)
	interactionMetricValueEquals(t, body, "seam_cache_hits_total", route, 4)
	interactionMetricValueEquals(t, body, "seam_quota_bypassed_total", route, 4)
	interactionMetricValueEquals(t, body, "seam_cache_misses_total", route, 1)
	interactionMetricValueEquals(t, body, "seam_quota_cost_total", route, 0.10)
	interactionMetricAbsent(t, body, "seam_quota_exceeded_total", route)
}

// TestNonGetInteraction_ChargedButNotCached verifies that non-GET requests
// bypass the response cache but still pay quota at admission, and that their
// responses never replace the GET cache entry.
func TestNonGetInteraction_ChargedButNotCached(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	route := "/api/getonly"
	s.quotaTracker.SetQuota(route, QuotaConfig{
		Limit:  10.0,
		Window: 1 * time.Hour,
		Scope:  "per-route",
	})
	s.quotaTracker.SetCostPerCall(route, 0.10)
	s.cacheTTLs[route] = 300

	upstreamCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "call-%d", upstreamCalls)
	})

	chained := interactionChain(s, handler)

	// GET: cache miss, charged, cached.
	req := httptest.NewRequest(http.MethodGet, route, nil)
	w := httptest.NewRecorder()
	chained.ServeHTTP(w, req)
	if got := w.Body.String(); got != "call-1" {
		t.Fatalf("first GET: expected fresh body %q, got %q", "call-1", got)
	}

	// POST: bypasses the cache (executes upstream) but is still charged.
	req = httptest.NewRequest(http.MethodPost, route, nil)
	w = httptest.NewRecorder()
	chained.ServeHTTP(w, req)
	if got := w.Body.String(); got != "call-2" {
		t.Errorf("POST: expected fresh upstream body %q, got %q", "call-2", got)
	}
	interactionAssertNoBypassHeaders(t, w, "POST")
	if w.Header().Get("X-Quota-Cost-Per-Call") == "" {
		t.Error("POST: expected quota cost header (charged despite cache bypass)")
	}

	// GET again: served from the GET cache entry — the POST response must not
	// have poisoned it, and no new charge applies.
	req = httptest.NewRequest(http.MethodGet, route, nil)
	w = httptest.NewRecorder()
	chained.ServeHTTP(w, req)
	if got := w.Body.String(); got != "call-1" {
		t.Errorf("second GET: expected cached GET body %q, got %q (POST must not enter the cache)", "call-1", got)
	}
	if w.Header().Get("X-SEAM-Cache") != "HIT" {
		t.Error("second GET: expected X-SEAM-Cache: HIT")
	}
	if upstreamCalls != 2 {
		t.Errorf("expected 2 upstream calls (GET miss + POST bypass), got %d", upstreamCalls)
	}
	if got := interactionAccumulated(s, route); math.Abs(got-0.20) > 1e-9 {
		t.Errorf("expected $0.20 accumulated (GET miss + POST), got $%.2f", got)
	}

	body := interactionScrapeMetrics(t, s)
	// The POST never consults the cache, so only the GET records a miss; the
	// second GET records the hit; both real upstream executions charged.
	interactionMetricValueEquals(t, body, "seam_cache_misses_total", route, 1)
	interactionMetricValueEquals(t, body, "seam_cache_hits_total", route, 1)
	interactionMetricValueEquals(t, body, "seam_quota_cost_total", route, 0.20)
}
