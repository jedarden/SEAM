# OpenAPI 3.1 Fragment Research

**Created:** 2026-08-06  
**Bead:** bf-q8eq  
**Purpose:** Research and document the structure of OpenAPI 3.1 paths/components fragments for SEAM's route-fragment schema design

---

## Executive Summary

OpenAPI 3.1 fragments are partial specifications containing only the `paths` and/or `components` objects, without the top-level `openapi`, `info`, or `servers` fields. SEAM uses fragments as the unit of route authoring—each service ships one or more fragment files that define their API routes along with SEAM-specific `x-*` extension fields.

**Key Finding:** OpenAPI 3.1 is fully backward compatible with 3.0, but introduces JSON Schema 2020-12 as the foundation for schema validation, replacing the proprietary OpenAPI Schema dialect.

---

## 1. OpenAPI 3.1 Fragment Structure

### 1.1 What is a Fragment?

An OpenAPI fragment is a partial specification document that contains only specific objects from the full OpenAPI document. For SEAM's purposes, fragments contain:

- **`paths`** (required): Path templates and their operations
- **`components`** (optional): Reusable schemas, parameters, responses, etc.
- **`x-*` extensions** (SEAM-specific): Gateway configuration fields

**Fragment vs. Full Document:**
- Full OpenAPI documents require `openapi`, `info`, and `servers` fields
- Fragments omit these—they're synthesized by SEAM's merge layer
- Fragments are not independently valid OpenAPI documents
- SEAM's merge layer produces the served complete document

### 1.2 OpenAPI 3.1 Key Changes from 3.0

| Feature | OpenAPI 3.0 | OpenAPI 3.1 |
|---------|-------------|-------------|
| Schema dialect | OpenAPI Schema (subset of JSON Schema draft 5) | **JSON Schema 2020-12** (full spec) |
| `schema` keyword | Limited to OpenAPI Schema dialect | Full JSON Schema 2020-12 vocabulary |
| Schema keywords | `nullable`, `exclusiveMinimum`/`exclusiveMaximum` as booleans | **`type` arrays**, `exclusiveMinimum`/`exclusiveMaximum` as numbers, **`const`**, **`prefixItems`**, **`unevaluatedProperties`** |
| Content negotiation | Limited | Enhanced via `content` in path items |
| Webhooks | No | **Yes** (first-class) |
| Path item refs | `$ref` only | **`$dynamicRef`** for recursive schemas |

**Critical for SEAM:** The validator must support JSON Schema 2020-12 features (`prefixItems`, `const`, `unevaluatedProperties`, `exclusiveMinimum`/`exclusiveMaximum` as numbers).

---

## 2. Fragment Structure Details

### 2.1 Paths Object

The `paths` object is a map of path templates to Path Item objects:

```yaml
paths:
  /users/{userId}:
    get:
      summary: Get user by ID
      operationId: getUser
      parameters:
        - name: userId
          in: path
          required: true
          schema:
            type: string
      responses:
        '200':
          description: User response
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/User'
    put:
      summary: Update user
      # ... operation definition
```

**Path Template Rules:**
- Path templates begin with `/`
- Path parameters use `{paramName}` syntax
- Path parameters map to Path Item Object `parameters` array
- Multiple operations per path (GET, POST, PUT, DELETE, etc.)

### 2.2 Components Object

The `components` object defines reusable artifacts:

```yaml
components:
  schemas:
    User:
      type: object
      required: [id, name]
      properties:
        id:
          type: string
          format: uuid
        name:
          type: string
    Error:
      type: object
      required: [message]
      properties:
        message:
          type: string
        code:
          type: string
  parameters:
    UserId:
      name: userId
      in: path
      required: true
      schema:
        type: string
  responses:
    NotFound:
      description: Resource not found
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/Error'
```

**Components Types:**
- `schemas` — reusable data models
- `parameters` — reusable parameter definitions
- `responses` — reusable response definitions
- `securitySchemes` — authentication schemes (rarely used in fragments)

### 2.3 Minimum Required Fields for Valid Fragment

According to OpenAPI 3.1 specification, a **complete document** requires:
- `openapi: "3.1.0"` (or `3.1.x`)
- `info` object with `title` and `version`
- `paths` object (may be empty, but must be present)

However, **SEAM fragments** have different requirements (enforced by `route-fragment-schema.json`):

