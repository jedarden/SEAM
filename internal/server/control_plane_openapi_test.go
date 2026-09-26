package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// The control-plane OpenAPI contract (control_plane_openapi.go) is pinned
// here at two levels: the document itself — paths, listener annotations,
// scope gates, and the error-envelope component every non-2xx response must
// reference — and the served surface over the real caller pipeline, including
// the error envelopes the four contract endpoints actually write.

func newControlPlaneContractTestServer(t *testing.T) *Server {
	t.Helper()
	s := New(&Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	})
	// Stage 3 default-denies an unresolvable caller before any control-plane
	// handler runs; present the fixed test identity so requests reach them.
	s.identityResolver = newLoopbackTestIdentityResolver()
	return s
}

// decodeControlPlaneDoc marshals and unmarshals the built document, proving it
// is JSON-serializable exactly as the handler serves it, and returns the
// decoded form.
func decodeControlPlaneDoc(t *testing.T, s *Server) map[string]interface{} {
	t.Helper()
	payload, err := json.Marshal(s.controlPlaneOpenAPIDocument())
	if err != nil {
		t.Fatalf("marshal control-plane document: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("served document is not valid JSON: %v", err)
	}
	return doc
}

func docPathItem(t *testing.T, doc map[string]interface{}, path string) map[string]interface{} {
	t.Helper()
	paths, ok := doc["paths"].(map[string]interface{})
	if !ok {
		t.Fatal("document has no paths object")
	}
	item, ok := paths[path].(map[string]interface{})
	if !ok {
		t.Fatalf("document is missing path %q; has: %v", path, docPaths(doc))
	}
	return item
}

func docPaths(doc map[string]interface{}) []string {
	paths, _ := doc["paths"].(map[string]interface{})
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestControlPlaneOpenAPIContractDocument pins the shape of the compiled-in
// document itself: the endpoints the control-plane contract must name, the
// listener annotation on every operation, the scope gate on the key-issuance
// endpoint, and the error envelope component.
func TestControlPlaneOpenAPIContractDocument(t *testing.T) {
	s := newControlPlaneContractTestServer(t)
	doc := decodeControlPlaneDoc(t, s)

	if got, ok := doc["openapi"].(string); !ok || !strings.HasPrefix(got, "3.") {
		t.Fatalf("openapi = %v, want a 3.x version string", doc["openapi"])
	}
	if _, ok := doc["components"].(map[string]interface{}); !ok {
		t.Fatal("document has no components section")
	}

	// The bead contract names these four endpoints; the documentation surfaces
	// complete the control-plane set.
	for _, path := range []string{
		"/api/v1/tailscale/ephemeral-key",
		"/whoami",
		"/scopes",
		"/changes",
		"/docs",
		"/docs/route",
		"/docs/paths",
		"/openapi.json",
	} {
		docPathItem(t, doc, path)
	}

	// Every operation declares its listener and binds every non-success
	// response to the shared ErrorResponse envelope component.
	components, _ := doc["components"].(map[string]interface{})
	schemas, _ := components["schemas"].(map[string]interface{})
	if _, ok := schemas["ErrorResponse"]; !ok {
		t.Fatal("components.schemas is missing the ErrorResponse envelope")
	}

	for path, item := range doc["paths"].(map[string]interface{}) {
		for method, op := range item.(map[string]interface{}) {
			operation, ok := op.(map[string]interface{})
			if !ok {
				continue // parameters etc. live at path-item level, not operation level
			}
			if got, ok := operation["x-seam-listener"].(string); !ok || got != "caller" {
				t.Errorf("%s %s: x-seam-listener = %v, want caller", strings.ToUpper(method), path, operation["x-seam-listener"])
			}
			responses, _ := operation["responses"].(map[string]interface{})
			if len(responses) == 0 {
				t.Errorf("%s %s: no responses declared", strings.ToUpper(method), path)
			}
			for status, resp := range responses {
				if status == "200" || status == "302" {
					continue
				}
				content, _ := resp.(map[string]interface{})["content"].(map[string]interface{})
				media, _ := content["application/json"].(map[string]interface{})
				schema, _ := media["schema"].(map[string]interface{})
				if ref, _ := schema["$ref"].(string); ref != "#/components/schemas/ErrorResponse" {
					t.Errorf("%s %s: response %s does not reference the ErrorResponse envelope (schema=%v)",
						strings.ToUpper(method), path, status, schema)
				}
			}
		}
	}

	// The scope-version header is documented on every 200 that sets it.
	for _, path := range []string{"/whoami", "/scopes", "/changes"} {
		responses := docPathItem(t, doc, path)["get"].(map[string]interface{})["responses"].(map[string]interface{})
		resp200, _ := responses["200"].(map[string]interface{})
		headers, _ := resp200["headers"].(map[string]interface{})
		if _, ok := headers["X-SEAM-Scope-Version"]; !ok {
			t.Errorf("%s: 200 response does not document X-SEAM-Scope-Version", path)
		}
	}

	// The key-issuance endpoint is the one scoped operation in the document.
	keyOp := docPathItem(t, doc, "/api/v1/tailscale/ephemeral-key")["post"].(map[string]interface{})
	scopes, ok := keyOp["x-required-scope"].([]interface{})
	if !ok || len(scopes) != 1 || scopes[0] != "seam:tailscale:key-create" {
		t.Errorf("ephemeral-key x-required-scope = %v, want [seam:tailscale:key-create]", keyOp["x-required-scope"])
	}
	if body, ok := keyOp["requestBody"].(map[string]interface{}); !ok || body["required"] != true {
		t.Errorf("ephemeral-key requestBody = %v, want required", keyOp["requestBody"])
	}

	// The envelope component carries the six documented fields, with
	// error+message required, and its error enum is exactly the public
	// taxonomy in HTTPStatusMapping.
	envelope, _ := schemas["ErrorResponse"].(map[string]interface{})
	required, _ := envelope["required"].([]interface{})
	if len(required) != 2 || required[0] != "error" || required[1] != "message" {
		t.Errorf("ErrorResponse.required = %v, want [error message]", required)
	}
	props, _ := envelope["properties"].(map[string]interface{})
	for _, field := range []string{"error", "message", "details", "validation_errors", "docs_url", "request_id"} {
		if _, ok := props[field]; !ok {
			t.Errorf("ErrorResponse is missing property %q", field)
		}
	}
	errorProp, _ := props["error"].(map[string]interface{})
	enum, _ := errorProp["enum"].([]interface{})
	wantCodes := make([]string, 0, len(HTTPStatusMapping))
	for code := range HTTPStatusMapping {
		wantCodes = append(wantCodes, string(code))
	}
	sort.Strings(wantCodes)
	gotCodes := make([]string, 0, len(enum))
	for _, c := range enum {
		gotCodes = append(gotCodes, c.(string))
	}
	if strings.Join(gotCodes, ",") != strings.Join(wantCodes, ",") {
		t.Errorf("ErrorResponse.error enum drifted from HTTPStatusMapping:\n got %v\nwant %v", gotCodes, wantCodes)
	}
}

// TestDocsControlPlaneJSONContract exercises the JSON negotiation branch over
// the real caller pipeline.
func TestDocsControlPlaneJSONContract(t *testing.T) {
	s := newControlPlaneContractTestServer(t)

	req := httptest.NewRequest(http.MethodGet, controlPlaneDocsPath, nil)
	req.Header.Set("Accept", "application/json")
	resp := httptest.NewRecorder()
	s.identityResolutionMiddleware(s.callerMux).ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", resp.Code, resp.Body.String())
	}
	if ct := resp.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	for _, path := range []string{"/whoami", "/scopes", "/changes", "/api/v1/tailscale/ephemeral-key"} {
		docPathItem(t, doc, path)
	}
	if got, _ := doc["openapi"].(string); !strings.HasPrefix(got, "3.") {
		t.Errorf("served openapi = %v, want 3.x", doc["openapi"])
	}
}

// TestDocsControlPlaneHTMLShell pins the HTML branch against the same toolbar
// contract TestDocsAgentationWiring enforces for /docs: exactly one import
// map, positioned before the mounting module, plus the nav linking the two
// documentation surfaces.
func TestDocsControlPlaneHTMLShell(t *testing.T) {
	s := newControlPlaneContractTestServer(t)

	req := httptest.NewRequest(http.MethodGet, controlPlaneDocsPath, nil)
	req.Header.Set("Accept", "text/html")
	resp := httptest.NewRecorder()
	s.identityResolutionMiddleware(s.callerMux).ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", resp.Code, resp.Body.String())
	}
	if ct := resp.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	page := resp.Body.String()

	if got := strings.Count(page, docsAgentationImportMapTag); got != 1 {
		t.Fatalf("import map tags = %d, want exactly 1", got)
	}
	mapIdx := strings.Index(page, docsAgentationImportMapTag)
	moduleIdx := strings.Index(page, docsAgentationModuleTag)
	if moduleIdx == -1 {
		t.Fatal("no module script — toolbar is not loaded")
	}
	if mapIdx > moduleIdx {
		t.Error("import map appears after the module script; it would never apply")
	}
	moduleEnd := strings.Index(page[moduleIdx:], "</script>")
	if moduleEnd == -1 {
		t.Fatal("unterminated module script")
	}
	module := page[moduleIdx : moduleIdx+moduleEnd]
	for _, snippet := range docsAgentationMountSnippets {
		if !strings.Contains(module, snippet) {
			t.Errorf("mount loader is missing %q", snippet)
		}
	}

	if !strings.Contains(page, `href="/docs"`) || !strings.Contains(page, `href="`+controlPlaneDocsPath+`"`) {
		t.Error("docs nav is missing the cross-links between /docs and /docs/control-plane")
	}
	if !strings.Contains(page, "var specData =") {
		t.Error("page does not embed the control-plane spec document")
	}
}

