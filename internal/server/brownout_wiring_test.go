package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// The tests in this file pin the wiring contract of the caller-facing
// brownout enforcement point (Server.brownoutMiddleware): the runtime
// semantics it must uphold are the ones documented in
// docs/notes/brownout-runtime-semantics.md. The defining constraint of the
// wiring is that the request arrives WITHOUT a route match in its context —
// dispatch publishes the match only inside the caller mux (stage 4, via
// withRouteMatch), after every middleware has run — so the middleware has to
// resolve the route itself.

// newBrownoutWiringServer builds a minimal Server whose route table holds a
// single route, with the brownout scheduler's clock under test control. It
// returns the composed caller-facing handler — the server's brownout
// middleware wrapping a next handler that records that it ran and answers
// 200 — plus a pointer to that record, so a test can assert both what the
// brownout check served and whether the chain continued past it.
func newBrownoutWiringServer(t *testing.T, clock func() time.Time, deprecated *DeprecationInfo) (http.Handler, *bool) {
	t.Helper()

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	bs := NewBrownoutScheduler()
	bs.SetClock(clock)

	server := &Server{
		routeTableHolder: NewThreadSafeTableHolder(&RouteTable{
			routes: []RouteEntry{
				{
					PathTemplate:   "/old-api",
					Method:         "GET",
					APIVersion:     "v1",
					UpstreamTarget: "http://upstream.example.com",
					Deprecated:     deprecated,
				},
			},
		}),
		brownoutScheduler: bs,
	}
	return server.brownoutMiddleware(next), &nextCalled
}

func serveThroughBrownoutMiddleware(handler http.Handler, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/old-api", nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func mustClock(t *testing.T, s string) time.Time {
	t.Helper()
	c, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("test bug: cannot parse clock %q: %v", s, err)
	}
	return c
}

// TestServerBrownoutMiddleware_ResolvesRouteWithoutContextMatch pins the
// defining wiring constraint: a 410 is served for a request that carries no
// route-match context, because the middleware resolves the route itself.
func TestServerBrownoutMiddleware_ResolvesRouteWithoutContextMatch(t *testing.T) {
	handler, nextCalled := newBrownoutWiringServer(t,
		func() time.Time { return mustClock(t, "2024-06-15T10:30:00Z") },
		&DeprecationInfo{
			Since:  "2024-01-01",
			Sunset: "2024-12-31",
			Brownouts: []BrownoutWindow{
				{Start: "2024-06-15T00:00:00Z", End: "2024-06-15T23:59:59Z"},
			},
		},
	)

	w := serveThroughBrownoutMiddleware(handler, nil)

	if *nextCalled {
		t.Error("Expected next handler NOT to be called during an active window")
	}
	if w.Code != http.StatusGone {
		t.Errorf("Expected status 410, got %d", w.Code)
	}
	if w.Header().Get("X-SEAM-Brownout") != "active" {
		t.Error("Expected X-SEAM-Brownout: active header on a window 410")
	}
}

// TestServerBrownoutMiddleware_OutsideWindowProceeds pins the pass-through
// side of the wiring: outside every window the request proceeds and carries
// no brownout marker.
func TestServerBrownoutMiddleware_OutsideWindowProceeds(t *testing.T) {
	handler, nextCalled := newBrownoutWiringServer(t,
		func() time.Time { return mustClock(t, "2024-05-15T10:30:00Z") },
		&DeprecationInfo{
			Since:  "2024-01-01",
			Sunset: "2024-12-31",
			Brownouts: []BrownoutWindow{
				{Start: "2024-06-15T00:00:00Z", End: "2024-06-15T23:59:59Z"},
			},
		},
	)

	w := serveThroughBrownoutMiddleware(handler, nil)

	if !*nextCalled {
		t.Error("Expected next handler to be called outside every window")
	}
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
	if w.Header().Get("X-SEAM-Brownout") != "" {
		t.Error("Expected no X-SEAM-Brownout header outside a window")
	}
}

