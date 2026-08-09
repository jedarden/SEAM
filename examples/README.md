# SEAM Route Fragment Examples

**Purpose:** Realistic SEAM route fragment examples demonstrating all five SEAM extensions (x-upstream-map, x-credential-probe, x-loop-guard, x-cost-per-call, x-quota) in production-like scenarios.

**Validation:** All examples validate against `/home/coding/SEAM/spec/route-fragment-schema.json` (v1).

**Last validated:** 2026-08-07

## Examples

### 1. ArgoCD Read-Only Proxy (`argocd-read-only-proxy.json`)

**Use case:** Phase 3 migration target — read-only proxy to ArgoCD API over Tailscale.

**SEAM extensions demonstrated:**
- `x-upstream` — Single upstream URL
- `x-upstream-tls` — Custom TLS configuration with insecure skip verify
- `x-upstream-plaintext` — Plaintext acknowledgement (HTTP over Tailscale)
- `x-required-scope` — Operation-level authorization requirements

**Why this example matters:** This is an actual production route that will be migrated to SEAM in Phase 3. It demonstrates the pattern for Kubernetes control plane read-only proxies.

---

### 2. Simple Secret Injection (`simple-secret-injection.json`)

**Use case:** Generic pass-through proxy that injects API credentials from OpenBao.

**SEAM extensions demonstrated:**
- `x-vault-path` + `x-inject-as` — Secret injection pair
- `x-upstream` — Single upstream URL
- `x-required-scope` — Authorization scopes per HTTP method

**Why this example matters:** This is the most common pattern for external API integrations (e.g., twitterapi-proxy). It's the foundational building block for most SEAM routes.

---

### 3. Credential Probing (`credential-probing.json`)

**Use case:** Proactive credential validation by polling a safe upstream endpoint.

**SEAM extensions demonstrated:**
- `x-credential-probe` — Proactive credential health checks
- `x-breaker` — Circuit breaker tuning per upstream
- `x-vault-path` + `x-inject-as` — Secret injection
- `x-required-scope` — Authorization requirements

**Why this example matters:** Credential rotation is a fact of life in production. This extension detects expired or invalid credentials before clients encounter 401 errors, improving reliability and observability.

---

### 4. Cost and Quota Limits (`cost-quota-limits.json`)

**Use case:** Cost-aware rate limiting for credit-metered APIs.

**SEAM extensions demonstrated:**
- `x-cost-per-call` — Per-operation cost in credits
- `x-quota` — Per-caller quota limits with time windows
- `x-vault-path` + `x-inject-as` — Secret injection
- `x-required-scope` — Authorization scopes

**Why this example matters:** Many external APIs are credit-metered (e.g., twitterapi.io charges ~18 credits for `/twitter/user/info`, 100 credits for `check_follow_relationship`). This extension prevents runaway billing while fair-sharing capacity across callers.

**Pattern:** Different operations have different costs (cheap lookups vs expensive batch operations) and proportional quota limits.

---

### 5. Complex Multi-Extension (`complex-multi-extension.json`)

**Use case:** Multi-instance routing with all five SEAM extensions working together.

**SEAM extensions demonstrated:**
- `x-upstream-map` — Multi-instance routing by region
- `x-instance-param` — Instance selector parameter (`{region}`)
- `x-credential-probe` — Per-instance probe intervals
- `x-breaker` — Per-instance circuit breaker settings
- `x-loop-guard` — Loop detection and backoff
- `x-cost-per-call` — Per-operation cost tracking
- `x-quota` — Per-caller quota enforcement
- `x-required-scope` — Fragment-root and per-instance authorization

**Why this example matters:** This is the complete picture — all five extensions working together in a single fragment. It demonstrates how SEAM handles complex, production-grade multi-region APIs with different costs, rate limits, and health checks per upstream.

**Pattern:** Regional backends (`us-east`, `eu-west`, `ap-southeast`) with different credentials, costs, and failure modes, all unified under one fragment.

---

### 6. Rate Limiting and Monitoring (`rate-limiting-monitoring.json`)

**Use case:** Comprehensive rate limiting and monitoring across multiple regional backends with different costs and quotas.

**SEAM extensions demonstrated:**
- `x-seam-schema: v1` — Version marker (required for all SEAM fragments)
- `x-upstream-map` — Multi-instance routing by region parameter
- `x-instance-param` — Instance selector parameter (`{region}`)
- `x-loop-guard` — Per-operation loop protection (max_depth, max_redirects)
- `x-cost-per-call` — Per-operation cost in USD with 2 decimal precision
- `x-quota` — Per-operation quota limits with configurable windows and scopes
- `x-vault-path` + `x-inject-as` — Secret injection per upstream instance
- `x-credential-probe` — Per-instance credential health checks
- `x-breaker` — Per-instance circuit breaker configuration
- `x-required-scope` — Fragment-root and per-instance authorization

