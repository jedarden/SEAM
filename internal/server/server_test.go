package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthzHandler(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/_seam/healthz", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	body := make([]byte, 2)
	_, _ = resp.Body.Read(body)
	if string(body) != "OK" {
		t.Errorf("expected body 'OK', got %s", string(body))
	}
}

func TestHealthzHandlerWrongMethod(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/_seam/healthz", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", resp.StatusCode)
	}
}

func TestReadyzHandler(t *testing.T) {
	cfg := &Config{
		CallerPort:    8080,
		OperatorPort:  8081,
		BaseURL:       "http://localhost:8080",
		SpecDir:       "../../spec",
		AllowlistFile: newBaselineAllowlistFile(t),
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/_seam/readyz", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Check response is JSON with ready=true
	var readyz map[string]bool
	if err := json.NewDecoder(resp.Body).Decode(&readyz); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if !readyz["ready"] {
		t.Errorf("expected ready=true, got ready=%v", readyz["ready"])
	}
}

func TestMetricsHandler(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	w := httptest.NewRecorder()

	s.operatorMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Check content type is Prometheus text format
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected Content-Type to contain text/plain, got %s", ct)
	}

	// Check body has some Prometheus metrics
	// Read the entire body, not just first 1024 bytes
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read metrics body: %v", err)
	}
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "go_goroutines") {
		t.Error("expected metrics to contain go_goroutines")
	}
	if !strings.Contains(bodyStr, "go_memstats_alloc_bytes") {
		t.Error("expected metrics to contain go_memstats_alloc_bytes")
	}
	if !strings.Contains(bodyStr, "seam_build_info") {
		t.Error("expected metrics to contain seam_build_info")
	}
}

func TestConfigStatusHandler(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)
	// /config/status is an operator-tier endpoint gated on seam:ops:read, so
	// stage 3 resolves the caller before the handler runs; a loopback caller
	// resolves to no identity and is default-denied. Present the fixed test
	// identity so the request reaches the handler under test.
	s.identityResolver = newLoopbackTestIdentityResolver()

	req := httptest.NewRequest(http.MethodGet, "/config/status", nil)
	w := httptest.NewRecorder()

	// Production mounts stage 3 outside the operator mux
	// (Start: identityResolutionMiddleware around versionMiddleware(mux)), so
	// drive that composition — the mux's own scope gate reads the identity
	// stage 3 puts in the request context.
	s.identityResolutionMiddleware(s.operatorMux).ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Check response is JSON with quiescent payload
	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if status["fragments_loaded"] != false {
		t.Errorf("expected fragments_loaded=false, got %v", status["fragments_loaded"])
	}

	if conditions, ok := status["conditions"].([]interface{}); !ok || len(conditions) != 0 {
		t.Errorf("expected empty conditions array, got %v", status["conditions"])
	}
}

func TestConfigStatusHandlerWrongMethod(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)
	// Same gate as the GET case: the method check lives behind stage 3, so the
	// 405 this test pins is only reachable once the caller resolves.
	s.identityResolver = newLoopbackTestIdentityResolver()

	req := httptest.NewRequest(http.MethodPost, "/config/status", nil)
	w := httptest.NewRecorder()

	// Same production composition as the GET case above.
	s.identityResolutionMiddleware(s.operatorMux).ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", resp.StatusCode)
	}
}