// TestServerBrownoutMiddleware_BetweenWindowsServesNormally pins the gap
// case of the doc's "between windows the route serves normally": with two
// disjoint windows, an instant after the first has closed and before the
// second opens proceeds to the caller. The gap's closing edge doubles as a
// boundary check — the gap ends exactly where the next window's inclusive
// start begins.
func TestServerBrownoutMiddleware_BetweenWindowsServesNormally(t *testing.T) {
	windows := []BrownoutWindow{
		{Start: "2024-06-15T00:00:00Z", End: "2024-06-15T02:00:00Z"},
		{Start: "2024-06-15T04:00:00Z", End: "2024-06-15T06:00:00Z"},
	}

	tests := []struct {
		name      string
		clockUTC  string
		wantBrown bool
	}{
		{"one second past the first window's end", "2024-06-15T02:00:01Z", false},
		{"middle of the gap between the windows", "2024-06-15T03:00:00Z", false},
		{"one second before the second window's start", "2024-06-15T03:59:59Z", false},
		{"exactly at the second window's start", "2024-06-15T04:00:00Z", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, nextCalled := newBrownoutWiringServer(t,
				func() time.Time { return mustClock(t, tt.clockUTC) },
				&DeprecationInfo{
					Since:     "2024-01-01",
					Sunset:    "2024-12-31",
					Brownouts: windows,
				},
			)

			w := serveThroughBrownoutMiddleware(handler, nil)

			if tt.wantBrown {
				if w.Code != http.StatusGone {
					t.Errorf("clock %s: expected 410 at the next window's start, got %d", tt.clockUTC, w.Code)
				}
				if *nextCalled {
					t.Errorf("clock %s: expected next NOT to be called", tt.clockUTC)
				}
				return
			}
			if !*nextCalled {
				t.Errorf("clock %s: expected next handler to be called in the gap between windows", tt.clockUTC)
			}
			if w.Code != http.StatusOK {
				t.Errorf("clock %s: expected status 200 between windows, got %d", tt.clockUTC, w.Code)
			}
			if w.Header().Get("X-SEAM-Brownout") != "" {
				t.Errorf("clock %s: expected no X-SEAM-Brownout header between windows", tt.clockUTC)
			}
		})
	}
}

// TestServerBrownoutMiddleware_NonUTCOffsetWindowHonoredInUTC pins timezone
// handling: a window written with a +02:00 offset is exactly its UTC
// rendering, compared as absolute instants — the gateway's local timezone
// never participates. [02:00+02:00, 04:00+02:00] == [00:00Z, 02:00Z].
func TestServerBrownoutMiddleware_NonUTCOffsetWindowHonoredInUTC(t *testing.T) {
	tests := []struct {
		name      string
		clockUTC  string
		wantBrown bool
	}{
		{"middle of the window in UTC terms", "2024-06-15T01:30:00Z", true},
		{"end boundary is inclusive across offsets", "2024-06-15T02:00:00Z", true},
		{"one second past the end instant", "2024-06-15T02:00:01Z", false},
		{"before the start instant", "2024-06-14T23:59:59Z", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, nextCalled := newBrownoutWiringServer(t,
				func() time.Time { return mustClock(t, tt.clockUTC) },
				&DeprecationInfo{
					Since:  "2024-01-01",
					Sunset: "2024-12-31",
					Brownouts: []BrownoutWindow{
						{Start: "2024-06-15T02:00:00+02:00", End: "2024-06-15T04:00:00+02:00"},
					},
				},
			)

			w := serveThroughBrownoutMiddleware(handler, nil)

			if tt.wantBrown {
				if w.Code != http.StatusGone {
					t.Errorf("clock %s: expected 410, got %d", tt.clockUTC, w.Code)
				}
				if *nextCalled {
					t.Errorf("clock %s: expected next NOT to be called", tt.clockUTC)
				}
				return
			}
			if w.Code != http.StatusOK {
				t.Errorf("clock %s: expected 200, got %d", tt.clockUTC, w.Code)
			}
		})
	}
}

