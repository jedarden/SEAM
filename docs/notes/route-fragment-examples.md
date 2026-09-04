# Route Fragment Schema Test Cases

**Status:** Comprehensive validation test suite for `route-fragment-schema.json` (v1)  
**Created:** 2026-08-07  
**Purpose:** Validate schema correctness and provide examples for integration testing

## Overview

This document provides comprehensive test cases for the SEAM route fragment schema. Test fragments are organized into three categories:
- **Valid fragments:** Minimal, typical, and comprehensive examples that should pass validation
- **Invalid fragments:** Deliberately malformed fragments testing specific constraint violations
- **Edge cases:** Boundary conditions and unusual but legal constructs

All fragments are presented as YAML for readability but can be converted to JSON for validation.

---

## Part 1: Valid Fragments

### 1.1 Valid Minimal Fragment

The smallest legal fragment demonstrating only required fields.

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-upstream: https://api.example.com

paths:
  /api/v1/status:
    get:
      summary: Health check endpoint
      responses:
        "200":
          description: OK
```

**Why it's valid:**
- Contains all required fields (`x-seam-schema`, `x-seam-owner`, `paths`)
- `x-seam-schema` is `v1` (correct format)
- `x-seam-owner` is a legal token (`[a-z0-9]([a-z0-9-]*[a-z0-9])?`)
- `x-upstream` is HTTPS (no plaintext acknowledgment needed)
- At least one path with one operation

---

### 1.2 Valid Fragment with All Extensions

Comprehensive example demonstrating every SEAM extension field.

```yaml
x-seam-schema: v1
x-seam-owner: weather-service
x-api-version: v1
x-upstream: https://weather.internal:443
x-upstream-tls:
  caBundle: weather-internal-ca
  serverName: weather.internal
x-vault-path: rs-manager/rs-manager/seam/routes/weather-service/api-key
x-inject-as:
  kind: header
  name: X-API-Key
x-credential-probe:
  path: /health
  method: GET
  interval: 30s
x-breaker:
  threshold: 10
  openSeconds: 30
  maxOpenSeconds: 300
x-upstream-strip-prefix: /weather
x-required-scope: weather:read
x-fanout-scope: weather:admin
x-seam-deprecated:
  since: "2026-08-01"
  sunset: "2026-12-31"
x-loop-guard:
  maxRepeats: 5
  window: 10m
x-cost-per-call:
  amount: 0.001
  unit: credits
x-quota:
  amount: 1000
  unit: credits
  window: 1h
x-unscrubbable: acknowledged

paths:
  /weather/api/v1/forecast:
    get:
      summary: Get weather forecast
      operationId: getForecast
      x-required-scope: weather:forecast
      x-loop-guard:
        maxRepeats: 10
        window: 10m
      x-cost-per-call:
        amount: 0.005
        unit: credits
      x-quota:
        amount: 100
        unit: credits
        window: 1h
      parameters:
        - name: location
          in: query
          required: true
          schema:
            type: string
      responses:
        "200":
          description: Forecast data
          content:
            application/json:
              schema:
                type: object
        "401":
          description: Unauthorized
        "429":
          description: Rate limit exceeded
```

**Why it's valid:**
- All extension fields present with legal values
- `x-vault-path` and `x-inject-as` are paired (both or neither)
- `x-credential-probe` present because credentials are injected
- `x-upstream-tls` provides custom CA configuration
- `x-seam-deprecated` includes required `since` date and optional `sunset`
- `x-loop-guard` has both `maxRepeats` (minimum 1) and a duration `window`
- `x-cost-per-call.amount` is non-negative and carries the opaque unit `credits`
- `x-quota` requires `x-cost-per-call` (constraint satisfied)
- `x-quota.unit` byte-matches `x-cost-per-call.unit`
- Operation-level extensions override fragment-level defaults

---

### 1.3 Valid Multi-Instance Map Fragment

Demonstrates `x-upstream-map` with per-instance configuration.

```yaml
x-seam-schema: v1
x-seam-owner: k8s-fleet
x-instance-param: cluster
x-upstream-map:
  prod-us-east:
    url: https://k8s-prod-us-east.example.com
    vaultPath: rs-manager/rs-manager/seam/routes/k8s-fleet/prod-us-east
    injectAs:
      kind: bearer
    probeInterval: 60s
    requiredScope: k8s:prod
  prod-us-west:
    url: https://k8s-prod-us-west.example.com
    vaultPath: rs-manager/rs-manager/seam/routes/k8s-fleet/prod-us-west
    injectAs:
      kind: bearer
    probeInterval: 60s
    requiredScope: k8s:prod
  dev:
    url: https://k8s-dev.example.com
    vaultPath: rs-manager/rs-manager/seam/routes/k8s-fleet/dev
    injectAs:
      kind: bearer
    probeInterval: 120s
    tls:
      caBundle: dev-ca-bundle
      insecureSkipVerify: acknowledged
  _all:
    url: https://k8s-fanout-target.example.com
    requiredScope: k8s:admin
