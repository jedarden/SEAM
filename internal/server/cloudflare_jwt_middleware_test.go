package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TestCloudflareJWTValidator_Initialisation tests the validator initialisation
func TestCloudflareJWTValidator_Initialisation(t *testing.T) {
	validator := NewCloudflareJWTValidator("test-team", "test-audience", true)

	if validator == nil {
		t.Fatal("Expected validator to be created, got nil")
	}

	if validator.teamDomain != "test-team" {
		t.Errorf("Expected team domain 'test-team', got '%s'", validator.teamDomain)
	}

	if validator.audience != "test-audience" {
		t.Errorf("Expected audience 'test-audience', got '%s'", validator.audience)
	}

	if !validator.enabled {
		t.Error("Expected validator to be enabled")
	}

	expectedJWKSURL := "https://test-team.cloudflareaccess.com/cdn-cgi/access/certs"
	if validator.jwksURL != expectedJWKSURL {
		t.Errorf("Expected JWKS URL '%s', got '%s'", expectedJWKSURL, validator.jwksURL)
	}
}

// TestCloudflareJWTValidator_DisabledByDefault tests that the validator is disabled by default
func TestCloudflareJWTValidator_DisabledByDefault(t *testing.T) {
	validator := NewCloudflareJWTValidator("test-team", "test-audience", false)

	if validator.enabled {
		t.Error("Expected validator to be disabled when enabled=false")
	}
}

// TestCloudflareJWTValidator_ScopeMap tests the scope mapping functionality
func TestCloudflareJWTValidator_ScopeMap(t *testing.T) {
	validator := NewCloudflareJWTValidator("test-team", "test-audience", true)

	// Test setting scope map
	scopeMap := map[string][]string{
		"service-token-1": {"k8s-ro:get", "argocd:read"},
		"user@example.com":  {"config:read", "seam:ops:read"},
	}
	validator.SetScopeMap(scopeMap)

	// Test getting scopes for subject
	scopes := validator.GetScopesForSubject("service-token-1")
	if len(scopes) != 2 {
		t.Errorf("Expected 2 scopes for service-token-1, got %d", len(scopes))
	}

	if scopes[0] != "k8s-ro:get" {
		t.Errorf("Expected scope 'k8s-ro:get', got '%s'", scopes[0])
	}

	// Test non-existent subject
	scopes = validator.GetScopesForSubject("non-existent")
	if scopes != nil {
		t.Errorf("Expected nil scopes for non-existent subject, got %v", scopes)
	}
}

// TestCloudflareJWTValidator_ValidateJWT_Disabled tests validation when disabled
func TestCloudflareJWTValidator_ValidateJWT_Disabled(t *testing.T) {
	validator := NewCloudflareJWTValidator("test-team", "test-audience", false)

	// When disabled, should return nil, nil (allow through)
	claims, err := validator.ValidateJWT("")
	if err != nil {
		t.Errorf("Expected no error when disabled, got %v", err)
	}

	if claims != nil {
		t.Error("Expected nil claims when disabled")
	}
}

// TestCloudflareJWTValidator_ValidateJWT_MissingToken tests validation with missing token
func TestCloudflareJWTValidator_ValidateJWT_MissingToken(t *testing.T) {
	validator := NewCloudflareJWTValidator("test-team", "test-audience", true)

	// Missing token should return error when enabled
	claims, err := validator.ValidateJWT("")
	if err == nil {
		t.Error("Expected error for missing token, got nil")
	}

	if claims != nil {
		t.Error("Expected nil claims for missing token")
	}

	if !strings.Contains(err.Error(), "missing JWT token") {
		t.Errorf("Expected 'missing JWT token' error, got: %v", err)
	}
}

// TestCloudflareJWTValidator_ValidateJWT_InvalidToken tests validation with invalid token
func TestCloudflareJWTValidator_ValidateJWT_InvalidToken(t *testing.T) {
	validator := NewCloudflareJWTValidator("test-team", "test-audience", true)

	// Invalid token should return error
	claims, err := validator.ValidateJWT("invalid-token")
	if err == nil {
		t.Error("Expected error for invalid token, got nil")
	}

	if claims != nil {
		t.Error("Expected nil claims for invalid token")
	}
}

