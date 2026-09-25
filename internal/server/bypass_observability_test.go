package server

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file pins the bypass observability contract specified in
// docs/notes/cache-quota-bypass-observability.md: which metrics, headers and
// quota state each request class produces. Unlike the interaction tests in
// cache_quota_health_interaction_test.go, the chain here is the production
// order for the three signal-producing layers — metricsMiddleware OUTSIDE
// cacheMiddleware OUTSIDE quotaMiddleware (server.go wires exactly this order,
// minus the layers that carry no signals) — so the assertions cover the
// seam_http_* series and the metric-route context labels that the production
// chain attaches before the cache middleware runs.
//
// Pinned per request class:
//
//   - Control-plane, health-sentinel and reserved-prefix requests: no
//     seam_http_*, seam_cache_* or seam_quota_* samples at all, no bypass or
//     quota headers, fresh upstream execution, $0 accumulated — even with TTL,
//     quota and per-call cost configured against the reserved paths
//     themselves. A quota-refused sanity path proves the configuration bites.
//   - A successful cache hit on caller traffic: X-SEAM-Cache: HIT,
//     X-Quota-Bypassed: cache-hit, every admission-time quota header stripped
//     (including X-SEAM-Budget-Remaining), seam_cache_hits_total and
//     seam_quota_bypassed_total incremented while the charged cost stays
//     frozen, and the request still counted in seam_http_requests_total.

// observabilityChain wires the production signal-producing middleware order:
// metrics outermost, then cache, then quota.
func observabilityChain(s *Server, upstream http.HandlerFunc) http.Handler {
	return s.metricsMiddleware(s.cacheMiddleware(s.quotaMiddleware(upstream)))
}

// observabilityScrape fetches the Prometheus exposition from the operator mux.
func observabilityScrape(t *testing.T, s *Server) string {
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

// observabilitySampleLines returns every exposition sample line whose metric
// name (with or without labels) equals family.
func observabilitySampleLines(body, family string) []string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, family+"{") || strings.HasPrefix(line, family+" ") {
			lines = append(lines, line)
		}
	}
	return lines
}

// observabilityFamilyAbsent asserts the exposition carries no sample for the
// metric family at all.
func observabilityFamilyAbsent(t *testing.T, body, family string) {
	t.Helper()
	if lines := observabilitySampleLines(body, family); len(lines) > 0 {
		t.Errorf("expected no %s samples, got %q", family, lines)
	}
}

// observabilitySampleValue returns the numeric value of the single sample
// line with the given full exposition prefix (metric name plus the exact
// label set). Fails if zero or several samples match.
func observabilitySampleValue(t *testing.T, body, linePrefix string) float64 {
	t.Helper()
	var value float64
	matches := 0
	for _, line := range observabilitySampleLines(body, strings.SplitN(linePrefix, "{", 2)[0]) {
		if !strings.HasPrefix(line, linePrefix) {
			continue
		}
		matches++
		valuePart := line[strings.LastIndex(line, " ")+1:]
		parsed, err := strconv.ParseFloat(valuePart, 64)
		if err != nil {
			t.Fatalf("sample %q does not end in a number", line)
		}
		value = parsed
	}
	if matches != 1 {
		t.Fatalf("expected exactly one sample with prefix %q, found %d in:\n%s", linePrefix, matches, body)
	}
	return value
}

