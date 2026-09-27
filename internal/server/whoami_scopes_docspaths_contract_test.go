package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The caller listener's three introspection surfaces — /whoami (identity),
// /scopes (scope listing) and /docs/paths (path inventory) — complete the
// contract-pinning series alongside the ephemeral-key, /health/upstreams and
// capture/cache pins. The 200 shapes and the ?all=1 gate already had handler-
// level and document-level coverage; what was unpinned were the cells the
// compiled-in control-plane document promises and only the served pipeline can
// prove: the stage-3 default-deny each surface documents as its 403, the
// filtered/unfiltered /scopes map over the real identity, the documented
// Cache-Control: no-store on /docs/paths' 200, its three-state per-path
// inventory, and the exact-path reservation of the /docs/paths namespace.

// TestCallerControlPlaneIdentityDenial pins the documented 403 on each of the
// three surfaces: an unresolvable caller is default-denied by stage 3 before
// any handler runs, through the common error envelope, with the exact
// "Identity resolution failed" message the compiled-in document quotes.
func TestCallerControlPlaneIdentityDenial(t *testing.T) {
	for _, path := range []string{"/whoami", "/scopes", "/docs/paths"} {
		t.Run(strings.TrimPrefix(path, "/"), func(t *testing.T) {
			s := newControlPlaneContractTestServer(t)
			s.identityResolver.setResolveOverride(func(string) (*Identity, error) {
				return nil, errors.New("no tailnet identity for the test caller")
			})

			resp := serveControlPlane(t, s, http.MethodGet, path, "")
			assertEnvelope(t, resp, http.StatusForbidden, "forbidden", nil)

			var env struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode denial envelope: %v", err)
			}
			if env.Message != "Identity resolution failed" {
				t.Errorf("denial message = %q, want %q", env.Message, "Identity resolution failed")
			}
		})
	}
}

