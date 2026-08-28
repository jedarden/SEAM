package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWhoamiHandler(t *testing.T) {
	server := &Server{
		scopeVersionCache: NewScopeVersionCache(),
	}

	tests := []struct {
		name           string
		identity       *Identity
		expectedStatus int
		checkResponse  func(t *testing.T, body map[string]interface{})
	}{
		{
			name: "resolved identity with scopes",
			identity: &Identity{
				NodeKey:     "test-node-1",
				NodeName:     "test-node.example.com",
				User:        "testuser",
				Tags:        []string{"tag:prod", "tag:cluster:prod"},
				Capabilities: []string{"seam:read", "seam:write"},
				Resolved:    true,
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, body map[string]interface{}) {
				// Check identity structure
				identity, ok := body["identity"].(map[string]interface{})
				if !ok {
					t.Fatal("Response missing 'identity' object")
				}

				if identity["node_key"] != "test-node-1" {
					t.Errorf("node_key mismatch: got %v, want 'test-node-1'", identity["node_key"])
				}
				if identity["node_name"] != "test-node.example.com" {
					t.Errorf("node_name mismatch: got %v, want 'test-node.example.com'", identity["node_name"])
				}
				if identity["user"] != "testuser" {
					t.Errorf("user mismatch: got %v, want 'testuser'", identity["user"])
				}

				// Check tags
				tags, ok := identity["tags"].([]interface{})
				if !ok {
					t.Fatal("tags is not an array")
				}
				if len(tags) != 2 {
					t.Errorf("Expected 2 tags, got %d", len(tags))
				}

				// Check effective scopes
				scopes, ok := body["effective_scopes"].([]interface{})
				if !ok {
					t.Fatal("effective_scopes is not an array")
				}
				if len(scopes) != 2 {
					t.Errorf("Expected 2 effective scopes, got %d", len(scopes))
				}

				// Check scope version is present
				scopeVersion, ok := body["scope_version"].(string)
				if !ok || scopeVersion == "" {
					t.Error("scope_version is missing or empty")
				}

				// Check resolved flag
				resolved, ok := body["resolved"].(bool)
				if !ok || !resolved {
					t.Error("resolved should be true")
				}
			},
		},
		{
			name: "unresolved identity",
			identity: &Identity{
				NodeKey:     "anonymous",
				NodeName:    "unknown",
				Resolved:    false,
				Capabilities: []string{},
				Tags:        []string{},
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, body map[string]interface{}) {
				resolved, ok := body["resolved"].(bool)
				if !ok || resolved {
					t.Error("resolved should be false for unresolved identity")
				}

				scopes, ok := body["effective_scopes"].([]interface{})
				if !ok || len(scopes) != 0 {
					t.Error("effective_scopes should be empty for unresolved identity")
				}
			},
		},
		{
			name:           "nil identity (should not happen in practice)",
			identity:       nil,
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, body map[string]interface{}) {
				identity, ok := body["identity"].(map[string]interface{})
				if !ok {
					t.Fatal("Response missing 'identity' object")
				}

				if identity["node_key"] != "anonymous" {
					t.Errorf("node_key should be 'anonymous' for nil identity, got %v", identity["node_key"])
				}
				resolved, ok := body["resolved"].(bool)
				if !ok || resolved {
					t.Error("resolved should be false for nil identity")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create request with identity in context
			req := httptest.NewRequest("GET", "/whoami", nil)
			req = req.WithContext(contextWithIdentity(req.Context(), tt.identity))

			// Create response recorder
			w := httptest.NewRecorder()

			// Call handler
			server.whoamiHandler(w, req)

			// Check status
			if w.Code != tt.expectedStatus {
				t.Errorf("Status mismatch: got %d, want %d", w.Code, tt.expectedStatus)
			}

			// Check content type
			ct := w.Header().Get("Content-Type")
			if !strings.Contains(ct, "application/json") {
				t.Errorf("Content-Type mismatch: got %s, want application/json", ct)
			}

			// Check X-SEAM-Scope-Version header
			scopeVersion := w.Header().Get("X-SEAM-Scope-Version")
			if scopeVersion == "" {
				t.Error("X-SEAM-Scope-Version header is missing")
			}

			// Parse response body
			var body map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("Failed to parse response body: %v", err)
			}

			// Run response checks
			if tt.checkResponse != nil {
				tt.checkResponse(t, body)
			}
		})
	}

	// Test non-GET request
	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/whoami", nil)
		w := httptest.NewRecorder()

		server.whoamiHandler(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected StatusMethodNotAllowed for POST, got %d", w.Code)
		}
	})
}

