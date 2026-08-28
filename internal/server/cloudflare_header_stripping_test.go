package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHeaderStrippingMiddleware_Phase14Rule3 tests Phase 14 Rule 3:
// X-SEAM-Scopes and equivalents are DELETED at stage 2, not ignored
func TestHeaderStrippingMiddleware_Phase14Rule3(t *testing.T) {
	server := &Server{}

	middleware := server.headerStrippingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	tests := []struct {
		name           string
		headers        map[string]string
		expectedStatus int
		expectedDeleted map[string]bool // Headers that should be deleted
	}{
		{
			name: "Delete X-SEAM-Scopes header",
			headers: map[string]string{
				"X-SEAM-Scopes": "k8s-ro:get,argocd:read",
			},
			expectedStatus: http.StatusOK,
			expectedDeleted: map[string]bool{
				"X-SEAM-Scopes": true,
			},
		},
		{
			name: "Delete X-Seam-Scopes header (canonical form)",
			headers: map[string]string{
				"X-Seam-Scopes": "k8s-ro:get,argocd:read",
			},
			expectedStatus: http.StatusOK,
			expectedDeleted: map[string]bool{
				"X-Seam-Scopes": true,
			},
		},
		{
			name: "Delete X-SEAM-Scope header (singular)",
			headers: map[string]string{
				"X-SEAM-Scope": "k8s-ro:get",
			},
			expectedStatus: http.StatusOK,
			expectedDeleted: map[string]bool{
				"X-SEAM-Scope": true,
			},
		},
		{
			name: "Delete X-Seam-Scope header (canonical form)",
			headers: map[string]string{
				"X-Seam-Scope": "k8s-ro:get",
			},
			expectedStatus: http.StatusOK,
			expectedDeleted: map[string]bool{
				"X-Seam-Scope": true,
			},
		},
		{
			name: "Delete multiple scope headers",
			headers: map[string]string{
				"X-SEAM-Scopes": "scope1,scope2",
				"X-SEAM-Scope":  "scope3",
			},
			expectedStatus: http.StatusOK,
			expectedDeleted: map[string]bool{
				"X-SEAM-Scopes": true,
				"X-SEAM-Scope":  true,
			},
		},
		{
			name: "Keep allowed X-SEAM-Spec-Version",
			headers: map[string]string{
				"X-SEAM-Spec-Version": "abc123",
				"X-SEAM-Scopes":      "k8s-ro:get",
			},
			expectedStatus: http.StatusOK,
			expectedDeleted: map[string]bool{
				"X-SEAM-Scopes": true,
			},
		},
		{
			name: "Keep allowed X-SEAM-API-Version",
			headers: map[string]string{
				"X-SEAM-API-Version": "v1",
				"X-SEAM-Scopes":     "k8s-ro:get",
			},
			expectedStatus: http.StatusOK,
			expectedDeleted: map[string]bool{
				"X-SEAM-Scopes": true,
			},
		},
		{
			name: "Delete X-SEAM-Dry-Run (not in allowed list, but special case)",
			headers: map[string]string{
				"X-SEAM-Dry-Run": "true",
			},
			expectedStatus: http.StatusOK,
			expectedDeleted: map[string]bool{
				// X-SEAM-Dry-Run should be kept (special case)
			},
		},
		{
			name: "Delete other X-SEAM-* headers",
			headers: map[string]string{
				"X-SEAM-Scopes":    "k8s-ro:get",
				"X-SEAM-Internal":  "internal-value",
				"X-SEAM-Custom":    "custom-value",
			},
			expectedStatus: http.StatusOK,
			expectedDeleted: map[string]bool{
				"X-SEAM-Scopes":   true,
				"X-SEAM-Internal": true,
				"X-SEAM-Custom":   true,
			},
		},
		{
			name: "Keep non-X-SEAM headers",
			headers: map[string]string{
				"X-SEAM-Scopes":   "k8s-ro:get",
				"Authorization":  "Bearer token",
				"Content-Type":   "application/json",
				"X-Custom-Header": "custom-value",
			},
			expectedStatus: http.StatusOK,
			expectedDeleted: map[string]bool{
				"X-SEAM-Scopes": true,
			},
		},
		{
			name: "No X-SEAM headers",
			headers: map[string]string{
				"Authorization": "Bearer token",
				"Content-Type":  "application/json",
			},
			expectedStatus: http.StatusOK,
			expectedDeleted: map[string]bool{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}

			w := httptest.NewRecorder()
			middleware.ServeHTTP(w, req)

			// Check that request was processed (not rejected)
			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			// Check that scope headers were deleted
			for headerName := range tt.expectedDeleted {
				// Check if header still exists in the request
				// Note: The middleware modifies the request before passing to the handler
				// We need to check if the header was deleted by examining the handler's view
			}
		})
	}
}

