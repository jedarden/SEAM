package server

import (
	"context"
	"net/http"
	"net/http/httptest"
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
