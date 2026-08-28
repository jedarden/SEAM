# Phase 14: Cloudflare Access Ingress - JWT Validation and Scope Mapping

## Overview

Phase 14 implements Cloudflare Access authentication for SEAM, enabling public ingress while maintaining security through JWT validation and scope-based authorization. This is the ONLY phase that lifts the no-public-ingress rule.

## Implementation

### 1. JWT Validation Middleware (Phase 14 Rule 1)

**File**: `internal/server/cloudflare_jwt_middleware.go`

The Cloudflare JWT validation middleware validates Cloudflare Access service tokens on EVERY request before any other processing:

- **Signature Validation**: Validates JWT signatures against the team domain's published JWKS
- **Claims Validation**: Verifies aud, iss, exp, nbf claims
- **Default-Deny**: Returns 403 BEFORE route matching, secret lookup, or upstream contact

**Pipeline Position**: OUTERMOST - runs before all other middleware

### 2. Service-Token->Scopes Mapping (Phase 14 Rule 2)

**File**: `internal/server/cloudflare_jwt_middleware.go`

Scopes are bound to verified JWT subjects (sub/common_name) via a SEAM-side service-token->scopes map:

```go
validator.SetScopeMap(map[string][]string{
    "service-token-1": {"k8s-ro:get", "argocd:read"},
    "user@example.com": {"config:read", "seam:ops:read"},
})
```

This prevents client-assertable scope claims - only server-configured mappings are honored.

### 3. X-SEAM-Scopes Header Deletion (Phase 14 Rule 3)

**File**: `internal/server/header_middleware.go`

X-SEAM-Scopes and equivalent headers are DELETED at stage 2, not ignored:

```go
var deletedScopeHeaders = map[string]bool{
    "X-Seam-Scopes": true,
    "X-Seam-Scope":  true,
}
```

This prevents clients from asserting their own scopes via headers.

### 4. Default-Deny Mode (Phase 14 Rule 4)

**File**: `internal/server/cloudflare_jwt_middleware.go`

Mode is default-OFF. No valid JWT = 403 BEFORE route matching, secret lookup, or upstream contact:

```go
// Skip if validator is not configured
if s.cloudflareJWTValidator == nil {
    log.Printf("[Cloudflare-JWT] No validator configured - allowing request")
    next.ServeHTTP(w, r)
    return
}

// No JWT provided - 403
if s.cloudflareJWTValidator.enabled {
    NewErrorResponse(ErrCodeForbidden, "Cloudflare Access authentication required").Write(w, r)
    return
}
```

## Configuration

### Server Configuration

**File**: `internal/server/server.go`

Add to `Config` struct:

```go
type Config struct {
    // ... existing fields ...

    // Phase 14: Cloudflare Access JWT validation configuration
    CloudflareAccessEnabled  bool   // Enable Cloudflare Access JWT validation (default: false)
    CloudflareTeamDomain     string // Cloudflare team domain (e.g., "ardenone")
    CloudflareAudience       string // Expected JWT audience (Access Application ID)
}
```

### Environment Variables

Set via environment variables or CLI flags:

```bash
export SEAM_CLOUDFLARE_ACCESS_ENABLED=true
export SEAM_CLOUDFLARE_TEAM_DOMAIN=ardenone
export SEAM_CLOUDFLARE_AUDIENCE=<your-access-application-id>
```

## Cloudflare Tunnel + Access Application Setup

### 1. Create Cloudflare Access Application

1. Go to Cloudflare Dashboard > Zero Trust > Access > Applications
2. Create a new application with type "Self-Hosted"
3. Configure:
   - **Application Name**: SEAM Gateway
   - **Session Duration**: As needed
   - **Application Landing Page**: Your SEAM public URL
4. Add identity providers (Google SSO, email, etc.)
5. **Important**: Copy the Application ID (this is your `audience`)

### 2. Create Cloudflare Tunnel

```bash
# Install cloudflared
curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 -o cloudflared
chmod +x cloudflared
sudo mv cloudflared /usr/local/bin/

# Create tunnel
cloudflared tunnel create seam-tunnel

# Note the tunnel ID from output

# Route traffic to tunnel
cloudflared tunnel route dns seam-tunnel seam.example.com

# Start tunnel with config
cloudflared tunnel run seam-tunnel --config config.yml
```