// TestBypassObservability_ReservedRequestsEmitNoSignals pins the observability
// half of the reserved-path contract: control-plane, health-sentinel and
// reserved-prefix requests produce no signal in any layer — no seam_http_*
// series (metricsMiddleware skips them), no seam_cache_* or seam_quota_*
// series (cache and quota skip them), no bypass or quota headers, always-fresh
// execution and $0 accumulated quota — even when TTL, quota and per-call cost
// are configured against the reserved paths themselves.
func TestBypassObservability_ReservedRequestsEmitNoSignals(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Every reserved family: control-plane exact matches, the /_seam/ health
	// names, and the reserved prefixes beyond their exact members.
	reserved := []string{
		"/docs",
		"/openapi.json",
		"/health/credentials",
		"/health/upstreams",
		"/_seam/health",
		"/_seam/healthz",
		"/_seam/readyz",
		"/health/deep/nested",
		"/config/anything",
		"/approvals/pending",
	}

	upstreamCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "fresh-%d", upstreamCalls)
	})

	// Configure every signal-producing layer FOR each reserved path. If any
	// layer engaged on this traffic, the assertions below would fail: the
	// second request would be a cache HIT, the quota charge would accumulate,
	// and samples would appear.
	for _, path := range reserved {
		s.cacheTTLs[path] = 300
		s.quotaTracker.SetQuota(path, QuotaConfig{
			Limit:  0.10,
			Window: 1 * time.Hour,
			Scope:  "per-route",
		})
		s.quotaTracker.SetCostPerCall(path, 0.50)
	}

	// Sanity path that the same configuration DOES refuse: cost 0.50 against a
	// 0.10 limit is refused at admission. Without this proof, "reserved paths
	// answer 200" would be indistinguishable from "quota never engages".
	sanity := "/api/obs-sanity"
	s.quotaTracker.SetQuota(sanity, QuotaConfig{
		Limit:  0.10,
		Window: 1 * time.Hour,
		Scope:  "per-route",
	})
	s.quotaTracker.SetCostPerCall(sanity, 0.50)

	chained := observabilityChain(s, handler)

	for _, path := range reserved {
		for i := 1; i <= 2; i++ {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			chained.ServeHTTP(w, req)

			scenario := fmt.Sprintf("%s request #%d", path, i)
			if w.Code != http.StatusOK {
				t.Errorf("%s: expected 200 (reserved traffic is never refused), got %d", scenario, w.Code)
			}
			if got := w.Body.String(); got != fmt.Sprintf("fresh-%d", upstreamCalls) {
				t.Errorf("%s: expected a fresh upstream body, got %q", scenario, got)
			}
			interactionAssertNoBypassHeaders(t, w, scenario)
			for _, header := range []string{
				"X-Quota-Cost-Per-Call",
				"X-Quota-Remaining",
				"X-SEAM-Budget-Remaining",
			} {
				if got := w.Header().Get(header); got != "" {
					t.Errorf("%s: expected no %s header on reserved-path traffic, got %q", scenario, header, got)
				}
			}
		}
	}

	if upstreamCalls != 2*len(reserved) {
		t.Errorf("expected %d upstream calls (always fresh), got %d", 2*len(reserved), upstreamCalls)
	}
	for _, path := range reserved {
		if got := interactionAccumulated(s, path); got != 0 {
			t.Errorf("reserved path %s: expected $0 accumulated quota, got $%.2f", path, got)
		}
	}

	// The sanity path IS refused by the same configuration: quota enforcement
	// bites, so the reserved 200s above are a real bypass. Refusal is 429 via
	// the ErrCodeQuotaExceeded envelope mapping.
	req := httptest.NewRequest(http.MethodGet, sanity, nil)
	w := httptest.NewRecorder()
	chained.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("sanity path: expected 429 quota refusal under the shared config, got %d", w.Code)
	}
	if got := interactionAccumulated(s, sanity); got != 0 {
		t.Errorf("sanity path: a refused request must not be charged, got $%.2f", got)
	}

	body := observabilityScrape(t, s)

	// Reserved paths contribute nothing to any caller-request series:
	// metricsMiddleware short-circuits them before any label context or
	// counter child is created. The only non-reserved request (the refused
	// sanity call) contributes exactly one sample to each family it passes
	// through — assert the reserved paths contributed none of it.
	for _, family := range []string{
		"seam_http_requests_total",
		"seam_http_request_duration_seconds_bucket",
		"seam_http_request_duration_seconds_sum",
		"seam_http_request_duration_seconds_count",
		"seam_http_requests_in_flight",
		"seam_route_version_requests_total",
		"seam_cache_hits_total",
		"seam_cache_misses_total",
	} {
		for _, line := range observabilitySampleLines(body, family) {
			for _, path := range reserved {
				if strings.Contains(line, `route="`+path+`"`) {
					t.Errorf("reserved path %s leaked into %s: %q", path, family, line)
				}
			}
		}
	}

	// The refused sanity request was still counted by metricsMiddleware (it
	// runs outside quota) at its own status, and its cache lookup recorded a
	// miss; no hit ever happened.
	observabilityFamilyAbsent(t, body, "seam_cache_hits_total")
	if got := observabilitySampleValue(t, body, `seam_cache_misses_total{route="unmatched",version="unknown"}`); got != 1 {
		t.Errorf("seam_cache_misses_total: expected only the sanity-path miss, got %v", got)
	}
	if got := observabilitySampleValue(t, body, `seam_http_requests_total{method="GET",route="unmatched",status="429",version="unknown"}`); got != 1 {
		t.Errorf("sanity refusal in seam_http_requests_total: expected 1, got %v", got)
	}
	interactionMetricValueEquals(t, body, "seam_quota_exceeded_total", sanity, 1)
	for _, path := range reserved {
		for _, family := range []string{
			"seam_quota_cost_total",
			"seam_quota_bypassed_total",
			"seam_quota_exceeded_total",
		} {
			interactionMetricAbsent(t, body, family, path)
		}
	}
}