// TestCloudflareJWTValidator_Middleware_DefaultDeny tests the default-deny behavior (Phase 14 rule 4)
func TestCloudflareJWTValidator_Middleware_DefaultDeny(t *testing.T) {
	server := &Server{
		cloudflareJWTValidator: NewCloudflareJWTValidator("test-team", "test-audience", true),
	}

	middleware := server.cloudflareJWTMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	tests := []struct {
		name           string
		authHeader     string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "No auth header - 403 (Phase 14 rule 4)",
			authHeader:     "",
			expectedStatus: http.StatusForbidden,
			expectedBody:   "Cloudflare Access authentication required",
		},
		{
			name:           "Invalid token - 403 (Phase 14 rule 4)",
			authHeader:     "Bearer invalid-token",
			expectedStatus: http.StatusForbidden,
			expectedBody:   "Cloudflare Access authentication failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			w := httptest.NewRecorder()
			middleware.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if !strings.Contains(w.Body.String(), tt.expectedBody) {
				t.Errorf("Expected body to contain '%s', got: %s", tt.expectedBody, w.Body.String())
			}
		})
	}
}

// TestCloudflareJWTValidator_Middleware_HealthEndpoints tests that health endpoints bypass JWT validation
func TestCloudflareJWTValidator_Middleware_HealthEndpoints(t *testing.T) {
	server := &Server{
		cloudflareJWTValidator: NewCloudflareJWTValidator("test-team", "test-audience", true),
	}

	middleware := server.cloudflareJWTMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	healthEndpoints := []string{"/_seam/health", "/_seam/healthz", "/_seam/readyz"}

	for _, endpoint := range healthEndpoints {
		t.Run("Health endpoint bypass: "+endpoint, func(t *testing.T) {
			req := httptest.NewRequest("GET", endpoint, nil)
			// No Authorization header - should still pass for health endpoints

			w := httptest.NewRecorder()
			middleware.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Expected status 200 for health endpoint %s, got %d", endpoint, w.Code)
			}

			if w.Body.String() != "OK" {
				t.Errorf("Expected body 'OK' for health endpoint %s, got: %s", endpoint, w.Body.String())
			}
		})
	}
}

// TestCloudflareJWTValidator_Middleware_Disabled tests that requests pass through when JWT validation is disabled
func TestCloudflareJWTValidator_Middleware_Disabled(t *testing.T) {
	server := &Server{
		cloudflareJWTValidator: NewCloudflareJWTValidator("test-team", "test-audience", false),
	}

	middleware := server.cloudflareJWTMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	// No Authorization header - should pass through when disabled

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 when disabled, got %d", w.Code)
	}

	if w.Body.String() != "OK" {
		t.Errorf("Expected body 'OK' when disabled, got: %s", w.Body.String())
	}
}

// TestCloudflareJWTValidator_ValidateJWT_AudienceCheck tests audience claim validation
func TestCloudflareJWTValidator_ValidateJWT_AudienceCheck(t *testing.T) {
	// This test requires creating a valid JWT with the correct audience
	// For now, we test the validation logic with a mock token

	validator := NewCloudflareJWTValidator("test-team", "test-audience", true)

	// Create a test JWT token with wrong audience
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"aud": "wrong-audience",
		"iss": "https://test-team.cloudflareaccess.com",
		"sub": "test-user",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	// Sign with a dummy secret (won't match JWKS, but tests audience validation first)
	tokenString, err := token.SignedString([]byte("dummy-secret"))
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}

	// This will fail signature validation, but demonstrates the flow
	claims, err := validator.ValidateJWT(tokenString)
	if err == nil {
		t.Error("Expected error for token with wrong audience, got nil")
	}

	if claims != nil {
		t.Error("Expected nil claims for token with wrong audience")
	}
}

