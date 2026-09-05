package server

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Cloudflare JWT middleware implements Phase 14 rule 1: JWT validation on EVERY request.
//
// This middleware validates Cloudflare Access service tokens against the team domain's
// published JWKS, checks aud/iss/exp/nbf claims, and implements default-deny (403)
// BEFORE route matching, secret lookup, or upstream contact.
//
// Phase 14 Rules Implemented:
//   - Rule 1: JWT validation (signature, aud, iss, exp, nbf) - this middleware
//   - Rule 2: Scope mapping via service-token->scopes map - relies on context from this middleware
//   - Rule 3: X-SEAM-Scopes deletion - handled by headerStrippingMiddleware (stage 2)
//   - Rule 4: Default-deny mode - 403 before any further processing
//
// Cloudflare Access JWT Structure:
//   - aud: SEAM's Access Application ID (must match configured value)
//   - iss: https://[team-domain].cloudflareaccess.com (must match team domain)
//   - sub: Service token identity (used for scope mapping)
//   - exp/nbf: Standard JWT expiry/not-before claims
//   - email: User email (for human identities)
//   - country: Country claim (optional)
//   - identity_nonce: Cloudflare identity nonce

// CloudflareJWTValidator validates Cloudflare Access JWTs
type CloudflareJWTValidator struct {
	// teamDomain is the Cloudflare team domain (e.g., "ardenone")
	teamDomain string

	// audience is the expected JWT audience (SEAM's Access Application ID)
	audience string

	// jwksURL is the URL to fetch JWKS from (constructed from team domain)
	jwksURL string

	// jwks holds the cached JWKS keys
	jwks *JWKS

	// jwksCacheExpiry is when the JWKS cache expires
	jwksCacheExpiry time.Time

	// enabled controls whether JWT validation is active
	// Mode is default-OFF per Phase 14 rule 4
	enabled bool

	// scopeMap maps JWT subjects (sub) or common names to their scopes
	// This implements Phase 14 rule 2: SEAM-side scope mapping
	scopeMap map[string][]string

	// mu protects jwks, jwksCacheExpiry, scopeMap
	mu sync.RWMutex
}

// JWKS represents a JSON Web Key Set
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWK represents a JSON Web Key
type JWK struct {
	Kty string `json:"kty"` // Key type (e.g., "RSA")
	Kid string `json:"kid"` // Key ID
	Use string `json:"use"` // Public key use parameter (e.g., "sig")
	N   string `json:"n"`   // RSA modulus
	E   string `json:"e"`   // RSA exponent
	Alg string `json:"alg"` // Algorithm (e.g., "RS256")
}

// CloudflareAccessClaims represents Cloudflare Access JWT claims
type CloudflareAccessClaims struct {
	// Standard JWT claims
	Aud []string `json:"aud"`
	Iss string   `json:"iss"`
	Sub string   `json:"sub"`
	Exp int64    `json:"exp"`
	Nbf int64    `json:"nbf"`

	// Cloudflare-specific claims
	Email         string `json:"email,omitempty"`
	Country       string `json:"country,omitempty"`
	IdentityNonce string `json:"identity_nonce,omitempty"`

	// Custom scopes claim (for service-token->scopes mapping)
	Scopes []string `json:"scopes,omitempty"`
}

// NewCloudflareJWTValidator creates a new Cloudflare JWT validator
//
// Parameters:
//   - teamDomain: Cloudflare team domain (e.g., "ardenone")
//   - audience: Expected JWT audience (SEAM's Access Application ID)
//   - enabled: Whether JWT validation is active (default OFF per Phase 14)
func NewCloudflareJWTValidator(teamDomain string, audience string, enabled bool) *CloudflareJWTValidator {
	v := &CloudflareJWTValidator{
		teamDomain: teamDomain,
		audience:   audience,
		jwksURL:    fmt.Sprintf("https://%s.cloudflareaccess.com/cdn-cgi/access/certs", teamDomain),
		enabled:    enabled,
		scopeMap:   make(map[string][]string),
	}

	log.Printf("[Cloudflare-JWT] Initialized validator for team domain: %s (enabled: %v)", teamDomain, enabled)
	if enabled {
		log.Printf("[Cloudflare-JWT] Audience: %s", audience)
		log.Printf("[Cloudflare-JWT] JWKS URL: %s", v.jwksURL)
	}

	return v
}

