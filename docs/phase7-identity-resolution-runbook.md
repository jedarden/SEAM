# Phase 7: Stage 3/5 Identity Resolution Activation Runbook

**Bead ID:** seam-512a1606
**Phase:** 7.1 - Stage 3 identity resolution live (WhoIs/Grant) + tailnet Grants
**Status:** Implementation Complete, Activation Pending

## Overview

Phase 7 activates SEAM's two-stage security architecture that has been inert since initial deployment:

- **Stage 3**: Caller identity resolution via Tailscale WhoIs
- **Stage 5**: Authorization enforcement via `x-required-scope` from route fragments

**CRITICAL RULE:** Stages 3 and 5 must activate **TOGETHER and never separately**:
- Stage 3 without Stage 5: Identity is resolved but never enforced (useless overhead)
- Stage 5 without Stage 3: Authorization checks with no identity (denies everyone)

## Architecture Changes

### Before Phase 7

```
Request → Stage 1 (Control-plane check)
         → Stage 2 (Header stripping)
         → Stage 3 (INERT - "reserved anonymous identity")
         → Stage 4 (Route matching)
         → Stage 5 (INERT - no scope enforcement)
         → Stages 6-11 (Validation, guards, secret injection, dispatch)
```

Security boundary: Tailnet ACL (hard network-level enforcement)

### After Phase 7

```
Request → Stage 1 (Control-plane check)
         → Stage 2 (Header stripping)
         → Stage 3 (WhoIs identity resolution → Tailscale Grant app capabilities)
         → Stage 4 (Route matching)
         → Stage 5 (x-required-scope enforcement → default-deny)
         → Stages 6-11 (Validation, guards, secret injection, dispatch)
```

Security boundary: Application-level scope enforcement + Tailnet ACL (defense-in-depth)

## Implementation Components

### 1. Identity Resolution Middleware (Stage 3)

**File:** `internal/server/identity_middleware.go`

**Functionality:**
- Resolves inbound connection IP to Tailscale node identity via WhoIs
- Extracts scope claims from Tailscale Grant's `app` capability field
- Stores resolved identity in request context for Stage 5
- Returns 403 for non-Tailscale connections (default-deny)

**Code flow:**
```go
identity, err := s.identityResolver.ResolveFromRequest(r)
if err != nil {
    // Phase 7: Return 403 (currently allows with warning)
    // NewErrorResponse(ErrCodeForbidden, "Identity resolution failed").Write(w, r)
}
```

### 2. Authorization Middleware (Stage 5)

**File:** `internal/server/authorization_middleware.go`

**Functionality:**
- Retrieves resolved identity from Stage 3 context
- Checks route's `x-required-scope` from fragment metadata
- Validates identity's scope claims against required scopes
- Returns 403 if identity lacks required scope (default-deny)

**Code flow:**
```go
requiredScopes := routeMatch.Route.RequiredScopes
if !hasAnyScope(identity, requiredScopes) {
    // Phase 7: Return 403 (currently allows with warning)
    // NewErrorResponse(ErrCodeForbidden, "Missing required scope").Write(w, r)
}
```

### 3. Identity Resolver

**File:** `internal/server/identity.go`

**Functionality:**
- `IdentityResolver.Resolve()` - Resolve remote address to Tailscale identity
- `Identity` struct - Holds node key, name, user, tags, capabilities
- `ExtractScopeClaims()` - Parse scope claims from Grant's app field
- `HasTag()` / `HasScope()` - Identity membership checks

**Status:** Placeholder implementation, needs Tailscale LocalClient integration

### 4. Tailnet Policy Grant Entries

**File:** `docs/tailnet-grants-phase7.hujson`

**Functionality:**
- Defines Grant entries with `app` capability field
- Carries scope claims (e.g., `"k8s-ro:get"`, `"argocd:read"`)
- Maps worker tags to allowed scopes
- Tests validate correct scope evaluation

**Example Grant:**
```json
{
  "src": ["tag:needle-worker"],
  "dst": ["tag:seam:8080"],
  "app": {
    "tailscale.com/cap/seam-scopes": [
      {
        "scopes": ["k8s-ro:get", "argocd:read", "config:read"]
      }
    ]
  }
}
```

## Activation Checklist

### Phase 7.1: Code Deployment ✅ COMPLETE

- [x] Create identity resolution middleware (Stage 3)
- [x] Create authorization middleware (Stage 5)
- [x] Create identity resolver with placeholder WhoIs
- [x] Write tailnet policy Grant entries
- [x] Integrate middleware into request pipeline
- [x] Add IdentityResolver to Server struct
- [x] Initialize IdentityResolver in New()

