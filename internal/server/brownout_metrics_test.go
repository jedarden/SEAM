package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file pins the metrics half of the brownout contract specified in
// docs/notes/brownout-runtime-semantics.md ("Metrics"): a window 410 is
// ordinary counted traffic at the metrics layer, and metering-silent below
// it. The chain here is the production order of the four signal-producing
// layers — metricsMiddleware OUTSIDE brownoutMiddleware OUTSIDE
// cacheMiddleware OUTSIDE quotaMiddleware (server.go wires exactly this
// order) — with TTL and per-call cost configured against the route, so the
// in-window absences are proven against a configuration that demonstrably
// produces those samples out-of-window.
//
// Pinned:
//
//   - A window 410 is counted in seam_http_requests_total at the deprecated
//     route's own labels with status="410", and in the duration histogram —
//     same counters as the 200, no special-casing.
//   - It increments seam_route_version_requests_total: a caller that appears
//     inside a window is real dependency and must keep the retirement quiet
//     window shut.
//   - It produces no cache hit/miss sample and no quota charge, bypass or
//     refusal event — the same short-circuit that makes the 410 consume no
//     budget — and carries no quota headers.
//   - The out-of-window control request through the same chain serves 200,
//     records the cache miss and the quota charge: the in-window absences
//     are caused by the window 410, not by the fixture.
func TestBrownoutWindow410Metrics(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// The clock is mutable so one fixture serves both phases: inside the
	// window (410) and before it (control 200).
	now := mustClock(t, "2024-06-15T10:30:00Z")
	bs := NewBrownoutScheduler()
	bs.SetClock(func() time.Time { return now })
	s.brownoutScheduler = bs

	s.routeTableHolder = NewThreadSafeTableHolder(&RouteTable{
		routes: []RouteEntry{
			{
				PathTemplate:   "/old-api",
				Method:         "GET",
				APIVersion:     "v1",
				UpstreamTarget: "http://upstream.example.com",
				Deprecated: &DeprecationInfo{
					Since:  "2024-01-01",
					Sunset: "2024-12-31",
					Brownouts: []BrownoutWindow{
						{Start: "2024-06-15T00:00:00Z", End: "2024-06-15T23:59:59Z"},
					},
				},
			},
		},
	})

	// Configure the inner layers so their in-window silence is meaningful:
	// with TTL and per-call cost set, the out-of-window control request
	// below produces a cache miss and a charge.
	s.cacheTTLs["/old-api"] = 300
	s.quotaTracker.SetQuota("/old-api", QuotaConfig{
		Limit:  10.00,
		Window: 1 * time.Hour,
		Scope:  "per-route",
	})
	s.quotaTracker.SetCostPerCall("/old-api", 0.50)

	upstreamCalls := 0
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "fresh")
	})

	chained := s.metricsMiddleware(s.brownoutMiddleware(s.cacheMiddleware(s.quotaMiddleware(upstream))))

	// Phase 1 — inside the window: 410, no quota headers, upstream untouched.
	inWindow := httptest.NewRequest(http.MethodGet, "/old-api", nil)
	inWindowW := httptest.NewRecorder()
	chained.ServeHTTP(inWindowW, inWindow)

	if inWindowW.Code != http.StatusGone {
		t.Fatalf("in-window request: expected 410, got %d", inWindowW.Code)
	}
	if got := inWindowW.Header().Get("X-SEAM-Brownout"); got != "active" {
		t.Errorf("in-window request: expected X-SEAM-Brownout: active, got %q", got)
	}
	for _, header := range []string{
		"X-Quota-Cost-Per-Call",
		"X-Quota-Remaining",
		"X-SEAM-Budget-Remaining",
		"X-Quota-Bypassed",
	} {
		if got := inWindowW.Header().Get(header); got != "" {
			t.Errorf("in-window 410: expected no %s header (served before quota), got %q", header, got)
		}
	}

	// Phase 2 — before the window opens: the same request through the same
	// chain serves 200 from the upstream.
	now = mustClock(t, "2024-06-14T10:30:00Z")
	outWindow := httptest.NewRequest(http.MethodGet, "/old-api", nil)
	outWindowW := httptest.NewRecorder()
	chained.ServeHTTP(outWindowW, outWindow)

	if outWindowW.Code != http.StatusOK {
		t.Fatalf("out-of-window request: expected 200, got %d", outWindowW.Code)
	}
	if got := outWindowW.Body.String(); got != "fresh" {
		t.Errorf("out-of-window request: expected upstream body, got %q", got)
	}
	if upstreamCalls != 1 {
		t.Errorf("expected exactly 1 upstream call (the 410 never proxied), got %d", upstreamCalls)
	}

	body := observabilityScrape(t, s)

	// Both requests were counted by the same counter at their own statuses,
	// under the deprecated route's own labels.
	if got := observabilitySampleValue(t, body, `seam_http_requests_total{method="GET",route="/old-api",status="410",version="v1"}`); got != 1 {
		t.Errorf("seam_http_requests_total status=410: expected 1, got %v", got)
	}
	if got := observabilitySampleValue(t, body, `seam_http_requests_total{method="GET",route="/old-api",status="200",version="v1"}`); got != 1 {
		t.Errorf("seam_http_requests_total status=200: expected 1, got %v", got)
	}
	if got := observabilitySampleValue(t, body, `seam_http_request_duration_seconds_count{method="GET",route="/old-api",version="v1"}`); got != 2 {
		t.Errorf("seam_http_request_duration_seconds_count: expected 2 (the 410 is timed too), got %v", got)
	}

	// The retirement counter saw BOTH callers: in-window traffic is real
	// dependency and must hold the quiet window shut. The spec_version label
	// is whatever the spec ring buffer's current version is, so the sample is
	// matched on the route label alone.
	if got := brownoutRouteVersionSample(t, body, "/old-api"); got != 2 {
		t.Errorf("seam_route_version_requests_total: expected 2 (the 410'd caller counts), got %v", got)
	}

	// Cache and quota saw only the out-of-window request.
	observabilityFamilyAbsent(t, body, "seam_cache_hits_total")
	if got := observabilitySampleValue(t, body, `seam_cache_misses_total{route="/old-api",version="v1"}`); got != 1 {
		t.Errorf("seam_cache_misses_total: expected only the out-of-window miss, got %v", got)
	}
	if got := observabilitySampleValue(t, body, `seam_quota_cost_total{route="/old-api"}`); got != 0.50 {
		t.Errorf("seam_quota_cost_total: expected 0.5 (only the out-of-window charge), got %v", got)
	}
	observabilityFamilyAbsent(t, body, "seam_quota_bypassed_total")
	observabilityFamilyAbsent(t, body, "seam_quota_exceeded_total")
	if got := interactionAccumulated(s, "/old-api"); got != 0.50 {
		t.Errorf("accumulated quota: expected $0.50 (the 410 charged nothing), got $%.2f", got)
	}
}

// brownoutRouteVersionSample returns the value of the single
// seam_route_version_requests_total sample carrying the given route label,
// whatever its spec_version label is (the ring buffer's current version is
// environment-dependent and deliberately not pinned here).
func brownoutRouteVersionSample(t *testing.T, body, route string) float64 {
	t.Helper()
	var value float64
	matches := 0
	for _, line := range observabilitySampleLines(body, "seam_route_version_requests_total") {
		if !containsRouteLabel(line, route) {
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
		t.Fatalf("expected exactly one seam_route_version_requests_total sample for route %q, found %d in:\n%s", route, matches, body)
	}
	return value
}

// containsRouteLabel reports whether an exposition sample line carries the
// given route label value.
func containsRouteLabel(line, route string) bool {
	return strings.Contains(line, fmt.Sprintf(`route="%s"`, route))
}
