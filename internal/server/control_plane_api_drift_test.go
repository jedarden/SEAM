package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
)

// controlPlaneDriftEndpoint is the small contract shared by the published
// endpoint tables, the listener registrations, and the control-plane OpenAPI
// document. The focused endpoint tests exercise handler details; this test is
// deliberately about keeping those three public surfaces in lockstep.
type controlPlaneDriftEndpoint struct {
	path          string
	method        string
	listener      string
	authorization string
}

func controlPlaneDriftCallerEndpoints() []controlPlaneDriftEndpoint {
	return []controlPlaneDriftEndpoint{
		{path: "/whoami", method: http.MethodGet, listener: "caller", authorization: "none"},
		{path: "/scopes", method: http.MethodGet, listener: "caller", authorization: "seam:scopes:read-all"},
		{path: "/changes", method: http.MethodGet, listener: "caller", authorization: "none"},
		{path: "/api/v1/tailscale/ephemeral-key", method: http.MethodPost, listener: "caller", authorization: "seam:tailscale:key-create"},
		{path: "/docs", method: http.MethodGet, listener: "caller", authorization: "none"},
		{path: "/docs/route", method: http.MethodGet, listener: "caller", authorization: "none"},
		{path: "/docs/paths", method: http.MethodGet, listener: "caller", authorization: "none"},
		{path: "/openapi.json", method: http.MethodGet, listener: "caller", authorization: "none"},
	}
}

func controlPlaneDriftOperatorEndpoints() []controlPlaneDriftEndpoint {
	return []controlPlaneDriftEndpoint{
		{path: "/config/status", method: http.MethodGet, listener: "operator", authorization: "seam:ops:read"},
		{path: "/_seam/capture/save", method: http.MethodPost, listener: "operator", authorization: "seam:ops:read"},
		{path: "/_seam/capture/status", method: http.MethodGet, listener: "operator", authorization: "seam:ops:read"},
		{path: "/_seam/cache/status", method: http.MethodGet, listener: "operator", authorization: "seam:ops:read"},
		{path: "/_seam/cache/cleanup", method: http.MethodPost, listener: "operator", authorization: "seam:ops:read"},
		{path: "/health/credentials", method: http.MethodGet, listener: "operator", authorization: "seam:ops:read"},
		{path: "/health/upstreams", method: http.MethodGet, listener: "operator", authorization: "seam:ops:read"},
	}
}

func controlPlaneDriftAllEndpoints() []controlPlaneDriftEndpoint {
	return append(controlPlaneDriftCallerEndpoints(), controlPlaneDriftOperatorEndpoints()...)
}

func controlPlaneDriftKey(endpoint controlPlaneDriftEndpoint) string {
	return endpoint.method + " " + endpoint.path
}

// markdownControlPlaneRows reads the endpoint rows from one published table.
// Keeping this parser intentionally narrow makes a malformed or moved table
// fail loudly instead of silently turning the documentation check into a
// substring search.
func markdownControlPlaneRows(t *testing.T, document, header string, matrix bool) map[string]controlPlaneDriftEndpoint {
	t.Helper()
	lines := strings.Split(document, "\n")
	headerIndex := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			if headerIndex != -1 {
				t.Fatalf("documentation contains duplicate table header %q", header)
			}
			headerIndex = i
		}
	}
	if headerIndex == -1 {
		t.Fatalf("documentation is missing table header %q", header)
	}

	rows := make(map[string]controlPlaneDriftEndpoint)
	for _, line := range lines[headerIndex+1:] {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(line, "|")
		if len(cells) < 5 || strings.Contains(cells[1], "---") {
			continue
		}
		path := strings.Trim(strings.TrimSpace(cells[1]), "`")
		if !strings.HasPrefix(path, "/") {
			continue
		}

		var endpoint controlPlaneDriftEndpoint
		endpoint.path = path
		if matrix {
			endpoint.listener = strings.TrimSpace(cells[2])
			endpoint.method = strings.Trim(strings.TrimSpace(cells[3]), "`")
			endpoint.authorization = strings.TrimSpace(cells[4])
		} else {
			endpoint.listener = "caller"
			endpoint.method = strings.Trim(strings.TrimSpace(cells[2]), "`")
			endpoint.authorization = strings.TrimSpace(cells[3])
		}
		key := controlPlaneDriftKey(endpoint)
		if _, exists := rows[key]; exists {
			t.Fatalf("documentation contains duplicate endpoint row %q", key)
		}
		rows[key] = endpoint
	}
	return rows
}

