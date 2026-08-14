package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestInvalidVersionReturns400WithHeaders tests that invalid version parameters
// return 400 status with version headers included in the error response.
// This validates that version headers are present on ALL responses, including errors.
func TestInvalidVersionReturns400WithHeaders(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	tests := []struct {
		name           string
		url            string
		expectedStatus int
	}{
		{
			name:           "/docs with invalid version",
			url:            "/docs?version=v1",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "/openapi.json with invalid version",
			url:            "/openapi.json?version=beta",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "/docs/route with invalid version",
			url:            "/docs/route?path=/openapi.json&method=GET&version=invalid",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			w := httptest.NewRecorder()

			s.callerMux.ServeHTTP(w, req)

			resp := w.Result()

			// Check status code
			if resp.StatusCode != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, resp.StatusCode)
			}

			// Check version headers are present on error response
			specVersion := resp.Header.Get("X-SEAM-Spec-Version")
			if specVersion == "" {
				t.Error("expected X-SEAM-Spec-Version header to be set on error response")
			}

			apiVersion := resp.Header.Get("X-SEAM-API-Version")
			if apiVersion != "_unversioned" {
				t.Errorf("expected X-SEAM-API-Version _unversioned, got %s", apiVersion)
			}

			// Decode response body
			var body map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Errorf("failed to decode response body: %v", err)
			}

			// Check error message mentions expected alphabet
			if body["expected_version"] != "_unversioned" {
				t.Errorf("expected error to mention expected_version=_unversioned, got %v", body["expected_version"])
			}

			// Check error message is clear
			if body["error"] != "invalid_version_parameter" {
				t.Errorf("expected error=invalid_version_parameter, got %v", body["error"])
			}

			message, ok := body["message"].(string)
			if !ok {
				t.Error("expected message to be a string")
			} else if message == "" {
				t.Error("expected message to be non-empty")
			}
		})
	}
}

// TestValidVersionAcceptedOnAllEndpoints tests that ?version=_unversioned
// is accepted on all endpoints.
func TestValidVersionAcceptedOnAllEndpoints(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)

	tests := []struct {
		name           string
		url            string
		expectedStatus int
	}{
		{
			name:           "/docs with version=_unversioned",
			url:            "/docs?version=_unversioned",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "/openapi.json with version=_unversioned",
			url:            "/openapi.json?version=_unversioned",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "/docs/route with version=_unversioned",
			url:            "/docs/route?path=/openapi.json&method=GET&version=_unversioned",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			w := httptest.NewRecorder()

			s.callerMux.ServeHTTP(w, req)

			resp := w.Result()

			// Check status code - should succeed with valid version
			if resp.StatusCode != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, resp.StatusCode)
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
		})
	}
}
