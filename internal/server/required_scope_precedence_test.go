package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	libopenapi "github.com/pb33f/libopenapi"

	"github.com/ardenone/seam/internal/spec"
)

// requiredScopeFixtureFragment is a route fragment with a fragment-root
// x-required-scope DEFAULT and one operation-level override. It exercises the
// full documented pipeline: fragment load, route-metadata propagation, merge,
// and route-table build. publicScopeFixtureFragment is the control: no scope
// form anywhere.
const requiredScopeFixtureFragment = `x-seam-schema: v1
x-seam-owner: geo-service
x-api-version: v1
x-upstream: https://geo-service.ardenone.internal
x-required-scope: "geo-service:query"
paths:
  /items:
    get:
      responses:
        '200':
          description: ok
    post:
      x-required-scope: [geo-service:mutate]
      responses:
        '200':
          description: ok
`

const publicScopeFixtureFragment = `x-seam-schema: v1
x-seam-owner: anon-service
x-api-version: v1
x-upstream: https://anon-service.ardenone.internal
paths:
  /open:
    get:
      responses:
        '200':
          description: ok
`

// requiredScopeTableFromFragments drives the real fragment pipeline (load,
// propagate, merge, parse, build) over the given owner/fragment pairs and
// returns the built route table. It fails the test if a fragment is
// quarantined or the build errors.
func requiredScopeTableFromFragments(t *testing.T, fragments map[string]string) *RouteTable {
	t.Helper()
	root := t.TempDir()
	for owner, fragment := range fragments {
		if err := writeOwnerFragment(root, owner, "route.yaml", fragment); err != nil {
			t.Fatal(err)
		}
	}

	loader, err := spec.NewWithFragments(root, "http://localhost:9999", "", root)
	if err != nil {
		t.Fatal(err)
	}
	if quarantined := loader.FragmentLoader.GetQuarantinedCount(); quarantined != 0 {
		t.Fatalf("expected no quarantined fragments, got %d", quarantined)
	}

	doc, err := libopenapi.NewDocument(loader.GetRawDocument())
	if err != nil {
		t.Fatalf("failed to load merged document: %v", err)
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatalf("failed to build model: %v", err)
	}

	table, err := BuildRouteTable(&model.Model)
	if err != nil {
		t.Fatalf("expected route table to build, got: %v", err)
	}
	return table
}

// writeOwnerFragment lays a fragment out the way the loader expects it:
// <root>/<owner>/<name>, with x-seam-owner naming the parent directory.
func writeOwnerFragment(root, owner, name, contents string) error {
	directory := filepath.Join(root, owner)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600)
}

func requiredScopeFor(t *testing.T, table *RouteTable, path, method string) []string {
	t.Helper()
	for _, entry := range table.GetRoutes() {
		if entry.PathTemplate == path && entry.Method == method {
			return entry.RequiredScopes
		}
	}
	t.Fatalf("route %s %s not found in built table", method, path)
	return nil
}

// TestBuildRouteTable_RequiredScopePrecedence pins the documented precedence
// between fragment-root (route-level) and operation-level x-required-scope:
// the operation-level value is the authority and REPLACES the root default,
// never merges with it; a silent operation inherits the root default; a route
// with neither is public. The fragment-root half of this contract is what the
// internal route-metadata propagation exists to carry across the merge.
func TestBuildRouteTable_RequiredScopePrecedence(t *testing.T) {
	table := requiredScopeTableFromFragments(t, map[string]string{
		"geo-service":  requiredScopeFixtureFragment,
		"anon-service": publicScopeFixtureFragment,
	})

	t.Run("silent operation inherits the fragment-root default", func(t *testing.T) {
		scopes := requiredScopeFor(t, table, "/items", "GET")
		if len(scopes) != 1 || scopes[0] != "geo-service:query" {
			t.Fatalf("expected GET /items to inherit [geo-service:query] from the fragment root, got %v", scopes)
		}
	})

	t.Run("operation-level value replaces the default and never merges", func(t *testing.T) {
		scopes := requiredScopeFor(t, table, "/items", "POST")
		if len(scopes) != 1 || scopes[0] != "geo-service:mutate" {
			t.Fatalf("expected POST /items to be governed by exactly [geo-service:mutate], got %v", scopes)
		}
		for _, inherited := range scopes {
			if inherited == "geo-service:query" {
				t.Fatalf("operation-level x-required-scope merged with the fragment-root default instead of replacing it: %v", scopes)
			}
		}
	})

	t.Run("fragment-root default covers every route in the fragment", func(t *testing.T) {
		// A second, scopeless path in the same fragment would also inherit —
		// the root default is route-wide, so the inheritance check pins
		// GET /items only. The genuinely public case needs its own fragment.
		scopes := requiredScopeFor(t, table, "/items", "GET")
		if len(scopes) != 1 || scopes[0] != "geo-service:query" {
			t.Fatalf("expected GET /items to still be governed by the root default, got %v", scopes)
		}
	})

	t.Run("fragment without any scope form yields public routes", func(t *testing.T) {
		scopes := requiredScopeFor(t, table, "/open", "GET")
		if len(scopes) != 0 {
			t.Fatalf("expected GET /open to have no scope requirement, got %v", scopes)
		}
	})
}

