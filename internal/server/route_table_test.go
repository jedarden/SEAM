package server

import (
	"net/http"
	"os"
	"testing"

	"github.com/ardenone/seam/internal/spec"
)

// createTestSpecLoader creates a minimal spec loader for testing
func createTestSpecLoader(t *testing.T) *spec.Loader {
	t.Helper()

	// Create a minimal OpenAPI spec for testing
	specJSON := `{
		"openapi": "3.0.0",
		"info": {
			"title": "Test API",
			"version": "1.0.0"
		},
		"paths": {
			"/test": {
				"get": {
					"operationId": "testGet",
					"responses": {
						"200": {
							"description": "Success"
						}
					}
				}
			}
		}
	}`

	// Create a temporary directory for spec files
	tmpDir := t.TempDir()

	// Write the spec to a file. spec.New only looks for openapi.yaml, but YAML
	// is a superset of JSON so this content parses fine under that extension.
	specPath := tmpDir + "/openapi.yaml"
	if err := os.WriteFile(specPath, []byte(specJSON), 0644); err != nil {
		t.Fatalf("failed to write spec file: %v", err)
	}

	loader, err := spec.New(tmpDir, "http://localhost:8080")
	if err != nil {
		t.Fatalf("failed to create spec loader: %v", err)
	}

	return loader
}

func TestNewRouteTable(t *testing.T) {
	loader := createTestSpecLoader(t)
	table := NewRouteTable(loader)

	if table == nil {
		t.Fatal("NewRouteTable returned nil")
	}

	// Building the table from spec is out of scope for this bead (child 2);
	// NewRouteTable must return an empty table.
	if table.RouteCount() != 0 {
		t.Errorf("expected RouteCount=0 (spec-driven population is child bead 2), got %d", table.RouteCount())
	}
}

func TestRouteEntryInitialization(t *testing.T) {
	tests := []struct {
		name          string
		entry         RouteEntry
		wantValid     bool
		expectedError string
	}{
		{
			name: "valid route entry",
			entry: RouteEntry{
				PathTemplate:   "/api/v1/users/{id}",
				Method:         "GET",
				APIVersion:     "v1",
				UpstreamTarget: "http://userservice:8080",
			},
			wantValid: true,
		},
		{
			name: "valid route with POST method",
			entry: RouteEntry{
				PathTemplate:   "/api/v2/orders",
				Method:         "POST",
				APIVersion:     "v2",
				UpstreamTarget: "http://orderservice:8080",
			},
			wantValid: true,
		},
		{
			name: "valid unversioned route",
			entry: RouteEntry{
				PathTemplate:   "/health",
				Method:         "GET",
				APIVersion:     "_unversioned",
				UpstreamTarget: "http://healthservice:8080",
			},
			wantValid: true,
		},
		{
			name: "empty path template",
			entry: RouteEntry{
				PathTemplate:   "",
				Method:         "GET",
				APIVersion:     "v1",
				UpstreamTarget: "http://service:8080",
			},
			wantValid:     false,
			expectedError: "PathTemplate cannot be empty",
		},
		{
			name: "empty method",
			entry: RouteEntry{
				PathTemplate:   "/api/v1/users",
				Method:         "",
				APIVersion:     "v1",
				UpstreamTarget: "http://service:8080",
			},
			wantValid:     false,
			expectedError: "Method cannot be empty",
		},
		{
			name: "empty API version",
			entry: RouteEntry{
				PathTemplate:   "/api/v1/users",
				Method:         "GET",
				APIVersion:     "",
				UpstreamTarget: "http://service:8080",
			},
			wantValid:     false,
			expectedError: "APIVersion cannot be empty",
		},
		{
			name: "empty upstream target",
			entry: RouteEntry{
				PathTemplate:   "/api/v1/users",
				Method:         "GET",
				APIVersion:     "v1",
				UpstreamTarget: "",
			},
			wantValid:     false,
			expectedError: "UpstreamTarget cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := NewRouteTable(nil)
			table.AddRoute(tt.entry)

			err := table.Validate()

			if tt.wantValid {
				if err != nil {
					t.Errorf("expected route to be valid, got error: %v", err)
				}
			} else {
				if err == nil {
					t.Error("expected validation error, got nil")
				} else if tt.expectedError != "" {
					// Check if error message contains expected substring
					if !containsString(err.Error(), tt.expectedError) {
						t.Errorf("expected error to contain %q, got %q", tt.expectedError, err.Error())
					}
				}
			}
		})
	}
}