// TestDocsControlPlaneLinkedFromDocs pins discoverability: a reader of the
// upstream reference page must be able to reach the control-plane contract
// from it, and both pages must keep exactly one import map after the shared
// shell learned the nav.
func TestDocsControlPlaneLinkedFromDocs(t *testing.T) {
	s := newControlPlaneContractTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	req.Header.Set("Accept", "text/html")
	resp := httptest.NewRecorder()
	s.identityResolutionMiddleware(s.callerMux).ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("GET /docs status = %d", resp.Code)
	}
	page := resp.Body.String()
	if !strings.Contains(page, `href="`+controlPlaneDocsPath+`"`) {
		t.Error("/docs page does not link to the control-plane contract surface")
	}
	if got := strings.Count(page, docsAgentationImportMapTag); got != 1 {
		t.Errorf("import map tags on /docs = %d, want exactly 1", got)
	}
}

// TestControlPlaneErrorEnvelopeIntegration walks the four contract endpoints'
// documented error paths through the real caller pipeline and pins the shared
// envelope: code, message, details, and the no-store/nosniff headers the
// envelope writer stamps.
func TestControlPlaneErrorEnvelopeIntegration(t *testing.T) {
	t.Run("wrong_method_on_whoami", func(t *testing.T) {
		s := newControlPlaneContractTestServer(t)
		resp := serveControlPlane(t, s, http.MethodPost, "/whoami", "")
		assertEnvelope(t, resp, http.StatusMethodNotAllowed, "method_not_allowed", nil)
	})

	t.Run("scopes_all_requires_read_all_scope", func(t *testing.T) {
		s := newControlPlaneContractTestServer(t)
		// The loopback identity holds seam:scopes:read-all; replace it with a
		// caller that does not, so the ?all=1 gate fires.
		s.identityResolver.setResolveOverride(func(remoteAddr string) (*Identity, error) {
			return &Identity{
				Resolved:     true,
				NodeName:     "plain-caller",
				NodeKey:      "plain-caller-node-key",
				Capabilities: []string{"k8s-ro:get"},
			}, nil
		})
		resp := serveControlPlane(t, s, http.MethodGet, "/scopes?all=1", "")
		assertEnvelope(t, resp, http.StatusForbidden, "forbidden",
			map[string]string{"required_scope": "seam:scopes:read-all"})
	})

	t.Run("ephemeral_key_empty_worker_id", func(t *testing.T) {
		s := newControlPlaneContractTestServer(t)
		// Past the scope gate with the required scope held, the empty
		// worker_id is the documented 400 with details.field=worker_id.
		s.identityResolver.setResolveOverride(func(remoteAddr string) (*Identity, error) {
			return &Identity{
				Resolved:     true,
				NodeName:     "key-caller",
				NodeKey:      "key-caller-node-key",
				Capabilities: []string{"seam:tailscale:key-create"},
			}, nil
		})
		resp := serveControlPlane(t, s, http.MethodPost, "/api/v1/tailscale/ephemeral-key", `{}`)
		assertEnvelope(t, resp, http.StatusBadRequest, "bad_request",
			map[string]string{"field": "worker_id"})
	})

	t.Run("changes_rejects_unknown_level", func(t *testing.T) {
		s := newControlPlaneContractTestServer(t)
		resp := serveControlPlane(t, s, http.MethodGet, "/changes?level=9", "")
		assertEnvelope(t, resp, http.StatusBadRequest, "bad_request",
			map[string]string{"valid_levels": "1, 2"})
	})

	t.Run("wrong_method_on_contract_surface", func(t *testing.T) {
		s := newControlPlaneContractTestServer(t)
		resp := serveControlPlane(t, s, http.MethodPost, controlPlaneDocsPath, "")
		assertEnvelope(t, resp, http.StatusMethodNotAllowed, "method_not_allowed", nil)
	})
}

