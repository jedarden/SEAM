package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOperatorScopeMiddleware_ConfigStatus verifies that /config/status requires seam:ops:read scope
func TestOperatorScopeMiddleware_ConfigStatus(t *testing.T) {
	tests := []struct {
		name           string
		hasScope       bool
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "with seam:ops:read scope - allowed",
			hasScope:       true,
			expectedStatus: http.StatusOK,
			expectedBody:   "", // Success response varies
		},
		{
			name:           "without seam:ops:read scope - denied",
			hasScope:       false,
			expectedStatus: http.StatusForbidden,
			expectedBody:   "seam:ops:read",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{
				identityResolver: NewIdentityResolver(),
			}

			// Create a test handler that returns 200 OK
			handlerCalled := false
			nextHandler := func(w http.ResponseWriter, r *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"status":"ok"}`))
			}

			// Wrap with scope middleware
			middleware := s.operatorScopeMiddleware("seam:ops:read", nextHandler)

			// Create test request with identity context
			req := httptest.NewRequest(http.MethodGet, "/config/status", nil)
			identity := &Identity{
				NodeName:     "test-node",
				NodeKey:      "test-key",
				Resolved:     tt.hasScope, // Only resolved if we want the scope check to pass
				Capabilities: []string{},
			}
			if tt.hasScope {
				identity.Capabilities = []string{"seam:ops:read"}
			}
			ctx := contextWithIdentity(req.Context(), identity)
			req = req.WithContext(ctx)

			// Record response
			w := httptest.NewRecorder()
			middleware.ServeHTTP(w, req)

			// Check status
			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			// Check body contains expected content
			body := w.Body.String()
			if tt.expectedBody != "" && !strings.Contains(body, tt.expectedBody) {
				t.Errorf("expected body to contain %q, got %q", tt.expectedBody, body)
			}

			// Verify handler was only called when scope was present
			if tt.hasScope && !handlerCalled {
				t.Error("handler should have been called when scope is present")
			}
			if !tt.hasScope && handlerCalled {
				t.Error("handler should not have been called when scope is missing")
			}
		})
	}
}

// TestOperatorScopeMiddleware_HealthCredentials verifies that /health/credentials requires seam:ops:read
func TestOperatorScopeMiddleware_HealthCredentials(t *testing.T) {
	s := &Server{
		identityResolver: NewIdentityResolver(),
	}

	handlerCalled := false
	nextHandler := func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"healthy":true}`))
	}

	middleware := s.operatorScopeMiddleware("seam:ops:read", nextHandler)

	// Test without scope - should deny
	req := httptest.NewRequest(http.MethodGet, "/health/credentials", nil)
	identity := &Identity{
		NodeName:     "test-node",
		NodeKey:      "test-key",
		Resolved:     true,
		Capabilities: []string{}, // No scopes
	}
	ctx := contextWithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403 without scope, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "seam:ops:read") {
		t.Errorf("expected 403 response to name the required scope, got %q", body)
	}

	if handlerCalled {
		t.Error("handler should not have been called without scope")
	}
}