func TestAddRoute(t *testing.T) {
	table := NewRouteTable(nil)

	entry := RouteEntry{
		PathTemplate:   "/api/v1/users/{id}",
		Method:         "GET",
		APIVersion:     "v1",
		UpstreamTarget: "http://userservice:8080",
	}

	table.AddRoute(entry)

	if table.RouteCount() != 1 {
		t.Errorf("expected RouteCount=1, got %d", table.RouteCount())
	}

	routes := table.GetRoutes()
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}

	if routes[0].PathTemplate != entry.PathTemplate {
		t.Errorf("expected PathTemplate=%q, got %q", entry.PathTemplate, routes[0].PathTemplate)
	}
	if routes[0].Method != entry.Method {
		t.Errorf("expected Method=%q, got %q", entry.Method, routes[0].Method)
	}
	if routes[0].APIVersion != entry.APIVersion {
		t.Errorf("expected APIVersion=%q, got %q", entry.APIVersion, routes[0].APIVersion)
	}
	if routes[0].UpstreamTarget != entry.UpstreamTarget {
		t.Errorf("expected UpstreamTarget=%q, got %q", entry.UpstreamTarget, routes[0].UpstreamTarget)
	}
}

func TestAddMultipleRoutes(t *testing.T) {
	table := NewRouteTable(nil)

	entries := []RouteEntry{
		{PathTemplate: "/api/v1/users/{id}", Method: "GET", APIVersion: "v1", UpstreamTarget: "http://userservice:8080"},
		{PathTemplate: "/api/v1/users", Method: "POST", APIVersion: "v1", UpstreamTarget: "http://userservice:8080"},
		{PathTemplate: "/api/v2/orders/{order_id}", Method: "GET", APIVersion: "v2", UpstreamTarget: "http://orderservice:8080"},
	}

	for _, entry := range entries {
		table.AddRoute(entry)
	}

	if table.RouteCount() != 3 {
		t.Errorf("expected RouteCount=3, got %d", table.RouteCount())
	}

	routes := table.GetRoutes()
	if len(routes) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(routes))
	}
}

func TestGetRoutes(t *testing.T) {
	table := NewRouteTable(nil)

	entry := RouteEntry{
		PathTemplate:   "/api/v1/users/{id}",
		Method:         "GET",
		APIVersion:     "v1",
		UpstreamTarget: "http://userservice:8080",
	}
	table.AddRoute(entry)

	// GetRoutes should return a copy, not the underlying slice
	routes1 := table.GetRoutes()
	routes2 := table.GetRoutes()

	if &routes1[0] == &routes2[0] {
		t.Error("GetRoutes should return a copy of the routes slice")
	}

	// Modifying the returned slice should not affect the table
	routes1[0].PathTemplate = "/modified"
	routes3 := table.GetRoutes()
	if routes3[0].PathTemplate == "/modified" {
		t.Error("modifying returned routes should not affect the table")
	}
}

func TestRouteMatchStruct(t *testing.T) {
	route := RouteEntry{
		PathTemplate:   "/api/v1/users/{id}",
		Method:         "GET",
		APIVersion:     "v1",
		UpstreamTarget: "http://userservice:8080",
	}

	params := map[string]string{
		"id": "123",
	}

	match := RouteMatch{
		Route:      route,
		PathParams: params,
	}

	// Verify Route field
	if match.Route.PathTemplate != route.PathTemplate {
		t.Errorf("expected Route.PathTemplate=%q, got %q", route.PathTemplate, match.Route.PathTemplate)
	}
	if match.Route.Method != route.Method {
		t.Errorf("expected Route.Method=%q, got %q", route.Method, match.Route.Method)
	}
	if match.Route.APIVersion != route.APIVersion {
		t.Errorf("expected Route.APIVersion=%q, got %q", route.APIVersion, match.Route.APIVersion)
	}
	if match.Route.UpstreamTarget != route.UpstreamTarget {
		t.Errorf("expected Route.UpstreamTarget=%q, got %q", route.UpstreamTarget, match.Route.UpstreamTarget)
	}

	// Verify PathParams field
	if match.PathParams == nil {
		t.Fatal("PathParams is nil")
	}
	if match.PathParams["id"] != "123" {
		t.Errorf("expected PathParams[\"id\"]=%q, got %q", "123", match.PathParams["id"])
	}
}