**config.yml**:
```yaml
tunnel: <tunnel-id>
credentials-file: /path/to/<tunnel-id>.json

ingress:
  - hostname: seam.example.com
    service: http://localhost:8080
  - service: http_status:404
```

### 3. Configure SEAM

Update SEAM configuration:

```yaml
# declarative-config/k8s/rs-manager/seam/deployment.yaml
env:
  - name: SEAM_CLOUDFLARE_ACCESS_ENABLED
    value: "true"
  - name: SEAM_CLOUDFLARE_TEAM_DOMAIN
    value: "ardenone"
  - name: SEAM_CLOUDFLARE_AUDIENCE
    value: "<your-application-id>"
```

### 4. Configure Scope Mapping

Add scope mapping to SEAM configuration:

```go
// In server initialization or configuration
scopeMap := map[string][]string{
    "service-token-1": {"k8s-ro:get", "argocd:read"},
    "user@example.com": {"config:read", "seam:ops:read"},
}
s.cloudflareJWTValidator.SetScopeMap(scopeMap)
```

## JWT Structure

Cloudflare Access JWTs contain:

```json
{
  "aud": ["<your-application-id>"],
  "iss": "https://<team-domain>.cloudflareaccess.com",
  "sub": "<user-identity-or-service-token>",
  "exp": 1234567890,
  "nbf": 1234567800,
  "email": "user@example.com",
  "country": "US",
  "identity_nonce": "abc123"
}
```

## Testing

### Unit Tests

**Files**:
- `internal/server/cloudflare_jwt_middleware_test.go`
- `internal/server/cloudflare_header_stripping_test.go`

Run tests:
```bash
go test ./internal/server -run TestCloudflareJWT
go test ./internal/server -run TestHeaderStripping
```

### Integration Tests

Test with real Cloudflare Access tokens:

```bash
# Get a token from Cloudflare Access
curl -H "Authorization: Bearer <token>" https://seam.example.com/whoami

# Test without token (should get 403)
curl https://seam.example.com/whoami

# Test with invalid token (should get 403)
curl -H "Authorization: Bearer invalid" https://seam.example.com/whoami
```

## Security Considerations

1. **Tunnel Arrival Proves Nothing**: JWT validation is required even for tunnel connections
2. **Signature Validation**: Always validate against JWKS, never skip signature checks
3. **Audience Binding**: Must match your Access Application ID exactly
4. **Scope Mapping**: Only server-configured scopes are honored, never client-provided
5. **Header Deletion**: X-SEAM-Scopes headers are always deleted, never ignored
6. **Default-Deny**: No valid JWT = 403 before any processing

## Migration from Tailscale

To migrate from Tailscale-only access to Cloudflare Access:

1. **Phase 1**: Enable Cloudflare JWT validation in disabled mode
2. **Phase 2**: Create Cloudflare Tunnel and Access application
3. **Phase 3**: Configure scope mapping for existing Tailscale identities
4. **Phase 4**: Enable Cloudflare JWT validation (set enabled=true)
5. **Phase 5**: Test with Cloudflare Access tokens
6. **Phase 6**: Gradually migrate clients from Tailscale to Cloudflare Access

## Troubleshooting

### JWT Validation Fails

1. Check team domain matches `https://<team-domain>.cloudflareaccess.com`
2. Verify audience matches Access Application ID exactly
3. Ensure JWKS URL is accessible: `https://<team-domain>.cloudflareaccess.com/cdn-cgi/access/certs`
4. Check token expiration and not-before claims

### Scope Authorization Fails

1. Verify scope map contains the JWT subject
2. Check that scopes in map match route requirements
3. Ensure identity resolution is working (Stage 3)

### Health Endpoint Fails

Health endpoints (`/_seam/health`, `/_seam/healthz`, `/_seam/readyz`) bypass JWT validation intentionally.

### Header Deletion Not Working

1. Check that deletedScopeHeaders map contains the header name
2. Verify header name case (Go canonicalizes headers)
3. Ensure middleware is in correct pipeline position (stage 2)

## References

- Cloudflare Access Documentation: https://developers.cloudflare.com/cloudflare-one/identity/
- Cloudflare Tunnel Documentation: https://developers.cloudflare.com/cloudflare-one/connections/connect-apps/
- JWT/JWKS RFC: https://tools.ietf.org/html/rfc7519