// TestServerBrownoutMiddleware_ProbeRequestsBypass pins the probe bypass: a
// credential probe must not observe a gateway-side 410 as an upstream
// failure, or a brownout window would mark healthy credentials unhealthy.
func TestServerBrownoutMiddleware_ProbeRequestsBypass(t *testing.T) {
	handler, nextCalled := newBrownoutWiringServer(t,
		func() time.Time { return mustClock(t, "2024-06-15T10:30:00Z") },
		&DeprecationInfo{
			Since:  "2024-01-01",
			Sunset: "2024-12-31",
			Brownouts: []BrownoutWindow{
				{Start: "2024-06-15T00:00:00Z", End: "2024-06-15T23:59:59Z"},
			},
		},
	)

	w := serveThroughBrownoutMiddleware(handler, map[string]string{"X-SEAM-Probe": "true"})

	if !*nextCalled {
		t.Error("Expected probe request to bypass the brownout window")
	}
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 for a probe during a window, got %d", w.Code)
	}
}

// TestServerBrownoutMiddleware_PastSunsetServesNormally pins sunset
// behavior: sunset is advisory and never removes a route by itself. Past the
// last window — and a fortiori past sunset — the route serves normally until
// a human merges the removal PR.
func TestServerBrownoutMiddleware_PastSunsetServesNormally(t *testing.T) {
	handler, nextCalled := newBrownoutWiringServer(t,
		func() time.Time { return mustClock(t, "2025-03-01T00:00:00Z") },
		&DeprecationInfo{
			Since:  "2024-01-01",
			Sunset: "2024-12-31",
			Brownouts: []BrownoutWindow{
				{Start: "2024-06-15T00:00:00Z", End: "2024-06-15T23:59:59Z"},
			},
		},
	)

	w := serveThroughBrownoutMiddleware(handler, nil)

	if !*nextCalled {
		t.Error("Expected next handler to be called past sunset with no active window")
	}
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 past sunset, got %d", w.Code)
	}
}

// TestServerBrownoutMiddleware_UnparseableWindowIsInert pins the fail-safe
// direction for malformed windows at the wiring level: a window that does
// not parse never blocks traffic (lint is the gate that rejects it up
// front).
func TestServerBrownoutMiddleware_UnparseableWindowIsInert(t *testing.T) {
	handler, nextCalled := newBrownoutWiringServer(t,
		func() time.Time { return mustClock(t, "2024-06-15T10:30:00Z") },
		&DeprecationInfo{
			Since:  "2024-01-01",
			Sunset: "2024-12-31",
			Brownouts: []BrownoutWindow{
				{Start: "not-a-timestamp", End: "2024-06-15T23:59:59Z"},
			},
		},
	)

	w := serveThroughBrownoutMiddleware(handler, nil)

	if !*nextCalled {
		t.Error("Expected unparseable window to be inert (next handler called)")
	}
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 with an unparseable window, got %d", w.Code)
	}
}