// TestScopesResponseThroughCallerPipeline pins the /scopes 200 contract over
// the real pipeline: the five documented top-level keys, the default filter
// down to the caller's own scopes, and the ?all=1 lift that exposes both
// sources the handler merges.
func TestScopesResponseThroughCallerPipeline(t *testing.T) {
	s := newControlPlaneContractTestServer(t)

	// The loopback test identity holds exactly these five capabilities, so
	// effective_count, the filtered membership, and the scope-version hash
	// below are deterministic.
	held := []string{"k8s-ro:get", "argocd:read", "config:read", "seam:ops:read", "seam:scopes:read-all"}
	heldSet := make(map[string]bool, len(held))
	for _, scope := range held {
		heldSet[scope] = true
	}
	wantScopeVersion := ComputeScopeVersionHash(held)

	// Stage 3 sits outside the scope-version middleware, the same order Start
	// assembles: the middleware computes the hash from the identity stage 3
	// put in the request context, so pinning the header requires this order.
	serveScopes := func(t *testing.T, target string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		resp := httptest.NewRecorder()
		s.identityResolutionMiddleware(s.scopeVersionMiddleware(s.callerMux)).ServeHTTP(resp, req)
		return resp
	}

	t.Run("default_is_filtered_to_the_callers_own_scopes", func(t *testing.T) {
		resp := serveScopes(t, "/scopes")
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, body: %s", resp.Code, resp.Body.String())
		}
		if got := resp.Header().Get("X-SEAM-Scope-Version"); got != wantScopeVersion {
			t.Errorf("X-SEAM-Scope-Version = %q, want the caller's scope-set hash %q", got, wantScopeVersion)
		}

		var body struct {
			Scopes map[string]struct {
				Routes []string `json:"routes"`
				Source string   `json:"source"`
			} `json:"scopes"`
			Filtered       bool `json:"filtered"`
			TotalScopes    int  `json:"total_scopes"`
			Returned       int  `json:"returned"`
			EffectiveCount int  `json:"effective_count"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode scopes response: %v", err)
		}

		if !body.Filtered {
			t.Error("filtered = false on the default call, want true")
		}
		if body.EffectiveCount != len(held) {
			t.Errorf("effective_count = %d, want %d", body.EffectiveCount, len(held))
		}
		if body.Returned != len(body.Scopes) {
			t.Errorf("returned = %d, want len(scopes) = %d", body.Returned, len(body.Scopes))
		}
		if body.Returned > body.TotalScopes {
			t.Errorf("returned = %d exceeds total_scopes = %d", body.Returned, body.TotalScopes)
		}

		for scope, info := range body.Scopes {
			if !heldSet[scope] {
				t.Errorf("filtered response leaks scope %q the caller does not hold", scope)
			}
			if info.Source != "spec" && info.Source != "builtin" {
				t.Errorf("scope %q: source = %q, want spec or builtin", scope, info.Source)
			}
			if len(info.Routes) == 0 {
				t.Errorf("scope %q: routes is empty, want at least one route or <control-plane>", scope)
			}
		}

		// The one builtin scope the caller holds must survive the filter — this
		// is the assertion that fails if filtering inverts, breaks, or starts
		// dropping builtin entries from the merged map. (The identity's other
		// held scopes are not in the compiled-in builtin set and the spec
		// declares no routes here, so this is the filtered map's one
		// guaranteed member.)
		if _, ok := body.Scopes["seam:scopes:read-all"]; !ok {
			t.Error("filtered map is missing builtin scope seam:scopes:read-all, which the caller holds")
		}
	})

	t.Run("all_1_lifts_the_filter", func(t *testing.T) {
		resp := serveScopes(t, "/scopes?all=1")
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, body: %s", resp.Code, resp.Body.String())
		}
		if got := resp.Header().Get("X-SEAM-Scope-Version"); got != wantScopeVersion {
			t.Errorf("X-SEAM-Scope-Version = %q, want the caller's scope-set hash %q", got, wantScopeVersion)
		}

		var body struct {
			Scopes map[string]struct {
				Routes []string `json:"routes"`
				Source string   `json:"source"`
			} `json:"scopes"`
			Filtered    bool `json:"filtered"`
			TotalScopes int  `json:"total_scopes"`
			Returned    int  `json:"returned"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode scopes response: %v", err)
		}

		if body.Filtered {
			t.Error("filtered = true with ?all=1, want false")
		}
		if body.Returned != body.TotalScopes || body.TotalScopes != len(body.Scopes) {
			t.Errorf("unfiltered counts disagree: returned=%d total_scopes=%d len(scopes)=%d",
				body.Returned, body.TotalScopes, len(body.Scopes))
		}

		// Every compiled-in builtin scope appears under some source: spec
		// entries keep their routes, pure builtin entries map to the
		// <control-plane> placeholder.
		sawBuiltinSource := false
		for scope, info := range body.Scopes {
			if info.Source == "builtin" {
				sawBuiltinSource = true
				if len(info.Routes) != 1 || info.Routes[0] != "<control-plane>" {
					t.Errorf("builtin scope %q: routes = %v, want [<control-plane>]", scope, info.Routes)
				}
			}
		}
		if !sawBuiltinSource {
			t.Error("no scope with source=builtin in the unfiltered map")
		}
		for _, builtin := range BuiltinControlPlaneScopes {
			if _, ok := body.Scopes[builtin]; !ok {
				t.Errorf("unfiltered map is missing builtin scope %q", builtin)
			}
		}
	})
}

