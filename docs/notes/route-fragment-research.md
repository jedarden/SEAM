# OpenAPI 3.1 Fragment Patterns Research & ADR-001 Resolution

**Date:** 2026-08-07  
**Task:** Research OpenAPI fragment patterns and resolve ADR-001 language choice for route-fragment schema

---

## Decision

**CHOSEN_FORMAT: json-schema**

The route-fragment schema is defined as **JSON Schema draft-2020-12**, not Go structs.

---

## ADR-001 Language Choice

**ADR-001 (2026-07-20): Ratify Go as the SEAM Gateway Implementation Language**

### Key Points from ADR-001:

1. **Language Decision:** Go was chosen as the implementation language for the SEAM gateway
2. **Primary Reason:** pb33f `libopenapi` + `libopenapi-validator` is the only mature, off-the-shelf OpenAPI 3.1 request validator available
3. **Schema Format:** While the gateway is Go, the **route-fragment schema is JSON Schema draft-2020-12** (language-independent)
4. **Rationale:** JSON Schema allows:
   - Language-independent validation (used by `seam lint` CLI tool)
   - Runtime validation in Go gateway via `jsonschema` compiler
   - Potential future use by non-Go tools
   - Clear separation between schema definition and implementation

### From ADR-001 Consequences:

> **"The LANGUAGE blocker is closed; Phase 1 is not wholly unblocked."**
> 
> Phase 1b (fragment merge, validation, quarantine) **remains gated on the route-fragment schema** (Open Question 1, bead `bf-2wt`), which is now the critical path for the project.

The ADR clarifies that while Go is the implementation language, the schema format itself is a separate concern.

---

## OpenAPI 3.1 Fragment Patterns

### 1. Fragment Schema Definition

The SEAM route-fragment schema is defined in `/home/coding/SEAM/spec/route-fragment-schema.json` as a concrete JSON Schema draft-2020-12 document.

**Schema Metadata:**
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://ardenone.com/seam/route-fragment-schema/v1.json",
  "title": "SEAM Route Fragment (x-seam-schema v1)"
}
```

**Structure:**
- Fragment-root SEAM extension fields: `x-seam-schema`, `x-seam-owner`, `x-api-version`, `x-upstream`, `x-vault-path`, etc.
- Standard OpenAPI 3.1 fields: `openapi`, `info`, `paths`, `components`
- Operation-level extensions: `x-required-scope`, `x-loop-guard`, `x-cost-per-call`, `x-quota`

### 2. Existing Fragment Examples

**Example 1: Simple Proxy (from `/examples/fragments/1-argocd-read-only-proxy.yaml`)**

```yaml
x-seam-schema: v1
x-seam-owner: argocd-ro
x-api-version: v1
x-upstream: https://argocd-ro-ardenone-manager-ts.ardenone.com:8444
x-upstream-strip-prefix: /argocd
x-required-scope: argocd:read

openapi: 3.1.0
info:
  title: ArgoCD Read-Only Proxy
  version: 1.0.0

paths:
  /argocd/api/v1/applications:
    get:
      summary: List all applications
      operationId: listApplications
      responses:
        '200':
          description: Successfully retrieved applications list
```

**Example 2: Secret Injection (from `/examples/fragments/2-secret-injecting-route.yaml`)**

```yaml
x-seam-schema: v1
x-seam-owner: weather-service
x-api-version: v1
x-upstream: https://weather-backend.ardenone.internal
x-vault-path: seam/routes/weather-service/api-key
x-inject-as:
  kind: header
  name: X-API-Key
x-credential-probe:
  path: /health
  method: GET
  interval: 5m
x-breaker:
  threshold: 10
  openSeconds: 30
  maxOpenSeconds: 300

paths:
  /weather/current:
    get:
      summary: Get current weather for a location
      operationId: getCurrentWeather
      x-loop-guard:
        max_iterations: 3
        backoff_ms: 100