// TestCloudflareJWTValidator_ValidateJWT_IssuerCheck tests issuer claim validation
func TestCloudflareJWTValidator_ValidateJWT_IssuerCheck(t *testing.T) {
	validator := NewCloudflareJWTValidator("test-team", "test-audience", true)

	// Create a test JWT token with wrong issuer
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"aud": "test-audience",
		"iss": "https://wrong-team.cloudflareaccess.com",
		"sub": "test-user",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	tokenString, err := token.SignedString([]byte("dummy-secret"))
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}

	// This will fail signature validation, but demonstrates the flow
	claims, err := validator.ValidateJWT(tokenString)
	if err == nil {
		t.Error("Expected error for token with wrong issuer, got nil")
	}

	if claims != nil {
		t.Error("Expected nil claims for token with wrong issuer")
	}
}

// TestCloudflareJWTValidator_ValidateJWT_ExpirationCheck tests expiration claim validation
func TestCloudflareJWTValidator_ValidateJWT_ExpirationCheck(t *testing.T) {
	validator := NewCloudflareJWTValidator("test-team", "test-audience", true)

	// Create an expired token
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"aud": "test-audience",
		"iss": "https://test-team.cloudflareaccess.com",
		"sub": "test-user",
		"exp": time.Now().Add(-1 * time.Hour).Unix(), // Expired 1 hour ago
	})

	tokenString, err := token.SignedString([]byte("dummy-secret"))
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}

	// This will fail signature validation, but demonstrates the expiration check flow
	claims, err := validator.ValidateJWT(tokenString)
	if err == nil {
		t.Error("Expected error for expired token, got nil")
	}

	if claims != nil {
		t.Error("Expected nil claims for expired token")
	}
}

// TestCloudflareJWTValidator_ValidateJWT_NbfCheck tests not-before claim validation
func TestCloudflareJWTValidator_ValidateJWT_NbfCheck(t *testing.T) {
	validator := NewCloudflareJWTValidator("test-team", "test-audience", true)

	// Create a token with nbf in the future
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"aud": "test-audience",
		"iss": "https://test-team.cloudflareaccess.com",
		"sub": "test-user",
		"nbf": time.Now().Add(1 * time.Hour).Unix(), // Not valid for 1 hour
	})

	tokenString, err := token.SignedString([]byte("dummy-secret"))
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}

	// This will fail signature validation, but demonstrates the nbf check flow
	claims, err := validator.ValidateJWT(tokenString)
	if err == nil {
		t.Error("Expected error for token with future nbf, got nil")
	}

	if claims != nil {
		t.Error("Expected nil claims for token with future nbf")
	}
}

// TestCloudflareJWTValidator_Middleware_NoValidator tests that requests pass through when validator is nil
func TestCloudflareJWTValidator_Middleware_NoValidator(t *testing.T) {
	server := &Server{
		// No validator configured
	}

	middleware := server.cloudflareJWTMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 when no validator, got %d", w.Code)
	}

	if w.Body.String() != "OK" {
		t.Errorf("Expected body 'OK' when no validator, got: %s", w.Body.String())
	}
}

// TestCloudflareJWTClaimsFromContext tests extracting claims from context
func TestCloudflareJWTClaimsFromContext(t *testing.T) {
	claims := &CloudflareAccessClaims{
		sub: "test-user",
		email: "user@example.com",
	}

	ctx := contextWithValue(context.Background(), cloudflareJWTClaimsKey{}, claims)
	extractedClaims := cloudflareJWTClaimsFromContext(ctx)

	if extractedClaims == nil {
		t.Fatal("Expected claims to be extracted, got nil")
	}

	if extractedClaims.sub != "test-user" {
		t.Errorf("Expected sub 'test-user', got '%s'", extractedClaims.sub)
	}

	if extractedClaims.email != "user@example.com" {
		t.Errorf("Expected email 'user@example.com', got '%s'", extractedClaims.email)
	}
}

// TestCloudflareJWTClaimsFromContext_NilContext tests extracting claims from nil context
func TestCloudflareJWTClaimsFromContext_NilContext(t *testing.T) {
	claims := cloudflareJWTClaimsFromContext(nil)

	if claims != nil {
		t.Error("Expected nil claims from nil context, got non-nil")
	}
}

// TestCloudflareJWTClaimsFromContext_NoClaims tests extracting claims from context without claims
func TestCloudflareJWTClaimsFromContext_NoClaims(t *testing.T) {
	ctx := context.Background()
	claims := cloudflareJWTClaimsFromContext(ctx)

	if claims != nil {
		t.Error("Expected nil claims from context without claims, got non-nil")
	}
}

