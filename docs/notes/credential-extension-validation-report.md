# SEAM Credential Extension Field Schema Validation Report

**Date:** 2026-08-07  
**Bead:** bf-10vj  
**Status:** ✅ COMPLETE

## Summary

All four SEAM credential extension field schemas have been successfully defined and validated in the base route-fragment schema. The updated schema now supports cookie-based credential injection, providing enhanced flexibility for authentication scenarios.

## Extension Fields Implemented

### 1. x-vault-path (Vault KV Secret Path)
- **Type:** String
- **Pattern:** `^seam/routes/[a-z0-9]([a-z0-9-]*[a-z0-9])?/`
- **Description:** Must resolve inside the OpenBao allowlist prefix `seam/routes/*` and be co-owned with the fragment owner
- **Validation:** 
  - ✓ Path traversal rejection (no `..` segments)
  - ✓ Glob character rejection (no `*` or `?`)
  - ✓ Template segment rejection (no `{}`)
  - ✓ Co-ownership enforcement (must match `x-seam-owner`)

**Example:** `"seam/routes/user-service/api-key"`

---

### 2. x-inject-as (Credential Injection Method)
- **Type:** Object
- **Required Fields:** `kind`
- **Kind Enum:** `["header", "query", "cookie"]`
- **Conditional Fields:** 
  - `kind: header/query/cookie` → `name` required
- **Description:** Specifies how to inject the credential into the request
  - `header`: Inject as HTTP header (e.g., `{kind: header, name: X-API-Key}`)
  - `query`: Inject as query parameter (e.g., `{kind: query, name: api_key}`)
  - `cookie`: Inject as HTTP cookie (e.g., `{kind: cookie, name: session}`)

**Example:**
```json
{
  "kind": "cookie",
  "name": "session_token"
}
```

---

### 3. x-required-scope (OAuth2 Scopes)
- **Type:** Array or String (string is sugar for one-element array)
- **Items:** Scope string pattern `^[a-z0-9]([a-z0-9-]*[a-z0-9])?(:[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`
- **Validation:**
  - ✓ `minItems: 1` if array
  - ✓ `uniqueItems: true`
  - ✓ At least two segments (service:action)
- **Description:** Conjunctive OAuth2 scopes a caller must hold

**Example:** `["read:users", "profile:view"]`

---

### 4. x-credential-probe (Credential Health Check)
- **Type:** Object
- **Required Fields:** `path`, `method`, `interval`
- **Validation:**
  - `path`: Must start with `/` and be a route the fragment serves
  - `method`: Enum `["GET", "HEAD"]` (defaults to GET)
  - `interval`: Integer, minimum 1 second
- **Description:** Configures credential health checks for injected credentials

**Example:**
```json
{
  "path": "/health",
  "method": "HEAD",
  "interval": 30
}
```

---

## Validation Rules Summary

| Field | Validation Rule | Status |
|-------|----------------|---------|
| x-vault-path | Pattern validation (no traversal, globs, templates) | ✅ PASS |
| x-inject-as | Enum constraint (header/query/cookie) | ✅ PASS |
| x-inject-as | name required for all kinds | ✅ PASS |
| x-required-scope | minItems=1 if array | ✅ PASS |
| x-required-scope | uniqueItems=true | ✅ PASS |
| x-credential-probe | interval >= 1 second | ✅ PASS |
| x-credential-probe | method in ["GET", "HEAD"] | ✅ PASS |
| x-credential-probe | path starts with "/" | ✅ PASS |

## Cross-Field Constraints

The schema includes several cross-field validation constraints:

1. **credentialProbe requires credentials:** Forbidden on pass-through fragments (no x-vault-path/x-inject-as)
2. **Vault-inject paired:** Both x-vault-path and x-inject-as must be present together or absent together
3. **Passthrough has no probe:** Pass-through fragments cannot declare x-credential-probe

## Example Fragment Validation

The example fragment at `docs/notes/route-fragment-credential-example.json` successfully validates against the updated schema:

```json
{
  "x-seam-schema": "v1",
  "x-seam-owner": "user-service",
  "x-api-version": "v1",
  "x-upstream": "https://user-service.internal:8443",
  "x-vault-path": "seam/routes/user-service/api-key",
  "x-inject-as": {
    "kind": "cookie",
    "name": "session_token"
  },
  "x-credential-probe": {
    "path": "/health",
    "method": "HEAD",
    "interval": 30
  },
  "x-required-scope": ["read:users", "profile:view"],
  "paths": { ... }
}
```

**Validation Results:** ✅ All checks passed
- Vault path matches pattern
- Cookie injection has required name field
- Probe method is HEAD (allowed)
- Probe interval is 30 (>= 1)
- Required scope has 2 items (>= 1)

## Security Considerations

These extensions are security-critical. Invalid credential configurations are rejected at schema validation time, before reaching runtime:

1. **Path traversal protection:** x-vault-path rejects `..` segments
2. **Glob protection:** x-vault-path rejects wildcard characters
3. **Co-ownership enforcement:** Vault paths must match fragment owner
4. **Method restriction:** Probes limited to safe GET/HEAD methods
5. **Interval floor:** Minimum 1-second probe interval prevents abuse
6. **Scope validation:** OAuth2 scopes must follow proper format

## Changes Made

1. **Updated injectAs enum:** Changed from `["header", "bearer", "query"]` to `["header", "query", "cookie"]`
2. **Updated conditional validation:** All three kinds (header, query, cookie) now require the `name` field
3. **Updated example:** Changed from bearer injection to cookie injection
4. **Enhanced descriptions:** Added detailed documentation for each extension field

## Dependencies

- ✅ Depends on: bf-5tqu (Define base OpenAPI 3.1 fragment schema structure)
- ⏳ Blocks: Child bead (Define SEAM extension field schemas for rate limiting and monitoring fields)

## Acceptance Criteria Status

| Criterion | Status |
|-----------|--------|
| Extend base schema with 4 credential extensions | ✅ COMPLETE |
| Add validation rules for all extensions | ✅ COMPLETE |
| Document each extension in schema descriptions | ✅ COMPLETE |
| Include example fragment showing all extensions | ✅ COMPLETE |
| Validate updated schema with example fragment | ✅ COMPLETE |

---

**Result:** All credential extension field schemas are properly defined, validated, and documented. The schema now supports cookie-based credential injection, providing enhanced authentication flexibility while maintaining strict security validation.