| Field | Required? | Notes |
|-------|-----------|-------|
| `x-seam-schema` | **Yes** | Fragment format version marker (`"v1"`), prevents schema guessing |
| `x-seam-owner` | **Yes** | Service name, verified against mounted parent directory |
| `paths` | **Yes** | At least one path template required (`minProperties: 1`) |
| `components` | No | Optional, for reusable schemas/parameters/responses |
| `openapi` | No | **Tolerated but informational** — synthesized by merge layer |
| `info` | No | **Tolerated but informational** — synthesized by merge layer |
| `x-api-version` | No | Defaults to `_unversioned` if absent |

---

## 3. Example Fragments

### 3.1 Minimal Fragment (Pass-through)

```yaml
x-seam-schema: v1
x-seam-owner: echo
x-upstream: https://echo.example.svc.cluster.local
paths:
  /echo:
    get:
      summary: Echo endpoint
      responses:
        '200':
          description: Success
```

### 3.2 Single-Instance Credential-Injecting Fragment

```yaml
x-seam-schema: v1
x-seam-owner: argocd
x-upstream: https://argocd-ro-ardenone-manager-ts.ardenone.com:8444
x-upstream-tls:
  caBundle: argocd-ro.pem
x-upstream-strip-prefix: /argocd
x-vault-path: seam/routes/argocd/ro-token
x-inject-as:
  kind: bearer
  name: authorization
x-credential-probe:
  path: /argocd/api/v1/applications
  method: GET
  interval: 6h
paths:
  /argocd/api/v1/applications:
    get:
      x-required-scope:
        - argocd:read
      summary: List ArgoCD applications (read-only)
      responses:
        '200':
          description: Application list
          content:
            application/json:
              schema:
                type: array
```

### 3.3 Multi-Instance Map Fragment

```yaml
x-seam-schema: v1
x-seam-owner: k8s
x-instance-param: cluster
x-fanout-scope:
  - k8s-ro:get
x-upstream-map:
  ardenone-cluster:
    url: https://traefik-ardenone-cluster:8001
    vaultPath: seam/routes/k8s/ardenone-cluster-ro
    injectAs:
      kind: bearer
      name: authorization
    requiredScope:
      - k8s-ro:get
    probeInterval: 24h
  apexalgo-iad:
    url: http://kubectl-proxy-apexalgo-iad:8001
    plaintext: acknowledged
    vaultPath: seam/routes/k8s/apexalgo-iad-admin
    injectAs:
      kind: bearer
      name: authorization
    requiredScope:
      - k8s-rw:admin
    probeInterval: 12h
paths:
  /k8s/{cluster}/api/v1/pods:
    get:
      x-required-scope:
        - k8s-ro:get
      summary: List pods in cluster
    delete:
      x-required-scope:
        - k8s-rw:delete
      summary: Delete pod
```

### 3.4 Adapter Fragment (Version Migration)

```yaml
x-seam-schema: v1
x-seam-owner: argocd
x-api-version: v1
x-seam-deprecated:
  since: "2026-07-22"
x-adapter:
  targetVersion: v2
  request: []
  response: []
paths:
  /argocd/api/v1/applications:
    get:
      x-required-scope:
        - argocd:read
      summary: List ArgoCD applications (v1 - deprecated)
```

---

## 4. ADR-001: Language Choice Decision

### 4.1 Decision Summary

**ADR-001 (2026-07-20)** ratified **Go** as the SEAM gateway implementation language.

**Key Reasons:**

1. **Validator is the product** — pb33f `libopenapi-validator` is the only mature, off-the-shelf OpenAPI 3.1 request validator across Go, Rust, TypeScript, and Python. Its error model (JSON pointer + line/col + `HowToFix`) maps directly to SEAM's structured 400s.

2. **Tailscale support is first-party** — `tsnet` (embedded node) and `client/local` (WhoIs against sidecar) are maintained by Tailscale itself. Rust's `tailscale-rs` is explicitly experimental.

3. **Security posture comes free** — stdlib `httputil.ReverseProxy`'s `Rewrite` hook strips hop-by-hop and client-spoofable `X-Forwarded-*` headers before user code runs, closing secret injection pitfalls by design.

4. **Cost is bounded and one-time** — Go is a new toolchain in a Rust-centric shop, but that's paid once at setup, not per-route like a hand-rolled validator's maintenance would recur.

### 4.2 Validator Architecture

The Go validator uses **both** JSON Schema and Go structs:

| Component | Validation Method | Purpose |
|-----------|-------------------|---------|
| **OpenAPI-native correctness** | `pb33f/libopenapi` + `libopenapi-validator` | Validates `paths`, `components`, parameter/response schemas, standard `deprecated` boolean |
| **SEAM `x-*` shape validation** | **JSON Schema 2020-12** against `route-fragment-schema.json` | Validates SEAM extension field shapes and intra-object relations |
| **Cross-field/cross-path rules** | **Go code** in `internal/spec` | Validator-side constraints that JSON Schema cannot express |

**Why both?**
- JSON Schema can validate single-object shape and intra-object relations
- JSON Schema **cannot** validate cross-path, cross-fragment, manifest-level, or merge-time constraints
- Go code enforces those higher-level rules **on top of** the schema

### 4.3 JSON Schema Version

**JSON Schema 2020-12** is the mandated version for `route-fragment-schema.json` and all SEAM schema validation.

**Rationale:**
- OpenAPI 3.1 is built on JSON Schema 2020-12
- 2020-12 is the current stable release with broad ecosystem support
- 2020-12 introduces `prefixItems`, `unevaluatedProperties`, `const`, and other features used by OpenAPI 3.1
- pb33f libopenapi-validator uses `santhosh-tekuri/jsonschema` which supports 2020-12

**Engine Used:** The Ajv 8.20 runner (in `~/scratch/seam-schema-verify/`) validates the committed schema against the 2020-12 meta-schema. The Go validator will use the same 2020-12 engine.

---

## 5. Required vs. Optional Fields

### 5.1 Fragment-Root Fields

| Field | Type | Required? | Description |
|-------|------|-----------|-------------|
| `x-seam-schema` | `const: "v1"` | **Yes** | Fragment format version |
| `x-seam-owner` | string | **Yes** | Service owner token |
| `paths` | object (min 1) | **Yes** | Path templates |
| `components` | object | No | Reusable schemas/parameters |
| `x-api-version` | string | No | Contract version (default: `_unversioned`) |
| `x-upstream` | string | Conditional* | Single upstream URL |
| `x-upstream-map` | object | Conditional* | Multi-instance map |
| `x-instance-param` | string | Conditional* | Path parameter for map selection |
| `x-upstream-strip-prefix` | string | No | Literal prefix to strip |
| `x-upstream-tls` | object | No | TLS configuration |
| `x-upstream-plaintext` | `acknowledged` | Conditional* | Required for `http://` upstreams |
| `x-vault-path` | string | Paired† | OpenBao secret path |
| `x-inject-as` | object | Paired† | Injection method |
| `x-credential-probe` | object | No | Credential health check |
| `x-breaker` | object | No | Circuit breaker tuning |
| `x-required-scope` | array/string | No | OAuth2 scope requirement (root default) |
| `x-fanout-scope` | array/string | No | Scope for `_all` fanout |
| `x-seam-deprecated` | object | No | Deprecation metadata |
| `x-adapter` | object | No | Version migration adapter |
| `x-unscrubbable` | `acknowledged` | No | Opt-out of response scrubbing |
| `x-requires-approval` | boolean | No | Reserved (approval-gated routes) |

*Conditional: mutually exclusive with its counterpart, required for non-adapter fragments  
†Paired: both-or-neither constraint

### 5.2 Path-Item-Level Fields

| Field | Type | Required? | Description |
|-------|------|-----------|-------------|
| `x-upstream-path-template` | string | No | Authoritative upstream path |

### 5.3 Operation-Level Fields

| Field | Type | Required? | Description |
|-------|------|-----------|-------------|
| `x-required-scope` | array/string | No | OAuth2 scope requirement |
| `x-loop-guard` | object | No | Loop detection guard |
| `x-cost-per-call` | object | Conditional* | Cost per API call |
| `x-quota` | object | Conditional* | Usage quota |
| `x-unscrubbable` | `acknowledged` | No | Opt-out of response scrubbing |
| `x-requires-approval` | boolean | No | Reserved |

*Conditional: `x-quota` requires `x-cost-per-call`

---

## 6. JSON Schema 2020-12 Features Used

### 6.1 Keywords Used in route-fragment-schema.json

| Keyword | Purpose | Example |
|---------|---------|---------|
| `prefixItems` | Tuple validation | `[type, format, enum]` |
| `const` | Single value | `x-seam-schema: "v1"` |
| `unevaluatedProperties` | Conditional additional properties | `additionalProperties: false` after patternProperties |
| `exclusiveMinimum`/`exclusiveMaximum` | Numeric ranges (as numbers) | `interval: {minimum: 0, exclusiveMinimum: true}` |
| `minProperties`/`maxProperties` | Object size constraints | `paths: {minProperties: 1}` |
| `patternProperties` | Conditional property validation | Duration format validation |
| `allOf` | Schema composition | Cross-field constraints |
| `$ref` | Schema reuse | `$def: ownerToken` |
| `$dynamicRef` | Recursive schemas | (not yet used, reserved for future) |