x-fanout-scope: k8s:ops

paths:
  /k8s/{cluster}/pods:
    get:
      summary: List pods in cluster
      responses:
        "200":
          description: Pod list
  /k8s/{cluster}/nodes:
    get:
      summary: List nodes in cluster
      responses:
        "200":
          description: Node list
  /k8s/{cluster}/namespaces:
    get:
      summary: List namespaces in cluster
      responses:
        "200":
          description: Namespace list
```

**Why it's valid:**
- `x-instance-param` is present and matches the segment name in all paths (`{cluster}`)
- All map entries have required `url` field
- Map entries with `vaultPath` also have `injectAs` (paired)
- Map entries can override `probeInterval`, `tls`, and `requiredScope`
- `_all` is a reserved fan-out key
- `x-fanout-scope` gates fan-out requests
- Multiple map entries (more than one) is legal

---

### 1.4 Valid Adapter Fragment

Demonstrates `x-adapter` for version migration.

```yaml
x-seam-schema: v1
x-seam-owner: legacy-api
x-api-version: v2
x-adapter:
  targetVersion: v1
  request:
    - transform:
        type: header-add
        name: X-API-Version
        value: "2"
    - transform:
        type: path-rewrite
        from: "/v2/api"
        to: "/v1/api"
  response:
    - transform:
        type: response-map
        statusCode: 200
        headers:
          X-Legacy-Deprecated: "true"

paths:
  /v2/api/users:
    get:
      summary: List users (v2 endpoint)
      responses:
        "200":
          description: User list
        "400":
          description: Bad request
```

**Why it's valid:**
- `x-adapter` is present with required `targetVersion`, `request`, and `response`
- `x-adapter` is mutually exclusive with upstream fields (no `x-upstream`, `x-upstream-map`, etc.)
- `targetVersion` references a live API version (`v1`)
- Transform arrays are permissive (exact vocabulary enforced by Phase 8)

---

### 1.5 Valid Fragment with Plaintext Upstream

Demonstrates `x-upstream-plaintext` acknowledgment.

```yaml
x-seam-schema: v1
x-seam-owner: monitoring
x-upstream: http://prometheus.monitoring.svc.cluster.local:9090
x-upstream-plaintext: acknowledged
x-credential-probe:
  path: /api/v1/query
  method: POST
  interval: 5m
x-upstream-strip-prefix: /prometheus

paths:
  /prometheus/api/v1/query:
    post:
      summary: Execute PromQL query
      responses:
        "200":
          description: Query result
```

**Why it's valid:**
- `x-upstream` uses `http://` (plaintext)
- `x-upstream-plaintext: acknowledged` is present (required for plaintext)
- `x-credential-probe` uses POST method (legal)
- No `x-vault-path` or `x-inject-as` (pass-through fragment)

---

## Part 2: Invalid Fragments

### 2.1 Invalid: Missing Required Field `x-seam-schema`

**Expected error:** Missing required property: `x-seam-schema`

```yaml
x-seam-owner: test-service
x-upstream: https://api.example.com

paths:
  /api/v1/status:
    get:
      summary: Health check
      responses:
        "200":
          description: OK
```

**What's wrong:**
- `x-seam-schema` is required but missing

---

### 2.2 Invalid: Unpaired Vault and Inject

