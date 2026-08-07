# OpenAPI 3.1 Fragment Structure and SEAM Extension Fields

**Created:** 2026-08-07  
**Bead:** bf-up6r  
**Purpose:** Comprehensive documentation of OpenAPI 3.1 fragment patterns and SEAM extension fields for gateway route configuration

---

## Part 1: OpenAPI 3.1 Fragment Structure

### 1.1 What is a Fragment?

An OpenAPI fragment is a **partial specification document** containing only specific objects from a full OpenAPI document. SEAM uses fragments as the unit of route authoring—each service ships one or more fragment files that define their API routes along with SEAM-specific `x-*` extension fields.

**Fragment vs. Full Document:**
- Full OpenAPI documents require `openapi`, `info`, and `servers` fields
- Fragments omit these—they're synthesized by SEAM's merge layer
- Fragments are not independently valid OpenAPI documents
- SEAM's merge layer produces the served complete document

### 1.2 Valid Fragment Subset

**Supported Objects:**

| Object | Required? | Description |
|-------|-----------|-------------|
| `paths` | **Yes** (min 1) | Path templates and their operations |
| `components` | No | Reusable schemas, parameters, responses |
| `x-*` extensions | Varies | SEAM-specific configuration fields |

**Not Supported in Fragments:**
- `openapi` — tolerated but informational (synthesized by merge layer)
- `info` — tolerated but informational (synthesized by merge layer)
- `servers` — never used (upstream hosts live only in SEAM `x-*` fields)

### 1.3 Paths Object Structure

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
```

**Path Template Rules:**
- Must begin with `/`
- Path parameters use `{paramName}` syntax
- Multiple operations per path allowed (GET, POST, PUT, DELETE, etc.)
- At least one path template required (`minProperties: 1`)

### 1.4 Components Object Structure

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
```

**Components Types:**
- `schemas` — reusable data models
- `parameters` — reusable parameter definitions
- `responses` — reusable response definitions
- `securitySchemes` — authentication schemes (rarely used in fragments)

### 1.5 Reference Resolution Rules

**$ref Resolution:**
- **Internal references** (`#/components/schemas/...`) — fully supported within fragment
- **Cross-fragment references** — NOT supported (fragments are independent units)
- **External references** — NOT supported (no `"$ref": "https://..."`)

**Reference Validation:**
- All internal references must resolve within the same fragment
- The merge layer produces a complete document with resolved references
- Cross-path or cross-fragment dependencies are enforced by Go validator, not schema

### 1.6 Required vs. Optional Fields

**Fragment-Level Required Fields:**

| Field | Type | Required? | Default |
|-------|------|-----------|---------|
| `x-seam-schema` | `const: "v1"` | **Yes** | — |
| `x-seam-owner` | string | **Yes** | — |
| `paths` | object (min 1) | **Yes** | — |
| `components` | object | No | — |
| `x-api-version` | string | No | `_unversioned` |

**Conditional Requirements:**
- `x-upstream` OR `x-upstream-map` — required for non-adapter fragments
- `x-instance-param` — required with `x-upstream-map`, forbidden without it
- `x-vault-path` AND `x-inject-as` — both-or-neither constraint
- `x-upstream-plaintext` — required for `http://` upstreams

---

## Part 2: SEAM Extension Fields

### 2.1 Fragment-Root Fields

#### x-seam-schema
- **Type:** `const: "v1"`
- **Required:** Yes
- **Constraints:** Only accepted value is `"v1"`
- **Example:** `"x-seam-schema": "v1"`
- **Relationships:** None
- **Purpose:** Versions the format SEAM parses. A `v2` fragment would be rejected, not guessed