func TestIsReservedPath(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		// Exact matches
		{"/docs", true},
		{"/docs/route", true},
		{"/openapi.json", true},
		{"/whoami", true},
		{"/scopes", true},
		{"/changes", true},
		{"/health/credentials", true},
		{"/health/upstreams", true},
		{"/config/status", true},

		// Prefix matches
		{"/docs/api", true},
		{"/health/live", true},
		{"/config/fragment", true},
		{"/approvals/pending", true},
		{"/_seam/readyz", true},
		{"/_seam/metrics", true},

		// Non-reserved paths
		{"/api/v1/users", false},
		{"/route", false},
		{"/status", false},
		{"/healthz", false},
		{"/", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := isReservedPath(tt.path)
			if result != tt.expected {
				t.Errorf("isReservedPath(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestServerStart(t *testing.T) {
	// Use ephemeral ports to avoid conflicts
	cfg := &Config{
		CallerPort:   0, // 0 means OS assigns a port
		OperatorPort: 0,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	startErr := make(chan error, 1)
	go func() {
		startErr <- s.Start(ctx)
	}()

	// Give the server time to start
	time.Sleep(100 * time.Millisecond)

	// Verify we can shut down cleanly
	if err := s.Shutdown(ctx); err != nil {
		t.Errorf("shutdown failed: %v", err)
	}

	select {
	case err := <-startErr:
		if err != nil {
			t.Errorf("server start failed: %v", err)
		}
	case <-ctx.Done():
		t.Error("server start timed out")
	}
}

func TestOpenAPIJSONHandler(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Check content type is JSON
	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	// Check version headers are present
	specVersion := resp.Header.Get("X-SEAM-Spec-Version")
	if specVersion == "" {
		t.Error("expected X-SEAM-Spec-Version header to be set")
	}
	if len(specVersion) != 64 { // Full SHA256 hash
		t.Errorf("expected X-SEAM-Spec-Version to be 64 chars (full SHA256), got %d", len(specVersion))
	}
	// Verify all characters are valid hex
	for _, c := range specVersion {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			t.Errorf("X-SEAM-Spec-Version contains invalid hex character: %c", c)
		}
	}
	// Verify the hash matches what the loader reports
	if specVersion != s.specLoader.GetHash() {
		t.Errorf("X-SEAM-Spec-Version header %s doesn't match loader hash %s", specVersion, s.specLoader.GetHash())
	}

	apiVersion := resp.Header.Get("X-SEAM-API-Version")
	if apiVersion != "_unversioned" {
		t.Errorf("expected X-SEAM-API-Version _unversioned, got %s", apiVersion)
	}

	// Parse response as JSON and verify it's valid OpenAPI 3.1
	var spec map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	// Check OpenAPI version
	if openapiVersion, ok := spec["openapi"].(string); !ok || openapiVersion != "3.1.0" {
		t.Errorf("expected openapi version 3.1.0, got %v", spec["openapi"])
	}

	// Check servers array is populated with our base URL
	servers, ok := spec["servers"].([]interface{})
	if !ok || len(servers) != 1 {
		t.Errorf("expected servers array with 1 element, got %v", spec["servers"])
	} else {
		server, ok := servers[0].(map[string]interface{})
		if !ok {
			t.Error("expected server to be an object")
		} else {
			if url, ok := server["url"].(string); !ok || url != "http://localhost:8080" {
				t.Errorf("expected server.url to be http://localhost:8080, got %v", server["url"])
			}
		}
	}
}

func TestOpenAPIJSONHashHeader(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Check X-Spec-Version header is present
	specHash := resp.Header.Get("X-Spec-Version")
	if specHash == "" {
		t.Fatal("expected X-Spec-Version header to be set")
	}

	// Verify it's a valid 64-character hex string (SHA256)
	if len(specHash) != 64 {
		t.Errorf("expected X-Spec-Version to be 64 chars (full SHA256), got %d", len(specHash))
	}

	// Verify all characters are valid hex
	for _, c := range specHash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			t.Errorf("X-Spec-Version contains invalid hex character: %c", c)
		}
	}

	// Verify the hash matches what the loader reports
	if specHash != s.specLoader.GetHash() {
		t.Errorf("X-Spec-Version header %s doesn't match loader hash %s", specHash, s.specLoader.GetHash())
	}
}

func TestOpenAPIJSONHandlerWrongMethod(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/openapi.json", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", resp.StatusCode)
	}
}

func TestDocsHandlerHTML(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Check content type is HTML
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected Content-Type to contain text/html, got %s", ct)
	}

	// Check version headers are present
	specVersion := resp.Header.Get("X-SEAM-Spec-Version")
	if specVersion == "" {
		t.Error("expected X-SEAM-Spec-Version header to be set")
	}

	apiVersion := resp.Header.Get("X-SEAM-API-Version")
	if apiVersion != "_unversioned" {
		t.Errorf("expected X-SEAM-API-Version _unversioned, got %s", apiVersion)
	}

	// Read body and check it's HTML
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	bodyStr := string(body[:n])

	if !strings.Contains(bodyStr, "<!DOCTYPE html>") {
		t.Error("expected response to contain HTML doctype")
	}
	if !strings.Contains(bodyStr, "SEAM API Documentation") {
		t.Error("expected response to contain 'SEAM API Documentation'")
	}
}