```

### 3. Fragment Schema Patterns from Open-Source

Based on research of OpenAPI 3.1 fragment patterns:

**Pattern 1: Component-Based Reusability**
- OpenAPI 3.1 supports the `components` object for reusable schemas, parameters, and responses
- Fragments can define components that are merged into the final document
- Reference: [OpenAPI Specification 3.1.0](https://github.com/OAI/OpenAPI-Specification/blob/main/versions/3.1.0.md)

**Pattern 2: Extension Fields (x-*)**
- OpenAPI 3.1 allows custom extension fields prefixed with `x-`
- SEAM uses: `x-seam-schema`, `x-seam-owner`, `x-api-version`, `x-upstream`, `x-vault-path`, `x-inject-as`, etc.
- These carry gateway-specific metadata not covered by standard OpenAPI
- Reference: [OpenAPI Specification - Extensions](https://swagger.io/specification/)

**Pattern 3: Path Item Fragments**
- Fragments define a subset of `paths` rather than a complete document
- Multiple fragments are merged at runtime by the gateway
- Collision detection on (path, method, `x-api-version`) triple
- Each fragment is owned by a service (matched to directory structure: `fragments.d/<owner>/`)

---

## Rationale for JSON Schema over Go Structs

### Why JSON Schema?

1. **Language Independence**
   - `seam lint` CLI tool can validate fragments without needing to compile Go code
   - Schema can be used by other tools (Python validators, TypeScript clients, etc.)
   - Clear separation of concerns: schema definition vs. implementation

2. **Runtime Validation**
   - Go gateway uses `github.com/santhosh-tekuri/jsonschema/v6` compiler
   - Fragments are validated at load time before merging
   - Failed validation triggers quarantine with clear error messages

3. **OpenAPI 3.1 Compliance**
   - JSON Schema draft-2020-12 is the schema format used by OpenAPI 3.1
   - Consistent with ecosystem patterns (e.g., [APIDevTools/openapi-schemas](https://github.com/APIDevTools/openapi-schemas))
   - Can be referenced by `$schema` and `$id` for portability

4. **Validation Constraints**
   - JSON Schema can express shape constraints (patterns, min/max, required fields)
   - Cross-field constraints (e.g., "x-vault-path requires x-inject-as") expressed via `allOf` + conditional schemas
   - More complex constraints are enforced by Go validator layer (business logic, directory checking, allowlists)

### Why NOT Go Structs?

Go structs would couple the fragment schema too tightly to the implementation:
- Schema changes would require recompilation
- Non-Go tools couldn't validate fragments without embedding Go logic
- JSON Schema is the industry standard for OpenAPI validation
- ADR-001's Go choice applies to the **gateway implementation**, not the **schema definition**

---

## Examples to Emulate

### 1. APIDevTools/openapi-schemas
**Repository:** [https://github.com/APIDevTools/openapi-schemas](https://github.com/APIDevTools/openapi-schemas)

- Provides JSON Schemas for every version of the OpenAPI Specification
- Uses `$schema`, `$id`, `title`, `description` metadata
- Defines reusable `$defs` for common patterns
- Pattern: Concrete JSON Schema files that can validate OpenAPI documents

### 2. OAI/OpenAPI-Specification
**Repository:** [https://github.com/OAI/OpenAPI-Specification](https://github.com/OAI/OpenAPI-Specification)

- Official OpenAPI specification with JSON Schema validation
- Demonstrates extension field patterns (`x-*` prefixes)
- Shows how to compose documents from multiple parts
- Pattern: Specification-first approach with clear extension points

### 3. SEAM's Own Fragment Schema
**File:** `/home/coding/SEAM/spec/route-fragment-schema.json`

- Concrete JSON Schema draft-2020-12 for route fragments
- Defines SEAM extension fields (`x-seam-*`)
- Uses `$defs` for reusable field definitions
- Uses `allOf` for cross-field constraint validation
- Pattern: Extension schema that builds on OpenAPI 3.1 foundation

---

## Implementation in Go Gateway

The Go gateway (`internal/spec/fragment.go`) implements fragment validation:

```go
type FragmentLoader struct {
    schemaCompiler *jsonschema.Compiler  // JSON Schema compiler
    fragments      []*Fragment
    quarantined    []*Fragment
    document       libopenapi.Document
}

func (fl *FragmentLoader) ValidateFragments(schemaPath string) error {
    // Load route-fragment-schema.json
    schemaBytes, _ := os.ReadFile(schemaPath)
    
    // Compile JSON Schema
    fl.schemaCompiler.AddResource("schema.json", schemaDef)
    schema := fl.schemaCompiler.Compile("schema.json")
    
    // Validate each fragment
    for _, fragment := range fl.fragments {
        if err := schema.Validate(fragment.ParsedFragment); err != nil {
            fragment.QueuedForQuarantine = true
            fragment.QuarantineReasons = append(fragment.QuarantineReasons, err.Error())
        }
    }
}
```

This pattern allows:
1. Schema definition as a standalone JSON file
2. Runtime validation in Go without recompilation
3. Clear error messages from JSON Schema validation
4. Business logic constraints enforced in Go code (owner checking, reserved paths, etc.)

---

## Conclusion

**CHOSEN_FORMAT: json-schema**

The route-fragment schema uses **JSON Schema draft-2020-12** defined in `/home/coding/SEAM/spec/route-fragment-schema.json`. This is:

1. **Consistent with ADR-001:** Go is the gateway implementation language; JSON Schema is the validation format
2. **Language-independent:** Can be used by `seam lint` and other tools
3. **OpenAPI 3.1 compliant:** Uses the same schema format as OpenAPI itself
4. **Runtime-friendly:** Validated in Go via `jsonschema` compiler
5. **Extensible:** Clear extension fields (`x-seam-*`) for gateway-specific metadata

The existing implementation in `internal/spec/fragment.go` demonstrates this pattern: fragments are validated against the JSON Schema at load time, with quarantining of invalid fragments.

---

## References

- [OpenAPI Specification 3.1.0](https://github.com/OAI/OpenAPI-Specification/blob/main/versions/3.1.0.md)
- [OpenAPI Specification - Extensions](https://swagger.io/specification/)
- [APIDevTools/openapi-schemas](https://github.com/APIDevTools/openapi-schemas)
- [Reusing Description - OpenAPI Documentation](https://learn.openapis.org/specification/components.html)
- [OpenAPI 3.1 PetStore Example](https://gist.github.com/seriousme/55bd4c8ba2e598e416bb55bd4cd362dc)
- ADR-001 in `/home/coding/SEAM/docs/plan/plan.md` (lines 1054-1080)
- Route Fragment Schema in `/home/coding/SEAM/spec/route-fragment-schema.json`
- Fragment examples in `/home/coding/SEAM/examples/fragments/`