#### x-seam-owner
- **Type:** string (pattern: `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
- **Required:** Yes
- **Constraints:** One segment, no slashes, no dots
- **Example:** `"x-seam-owner": "argocd"`
- **Relationships:** Must equal mounted parent directory; bounds `x-vault-path` prefix
- **Purpose:** Redundant in-file declaration of service owner, verified against filesystem

#### x-api-version
- **Type:** string (pattern: `^v[1-9][0-9]*$`)
- **Required:** No
- **Constraints:** Must be `v` followed by 1-9 and zero or more digits
- **Example:** `"x-api-version": "v1"`
- **Relationships:** Absent → keyed as `_unversioned` (reserved value, cannot author)
- **Purpose:** Versions the contract SEAM serves, enables coexistence of multiple API versions

#### x-upstream
- **Type:** string (URI pattern)
- **Required:** Conditional (mutually exclusive with `x-upstream-map`)
- **Constraints:** HTTPS or HTTP; host must be in operator-owned allowlist; IP literals rejected
- **Example:** `"x-upstream": "https://argocd-ro.example.com:8444"`
- **Relationships:** Mutually exclusive with `x-upstream-map`; requires `x-upstream-plaintext` for `http://`
- **Purpose:** Single upstream base URL for simple routes

#### x-upstream-map
- **Type:** object (map of instance name → entry object)
- **Required:** Conditional (mutually exclusive with `x-upstream`)
- **Constraints:** Each entry has `url` (required), `vaultPath`, `injectAs`, `requiredScope`, `probeInterval`, `plaintext`
- **Example:**
```json
"x-upstream-map": {
  "ardenone-cluster": {
    "url": "https://traefik-ardenone-cluster:8001",
    "vaultPath": "seam/routes/k8s/ardenone-cluster-ro",
    "injectAs": { "kind": "bearer" },
    "requiredScope": ["k8s-ro:get"],
    "probeInterval": "24h"
  }
}
```
- **Relationships:** Requires `x-instance-param`; mutually exclusive with `x-upstream`
- **Purpose:** Multi-instance map for routing to multiple upstreams (e.g., multiple Kubernetes clusters)

#### x-instance-param
- **Type:** string (bare parameter name)
- **Required:** Conditional
- **Constraints:** Must be a path parameter present in EVERY path in the fragment
- **Example:** `"x-instance-param": "cluster"`
- **Relationships:** Required with `x-upstream-map`, forbidden with `x-upstream`
- **Purpose:** Names the path parameter that selects which `x-upstream-map` entry to use

#### x-upstream-strip-prefix
- **Type:** string (literal prefix pattern)
- **Required:** No
- **Constraints:** Must begin with `/`, must not end with `/`, must not contain `{`
- **Example:** `"x-upstream-strip-prefix": "/argocd"`
- **Relationships:** Must be a prefix of every path in the fragment
- **Purpose:** Shorthand for stripping a service prefix before forwarding

#### x-upstream-tls
- **Type:** object
- **Required:** No
- **Constraints:** Optional fields: `caBundle` (string), `serverName` (string), `insecureSkipVerify` (`"acknowledged"`)
- **Example:**
```json
"x-upstream-tls": {
  "caBundle": "argocd-ro.pem",
  "serverName": "argocd.example.com"
}
```
- **Relationships:** `insecureSkipVerify: "acknowledged"` is the only accepted value for opt-out
- **Purpose:** TLS verification configuration; absent = verify against system trust store + hostname

#### x-upstream-plaintext
- **Type:** `const: "acknowledged"`
- **Required:** Conditional
- **Constraints:** Required when `x-upstream` is `http://`; mutually exclusive with `x-upstream-map`
- **Example:** `"x-upstream-plaintext": "acknowledged"`
- **Relationships:** Forbidden with `x-upstream-map` (use per-entry `plaintext` there)
- **Purpose:** Acknowledges that plaintext HTTP is acceptable for tailnet-internal or in-cluster hops only

#### x-vault-path
- **Type:** string (vault path pattern)
- **Required:** Paired
- **Constraints:** Must match `^seam/routes/<owner>/[^*]+$` where `<owner>` equals `x-seam-owner`
- **Example:** `"x-vault-path": "seam/routes/argocd/ro-token"`
- **Relationships:** Both-or-neither with `x-inject-as`
- **Purpose:** Which OpenBao secret to fetch for credential injection

#### x-inject-as
- **Type:** object
- **Required:** Paired
- **Constraints:** Must have `kind` (enum: `"header"`, `"bearer"`, `"query"`) and `name` (string)
- **Example:**
```json
"x-inject-as": {
  "kind": "bearer",
  "name": "authorization"
}
```
- **Relationships:** Both-or-neither with `x-vault-path`; for `kind: "header"` the `name` is the header name; for `kind: "bearer"` the `name` is ignored (always `Authorization: Bearer`)
- **Purpose:** How to inject the fetched credential

#### x-credential-probe
- **Type:** object
- **Required:** No
- **Constraints:** Must have `path` (string beginning with `/`), `method` (HTTP method), `interval` (duration pattern)
- **Example:**
```json
"x-credential-probe": {
  "path": "/argocd/api/v1/applications",
  "method": "GET",
  "interval": "6h"
}
```
- **Relationships:** Forbidden on pass-through fragments (no `x-vault-path`/`x-inject-as`)
- **Purpose:** Credential health sentinel configuration for proactive validation

#### x-breaker
- **Type:** object
- **Required:** No
- **Constraints:** Optional fields: `enabled` (boolean, default true), tuning parameters
- **Example:**
```json
"x-breaker": {
  "enabled": false
}
```
- **Relationships:** Upstream-facing → forbidden on adapter fragments
- **Purpose:** Circuit breaker tuning per upstream

#### x-required-scope (fragment-root)
- **Type:** array of strings or single string
- **Required:** No
- **Constraints:** Array of scope strings or single string (converted to one-element array)
- **Example:** `"x-required-scope": ["argocd:read", "seam:ops"]` or `"x-required-scope": "argocd:read"`
- **Relationships:** Operation-level value replaces fragment-root default; conjunctive (all scopes required)
- **Purpose:** OAuth2 scope requirement as fragment-root default

#### x-fanout-scope
- **Type:** array of strings or single string
- **Required:** No
- **Constraints:** Same as `x-required-scope`
- **Example:** `"x-fanout-scope": ["k8s-ro:get"]`
- **Relationships:** In addition to operation-level scope; gates the `_all` fan-out
- **Purpose:** Additional scope requirement for `_all` multi-instance fan-out requests

#### x-seam-deprecated
- **Type:** object
- **Required:** No
- **Constraints:** Must have `since` (ISO 8601 date); optional: `sunset` (ISO 8601 date), `brownout` (array of `{start, end}` date ranges)
- **Example:**
```json
"x-seam-deprecated": {
  "since": "2026-07-22",
  "sunset": "2026-12-31",
  "brownout": [
    {"start": "2026-11-01", "end": "2026-11-30"}
  ]
}
```
- **Relationships:** Object form required (bare boolean rejected)
- **Purpose:** Marks every route in the fragment as deprecated with optional sunset and brownout periods

#### x-adapter
- **Type:** object
- **Required:** No
- **Constraints:** Must have `targetVersion` (string matching `^v[1-9][0-9]*$`); optional: `request`, `response` arrays
- **Example:**
```json
"x-adapter": {
  "targetVersion": "v2",
  "request": [],
  "response": []
}
```
- **Relationships:** Mutually exclusive with all upstream-facing fields
- **Purpose:** Declarative request/response transforms for version migration

#### x-unscrubbable (fragment-root)
- **Type:** `const: "acknowledged"`
- **Required:** No
- **Constraints:** Only accepted value is `"acknowledged"`
- **Example:** `"x-unscrubbable": "acknowledged"`
- **Relationships:** Also allowed at operation level
- **Purpose:** Opt-out of response scrubbing for routes that cannot be scrubbed

#### x-requires-approval (fragment-root)
- **Type:** boolean
- **Required:** No
- **Constraints:** Accepted but not enforced (reserved for future approval-gated routes)
- **Example:** `"x-requires-approval": true`
- **Relationships:** None
- **Purpose:** Reserved field for future approval workflow

### 2.2 Path-Item-Level Fields

#### x-upstream-path-template
- **Type:** string (pattern: `^/`)
- **Required:** No
- **Constraints:** Must begin with `/`; every `{param}` must exist in matched path template
- **Example:** `"/api/v1/pods/{namespace}"`
- **Relationships:** Upstream-facing → forbidden on adapter fragments; wins over `x-upstream-strip-prefix`
- **Purpose:** Authoritative upstream path template in the upstream's terms

### 2.3 Operation-Level Fields

#### x-required-scope (operation-level)
- **Type:** array of strings or single string
- **Required:** No
- **Constraints:** Same as fragment-root form
- **Example:** In a `get` operation: `"x-required-scope": ["k8s-ro:get"]`
- **Relationships:** Replaces fragment-root default (never merges)
- **Purpose:** Per-method OAuth2 scope requirement

#### x-loop-guard
- **Type:** object
- **Required:** No
- **Constraints:** Must have `max_iterations` (integer >= 1) and `backoff_ms` (integer >= 0)
- **Example:**
```json
"x-loop-guard": {
  "max_iterations": 3,
  "backoff_ms": 100
}
```
- **Relationships:** Absent = no loop guard on that route
- **Purpose:** Loop detection guard configuration

#### x-cost-per-call
- **Type:** number
- **Required:** Conditional
- **Constraints:** Must be >= 0
- **Example:** `"x-cost-per-call": 1`
- **Relationships:** Required if `x-quota` is present on same operation
- **Purpose:** Cost per API call in arbitrary units for quota tracking

#### x-quota
- **Type:** object
- **Required:** Conditional
- **Constraints:** Must have `limit` (integer >= 1), `window_seconds` (integer >= 1), and `unit` (string)
- **Example:**
```json
"x-quota": {
  "limit": 1000,
  "window_seconds": 3600,
  "unit": "call"
}
```
- **Relationships:** Requires `x-cost-per-call` on same operation; `unit` must match `x-cost-per-call.unit`
- **Purpose:** Usage quota configuration with tumbling window

#### x-unscrubbable (operation-level)
- **Type:** `const: "acknowledged"`
- **Required:** No
- **Constraints:** Same as fragment-root form
- **Example:** In a `post` operation: `"x-unscrubbable": "acknowledged"`
- **Relationships:** Overrides fragment-root setting
- **Purpose:** Operation-level opt-out of response scrubbing

#### x-requires-approval (operation-level)
- **Type:** boolean
- **Required:** No
- **Constraints:** Same as fragment-root form
- **Example:** In a `delete` operation: `"x-requires-approval": true`
- **Relationships:** None
- **Purpose:** Reserved field for future approval workflow

---

## Part 3: Cross-Field Constraints

### 3.1 Schema-Encoded Constraints

The JSON Schema encodes these constraints via `allOf`:

1. **adapter-excludes-upstream-fields** — `x-adapter` fragments cannot declare upstream-facing fields
2. **upstream-xor-map** — `x-upstream` and `x-upstream-map` are mutually exclusive
3. **forwarding-fragment-has-upstream** — non-adapter fragments must have an upstream target
4. **map-requires-instance-param** — `x-upstream-map` requires `x-instance-param`
5. **instance-param-only-with-map** — `x-instance-param` requires `x-upstream-map`
6. **upstream-excludes-instance-param** — single-`x-upstream` fragments cannot have `x-instance-param`
7. **vault-inject-paired** — `x-vault-path` and `x-inject-as` are both-or-neither
8. **passthrough-has-no-probe** — pass-through fragments cannot have `x-credential-probe`
9. **plaintext-required-on-http** — `http://` upstreams require `x-upstream-plaintext`
10. **plaintext-excludes-map** — `x-upstream-plaintext` is forbidden with `x-upstream-map`
11. **quota-requires-cost** — `x-quota` requires `x-cost-per-call` on same operation

### 3.2 Validator-Side Constraints (Go-Only)

These constraints are enforced by Go code in `internal/spec`:

1. **Cross-path validation** — `x-instance-param` must exist in every path
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

## Part 4: Data Types and Constraints Reference

### 4.1 Common Patterns

**Duration Format:**
- Pattern: `^[1-9][0-9]*[smhd]$`
- Examples: `30s`, `5m`, `2h`, `1d`
- Maximum: `168h` (1 week) — warned if exceeded

**URI/URL Patterns:**
- HTTPS: `^https://`
- HTTP: `^http://` (requires `x-upstream-plaintext: "acknowledged"`)
- IP literals rejected (must use hostname)

**Scope Pattern:**
- Format: `service:action` (e.g., `k8s-ro:get`, `argocd:read`)

**Owner Token Pattern:**
- Regex: `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
- One segment, no slashes, no dots

**API Version Pattern:**
- Regex: `^v[1-9][0-9]*$`
- Examples: `v1`, `v2`, `v10`

### 4.2 Enumerated Values

**Injection Kind:**
- `"header"` — inject as named header
- `"bearer"` — inject as `Authorization: Bearer` (name ignored)
- `"query"` — inject as query parameter

**Acknowledgment Values:**
- `"acknowledged"` — the only accepted value for opt-out fields

---

## Part 5: Example Fragments

### 5.1 Minimal Pass-Through Fragment

```json
{
  "x-seam-schema": "v1",
  "x-seam-owner": "echo",
  "x-upstream": "https://echo.example.svc.cluster.local",
  "paths": {
    "/echo": {
      "get": {
        "summary": "Echo endpoint",
        "responses": {
          "200": { "description": "Success" }
        }
      }
    }
  }
}
```

### 5.2 Single-Instance Credential-Injection Fragment

```json
{
  "x-seam-schema": "v1",
  "x-seam-owner": "argocd",
  "x-upstream": "https://argocd-ro-ardenone-manager-ts.ardenone.com:8444",
  "x-upstream-tls": { "caBundle": "argocd-ro.pem" },
  "x-upstream-strip-prefix": "/argocd",
  "x-vault-path": "seam/routes/argocd/ro-token",
  "x-inject-as": { "kind": "bearer" },
  "x-credential-probe": {
    "path": "/argocd/api/v1/applications",
    "method": "GET",
    "interval": "6h"
  },
  "paths": {
    "/argocd/api/v1/applications": {
      "get": {
        "x-required-scope": ["argocd:read"],
        "summary": "List ArgoCD applications (read-only)",
        "responses": {
          "200": { "description": "Application list" }
        }
      }
    }
  }
}
```

### 5.3 Multi-Instance Map Fragment

```json
{
  "x-seam-schema": "v1",
  "x-seam-owner": "k8s",
  "x-instance-param": "cluster",
  "x-fanout-scope": ["k8s-ro:get"],
  "x-upstream-map": {
    "ardenone-cluster": {
      "url": "https://traefik-ardenone-cluster:8001",
      "vaultPath": "seam/routes/k8s/ardenone-cluster-ro",
      "injectAs": { "kind": "bearer" },
      "requiredScope": ["k8s-ro:get"],
      "probeInterval": "24h"
    },
    "apexalgo-iad": {
      "url": "http://kubectl-proxy-apexalgo-iad:8001",
      "plaintext": "acknowledged",
      "vaultPath": "seam/routes/k8s/apexalgo-iad-admin",
      "injectAs": { "kind": "bearer" },
      "requiredScope": ["k8s-rw:admin"],
      "probeInterval": "12h"
    }
  },
  "paths": {
    "/k8s/{cluster}/api/v1/pods": {
      "get": {
        "x-required-scope": ["k8s-ro:get"],
        "summary": "List pods in cluster"
      },
      "delete": {
        "x-required-scope": ["k8s-rw:delete"],
        "summary": "Delete pod"
      }
    }
  }
}
```

---

## References

### OpenAPI 3.1 Specification
- **Official Spec:** https://spec.openapis.org/oas/v3.1.0
- **JSON Schema 2020-12:** https://json-schema.org/draft/2020-12/release-notes
- **Changes from 3.0:** https://swagger.io/blog/news/whats-new-in-openapi-3-1/

### SEAM Documentation
- **Route Fragment Schema:** `/home/coding/SEAM/docs/notes/route-fragment-schema.md`
- **OpenAPI Fragment Research:** `/home/coding/SEAM/docs/notes/openapi-fragment-research.md`
- **Plan:** `/home/coding/SEAM/docs/plan/plan.md`
- **Schema File:** `/home/coding/SEAM/docs/notes/route-fragment-schema.json`

### Implementation
- **Loader Code:** `/home/coding/SEAM/internal/spec/loader.go`
- **pb33f libopenapi:** https://pb33f.io/libopenapi/
- **pb33f libopenapi-validator:** https://github.com/pb33f/libopenapi-validator

---

**Document Status:** Complete ✅  
**Version:** 1.0  
**Last Updated:** 2026-08-07
