package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
	"go.yaml.in/yaml/v4"
)

// TestDispatchHandler_RouteTableMatch verifies that the dispatch handler uses
// RouteTable.Match to match requests and extracts the upstream target from the
// matched route entry.
func TestDispatchHandler_RouteTableMatch(t *testing.T) {
	// Create a test upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "upstream response",
			"path":    r.URL.Path,
			"method":  r.Method,
		})
	}))
	defer upstream.Close()

	// Build an OpenAPI spec with x-upstream extension
	spec := &v3.Document{
		Paths: &v3.Paths{
			PathItems: orderedmap.New[string, *v3.PathItem](),
		},
	}

	pathItem := &v3.PathItem{
		Get: &v3.Operation{
			Responses: &v3.Responses{
				Codes: orderedmap.New[string, *v3.Response](),
			},
		},
		Post: &v3.Operation{
			Responses: &v3.Responses{
				Codes: orderedmap.New[string, *v3.Response](),
			},
		},
	}

	spec.Paths.PathItems.Set("/test", pathItem)

	// Add responses to operations
	pathItem.Get.Responses.Codes.Set("200", &v3.Response{
		Description: "Success",
	})
	pathItem.Post.Responses.Codes.Set("201", &v3.Response{
		Description: "Created",
	})

	// Add x-upstream extension via Extensions (which is a map[string]any that wraps yaml.Node)
	pathItem.Get.Extensions = orderedmap.New[string, *yaml.Node]()
	pathItem.Get.Extensions.Set("x-upstream", &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: upstream.URL,
	})

	pathItem.Post.Extensions = orderedmap.New[string, *yaml.Node]()
	pathItem.Post.Extensions.Set("x-upstream", &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: upstream.URL,
	})

	// Build route table from spec
	table, err := BuildRouteTable(spec)
	if err != nil {
		t.Fatalf("Failed to build route table: %v", err)
	}

	if table.RouteCount() != 2 {
		t.Errorf("Expected 2 routes, got %d", table.RouteCount())
	}

	// Verify routes have upstream targets
	routes := table.GetRoutes()
	for _, route := range routes {
		if route.UpstreamTarget == "" {
			t.Errorf("Route %s %s has empty upstream target", route.Method, route.PathTemplate)
		}
	}

	// Create a test server with minimal config (skip spec loader)
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "./spec",
	}
	srv := &Server{
		config:          cfg,
		routeTable:      table,
		proxyMap:        make(map[string]*ReverseProxy),
		cache:           NewResponseCache(),
		singleFlight:    NewSingleFlight(),
		cacheTTLs:       make(map[string]int),
		circuitBreakers: NewCircuitBreakerStateRegistry(),
		quotaTracker:    NewQuotaTracker(),
		costPerCalls:    make(map[string]float64),
	}

	// Test GET request
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	srv.dispatchHandler(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if result["message"] != "upstream response" {
		t.Errorf("Expected message 'upstream response', got %v", result["message"])
	}

	// Test POST request
	req = httptest.NewRequest(http.MethodPost, "/test", nil)
	w = httptest.NewRecorder()
	srv.dispatchHandler(w, req)

	resp = w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
}

// TestDispatchHandler_NoRoute verifies that dispatchHandler returns 404 when
// no route matches the request.
func TestDispatchHandler_NoRoute(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "./spec",
	}
	srv := &Server{
		config: cfg,
		routeTable: &RouteTable{
			routes: make([]RouteEntry, 0),
		},
		proxyMap:        make(map[string]*ReverseProxy),
		cache:           NewResponseCache(),
		singleFlight:    NewSingleFlight(),
		cacheTTLs:       make(map[string]int),
		circuitBreakers: NewCircuitBreakerStateRegistry(),
		quotaTracker:    NewQuotaTracker(),
		costPerCalls:    make(map[string]float64),
	}

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.dispatchHandler(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if result["error"] != "route_not_found" {
		t.Errorf("Expected error 'route_not_found', got %v", result["error"])
	}
}

// TestDispatchHandler_NoUpstream verifies that dispatchHandler returns 503
// when a matched route has no upstream target.
func TestDispatchHandler_NoUpstream(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "./spec",
	}
	srv := &Server{
		config: cfg,
		routeTable: &RouteTable{
			routes: []RouteEntry{
				{
					PathTemplate:   "/test",
					Method:         http.MethodGet,
					APIVersion:     "v1",
					UpstreamTarget: "", // Empty upstream target
				},
			},
		},
		proxyMap:        make(map[string]*ReverseProxy),
		cache:           NewResponseCache(),
		singleFlight:    NewSingleFlight(),
		cacheTTLs:       make(map[string]int),
		circuitBreakers: NewCircuitBreakerStateRegistry(),
		quotaTracker:    NewQuotaTracker(),
		costPerCalls:    make(map[string]float64),
	}

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	srv.dispatchHandler(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if result["error"] != "no_upstream_configured" {
		t.Errorf("Expected error 'no_upstream_configured', got %v", result["error"])
	}
}