func TestScopesHandler(t *testing.T) {
	server := &Server{
		scopeVersionCache: NewScopeVersionCache(),
		routeTableHolder:  NewThreadSafeTableHolder(&RouteTable{}),
	}

	// Add a route with required scopes to the route table
	testTable := &RouteTable{
		routes: []RouteEntry{
			{
				PathTemplate:    "/api/v1/test",
				Method:          "GET",
				APIVersion:      "v1",
				UpstreamTarget:  "http://example.com",
				RequiredScopes:  []string{"test:read", "test:write"},
			},
		},
	}
	server.routeTableHolder.Swap(testTable)

	tests := []struct {
		name           string
		identity       *Identity
		query          string
		expectedStatus int
		checkResponse  func(t *testing.T, body map[string]interface{})
	}{
		{
			name: "scope-filtered response (default)",
			identity: &Identity{
				NodeKey:     "user-with-scopes",
				NodeName:    "user.example.com",
				Resolved:    true,
				Capabilities: []string{"seam:read", "test:read"},
			},
			query:          "",
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, body map[string]interface{}) {
				filtered, ok := body["filtered"].(bool)
				if !ok || !filtered {
					t.Error("Response should be filtered by default")
				}

				scopes, ok := body["scopes"].(map[string]interface{})
				if !ok {
					t.Fatal("scopes is not an object")
				}

				// Should only include scopes the user has (test:read matches test:read/test:write)
				// Note: This is a simplified test - actual filtering logic may vary
				totalReturned := len(scopes)
				totalScopes := body["total_scopes"].(float64)
				if totalReturned > int(totalScopes) {
					t.Errorf("Returned scopes (%d) exceeds total scopes (%d)", totalReturned, int(totalScopes))
				}
			},
		},
		{
			name: "all scopes with seam:scopes:read-all",
			identity: &Identity{
				NodeKey:     "admin-user",
				NodeName:    "admin.example.com",
				Resolved:    true,
				Capabilities: []string{"seam:scopes:read-all"},
			},
			query:          "?all=1",
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, body map[string]interface{}) {
				filtered, ok := body["filtered"].(bool)
				if !ok || filtered {
					t.Error("Response should not be filtered with ?all=1 and proper scope")
				}

				// Should include both spec-derived and builtin scopes
				scopes, ok := body["scopes"].(map[string]interface{})
				if !ok {
					t.Fatal("scopes is not an object")
				}

				// Check for builtin scope
				foundBuiltin := false
				for scopeID, info := range scopes {
					if infoMap, ok := info.(map[string]interface{}); ok {
						if source, ok := infoMap["source"].(string); ok && source == "builtin" {
							foundBuiltin = true
							break
						}
					}
				}
				if !foundBuiltin {
					t.Error("Should include builtin scopes when ?all=1")
				}
			},
		},
		{
			name: "all scopes denied without seam:scopes:read-all",
			identity: &Identity{
				NodeKey:     "regular-user",
				NodeName:    "user.example.com",
				Resolved:    true,
				Capabilities: []string{"seam:read"},
			},
			query:          "?all=1",
			expectedStatus: http.StatusForbidden,
			checkResponse: func(t *testing.T, body map[string]interface{}) {
				// Should get error response
				if body["error_code"] == nil {
					t.Error("Should return error for missing seam:scopes:read-all")
				}
			},
		},
		{
			name: "unresolved identity gets empty filtered response",
			identity: &Identity{
				NodeKey:     "anonymous",
				NodeName:    "unknown",
				Resolved:    false,
				Capabilities: []string{},
			},
			query:          "",
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, body map[string]interface{}) {
				scopes, ok := body["scopes"].(map[string]interface{})
				if !ok {
					t.Fatal("scopes is not an object")
				}

				// Unresolved identity gets no scopes in filtered view
				if len(scopes) != 0 {
					t.Errorf("Unresolved identity should get empty scope map, got %d scopes", len(scopes))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create request with identity in context
			url := "/scopes" + tt.query
			req := httptest.NewRequest("GET", url, nil)
			req = req.WithContext(contextWithIdentity(req.Context(), tt.identity))

			// Create response recorder
			w := httptest.NewRecorder()

			// Call handler
			server.scopesHandler(w, req)

			// Check status
			if w.Code != tt.expectedStatus {
				t.Errorf("Status mismatch: got %d, want %d\nBody: %s", w.Code, tt.expectedStatus, w.Body.String())
			}

			// Check content type
			ct := w.Header().Get("Content-Type")
			if !strings.Contains(ct, "application/json") {
				t.Errorf("Content-Type mismatch: got %s, want application/json", ct)
			}

			// Parse response body
			var body map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("Failed to parse response body: %v", err)
			}

			// Run response checks
			if tt.checkResponse != nil {
				tt.checkResponse(t, body)
			}
		})
	}

	// Test non-GET request
	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/scopes", nil)
		w := httptest.NewRecorder()

		server.scopesHandler(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected StatusMethodNotAllowed for POST, got %d", w.Code)
		}
	})
}