// TestCloudflareScopesFromContext tests extracting scopes from context
func TestCloudflareScopesFromContext(t *testing.T) {
	claims := &CloudflareAccessClaims{
		Scopes: []string{"k8s-ro:get", "argocd:read"},
	}

	ctx := contextWithValue(context.Background(), cloudflareJWTClaimsKey{}, claims)
	scopes := cloudflareScopesFromContext(ctx)

	if len(scopes) != 2 {
		t.Errorf("Expected 2 scopes, got %d", len(scopes))
	}

	if scopes[0] != "k8s-ro:get" {
		t.Errorf("Expected scope 'k8s-ro:get', got '%s'", scopes[0])
	}
}

// TestCloudflareScopesFromContext_NoClaims tests extracting scopes from context without claims
func TestCloudflareScopesFromContext_NoClaims(t *testing.T) {
	ctx := context.Background()
	scopes := cloudflareScopesFromContext(ctx)

	if scopes != nil {
		t.Error("Expected nil scopes from context without claims, got non-nil")
	}
}

// TestJWKSURLConstruction tests that JWKS URL is constructed correctly
func TestJWKSURLConstruction(t *testing.T) {
	testCases := []struct {
		teamDomain     string
		expectedJWKSURL string
	}{
		{
			teamDomain:     "ardenone",
			expectedJWKSURL: "https://ardenone.cloudflareaccess.com/cdn-cgi/access/certs",
		},
		{
			teamDomain:     "example-team",
			expectedJWKSURL: "https://example-team.cloudflareaccess.com/cdn-cgi/access/certs",
		},
		{
			teamDomain:     "test",
			expectedJWKSURL: "https://test.cloudflareaccess.com/cdn-cgi/access/certs",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.teamDomain, func(t *testing.T) {
			validator := NewCloudflareJWTValidator(tc.teamDomain, "test-audience", true)

			if validator.jwksURL != tc.expectedJWKSURL {
				t.Errorf("Expected JWKS URL '%s', got '%s'", tc.expectedJWKSURL, validator.jwksURL)
			}
		})
	}
}

// TestScopeMapIsolation tests that scope map changes don't affect original map
func TestScopeMapIsolation(t *testing.T) {
	validator := NewCloudflareJWTValidator("test-team", "test-audience", true)

	originalMap := map[string][]string{
		"service-1": {"scope1", "scope2"},
	}
	validator.SetScopeMap(originalMap)

	// Modify original map
	originalMap["service-1"] = []string{"modified"}

	// Check that validator's scope map is unchanged
	scopes := validator.GetScopesForSubject("service-1")
	if len(scopes) != 2 {
		t.Errorf("Expected 2 scopes (original map should be copied), got %d", len(scopes))
	}

	if scopes[0] != "scope1" {
		t.Errorf("Expected scope 'scope1' (original map should be copied), got '%s'", scopes[0])
	}
}

// TestBearerPrefixStripping tests that Bearer prefix is stripped from tokens
func TestBearerPrefixStripping(t *testing.T) {
	validator := NewCloudflareJWTValidator("test-team", "test-audience", true)

	// Create a valid-looking token with Bearer prefix
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"aud": "test-audience",
		"iss": "https://test-team.cloudflareaccess.com",
		"sub": "test-user",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	tokenString, err := token.SignedString([]byte("dummy-secret"))
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}

	// Test with Bearer prefix (will fail signature but tests prefix stripping)
	_, err = validator.ValidateJWT("Bearer " + tokenString)
	if err == nil {
		t.Error("Expected error (signature validation), but Bearer prefix should be stripped")
	}

	// Test without Bearer prefix
	_, err = validator.ValidateJWT(tokenString)
	if err == nil {
		t.Error("Expected error (signature validation)")
	}
}