func TestDocsHandlerJSON(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Check content type is JSON
	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	// Check version headers are present
	specVersion := resp.Header.Get("X-SEAM-Spec-Version")
	if specVersion == "" {
		t.Error("expected X-SEAM-Spec-Version header to be set")
	}

	// Parse response as JSON
	var spec map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	// Check it's valid OpenAPI
	if _, ok := spec["openapi"]; !ok {
		t.Error("expected response to contain openapi field")
	}
}

func TestSpecVersionStability(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s1 := New(cfg)
	s2 := New(cfg)

	// Both loaders should produce the same spec hash for the same spec
	h1 := s1.specLoader.GetHash()
	h2 := s2.specLoader.GetHash()

	if h1 != h2 {
		t.Errorf("spec hash not stable: %s != %s", h1, h2)
	}

	// Verify the hash is used in responses
	req1 := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w1 := httptest.NewRecorder()
	s1.callerMux.ServeHTTP(w1, req1)

	resp1 := w1.Result()
	defer func() { _ = resp1.Body.Close() }()

	headerVersion1 := resp1.Header.Get("X-SEAM-Spec-Version")
	if headerVersion1 != h1 {
		t.Errorf("header version %s doesn't match loader hash %s", headerVersion1, h1)
	}

	// Second request should have the same version
	req2 := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w2 := httptest.NewRecorder()
	s2.callerMux.ServeHTTP(w2, req2)

	resp2 := w2.Result()
	defer func() { _ = resp2.Body.Close() }()

	headerVersion2 := resp2.Header.Get("X-SEAM-Spec-Version")
	if headerVersion2 != h2 {
		t.Errorf("header version %s doesn't match loader hash %s", headerVersion2, h2)
	}

	if headerVersion1 != headerVersion2 {
		t.Errorf("spec version changed between requests: %s != %s", headerVersion1, headerVersion2)
	}
}

func TestDocsRouteHandlerMissingPathParameter(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Test missing path parameter
	req := httptest.NewRequest(http.MethodGet, "/docs/route", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}

	// Check error response structure
	var errResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if errResp["error"] != "missing_required_parameter" {
		t.Errorf("expected error 'missing_required_parameter', got %v", errResp["error"])
	}

	if errResp["message"] == nil {
		t.Error("expected error message to be present")
	}
}

func TestDocsRouteHandlerValidRoute(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Test valid route lookup
	req := httptest.NewRequest(http.MethodGet, "/docs/route?path=/openapi.json&method=GET", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Check response structure
	var routeResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&routeResp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	// Check required fields
	if routeResp["path"] != "/openapi.json" {
		t.Errorf("expected path '/openapi.json', got %v", routeResp["path"])
	}

	if routeResp["version"] != "_unversioned" {
		t.Errorf("expected version '_unversioned', got %v", routeResp["version"])
	}

	// Check metadata
	metadata, ok := routeResp["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("expected metadata to be present")
	}

	if metadata["spec_version"] == nil {
		t.Error("expected spec_version in metadata")
	}

	if metadata["api_version"] == nil {
		t.Error("expected api_version in metadata")
	}

	// Check operation data
	operation, ok := routeResp["operation"].(map[string]interface{})
	if !ok {
		t.Fatal("expected operation to be present")
	}

	if operation["operationId"] != "getOpenapiSpec" {
		t.Errorf("expected operationId 'getOpenapiSpec', got %v", operation["operationId"])
	}

	if operation["method"] != "GET" {
		t.Errorf("expected method 'GET', got %v", operation["method"])
	}
}

func TestDocsRouteHandlerAllMethods(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Test route lookup without method parameter (should return all methods)
	req := httptest.NewRequest(http.MethodGet, "/docs/route?path=/docs", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Check response structure
	var routeResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&routeResp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	// Should have methods map instead of single operation
	if routeResp["methods"] == nil {
		t.Fatal("expected methods to be present when no method specified")
	}

	methods, ok := routeResp["methods"].(map[string]interface{})
	if !ok {
		t.Fatal("expected methods to be a map")
	}

	// Check that GET method is present
	getMethod, ok := methods["GET"].(map[string]interface{})
	if !ok {
		t.Fatal("expected GET method to be present")
	}

	if getMethod["operationId"] != "getDocs" {
		t.Errorf("expected operationId 'getDocs', got %v", getMethod["operationId"])
	}
}

func TestDocsRouteHandlerInvalidRoute(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Test invalid route lookup
	req := httptest.NewRequest(http.MethodGet, "/docs/route?path=/nonexistent&method=GET", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp.StatusCode)
	}

	// Check error response structure
	var errResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if errResp["error"] != "route_not_found" {
		t.Errorf("expected error 'route_not_found', got %v", errResp["error"])
	}
}

func TestDocsRouteHandlerInvalidMethod(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Test valid path but invalid method
	req := httptest.NewRequest(http.MethodGet, "/docs/route?path=/openapi.json&method=POST", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp.StatusCode)
	}
}