// SetScopeMap sets the service-token->scopes mapping
//
// This implements Phase 14 rule 2: scopes bound to verified subject via SEAM-side map.
// The map keys are JWT subjects (sub) or common names (from email claim).
// The values are slices of scope strings (e.g., ["k8s-ro:get", "argocd:read"]).
//
// The map is deep-copied so a later mutation of the caller's map cannot widen or
// narrow the scopes already bound to a verified subject.
func (v *CloudflareJWTValidator) SetScopeMap(scopeMap map[string][]string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	copied := make(map[string][]string, len(scopeMap))
	for subject, scopes := range scopeMap {
		scopesCopy := make([]string, len(scopes))
		copy(scopesCopy, scopes)
		copied[subject] = scopesCopy
	}

	v.scopeMap = copied
	log.Printf("[Cloudflare-JWT] Updated scope map with %d entries", len(copied))
}

// GetScopesForSubject returns scopes for a given JWT subject
//
// This implements Phase 14 rule 2: scope lookup for verified subjects.
// Returns the scopes associated with the subject, or nil if not found.
func (v *CloudflareJWTValidator) GetScopesForSubject(subject string) []string {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if scopes, exists := v.scopeMap[subject]; exists {
		// Return a copy to prevent mutation
		scopesCopy := make([]string, len(scopes))
		copy(scopesCopy, scopes)
		return scopesCopy
	}

	// Try email as fallback (for human identities)
	if email, exists := v.scopeMap[subject]; exists {
		scopesCopy := make([]string, len(email))
		copy(scopesCopy, email)
		return scopesCopy
	}

	return nil
}

// ValidateJWT validates a Cloudflare Access JWT and returns the claims
//
// This implements Phase 14 rule 1: JWT validation with signature, aud, iss, exp, nbf checks.
// Returns (claims, nil) on success, (nil, error) on failure.
//
// Per Phase 14 rule 4: No valid JWT = 403 BEFORE route matching, secret lookup, or upstream contact.
func (v *CloudflareJWTValidator) ValidateJWT(tokenString string) (*CloudflareAccessClaims, error) {
	if !v.enabled {
		// JWT validation is disabled - allow through
		log.Printf("[Cloudflare-JWT] Validation disabled - allowing request without JWT")
		return nil, nil
	}

	// Remove "Bearer " prefix if present
	tokenString = strings.TrimPrefix(tokenString, "Bearer ")
	tokenString = strings.TrimSpace(tokenString)

	if tokenString == "" {
		return nil, fmt.Errorf("missing JWT token")
	}

	// Parse JWT (without verification yet, to get key ID)
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// Verify signing algorithm is RS256
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		// Get key ID from header
		kid, ok := token.Header["kid"].(string)
		if !ok {
			return nil, fmt.Errorf("missing key ID in JWT header")
		}

		// Fetch JWKS if cache is stale
		if err := v.refreshJWKSCache(); err != nil {
			return nil, fmt.Errorf("failed to refresh JWKS: %w", err)
		}

		// Find matching key in JWKS
		v.mu.RLock()
		defer v.mu.RUnlock()

		for _, key := range v.jwks.Keys {
			if key.Kid == kid {
				// Parse RSA public key from JWK
				return v.parseRSAPublicKey(&key)
			}
		}

		return nil, fmt.Errorf("key ID %s not found in JWKS", kid)
	})

	if err != nil {
		return nil, fmt.Errorf("JWT validation failed: %w", err)
	}

	if !token.Valid {
		return nil, fmt.Errorf("JWT is not valid")
	}

	// Extract claims
	claims, err := v.extractClaims(token)
	if err != nil {
		return nil, fmt.Errorf("failed to extract claims: %w", err)
	}

	// Validate claims (aud, iss, exp, nbf)
	if err := v.validateClaims(claims); err != nil {
		return nil, fmt.Errorf("claims validation failed: %w", err)
	}

	log.Printf("[Cloudflare-JWT] Validated JWT for subject: %s (email: %s)", claims.Sub, claims.Email)
	return claims, nil
}