// TestOperatorScopeMiddleware_HealthUpstreams verifies that /health/upstreams requires seam:ops:read
func TestOperatorScopeMiddleware_HealthUpstreams(t *testing.T) {
	s := &Server{
		identityResolver: NewIdentityResolver(),
	}

	handlerCalled := false
	nextHandler := func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"upstreams":[{"name":"test","healthy":true}]}`))
	}

	middleware := s.operatorScopeMiddleware("seam:ops:read", nextHandler)

	// Test with scope - should allow
	req := httptest.NewRequest(http.MethodGet, "/health/upstreams", nil)
	identity := &Identity{
		NodeName:     "test-node",
		NodeKey:      "test-key",
		Resolved:     true,
		Capabilities: []string{"seam:ops:read"},
	}
	ctx := contextWithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 with scope, got %d", w.Code)
	}

	if !handlerCalled {
		t.Error("handler should have been called with scope")
	}
}

// TestOperatorScopeMiddleware_UnresolvedIdentity verifies that unresolved identities are denied
func TestOperatorScopeMiddleware_UnresolvedIdentity(t *testing.T) {
	s := &Server{
		identityResolver: NewIdentityResolver(),
	}

	handlerCalled := false
	nextHandler := func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}

	middleware := s.operatorScopeMiddleware("seam:ops:read", nextHandler)

	// Test with unresolved identity - should deny even with scope in capabilities
	req := httptest.NewRequest(http.MethodGet, "/config/status", nil)
	identity := &Identity{
		NodeName:     "test-node",
		NodeKey:      "test-key",
		Resolved:     false,                     // Unresolved
		Capabilities: []string{"seam:ops:read"}, // Has scope but unresolved
	}
	ctx := contextWithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for unresolved identity, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "seam:ops:read") {
		t.Errorf("expected 403 response to name the required scope, got %q", body)
	}

	if handlerCalled {
		t.Error("handler should not have been called for unresolved identity")
	}
}

// TestOperatorScopeMiddleware_NoIdentity verifies that requests without identity are denied
func TestOperatorScopeMiddleware_NoIdentity(t *testing.T) {
	s := &Server{
		identityResolver: NewIdentityResolver(),
	}

	handlerCalled := false
	nextHandler := func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}

	middleware := s.operatorScopeMiddleware("seam:ops:read", nextHandler)

	// Test without identity in context - should deny
	req := httptest.NewRequest(http.MethodGet, "/config/status", nil)
	// No identity added to context

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403 without identity, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "seam:ops:read") {
		t.Errorf("expected 403 response to name the required scope, got %q", body)
	}

	if handlerCalled {
		t.Error("handler should not have been called without identity")
	}
}

// TestOperatorScopeMiddleware_ErrorResponseFormat verifies the 403 response format
func TestOperatorScopeMiddleware_ErrorResponseFormat(t *testing.T) {
	s := &Server{
		identityResolver: NewIdentityResolver(),
	}

	nextHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}

	middleware := s.operatorScopeMiddleware("seam:ops:read", nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/config/status", nil)
	identity := &Identity{
		NodeName:     "test-node",
		NodeKey:      "test-key",
		Resolved:     true,
		Capabilities: []string{}, // Missing required scope
	}
	ctx := contextWithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	// Check status
	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", w.Code)
	}

	// Check response is JSON
	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("expected Content-Type to contain application/json, got %q", contentType)
	}

	// Parse response and verify structure
	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}

	// Verify error response structure. The public envelope carries the code in
	// the "error" field (ErrorResponse in errors.go), not "code".
	if code, ok := response["error"].(string); !ok || code != "forbidden" {
		t.Errorf("expected error=forbidden, got %v", response["error"])
	}

	// Verify the scope name is in the message
	message, ok := response["message"].(string)
	if !ok {
		t.Fatalf("expected message field in response")
	}
	if !strings.Contains(message, "seam:ops:read") {
		t.Errorf("expected message to contain scope name, got %q", message)
	}
}

// TestOperatorScopeMiddleware_CaseInsensitive verifies scope checking is case-insensitive
func TestOperatorScopeMiddleware_CaseInsensitive(t *testing.T) {
	s := &Server{
		identityResolver: NewIdentityResolver(),
	}

	tests := []string{
		"seam:ops:read", // Exact match
		"SEAM:OPS:READ", // Uppercase
		"Seam:Ops:Read", // Mixed case
		"seam:OPS:read", // Partial case
	}

	for _, scope := range tests {
		t.Run(scope, func(t *testing.T) {
			handlerCalled := false
			nextHandler := func(w http.ResponseWriter, r *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
			}

			middleware := s.operatorScopeMiddleware("seam:ops:read", nextHandler)

			req := httptest.NewRequest(http.MethodGet, "/config/status", nil)
			identity := &Identity{
				NodeName:     "test-node",
				NodeKey:      "test-key",
				Resolved:     true,
				Capabilities: []string{scope},
			}
			ctx := contextWithIdentity(req.Context(), identity)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			middleware.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("expected status 200 with scope %q, got %d", scope, w.Code)
			}

			if !handlerCalled {
				t.Error("handler should have been called with scope")
			}
		})
	}
}