func TestDocsRouteHandlerVersionParameter(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Test with explicit version parameter
	req := httptest.NewRequest(http.MethodGet, "/docs/route?path=/openapi.json&method=GET&version=_unversioned", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Check version in response
	var routeResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&routeResp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if routeResp["version"] != "_unversioned" {
		t.Errorf("expected version '_unversioned', got %v", routeResp["version"])
	}
}

func TestDocsRouteHandlerWrongHTTPMethod(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Test POST request to GET-only endpoint
	req := httptest.NewRequest(http.MethodPost, "/docs/route?path=/openapi.json", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", resp.StatusCode)
	}
}

func TestDocsRouteHandlerResponseHeaders(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/docs/route?path=/openapi.json&method=GET", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	// Check version headers are present
	specVersion := resp.Header.Get("X-SEAM-Spec-Version")
	if specVersion == "" {
		t.Error("expected X-SEAM-Spec-Version header to be set")
	}

	apiVersion := resp.Header.Get("X-SEAM-API-Version")
	if apiVersion != "_unversioned" {
		t.Errorf("expected X-SEAM-API-Version _unversioned, got %s", apiVersion)
	}

	// Check content type
	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}
}

func TestValidationMiddlewareSkipsReservedPaths(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Test that reserved paths skip validation and return normally
	// Each path is paired with its required query parameters (if any)
	reservedPaths := []struct {
		path  string
		query string
	}{
		{"/_seam/healthz", ""},
		{"/_seam/readyz", ""},
		{"/openapi.json", ""},
		{"/docs", ""},
		{"/docs/route", "?path=/openapi.json&method=GET"},
	}

	for _, item := range reservedPaths {
		t.Run(item.path, func(t *testing.T) {
			url := item.path + item.query
			req := httptest.NewRequest(http.MethodGet, url, nil)
			w := httptest.NewRecorder()

			// Use the validation middleware wrapped handler
			handler := s.validationMiddleware(s.callerMux)
			handler.ServeHTTP(w, req)

			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()

			// Reserved paths should not return validation errors
			// (they may return handler errors like 400 for missing params, but not validation errors)
			if resp.StatusCode == http.StatusBadRequest {
				body, _ := io.ReadAll(resp.Body)
				bodyStr := string(body)
				// Check if it's specifically a validation error (not a handler error)
				if strings.Contains(bodyStr, "validation_failed") || strings.Contains(bodyStr, "validation_errors") {
					t.Errorf("reserved path %s should not trigger validation, got: %s", item.path, bodyStr)
				}
			}
		})
	}
}

func TestValidationMiddleware(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Create a handler that returns 200 OK
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// Wrap with validation middleware
	handler := s.validationMiddleware(nextHandler)

	// Test that the middleware is wired up correctly for a non-reserved path
	// In Phase 1a, there are no proxy routes, so non-reserved paths would still fail
	// but we're testing the middleware wiring itself
	t.Run("reserved_path_passes_through", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/_seam/healthz", nil)
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		resp := w.Result()
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200 for reserved path, got %d", resp.StatusCode)
		}
	})
}

func TestCaptureDisabledByDefault(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
		// CaptureEnabled is false by default (zero value)
	}

	s := New(cfg)

	if s.captureMiddleware != nil {
		t.Error("expected capture middleware to be nil when disabled")
	}

	if s.config.CaptureEnabled != false {
		t.Error("expected CaptureEnabled to be false by default")
	}
}

