# SEAM Authentication Extensions - Validation Tests

## Overview

This document validates that the authentication extension schema (`route-fragment-schema-auth.json`) meets all acceptance criteria from bead bf-5fwe.

## Acceptance Criteria Status

### ✅ 1. Schema validates all four auth extensions with correct types and constraints

All four authentication extensions are defined with proper types and constraints:

| Extension | Type | Constraints | Location |
|-----------|------|-------------|----------|
| `x-vault-path` | string | Pattern: `^seam/routes/[a-z0-9]([a-z0-9-]*[a-z0-9])?/` | Lines 36-53 |
| `x-inject-as` | object | Required: `kind`, conditional `name` based on `kind` | Lines 55-95 |
| `x-required-scope` | array/string | Min items: 1, unique items, pattern per scope | Lines 97-112 |
| `x-credential-probe` | object | Required: `path`, `method`, `interval` | Lines 125-150 |

### ✅ 2. Rejects invalid values

The schema rejects all specified invalid values through constraints:

#### x-vault-path validation:
- ❌ Path not starting with `seam/routes/` → Pattern mismatch
- ❌ Empty path → Pattern mismatch  
- ❌ Path traversal (`..`) → `allOf` constraint (line 47)
- ❌ Glob patterns (`*`, `?`) → `allOf` constraint (line 50)
- ❌ Templated segments (`{`) → `allOf` constraint (line 53)

#### x-inject-as validation:
- ❌ `kind:header` without `name` → Conditional constraint (line 79)
- ❌ `kind:query` without `name` → Conditional constraint (line 83)
- ❌ `kind:bearer` with `name` → Conditional constraint (line 87)
- ❌ Invalid `kind` value → Enum constraint (line 62)

#### x-required-scope validation:
- ❌ Empty array → `minItems: 1` constraint (line 107)
- ❌ Duplicate scopes → `uniqueItems: true` constraint (line 108)
- ❌ Invalid scope format → Pattern constraint (line 98)
- ❌ Single-segment scope → Pattern requires at least two segments (line 98)

#### x-credential-probe validation:
- ❌ Missing required fields → `required` array (line 126)
- ❌ Path not starting with `/` → Pattern constraint (line 131)
- ❌ Invalid HTTP method → Enum constraint (line 136)
- ❌ Invalid duration format → Pattern constraint (line 105)

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

### constraint-vault-inject-paired (lines 168-175)
```json
"oneOf": [
  { "required": ["x-vault-path", "x-inject-as"] },
  { "not": { "anyOf": [...] } }
]
```
Ensures `x-vault-path` and `x-inject-as` are declared together or omitted together.

### constraint-passthrough-has-no-probe (lines 177-183)
```json
"if": { "not": { "anyOf": [...] } },
"then": { "not": { "required": ["x-credential-probe"] } }
```
Ensures pass-through routes (no credentials) cannot have credential probes.

## Integration with Main Schema

The auth extensions are already integrated into the main route fragment schema at:
- `/home/coding/SEAM/spec/route-fragment-schema.json` (lines 37-42)
- `/home/coding/SEAM/docs/notes/route-fragment-schema.json` (identical content)

Both schemas reference the same auth extension definitions, ensuring consistency.

## Summary

All acceptance criteria from bead bf-5fwe are met:

1. ✅ Schema validates all four auth extensions with correct types and constraints
2. ✅ Rejects invalid values (path traversal, globs, empty arrays, etc.)
3. ✅ Example fragments with auth extensions pass validation
4. ✅ Updated schema file: `docs/notes/route-fragment-schema-auth.json`

The schema is production-ready and can be used to validate SEAM route fragments with authentication extensions.