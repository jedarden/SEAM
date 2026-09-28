package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// healthSentinelCrossCuttingCase describes the two listener surfaces for one
// health endpoint. Keeping the cases together makes it difficult to add a new
// sentinel and test only its happy path while forgetting the listener or
// scope boundary that protects the other one.
type healthSentinelCrossCuttingCase struct {
	name       string
	path       string
	callerOwn  bool
	bodyMarker string
}

var healthSentinelCrossCuttingCases = []healthSentinelCrossCuttingCase{
	{
		name:       "health-alias",
		path:       "/_seam/health",
		callerOwn:  true,
		bodyMarker: "OK",
	},
	{
		name:       "healthz",
		path:       "/_seam/healthz",
		callerOwn:  true,
		bodyMarker: "OK",
	},
	{
		name:       "readyz",
		path:       "/_seam/readyz",
		callerOwn:  true,
		bodyMarker: `"ready":true`,
	},
	{
		name:       "credential-health",
		path:       "/health/credentials",
		bodyMarker: `"credentials"`,
	},
	{
		name:       "upstream-health",
		path:       "/health/upstreams",
		bodyMarker: `"upstreams":`,
	},
}

// TestHealthSentinelCrossCuttingBoundaries is the shared boundary matrix for
// the health surface. Each endpoint must be served only by its documented
// listener; operator endpoints additionally require seam:ops:read. Every
// response is checked for the handler's no-store contract and for accidental
// reflection of a credential-shaped request value, including on denials.
func TestHealthSentinelCrossCuttingBoundaries(t *testing.T) {
	const credentialCanary = "health-sentinel-credential-REPLACE"

	for _, tc := range healthSentinelCrossCuttingCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSentinelIsolationTestServer(t)

			request := func(handler http.Handler) *httptest.ResponseRecorder {
				t.Helper()
				req := httptest.NewRequest(http.MethodGet, tc.path, nil)
				req.Header.Set("Authorization", "Bearer "+credentialCanary)
				req.Header.Set("X-Auth-Token", credentialCanary)
				req.Header.Set("Cookie", "credential="+credentialCanary)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec
			}

			var own, other http.Handler
			if tc.callerOwn {
				own = s.callerMux
				other = s.identityResolutionMiddleware(s.operatorMux)
			} else {
				own = s.identityResolutionMiddleware(s.operatorMux)
				other = s.identityResolutionMiddleware(s.callerMux)
			}

			served := request(own)
			if served.Code != http.StatusOK {
				t.Fatalf("own listener status = %d, want 200 (body: %s)", served.Code, served.Body.String())
			}
			if !strings.Contains(served.Body.String(), tc.bodyMarker) {
				t.Errorf("own listener body = %q, want marker %q", served.Body.String(), tc.bodyMarker)
			}
			assertHealthSentinelNoStoreAndNoCanary(t, served, credentialCanary, "own listener")

			refused := request(other)
			if refused.Code != http.StatusNotFound {
				t.Fatalf("other listener status = %d, want 404 (body: %s)", refused.Code, refused.Body.String())
			}
			if strings.Contains(refused.Body.String(), tc.bodyMarker) {
				t.Errorf("other listener denial echoes sentinel body marker %q: %s", tc.bodyMarker, refused.Body.String())
			}
			assertHealthSentinelNoCanary(t, refused, credentialCanary, "other listener")

			if tc.callerOwn {
				return
			}

			// A resolved identity with ordinary proxy access is still not an
			// operator. Exercise the registered endpoint, not a bare middleware
			// instance, so dropping the route's scope wrapper fails this matrix.
			s.identityResolver.setResolveOverride(func(remoteAddr string) (*Identity, error) {
				return &Identity{
					Resolved:     true,
					NodeName:     "health-caller",
					NodeKey:      "health-caller-node-key",
					Capabilities: []string{"k8s-ro:get"},
				}, nil
			})
			denied := request(s.identityResolutionMiddleware(s.operatorMux))
			if denied.Code != http.StatusForbidden {
				t.Fatalf("operator listener without seam:ops:read status = %d, want 403 (body: %s)", denied.Code, denied.Body.String())
			}
			if !strings.Contains(denied.Body.String(), "seam:ops:read") {
				t.Errorf("operator denial = %q, want required scope named", denied.Body.String())
			}
			if strings.Contains(denied.Body.String(), tc.bodyMarker) {
				t.Errorf("operator denial echoes sentinel body marker %q: %s", tc.bodyMarker, denied.Body.String())
			}
			assertHealthSentinelNoCanary(t, denied, credentialCanary, "operator scope denial")
		})
	}
}

