# Bead bf-1l4q: Base OpenAPI 3.1 Fragment Schema

**Created:** 2026-08-06
**Bead ID:** bf-1l4q

## Summary

Created a JSON Schema 2020-12 compliant base OpenAPI 3.1 fragment schema that validates paths and components objects without SEAM-specific extensions.

## Deliverables

### 1. base-fragment-schema.json
Created `/home/coding/SEAM/docs/notes/base-fragment-schema.json` - a comprehensive JSON Schema that validates:

**Required fields:**
- `paths` (minimum 1 path template)

**Optional fields:**
- `components` (schemas, parameters, responses, etc.)
- `openapi` (informational, for compatibility)
- `info` (informational, for compatibility)
- `servers` (informational, for compatibility)

**Schema covers:**
- Path items with operations (get, put, post, delete, etc.)
- Parameters (query, header, path, cookie) with proper validation
- Request bodies and responses
- Components (schemas, parameters, responses, examples, etc.)
- Proper HTTP status code validation (100-599 or "default")
- Path parameter required enforcement (path parameters must be `required: true`)
- Media types and content negotiation
- Links, callbacks, security schemes

### 2. Test Suite
Created `/home/coding/SEAM/docs/notes/test-base-fragment.js` - Node.js test script using Ajv 8.x:

**Valid test cases (8):**
- Minimal valid fragment
- Fragment with components
- Path parameters
- Multiple operations
- With optional openapi field
- Reusable parameters in components
- Response with content
- Multiple paths

**Invalid test cases (10):**
- Missing paths
- Empty paths object
- Path does not start with /
- Operation missing responses
- Empty responses object
- Response missing description
- Invalid openapi version
- Parameter missing required fields
- Path parameter not marked required
- Invalid HTTP status code

## Test Results

```
✅ ALL TESTS PASSED
Results: 18 passed, 0 failed out of 18 tests
```

## Key Design Decisions

1. **JSON Schema 2020-12 compliance** - Uses modern features like `prefixItems`, `const`, `unevaluatedProperties` as specified in ADR-001

2. **OpenAPI 3.1 native structure** - Follows official OpenAPI 3.1 spec for paths/components without SEAM x-* extensions

3. **Proper HTTP status validation** - Only accepts valid HTTP status codes (1xx-5xx ranges or "default")

4. **Path parameter enforcement** - Path parameters must be marked as `required: true` per OpenAPI spec

5. **Separation of concerns** - Base schema validates only OpenAPI-native structure; SEAM extensions will be added in a separate schema (child bead)

## Next Steps

Child bead will add SEAM-specific x-* extensions to build the complete route-fragment-schema.json.

## ADR-001 Compliance

Uses JSON Schema 2020-12 as required by ADR-001. Schema will be used alongside Go structs for validation:
- JSON Schema: shape validation and intra-object constraints
- Go code: cross-field, cross-fragment, and merge-time constraints