// TestServerBrownoutMiddleware_410NamesFirstActiveWindow pins which window a
// 410 names when two are active at the same instant (adjacent inclusive
// boundaries): the FIRST window in array order.
func TestServerBrownoutMiddleware_410NamesFirstActiveWindow(t *testing.T) {
	handler, _ := newBrownoutWiringServer(t,
		func() time.Time { return mustClock(t, "2024-06-15T11:00:00Z") },
		&DeprecationInfo{
			Since:  "2024-01-01",
			Sunset: "2024-12-31",
			Brownouts: []BrownoutWindow{
				{Start: "2024-06-15T10:00:00Z", End: "2024-06-15T11:00:00Z"},
				{Start: "2024-06-15T11:00:00Z", End: "2024-06-15T12:00:00Z"},
			},
		},
	)

	w := serveThroughBrownoutMiddleware(handler, nil)

	if w.Code != http.StatusGone {
		t.Fatalf("Expected 410 at the adjacent boundary instant, got %d", w.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("Failed to parse response JSON: %v", err)
	}
	brownout, ok := body["brownout"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected brownout object in body, got %v", body["brownout"])
	}
	if brownout["start"] != "2024-06-15T10:00:00Z" {
		t.Errorf("Expected the FIRST window's start to be named, got %v", brownout["start"])
	}
}

// TestBrownoutCacheInteraction_Window410PrecedesCacheAndQuota pins the
// production caller-chain order the doc claims (brownout outermost of the
// cache/quota/brownout trio, quota innermost): a response cached just before
// a window opens must NOT be served once the window is active, the window
// 410 must consume no quota, and the 410 itself must not be cached so the
// route recovers through to the upstream once the window closes.
func TestBrownoutCacheInteraction_Window410PrecedesCacheAndQuota(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

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
						{Start: "2024-06-15T00:00:00Z", End: "2024-06-15T02:00:00Z"},
					},
				},
			},
		},
	})

	// clock is a variable so the test can move time forward between requests.
	clock := mustClock(t, "2024-06-14T23:59:30Z") // 30 s before the window opens
	s.brownoutScheduler.SetClock(func() time.Time { return clock })

	// Invite caching and charging: the window must preempt them anyway. The
	// 5-minute TTL keeps the pre-window cache entry live for the whole test
	// (the cache expires on the real clock; this test runs in milliseconds),
	// which is what makes step 2 a genuine discriminator: a cache-first order
	// would serve the live entry instead of the 410.
	s.cacheTTLs["/old-api"] = 300
	s.quotaTracker.SetQuota("/old-api", QuotaConfig{
		Limit:  10.0,
		Window: 1 * time.Hour,
		Scope:  "per-route",
	})
	s.quotaTracker.SetCostPerCall("/old-api", 0.10)

	upstreamCalls := 0
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "fresh-%d", upstreamCalls)
	})

	// Production order under test: brownout wraps cache wraps quota.
	handler := s.brownoutMiddleware(s.cacheMiddleware(s.quotaMiddleware(upstream)))

	// 1. Pre-window: served by the upstream, charged, and cached.
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, httptest.NewRequest(http.MethodGet, "/old-api", nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("pre-window request: expected 200, got %d", w1.Code)
	}
	if upstreamCalls != 1 {
		t.Fatalf("pre-window request: expected 1 upstream call, got %d", upstreamCalls)
	}
	spentPreWindow := interactionAccumulated(s, "/old-api")
	if spentPreWindow != 0.10 {
		t.Fatalf("pre-window request: expected 0.10 quota spent, got %v", spentPreWindow)
	}

	// 2. Window open (clock +90 s; the cache entry is 90 s into its 300 s
	// TTL, so a cache-first order would serve the stale 200 here).
	clock = mustClock(t, "2024-06-15T00:01:00Z")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/old-api", nil))
	if w2.Code != http.StatusGone {
		t.Fatalf("in-window request: expected the window 410 to preempt the still-live cached response, got %d", w2.Code)
	}
	if w2.Header().Get("X-SEAM-Brownout") != "active" {
		t.Error("in-window request: expected X-SEAM-Brownout: active")
	}
	if upstreamCalls != 1 {
		t.Errorf("in-window request: expected no upstream call, got %d total", upstreamCalls)
	}
	if got := interactionAccumulated(s, "/old-api"); got != spentPreWindow {
		t.Errorf("in-window request: window 410 must not consume quota, accumulated %v -> %v", spentPreWindow, got)
	}

	// The 410 must not have been cached: the only entry under the request's
	// cache key is still the pre-window 200. (The cache expires on the real
	// clock, so this check is wall-clock-independent at test timescales.)
	cacheKey := GenerateCacheKey("GET", "/old-api", url.Values{})
	entry, found := s.cache.Get(cacheKey)
	if !found {
		t.Fatal("in-window request: expected the pre-window cache entry to still exist")
	}
	if entry.StatusCode != http.StatusOK || string(entry.Body) != "fresh-1" {
		t.Errorf("in-window request: cache holds (%d, %q), expected the untouched pre-window 200 — a 410 must never be cached", entry.StatusCode, entry.Body)
	}

	// 3. Window closed: with the (naturally expiring) pre-window entry gone,
	// the route recovers through to the upstream.
	s.cache.Delete(cacheKey)
	clock = mustClock(t, "2024-06-15T02:01:00Z")
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, httptest.NewRequest(http.MethodGet, "/old-api", nil))
	if w3.Code != http.StatusOK {
		t.Fatalf("post-window request: expected 200, got %d", w3.Code)
	}
	if body := w3.Body.String(); body != "fresh-2" {
		t.Errorf("post-window request: expected a fresh upstream response, got %q", body)
	}
}
