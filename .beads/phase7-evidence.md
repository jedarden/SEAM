# Phase 7 Completion Criteria Evidence

**Date**: 2026-09-01  
**Bead**: seam-45226dda  
**Context**: Umbrella bead seam-72c18610 was closed 2026-08-27/28. internal/server has not compiled since 2026-08-30 with 99 compile errors accumulated. Phase 7 acceptance was never demonstrated against a running binary.

## Summary

**RESULT**: ❌ **PHASE 7 INCOMPLETE** - Cannot verify acceptance criteria due to inability to compile and run SEAM binary. Phase 7 was marked complete without live demonstration against a running binary.

## Completion Criteria Verification

### Criterion 1: NEEDLE-side per-worker `tsnet` identity provisioning
**Status**: ⚠️ **PARTIAL** - Code exists but in placeholder mode  
**Evidence**: `internal/server/identity.go` contains IdentityResolver with:
- ✅ Identity struct with NodeKey, NodeName, User, Tags, Capabilities
- ✅ Tailscale IP detection (100.x.x.x range)
- ❌ Real Tailscale LocalClient integration is TODO (line 52: "TODO: Add Tailscale LocalClient for WhoIs calls")
- ❌ Uses test mode via `SEAM_TEST_IDENTITY_MODE` environment variable
- ❌ Production mode returns unresolved identity (lines 103-108)

**Code Reference**: `internal/server/identity.go:51-116`

### Criterion 2: `x-required-scope` route tagging
**Status**: ✅ **IMPLEMENTED**  
**Evidence**: Route table carries RequiredScopes field, populated from fragment `x-required-scope` declarations.

**Code Reference**: Route struct in `internal/server/route_table.go`

### Criterion 3: Grant-based scope enforcement at the gateway (Stage 5)
**Status**: ✅ **IMPLEMENTED** - Phase 7 activated  
**Evidence**: `internal/server/authorization_middleware.go`:
- ✅ Stage 5.4: Default-deny for unresolved identities (line 59-66)
- ✅ Stage 5.5: Scope intersection checking (line 68-86)
- ✅ Phase 7 activated comments throughout
- ✅ Returns 403 Forbidden with scope details when denied

**Code Reference**: `internal/server/authorization_middleware.go:26-91`

### Criterion 4: `/whoami` self-service endpoint
**Status**: ✅ **IMPLEMENTED**  
**Evidence**: `internal/server/control_plane_handlers.go:46-97`:
- ✅ Returns resolved identity (node, user, tags)
- ✅ Returns effective scopes from identity capabilities
- ✅ Returns current X-SEAM-Scope-Version hash
- ✅ Sets X-SEAM-Scope-Version response header

**Code Reference**: `internal/server/control_plane_handlers.go:46-97`

### Criterion 5: `/scopes` endpoint with scope filtering
**Status**: ✅ **IMPLEMENTED**  
**Evidence**: `internal/server/control_plane_handlers.go:114-162`:
- ✅ Default: scope-filtered by caller's effective scopes
- ✅ `?all=1`: requires `seam:scopes:read-all` scope
- ✅ Merged from TWO sources (spec + builtin)
- ✅ Returns filtered/total counts

**Code Reference**: `internal/server/control_plane_handlers.go:114-162`

### Criterion 6: Built-in control-plane scope declarations compiled into binary
**Status**: ✅ **IMPLEMENTED**  
**Evidence**: `internal/server/control_plane_handlers.go:16-31`:
- ✅ `BuiltinControlPlaneScopes` array with 13 scopes
- ✅ Includes `seam:ops:read`, `seam:scopes:read-all`
- ✅ Labeled `"source: builtin"` in scope map

**Code Reference**: `internal/server/control_plane_handlers.go:16-31`

### Criterion 7: Stage 3 identity resolution with default-deny
**Status**: ⚠️ **PARTIAL** - Infrastructure exists, integration incomplete  
**Evidence**: 
- ✅ Identity resolution middleware exists (`identityResolutionMiddleware`)
- ✅ Resolves Tailscale IPs
- ❌ Real Tailscale WhoIs integration is TODO
- ❌ Uses test mode instead of real Grant resolution

**Code Reference**: `internal/server/identity.go:69-116`

### Criterion 8: Scope filtering of `/openapi.json`, `/docs`, `/docs/route`
**Status**: ❓ **UNKNOWN** - Requires testing  
**Evidence**: Cannot verify without running binary. Code inspection shows scope filtering infrastructure exists, but actual filtering behavior untested.

**Required Test**: Call `/openapi.json` with different scopes and verify filtered response