### 6.2 Cross-Field Constraints (Schema-Encoded)

The schema encodes **10 cross-field constraints** via `allOf`:

1. `constraint-adapter-excludes-upstream-fields` — adapter fragments cannot declare upstream-facing fields
2. `constraint-upstream-xor-map` — `x-upstream` and `x-upstream-map` are mutually exclusive
3. `constraint-forwarding-fragment-has-upstream` — non-adapter fragments must have an upstream target
4. `constraint-map-requires-instance-param` — `x-upstream-map` requires `x-instance-param`
5. `constraint-instance-param-only-with-map` — `x-instance-param` requires `x-upstream-map`
6. `constraint-upstream-excludes-instance-param` — single-`x-upstream` fragments cannot have `x-instance-param`
7. `constraint-vault-inject-paired` — `x-vault-path` and `x-inject-as` are both-or-neither
8. `constraint-passthrough-has-no-probe` — pass-through fragments cannot have probes
9. `constraint-plaintext-required-on-http` — `http://` upstreams require `x-upstream-plaintext`
10. `constraint-plaintext-excludes-map` — `x-upstream-plaintext` is forbidden with `x-upstream-map`

### 6.3 Validator-Side Constraints (Go-Only)

These **cannot** be expressed in JSON Schema and are enforced by `internal/spec`:

1. **Cross-path template validation** — `x-instance-param` must exist in every path
2. **Prefix validation** — `x-upstream-strip-prefix` must prefix every path
3. **Upstream path template validation** — every `{param}` must exist in matched template
4. **Probe route validation** — `x-credential-probe.path` must be a served route
5. **Control-plane namespace reservation** — no path may collide with reserved prefixes
6. **Unit equality** — `x-quota.unit` must equal `x-cost-per-call.unit`
7. **Per-instance resolution** — effective `(vaultPath, injectAs)` per map entry
8. **Upstream allowlist** — host membership in operator-owned allowlist
9. **Vault prefix enforcement** — `x-vault-path` must be within `seam/routes/<owner>/*`
10. **Owner binding** — `x-seam-owner` must equal mounted parent directory
11. **CA bundle existence** — `x-upstream-tls.caBundle` must exist in secret
12. **Collision detection** — `(path, method, x-api-version)` uniqueness across fragments

---

## 7. Validation Corpus

The `route-fragment-schema.json` has been validated against 31 test cases (8 valid, 23 invalid) using Ajv 8.20.0:

### 7.1 Valid Shapes Accepted

1. Single-instance credential-injecting fragment
2. HTTPS pass-through fragment
3. HTTP pass-through with `x-upstream-plaintext`
4. Multi-instance map with per-instance vault/inject
5. Multi-instance map with plaintext entry
6. Adapter fragment with `x-adapter`
7. Operation-level cost + quota
8. Operation-level scope replacing root default

### 7.2 Invalid Shapes Rejected

1. Unpaired vault/inject (vault only)
2. Unpaired vault/inject (inject only)
3. Map without instance param
4. Instance param without map
5. Upstream + map together
6. Adapter + upstream together
7. Upstream-less non-adapter fragment
8. HTTP upstream without plaintext acknowledgment
9. Quota without cost
10. Bare `x-seam-deprecated: true` (must be object)
11. `v2` schema marker (only `v1` accepted)
12. Missing `x-seam-owner`
13. Missing `paths`
14. IP-literal host (rejected by schema)
15. InjectAs kind↔name violations
16. Plaintext alongside map
17. Probe on pass-through fragment
18. Authored `_unversioned` (reserved)
19. Calendar-aligned duration (`1mo`)
20. Brownout without sunset

---

## 8. Implementation Notes

### 8.1 Fragment Merge Process

1. Load all `.yaml`/`.json` files from `/etc/gateway/routes.d/<svc>/`
2. Validate each fragment against `route-fragment-schema.json`
3. Check validator-side constraints (owner binding, vault prefix, allowlist)
4. Detect collisions on `(path, method, x-api-version)`
5. Merge into single OpenAPI document
6. Populate `servers` with caller-facing base URL
7. Build libopenapi model and validator
8. Serve merged spec at `/openapi.json`

### 8.2 Fragment Loading in Go

Current implementation (from `internal/spec/loader.go`):

