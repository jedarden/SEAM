package server

import (
	"net/http"
	"os"
	"testing"

	"github.com/ardenone/seam/internal/spec"
	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
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

	// Spec-driven population now populates the table at construction time.
	// createTestSpecLoader's fixture defines one operation (GET /test).
	if table.RouteCount() != 1 {
		t.Errorf("expected RouteCount=1 (spec-driven population from GET /test), got %d", table.RouteCount())
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

// Tests for BuildRouteTable function

func TestBuildRouteTable_NilSpec(t *testing.T) {
	table, err := BuildRouteTable(nil)

	if err == nil {
		t.Error("expected error for nil spec, got nil")
	}
	if table != nil {
		t.Error("expected nil table for nil spec, got non-nil")
	}
}

func TestBuildRouteTable_NoPaths(t *testing.T) {
	specJSON := `{
		"openapi": "3.0.0",
		"info": {
			"title": "Test API",
			"version": "1.0.0"
		},
		"paths": {}
	}`

	doc, err := libopenapi.NewDocument([]byte(specJSON))
	if err != nil {
		t.Fatalf("failed to create document: %v", err)
	}

	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatalf("failed to build model: %v", err)
	}

	table, err := BuildRouteTable(&model.Model)

	if err != nil {
		t.Fatalf("expected empty paths object to be valid, got: %v", err)
	}
	if table == nil {
		t.Fatal("expected non-nil table for empty paths object")
	}
	if table.RouteCount() != 0 {
		t.Errorf("expected 0 routes for empty paths object, got %d", table.RouteCount())
	}
}

func TestBuildRouteTable_SingleRoute(t *testing.T) {
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

	doc, err := libopenapi.NewDocument([]byte(specJSON))
	if err != nil {
		t.Fatalf("failed to create document: %v", err)
	}

	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatalf("failed to build model: %v", err)
	}

	table, err := BuildRouteTable(&model.Model)

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if table == nil {
		t.Fatal("expected non-nil table, got nil")
	}

	if table.RouteCount() != 1 {
		t.Errorf("expected 1 route, got %d", table.RouteCount())
	}

	routes := table.GetRoutes()
	if len(routes) != 1 {
		t.Fatalf("expected 1 route in slice, got %d", len(routes))
	}

	entry := routes[0]
	if entry.PathTemplate != "/test" {
		t.Errorf("expected PathTemplate=/test, got %q", entry.PathTemplate)
	}
	if entry.Method != "GET" {
		t.Errorf("expected Method=GET, got %q", entry.Method)
	}
	if entry.APIVersion != "v1" {
		t.Errorf("expected APIVersion=v1 (default), got %q", entry.APIVersion)
	}
}

func TestBuildRouteTable_MultiplePathsAndMethods(t *testing.T) {
	specJSON := `{
		"openapi": "3.0.0",
		"info": {
			"title": "Test API",
			"version": "1.0.0"
		},
		"paths": {
			"/users": {
				"get": {
					"operationId": "listUsers",
					"responses": {
						"200": {
							"description": "Success"
						}
					}
				},
				"post": {
					"operationId": "createUser",
					"responses": {
						"201": {
							"description": "Created"
						}
					}
				}
			},
			"/users/{id}": {
				"get": {
					"operationId": "getUser",
					"responses": {
						"200": {
							"description": "Success"
						}
					}
				},
				"put": {
					"operationId": "updateUser",
					"responses": {
						"200": {
							"description": "Success"
						}
					}
				},
				"delete": {
					"operationId": "deleteUser",
					"responses": {
						"204": {
							"description": "No Content"
						}
					}
				}
			},
			"/health": {
				"get": {
					"operationId": "healthCheck",
					"responses": {
						"200": {
							"description": "OK"
						}
					}
				}
			}
		}
	}`

	doc, err := libopenapi.NewDocument([]byte(specJSON))
	if err != nil {
		t.Fatalf("failed to create document: %v", err)
	}

	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatalf("failed to build model: %v", err)
	}

	table, err := BuildRouteTable(&model.Model)

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Should have 6 routes:
	// GET /users, POST /users
	// GET /users/{id}, PUT /users/{id}, DELETE /users/{id}
	// GET /health
	expectedCount := 6
	if table.RouteCount() != expectedCount {
		t.Errorf("expected %d routes, got %d", expectedCount, table.RouteCount())
	}

	routes := table.GetRoutes()
	if len(routes) != expectedCount {
		t.Fatalf("expected %d routes in slice, got %d", expectedCount, len(routes))
	}

	// Verify some specific routes
	foundUsersGet := false
	foundUsersPost := false
	foundUsersIdGet := false
	foundUsersIdPut := false
	foundUsersIdDelete := false
	foundHealthGet := false

	for _, route := range routes {
		switch route.PathTemplate {
		case "/users":
			if route.Method == "GET" {
				foundUsersGet = true
			}
			if route.Method == "POST" {
				foundUsersPost = true
			}
		case "/users/{id}":
			if route.Method == "GET" {
				foundUsersIdGet = true
			}
			if route.Method == "PUT" {
				foundUsersIdPut = true
			}
			if route.Method == "DELETE" {
				foundUsersIdDelete = true
			}
		case "/health":
			if route.Method == "GET" {
				foundHealthGet = true
			}
		}
	}

	if !foundUsersGet {
		t.Error("missing route: GET /users")
	}
	if !foundUsersPost {
		t.Error("missing route: POST /users")
	}
	if !foundUsersIdGet {
		t.Error("missing route: GET /users/{id}")
	}
	if !foundUsersIdPut {
		t.Error("missing route: PUT /users/{id}")
	}
	if !foundUsersIdDelete {
		t.Error("missing route: DELETE /users/{id}")
	}
	if !foundHealthGet {
		t.Error("missing route: GET /health")
	}
}

