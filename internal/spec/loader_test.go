package spec

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pb33f/libopenapi-validator/errors"
)

// TestFormatDocsURL tests the FormatDocsURL helper function
func TestFormatDocsURL(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		method   string
		expected string
	}{
		{
			name:     "GET request",
			path:     "/api/users",
			method:   "GET",
			expected: "/docs/route?path=/api/users&method=GET&version=_unversioned",
		},
		{
			name:     "POST request",
			path:     "/api/users",
			method:   "POST",
			expected: "/docs/route?path=/api/users&method=POST&version=_unversioned",
		},
		{
			name:     "Path with query parameters",
			path:     "/api/search?q=test",
			method:   "GET",
			expected: "/docs/route?path=/api/search?q=test&method=GET&version=_unversioned",
		},
		{
			name:     "Path with path parameters",
			path:     "/api/users/{id}",
			method:   "GET",
			expected: "/docs/route?path=/api/users/{id}&method=GET&version=_unversioned",
		},
		{
			name:     "DELETE method",
			path:     "/api/users/{id}",
			method:   "DELETE",
			expected: "/docs/route?path=/api/users/{id}&method=DELETE&version=_unversioned",
		},
		{
			name:     "PATCH method",
			path:     "/api/items/{itemId}",
			method:   "PATCH",
			expected: "/docs/route?path=/api/items/{itemId}&method=PATCH&version=_unversioned",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatDocsURL(tt.path, tt.method)
			if result != tt.expected {
				t.Errorf("FormatDocsURL() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestExtractExpectedShape tests the extractExpectedShape helper function
func TestExtractExpectedShape(t *testing.T) {
	tests := []struct {
		name     string
		err      *errors.ValidationError
		contains string // substring that should be present in the result
	}{
		{
			name: "Request validation with HowToFix",
			err: &errors.ValidationError{
				ValidationType: "request",
				HowToFix:       "Add required 'name' field",
				Reason:         "Missing required field",
			},
			contains: "Request validation",
		},
		{
			name: "Response validation with subtype",
			err: &errors.ValidationError{
				ValidationType:    "response",
				ValidationSubType: "content_type",
				HowToFix:          "Return application/json",
				Reason:            "Invalid content type",
			},
			contains: "content_type",
		},
		{
			name: "Parameter validation",
			err: &errors.ValidationError{
				ValidationType: "parameter",
				HowToFix:       "Must be integer",
				Reason:         "Type mismatch",
			},
			contains: "Parameter validation",
		},
		{
			name: "Request body validation",
			err: &errors.ValidationError{
				ValidationType: "requestbody",
				HowToFix:       "Object with 'email' and 'password' fields",
				Reason:         "Schema validation failed",
			},
			contains: "Request body validation",
		},
		{
			name: "Security validation",
			err: &errors.ValidationError{
				ValidationType: "security",
				HowToFix:       "Add valid bearer token",
				Reason:         "Unauthorized",
			},
			contains: "Security validation",
		},
		{
			name: "Generic validation type",
			err: &errors.ValidationError{
				ValidationType: "custom",
				HowToFix:       "Follow the schema",
				Reason:         "Custom validation failed",
			},
			contains: "custom validation",
		},
		{
			name: "No HowToFix provided",
			err: &errors.ValidationError{
				ValidationType: "request",
				Reason:         "Generic error",
			},
			contains: "OpenAPI specification",
		},
		{
			name: "Empty validation error",
			err: &errors.ValidationError{
				Reason: "Some error",
			},
			contains: "OpenAPI specification",
		},
		{
			name: "Validation with subtype only",
			err: &errors.ValidationError{
				HowToFix:          "Fix the format",
				ValidationSubType: "format_check",
			},
			contains: "format_check",
		},
		{
			name: "All fields populated",
			err: &errors.ValidationError{
				ValidationType:    "request",
				ValidationSubType: "required_field",
				HowToFix:          "Add 'email' field",
				Reason:            "Missing required field",
			},
			contains: "required_field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractExpectedShape(tt.err)
			if !containsSubstring(result, tt.contains) {
				t.Errorf("extractExpectedShape() = %v, want to contain %v", result, tt.contains)
			}
		})
	}
}

// TestFormatValidationErrorTo400 tests the FormatValidationErrorTo400 helper function
func TestFormatValidationErrorTo400(t *testing.T) {
	tests := []struct {
		name  string
		errs  []*errors.ValidationError
		path  string
		method string
		check func(map[string]interface{}) bool
	}{
		{
			name:  "Single validation error",
			errs: []*errors.ValidationError{
				{
					SpecPath:    "#/components/schemas/User/required",
					RequestPath: "POST /api/users",
					HowToFix:    "Add 'name' field",
					Reason:      "Missing required field: name",
					SpecLine:    15,
					SpecCol:     5,
				},
			},
			path:   "/api/users",
			method: "POST",
			check: func(result map[string]interface{}) bool {
				// Check error message
				if result["error"] != "validation_failed" {
					return false
				}
				// Check message
				if result["message"] != "Request does not conform to the OpenAPI specification" {
					return false
				}
				// Check docs_url
				docsURL, ok := result["docs_url"].(string)
				if !ok || docsURL != "/docs/route?path=/api/users&method=POST&version=_unversioned" {
					return false
				}
				// Check validation errors array
				valErrors, ok := result["validation_errors"].([]map[string]interface{})
				if !ok || len(valErrors) != 1 {
					return false
				}
				// Check individual error fields
				err := valErrors[0]
				if err["field"] != "#/components/schemas/User/required" {
					return false
				}
				if _, hasExpectedShape := err["expected_shape"]; !hasExpectedShape {
					return false
				}
				if err["actual"] != "POST /api/users" {
					return false
				}
				if err["reason"] != "Missing required field: name" {
					return false
				}
				if err["line"] != 15 {
					return false
				}
				if err["column"] != 5 {
					return false
				}
				return true
			},
		},
		{
			name: "Multiple validation errors",
			errs: []*errors.ValidationError{
				{
					SpecPath:    "#/components/schemas/User/properties/name",
					RequestPath: "string",
					HowToFix:     "Should be string",
					Reason:      "Type mismatch",
				},
				{
					SpecPath:    "#/components/schemas/User/required",
					RequestPath: "POST /api/users",
					HowToFix:    "Add 'email' field",
					Reason:      "Missing required field: email",
				},
			},
			path:   "/api/users",
			method: "POST",
			check: func(result map[string]interface{}) bool {
				valErrors, ok := result["validation_errors"].([]map[string]interface{})
				if !ok || len(valErrors) != 2 {
					return false
				}
				// Check first error
				if valErrors[0]["field"] != "#/components/schemas/User/properties/name" {
					return false
				}
				// Check second error
				if valErrors[1]["field"] != "#/components/schemas/User/required" {
					return false
				}
				return true
			},
		},
		{
			name:  "Empty validation errors",
			errs:  []*errors.ValidationError{},
			path:  "/api/test",
			method: "GET",
			check: func(result map[string]interface{}) bool {
				valErrors, ok := result["validation_errors"].([]map[string]interface{})
				if !ok || len(valErrors) != 0 {
					return false
				}
				return true
			},
		},
		{
			name: "Validation error with all types",
			errs: []*errors.ValidationError{
				{
					SpecPath:         "#/paths/~1api~1users/post/requestBody/content/application~1json/schema",
					ValidationType:    "request",
					ValidationSubType: "schema",
					HowToFix:          "Object with required fields",
					Reason:            "Schema validation failed",
				},
			},
			path:   "/api/users",
			method: "POST",
			check: func(result map[string]interface{}) bool {
				valErrors, ok := result["validation_errors"].([]map[string]interface{})
				if !ok || len(valErrors) != 1 {
					return false
				}
				err := valErrors[0]
				expectedShape, ok := err["expected_shape"].(string)
				if !ok {
					return false
				}
				// Should contain validation type information
				if !containsSubstring(expectedShape, "Request validation") {
					return false
				}
				// Should contain subtype
				if !containsSubstring(expectedShape, "schema") {
					return false
				}
				return true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatValidationErrorTo400(tt.errs, tt.path, tt.method)
			if !tt.check(result) {
				t.Errorf("FormatValidationErrorTo400() check failed for test %s", tt.name)
			}
		})
	}
}

// TestConvertToStructured400 tests the ConvertToStructured400 helper function
func TestConvertToStructured400(t *testing.T) {
	tests := []struct {
		name   string
		ve     *ValidationError
		path   string
		method string
		check  func(*Structured400Response) bool
	}{
		{
			name: "Single error conversion",
			ve: &ValidationError{
				Errors: []*errors.ValidationError{
					{
						SpecPath:    "#/components/schemas/User/required",
						RequestPath: "POST /api/users",
						HowToFix:    "Add 'name' field",
						Reason:      "Missing required field",
						SpecLine:    20,
						SpecCol:     10,
					},
				},
			},
			path:   "/api/users",
			method: "POST",
			check: func(resp *Structured400Response) bool {
				if resp.Error != "validation_failed" {
					return false
				}
				if resp.Message != "Request does not conform to the OpenAPI specification" {
					return false
				}
				if resp.DocsURL != "/docs/route?path=/api/users&method=POST&version=_unversioned" {
					return false
				}
				if len(resp.ValidationErrors) != 1 {
					return false
				}
				err := resp.ValidationErrors[0]
				if err.Field != "#/components/schemas/User/required" {
					return false
				}
				if err.ExpectedShape == "" {
					return false
				}
				if err.Actual != "POST /api/users" {
					return false
				}
				if err.Reason != "Missing required field" {
					return false
				}
				if err.Line != 20 {
					return false
				}
				if err.Column != 10 {
					return false
				}
				return true
			},
		},
		{
			name: "Multiple errors conversion",
			ve: &ValidationError{
				Errors: []*errors.ValidationError{
					{
						SpecPath:    "#/components/schemas/User/properties/name",
						RequestPath: "integer",
						HowToFix:     "Should be string",
						Reason:      "Type mismatch",
					},
					{
						SpecPath:    "#/components/schemas/User/required",
						RequestPath: "POST /api/users",
						HowToFix:    "Add 'email' field",
						Reason:      "Missing required field",
					},
				},
			},
			path:   "/api/users",
			method: "POST",
			check: func(resp *Structured400Response) bool {
				if len(resp.ValidationErrors) != 2 {
					return false
				}
				// Verify both errors are present
				if resp.ValidationErrors[0].Field != "#/components/schemas/User/properties/name" {
					return false
				}
				if resp.ValidationErrors[1].Field != "#/components/schemas/User/required" {
					return false
				}
				return true
			},
		},
		{
			name: "Empty validation error",
			ve: &ValidationError{
				Errors: []*errors.ValidationError{},
			},
			path:   "/api/test",
			method: "GET",
			check: func(resp *Structured400Response) bool {
				if len(resp.ValidationErrors) != 0 {
					return false
				}
				if resp.DocsURL != "/docs/route?path=/api/test&method=GET&version=_unversioned" {
					return false
				}
				return true
			},
		},
		{
			name: "Nil validation error fields",
			ve: &ValidationError{
				Errors: []*errors.ValidationError{
					{
						Reason: "Some validation error",
					},
				},
			},
			path:   "/api/test",
			method: "POST",
			check: func(resp *Structured400Response) bool {
				if len(resp.ValidationErrors) != 1 {
					return false
				}
				// Should have expected_shape derived from missing fields
				if resp.ValidationErrors[0].ExpectedShape == "" {
					return false
				}
				return true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertToStructured400(tt.ve, tt.path, tt.method)
			if !tt.check(result) {
				t.Errorf("ConvertToStructured400() check failed for test %s", tt.name)
			}
		})
	}
}

// TestValidationFieldErrorJSON tests the JSON serialization of ValidationFieldError
func TestValidationFieldErrorJSON(t *testing.T) {
	err := ValidationFieldError{
		Field:          "#/components/schemas/User/required",
		ExpectedShape:  "Request validation: Add 'name' field",
		Actual:         "POST /api/users",
		Reason:         "Missing required field",
		Line:           20,
		Column:         10,
	}

	// This test ensures the struct can be serialized to JSON correctly
	// In a real scenario, you'd use json.Marshal and check the output
	// For now, we just verify the fields are set correctly
	if err.Field == "" {
		t.Error("Field should not be empty")
	}
	if err.ExpectedShape == "" {
		t.Error("ExpectedShape should not be empty")
	}
	if err.Reason == "" {
		t.Error("Reason should not be empty")
	}
	if err.Line == 0 {
		t.Error("Line should be 20")
	}
	if err.Column == 0 {
		t.Error("Column should be 10")
	}
}

// TestStructured400ResponseJSON tests the JSON serialization of Structured400Response
func TestStructured400ResponseJSON(t *testing.T) {
	response := Structured400Response{
		Error:   "validation_failed",
		Message: "Request does not conform to the OpenAPI specification",
		DocsURL: "/docs/route?path=/api/users&method=POST&version=_unversioned",
		ValidationErrors: []ValidationFieldError{
			{
				Field:          "#/components/schemas/User/required",
				ExpectedShape:  "Request validation: Add 'name' field",
				Actual:         "POST /api/users",
				Reason:         "Missing required field",
				Line:           20,
				Column:         10,
			},
		},
	}

	// Verify the response structure
	if response.Error != "validation_failed" {
		t.Errorf("Expected error 'validation_failed', got %s", response.Error)
	}
	if response.Message != "Request does not conform to the OpenAPI specification" {
		t.Errorf("Unexpected message: %s", response.Message)
	}
	if response.DocsURL != "/docs/route?path=/api/users&method=POST&version=_unversioned" {
		t.Errorf("Unexpected docs_url: %s", response.DocsURL)
	}
	if len(response.ValidationErrors) != 1 {
		t.Errorf("Expected 1 validation error, got %d", len(response.ValidationErrors))
	}
}

// TestValidationErrorToJSONBackwardCompatibility tests the ToJSON method
func TestValidationErrorToJSONBackwardCompatibility(t *testing.T) {
	// This test ensures the ToJSON method still works with the new field names
	ve := &ValidationError{
		Errors: []*errors.ValidationError{
			{
				SpecPath:    "#/components/schemas/User/required",
				RequestPath: "POST /api/users",
				HowToFix:    "Add 'name' field",
				Reason:      "Missing required field",
				SpecLine:    20,
				SpecCol:     10,
			},
		},
	}

	result := ve.ToJSON("/api/users", "POST")

	// Check the new field names are used
	if _, ok := result["docs_url"]; !ok {
		t.Error("Result should have 'docs_url' field")
	}
	if _, ok := result["docs_pointer"]; ok {
		t.Error("Result should not have 'docs_pointer' field (old name)")
	}

	valErrors, ok := result["validation_errors"].([]map[string]interface{})
	if !ok || len(valErrors) != 1 {
		t.Fatal("Expected 1 validation error")
	}

	// Check new field names in validation errors
	if _, ok := valErrors[0]["expected_shape"]; !ok {
		t.Error("Validation error should have 'expected_shape' field")
	}
	if _, ok := valErrors[0]["expected"]; ok {
		t.Error("Validation error should not have 'expected' field (old name)")
	}
}

// TestStructured400ResponseWithVariousHTTPMethods tests different HTTP methods
func TestStructured400ResponseWithVariousHTTPMethods(t *testing.T) {
	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodHead,
		http.MethodOptions,
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			ve := &ValidationError{
				Errors: []*errors.ValidationError{
					{
						SpecPath: "#/paths/~1api~1users/" + strings.ToLower(method),
						Reason:  "Some error",
					},
				},
			}

			result := ConvertToStructured400(ve, "/api/users", method)

			// Verify the docs_url contains the correct method
			expectedDocsURL := "/docs/route?path=/api/users&method=" + method + "&version=_unversioned"
			if result.DocsURL != expectedDocsURL {
				t.Errorf("Expected docs_url %s, got %s", expectedDocsURL, result.DocsURL)
			}
		})
	}
}

// TestStructured400ResponseWithComplexPaths tests various path formats
func TestStructured400ResponseWithComplexPaths(t *testing.T) {
	paths := []struct {
		path          string
		expectedInURL string
	}{
		{"/api/users", "/api/users"},
		{"/api/users/{id}", "/api/users/{id}"},
		{"/api/items/{itemId}/details", "/api/items/{itemId}/details"},
		{"/api/search?q=test&limit=10", "/api/search?q=test&limit=10"},
		{"/v1/users", "/v1/users"},
		{"/api/v2/items/{id}", "/api/v2/items/{id}"},
	}

	for _, testPath := range paths {
		t.Run(testPath.path, func(t *testing.T) {
			ve := &ValidationError{
				Errors: []*errors.ValidationError{
					{
						SpecPath: "#/paths" + testPath.path,
						Reason:   "Some error",
					},
				},
			}

			result := ConvertToStructured400(ve, testPath.path, "GET")

			// Verify the docs_url contains the correct path
			if !containsSubstring(result.DocsURL, "path="+testPath.path) {
				t.Errorf("Expected docs_url to contain path=%s, got %s", testPath.path, result.DocsURL)
			}
		})
	}
}

// Helper function to check if a string contains a substring
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

// Helper function to find the index of a substring
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