### Criterion 9: 404 vs 403 oracle rule
**Status**: ❓ **UNKNOWN** - Requires testing  
**Evidence**: Cannot verify without running binary. The authorization middleware returns 403 for scope-denied requests, but the filtered-spec 404 (route outside caller's filtered spec) vs visible-but-not-invocable 403 split cannot be verified without live testing.

**Required Test**: 
- Test route completely outside caller's grants → should 404
- Test route visible but wrong method scope → should 403 with scope details

### Criterion 10: Per-instance scope enforcement for multi-instance fragments
**Status**: ❓ **UNKNOWN** - Requires testing  
**Evidence**: Cannot verify without running binary. Phase 5 added multi-instance kubectl-proxy map with observer/admin instances, but per-instance `requiredScope` enforcement cannot be verified without testing.

**Required Test**: Call kubectl-proxy routes with observer vs admin scope grants

### Criterion 11: `X-SEAM-Scope-Version` response header
**Status**: ✅ **IMPLEMENTED**  
**Evidence**: `internal/server/control_plane_handlers.go`:
- ✅ Set by `/whoami` handler (line 94)
- ✅ Set by `/scopes` handler (line 159)
- ✅ Computed via `scopeVersionCache.RecordScopeVersion()`

**Code Reference**: `internal/server/control_plane_handlers.go:94,159`

### Criterion 12: Bounded hash→scope-set retention map
**Status**: ✅ **IMPLEMENTED**  
**Evidence**: `internal/server/scope_version.go:20-50`:
- ✅ 4 distinct scope versions per identity
- ✅ 24-hour idle TTL
- ✅ Global LRU cap (100 entries)

**Code Reference**: `internal/server/scope_version.go:20-50`

### Criterion 13: `seam:ops:read` scope and operator gating
**Status**: ❓ **UNKNOWN** - Requires testing  
**Evidence**: Cannot verify without running binary. Built-in scopes include `seam:ops:read`, but actual enforcement on `/config/status`, `/health/credentials`, `/health/upstreams` cannot be verified.

**Required Test**: Call operator endpoints without `seam:ops:read` → should 403

## Blocking Issues

### Issue 1: Compilation Failures
**Status**: 🔴 **CRITICAL BLOCKER**  
**Description**: internal/server has not compiled since 2026-08-30 with 99 compile errors accumulated.

**Action Required**: Fix all compilation errors before Phase 7 can be verified.

**New Bead Reference**: [CREATE BEAD: Fix internal/server compilation errors]

### Issue 2: Missing Tailscale LocalClient Integration
**Status**: 🔴 **CRITICAL BLOCKER**  
**Description**: Identity resolution exists but uses placeholder test mode. Real WhoIs integration is TODO.

**Evidence**: `internal/server/identity.go:52-83`

**Action Required**: Integrate actual Tailscale LocalClient.WhoIs() for real identity resolution.

**New Bead Reference**: [CREATE BEAD: Integrate Tailscale LocalClient for WhoIs identity resolution]

### Issue 3: No Running Binary for Testing
**Status**: 🔴 **CRITICAL BLOCKER**  
**Description**: SEAM is not deployed (no pods in seam namespace). Cannot test acceptance criteria against live instance.

**Action Required**: Build and deploy SEAM to rs-manager for live verification.

**New Bead Reference**: [CREATE BEAD: Deploy SEAM to rs-manager for Phase 7 verification]

## Code Quality Observations

1. **Good**: Comprehensive Phase 7 comments and documentation throughout codebase
2. **Good**: Clear separation of Stage 3 (identity) and Stage 5 (authorization) responsibilities  
3. **Concern**: Production code returns unresolved identity and allows requests (lines 103-108 in identity.go)
4. **Concern**: Test mode (`SEAM_TEST_IDENTITY_MODE`) used in production code path

## Conclusion

**Phase 7 Status**: ❌ **INCOMPLETE** - Cannot be verified as complete due to:
1. Inability to compile and run SEAM binary
2. Missing Tailscale LocalClient integration (placeholder code)
3. No live instance to test against

**Recommendation**: Phase 7 should not have been closed as complete on 2026-08-27/28. The umbrella bead seam-72c18610 should be reopened or a new bead created to complete the actual implementation and verification work.

**Next Steps**:
1. Create bead to fix compilation errors
2. Create bead to integrate Tailscale LocalClient  
3. Build and deploy SEAM
4. Re-verify all Phase 7 criteria against running binary
5. Close Phase 7 only when all criteria pass live tests

**Test Commands Required** (once SEAM is running):
```bash
# Test identity resolution
curl -H "Authorization: Bearer <test-token>" https://seam-rs-manager.tail1b1987.ts.net/whoami

# Test scope filtering
curl https://seam-rs-manager.tail1b1987.ts.net/scopes
curl https://seam-rs-manager.tail1b1987.ts.net/scopes?all=1

# Test 404/403 oracle
curl -X POST https://seam-rs-manager.tail1b1987.ts.net/k8s/{cluster}/api/v1/namespaces/{ns}/pods

# Test operator gating
curl https://seam-rs-manager.tail1b1987.ts.net/config/status
curl https://seam-rs-manager.tail1b1987.ts.net/health/credentials
```
