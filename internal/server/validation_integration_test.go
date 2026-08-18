package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ardenone/seam/internal/spec"
)

// TestValidationIntegration_ValidRequest_PassesThrough verifies that valid requests conforming to the spec pass through
func TestValidationIntegration_ValidRequest_PassesThrough(t *testing.T) {
	// Use test fragments from /home/coding/SEAM/fragments
	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", "../../fragments")
	if err != nil {
		t.Fatalf("Failed to create spec loader: %v", err)
	}

	server := &Server{
		specLoader: loader,
	}

	// Create validation middleware with a final handler that returns 200
	middleware := server.validationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{
			name:       "Valid GET request to /test/get",
			method:     http.MethodGet,
			path:       "/test/get?id=test123",
			wantStatus: http.StatusOK,
		},
		{
			name:       "Valid GET request to /test/{id}",
			method:     http.MethodGet,
			path:       "/test/abc123",
			wantStatus: http.StatusOK,
		},
		{
			name:       "Valid POST request to /test/post with required field",
			method:     http.MethodPost,
			path:       "/test/post",
			body:       `{"name": "test", "value": 42}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "Valid POST request with only required field",
			method:     http.MethodPost,
			path:       "/test/post",
			body:       `{"name": "test"}`,
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodyReader *bytes.Reader
			if tt.body != "" {
				bodyReader = bytes.NewReader([]byte(tt.body))
			} else {
				bodyReader = bytes.NewReader(nil)
			}

			req := httptest.NewRequest(tt.method, tt.path, bodyReader)
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()

			middleware.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("Expected status %d, got %d", tt.wantStatus, w.Code)
			}

			if tt.wantStatus == http.StatusOK && w.Body.String() != "OK" {
				t.Errorf("Expected body 'OK', got '%s'", w.Body.String())
			}
		})
	}
}

// TestValidationIntegration_MalformedRequest_Returns400 verifies that malformed requests return structured 400 responses
func TestValidationIntegration_MalformedRequest_Returns400(t *testing.T) {
	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", "../../fragments")
	if err != nil {
		t.Fatalf("Failed to create spec loader: %v", err)
	}

	server := &Server{
		specLoader: loader,
	}

	middleware := server.validationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name             string
		method           string
		path             string
		body             string
		wantStatus       int
		wantErrorField   string
		wantReasonSubstr string
	}{
		{
			name:             "POST with missing required field 'name'",
			method:           http.MethodPost,
			path:             "/test/post",
			body:             `{"value": 42}`,
			wantStatus:       http.StatusBadRequest,
			wantErrorField:   "/test/post",
			wantReasonSubstr: "schema",
		},
		{
			name:             "POST with invalid JSON",
			method:           http.MethodPost,
			path:             "/test/post",
			body:             `{"name": "test", invalid json}`,
			wantStatus:       http.StatusBadRequest,
			wantReasonSubstr: "json",
		},
		{
			name:             "POST with wrong type for value field",
			method:           http.MethodPost,
			path:             "/test/post",
			body:             `{"name": "test", "value": "not an integer"}`,
			wantStatus:       http.StatusBadRequest,
			wantErrorField:   "/test/post",
			wantReasonSubstr: "schema",
		},
		{
			name:             "POST with empty body when required",
			method:           http.MethodPost,
			path:             "/test/post",
			body:             "",
			wantStatus:       http.StatusBadRequest,
			wantReasonSubstr: "required",
		},
		{
			name:       "POST without Content-Type header",
			method:     http.MethodPost,
			path:       "/test/post",
			body:       `{"name": "test"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodyReader *bytes.Reader
			if tt.body != "" {
				bodyReader = bytes.NewReader([]byte(tt.body))
			} else {
				bodyReader = bytes.NewReader(nil)
			}

			req := httptest.NewRequest(tt.method, tt.path, bodyReader)
			if tt.body != "" && tt.name != "POST without Content-Type header" {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()

			middleware.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("Expected status %d, got %d. Body: %s", tt.wantStatus, w.Code, w.Body.String())
			}

			// Verify structured error response for 400s
			if tt.wantStatus == http.StatusBadRequest {
				var response SpecValidationResponse
				if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
					t.Fatalf("Failed to decode error response: %v. Body: %s", err, w.Body.String())
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

				// Verify field-level errors if specified
				if tt.wantErrorField != "" {
					found := false
					for _, verr := range response.ValidationErrors {
						if strings.Contains(verr.Field, tt.wantErrorField) {
							found = true
							if tt.wantReasonSubstr != "" && !strings.Contains(strings.ToLower(verr.Reason), strings.ToLower(tt.wantReasonSubstr)) {
								t.Errorf("Expected reason to contain '%s', got '%s'", tt.wantReasonSubstr, verr.Reason)
							}
						}
					}
					if !found {
						t.Errorf("Expected to find error for field '%s', got errors: %+v", tt.wantErrorField, response.ValidationErrors)
					}
				}
			}
		})
	}
}