**Why this example matters:** This demonstrates the complete rate limiting and monitoring suite with realistic multi-region API routing. Each operation (`/{region}/query`, `/{region}/search`, `/{region}/batch`) has different costs, quotas, and loop guard settings to reflect real-world API pricing models.

**Pattern:**
- **Query endpoint**: Higher cost (0.002 USD), moderate quota (500/hour), standard loop guard (depth=10, redirects=5)
- **Search endpoint**: Lower cost (0.001 USD), higher quota (2000/hour), relaxed loop guard (depth=5, redirects=3)
- **Batch endpoint**: Highest cost (0.005 USD), lower quota (200/hour), strict loop guard (depth=3, redirects=2)

This shows how cost-aware rate limiting scales with operation complexity and resource consumption.

---

## Running Validation

All examples are validated against the schema:

```bash
cd /home/coding/SEAM
node examples/validate_examples.js
```

**Expected output:**
```
=== Validating SEAM Route Fragment Examples ===

🔍 Validating: argocd-read-only-proxy.json
✅ argocd-read-only-proxy.json - VALID

🔍 Validating: simple-secret-injection.json
✅ simple-secret-injection.json - VALID

🔍 Validating: credential-probing.json
✅ credential-probing.json - VALID

🔍 Validating: cost-quota-limits.json
✅ cost-quota-limits.json - VALID

🔍 Validating: complex-multi-extension.json
✅ complex-multi-extension.json - VALID

🔍 Validating: rate-limiting-monitoring.json
✅ rate-limiting-monitoring.json - VALID

=== Validation Summary ===
✅ Passed: 6/6
❌ Failed: 0/6

✅ All example fragments are valid!
```

---

## Schema Limitations Discovered

During validation of these examples, the following schema limitations were noted (all are documented in `/home/coding/SEAM/docs/notes/route-fragment-schema-limitations.md`):

1. **Cross-path validation cannot be expressed in JSON Schema** — e.g., verifying that `x-instance-param` appears in every path template requires validator-side rules.

2. **Per-entry vault/inject pairing is incomplete** — The `x-upstream-map` entry validation requires evaluating merged defaults (fragment-level vault + per-entry override), which cannot be expressed in pure JSON Schema.

3. **Manifest-level validation is external** — Checking that upstream URLs are in the allowlist requires validator-side access to ConfigMap `seam-upstream-allowlist`.

4. **Merge-time collision detection is runtime-only** — Detecting duplicate `(path, method, x-api-version)` keys across multiple fragments happens at merge time, not schema validation time.

None of these limitations are defects in the schema — they are inherent constraints of JSON Schema 2020-12 and are intentionally delegated to the Go validator (`internal/spec`) as documented in the schema's design principles.

---

## Integration with SEAM Phases

These examples demonstrate how the schema integrates across SEAM's implementation phases:

| Phase | Example | Integration Point |
|-------|---------|-------------------|
| **Phase 1b** (runtime quarantine) | All examples | Admission control when fragments are loaded |
| **Phase 3** (ArgoCD proxy) | `argocd-read-only-proxy.json` | Actual migration target |
| **Phase 5** (kubectl-proxies) | `complex-multi-extension.json` | Multi-instance fanout pattern |
| **Phase 7** (cost/quota) | `cost-quota-limits.json`, `complex-multi-extension.json` | Rate limiting enforcement |
| **Phase 9a** (seam lint) | All examples | CI validation before commit |

---

## Testing Future Schema Changes

When the schema evolves (e.g., `x-seam-schema: v2`), these examples serve as the regression corpus:

1. **Run validation:** `node examples/validate_examples.js`
2. **Check for breaking changes:** Did any previously-valid examples become invalid?
3. **Update examples:** If the change is intentional (e.g., new required field), update the examples.
4. **Document migration path:** Add a migration guide to the limitations document.

---

## Reference Documentation

- **Schema:** `/home/coding/SEAM/spec/route-fragment-schema.json`
- **Design authority:** `/home/coding/SEAM/docs/notes/route-fragment-schema.md`
- **Integration guide:** `/home/coding/SEAM/docs/notes/route-fragment-schema-integration.md`
- **Limitations:** `/home/coding/SEAM/docs/notes/route-fragment-schema-limitations.md`
- **Plan:** `/home/coding/SEAM/docs/plan/plan.md` (Phase 1 runtime quarantine, Phase 9a seam lint)

---

**Version:** 1.0  
**Created:** 2026-08-07  
**Last updated:** 2026-08-07
