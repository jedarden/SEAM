package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestVersionInjectionMiddleware verifies that the version injection middleware
// adds the correct headers to all responses.
func TestVersionInjectionMiddleware(t *testing.T) {
	// Create a minimal config
	cfg := &Config{
		CallerPort:   8888,
		OperatorPort: 8889,
		BaseURL:      "http://localhost:8888",
		SpecDir:      "../../spec",
		FragmentMode: false,
	}

	// Create server (this loads the spec and computes the hash)
	s := New(cfg)

	// Create a test handler that returns a simple response
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Wrap with version injection middleware
	handler := s.versionInjectionMiddleware(testHandler)

	// Create a test request
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	// Serve the request
	handler.ServeHTTP(w, req)

	// Check the response headers
	resp := w.Result()
	defer resp.Body.Close()

	// Verify X-SEAM-Spec-Version header is present
	specVersion := resp.Header.Get("X-Seam-Spec-Version")
	if specVersion == "" {
		t.Errorf("X-Seam-Spec-Version header not found")
	} else {
		t.Logf("X-Seam-Spec-Version: %s", specVersion)
		// Verify it's 64 characters (full SHA256 hash)
		if len(specVersion) != 64 {
			t.Errorf("X-Seam-Spec-Version should be 64 characters (full SHA256 hash), got %d", len(specVersion))
		}
	}

	// Verify X-SEAM-API-Version header is NOT present (comes in next task)
	apiVersion := resp.Header.Get("X-Seam-Api-Version")
	if apiVersion != "" {
		t.Errorf("X-Seam-Api-Version should not be present yet, got '%s'", apiVersion)
	}

	// Verify the response body is still correct
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}
	if string(body) != "OK" {
		t.Errorf("Response body should be 'OK', got '%s'", string(body))
	}
}

// TestVersionInjectionMiddlewareWithStatusCode verifies that headers are added
// even when the handler explicitly calls WriteHeader.
func TestVersionInjectionMiddlewareWithStatusCode(t *testing.T) {
	cfg := &Config{
		CallerPort:   8888,
		OperatorPort: 8889,
		BaseURL:      "http://localhost:8888",
		SpecDir:      "../../spec",
		FragmentMode: false,
	}

	s := New(cfg)

	// Handler that explicitly sets a status code
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("Created"))
	})

	handler := s.versionInjectionMiddleware(testHandler)
	req := httptest.NewRequest("POST", "/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	// Check headers are present
	specVersion := resp.Header.Get("X-Seam-Spec-Version")
	if specVersion == "" {
		t.Errorf("X-Seam-Spec-Version header not found with WriteHeader")
	}

	// Verify X-SEAM-API-Version header is NOT present (comes in next task)
	apiVersion := resp.Header.Get("X-Seam-Api-Version")
	if apiVersion != "" {
		t.Errorf("X-Seam-Api-Version should not be present yet, got '%s'", apiVersion)
	}

	// Verify status code is preserved
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("Status code should be 201, got %d", resp.StatusCode)
	}
}

// TestVersionInjectionMiddlewareHealthEndpoints verifies that headers are added
// to health endpoints (reserved paths).
func TestVersionInjectionMiddlewareHealthEndpoints(t *testing.T) {
	cfg := &Config{
		CallerPort:   8888,
		OperatorPort: 8889,
		BaseURL:      "http://localhost:8888",
		SpecDir:      "../../spec",
		FragmentMode: false,
	}

	s := New(cfg)

	// Wrap the health handler directly with version middleware
	healthHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := s.versionInjectionMiddleware(healthHandler)

	// Test /_seam/health endpoint
	req := httptest.NewRequest("GET", "/_seam/health", nil)
	w := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	// Check headers are present on health endpoint
	specVersion := resp.Header.Get("X-Seam-Spec-Version")
	if specVersion == "" {
		t.Errorf("X-Seam-Spec-Version header not found on health endpoint")
	}

	// Verify X-SEAM-API-Version header is NOT present on health endpoint (comes in next task)
	apiVersion := resp.Header.Get("X-Seam-Api-Version")
	if apiVersion != "" {
		t.Errorf("X-Seam-Api-Version should not be present yet on health endpoint, got '%s'", apiVersion)
	}

	t.Logf("Health endpoint response: status=%d, specVersion=%s", resp.StatusCode, specVersion)
}