// TestValidationIntegration_ReservedPaths_BypassValidation verifies that reserved paths bypass validation
func TestValidationIntegration_ReservedPaths_BypassValidation(t *testing.T) {
	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", "../../fragments")
	if err != nil {
		t.Fatalf("Failed to create spec loader: %v", err)
	}

	server := &Server{
		specLoader: loader,
	}

	middleware := server.validationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("BYPASSED"))
	}))

	tests := []struct {
		name   string
		path   string
		method string
		body   string
	}{
		{
			name:   "/docs endpoint",
			path:   "/docs",
			method: http.MethodGet,
		},
		{
			name:   "/docs/route endpoint",
			path:   "/docs/route",
			method: http.MethodGet,
		},
		{
			name:   "/openapi.json endpoint",
			path:   "/openapi.json",
			method: http.MethodGet,
		},
		{
			name:   "/_seam/health endpoint",
			path:   "/_seam/health",
			method: http.MethodGet,
		},
		{
			name:   "/config/status endpoint",
			path:   "/config/status",
			method: http.MethodGet,
		},
		{
			name:   "/health/credentials endpoint",
			path:   "/health/credentials",
			method: http.MethodGet,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodyReader *bytes.Reader
			if tt.body != "" {
				bodyReader = bytes.NewReader([]byte(tt.body))
			} else {
				bodyReader = bytes.NewReader(nil)
			}

			req := httptest.NewRequest(tt.method, tt.path, bodyReader)
			w := httptest.NewRecorder()

			middleware.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Reserved path %s should bypass validation and return 200, got %d", tt.path, w.Code)
			}

			if w.Body.String() != "BYPASSED" {
				t.Errorf("Reserved path %s should bypass validation, got body: %s", tt.path, w.Body.String())
			}
		})
	}
}

// TestValidationIntegration_DocsRouteEndpoint_Verified verifies the /docs/route endpoint functionality
func TestValidationIntegration_DocsRouteEndpoint_Verified(t *testing.T) {
	// Create a minimal server with docs handler
	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", "../../fragments")
	if err != nil {
		t.Fatalf("Failed to create spec loader: %v", err)
	}

	server := &Server{
		specLoader: loader,
	}

	// Test /docs/route endpoint with various query parameters
	tests := []struct {
		name           string
		path           string
		wantStatus     int
		wantInResponse []string
	}{
		{
			name:           "GET /docs/route for existing path",
			path:           "/docs/route?path=/test/get&method=GET",
			wantStatus:     http.StatusOK,
			wantInResponse: []string{"/test/get", "GET", "testGet"},
		},
		{
			name:           "GET /docs/route for POST endpoint",
			path:           "/docs/route?path=/test/post&method=POST",
			wantStatus:     http.StatusOK,
			wantInResponse: []string{"/test/post", "POST", "testPost"},
		},
		{
			name:           "GET /docs/route for path with parameter",
			path:           "/docs/route?path=/test/{id}&method=GET",
			wantStatus:     http.StatusOK,
			wantInResponse: []string{"/test/{id}", "GET", "testGetById"},
		},
		{
			name:       "GET /docs/route for non-existent path (404)",
			path:       "/docs/route?path=/nonexistent&method=GET",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "GET /docs/route with missing path parameter (400)",
			path:       "/docs/route?method=GET",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:           "GET /docs/route with missing method parameter (200, returns all methods)",
			path:           "/docs/route?path=/test/{id}",
			wantStatus:     http.StatusOK,
			wantInResponse: []string{"GET", "DELETE"},
		},
		{
			name:           "GET /docs/route for all methods",
			path:           "/docs/route?path=/test/{id}",
			wantStatus:     http.StatusOK,
			wantInResponse: []string{"GET", "DELETE"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()

			server.docsRouteHandler(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("Expected status %d, got %d. Body: %s", tt.wantStatus, w.Code, w.Body.String())
			}

			// Verify response content for successful requests
			if tt.wantStatus == http.StatusOK && len(tt.wantInResponse) > 0 {
				body := w.Body.String()
				for _, expected := range tt.wantInResponse {
					if !strings.Contains(body, expected) {
						t.Errorf("Expected response to contain '%s', got: %s", expected, body)
					}
				}
			}
		})
	}
}