// TestHealthSentinelCrossCuttingMiddlewareBypass drives every health sentinel
// through the corresponding production cache/quota composition. TTL and cost
// are deliberately configured on the sentinel paths themselves. The same
// middleware instances must still cache/refuse non-reserved traffic, making
// the health assertions non-vacuous.
func TestHealthSentinelCrossCuttingMiddlewareBypass(t *testing.T) {
	s := newSentinelIsolationTestServer(t)

	caller := s.cacheMiddleware(s.quotaMiddleware(s.callerMux))
	operator := s.identityResolutionMiddleware(s.cacheMiddleware(s.quotaMiddleware(s.operatorMux)))
	for _, tc := range healthSentinelCrossCuttingCases {
		s.cacheTTLs[tc.path] = 300
		s.quotaTracker.SetQuota(tc.path, QuotaConfig{
			Limit:  0.01,
			Window: time.Hour,
			Scope:  "per-route",
		})
		s.quotaTracker.SetCostPerCall(tc.path, 1.0)
	}

	const credentialCanary = "health-sentinel-credential-REPLACE"
	for _, tc := range healthSentinelCrossCuttingCases {
		t.Run(tc.name, func(t *testing.T) {
			handler := caller
			if !tc.callerOwn {
				handler = operator
			}

			beforeCache := s.cache.Stats()
			beforeQuota := interactionAccumulated(s, tc.path)
			for requestNumber := 1; requestNumber <= 2; requestNumber++ {
				req := httptest.NewRequest(http.MethodGet, tc.path, nil)
				req.Header.Set("Authorization", "Bearer "+credentialCanary)
				req.Header.Set("X-Auth-Token", credentialCanary)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				scenario := fmt.Sprintf("%s request #%d", tc.path, requestNumber)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s status = %d, want 200 (body: %s)", scenario, rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), tc.bodyMarker) {
					t.Errorf("%s body = %q, want marker %q", scenario, rec.Body.String(), tc.bodyMarker)
				}
				assertHealthSentinelNoStoreAndNoCanary(t, rec, credentialCanary, scenario)
				for _, header := range []string{
					"X-SEAM-Cache", "X-Quota-Bypassed", "X-Quota-Cost-Per-Call",
					"X-Quota-Remaining", "X-SEAM-Budget-Remaining",
				} {
					if got := rec.Header().Get(header); got != "" {
						t.Errorf("%s: %s = %q, want absent on sentinel traffic", scenario, header, got)
					}
				}
			}

			afterCache := s.cache.Stats()
			if afterCache != beforeCache {
				t.Errorf("%s changed cache stats: before=%+v after=%+v", tc.path, beforeCache, afterCache)
			}
			if got := interactionAccumulated(s, tc.path); got != beforeQuota {
				t.Errorf("%s changed quota accumulation: before=$%.2f after=$%.2f", tc.path, beforeQuota, got)
			}
		})
	}

	// Quota control: the same admission configuration refuses an ordinary
	// path, proving the sentinel requests did not merely avoid a dead quota
	// implementation.
	quotaPath := "/health-sentinel-quota-control"
	s.quotaTracker.SetQuota(quotaPath, QuotaConfig{Limit: 0.01, Window: time.Hour, Scope: "per-route"})
	s.quotaTracker.SetCostPerCall(quotaPath, 1.0)
	quotaControl := httptest.NewRecorder()
	caller.ServeHTTP(quotaControl, httptest.NewRequest(http.MethodGet, quotaPath, nil))
	if quotaControl.Code != http.StatusTooManyRequests {
		t.Fatalf("quota control status = %d, want 429", quotaControl.Code)
	}

	// Cache control: an ordinary path under the same cache middleware is
	// stored and replayed, proving the health-path cache bypass is real.
	cachePath := "/health-sentinel-cache-control"
	s.cacheTTLs[cachePath] = 300
	cacheControlFirst := httptest.NewRecorder()
	caller.ServeHTTP(cacheControlFirst, httptest.NewRequest(http.MethodGet, cachePath, nil))
	cacheControlSecond := httptest.NewRecorder()
	caller.ServeHTTP(cacheControlSecond, httptest.NewRequest(http.MethodGet, cachePath, nil))
	if cacheControlSecond.Header().Get("X-SEAM-Cache") != "HIT" {
		t.Fatalf("cache control second response X-SEAM-Cache = %q, want HIT", cacheControlSecond.Header().Get("X-SEAM-Cache"))
	}
}

func assertHealthSentinelNoStoreAndNoCanary(t *testing.T, rec *httptest.ResponseRecorder, canary, scenario string) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); rec.Code == http.StatusOK && got != "no-store" {
		t.Errorf("%s Cache-Control = %q, want no-store", scenario, got)
	}
	assertHealthSentinelNoCanary(t, rec, canary, scenario)
}

func assertHealthSentinelNoCanary(t *testing.T, rec *httptest.ResponseRecorder, canary, scenario string) {
	t.Helper()
	if strings.Contains(rec.Body.String(), canary) {
		t.Errorf("%s response body exposes credential canary %q: %s", scenario, canary, rec.Body.String())
	}
	for name, values := range rec.Header() {
		for _, value := range values {
			if strings.Contains(value, canary) {
				t.Errorf("%s response header %s exposes credential canary %q", scenario, name, canary)
			}
		}
	}
}
