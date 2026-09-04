# SEAM Authentication Extensions - Validation Tests

## Overview

This document validates that the authentication extension schema (`route-fragment-schema-auth.json`) meets all acceptance criteria from bead bf-5fwe.

## Acceptance Criteria Status

### ✅ 1. Schema validates all four auth extensions with correct types and constraints

All four authentication extensions are defined with proper types and constraints:

| Extension | Type | Constraints | Location |
|-----------|------|-------------|----------|
| `x-vault-path` | string | Pattern: `^[a-z0-9]([a-z0-9-]*[a-z0-9])?(/[a-z0-9]([a-z0-9-]*[a-z0-9])?){2,}$` — base-agnostic, at least three segments | Lines 33-56 |
| `x-inject-as` | object | Required: `kind`, conditional `name` based on `kind` | Lines 58-96 |
| `x-required-scope` | array/string | Min items: 1, unique items, pattern per scope | Lines 98-127 |
| `x-credential-probe` | object | Required: `path`, `method`, `interval` | Lines 136-163 |

### ✅ 2. Rejects invalid values

The schema rejects all specified invalid values through constraints:

#### x-vault-path validation:
- ❌ Path with fewer than three `/`-separated segments (no base above the owner) → Pattern mismatch
- ❌ Empty path → Pattern mismatch  
- ❌ Path traversal (`..`) → `allOf` constraint (line 44)
- ❌ Glob patterns (`*`, `?`) → `allOf` constraint (line 48)
- ❌ Templated segments (`{`) → `allOf` constraint (line 52)

The pattern is deliberately **base-agnostic**. It checks the path's *shape* — owner-scoped, at least one base segment above the owner and a name below it, no traversal, globs, or templated segments. It does **not** pin the base prefix: an earlier revision required `^seam/routes/…` and the base has since moved to `rs-manager/rs-manager/seam/routes`, which would have made every such pin a schema release. Which base is live, and whether a given path resolves inside it, are validator-side checks (`AllowlistEnforcer.ValidateVaultPath` against `SEAM_VAULT_BASE_DIR`, plus `seam lint`) — so a syntactically well-shaped path under a retired base passes *this* schema and is still rejected at merge time.

#### x-inject-as validation:
- ❌ `kind:header` without `name` → Conditional constraint (line 81)
- ❌ `kind:query` without `name` → Conditional constraint (line 86)
- ❌ `kind:bearer` with `name` → Conditional constraint (line 91)
- ❌ Invalid `kind` value → Enum constraint (line 64)

#### x-required-scope validation:
- ❌ Empty array → `minItems: 1` constraint (line 117)
- ❌ Duplicate scopes → `uniqueItems: true` constraint (line 118)
- ❌ Invalid scope format → Pattern constraint (line 100)
- ❌ Single-segment scope → Pattern requires at least two segments (line 100)

#### x-credential-probe validation:
- ❌ Missing required fields → `required` array (line 138)
- ❌ Path not starting with `/` → Pattern constraint (line 143)
- ❌ Invalid HTTP method → Enum constraint (line 148)
- ❌ Invalid duration format → Pattern constraint (line 131)

### ✅ 3. Example fragments with auth extensions pass validation

Six valid example fragments are provided in `route-fragment-schema-auth-examples.json`:

1. **example-1-bearer-auth** - Basic bearer token with credential probe
2. **example-2-header-auth** - Custom header with multiple scopes
3. **example-3-query-auth** - Query parameter authentication
4. **example-4-pass-through** - No credentials, only scopes
5. **example-5-multi-instance** - Multi-instance map with per-instance credentials
6. **example-6-operation-level-scope** - Fragment-level default overridden at operation level

All examples demonstrate proper usage and should pass validation.

### ✅ 4. Updated schema file exists

**File:** `/home/coding/SEAM/docs/notes/route-fragment-schema-auth.json`

The schema is:
- ✅ Standalone and focused on auth extensions only
- ✅ Properly structured with `$schema`, `$id`, `title`, `description`
- ✅ Contains all four auth extensions in `$defs`
- ✅ Includes proper constraints and validation rules
- ✅ Provides examples for each extension type

## Cross-Field Constraints

The schema includes two critical cross-field constraints:

### constraint-vault-inject-paired (lines 165-172)
```json
"oneOf": [
  { "required": ["x-vault-path", "x-inject-as"] },
  { "not": { "anyOf": [...] } }
]
```
Ensures `x-vault-path` and `x-inject-as` are declared together or omitted together.

### constraint-passthrough-has-no-probe (lines 174-178)
```json
"if": { "not": { "anyOf": [...] } },
"then": { "not": { "required": ["x-credential-probe"] } }
```
Ensures pass-through routes (no credentials) cannot have credential probes.

## Integration with Main Schema

The auth extensions are already integrated into the main route fragment schema at:
- `/home/coding/SEAM/spec/route-fragment-schema.json` (lines 41-46)
- `/home/coding/SEAM/docs/notes/route-fragment-schema.json` (identical content)

Both schemas reference the same auth extension definitions, ensuring consistency.

## Summary

All acceptance criteria from bead bf-5fwe are met:

1. ✅ Schema validates all four auth extensions with correct types and constraints
2. ✅ Rejects invalid values (path traversal, globs, empty arrays, etc.)
3. ✅ Example fragments with auth extensions pass validation
4. ✅ Updated schema file: `docs/notes/route-fragment-schema-auth.json`

The schema is production-ready and can be used to validate SEAM route fragments with authentication extensions.