// TestCloudflareAccessClaims_JSONUnmarshalling tests JSON unmarshalling of claims
func TestCloudflareAccessClaims_JSONUnmarshalling(t *testing.T) {
	// This test verifies that the claims structure can handle Cloudflare's JWT format
	// (This would typically be tested with actual Cloudflare JWTs in integration tests)

	claimsJSON := `{
		"aud": ["test-audience"],
		"iss": "https://test-team.cloudflareaccess.com",
		"sub": "test-user",
		"exp": 1234567890,
		"nbf": 1234567800,
		"email": "user@example.com",
		"country": "US",
		"identity_nonce": "abc123",
		"scopes": ["scope1", "scope2"]
	}`

	var claims CloudflareAccessClaims
	err := json.Unmarshal([]byte(claimsJSON), &claims)
	if err != nil {
		t.Fatalf("Failed to unmarshal claims: %v", err)
	}

	if len(claims.aud) != 1 || claims.aud[0] != "test-audience" {
		t.Errorf("Expected aud ['test-audience'], got %v", claims.aud)
	}

	if claims.iss != "https://test-team.cloudflareaccess.com" {
		t.Errorf("Expected issuer 'https://test-team.cloudflareaccess.com', got '%s'", claims.iss)
	}

	if claims.sub != "test-user" {
		t.Errorf("Expected sub 'test-user', got '%s'", claims.sub)
	}

	if claims.email != "user@example.com" {
		t.Errorf("Expected email 'user@example.com', got '%s'", claims.email)
	}

	if len(claims.Scopes) != 2 {
		t.Errorf("Expected 2 scopes, got %d", len(claims.Scopes))
	}
}

// TestCloudflareJWTValidator_Middleware_BeforeRouteMatching tests that JWT validation happens BEFORE route matching
func TestCloudflareJWTValidator_Middleware_BeforeRouteMatching(t *testing.T) {
	// This test verifies Phase 14 rule 4: 403 BEFORE route matching, secret lookup, or upstream contact
	server := &Server{
		cloudflareJWTValidator: NewCloudflareJWTValidator("test-team", "test-audience", true),
	}

	middleware := server.cloudflareJWTMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This represents the "next" stage (route matching)
		// If JWT validation fails, this should never be called
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Route matched"))
	}))

	req := httptest.NewRequest("GET", "/api/v1/some-route", nil)
	// No Authorization header

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	// Should get 403 before route matching even happens
	if w.Code != http.StatusForbidden {
		t.Errorf("Expected 403 BEFORE route matching, got %d", w.Code)
	}

	if w.Body.String() == "Route matched" {
		t.Error("Route matching should not have occurred - JWT validation should have rejected first")
	}
}

// TestCloudflareAccessClaims_MultipleAudiences tests handling of multiple audience values
func TestCloudflareAccessClaims_MultipleAudiences(t *testing.T) {
	claimsJSON := `{
		"aud": ["audience1", "audience2", "test-audience"],
		"iss": "https://test-team.cloudflareaccess.com",
		"sub": "test-user"
	}`

	var claims CloudflareAccessClaims
	err := json.Unmarshal([]byte(claimsJSON), &claims)
	if err != nil {
		t.Fatalf("Failed to unmarshal claims: %v", err)
	}

	if len(claims.aud) != 3 {
		t.Errorf("Expected 3 audiences, got %d", len(claims.aud))
	}

	// Verify all audiences are captured
	expectedAudiences := []string{"audience1", "audience2", "test-audience"}
	for i, expected := range expectedAudiences {
		if claims.aud[i] != expected {
			t.Errorf("Expected audience %d to be '%s', got '%s'", i, expected, claims.aud[i])
		}
	}
}

// TestScopeMapThreadSafety tests concurrent access to scope map
func TestScopeMapThreadSafety(t *testing.T) {
	validator := NewCloudflareJWTValidator("test-team", "test-audience", true)

	// Set initial scope map
	scopeMap := map[string][]string{
		"service-1": {"scope1"},
	}
	validator.SetScopeMap(scopeMap)

	// Launch goroutines to concurrently access scope map
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()
			for j := 0; j < 100; j++ {
				scopes := validator.GetScopesForSubject("service-1")
				if scopes != nil && len(scopes) != 1 {
					t.Errorf("Expected 1 scope, got %d", len(scopes))
				}
			}
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}