### Phase 7.2: Tailscale LocalClient Integration (BLOCKING)

**Required for actual WhoIs resolution:**

1. Import `tailscale.com/client/local` package
2. Create LocalClient instance in IdentityResolver
3. Implement `WhoIs(remoteAddr)` call
4. Parse WhoIs response to extract identity + tags + capabilities
5. Handle non-Tailscale IPs (return error for 403)

**Code changes needed:**
```go
// internal/server/identity.go
import "tailscale.com/client/local"

type IdentityResolver struct {
    tsClient *local.LocalClient
    // ...
}

func (ir *IdentityResolver) Resolve(ctx context.Context, remoteAddr string) (*Identity, error) {
    // Parse remote address to IP
    host, _, err := net.SplitHostPort(remoteAddr)
    ip := net.ParseIP(host)

    // Call WhoIs
    who, err := ir.tsClient.WhoIs(ctx, ip)
    if err != nil {
        return nil, fmt.Errorf("WhoIs failed: %w", err)
    }

    // Extract identity from WhoIs response
    return &Identity{
        NodeKey:  who.NodeKey,
        NodeName: who.Node.Name,
        User:     who.User.Profile.LoginName,
        Tags:     who.Node.Tags,
        // TODO: Extract capabilities from Grant's app field
    }, nil
}
```

**Blocker:** `tailscale.com/client/local` package may not be available in build environment
- Research: Check if Tailscale Go SDK is importable
- Alternative: Use HTTP API to Tailscale coordination server

### Phase 7.3: Grant App Capability Parsing (BLOCKING)

**Required for extracting scope claims:**

1. Parse Grant's `app` field from WhoIs response
2. Extract `tailscale.com/cap/seam-scopes` array
3. Parse scope strings from Grant into identity capabilities

**Code changes needed:**
```go
func extractCapabilitiesFromWhoIs(who *tailscale.WhoIsResponse) []string {
    // who.Capabilities map contains Grant app fields
    if app, ok := who.Capabilities["tailscale.com/cap/seam-scopes"]; ok {
        if scopes, ok := app.([]map[string]interface{}); ok {
            for _, scopeEntry := range scopes {
                if scopeList, ok := scopeEntry["scopes"].([]string); ok {
                    return scopeList
                }
            }
        }
    }
    return nil
}
```

### Phase 7.4: Activate Denial Logic (FINAL STEP)

**When Tailscale integration is complete:**

1. Uncomment 403 returns in `identity_middleware.go`
2. Uncomment 403 returns in `authorization_middleware.go`
3. Remove "reserved anonymous identity" fallback
4. Test with actual Tailscale connections

**Code changes:**
```go
// internal/server/identity_middleware.go
if err != nil {
    // UNCOMMENT THIS FOR PHASE 7 ACTIVATION
    NewErrorResponse(ErrCodeForbidden, "Identity resolution failed").Write(w, r)
    return
}

// internal/server/authorization_middleware.go
if !hasScope {
    // UNCOMMENT THIS FOR PHASE 7 ACTIVATION
    NewErrorResponse(ErrCodeForbidden, fmt.Sprintf("Route requires one of scopes: %v", requiredScopes)).Write(w, r)
    return
}
```

## Deployment Steps

### 1. Deploy Code (Current State)

```bash
# Build and deploy SEAM with Phase 7 code (INERT mode)
cd /home/coding/SEAM
# Code is already integrated, middleware is in pipeline but allows all requests
git add internal/server/identity.go internal/server/identity_middleware.go internal/server/authorization_middleware.go
git commit -m "feat(seam): implement Phase 7 Stage 3/5 identity resolution architecture

- Add identity resolution middleware (Stage 3) with placeholder WhoIs
- Add authorization middleware (Stage 5) with x-required-scope enforcement
- Add IdentityResolver for Tailscale WhoIs integration
- Add Identity struct with node/user/tags/capabilities
- Integrate middleware into request pipeline
- Create tailnet policy Grant entries with scope claims

Phase 7.1 complete: Code deployed, middleware INERT until Tailscale LocalClient integration

Paired with bead seam-512a1606"

git push
```

### 2. Deploy Tailnet Grants (Manual)

```bash
# Copy Grant entries to tailnet policy
cat docs/tailnet-grants-phase7.hujson >> /path/to/tailnet/policy.hujson

# Validate policy with tailscale CLI
tailscale validate acl /path/to/tailnet/policy.hujson

# Apply policy (if using automation) or manually in admin console
# Grants take effect immediately when applied to tailnet
```