// TestDispatchHandler_ProxyCreationFailed verifies that dispatchHandler returns 503
// when proxy creation fails (e.g., invalid upstream URL).
func TestDispatchHandler_ProxyCreationFailed(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "./spec",
	}
	srv := &Server{
		config: cfg,
		routeTable: &RouteTable{
			routes: []RouteEntry{
				{
					PathTemplate:   "/test",
					Method:         http.MethodGet,
					APIVersion:     "v1",
					UpstreamTarget: "http://invalid:999999", // Invalid port
				},
			},
		},
		proxyMap:        make(map[string]*ReverseProxy),
		cache:           NewResponseCache(),
		singleFlight:    NewSingleFlight(),
		cacheTTLs:       make(map[string]int),
		circuitBreakers: NewCircuitBreakerStateRegistry(),
		quotaTracker:    NewQuotaTracker(),
		costPerCalls:    make(map[string]float64),
	}

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	srv.dispatchHandler(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if result["error"] != "proxy_creation_failed" {
		t.Errorf("Expected error 'proxy_creation_failed', got %v", result["error"])
	}
}

// TestExtractUpstreamTarget verifies the extractUpstreamTarget function.
func TestExtractUpstreamTarget(t *testing.T) {
	tests := []struct {
		name        string
		extension   map[string]interface{}
		expected    string
		expectError bool
	}{
		{
			name: "valid upstream extension",
			extension: map[string]interface{}{
				"x-upstream": "http://example.com",
			},
			expected:    "http://example.com",
			expectError: false,
		},
		{
			name: "no upstream extension",
			extension: map[string]interface{}{
				"x-other": "value",
			},
			expected:    "",
			expectError: false,
		},
		{
			name:        "no extensions",
			extension:   nil,
			expected:    "",
			expectError: false,
		},
		{
			name:        "empty upstream extension",
			extension:   map[string]interface{}{},
			expected:    "",
			expectError: false,
		},
		{
			name: "upstream with whitespace",
			extension: map[string]interface{}{
				"x-upstream": "  http://example.com  ",
			},
			expected:    "http://example.com",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			operation := &v3.Operation{}
			if tt.extension != nil {
				operation.Extensions = orderedmap.New[string, *yaml.Node]()
				for key, value := range tt.extension {
					strVal, ok := value.(string)
					if !ok {
						strVal = ""
					}
					operation.Extensions.Set(key, &yaml.Node{
						Kind:  yaml.ScalarNode,
						Value: strVal,
					})
				}
			}

			result, err := extractUpstreamTarget(operation)
			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestRouteTable_UpstreamTargetExtraction verifies that BuildRouteTable correctly
// extracts upstream targets from x-upstream extensions.
func TestRouteTable_UpstreamTargetExtraction(t *testing.T) {
	// Build an OpenAPI spec with x-upstream extensions
	spec := &v3.Document{
		Paths: &v3.Paths{
			PathItems: orderedmap.New[string, *v3.PathItem](),
		},
	}

	pathItem := &v3.PathItem{
		Get: &v3.Operation{
			Responses: &v3.Responses{
				Codes: orderedmap.New[string, *v3.Response](),
			},
		},
		Post: &v3.Operation{
			Responses: &v3.Responses{
				Codes: orderedmap.New[string, *v3.Response](),
			},
		},
		Put: &v3.Operation{
			Responses: &v3.Responses{
				Codes: orderedmap.New[string, *v3.Response](),
			},
		},
	}

	spec.Paths.PathItems.Set("/users", pathItem)

	// Add responses
	pathItem.Get.Responses.Codes.Set("200", &v3.Response{Description: "Success"})
	pathItem.Post.Responses.Codes.Set("201", &v3.Response{Description: "Created"})
	pathItem.Put.Responses.Codes.Set("200", &v3.Response{Description: "Updated"})

	// Add x-upstream extensions
	pathItem.Get.Extensions = orderedmap.New[string, *yaml.Node]()
	pathItem.Get.Extensions.Set("x-upstream", &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: "http://users-service:8080",
	})

	pathItem.Post.Extensions = orderedmap.New[string, *yaml.Node]()
	pathItem.Post.Extensions.Set("x-upstream", &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: "http://users-service:8080",
	})

	// PUT has no x-upstream extension, should get empty string

	// Build route table
	table, err := BuildRouteTable(spec)
	if err != nil {
		t.Fatalf("Failed to build route table: %v", err)
	}

	if table.RouteCount() != 3 {
		t.Errorf("Expected 3 routes, got %d", table.RouteCount())
	}

	routes := table.GetRoutes()

	// Verify GET and POST have upstream targets
	getRouteFound := false
	postRouteFound := false
	putRouteFound := false

	for _, route := range routes {
		if route.Method == http.MethodGet && route.PathTemplate == "/users" {
			getRouteFound = true
			if route.UpstreamTarget != "http://users-service:8080" {
				t.Errorf("GET /users: expected upstream 'http://users-service:8080', got %q", route.UpstreamTarget)
			}
		}
		if route.Method == http.MethodPost && route.PathTemplate == "/users" {
			postRouteFound = true
			if route.UpstreamTarget != "http://users-service:8080" {
				t.Errorf("POST /users: expected upstream 'http://users-service:8080', got %q", route.UpstreamTarget)
			}
		}
		if route.Method == http.MethodPut && route.PathTemplate == "/users" {
			putRouteFound = true
			if route.UpstreamTarget != "" {
				t.Errorf("PUT /users: expected empty upstream, got %q", route.UpstreamTarget)
			}
		}
	}

	if !getRouteFound {
		t.Error("GET /users route not found")
	}
	if !postRouteFound {
		t.Error("POST /users route not found")
	}
	if !putRouteFound {
		t.Error("PUT /users route not found")
	}
}

// TestRouteTableMatch_WithUpstream verifies that RouteTable.Match returns
// a RouteMatch with the correct upstream target.
func TestRouteTableMatch_WithUpstream(t *testing.T) {
	spec := &v3.Document{
		Paths: &v3.Paths{
			PathItems: orderedmap.New[string, *v3.PathItem](),
		},
	}

	pathItem := &v3.PathItem{
		Get: &v3.Operation{
			Responses: &v3.Responses{
				Codes: orderedmap.New[string, *v3.Response](),
			},
		},
	}

	spec.Paths.PathItems.Set("/test", pathItem)
	pathItem.Get.Responses.Codes.Set("200", &v3.Response{Description: "OK"})
	pathItem.Get.Extensions = orderedmap.New[string, *yaml.Node]()
	pathItem.Get.Extensions.Set("x-upstream", &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: "http://test-service:8080",
	})

	table, err := BuildRouteTable(spec)
	if err != nil {
		t.Fatalf("Failed to build route table: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	match, err := table.Match(req)
	if err != nil {
		t.Fatalf("Match failed: %v", err)
	}

	if match.Route.UpstreamTarget != "http://test-service:8080" {
		t.Errorf("Expected upstream 'http://test-service:8080', got %q", match.Route.UpstreamTarget)
	}

	if match.Route.PathTemplate != "/test" {
		t.Errorf("Expected path template '/test', got %q", match.Route.PathTemplate)
	}

	if match.Route.Method != http.MethodGet {
		t.Errorf("Expected method GET, got %s", match.Route.Method)
	}
}

// TestDispatchHandler_PathParameters verifies that dispatchHandler correctly
// handles path parameters when matching routes.
func TestDispatchHandler_PathParameters(t *testing.T) {
	// Create a test upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "user found",
			"path":    r.URL.Path,
		})
	}))
	defer upstream.Close()

	// Build an OpenAPI spec with path parameters
	spec := &v3.Document{
		Paths: &v3.Paths{
			PathItems: orderedmap.New[string, *v3.PathItem](),
		},
	}

	pathItem := &v3.PathItem{
		Get: &v3.Operation{
			Responses: &v3.Responses{
				Codes: orderedmap.New[string, *v3.Response](),
			},
		},
	}

	spec.Paths.PathItems.Set("/users/{id}", pathItem)
	pathItem.Get.Responses.Codes.Set("200", &v3.Response{Description: "OK"})
	pathItem.Get.Extensions = orderedmap.New[string, *yaml.Node]()
	pathItem.Get.Extensions.Set("x-upstream", &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: upstream.URL,
	})

	table, err := BuildRouteTable(spec)
	if err != nil {
		t.Fatalf("Failed to build route table: %v", err)
	}

	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "./spec",
	}
	srv := &Server{
		config:          cfg,
		routeTable:      table,
		proxyMap:        make(map[string]*ReverseProxy),
		cache:           NewResponseCache(),
		singleFlight:    NewSingleFlight(),
		cacheTTLs:       make(map[string]int),
		circuitBreakers: NewCircuitBreakerStateRegistry(),
		quotaTracker:    NewQuotaTracker(),
		costPerCalls:    make(map[string]float64),
	}

	// Test with a specific user ID
	req := httptest.NewRequest(http.MethodGet, "/users/123", nil)
	w := httptest.NewRecorder()
	srv.dispatchHandler(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
}