func TestValidateEmptyTable(t *testing.T) {
	table := NewRouteTable(nil)

	err := table.Validate()
	if err != nil {
		t.Errorf("expected empty table to be valid, got error: %v", err)
	}
}

func TestValidateMultipleInvalidRoutes(t *testing.T) {
	table := NewRouteTable(nil)

	// Add multiple invalid routes
	table.AddRoute(RouteEntry{
		PathTemplate:   "",
		Method:         "GET",
		APIVersion:     "v1",
		UpstreamTarget: "http://service:8080",
	})

	table.AddRoute(RouteEntry{
		PathTemplate:   "/api/v1/users",
		Method:         "",
		APIVersion:     "v1",
		UpstreamTarget: "http://service:8080",
	})

	err := table.Validate()
	if err == nil {
		t.Error("expected validation error for invalid routes, got nil")
	}
}

// Helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Test that RouteMatcher interface is correctly defined
func TestRouteMatcherInterface(t *testing.T) {
	// This test verifies that the RouteMatcher interface is correctly defined
	// by creating a type that implements it
	var _ RouteMatcher = &mockMatcher{}
}

// mockMatcher implements RouteMatcher for testing
type mockMatcher struct{}

func (m *mockMatcher) Match(req *http.Request) (*RouteMatch, error) {
	return nil, nil
}

func TestRouteEntryFieldValidation(t *testing.T) {
	tests := []struct {
		name       string
		fieldName  string
		fieldValue string
		wantValid  bool
	}{
		{
			name:       "valid GET method",
			fieldName:  "Method",
			fieldValue: "GET",
			wantValid:  true,
		},
		{
			name:       "valid POST method",
			fieldName:  "Method",
			fieldValue: "POST",
			wantValid:  true,
		},
		{
			name:       "valid PUT method",
			fieldName:  "Method",
			fieldValue: "PUT",
			wantValid:  true,
		},
		{
			name:       "valid DELETE method",
			fieldName:  "Method",
			fieldValue: "DELETE",
			wantValid:  true,
		},
		{
			name:       "valid PATCH method",
			fieldName:  "Method",
			fieldValue: "PATCH",
			wantValid:  true,
		},
		{
			name:       "valid v1 API version",
			fieldName:  "APIVersion",
			fieldValue: "v1",
			wantValid:  true,
		},
		{
			name:       "valid v2 API version",
			fieldName:  "APIVersion",
			fieldValue: "v2",
			wantValid:  true,
		},
		{
			name:       "valid unversioned API",
			fieldName:  "APIVersion",
			fieldValue: "_unversioned",
			wantValid:  true,
		},
		{
			name:       "valid http upstream target",
			fieldName:  "UpstreamTarget",
			fieldValue: "http://service:8080",
			wantValid:  true,
		},
		{
			name:       "valid https upstream target",
			fieldName:  "UpstreamTarget",
			fieldValue: "https://service.example.com",
			wantValid:  true,
		},
		{
			name:       "path template with single parameter",
			fieldName:  "PathTemplate",
			fieldValue: "/users/{id}",
			wantValid:  true,
		},
		{
			name:       "path template with multiple parameters",
			fieldName:  "PathTemplate",
			fieldValue: "/users/{user_id}/posts/{post_id}",
			wantValid:  true,
		},
		{
			name:       "root path",
			fieldName:  "PathTemplate",
			fieldValue: "/",
			wantValid:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := RouteEntry{
				PathTemplate:   "/api/v1/resource",
				Method:         "GET",
				APIVersion:     "v1",
				UpstreamTarget: "http://service:8080",
			}

			// Set the field being tested
			switch tt.fieldName {
			case "PathTemplate":
				entry.PathTemplate = tt.fieldValue
			case "Method":
				entry.Method = tt.fieldValue
			case "APIVersion":
				entry.APIVersion = tt.fieldValue
			case "UpstreamTarget":
				entry.UpstreamTarget = tt.fieldValue
			}

			table := NewRouteTable(nil)
			table.AddRoute(entry)

			err := table.Validate()
			if tt.wantValid && err != nil {
				t.Errorf("expected entry to be valid, got error: %v", err)
			} else if !tt.wantValid && err == nil {
				t.Error("expected entry to be invalid, got nil error")
			}
		})
	}
}