**Expected error:** `x-vault-path` and `x-inject-as` must be both present or both absent

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-upstream: https://api.example.com
x-vault-path: rs-manager/rs-manager/seam/routes/test-service/secret
# Missing x-inject-as

paths:
  /api/v1/data:
    get:
      responses:
        "200":
          description: OK
```

**What's wrong:**
- `x-vault-path` is present without `x-inject-as` (must be paired)

---

### 2.3 Invalid: Map Without Instance Param

**Expected error:** `x-upstream-map` requires `x-instance-param`

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-upstream-map:
  instance1:
    url: https://api1.example.com
  instance2:
    url: https://api2.example.com
# Missing x-instance-param

paths:
  /api/{instance}/data:
    get:
      responses:
        "200":
          description: OK
```

**What's wrong:**
- `x-upstream-map` is present without `x-instance-param` (required)

---

### 2.4 Invalid: HTTP Without Plaintext Acknowledgment

**Expected error:** `http://` upstream requires `x-upstream-plaintext: acknowledged`

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-upstream: http://api.example.com
# Missing x-upstream-plaintext

paths:
  /api/v1/data:
    get:
      responses:
        "200":
          description: OK
```

**What's wrong:**
- `x-upstream` uses `http://` without `x-upstream-plaintext: acknowledged`

---

### 2.5 Invalid: Quota Without Cost Per Call

**Expected error:** `x-quota` requires `x-cost-per-call`

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-upstream: https://api.example.com

paths:
  /api/v1/data:
    get:
      x-quota:
        amount: 1000
        unit: credits
        window: 1h
      # Missing x-cost-per-call
      responses:
        "200":
          description: OK
```

**What's wrong:**
- `x-quota` is present without `x-cost-per-call` (quota can't be enforced without knowing per-call cost)

---

### 2.6 Invalid: Adapter with Upstream Fields

**Expected error:** `x-adapter` is mutually exclusive with upstream fields

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-api-version: v2
x-adapter:
  targetVersion: v1
  request: []
  response: []
x-upstream: https://api.example.com
# x-adapter and x-upstream are mutually exclusive

paths:
  /api/v2/data:
    get:
      responses:
        "200":
          description: OK
```

**What's wrong:**
- `x-adapter` and `x-upstream` are mutually exclusive

---

### 2.7 Invalid: Illegal Owner Token

**Expected error:** `x-seam-owner` must match pattern `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`

```yaml
x-seam-schema: v1
x-seam-owner: Test_Service
x-upstream: https://api.example.com

paths:
  /api/v1/data:
    get:
      responses:
        "200":
          description: OK
```

**What's wrong:**
- `x-seam-owner` contains uppercase letters and underscores (illegal)

---

### 2.8 Invalid: Negative Cost Per Call

**Expected error:** `x-cost-per-call` must be non-negative

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-upstream: https://api.example.com
x-cost-per-call:
  amount: -0.001
  unit: credits

paths:
  /api/v1/data:
    get:
      responses:
        "200":
          description: OK
```

**What's wrong:**
- `x-cost-per-call.amount` is negative (must be >= 0)

---

### 2.9 Invalid: Loop Guard Missing Required Fields

**Expected error:** `x-loop-guard` requires `maxRepeats` and `window`

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-upstream: https://api.example.com
x-loop-guard:
  maxRepeats: 5
# Missing window

paths:
  /api/v1/data:
    get:
      responses:
        "200":
          description: OK
```

**What's wrong:**
- `x-loop-guard` is missing required field `window`

---

### 2.10 Invalid: Pass-Through Fragment with Credential Probe

**Expected error:** `x-credential-probe` is forbidden without `x-vault-path`/`x-inject-as`

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-upstream: https://api.example.com
x-credential-probe:
  path: /health
  method: GET
  interval: 30s
# No x-vault-path or x-inject-as (pass-through)

paths:
  /api/v1/data:
    get:
      responses:
        "200":
          description: OK
```

**What's wrong:**
- `x-credential-probe` is present on a pass-through fragment (no credentials to probe)

---

## Part 3: Edge Cases

### 3.1 Edge Case: Exact Seven-Day Loop Window

`168h` is valid and does not trigger the warning reserved for longer windows.

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-upstream: https://api.example.com
x-loop-guard:
  maxRepeats: 5
  window: 168h

paths:
  /api/v1/data:
    get:
      responses:
        "200":
          description: OK
```

