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
		SpecDir:      "./spec",
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
		SpecDir:      "./spec",
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
		SpecDir:      "./spec",
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
		SpecDir:      "./spec",
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
		SpecDir:      "./spec",
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
		SpecDir:      "./spec",
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
		SpecDir:      "./spec",
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