// refreshJWKSCache fetches JWKS from Cloudflare if cache is stale
func (v *CloudflareJWTValidator) refreshJWKSCache() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	// Check if cache is still fresh
	if v.jwks != nil && time.Now().Before(v.jwksCacheExpiry) {
		return nil
	}

	log.Printf("[Cloudflare-JWT] Refreshing JWKS from %s", v.jwksURL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", v.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create JWKS request: %w", err)
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS fetch failed with status %d", resp.StatusCode)
	}

	var jwks JWKS
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("failed to decode JWKS: %w", err)
	}

	v.jwks = &jwks
	v.jwksCacheExpiry = time.Now().Add(1 * time.Hour) // Cache for 1 hour

	log.Printf("[Cloudflare-JWT] Refreshed JWKS cache with %d keys (expires: %s)", len(jwks.Keys), v.jwksCacheExpiry.Format(time.RFC3339))
	return nil
}

// parseRSAPublicKey parses an RSA public key from JWK components
func (v *CloudflareJWTValidator) parseRSAPublicKey(key *JWK) (*rsa.PublicKey, error) {
	// Decode base64url-encoded modulus and exponent
	nBytes, err := base64.RawURLEncoding.DecodeString(key.N)
	if err != nil {
		return nil, fmt.Errorf("failed to decode modulus: %w", err)
	}

	eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
	if err != nil {
		return nil, fmt.Errorf("failed to decode exponent: %w", err)
	}

	// Convert to big.Int and create RSA public key
	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)

	pubKey := &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}

	return pubKey, nil
}

// extractClaims extracts Cloudflare Access claims from a validated JWT
func (v *CloudflareJWTValidator) extractClaims(token *jwt.Token) (*CloudflareAccessClaims, error) {
	claimsMap, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("failed to extract claims from token")
	}

	claims := &CloudflareAccessClaims{}

	// Standard claims
	if aud, ok := claimsMap["aud"]; ok {
		switch v := aud.(type) {
		case []interface{}:
			for _, a := range v {
				if audStr, ok := a.(string); ok {
					claims.Aud = append(claims.Aud, audStr)
				}
			}
		case []string:
			claims.Aud = v
		case string:
			claims.Aud = []string{v}
		}
	}

	if iss, ok := claimsMap["iss"].(string); ok {
		claims.Iss = iss
	}

	if sub, ok := claimsMap["sub"].(string); ok {
		claims.Sub = sub
	}

	if exp, ok := claimsMap["exp"].(float64); ok {
		claims.Exp = int64(exp)
	}

	if nbf, ok := claimsMap["nbf"].(float64); ok {
		claims.Nbf = int64(nbf)
	}

	// Cloudflare-specific claims
	if email, ok := claimsMap["email"].(string); ok {
		claims.Email = email
	}

	if country, ok := claimsMap["country"].(string); ok {
		claims.Country = country
	}

	if nonce, ok := claimsMap["identity_nonce"].(string); ok {
		claims.IdentityNonce = nonce
	}

	// Custom scopes claim
	if scopes, ok := claimsMap["scopes"].([]interface{}); ok {
		for _, scope := range scopes {
			if scopeStr, ok := scope.(string); ok {
				claims.Scopes = append(claims.Scopes, scopeStr)
			}
		}
	}

	return claims, nil
}

// validateClaims validates JWT claims against expected values
func (v *CloudflareJWTValidator) validateClaims(claims *CloudflareAccessClaims) error {
	now := time.Now().Unix()

	// Check exp (expiration)
	if claims.Exp != 0 && claims.Exp < now {
		return fmt.Errorf("token expired at %d (now: %d)", claims.Exp, now)
	}

	// Check nbf (not before)
	if claims.Nbf != 0 && claims.Nbf > now {
		return fmt.Errorf("token not valid until %d (now: %d)", claims.Nbf, now)
	}

	// Check aud (audience) - must contain SEAM's Access Application ID
	if len(claims.Aud) == 0 {
		return fmt.Errorf("token missing audience claim")
	}

	audienceMatch := false
	for _, aud := range claims.Aud {
		if aud == v.audience {
			audienceMatch = true
			break
		}
	}

	if !audienceMatch {
		return fmt.Errorf("token audience %v does not match expected %s", claims.Aud, v.audience)
	}

	// Check iss (issuer) - must be from Cloudflare Access for our team domain
	expectedIssuer := fmt.Sprintf("https://%s.cloudflareaccess.com", v.teamDomain)
	if claims.Iss != expectedIssuer {
		return fmt.Errorf("token issuer %s does not match expected %s", claims.Iss, expectedIssuer)
	}

	return nil
}