---

### 3.2 Edge Case: Zero Cost Per Call

Legal value for `x-cost-per-call` (minimum is 0).

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-upstream: https://api.example.com
x-cost-per-call:
  amount: 0
  unit: requests

paths:
  /api/v1/data:
    get:
      responses:
        "200":
          description: OK
```

---

### 3.3 Edge Case: Multiple Scope Syntax

Conjunctive scopes using array syntax.

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-upstream: https://api.example.com
x-required-scope:
  - read:general
  - write:specific

paths:
  /api/v1/data:
    get:
      responses:
        "200":
          description: OK
```

---

### 3.4 Edge Case: Large Duration Window

Legal but large duration window.

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-upstream: https://api.example.com
x-cost-per-call:
  amount: 1
  unit: requests
x-quota:
  amount: 10000
  unit: requests
  window: 168h

paths:
  /api/v1/data:
    get:
      responses:
        "200":
          description: OK
```

---

### 3.5 Edge Case: Empty Paths Object

Illegal: `paths` must have at least one property.

```yaml
x-seam-schema: v1
x-seam-owner: test-service
x-upstream: https://api.example.com

paths: {}
```

**Expected error:** `paths` must have minimum 1 property

---

## Part 4: Validation Test Suite

### 4.1 Running the Tests

Use the validation script to test all fragments:

```bash
node scripts/validate-fragments.js
```

**Expected output:**
```
✅ fragments.d/argocd/read-only-proxy.yaml: valid
✅ fragments.d/weather/forecast.yaml: valid
✅ fragments.d/k8s/fleet-proxies.yaml: valid
❌ fragments.d/test/missing-schema.yaml: invalid
   - Missing required property: x-seam-schema
❌ fragments.d/test/unpaired-vault.yaml: invalid
   - x-vault-path requires x-inject-as
...

Total: 12 fragments
Valid: 5
Invalid: 7
```

### 4.2 Test Categories

| Category | Count | Purpose |
|----------|-------|---------|
| Valid minimal | 1 | Verify minimal requirements |
| Valid comprehensive | 3 | Test all extension combinations |
| Invalid constraints | 7 | Test each constraint violation |
| Edge cases | 5 | Test boundary conditions |

---

## Part 5: Schema Evolution

### 5.1 Versioning Strategy

The schema uses `x-seam-schema` as a format version marker:
- **Current version:** `v1`
- **Future versions:** `v2`, `v3`, etc.

When `v2` is introduced:
- Old `v1` fragments remain valid (backward compatible)
- New fields are optional in `v1`, required in `v2`
- Validators support both versions during transition

### 5.2 Forward Compatibility

The schema uses `additionalProperties: true` at every level for forward compatibility:
- Unknown `x-*` keys are ignored by `v1` validators
- New fields can be added in `v2` without breaking `v1` validators
- `x-seam-schema` is the escape hatch for format changes

---

## Appendix: Test Fragment Files

Test fragments are organized in the following structure:

```
docs/notes/fragments/
├── valid/
│   ├── minimal.yaml
│   ├── all-extensions.yaml
│   ├── multi-instance-map.yaml
│   ├── adapter.yaml
│   └── plaintext-upstream.yaml
├── invalid/
│   ├── missing-schema.yaml
│   ├── unpaired-vault.yaml
│   ├── map-without-param.yaml
│   ├── http-no-plaintext.yaml
│   ├── quota-without-cost.yaml
│   ├── adapter-with-upstream.yaml
│   ├── illegal-owner.yaml
│   ├── negative-cost.yaml
│   ├── incomplete-loop-guard.yaml
│   └── passthrough-with-probe.yaml
└── edge-cases/
    ├── zero-redirects.yaml
    ├── zero-cost.yaml
    ├── multiple-scopes.yaml
    ├── large-window.yaml
    └── empty-paths.yaml
```

---

**Document version:** 1.0  
**Schema version:** v1  
**Last updated:** 2026-08-07  
**Next review:** When `v2` schema is proposed