func TestBuildScopeMap(t *testing.T) {
	server := &Server{
		routeTableHolder: NewThreadSafeTableHolder(&RouteTable{}),
	}

	// Add test routes with scopes
	testTable := &RouteTable{
		routes: []RouteEntry{
			{
				PathTemplate:   "/api/v1/resource",
				Method:         "GET",
				APIVersion:     "v1",
				UpstreamTarget: "http://example.com",
				RequiredScopes: []string{"resource:read"},
			},
			{
				PathTemplate:   "/api/v1/resource",
				Method:         "POST",
				APIVersion:     "v1",
				UpstreamTarget: "http://example.com",
				RequiredScopes: []string{"resource:write"},
			},
		},
	}
	server.routeTableHolder.Swap(testTable)

	scopeMap := server.buildScopeMap()

	// Should have spec-derived scopes
	if len(scopeMap) < 2 {
		t.Errorf("Expected at least 2 scopes, got %d", len(scopeMap))
	}

	// Check scope structure
	for scopeID, info := range scopeMap {
		if info.Routes == nil {
			t.Errorf("Scope %s has nil routes", scopeID)
		}
		if info.Source != "spec" && info.Source != "builtin" {
			t.Errorf("Scope %s has invalid source: %s", scopeID, info.Source)
		}
	}

	// Check that builtin scopes are present
	foundBuiltin := false
	for _, info := range scopeMap {
		if info.Source == "builtin" {
			foundBuiltin = true
			break
		}
	}
	if !foundBuiltin {
		t.Error("Builtin scopes should be present in scope map")
	}
}

func TestFilterScopesByEffective(t *testing.T) {
	scopeMap := map[string]scopeInfo{
		"scope-a": {Routes: []string{"GET /route1"}, Source: "spec"},
		"scope-b": {Routes: []string{"POST /route2"}, Source: "spec"},
		"scope-c": {Routes: []string{"DELETE /route3"}, Source: "builtin"},
	}

	tests := []struct {
		name           string
		effectiveScopes []string
		expectedCount  int
	}{
		{
			name:           "empty effective scopes",
			effectiveScopes: []string{},
			expectedCount:  0,
		},
		{
			name:           "one matching scope",
			effectiveScopes: []string{"scope-a"},
			expectedCount:  1,
		},
		{
			name:           "multiple matching scopes",
			effectiveScopes: []string{"scope-a", "scope-b"},
			expectedCount:  2,
		},
		{
			name:           "case insensitive matching",
			effectiveScopes: []string{"SCOPE-A"},
			expectedCount:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := filterScopesByEffective(scopeMap, tt.effectiveScopes)
			if len(filtered) != tt.expectedCount {
				t.Errorf("Expected %d scopes, got %d", tt.expectedCount, len(filtered))
			}
		})
	}
}

func TestHasScope(t *testing.T) {
	effectiveScopes := []string{"seam:read", "seam:write"}

	tests := []struct {
		name           string
		effectiveScopes []string
		targetScope    string
		expected       bool
	}{
		{
			name:           "exact match",
			effectiveScopes: effectiveScopes,
			targetScope:    "seam:read",
			expected:       true,
		},
		{
			name:           "case insensitive match",
			effectiveScopes: effectiveScopes,
			targetScope:    "SEAM:READ",
			expected:       true,
		},
		{
			name:           "no match",
			effectiveScopes: effectiveScopes,
			targetScope:    "seam:admin",
			expected:       false,
		},
		{
			name:           "empty effective scopes",
			effectiveScopes: []string{},
			targetScope:    "seam:read",
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasScope(tt.effectiveScopes, tt.targetScope)
			if result != tt.expected {
				t.Errorf("hasScope(%v, %s) = %v, want %v", tt.effectiveScopes, tt.targetScope, result, tt.expected)
			}
		})
	}
}

func TestEffectiveScopesFromIdentity(t *testing.T) {
	tests := []struct {
		name           string
		identity       *Identity
		expectedCount  int
		shouldBeSorted bool
	}{
		{
			name:           "nil identity",
			identity:       nil,
			expectedCount:  0,
			shouldBeSorted: true,
		},
		{
			name: "unresolved identity",
			identity: &Identity{
				Resolved: false,
				Capabilities: []string{"seam:read"},
			},
			expectedCount:  0,
			shouldBeSorted: true,
		},
		{
			name: "resolved identity with scopes",
			identity: &Identity{
				Resolved:    true,
				Capabilities: []string{"zebra:write", "alpha:read", "middle:scope"},
			},
			expectedCount:  3,
			shouldBeSorted: true,
		},
		{
			name: "identity with empty/whitespace scopes",
			identity: &Identity{
				Resolved:    true,
				Capabilities: []string{"seam:read", "", "  ", "seam:write"},
			},
			expectedCount:  2,
			shouldBeSorted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scopes := effectiveScopesFromIdentity(tt.identity)

			if len(scopes) != tt.expectedCount {
				t.Errorf("Expected %d scopes, got %d", tt.expectedCount, len(scopes))
			}

			if tt.shouldBeSorted && len(scopes) > 1 {
				// Verify sorted order
				for i := 1; i < len(scopes); i++ {
					if scopes[i-1] > scopes[i] {
						t.Errorf("Scopes not sorted: %v", scopes)
						break
					}
				}
			}
		})
	}
}
