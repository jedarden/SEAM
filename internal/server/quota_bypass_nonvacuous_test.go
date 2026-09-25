package server

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestQuotaBypass_CacheHitBypassNonVacuous pins the cache-hit quota bypass of
// docs/design/cache-health-sentinel-integration.md ("Cache Hit Bypass") with a
// configuration that can actually fail.
//
// The route under test carries a real per-call cost ($0.10) against a bounded
// per-route budget ($1.00). Three guards keep the bypass assertions from being
// vacuous:
//
//   - The charged miss proves quota ENGAGES on this route: the accumulation
//     and the admission-time headers appear on misses. On a cost-0 or
//     unconfigured route the tracker's cost==0 branch takes the cache-hit
//     path either way, and every assertion below would pass for nothing.
//   - Distinct query strings are distinct cache keys under the same quota
//     route key, so a run of forced misses drives that same budget to
//     exhaustion and the next miss is refused with 429 — proving the route
//     can be quota-refused at all.
//   - The exhausted budget must not touch a hit: the cached response still
//     replays 200 with X-SEAM-Cache: HIT and X-Quota-Bypassed: cache-hit
//     while every fresh miss on the route is refused above it. If the bypass
//     regressed and hits reached the quota middleware, this request would be
//     refused exactly like the misses.
func TestQuotaBypass_CacheHitBypassNonVacuous(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	const (
		route = "/api/bypass-pin"
		limit = 1.00
		cost  = 0.10
	)
	s.quotaTracker.SetQuota(route, QuotaConfig{
		Limit:  limit,
		Window: 1 * time.Hour,
		Scope:  "per-route",
	})
	s.quotaTracker.SetCostPerCall(route, cost)
	s.cacheTTLs[route] = 300

	upstreamCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "upstream-%d", upstreamCalls)
	})

	// Production signal-producing order: metrics OUTSIDE cache OUTSIDE quota
	// (server.go wires exactly this, minus the signal-free layers).
	chained := observabilityChain(s, handler)

	// (a) First request: a charged miss. Quota engages and consumes.
	req := httptest.NewRequest(http.MethodGet, route, nil)
	w := httptest.NewRecorder()
	chained.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("miss: expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("X-Quota-Cost-Per-Call"); got == "" {
		t.Error("miss: expected X-Quota-Cost-Per-Call (the configured cost must engage quota)")
	}
	if got := w.Header().Get("X-Quota-Bypassed"); got != "" {
		t.Errorf("miss: expected no X-Quota-Bypassed header, got %q", got)
	}
	if got := interactionAccumulated(s, route); math.Abs(got-cost) > 1e-9 {
		t.Errorf("miss: expected $%.2f consumed, got $%.2f", cost, got)
	}

	// (b)+(d) Second request: a hit. No charge, bypass headers, no dispatch.
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
	if upstreamCalls != 1 {
		t.Errorf("hit: expected the cached response (1 upstream call total), got %d", upstreamCalls)
	}
	if got := interactionAccumulated(s, route); math.Abs(got-cost) > 1e-9 {
		t.Errorf("hit: expected the charge frozen at $%.2f, got $%.2f", cost, got)
	}

	// (c) Sanity: this route can be quota-refused. Forced misses (distinct
	// cache keys, same quota route key) consume the rest of the budget; the
	// next miss is refused 429 and left uncharged.
	for i := 1; i <= 9; i++ {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("%s?q=%d", route, i), nil)
		w := httptest.NewRecorder()
		chained.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("forced miss #%d: expected 200, got %d", i, w.Code)
		}
	}
	if got := interactionAccumulated(s, route); math.Abs(got-limit) > 1e-6 {
		t.Errorf("after 10 charged misses: expected the $%.2f budget consumed, got $%.2f", limit, got)
	}
	if upstreamCalls != 10 {
		t.Errorf("forced misses: expected 10 upstream dispatches (charged miss + 9 forced), got %d", upstreamCalls)
	}

	req = httptest.NewRequest(http.MethodGet, route+"?q=refused", nil)
	w = httptest.NewRecorder()
	chained.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("exhausted route: expected 429 quota refusal, got %d", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got == "" {
		t.Error("refusal: expected Retry-After on the quota refusal")
	}
	if got := w.Header().Get("X-Quota-Bypassed"); got != "" {
		t.Errorf("refusal: a refusal is not a bypass, got X-Quota-Bypassed: %q", got)
	}
	if got := interactionAccumulated(s, route); math.Abs(got-limit) > 1e-6 {
		t.Errorf("refusal: a refused request must not be charged, got $%.2f", got)
	}

	// The exhausted budget still cannot touch a hit: the cached base response
	// replays with the full bypass signal while fresh misses are refused.
	req = httptest.NewRequest(http.MethodGet, route, nil)
	w = httptest.NewRecorder()
	chained.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("post-exhaustion hit: expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("X-SEAM-Cache"); got != "HIT" {
		t.Errorf("post-exhaustion hit: expected X-SEAM-Cache: HIT, got %q", got)
	}
	if got := w.Header().Get("X-Quota-Bypassed"); got != "cache-hit" {
		t.Errorf("post-exhaustion hit: expected X-Quota-Bypassed: cache-hit, got %q", got)
	}
	if upstreamCalls != 10 {
		t.Errorf("post-exhaustion hit: expected no new upstream dispatch (10 total), got %d", upstreamCalls)
	}
	if got := interactionAccumulated(s, route); math.Abs(got-limit) > 1e-6 {
		t.Errorf("post-exhaustion hit: expected the charge frozen at $%.2f, got $%.2f", limit, got)
	}

	// (d) Bypass, cost and refusal metrics on the quota tracker's route label.
	body := observabilityScrape(t, s)
	if got := interactionMetricValue(body, "seam_quota_bypassed_total", route); math.IsNaN(got) || got != 2 {
		t.Errorf("seam_quota_bypassed_total{route=%q}: expected 2 (initial + post-exhaustion hit), got %v", route, got)
	}
	if got := interactionMetricValue(body, "seam_quota_exceeded_total", route); math.IsNaN(got) || got != 1 {
		t.Errorf("seam_quota_exceeded_total{route=%q}: expected 1 (the refused miss), got %v", route, got)
	}
	if got := interactionMetricValue(body, "seam_quota_cost_total", route); math.IsNaN(got) || math.Abs(got-limit) > 1e-6 {
		t.Errorf("seam_quota_cost_total{route=%q}: expected $%.2f (hits bypassed the meter), got %v", route, limit, got)
	}
}