func TestBuildRouteTable_CustomAPIVersions(t *testing.T) {
	specJSON := `{
		"openapi": "3.0.0",
		"info": {
			"title": "Test API",
			"version": "1.0.0"
		},
		"paths": {
			"/v1/users": {
				"get": {
					"operationId": "listUsersV1",
					"x-api-version": "v1",
					"responses": {
						"200": {
							"description": "Success"
						}
					}
				}
			},
			"/v2/users": {
				"get": {
					"operationId": "listUsersV2",
					"x-api-version": "v2",
					"responses": {
						"200": {
							"description": "Success"
						}
					}
				}
			},
			"/unversioned/health": {
				"get": {
					"operationId": "healthCheck",
					"x-api-version": "_unversioned",
					"responses": {
						"200": {
							"description": "OK"
						}
					}
				}
			},
			"/default": {
				"get": {
					"operationId": "defaultVersion",
					"responses": {
						"200": {
							"description": "OK"
						}
					}
				}
			}
		}
	}`

	doc, err := libopenapi.NewDocument([]byte(specJSON))
	if err != nil {
		t.Fatalf("failed to create document: %v", err)
	}

	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatalf("failed to build model: %v", err)
	}

	table, err := BuildRouteTable(&model.Model)

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if table.RouteCount() != 4 {
		t.Errorf("expected 4 routes, got %d", table.RouteCount())
	}

	routes := table.GetRoutes()

	// Verify API versions
	for _, route := range routes {
		switch route.PathTemplate {
		case "/v1/users":
			if route.APIVersion != "v1" {
				t.Errorf("expected APIVersion=v1 for /v1/users, got %q", route.APIVersion)
			}
		case "/v2/users":
			if route.APIVersion != "v2" {
				t.Errorf("expected APIVersion=v2 for /v2/users, got %q", route.APIVersion)
			}
		case "/unversioned/health":
			if route.APIVersion != "_unversioned" {
				t.Errorf("expected APIVersion=_unversioned for /unversioned/health, got %q", route.APIVersion)
			}
		case "/default":
			if route.APIVersion != "v1" {
				t.Errorf("expected APIVersion=v1 (default) for /default, got %q", route.APIVersion)
			}
		}
	}
}

func TestBuildRouteTable_DuplicateDetection(t *testing.T) {
	table := &RouteTable{}
	seen := make(map[routeKey]struct{})
	entry := RouteEntry{PathTemplate: "/test", Method: "GET", APIVersion: "v1"}

	if err := addBuiltRoute(table, seen, entry); err != nil {
		t.Fatalf("first route should be accepted: %v", err)
	}
	if err := addBuiltRoute(table, seen, entry); err == nil {
		t.Fatal("expected duplicate route error, got nil")
	}
	if table.RouteCount() != 1 {
		t.Errorf("expected duplicate not to be appended, got %d routes", table.RouteCount())
	}
}

func TestBuildRouteTable_DuplicateDifferentVersions(t *testing.T) {
	// Same path+method but different versions are valid coexistence routes.
	table := &RouteTable{}
	seen := make(map[routeKey]struct{})
	for _, version := range []string{"v1", "v2"} {
		if err := addBuiltRoute(table, seen, RouteEntry{
			PathTemplate: "/test",
			Method:       "GET",
			APIVersion:   version,
		}); err != nil {
			t.Fatalf("version %s should be accepted: %v", version, err)
		}
	}
	if table.RouteCount() != 2 {
		t.Errorf("expected 2 routes (different versions), got %d", table.RouteCount())
	}
}