```go
// NewWithFragments creates a new spec loader that uses fragment-based loading
func NewWithFragments(specDir, baseURL, schemaPath string) (*Loader, error) {
    // Create fragment loader
    fragmentLoader, err := NewFragmentLoader()
    
    // Load fragments from directory
    fragmentsDir := filepath.Join(specDir, "fragments.d")
    if err := fragmentLoader.LoadDirectory(fragmentsDir); err != nil {
        return nil, fmt.Errorf("failed to load fragments: %w", err)
    }
    
    // Validate fragments against schema
    if schemaPath != "" {
        if err := fragmentLoader.ValidateFragments(schemaPath); err != nil {
            return nil, fmt.Errorf("fragment validation failed: %w", err)
        }
    }
    
    // Merge fragments into a single document
    mergedJSON, err := fragmentLoader.MergeFragments(baseURL)
    
    // Load merged document using libopenapi
    loadedDoc, err := libopenapi.NewDocument(mergedJSON)
    
    // Build model and create validator
    documentModel, err := loadedDoc.BuildV3Model()
    v := validator.NewValidatorFromV3Model(&model.Model, nil)
    
    return &Loader{...}, nil
}
```

### 8.3 JSON Schema Engine Choice

**Current:** Ajv 8.20.0 (JavaScript) for schema validation of the schema itself (in test harness)

**Planned Go Engine:** `santhosh-tekuri/jsonschema` (already used by pb33f libopenapi-validator)

**Rationale:** 
- Same engine used by OpenAPI validation layer
- Full JSON Schema 2020-12 support
- Active maintenance and OpenAPI 3.1 conformance
- Native Go integration (no JavaScript bridge)

---

## 9. Open Questions Resolved

| Question | Answer | Source |
|----------|--------|--------|
| **Language for validator?** | Go (ADR-001) | ADR-001 decision |
| **JSON Schema vs Go structs?** | **Both** — JSON Schema for shape, Go for cross-field rules | route-fragment-schema.md §2 |
| **JSON Schema version?** | 2020-12 (OpenAPI 3.1 foundation) | ADR-001 research |
| **Fragment requires `openapi` field?** | No — synthesized by merge layer | route-fragment-schema.md §1 |
| **Fragment requires `info` field?** | No — synthesized by merge layer | route-fragment-schema.md §1 |
| **Minimum fragment structure?** | `x-seam-schema`, `x-seam-owner`, `paths` | route-fragment-schema.json |
| **Can fragments have `x-api-version`?** | Yes, optional (default: `_unversioned`) | route-fragment-schema.md §3.1 |

---

## 10. References

### 10.1 OpenAPI 3.1 Specification

- **Official Spec:** https://spec.openapis.org/oas/v3.1.0
- **JSON Schema 2020-12:** https://json-schema.org/draft/2020-12/release-notes
- **Changes from 3.0:** https://swagger.io/blog/news/whats-new-in-openapi-3-1/

### 10.2 SEAM Documentation

- **Route Fragment Schema:** `/home/coding/SEAM/docs/notes/route-fragment-schema.md`
- **Plan:** `/home/coding/SEAM/docs/plan/plan.md`
- **Language Runtime Choice Research:** `/home/coding/SEAM/docs/research/language-runtime-choice.md`
- **Schema File:** `/home/coding/SEAM/docs/notes/route-fragment-schema.json`

### 10.3 Implementation

- **Loader Code:** `/home/coding/SEAM/internal/spec/loader.go`
- **pb33f libopenapi:** https://pb33f.io/libopenapi/
- **pb33f libopenapi-validator:** https://github.com/pb33f/libopenapi-validator

### 10.4 External Resources

- **OpenAPI 3.1 Introduction:** https://swagger.io/docs/specification/basic-structure/
- **JSON Schema 2020-12 Understanding:** https://json-schema.org/understanding-json-schema/
- **Go httputil.ReverseProxy:** https://pkg.go.dev/net/http/httputil#ReverseProxy

---

## Appendix: Validation Test Results

**Test Run:** 2026-07-22  
**Engine:** Ajv 8.20.0  
**Schema:** route-fragment-schema.json  
**Meta-Validation:** ✅ Valid against 2020-12 meta-schema  
**Compilation:** ✅ No dangling `$ref`  
**Accept Tests:** ✅ 8/8 valid shapes accepted  
**Reject Tests:** ✅ 21/21 invalid shapes rejected  

**Total:** 31/31 checks green ✅

---

**Document Status:** Complete ✅  
**Next Steps:** Use this research to inform Phase 1b fragment merge implementation and schema refinement.