func assertControlPlaneDriftRows(t *testing.T, got map[string]controlPlaneDriftEndpoint, want []controlPlaneDriftEndpoint) {
	t.Helper()
	wantKeys := make(map[string]bool, len(want))
	for _, endpoint := range want {
		key := controlPlaneDriftKey(endpoint)
		wantKeys[key] = true
		row, ok := got[key]
		if !ok {
			t.Errorf("documentation is missing endpoint %q", key)
			continue
		}
		if row.listener != endpoint.listener {
			t.Errorf("documentation row %q listener = %q, want %q", key, row.listener, endpoint.listener)
		}
		if endpoint.authorization != "" && endpoint.authorization != "none" && !strings.Contains(row.authorization, endpoint.authorization) {
			t.Errorf("documentation row %q authorization = %q, want it to mention %q", key, row.authorization, endpoint.authorization)
		}
	}
	for key := range got {
		if !wantKeys[key] {
			t.Errorf("documentation has unexpected endpoint row %q", key)
		}
	}
}

func servedControlPlaneOpenAPI(t *testing.T, s *Server) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, controlPlaneDocsPath, nil)
	req.Header.Set("Accept", "application/json")
	req = req.WithContext(contextWithIdentity(req.Context(), identityWithScopes(
		"seam:scopes:read-all", "seam:tailscale:key-create")))
	recorder := httptest.NewRecorder()
	s.callerMux.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200; body: %s", controlPlaneDocsPath, recorder.Code, recorder.Body.String())
	}
	var document map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("served control-plane OpenAPI is invalid JSON: %v", err)
	}
	return document
}

func openAPIPathMethods(t *testing.T, document map[string]interface{}, path string) map[string]interface{} {
	t.Helper()
	paths, ok := document["paths"].(map[string]interface{})
	if !ok {
		t.Fatal("served control-plane OpenAPI has no paths object")
	}
	item, ok := paths[path].(map[string]interface{})
	if !ok {
		t.Fatalf("served control-plane OpenAPI is missing path %q", path)
	}
	return item
}

func sortedDriftKeys(endpoints []controlPlaneDriftEndpoint) []string {
	keys := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		keys = append(keys, controlPlaneDriftKey(endpoint))
	}
	sort.Strings(keys)
	return keys
}