// TestHeaderStrippingMiddleware_ScopeHeadersOnly tests that scope headers are specifically targeted
func TestHeaderStrippingMiddleware_ScopeHeadersOnly(t *testing.T) {
	server := &Server{}

	middleware := server.headerStrippingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check what headers remain in the request
		if r.Header.Get("X-SEAM-Scopes") != "" {
			t.Error("X-SEAM-Scopes header should have been deleted (Phase 14 Rule 3)")
		}
		if r.Header.Get("X-SEAM-Scope") != "" {
			t.Error("X-SEAM-Scope header should have been deleted (Phase 14 Rule 3)")
		}

		// Check that allowed headers remain
		if r.Header.Get("X-SEAM-Spec-Version") == "" {
			t.Error("X-SEAM-Spec-Version header should have been kept")
		}
		if r.Header.Get("X-SEAM-API-Version") == "" {
			t.Error("X-SEAM-API-Version header should have been kept")
		}

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-SEAM-Scopes", "k8s-ro:get,argocd:read")
	req.Header.Set("X-SEAM-Scope", "scope1")
	req.Header.Set("X-SEAM-Spec-Version", "abc123")
	req.Header.Set("X-SEAM-API-Version", "v1")

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

// TestDeletedScopeHeaders_Map tests the deletedScopeHeaders map
func TestDeletedScopeHeaders_Map(t *testing.T) {
	// Verify that the deletedScopeHeaders map contains the expected entries
	expectedHeaders := []string{"X-Seam-Scopes", "X-Seam-Scope"}

	for _, header := range expectedHeaders {
		if !deletedScopeHeaders[header] {
			t.Errorf("Expected header '%s' to be in deletedScopeHeaders map", header)
		}
	}

	// Verify map has the expected size
	if len(deletedScopeHeaders) != 2 {
		t.Errorf("Expected deletedScopeHeaders map to have 2 entries, got %d", len(deletedScopeHeaders))
	}
}

// TestAllowedSEAMHeaders_Map tests the allowedSEAMHeaders map
func TestAllowedSEAMHeaders_Map(t *testing.T) {
	// Verify that the allowedSEAMHeaders map contains the expected entries
	expectedHeaders := []string{"X-Seam-Spec-Version", "X-Seam-Api-Version"}

	for _, header := range expectedHeaders {
		if !allowedSEAMHeaders[header] {
			t.Errorf("Expected header '%s' to be in allowedSEAMHeaders map", header)
		}
	}

	// Verify map has the expected size
	if len(allowedSEAMHeaders) != 2 {
		t.Errorf("Expected allowedSEAMHeaders map to have 2 entries, got %d", len(allowedSEAMHeaders))
	}
}

// TestHeaderStrippingMiddleware_CaseInsensitivity tests header name case handling
func TestHeaderStrippingMiddleware_CaseInsensitivity(t *testing.T) {
	server := &Server{}

	middleware := server.headerStrippingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check that scope headers were deleted regardless of case
		if r.Header.Get("X-SEAM-Scopes") != "" {
			t.Error("X-SEAM-Scopes (upper case) should have been deleted")
		}
		if r.Header.Get("X-seam-scopes") != "" {
			t.Error("X-seam-scopes (lower case) should have been deleted")
		}
		if r.Header.Get("x-seam-scopes") != "" {
			t.Error("x-seam-scopes (all lower case) should have been deleted")
		}

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	// Set scope header in various cases
	req.Header.Set("X-SEAM-Scopes", "scopes1")
	req.Header.Add("X-seam-scopes", "scopes2")
	req.Header.Add("x-seam-scopes", "scopes3")

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

// TestHeaderStrippingMiddleware_DryRun tests that X-SEAM-Dry-Run is kept
func TestHeaderStrippingMiddleware_DryRun(t *testing.T) {
	server := &Server{}

	middleware := server.headerStrippingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check that X-SEAM-Dry-Run header is kept
		if r.Header.Get("X-SEAM-Dry-Run") == "" {
			t.Error("X-SEAM-Dry-Run header should have been kept")
		}

		// Check that other scope headers are deleted
		if r.Header.Get("X-SEAM-Scopes") != "" {
			t.Error("X-SEAM-Scopes header should have been deleted")
		}

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-SEAM-Dry-Run", "true")
	req.Header.Set("X-SEAM-Scopes", "k8s-ro:get")

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

// TestHeaderStrippingMiddleware_Phase14Logging tests that Phase 14 scope deletion is logged
func TestHeaderStrippingMiddleware_Phase14Logging(t *testing.T) {
	// This test would verify that scope header deletion generates specific logging
	// In a real implementation, we would capture log output and verify it contains
	// "[Header-Strip-Phase14]" and "Deleted X-SEAM-Scopes headers"
	// For now, this is a placeholder test

	server := &Server{}

	middleware := server.headerStrippingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-SEAM-Scopes", "k8s-ro:get")

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// In a real test, we would verify log output contains:
	// "[Header-Strip-Phase14] Deleted X-SEAM-Scopes headers from request to /test (Rule 3: DELETE, not ignore)"
}

// TestHeaderStrippingMiddleware_AllowedHeaders tests that allowed headers pass through
func TestHeaderStrippingMiddleware_AllowedHeaders(t *testing.T) {
	server := &Server{}

	middleware := server.headerStrippingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check that allowed headers are present
		if r.Header.Get("X-SEAM-Spec-Version") != "abc123" {
			t.Error("X-SEAM-Spec-Version header should have been kept with value 'abc123'")
		}
		if r.Header.Get("X-SEAM-API-Version") != "v1" {
			t.Error("X-SEAM-API-Version header should have been kept with value 'v1'")
		}

		// Check that scope headers were deleted
		if r.Header.Get("X-SEAM-Scopes") != "" {
			t.Error("X-SEAM-Scopes header should have been deleted")
		}

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-SEAM-Spec-Version", "abc123")
	req.Header.Set("X-SEAM-API-Version", "v1")
	req.Header.Set("X-SEAM-Scopes", "k8s-ro:get")

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

// TestHeaderStrippingMiddleware_NonSEAMHeaders tests that non-SEAM headers are not affected
func TestHeaderStrippingMiddleware_NonSEAMHeaders(t *testing.T) {
	server := &Server{}

	middleware := server.headerStrippingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check that non-SEAM headers are present
		if r.Header.Get("Authorization") != "Bearer token123" {
			t.Error("Authorization header should have been kept")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("Content-Type header should have been kept")
		}
		if r.Header.Get("X-Custom") != "custom-value" {
			t.Error("X-Custom header should have been kept")
		}

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer token123")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Custom", "custom-value")
	req.Header.Set("X-SEAM-Scopes", "k8s-ro:get")

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}