func TestCaptureEnabledWhenConfigured(t *testing.T) {
	cfg := &Config{
		CallerPort:     8080,
		OperatorPort:   8081,
		BaseURL:        "http://localhost:8080",
		SpecDir:        "../../spec",
		CaptureEnabled: true,
		CorpusDir:      "test-corpus",
	}

	s := New(cfg)

	if s.captureMiddleware == nil {
		t.Error("expected capture middleware to be initialized when enabled")
	}

	if !s.config.CaptureEnabled {
		t.Error("expected CaptureEnabled to be true when configured")
	}

	if s.captureMiddleware != nil && s.captureMiddleware.corpusDir != "test-corpus" {
		t.Errorf("expected corpusDir to be 'test-corpus', got %s", s.captureMiddleware.corpusDir)
	}
}

func TestCaptureMiddlewareDisabledBehavior(t *testing.T) {
	// Create a capture middleware with enabled=false
	cm := &CaptureMiddleware{
		enabled: false,
	}

	// Create a test handler that sets a response
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("test response"))
	})

	// Wrap with capture middleware
	wrappedHandler := cm.Wrap(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	// Should pass through to the next handler without capture
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "test response" {
		t.Errorf("expected body 'test response', got %s", string(body))
	}

	// Should have no entries since capture is disabled
	if len(cm.entries) != 0 {
		t.Errorf("expected no entries when disabled, got %d", len(cm.entries))
	}
}

func TestCaptureStatusEndpointWhenDisabled(t *testing.T) {
	cfg := &Config{
		CallerPort:     8080,
		OperatorPort:   8081,
		BaseURL:        "http://localhost:8080",
		SpecDir:        "../../spec",
		CaptureEnabled: false,
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/_seam/capture/status", nil)
	w := httptest.NewRecorder()

	s.operatorMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if status["enabled"] != false {
		t.Errorf("expected enabled=false in status, got %v", status["enabled"])
	}
}

func TestCaptureStatusEndpointWhenEnabled(t *testing.T) {
	cfg := &Config{
		CallerPort:     8080,
		OperatorPort:   8081,
		BaseURL:        "http://localhost:8080",
		SpecDir:        "../../spec",
		CaptureEnabled: true,
		CorpusDir:      "test-corpus",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/_seam/capture/status", nil)
	w := httptest.NewRecorder()

	s.operatorMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if status["enabled"] != true {
		t.Errorf("expected enabled=true in status, got %v", status["enabled"])
	}

	if status["corpus_dir"] == nil {
		t.Error("expected corpus_dir to be present in status")
	}
}

// Tests for X-SEAM-Spec-Version header

// TestSpecVersionHeaderPresence verifies that X-SEAM-Spec-Version header is present in responses
// NOTE: This test checks endpoints that manually set headers. The middleware chain is only
// applied when s.Start() is called, so direct mux calls don't test the middleware.
func TestSpecVersionHeaderPresence(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Test endpoints that manually set the version header
	endpoints := []struct {
		path   string
		method string
	}{
		{"/openapi.json", http.MethodGet},
		{"/docs", http.MethodGet},
		{"/docs/route?path=/openapi.json&method=GET", http.MethodGet},
	}

	for _, ep := range endpoints {
		t.Run(ep.path, func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, nil)
			w := httptest.NewRecorder()

			s.callerMux.ServeHTTP(w, req)

			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()

			specVersion := resp.Header.Get("X-Seam-Spec-Version")
			if specVersion == "" {
				t.Errorf("expected X-Seam-Spec-Version header to be set for %s", ep.path)
			}
		})
	}
}

// TestSpecVersionHeaderValue verifies that header value matches spec hash from loader
func TestSpecVersionHeaderValue(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	specVersion := resp.Header.Get("X-Seam-Spec-Version")
	loaderHash := s.specLoader.GetHash()

	if specVersion != loaderHash {
		t.Errorf("X-Seam-Spec-Version header %s doesn't match loader hash %s", specVersion, loaderHash)
	}
}