// TestControlPlaneAPIAndOpenAPIDrift compares the two published markdown
// tables, the registered muxes, and the served control-plane OpenAPI. A new,
// removed, moved, or re-scoped endpoint must update every surface together.
func TestControlPlaneAPIAndOpenAPIDrift(t *testing.T) {
	caller := controlPlaneDriftCallerEndpoints()
	operator := controlPlaneDriftOperatorEndpoints()
	all := controlPlaneDriftAllEndpoints()
	matrix := append([]controlPlaneDriftEndpoint{}, operator...)
	matrix = append(matrix, caller[0], caller[1], caller[3])

	apiDocBytes, err := os.ReadFile("../../docs/notes/control-plane-api-contracts.md")
	if err != nil {
		t.Fatalf("read control-plane API documentation: %v", err)
	}
	matrixDocBytes, err := os.ReadFile("../../docs/notes/control-plane-endpoint-matrix.md")
	if err != nil {
		t.Fatalf("read control-plane endpoint matrix: %v", err)
	}
	apiRows := markdownControlPlaneRows(t, string(apiDocBytes), "| Endpoint | Method | Scope gate | Notes |", false)
	matrixRows := markdownControlPlaneRows(t, string(matrixDocBytes), "| Endpoint | Listener | Method | Authorization | Status codes | Successful response shape | Cache policy |", true)
	assertControlPlaneDriftRows(t, apiRows, caller)
	assertControlPlaneDriftRows(t, matrixRows, matrix)

	s := newControlPlaneContractTestServer(t)
	for _, endpoint := range caller {
		wrongMethod := http.MethodPost
		if endpoint.method == http.MethodPost {
			wrongMethod = http.MethodGet
		}
		response := serveMuxWithIdentity(s.callerMux, wrongMethod, endpoint.path,
			identityWithScopes("seam:scopes:read-all", "seam:tailscale:key-create"))
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("caller registration %s returned %d for wrong method, want 405", controlPlaneDriftKey(endpoint), response.StatusCode)
		}
		_ = response.Body.Close()
	}
	for _, endpoint := range operator {
		wrongMethod := http.MethodPost
		if endpoint.method == http.MethodPost {
			wrongMethod = http.MethodGet
		}
		response := serveMuxWithIdentity(s.operatorMux, wrongMethod, endpoint.path, identityWithScopes("seam:ops:read"))
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("operator registration %s returned %d for wrong method, want 405", controlPlaneDriftKey(endpoint), response.StatusCode)
		}
		_ = response.Body.Close()

		underScoped := serveMuxWithIdentity(s.operatorMux, endpoint.method, endpoint.path, identityWithScopes("k8s-ro:get"))
		body, _ := io.ReadAll(underScoped.Body)
		_ = underScoped.Body.Close()
		if underScoped.StatusCode != http.StatusForbidden || !strings.Contains(string(body), endpoint.authorization) {
			t.Errorf("operator registration %s under-scope response = %d %q, want 403 naming %q", controlPlaneDriftKey(endpoint), underScoped.StatusCode, body, endpoint.authorization)
		}
	}

	// The mux split is part of the contract, not merely an implementation
	// detail. Operator paths must not become caller endpoints, and caller paths
	// must not become operator endpoints.
	for _, endpoint := range operator {
		response := serveMuxWithIdentity(s.callerMux, endpoint.method, endpoint.path, identityWithScopes("seam:ops:read"))
		if response.StatusCode == http.StatusOK {
			t.Errorf("operator endpoint %s unexpectedly returned 200 on caller mux", controlPlaneDriftKey(endpoint))
		}
		_ = response.Body.Close()
	}
	for _, endpoint := range caller {
		response := serveMuxWithIdentity(s.operatorMux, endpoint.method, endpoint.path, identityWithScopes("seam:ops:read", "seam:scopes:read-all", "seam:tailscale:key-create"))
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("caller endpoint %s returned %d on operator mux, want 404", controlPlaneDriftKey(endpoint), response.StatusCode)
		}
		_ = response.Body.Close()
	}

	document := servedControlPlaneOpenAPI(t, s)
	paths, ok := document["paths"].(map[string]interface{})
	if !ok {
		t.Fatal("served control-plane OpenAPI paths is not an object")
	}
	if got, want := len(paths), len(caller); got != want {
		t.Fatalf("served control-plane OpenAPI has %d paths, want %d (%v)", got, want, sortedDriftKeys(caller))
	}
	for _, endpoint := range caller {
		item := openAPIPathMethods(t, document, endpoint.path)
		method := strings.ToLower(endpoint.method)
		operation, ok := item[method].(map[string]interface{})
		if !ok {
			t.Errorf("served OpenAPI %s is missing %s operation", endpoint.path, endpoint.method)
			continue
		}
		if got, _ := operation["x-seam-listener"].(string); got != endpoint.listener {
			t.Errorf("served OpenAPI %s %s listener = %q, want %q", endpoint.method, endpoint.path, got, endpoint.listener)
		}
		if endpoint.authorization == "seam:tailscale:key-create" {
			scopes, _ := operation["x-required-scope"].([]interface{})
			if len(scopes) != 1 || scopes[0] != endpoint.authorization {
				t.Errorf("served OpenAPI %s %s scope = %v, want [%s]", endpoint.method, endpoint.path, scopes, endpoint.authorization)
			}
		} else if _, exists := operation["x-required-scope"]; exists {
			t.Errorf("served OpenAPI %s %s unexpectedly declares x-required-scope", endpoint.method, endpoint.path)
		}
		for methodName := range item {
			if methodName != method {
				t.Errorf("served OpenAPI %s has unexpected operation %q", endpoint.path, methodName)
			}
		}
	}
	for _, endpoint := range operator {
		if _, exists := paths[endpoint.path]; exists {
			t.Errorf("operator endpoint %s leaked into caller control-plane OpenAPI", endpoint.path)
		}
	}

	// These are registered control-plane surfaces but the liveness aliases are
	// intentionally outside the OpenAPI business-endpoint catalog.
	for _, path := range []string{controlPlaneDocsPath, "/_seam/health", "/_seam/healthz", "/_seam/readyz"} {
		response := serveMuxWithIdentity(s.callerMux, http.MethodPost, path, nil)
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("registered control-plane alias %s returned %d for POST, want 405", path, response.StatusCode)
		}
		_ = response.Body.Close()
	}

	for _, endpoint := range all {
		if !isReservedPath(endpoint.path) {
			t.Errorf("documented control-plane endpoint %s is not reserved", endpoint.path)
		}
	}
	for _, path := range []string{controlPlaneDocsPath, "/_seam/health", "/_seam/healthz", "/_seam/readyz"} {
		if !isReservedPath(path) {
			t.Errorf("control-plane health/documentation path %s is not reserved", path)
		}
	}
	for _, path := range []string{"/changes/1.2.3", "/changes/", "/api/v1/tailscale/ephemeral-key/child", "/health"} {
		if isReservedPath(path) {
			t.Errorf("non-endpoint path %s unexpectedly became reserved", path)
		}
	}
}

