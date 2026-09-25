package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The tests in this file pin the response-header side of the caller-facing
// deprecated-route enforcement point (Server.brownoutMiddleware): outside
// brownout windows every response for an x-seam-deprecated route carries the
// Deprecation/Sunset and Link header set, and an active-window 410 carries
// exactly the 410 handler's own headers — never the pass-through set layered
// on top. The runtime contract these tests pin is documented in
// docs/notes/brownout-runtime-semantics.md ("Deprecation and Sunset response
// headers").
//
// Like the brownout wiring tests, every test here drives the middleware with
// a request that carries NO route match in its context — dispatch publishes
// the match only inside the caller mux (stage 4, via withRouteMatch), after
// every middleware has run — so header emission must ride on the
// self-resolving lookup, exactly as window enforcement does.

// newDeprecationWiringServer builds a minimal Server whose route table holds
// a single route /old-api with the given deprecation metadata, and returns
// the server (for clock control), the composed caller-facing handler
// (brownout middleware wrapping a next handler that answers 200 and records
// that it ran), and a pointer to that record.
func newDeprecationWiringServer(t *testing.T, clock func() time.Time, deprecated *DeprecationInfo) (*Server, http.Handler, *bool) {
	t.Helper()

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

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
	}
	if clock != nil {
		server.brownoutScheduler = NewBrownoutScheduler()
		server.brownoutScheduler.SetClock(clock)
	}
	return server, server.brownoutMiddleware(next), &nextCalled
}

func serveThroughDeprecationMiddleware(handler http.Handler, path string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func window(start, end string) BrownoutWindow {
	return BrownoutWindow{Start: start, End: end}
}

// TestServerDeprecationMiddleware_EmitsHeadersWithoutContextMatch pins the
// defining wiring constraint plus the full pass-through header set: a
// request that carries no route-match context still gets the Deprecation,
// Sunset and Link headers, because the middleware resolves the route itself.
func TestServerDeprecationMiddleware_EmitsHeadersWithoutContextMatch(t *testing.T) {
	_, handler, nextCalled := newDeprecationWiringServer(t, nil, &DeprecationInfo{
		Since:              "2024-01-01",
		Sunset:             "2024-12-31",
		ReplacementPath:    "/new-api",
		ReplacementVersion: "v2",
	})

	w := serveThroughDeprecationMiddleware(handler, "/old-api", nil)

	if !*nextCalled {
		t.Error("Expected next handler to be called outside windows")
	}
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
	if dep := w.Header().Get("Deprecation"); dep != "since=2024-01-01" {
		t.Errorf("Expected Deprecation: since=2024-01-01, got %q", dep)
	}
	if sun := w.Header().Get("Sunset"); sun != "2024-12-31" {
		t.Errorf("Expected Sunset: 2024-12-31, got %q", sun)
	}

	links := w.Header()["Link"]
	var sawDocsLink, sawChangesLink, sawAlternateLink bool
	for _, l := range links {
		if containsString(l, "/docs/route") && containsString(l, `rel="deprecation"`) {
			sawDocsLink = true
		}
		if containsString(l, "/changes") && containsString(l, `rel="deprecation"`) {
			sawChangesLink = true
		}
		if containsString(l, "/new-api?version=v2") && containsString(l, `rel="alternate"`) {
			sawAlternateLink = true
		}
	}
	if !sawDocsLink {
		t.Errorf("Expected a Link rel=deprecation to /docs/route among %v", links)
	}
	if !sawChangesLink {
		t.Errorf("Expected a Link rel=deprecation to /changes among %v", links)
	}
	if !sawAlternateLink {
		t.Errorf("Expected a Link rel=alternate to /new-api?version=v2 among %v", links)
	}
}

// TestServerDeprecationMiddleware_SunsetOmittedWhenUnset pins sunset header
// handling: a fragment that declares only since emits Deprecation and no
// Sunset header, and no alternate Link (nothing is declared as replacement).
func TestServerDeprecationMiddleware_SunsetOmittedWhenUnset(t *testing.T) {
	_, handler, _ := newDeprecationWiringServer(t, nil, &DeprecationInfo{
		Since: "2024-01-01",
	})

	w := serveThroughDeprecationMiddleware(handler, "/old-api", nil)

	if dep := w.Header().Get("Deprecation"); dep != "since=2024-01-01" {
		t.Errorf("Expected Deprecation: since=2024-01-01, got %q", dep)
	}
	if sun := w.Header().Get("Sunset"); sun != "" {
		t.Errorf("Expected no Sunset header without a sunset date, got %q", sun)
	}
	for _, l := range w.Header()["Link"] {
		if containsString(l, `rel="alternate"`) {
			t.Errorf("Expected no alternate Link without a replacement, got %q", l)
		}
	}
}

// TestServerDeprecationMiddleware_DormantOnNonDeprecatedRoute pins the
// dormant side: a route with no x-seam-deprecated metadata serves with no
// deprecation headers at all.
func TestServerDeprecationMiddleware_DormantOnNonDeprecatedRoute(t *testing.T) {
	_, handler, nextCalled := newDeprecationWiringServer(t, nil, nil)

	w := serveThroughDeprecationMiddleware(handler, "/old-api", nil)

	if !*nextCalled {
		t.Error("Expected next handler to be called for a non-deprecated route")
	}
	if w.Header().Get("Deprecation") != "" || w.Header().Get("Sunset") != "" {
		t.Errorf("Expected no deprecation headers on a non-deprecated route, got Deprecation=%q Sunset=%q",
			w.Header().Get("Deprecation"), w.Header().Get("Sunset"))
	}
}

// TestServerDeprecationMiddleware_NoRouteMatchIsDormant pins pass-through for
// a path that matches no route in the table.
func TestServerDeprecationMiddleware_NoRouteMatchIsDormant(t *testing.T) {
	_, handler, nextCalled := newDeprecationWiringServer(t, nil, &DeprecationInfo{
		Since: "2024-01-01",
	})

	w := serveThroughDeprecationMiddleware(handler, "/never-matched", nil)

	if !*nextCalled {
		t.Error("Expected next handler to be called for an unmatched path")
	}
	if w.Header().Get("Deprecation") != "" {
		t.Errorf("Expected no Deprecation header without a route match, got %q", w.Header().Get("Deprecation"))
	}
}

// TestServerDeprecationMiddleware_ReservedPathBypass pins the reserved-path
// bypass with a genuine discriminator: the route table DOES hold a deprecated
// route whose template is the reserved path /docs/route, so without the
// bypass the self-resolving lookup would match it and stamp headers onto the
// docs response.
func TestServerDeprecationMiddleware_ReservedPathBypass(t *testing.T) {
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})
	server := &Server{
		routeTableHolder: NewThreadSafeTableHolder(&RouteTable{
			routes: []RouteEntry{
				{
					PathTemplate:   "/docs/route",
					Method:         "GET",
					APIVersion:     "v1",
					UpstreamTarget: "http://upstream.example.com",
					Deprecated:     &DeprecationInfo{Since: "2024-01-01"},
				},
			},
		}),
	}

	w := serveThroughDeprecationMiddleware(server.brownoutMiddleware(next), "/docs/route", nil)

	if !nextCalled {
		t.Error("Expected next handler to be called for a reserved path")
	}
	if w.Header().Get("Deprecation") != "" {
		t.Errorf("Expected the reserved-path bypass to precede header emission, got Deprecation=%q",
			w.Header().Get("Deprecation"))
	}
}