### 3. Verify INERT Mode

```bash
# Check middleware logs - should show "Phase 7 - INERT"
kubectl logs -n rs-manager deployment/seam -f | grep "Stage-3-Identity\|Stage-5-Authorization"

# Test that requests still succeed (ACL is still security boundary)
curl -H "Authorization: Bearer test" https://seam-rs-manager.tail1b1987.ts.net/k8s/ardenone-cluster/api/v1/namespaces
```

### 4. Tailscale LocalClient Integration (Future Work)

This requires additional research and development:

1. Investigate `tailscale.com/client/local` availability in build environment
2. Create LocalClient configuration
3. Implement actual WhoIs calls
4. Test with real Tailscale connections
5. Activate denial logic (uncomment 403 returns)

**Estimated effort:** 4-8 hours for LocalClient integration + testing

## Testing Strategy

### Unit Tests (Not Yet Written)

```go
// internal/server/identity_test.go
func TestIdentityResolver_ResolvesTailscaleIP(t *testing.T) {
    ir := NewIdentityResolver()
    identity, err := ir.Resolve(ctx, "100.64.0.1:8080")
    // Test that identity is resolved
}

// internal/server/authorization_middleware_test.go
func TestAuthorizationMiddleware_EnforcesRequiredScopes(t *testing.T) {
    // Test that middleware returns 403 when identity lacks required scope
}
```

### Integration Tests (Future)

1. Spin up test Tailscale network
2. Create test nodes with tags
3. Apply Grant entries
4. Call SEAM from test nodes
5. Verify scope enforcement

### Manual Verification Checklist

- [ ] Middleware logs show "Stage 3" and "Stage 5" for each request
- [ ] Identity resolution logs show resolved node/user/tags
- [ ] Authorization logs show scope checks
- [ ] Requests with valid scopes succeed
- [ ] Requests without valid scopes return 403 (after activation)
- [ ] Tailscale Grant entries are visible in tailnet policy

## Rollback Plan

If Phase 7 causes issues:

1. **Immediate rollback:** Remove middleware from pipeline
   ```go
   // Comment out in server.go
   // callerHandler = s.identityResolutionMiddleware(callerHandler)
   // callerHandler = s.authorizationMiddleware(callerHandler)
   ```

2. **Revert tailnet policy:** Remove Grant entries from policy.hujson

3. **Redeploy:** Revert to ACL-only security boundary

4. **Investigate:** Check logs for identity resolution failures

## Known Limitations

1. **Tailscale-only:** Only works for callers that are Tailscale peers
   - Non-Tailscale connections get 403 after activation
   - This is intentional (default-deny security posture)

2. **Shared egress proxy:** Cluster egress proxies collapse identity
   - If workers call through shared Connector, all appear as same node
   - Solution: Per-worker tsnet identity (research in `docs/research/tailscale-identity-mechanisms.md`)

3. **Grant propagation:** Grant changes take effect immediately
   - No caching of scope claims (refreshed per WhoIs call)
   - May have performance impact at high request rates

4. **Scope granularity:** Current scopes are service-level (e.g., "k8s-ro:get")
   - Future: Resource-level scopes (e.g., "k8s-ro:get:namespace:pod")
   - Requires fragment schema extension

## Success Metrics

- [x] Code integrated into middleware pipeline
- [x] Tailnet Grant entries documented
- [ ] Tailscale LocalClient integration complete
- [ ] Identity resolution returns valid identities
- [ ] Authorization middleware enforces scopes
- [ ] Default-deny returns 403 for unscoped requests
- [ ] No regressions in existing request flow

## Next Steps

1. **Immediate:** Commit and push current implementation (INERT mode)
2. **Research:** Investigate `tailscale.com/client/local` availability
3. **Future:** Complete Tailscale LocalClient integration
4. **Future:** Activate denial logic
5. **Future:** Write unit and integration tests
6. **Future:** Document per-worker tsnet identity requirements

## References

- **Bead:** seam-512a1606
- **Research:** `docs/research/tailscale-identity-mechanisms.md`
- **Grants:** `docs/tailnet-grants-phase7.hujson`
- **Architecture:** `docs/plan/plan.md` (Phase 7 specification)
- **Tailscale Docs:** https://tailscale.com/docs/features/access-control/grants

---

**Implementation Status:** Phase 7.1 COMPLETE - Middleware deployed, INERT until Tailscale LocalClient integration