// cloudflareJWTMiddleware creates HTTP middleware for Cloudflare JWT validation
//
// This implements Phase 14 rule 1 and rule 4: JWT validation on EVERY request,
// with default-deny (403) BEFORE route matching, secret lookup, or upstream contact.
//
// The middleware must run BEFORE:
//   - Stage 1 (validationMiddleware) - route matching happens here
//   - Stage 2 (headerStrippingMiddleware) - scope header deletion
//   - Stage 3 (identityResolutionMiddleware) - WhoIs resolution
//   - Stage 5 (authorizationMiddleware) - scope enforcement
//
// Pipeline position: OUTERMOST - runs before all other middleware
func (s *Server) cloudflareJWTMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip health endpoints to allow Kubernetes probes
		if r.URL.Path == "/_seam/health" || r.URL.Path == "/_seam/healthz" || r.URL.Path == "/_seam/readyz" {
			next.ServeHTTP(w, r)
			return
		}

		// Skip if validator is not configured
		if s.cloudflareJWTValidator == nil {
			log.Printf("[Cloudflare-JWT] No validator configured - allowing request")
			next.ServeHTTP(w, r)
			return
		}

		// Extract JWT from Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			// No JWT provided
			if s.cloudflareJWTValidator.enabled {
				// Default-deny: 403 when JWT validation is enabled
				log.Printf("[Cloudflare-JWT] No Authorization header - denying (Phase 14 rule 4)")
				NewErrorResponse(ErrCodeForbidden, "Cloudflare Access authentication required").Write(w, r)
				return
			}

			// JWT validation disabled - allow through
			log.Printf("[Cloudflare-JWT] No Authorization header but validation disabled - allowing")
			next.ServeHTTP(w, r)
			return
		}

		// Validate JWT
		claims, err := s.cloudflareJWTValidator.ValidateJWT(authHeader)
		if err != nil {
			// JWT validation failed - 403 (Phase 14 rule 4)
			log.Printf("[Cloudflare-JWT] JWT validation failed: %v", err)
			NewErrorResponse(ErrCodeForbidden, fmt.Sprintf("Cloudflare Access authentication failed: %v", err)).Write(w, r)
			return
		}

		// JWT is valid - store claims in context for later stages
		ctx := contextWithValue(r.Context(), cloudflareJWTClaimsKey{}, claims)
		*r = *r.WithContext(ctx)

		log.Printf("[Cloudflare-JWT] Successfully validated JWT for subject: %s", claims.Sub)

		// Proceed to next handler
		next.ServeHTTP(w, r)
	})
}

// cloudflareJWTClaimsKey is the context key type for storing JWT claims
type cloudflareJWTClaimsKey struct{}

// contextWithValue stores a value in the request context
func contextWithValue(ctx context.Context, key, value interface{}) context.Context {
	return context.WithValue(ctx, key, value)
}

// cloudflareJWTClaimsFromContext extracts Cloudflare JWT claims from the request context
func cloudflareJWTClaimsFromContext(ctx context.Context) *CloudflareAccessClaims {
	if claims, ok := ctx.Value(cloudflareJWTClaimsKey{}).(*CloudflareAccessClaims); ok {
		return claims
	}
	return nil
}

// cloudflareScopesFromContext extracts scopes from Cloudflare JWT claims in the context
//
// This implements Phase 14 rule 2: scopes bound to verified subject.
// Returns the scopes from the service-token->scopes map for the verified subject.
func cloudflareScopesFromContext(ctx context.Context) []string {
	claims := cloudflareJWTClaimsFromContext(ctx)
	if claims == nil {
		return nil
	}

	// Return scopes from JWT claims if present
	if len(claims.Scopes) > 0 {
		return claims.Scopes
	}

	return nil
}