// TestDocsPathsResponseThroughCallerPipeline pins the /docs/paths inventory
// contract over the real pipeline: the documented Cache-Control: no-store on
// the 200, the spec/API version headers, the metadata object, and the
// three-state per-path list — plus the served body matching the 200 schema the
// compiled-in control-plane document publishes for the endpoint.
func TestDocsPathsResponseThroughCallerPipeline(t *testing.T) {
	s := newControlPlaneContractTestServer(t)

	// Two deterministically-shaped tracking entries: a path whose last attempt
	// succeeded, and one that has only failed attempts. Nothing else has been
	// dispatched through this server, so the inventory holds exactly these two
	// — a path with no dispatch since restart is absent, not a no-attempt entry.
	s.last2xxTracker.RecordAttempt("/api/v1/pinned-succeeded", "https://upstream.example:443")
	s.last2xxTracker.RecordSuccess("/api/v1/pinned-succeeded", "https://upstream.example:443", "")
	s.last2xxTracker.RecordAttempt("/api/v1/pinned-failing", "https://upstream.example:443")

	resp := serveControlPlane(t, s, http.MethodGet, "/docs/paths", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", resp.Code, resp.Body.String())
	}

	// The one cache header the compiled-in document promises on this surface.
	if cc := resp.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — the document pins /docs/paths as no-store", cc)
	}
	if ct := resp.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	specVersion := resp.Header().Get("X-SEAM-Spec-Version")
	if specVersion == "" {
		t.Error("X-SEAM-Spec-Version header is missing")
	}
	if resp.Header().Get("X-Spec-Version") != specVersion {
		t.Errorf("X-Spec-Version = %q, want the same hash X-SEAM-Spec-Version carries (%q)",
			resp.Header().Get("X-Spec-Version"), specVersion)
	}
	if resp.Header().Get("X-SEAM-API-Version") == "" {
		t.Error("X-SEAM-API-Version header is missing")
	}

	var body struct {
		Metadata struct {
			Description string `json:"description"`
			SpecVersion string `json:"spec_version"`
			APIVersion  string `json:"api_version"`
			TotalPaths  int    `json:"total_paths"`
		} `json:"metadata"`
		Paths []struct {
			Path                     string `json:"path"`
			State                    string `json:"state"`
			AttemptsSinceLastSuccess int    `json:"attempts_since_last_success"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /docs/paths body: %v", err)
	}

	if body.Metadata.Description == "" {
		t.Error("metadata.description is empty")
	}
	if body.Metadata.SpecVersion == "" || body.Metadata.APIVersion == "" {
		t.Errorf("metadata versions = %q/%q, want both populated",
			body.Metadata.SpecVersion, body.Metadata.APIVersion)
	}
	if body.Metadata.TotalPaths != len(body.Paths) {
		t.Errorf("metadata.total_paths = %d, want len(paths) = %d", body.Metadata.TotalPaths, len(body.Paths))
	}
	if len(body.Paths) != 2 {
		t.Fatalf("paths holds %d entries, want exactly the 2 recorded dispatches", len(body.Paths))
	}

	byPath := make(map[string]string, len(body.Paths))
	for _, p := range body.Paths {
		byPath[p.Path] = p.State
	}
	if got := byPath["/api/v1/pinned-succeeded"]; got != string(Last2xxSucceeded) {
		t.Errorf("succeeded path state = %q, want %q", got, Last2xxSucceeded)
	}
	if got := byPath["/api/v1/pinned-failing"]; got != string(Last2xxNoSuccess) {
		t.Errorf("failing path state = %q, want %q", got, Last2xxNoSuccess)
	}
	for _, p := range body.Paths {
		if p.Path == "/api/v1/pinned-failing" && p.AttemptsSinceLastSuccess != 1 {
			t.Errorf("failing path attempts_since_last_success = %d, want 1", p.AttemptsSinceLastSuccess)
		}
	}

	// The documented shape IS the served shape: the 200 schema the compiled-in
	// document publishes for /docs/paths must keep describing what the handler
	// actually writes, or a consumer reading /docs/control-plane is misled.
	doc := decodeControlPlaneDoc(t, s)
	op, _ := docPathItem(t, doc, "/docs/paths")["get"].(map[string]interface{})
	resp200, _ := op["responses"].(map[string]interface{})["200"].(map[string]interface{})
	content, _ := resp200["content"].(map[string]interface{})["application/json"].(map[string]interface{})
	schema, _ := content["schema"].(map[string]interface{})
	props, _ := schema["properties"].(map[string]interface{})
	pathsSchema, ok := props["paths"].(map[string]interface{})
	if !ok {
		t.Fatalf("documented 200 schema has no paths property: %v", schema)
	}
	if pathsSchema["type"] != "array" {
		t.Errorf("documented paths.type = %v, want array — the handler serves a per-path list", pathsSchema["type"])
	}
}

// TestCallerListenerAssembledPipelineStampsCallerScopeVersion pins the assembly
// itself, not just the middleware pair: over a real Start()ed listener, the
// X-SEAM-Scope-Version header must carry the caller's scope-set hash. The
// scope-version middleware computes that hash from the identity stage 3 puts in
// the request context, so assembling it outside stage 3 stamps the empty
// scope-set hash (SHA-256 of empty input) over the handlers' correct values on
// every caller-listener response.
func TestCallerListenerAssembledPipelineStampsCallerScopeVersion(t *testing.T) {
	callerPort := getAvailablePort(t)
	operatorPort := getAvailablePort(t)

	cfg := &Config{
		CallerPort:    callerPort,
		OperatorPort:  operatorPort,
		BaseURL:       fmt.Sprintf("http://localhost:%d", callerPort),
		SpecDir:       "../../spec",
		AllowlistFile: newBaselineAllowlistFile(t),
	}

	s := New(cfg)
	s.identityResolver = newLoopbackTestIdentityResolver()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = s.Shutdown(ctx) }()
	s.setOpenBaoReady(true)

	time.Sleep(100 * time.Millisecond)

	// Exactly the capability set the loopback test identity carries.
	want := ComputeScopeVersionHash([]string{
		"k8s-ro:get", "argocd:read", "config:read", "seam:ops:read", "seam:scopes:read-all",
	})

	for _, path := range []string{"/whoami", "/scopes"} {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d%s", callerPort, path))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, resp.StatusCode)
		}
		if got := resp.Header.Get("X-SEAM-Scope-Version"); got != want {
			t.Errorf("%s: X-SEAM-Scope-Version = %q, want the caller's scope-set hash %q",
				path, got, want)
		}
	}
}

// TestDocsPathsRejectsWrongMethodThroughEnvelope pins the documented 405 on
// /docs/paths through the common error envelope, the one non-2xx the handler
// itself writes (the 403 belongs to stage 3).
func TestDocsPathsRejectsWrongMethodThroughEnvelope(t *testing.T) {
	s := newControlPlaneContractTestServer(t)
	resp := serveControlPlane(t, s, http.MethodPost, "/docs/paths", "")
	assertEnvelope(t, resp, http.StatusMethodNotAllowed, "method_not_allowed", nil)
}

// TestDocsPathsSubpathsDispatchAsUnknownRoutes pins the namespace boundary: the
// inventory is reserved by exact path, so a sub-path is not a documentation
// surface — it falls through the catch-all dispatch and answers the same 404
// route_not_found envelope an unknown upstream route gets, the /changes/1.2.3
// precedent.
func TestDocsPathsSubpathsDispatchAsUnknownRoutes(t *testing.T) {
	s := newControlPlaneContractTestServer(t)
	for _, target := range []string{"/docs/paths/1.2.3", "/docs/paths/"} {
		t.Run(strings.TrimPrefix(target, "/docs/paths"), func(t *testing.T) {
			resp := serveControlPlane(t, s, http.MethodGet, target, "")
			assertEnvelope(t, resp, http.StatusNotFound, "route_not_found",
				map[string]string{"method": "GET", "path": target})

			// docs_url is a top-level envelope field, not a detail: RouteNotFound
			// points every unknown route at /docs.
			var env struct {
				DocsURL string `json:"docs_url"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode docs_url: %v", err)
			}
			if env.DocsURL != "/docs" {
				t.Errorf("docs_url = %q, want /docs", env.DocsURL)
			}
		})
	}
}
