package server

import (
	"context"
	"encoding/json"
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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", resp.StatusCode)
	}
}

func TestReadyzHandler(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/_seam/readyz", nil)
	w := httptest.NewRecorder()

	s.callerMux.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Check content type is Prometheus text format
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected Content-Type to contain text/plain, got %s", ct)
	}

	// Check body has some Prometheus metrics
	body := make([]byte, 1024)
	n, _ := resp.Body.Read(body)
	bodyStr := string(body[:n])

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

	req := httptest.NewRequest(http.MethodGet, "/config/status", nil)
	w := httptest.NewRecorder()

	s.operatorMux.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

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

	req := httptest.NewRequest(http.MethodPost, "/config/status", nil)
	w := httptest.NewRecorder()

	s.operatorMux.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
	if len(specVersion) != 16 { // SHA256 first 16 hex chars
		t.Errorf("expected X-SEAM-Spec-Version to be 16 chars, got %d", len(specVersion))
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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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

	// Both loaders should produce the same spec version for the same spec
	v1 := s1.specLoader.GetVersion()
	v2 := s2.specLoader.GetVersion()

	if v1 != v2 {
		t.Errorf("spec version not stable: %s != %s", v1, v2)
	}

	// Verify the version is used in responses
	req1 := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w1 := httptest.NewRecorder()
	s1.callerMux.ServeHTTP(w1, req1)

	resp1 := w1.Result()
	defer resp1.Body.Close()

	headerVersion1 := resp1.Header.Get("X-SEAM-Spec-Version")
	if headerVersion1 != v1 {
		t.Errorf("header version %s doesn't match loader version %s", headerVersion1, v1)
	}

	// Second request should have the same version
	req2 := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w2 := httptest.NewRecorder()
	s2.callerMux.ServeHTTP(w2, req2)

	resp2 := w2.Result()
	defer resp2.Body.Close()

	headerVersion2 := resp2.Header.Get("X-SEAM-Spec-Version")
	if headerVersion2 != v2 {
		t.Errorf("header version %s doesn't match loader version %s", headerVersion2, v2)
	}

	if headerVersion1 != headerVersion2 {
		t.Errorf("spec version changed between requests: %s != %s", headerVersion1, headerVersion2)
	}
}
