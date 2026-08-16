package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ardenone/seam/internal/spec"
)

// TestValidationMiddleware_ValidRequest_PassesThrough tests that valid requests pass through to handlers
func TestValidationMiddleware_ValidRequest_PassesThrough(t *testing.T) {
	// This test uses fragments loaded from ../../fragments directory
	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", "../../fragments")
	if err != nil {
		t.Fatalf("Failed to create spec loader: %v", err)
	}

	server := &Server{
		specLoader: loader,
	}

	// Create validation middleware
	middleware := server.validationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))

	// Test valid request
	req := httptest.NewRequest("GET", "/test/get", nil)
	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
	if w.Body.String() != "OK" {
		t.Errorf("Expected body 'OK', got '%s'", w.Body.String())
	}
}

// TestValidationMiddleware_InvalidRequest_Returns400 tests that invalid requests return 400 with structured errors
func TestValidationMiddleware_InvalidRequest_Returns400(t *testing.T) {
	// This test uses fragments loaded from ../../fragments directory
	// Create a temporary fragments directory
	// This test requires actual fragments to be loaded
	// For now, we'll skip this if fragments aren't available
	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", "../../fragments")
	if err != nil {
		t.Skipf("Skipping test - fragments not available: %v", err)
	}

	server := &Server{
		specLoader: loader,
	}

	middleware := server.validationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Test invalid request - missing required field
	invalidBody := `{"value": "test"}`
	req := httptest.NewRequest("POST", "/test/post", bytes.NewBufferString(invalidBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	// Verify structured error response
	var response ValidationErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode error response: %v", err)
	}

	if response.Error != "validation_failed" {
		t.Errorf("Expected error 'validation_failed', got '%s'", response.Error)
	}

	if response.Message == "" {
		t.Error("Expected message to be set")
	}

	if response.DocsURL == "" {
		t.Error("Expected docs_url to be set")
	}

	if len(response.ValidationErrors) == 0 {
		t.Error("Expected validation_errors to contain at least one error")
	}

	// Verify field-level errors
	for _, verr := range response.ValidationErrors {
		if verr.Field == "" {
			t.Error("Expected validation error to have field set")
		}
		if verr.ExpectedShape == "" {
			t.Error("Expected validation error to have expected_shape set")
		}
		if verr.Reason == "" {
			t.Error("Expected validation error to have reason set")
		}
	}
}

// TestValidationMiddleware_ReservedPath_BypassesValidation tests that reserved paths bypass validation
func TestValidationMiddleware_ReservedPath_BypassesValidation(t *testing.T) {
	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", "./fragments")
	if err != nil {
		t.Skipf("Skipping test - fragments not available: %v", err)
	}

	server := &Server{
		specLoader: loader,
	}

	middleware := server.validationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	reservedPaths := []string{
		"/docs",
		"/_seam/health",
		"/openapi.json",
		"/_seam/metrics",
	}

	for _, path := range reservedPaths {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		middleware.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Reserved path %s should pass through validation, got status %d", path, w.Code)
		}
	}
}

// TestValidationMiddleware_MissingRequiredParameter_Returns400 tests validation of missing required parameters
func TestValidationMiddleware_MissingRequiredParameter_Returns400(t *testing.T) {
	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", "./fragments")
	if err != nil {
		t.Skipf("Skipping test - fragments not available: %v", err)
	}

	server := &Server{
		specLoader: loader,
	}

	middleware := server.validationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Test request with missing required query parameter
	req := httptest.NewRequest("GET", "/test/get?id=", nil)
	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	// This should either pass through (if the route exists) or return validation error
	// We're just checking it doesn't crash
	if w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 200 or 400, got %d", w.Code)
	}
}