// TestValidationIntegration_ErrorResponseStructure verifies the structured error response format
func TestValidationIntegration_ErrorResponseStructure(t *testing.T) {
	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", "../../fragments")
	if err != nil {
		t.Fatalf("Failed to create spec loader: %v", err)
	}

	server := &Server{
		specLoader: loader,
	}

	middleware := server.validationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Test with a request missing the required 'name' field
	req := httptest.NewRequest(http.MethodPost, "/test/post", bytes.NewReader([]byte(`{"value": 42}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected status 400, got %d", w.Code)
	}

	// Verify response structure
	var response SpecValidationResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode error response: %v", err)
	}

	// Verify top-level fields
	if response.Error != "validation_failed" {
		t.Errorf("Expected error 'validation_failed', got '%s'", response.Error)
	}

	if response.Message == "" {
		t.Error("Expected message to be set")
	}

	if response.DocsURL == "" {
		t.Error("Expected docs_url to be set")
	}

	// Verify docs_url format
	expectedDocsPrefix := "/docs/route?path=/test/post&method=POST"
	if !strings.HasPrefix(response.DocsURL, expectedDocsPrefix) {
		t.Errorf("Expected docs_url to start with '%s', got '%s'", expectedDocsPrefix, response.DocsURL)
	}

	// Verify validation errors array
	if len(response.ValidationErrors) == 0 {
		t.Fatal("Expected validation_errors to contain at least one error")
	}

	// Verify individual validation error structure
	verr := response.ValidationErrors[0]
	if verr.Field == "" {
		t.Error("Expected validation error to have field set")
	}

	if verr.Reason == "" {
		t.Error("Expected validation error to have reason set")
	}

	if verr.ExpectedShape == "" {
		t.Error("Expected validation error to have expected_shape set")
	}

	// Verify that the error is about schema validation
	foundSchemaError := false
	for _, err := range response.ValidationErrors {
		// Check for schema-related errors (either field-specific or generic)
		if strings.Contains(strings.ToLower(err.Reason), "schema") ||
			strings.Contains(strings.ToLower(err.Reason), "required") ||
			strings.Contains(err.Field, "name") ||
			strings.Contains(err.Field, "/test/post") {
			foundSchemaError = true
			break
		}
	}

	if !foundSchemaError {
		t.Errorf("Expected to find schema validation error, got errors: %+v", response.ValidationErrors)
	}

	// Verify Content-Type header
	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("Expected Content-Type to contain 'application/json', got '%s'", contentType)
	}
}

// TestValidationIntegration_QueryParameterValidation verifies query parameter validation
func TestValidationIntegration_QueryParameterValidation(t *testing.T) {
	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", "../../fragments")
	if err != nil {
		t.Fatalf("Failed to create spec loader: %v", err)
	}

	server := &Server{
		specLoader: loader,
	}

	middleware := server.validationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{
			name:       "Valid query parameter",
			path:       "/test/get?id=test123",
			wantStatus: http.StatusOK,
		},
		{
			name:       "Optional query parameter omitted",
			path:       "/test/get",
			wantStatus: http.StatusOK,
		},
		{
			name:       "Multiple query parameters",
			path:       "/test/get?id=test123&extra=ignored",
			wantStatus: http.StatusOK, // Extra params are typically allowed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()

			middleware.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("Expected status %d, got %d", tt.wantStatus, w.Code)
			}
		})
	}
}

// TestValidationIntegration_PathParameterValidation verifies path parameter validation
func TestValidationIntegration_PathParameterValidation(t *testing.T) {
	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", "../../fragments")
	if err != nil {
		t.Fatalf("Failed to create spec loader: %v", err)
	}

	server := &Server{
		specLoader: loader,
	}

	middleware := server.validationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{
			name:       "Valid path parameter",
			path:       "/test/abc123",
			wantStatus: http.StatusOK,
		},
		{
			name:       "Path parameter with special characters",
			path:       "/test/test-with-special",
			wantStatus: http.StatusOK,
		},
		{
			name:       "Path parameter with numbers",
			path:       "/test/12345",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()

			middleware.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("Expected status %d, got %d", tt.wantStatus, w.Code)
			}
		})
	}
}

// TestValidationIntegration_ContentHandling verifies content type handling
func TestValidationIntegration_ContentHandling(t *testing.T) {
	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", "../../fragments")
	if err != nil {
		t.Fatalf("Failed to create spec loader: %v", err)
	}

	server := &Server{
		specLoader: loader,
	}

	middleware := server.validationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		contentType    string
		wantStatus     int
		wantInResponse string
	}{
		{
			name:        "Valid POST with JSON content-type",
			method:      http.MethodPost,
			path:        "/test/post",
			body:        `{"name": "test"}`,
			contentType: "application/json",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "POST without content-type header",
			method:      http.MethodPost,
			path:        "/test/post",
			body:        `{"name": "test"}`,
			contentType: "",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "POST with wrong content-type",
			method:      http.MethodPost,
			path:        "/test/post",
			body:        `{"name": "test"}`,
			contentType: "text/plain",
			wantStatus:  http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, bytes.NewReader([]byte(tt.body)))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			w := httptest.NewRecorder()

			middleware.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("Expected status %d, got %d. Body: %s", tt.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}

// TestValidationIntegration_DocsURLGeneration verifies docs_url generation in error responses
func TestValidationIntegration_DocsURLGeneration(t *testing.T) {
	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", "../../fragments")
	if err != nil {
		t.Fatalf("Failed to create spec loader: %v", err)
	}

	server := &Server{
		specLoader: loader,
	}

	middleware := server.validationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name               string
		method             string
		path               string
		body               string
		expectedDocsPrefix string
	}{
		{
			name:               "POST error to /test/post",
			method:             http.MethodPost,
			path:               "/test/post",
			body:               `{"value": 42}`,
			expectedDocsPrefix: "/docs/route?path=/test/post&method=POST",
		},
		{
			name:               "POST error with invalid JSON",
			method:             http.MethodPost,
			path:               "/test/post",
			body:               `{"invalid": json}`,
			expectedDocsPrefix: "/docs/route?path=/test/post&method=POST",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, bytes.NewReader([]byte(tt.body)))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			middleware.ServeHTTP(w, req)

			// Parse response
			var response SpecValidationResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("Failed to decode error response: %v", err)
			}

			// Verify docs_url contains the expected prefix
			if !strings.HasPrefix(response.DocsURL, tt.expectedDocsPrefix) {
				t.Errorf("Expected docs_url to start with '%s', got '%s'", tt.expectedDocsPrefix, response.DocsURL)
			}

			// Verify docs_url is properly URL-encoded
			parsedURL, err := url.Parse(response.DocsURL)
			if err != nil {
				t.Errorf("Failed to parse docs_url as URL: %v", err)
			}

			// Verify query parameters exist
			if parsedURL.Query().Get("path") == "" {
				t.Error("Expected docs_url to contain 'path' query parameter")
			}

			if parsedURL.Query().Get("method") == "" {
				t.Error("Expected docs_url to contain 'method' query parameter")
			}
		})
	}
}