// TestBuildRouteTable_MalformedRequiredScopeOverride pins the route-table's
// rejection of malformed x-required-scope overrides — the runtime counterpart
// to the lint rules in internal/spec. A malformed value must fail the table
// build with a diagnostic naming the extension, never silently downgrade to a
// public route. The path-item internal-marker cases are what a corrupt
// fragment-root default looks like after propagation.
func TestBuildRouteTable_MalformedRequiredScopeOverride(t *testing.T) {
	cases := []struct {
		name    string
		pathExt string
		opExt   string
		wantErr string
	}{
		{
			name:    "operation-level object",
			opExt:   `"x-required-scope": {"service": "action"}`,
			wantErr: "x-required-scope must be a string or array of strings",
		},
		{
			name:    "operation-level empty array",
			opExt:   `"x-required-scope": []`,
			wantErr: "x-required-scope array cannot be empty",
		},
		{
			name:    "operation-level empty string element",
			opExt:   `"x-required-scope": ["k8s-ro:get", ""]`,
			wantErr: "x-required-scope array element 1 is empty",
		},
		{
			name:    "propagated default is an empty array",
			pathExt: `"x-seam-internal-required-scope": []`,
			wantErr: "x-required-scope array cannot be empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fragment strings.Builder
			fragment.WriteString(`{"openapi": "3.0.0", "info": {"title": "t", "version": "1"}, "paths": {"/items": {`)
			if tc.pathExt != "" {
				fragment.WriteString(tc.pathExt + ", ")
			}
			fragment.WriteString(`"get": {`)
			if tc.opExt != "" {
				fragment.WriteString(tc.opExt + ", ")
			}
			fragment.WriteString(`"responses": {"200": {"description": "ok"}}}}}}`)

			doc, err := libopenapi.NewDocument([]byte(fragment.String()))
			if err != nil {
				t.Fatalf("failed to create document: %v", err)
			}
			model, err := doc.BuildV3Model()
			if err != nil {
				t.Fatalf("failed to build model: %v", err)
			}

			table, err := BuildRouteTable(&model.Model)
			if err == nil {
				t.Fatalf("expected route table build to fail, got table with %d routes", table.RouteCount())
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

// TestBuildRouteTable_NonStringScalarDefaultCoercesToLiteralScope documents
// the route table's runtime leniency toward scalar x-required-scope values:
// yaml decoding accepts any scalar as its literal text, so a numeric default
// becomes the single scope "42" instead of a build failure. The fragment
// schema is the shape authority and rejects that form at lint time
// (TestLint_MalformedRootRequiredScope in internal/spec); this test pins what
// an unlinted fragment would do rather than implying a rejection exists.
func TestBuildRouteTable_NonStringScalarDefaultCoercesToLiteralScope(t *testing.T) {
	specJSON := `{
		"openapi": "3.0.0",
		"info": {"title": "t", "version": "1"},
		"paths": {
			"/items": {
				"x-seam-internal-required-scope": 42,
				"get": {"responses": {"200": {"description": "ok"}}}
			}
		}
	}`
	doc, err := libopenapi.NewDocument([]byte(specJSON))
	if err != nil {
		t.Fatalf("failed to create document: %v", err)
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatalf("failed to build model: %v", err)
	}

	table, err := BuildRouteTable(&model.Model)
	if err != nil {
		t.Fatalf("expected the scalar default to coerce, got error: %v", err)
	}
	scopes := requiredScopeFor(t, table, "/items", "GET")
	if len(scopes) != 1 || scopes[0] != "42" {
		t.Fatalf("expected the scalar default to coerce to the literal scope [42], got %v", scopes)
	}
}

// runRequiredScopeRequest posts one request through the stage-5 authorization
// middleware with the given route and identity planted in the request context,
// and returns whether the next handler ran plus the recorder.
func runRequiredScopeRequest(t *testing.T, entry RouteEntry, identity *Identity) (bool, *httptest.ResponseRecorder) {
	t.Helper()
	s := &Server{identityResolver: NewIdentityResolver()}

	handlerCalled := false
	handler := s.authorizationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, entry.PathTemplate, nil)
	withRouteMatch(req, &RouteMatch{Route: entry}, nil)
	if identity != nil {
		req = req.WithContext(contextWithIdentity(req.Context(), identity))
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return handlerCalled, w
}

// TestAuthorizationMiddleware_RequiredScopeEnforcement drives the stage-5
// middleware over the scope outcomes the route table can produce: allowed,
// deliberately under-scoped, unresolved identity, and routes with no scope
// requirement at all. The 403 bodies are the documented error envelope.
func TestAuthorizationMiddleware_RequiredScopeEnforcement(t *testing.T) {
	scopedRoute := RouteEntry{
		PathTemplate:   "/items",
		Method:         "GET",
		RequiredScopes: []string{"k8s-ro:get", "k8s-rw:delete"},
	}
	publicRoute := RouteEntry{
		PathTemplate: "/public",
		Method:       "GET",
	}

	t.Run("identity holding one required scope is allowed", func(t *testing.T) {
		identity := &Identity{NodeName: "caller", Resolved: true, Capabilities: []string{"k8s-rw:delete"}}
		called, w := runRequiredScopeRequest(t, scopedRoute, identity)
		if !called {
			t.Fatal("expected the next handler to run for an identity holding one required scope")
		}
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("deliberately under-scoped identity is denied with the documented 403", func(t *testing.T) {
		identity := &Identity{NodeName: "caller", Resolved: true, Capabilities: []string{"analytics:read"}}
		called, w := runRequiredScopeRequest(t, scopedRoute, identity)
		if called {
			t.Fatal("the next handler must not run for an under-scoped identity")
		}
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d", w.Code)
		}
		var envelope ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("expected the documented error envelope, got: %s", w.Body.String())
		}
		if envelope.Error != ErrCodeForbidden {
			t.Fatalf("expected error code %q, got %q", ErrCodeForbidden, envelope.Error)
		}
		if !strings.Contains(envelope.Message, "Route requires one of scopes") {
			t.Fatalf("expected the scope-denial message, got %q", envelope.Message)
		}
		for _, required := range scopedRoute.RequiredScopes {
			if !strings.Contains(envelope.Message, required) {
				t.Fatalf("expected denial message to name required scope %q, got %q", required, envelope.Message)
			}
		}
	})

	t.Run("resolved identity with no capabilities is denied", func(t *testing.T) {
		identity := &Identity{NodeName: "caller", Resolved: true}
		called, w := runRequiredScopeRequest(t, scopedRoute, identity)
		if called {
			t.Fatal("the next handler must not run for an identity with no capabilities")
		}
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d", w.Code)
		}
	})

	t.Run("unresolved identity is denied before scope matching", func(t *testing.T) {
		identity := &Identity{NodeName: "caller", Resolved: false, Capabilities: []string{"k8s-ro:get"}}
		called, w := runRequiredScopeRequest(t, scopedRoute, identity)
		if called {
			t.Fatal("the next handler must not run for an unresolved identity")
		}
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d", w.Code)
		}
		var envelope ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("expected the documented error envelope, got: %s", w.Body.String())
		}
		if !strings.Contains(envelope.Message, "Identity resolution failed") {
			t.Fatalf("expected the identity-failure message, got %q", envelope.Message)
		}
	})

	t.Run("route with no scope requirement is public even for a scopeless identity", func(t *testing.T) {
		identity := &Identity{NodeName: "caller", Resolved: true}
		called, w := runRequiredScopeRequest(t, publicRoute, identity)
		if !called {
			t.Fatal("expected the next handler to run on a public route")
		}
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("missing route match passes through for control-plane paths", func(t *testing.T) {
		s := &Server{identityResolver: NewIdentityResolver()}
		handlerCalled := false
		handler := s.authorizationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlerCalled = true
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/config/status", nil)
		req = req.WithContext(contextWithIdentity(req.Context(), &Identity{NodeName: "operator", Resolved: true}))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if !handlerCalled {
			t.Fatal("expected the next handler to run when no route match is in the context")
		}
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})
}