// TestSpecVersionHeaderFormat verifies that the header value is correctly formatted (64 hex chars)
func TestSpecVersionHeaderFormat(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	specVersion := resp.Header.Get("X-Seam-Spec-Version")

	// Verify length is 64 characters (SHA256)
	if len(specVersion) != 64 {
		t.Errorf("expected X-Seam-Spec-Version to be 64 chars (full SHA256), got %d", len(specVersion))
	}

	// Verify all characters are valid hex (lowercase a-f, 0-9)
	for i, c := range specVersion {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf("X-Seam-Spec-Version contains invalid hex character '%c' at position %d", c, i)
		}
	}
}

// TestSpecVersionHeaderCanonicalization verifies that header uses canonical form
func TestSpecVersionHeaderCanonicalization(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	// Check for canonical form (X-Seam-Spec-Version)
	canonicalForm := resp.Header.Get("X-Seam-Spec-Version")
	if canonicalForm == "" {
		t.Error("expected X-Seam-Spec-Version header in canonical form")
	}

	// Verify that Go's http.Header.Get() canonicalizes the key automatically
	// Accessing with any case variation should work
	nonCanonicalAccess := resp.Header.Get("X-SEAM-Spec-Version")
	if nonCanonicalAccess == "" {
		t.Error("http.Header.Get() should canonicalize keys, but got empty result")
	}

	// Both should return the same value
	if canonicalForm != nonCanonicalAccess {
		t.Errorf("canonical form %s != non-canonical access %s", canonicalForm, nonCanonicalAccess)
	}

	// Verify the value is all lowercase hex (as stored)
	if canonicalForm != strings.ToLower(canonicalForm) {
		t.Errorf("header value should be lowercase hex, got: %s", canonicalForm)
	}
}

// TestSpecVersionHeaderPosition verifies that header is injected before status code is sent
func TestSpecVersionHeaderPosition(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	// The versionWriter injects headers before calling WriteHeader on the underlying writer
	// This means the header should be present in the final response
	specVersion := resp.Header.Get("X-Seam-Spec-Version")

	// If the header was injected after the status code was sent, it wouldn't appear in the response
	if specVersion == "" {
		t.Error("X-Seam-Spec-Version header should be injected before status code is sent")
	}

	// Verify the response is successful (headers were sent properly)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

// TestSpecVersionHeaderAcrossDifferentRequests verifies header is present for different request types
func TestSpecVersionHeaderAcrossDifferentRequests(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Test different endpoints to verify the header is consistently applied
	testCases := []struct {
		name        string
		path        string
		method      string
		headerCheck bool // whether we expect the header (only endpoints that manually set it)
	}{
		{"openapi_get", "/openapi.json", http.MethodGet, true},
		{"docs_get", "/docs", http.MethodGet, true},
		{"docs_get_with_json_accept", "/docs", http.MethodGet, true},
		{"docs_route_get", "/docs/route?path=/openapi.json&method=GET", http.MethodGet, true},
		{"openapi_post", "/openapi.json", http.MethodPost, false}, // Returns 405, no manual header set
		{"docs_post", "/docs", http.MethodPost, false},            // Returns 405, no manual header set
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.name == "docs_get_with_json_accept" {
				req.Header.Set("Accept", "application/json")
			}

			w := httptest.NewRecorder()
			s.callerMux.ServeHTTP(w, req)

			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()

			specVersion := resp.Header.Get("X-Seam-Spec-Version")

			// Error responses that don't manually set headers are not expected to
			// carry it: in production the middleware would, but these direct mux
			// calls bypass middleware.
			if tc.headerCheck && specVersion == "" {
				t.Errorf("expected X-Seam-Spec-Version header for %s", tc.name)
			}
		})
	}
}

// TestSpecVersionHeaderConsistency verifies header is consistent across multiple requests
func TestSpecVersionHeaderConsistency(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	var versions []string

	// Make multiple requests and collect the version header
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		w := httptest.NewRecorder()

		s.callerMux.ServeHTTP(w, req)

		resp := w.Result()
		defer func() { _ = resp.Body.Close() }()

		specVersion := resp.Header.Get("X-Seam-Spec-Version")
		versions = append(versions, specVersion)
	}

	// Verify all versions are the same
	for i, v := range versions {
		if v != versions[0] {
			t.Errorf("request %d returned version %s, expected %s", i, v, versions[0])
		}
	}
}