// TestServerDeprecationMiddleware_ProbeRequestsBypass pins the probe bypass
// for headers: a credential probe must not observe gateway-side deprecation
// metadata (defense-in-depth — probes target upstreams directly and never
// traverse the caller chain).
func TestServerDeprecationMiddleware_ProbeRequestsBypass(t *testing.T) {
	_, handler, nextCalled := newDeprecationWiringServer(t, nil, &DeprecationInfo{
		Since: "2024-01-01",
	})

	w := serveThroughDeprecationMiddleware(handler, "/old-api", map[string]string{"X-SEAM-Probe": "true"})

	if !*nextCalled {
		t.Error("Expected probe request to pass through")
	}
	if w.Header().Get("Deprecation") != "" {
		t.Errorf("Expected no Deprecation header on a probe, got %q", w.Header().Get("Deprecation"))
	}
}

// TestServerDeprecationMiddleware_OperationLevelDeprecatedAdvertised pins the
// deprecated-operation path: a route deprecated by OpenAPI `deprecated: true`
// alone (no x-seam-deprecated fragment root) has no since date, which
// extractDeprecation records as "unknown" — the header is still emitted, with
// that value verbatim.
func TestServerDeprecationMiddleware_OperationLevelDeprecatedAdvertised(t *testing.T) {
	_, handler, nextCalled := newDeprecationWiringServer(t, nil, &DeprecationInfo{
		Since: "unknown",
	})

	w := serveThroughDeprecationMiddleware(handler, "/old-api", nil)

	if !*nextCalled {
		t.Error("Expected next handler to be called")
	}
	if dep := w.Header().Get("Deprecation"); dep != "since=unknown" {
		t.Errorf("Expected Deprecation: since=unknown for an operation-level deprecation, got %q", dep)
	}
	if sun := w.Header().Get("Sunset"); sun != "" {
		t.Errorf("Expected no Sunset header, got %q", sun)
	}
}