// TestWhoamiContractThroughCallerPipeline pins the /whoami response contract
// end to end: headers, the four top-level keys, and the identity object shape.
func TestWhoamiContractThroughCallerPipeline(t *testing.T) {
	s := newControlPlaneContractTestServer(t)
	resp := serveControlPlane(t, s, http.MethodGet, "/whoami", "")

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", resp.Code, resp.Body.String())
	}
	if resp.Header().Get("X-SEAM-Scope-Version") == "" {
		t.Error("X-SEAM-Scope-Version header is missing")
	}

	var body map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	for _, key := range []string{"identity", "effective_scopes", "scope_version", "resolved"} {
		if _, ok := body[key]; !ok {
			t.Errorf("body is missing key %q, got %v", key, body)
		}
	}
	if body["resolved"] != true {
		t.Errorf("resolved = %v, want true for the resolved test identity", body["resolved"])
	}
	scopes, _ := body["effective_scopes"].([]interface{})
	if len(scopes) == 0 {
		t.Error("effective_scopes is empty for the resolved test identity")
	}
	identity, _ := body["identity"].(map[string]interface{})
	for _, key := range []string{"node_key", "node_name", "user", "tags"} {
		if _, ok := identity[key]; !ok {
			t.Errorf("identity is missing key %q, got %v", key, identity)
		}
	}
}

// serveControlPlane runs one request through stage 3 and the caller mux.
func serveControlPlane(t *testing.T, s *Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	resp := httptest.NewRecorder()
	s.identityResolutionMiddleware(s.callerMux).ServeHTTP(resp, req)
	return resp
}

// assertEnvelope decodes the shared error envelope and checks status, code,
// headers, and any expected detail keys.
func assertEnvelope(t *testing.T, resp *httptest.ResponseRecorder, wantStatus int, wantCode string, wantDetails map[string]string) {
	t.Helper()
	if resp.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body: %s)", resp.Code, wantStatus, resp.Body.String())
	}
	if ct := resp.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cc := resp.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if resp.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("X-Content-Type-Options = missing, want nosniff")
	}

	var envelope struct {
		Error   string         `json:"error"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope from %q: %v", resp.Body.String(), err)
	}
	if envelope.Error != wantCode {
		t.Errorf("error = %q, want %q", envelope.Error, wantCode)
	}
	if envelope.Message == "" {
		t.Error("error envelope carries an empty message")
	}
	for key, want := range wantDetails {
		if got, _ := envelope.Details[key].(string); got != want {
			t.Errorf("details[%q] = %v, want %q", key, envelope.Details[key], want)
		}
	}
}