// TestBypassObservability_CacheHitSignalContract pins the successful
// cache-hit observability contract end to end: the hit response carries
// X-SEAM-Cache: HIT and X-Quota-Bypassed: cache-hit with every admission-time
// quota header stripped (X-Quota-Cost-Per-Call, X-Quota-Remaining and
// X-SEAM-Budget-Remaining, which the charged miss stored into the cached
// entry), the bypass shows up in seam_cache_hits_total and
// seam_quota_bypassed_total while seam_quota_cost_total stays frozen at the
// single miss charge, and the request is still counted by seam_http_requests_total.
//
// It also pins the label-key split: cache series are labelled from the
// metric-route context ("unmatched" for a path no fragment defines, with
// version "unknown"), while quota series are labelled with the concrete
// request path the quota tracker is keyed on.
func TestBypassObservability_CacheHitSignalContract(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	route := "/api/obs/hit"
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
		_, _ = fmt.Fprintf(w, `{"call":%d}`, upstreamCalls)
	})

	chained := observabilityChain(s, handler)

	// Charged miss: quota headers present, including the budget snapshot that
	// must NOT survive onto later hits. A miss carries no X-SEAM-Cache header
	// — HIT is the only value the middleware ever emits.
	req := httptest.NewRequest(http.MethodGet, route, nil)
	w := httptest.NewRecorder()
	chained.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("miss: expected 200, got %d", w.Code)
	}
	// formatCost trims trailing zeros, so $0.10 renders as "$0.1".
	if got := w.Header().Get("X-Quota-Cost-Per-Call"); got != "$0.1" {
		t.Errorf("miss: expected X-Quota-Cost-Per-Call: $0.1, got %q", got)
	}
	if got := w.Header().Get("X-Quota-Remaining"); got != "$0.9" {
		t.Errorf("miss: expected X-Quota-Remaining: $0.9, got %q", got)
	}
	if got := w.Header().Get("X-SEAM-Budget-Remaining"); !strings.HasPrefix(got, "amount=$0.9 unit=call window=") {
		t.Errorf("miss: expected X-SEAM-Budget-Remaining to open with the $0.9 budget, got %q", got)
	}
	if got := w.Header().Get("X-SEAM-Cache"); got != "" {
		t.Errorf("miss: expected no X-SEAM-Cache header (only HIT is ever emitted), got %q", got)
	}
	missBody := w.Body.String()

	// Successful cache hit: bypass headers set, every admission-time quota
	// header stripped, cached body replayed.
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
	for _, header := range []string{"X-Quota-Cost-Per-Call", "X-Quota-Remaining", "X-SEAM-Budget-Remaining"} {
		if got := w.Header().Get(header); got != "" {
			t.Errorf("hit: expected %s stripped from the cached response, got %q", header, got)
		}
	}
	if got := w.Body.String(); got != missBody {
		t.Errorf("hit: expected cached body %q, got %q", missBody, got)
	}

	if upstreamCalls != 1 {
		t.Errorf("expected 1 upstream call (hit must not dispatch), got %d", upstreamCalls)
	}
	if got := interactionAccumulated(s, route); math.Abs(got-0.10) > 1e-9 {
		t.Errorf("expected $0.10 accumulated (hit bypassed deduction), got $%.2f", got)
	}

	body := observabilityScrape(t, s)

	// Cache series are labelled from the metric-route context: this server
	// loaded no fragment matching the path, so both samples read
	// route="unmatched", version="unknown".
	observabilitySampleValue(t, body, `seam_cache_hits_total{route="unmatched",version="unknown"}`)
	if got := observabilitySampleValue(t, body, `seam_cache_hits_total{route="unmatched",version="unknown"}`); got != 1 {
		t.Errorf("seam_cache_hits_total: expected 1 hit, got %v", got)
	}
	if got := observabilitySampleValue(t, body, `seam_cache_misses_total{route="unmatched",version="unknown"}`); got != 1 {
		t.Errorf("seam_cache_misses_total: expected 1 miss, got %v", got)
	}

	// Quota series are labelled with the concrete request path the quota
	// tracker is keyed on — the same string every quota API accepts.
	if got := interactionMetricValue(body, "seam_quota_bypassed_total", route); math.IsNaN(got) || got != 1 {
		t.Errorf("seam_quota_bypassed_total{route=%q}: expected 1, got %v", route, got)
	}
	if got := interactionMetricValue(body, "seam_quota_cost_total", route); math.IsNaN(got) || math.Abs(got-0.10) > 1e-9 {
		t.Errorf("seam_quota_cost_total{route=%q}: expected 0.10 (frozen at the miss), got %v", route, got)
	}

	// The hit is still a caller request: counted once per request by the
	// outermost metrics middleware, status 200 both times, in flight back to
	// zero once both completed.
	if got := observabilitySampleValue(t, body, `seam_http_requests_total{method="GET",route="unmatched",status="200",version="unknown"}`); got != 2 {
		t.Errorf("seam_http_requests_total: expected 2 (miss + hit), got %v", got)
	}
	if got := observabilitySampleValue(t, body, `seam_http_requests_in_flight{method="GET",route="unmatched",version="unknown"}`); got != 0 {
		t.Errorf("seam_http_requests_in_flight: expected 0 after completion, got %v", got)
	}
	if lines := observabilitySampleLines(body, "seam_http_request_duration_seconds_count"); len(lines) == 0 {
		t.Error("expected seam_http_request_duration_seconds_count samples for the two caller requests")
	}
}