// TestServerDeprecationMiddleware_ActiveWindow410NotDoubleHeadered pins the
// window/pass-through interaction: inside an active window the 410 is served
// before the pass-through header set runs, so the response carries exactly
// the 410 handler's own headers — two Link entries (changes + replacement),
// no /docs/route link, and nothing stamped twice.
func TestServerDeprecationMiddleware_ActiveWindow410NotDoubleHeadered(t *testing.T) {
	_, handler, nextCalled := newDeprecationWiringServer(t,
		func() time.Time { return mustClock(t, "2024-06-15T10:30:00Z") },
		&DeprecationInfo{
			Since:              "2024-01-01",
			Sunset:             "2024-12-31",
			ReplacementPath:    "/new-api",
			ReplacementVersion: "v2",
			Brownouts: []BrownoutWindow{
				window("2024-06-15T00:00:00Z", "2024-06-15T23:59:59Z"),
			},
		},
	)

	w := serveThroughDeprecationMiddleware(handler, "/old-api", nil)

	if *nextCalled {
		t.Error("Expected next handler NOT to be called during an active window")
	}
	if w.Code != http.StatusGone {
		t.Fatalf("Expected status 410, got %d", w.Code)
	}
	if w.Header().Get("X-SEAM-Brownout") != "active" {
		t.Error("Expected X-SEAM-Brownout: active")
	}
	links := w.Header()["Link"]
	if len(links) != 2 {
		t.Errorf("Expected exactly 2 Link headers on the 410 (changes + replacement), got %d: %v", len(links), links)
	}
	for _, l := range links {
		if containsString(l, "/docs/route") {
			t.Errorf("Expected no /docs/route link on a window 410 (pass-through set must not layer on), got %q", l)
		}
	}
}

// TestServerDeprecationMiddleware_InactiveWindowCarriesHeaders pins the
// inactive-brownout side: in the gap between windows the route serves
// normally and the deprecation header set is present.
func TestServerDeprecationMiddleware_InactiveWindowCarriesHeaders(t *testing.T) {
	_, handler, nextCalled := newDeprecationWiringServer(t,
		func() time.Time { return mustClock(t, "2024-06-15T03:00:00Z") },
		&DeprecationInfo{
			Since:  "2024-01-01",
			Sunset: "2024-12-31",
			Brownouts: []BrownoutWindow{
				window("2024-06-15T00:00:00Z", "2024-06-15T02:00:00Z"),
				window("2024-06-15T04:00:00Z", "2024-06-15T06:00:00Z"),
			},
		},
	)

	w := serveThroughDeprecationMiddleware(handler, "/old-api", nil)

	if !*nextCalled {
		t.Error("Expected next handler to be called between windows")
	}
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 between windows, got %d", w.Code)
	}
	if w.Header().Get("X-SEAM-Brownout") != "" {
		t.Error("Expected no X-SEAM-Brownout header between windows")
	}
	if dep := w.Header().Get("Deprecation"); dep != "since=2024-01-01" {
		t.Errorf("Expected Deprecation: since=2024-01-01 between windows, got %q", dep)
	}
	if sun := w.Header().Get("Sunset"); sun != "2024-12-31" {
		t.Errorf("Expected Sunset: 2024-12-31 between windows, got %q", sun)
	}
}

// TestServerDeprecationMiddleware_PastSunsetStillAdvertised pins sunset
// handling against the header contract: sunset is advisory, so past sunset
// the route still serves normally — and still advertises its deprecation,
// for as long as the route lives.
func TestServerDeprecationMiddleware_PastSunsetStillAdvertised(t *testing.T) {
	_, handler, nextCalled := newDeprecationWiringServer(t,
		func() time.Time { return mustClock(t, "2025-03-01T00:00:00Z") },
		&DeprecationInfo{
			Since:  "2024-01-01",
			Sunset: "2024-12-31",
		},
	)

	w := serveThroughDeprecationMiddleware(handler, "/old-api", nil)

	if !*nextCalled {
		t.Error("Expected next handler to be called past sunset")
	}
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 past sunset, got %d", w.Code)
	}
	if dep := w.Header().Get("Deprecation"); dep != "since=2024-01-01" {
		t.Errorf("Expected Deprecation header past sunset, got %q", dep)
	}
	if sun := w.Header().Get("Sunset"); sun != "2024-12-31" {
		t.Errorf("Expected Sunset header past sunset, got %q", sun)
	}
}