// TestControlPlaneHealthAliasesAndChangesSemantics covers the parts of the
// control-plane contract that are intentionally not ordinary OpenAPI business
// paths: liveness aliases and the exact /changes namespace/response rules.
func TestControlPlaneHealthAliasesAndChangesSemantics(t *testing.T) {
	s := newControlPlaneContractTestServer(t)
	for _, path := range []string{"/_seam/health", "/_seam/healthz"} {
		response := serveMuxWithIdentity(s.callerMux, http.MethodGet, path, nil)
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || string(body) != "OK" || response.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("health alias %s = status %d body %q cache %q, want 200/OK/no-store", path, response.StatusCode, body, response.Header.Get("Cache-Control"))
		}
	}
	ready := serveMuxWithIdentity(s.callerMux, http.MethodGet, "/_seam/readyz", nil)
	var readiness map[string]interface{}
	if err := json.NewDecoder(ready.Body).Decode(&readiness); err != nil {
		t.Errorf("decode /_seam/readyz response: %v", err)
	}
	_ = ready.Body.Close()
	if ready.StatusCode != http.StatusOK && ready.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/_seam/readyz status = %d, want 200 or 503", ready.StatusCode)
	}
	if ready.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("/_seam/readyz Cache-Control = %q, want no-store", ready.Header.Get("Cache-Control"))
	}
	if _, ok := readiness["ready"]; !ok {
		t.Errorf("/_seam/readyz body has no ready field: %v", readiness)
	}

	v1Hash, v2Hash := seedChangesRingBuffer(t, s)
	response := serveChangesRequest(t, s, http.MethodGet, "/changes?level=2&scope-since=scope-v1")
	if response.Code != http.StatusOK {
		t.Fatalf("/changes level 2 status = %d, body: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("/changes Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}
	changes := decodeChangesResponse(t, response)
	if changes.SinceSpec != v1Hash || changes.CurrentSpec != v2Hash || !changes.SinceKnown || changes.Query.Level != "2" {
		t.Errorf("/changes version contract = %+v, want since=%s current=%s known level=2", changes, v1Hash, v2Hash)
	}
	if changes.ScopeChanges == nil || changes.ScopeChanges.ChangeType != "unknown" {
		t.Errorf("/changes scope_changes = %+v, want unknown placeholder", changes.ScopeChanges)
	}

	unknown := serveChangesRequest(t, s, http.MethodGet, "/changes?since=evicted-version")
	unknownBody := decodeChangesResponse(t, unknown)
	if unknown.Code != http.StatusOK || unknownBody.SinceKnown || len(unknownBody.Routes) != 0 {
		t.Errorf("unknown /changes since response = status %d known=%v routes=%d, want 200/false/0", unknown.Code, unknownBody.SinceKnown, len(unknownBody.Routes))
	}
	invalid := serveChangesRequest(t, s, http.MethodGet, "/changes?level=3")
	if invalid.Code != http.StatusBadRequest {
		t.Errorf("invalid /changes level status = %d, want 400", invalid.Code)
	}

	subpath := serveChangesRequest(t, s, http.MethodGet, "/changes/1.2.3")
	if subpath.Code != http.StatusNotFound {
		t.Errorf("/changes/1.2.3 status = %d, want 404 (exact /changes reservation only)", subpath.Code)
	}

	apiDocBytes, err := os.ReadFile("../../docs/notes/control-plane-api-contracts.md")
	if err != nil {
		t.Fatalf("read API documentation for /changes semantics: %v", err)
	}
	apiDoc := string(apiDocBytes)
	for _, phrase := range []string{"unknown/evicted is `200` + `since_known:false`", "`scope-since` adds `scope_changes`", "`/changes/…` sub-paths are not migration endpoints"} {
		if !strings.Contains(apiDoc, phrase) {
			t.Errorf("API documentation lost /changes contract phrase %q", phrase)
		}
	}

	openAPI := servedControlPlaneOpenAPI(t, s)
	changesOperation := openAPIPathMethods(t, openAPI, "/changes")["get"].(map[string]interface{})
	parameters := changesOperation["parameters"].([]interface{})
	levelParameterFound := false
	for _, raw := range parameters {
		parameter := raw.(map[string]interface{})
		if parameter["name"] != "level" {
			continue
		}
		levelParameterFound = true
		schema := parameter["schema"].(map[string]interface{})
		enum := schema["enum"].([]interface{})
		if len(enum) != 2 || enum[0] != "1" || enum[1] != "2" {
			t.Errorf("OpenAPI /changes level enum = %v, want [1 2]", enum)
		}
	}
	if !levelParameterFound {
		t.Error("OpenAPI /changes is missing the level parameter")
	}
	components := openAPI["components"].(map[string]interface{})
	schemas := components["schemas"].(map[string]interface{})
	changesSchema := schemas["ChangesResponse"].(map[string]interface{})
	properties := changesSchema["properties"].(map[string]interface{})
	for _, property := range []string{"since_known", "routes", "route_count", "scope_changes"} {
		if _, ok := properties[property]; !ok {
			t.Errorf("OpenAPI ChangesResponse is missing documented property %q", property)
		}
	}

}