func TestBuildRouteTable_AllHTTPMethods(t *testing.T) {
	specJSON := `{
		"openapi": "3.0.0",
		"info": {
			"title": "Test API",
			"version": "1.0.0"
		},
		"paths": {
			"/resource": {
				"get": {
					"operationId": "getResource",
					"responses": {"200": {"description": "OK"}}
				},
				"post": {
					"operationId": "createResource",
					"responses": {"201": {"description": "Created"}}
				},
				"put": {
					"operationId": "updateResource",
					"responses": {"200": {"description": "OK"}}
				},
				"delete": {
					"operationId": "deleteResource",
					"responses": {"204": {"description": "No Content"}}
				},
				"patch": {
					"operationId": "patchResource",
					"responses": {"200": {"description": "OK"}}
				},
				"head": {
					"operationId": "headResource",
					"responses": {"200": {"description": "OK"}}
				},
				"options": {
					"operationId": "optionsResource",
					"responses": {"200": {"description": "OK"}}
				},
				"trace": {
					"operationId": "traceResource",
					"responses": {"200": {"description": "OK"}}
				}
			}
		}
	}`

	doc, err := libopenapi.NewDocument([]byte(specJSON))
	if err != nil {
		t.Fatalf("failed to create document: %v", err)
	}

	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatalf("failed to build model: %v", err)
	}

	table, err := BuildRouteTable(&model.Model)

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	expectedCount := 5
	if table.RouteCount() != expectedCount {
		t.Errorf("expected %d supported routes, got %d", expectedCount, table.RouteCount())
	}

	routes := table.GetRoutes()
	methodsFound := make(map[string]bool)

	for _, route := range routes {
		if route.PathTemplate == "/resource" {
			methodsFound[route.Method] = true
		}
	}

	expectedMethods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
	for _, method := range expectedMethods {
		if !methodsFound[method] {
			t.Errorf("missing method: %s", method)
		}
	}
}

func TestBuildRouteTable_NilPathItem(t *testing.T) {
	// A nil path item is not a valid OpenAPI Path Item Object. Constructing the
	// high-level model directly lets this test exercise the builder's guard;
	// the parser normally rejects or drops null path values earlier.
	spec := &v3.Document{
		Paths: &v3.Paths{
			PathItems: func() *orderedmap.Map[string, *v3.PathItem] {
				items := orderedmap.New[string, *v3.PathItem]()
				items.Set("/empty", nil)
				return items
			}(),
		},
	}

	table, err := BuildRouteTable(spec)

	if err == nil {
		t.Fatal("expected an error for nil path item, got nil")
	}
	if table != nil {
		t.Fatal("expected nil table for malformed path item")
	}
}

func TestExtractAPIVersion(t *testing.T) {
	tests := []struct {
		name          string
		operationJSON string
		expectedVer   string
	}{
		{
			name: "string version",
			operationJSON: `{
				"operationId": "test",
				"x-api-version": "v2",
				"responses": {"200": {"description": "OK"}}
			}`,
			expectedVer: "v2",
		},
		{
			name: "numeric version is malformed",
			operationJSON: `{
				"operationId": "test",
				"x-api-version": 2,
				"responses": {"200": {"description": "OK"}}
			}`,
			expectedVer: "",
		},
		{
			name: "default version (no extension)",
			operationJSON: `{
				"operationId": "test",
				"responses": {"200": {"description": "OK"}}
			}`,
			expectedVer: "v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specJSON := `{
				"openapi": "3.0.0",
				"info": {"title": "Test", "version": "1.0.0"},
				"paths": {
					"/test": {
						"get": ` + tt.operationJSON + `
					}
				}
			}`

			doc, err := libopenapi.NewDocument([]byte(specJSON))
			if err != nil {
				t.Fatalf("failed to create document: %v", err)
			}

			model, err := doc.BuildV3Model()
			if err != nil {
				t.Fatalf("failed to build model: %v", err)
			}

			table, err := BuildRouteTable(&model.Model)
			if tt.expectedVer == "" {
				if err == nil {
					t.Fatal("expected malformed x-api-version error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}

			routes := table.GetRoutes()
			if len(routes) != 1 {
				t.Fatalf("expected 1 route, got %d", len(routes))
			}

			if routes[0].APIVersion != tt.expectedVer {
				t.Errorf("expected APIVersion=%q, got %q", tt.expectedVer, routes[0].APIVersion)
			}
		})
	}
}