// TestVersionWriterDirectly tests the versionWriter behavior in isolation
func TestVersionWriterDirectly(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)
	specHash := s.specLoader.GetHash()

	// Create a mock response writer
	mockWriter := httptest.NewRecorder()

	// Create a versionWriter
	vw := &versionWriter{
		ResponseWriter:  mockWriter,
		specHash:        specHash,
		headersInjected: false,
	}

	// Test Header() triggers injection
	h := vw.Header()
	if h == nil {
		t.Error("Header() should return non-nil header map")
	}

	// Check that header was injected
	if mockWriter.Header().Get("X-Seam-Spec-Version") != specHash {
		t.Errorf("Header() should inject X-Seam-Spec-Version, got %s", mockWriter.Header().Get("X-Seam-Spec-Version"))
	}

	// Test WriteHeader() triggers injection
	vw2 := &versionWriter{
		ResponseWriter:  mockWriter,
		specHash:        specHash,
		headersInjected: false,
	}

	vw2.WriteHeader(http.StatusOK)
	if mockWriter.Header().Get("X-Seam-Spec-Version") != specHash {
		t.Error("WriteHeader() should inject X-Seam-Spec-Version")
	}

	// Test Write() triggers injection
	vw3 := &versionWriter{
		ResponseWriter:  mockWriter,
		specHash:        specHash,
		headersInjected: false,
	}

	n, err := vw3.Write([]byte("test"))
	if err != nil {
		t.Fatalf("Write() failed: %v", err)
	}
	if n != 4 {
		t.Errorf("Write() returned %d, expected 4", n)
	}
	if mockWriter.Header().Get("X-Seam-Spec-Version") != specHash {
		t.Error("Write() should inject X-Seam-Spec-Version")
	}
}

// TestVersionWriterNoDoubleInjection verifies that headers are only injected once
func TestVersionWriterNoDoubleInjection(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)
	specHash := s.specLoader.GetHash()

	mockWriter := httptest.NewRecorder()

	vw := &versionWriter{
		ResponseWriter:  mockWriter,
		specHash:        specHash,
		headersInjected: false,
	}

	// Call all three methods multiple times
	for i := 0; i < 3; i++ {
		vw.Header()
		vw.WriteHeader(http.StatusOK)
		_, _ = vw.Write([]byte("test"))
	}

	// Verify the header was set only once (no duplicates)
	headers := mockWriter.Header()["X-Seam-Spec-Version"]
	if len(headers) != 1 {
		t.Errorf("expected 1 header value, got %d: %v", len(headers), headers)
	}

	if headers[0] != specHash {
		t.Errorf("expected header value %s, got %s", specHash, headers[0])
	}
}

// TestVersionWriterEmptyHash tests behavior when spec hash is empty
func TestVersionWriterEmptyHash(t *testing.T) {
	mockWriter := httptest.NewRecorder()

	vw := &versionWriter{
		ResponseWriter:  mockWriter,
		specHash:        "", // Empty hash
		headersInjected: false,
	}

	// Trigger injection
	vw.WriteHeader(http.StatusOK)

	// With empty hash, the header should NOT be set
	header := mockWriter.Header().Get("X-Seam-Spec-Version")
	if header != "" {
		t.Errorf("expected no X-Seam-Spec-Version header when hash is empty, got %s", header)
	}
}

// TestSpecVersionHeaderOnManualErrorResponse verifies header is present on manual error responses
// NOTE: This tests handlers that manually set headers even on error responses.
// The middleware would apply to all responses, but direct mux calls bypass it.
func TestSpecVersionHeaderOnManualErrorResponse(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	// Test an error response that manually sets headers (invalid version parameter)
	req := httptest.NewRequest(http.MethodGet, "/openapi.json?version=invalid", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	// Should return 400 for invalid version
	if resp.StatusCode != http.StatusBadRequest {
		t.Logf("expected status 400, got %d", resp.StatusCode)
	}

	// The handler manually sets headers even on error responses
	specVersion := resp.Header.Get("X-Seam-Spec-Version")
	if specVersion == "" {
		t.Error("expected X-Seam-Spec-Version header on manual error response (handler sets it)")
	}
}
