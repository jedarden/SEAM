# Phase 5 Completion Criteria Verification Evidence

**Date**: 2026-09-01
**Bead**: seam-9d099431
**Binary**: `/home/coding/SEAM/seam`
**Verification Method**: Direct binary testing against plan.md criteria

## Phase 5 Completion Criteria

From `docs/plan/plan.md`, Phase 5 requires:
> Onboard kubectl-proxy endpoints for additional clusters as **one parametrized multi-instance fragment** (requires Phase 10) carrying **`x-instance-param: cluster`** as its designated instance selector, adding a Tailscale Connector per cluster on rs-manager as needed. **Each of the eight bare-MagicDNS `x-upstream-map` hosts must be added to `seam-upstream-allowlist` as its own line**

### Required Nine Clusters (per plan)
> ardenone-cluster, ardenone-manager, apexalgo-iad, iad-options, iad-kalshi, **iad-native-ads**, iad-ci, ord-devimprint, and rs-manager itself — **nine**.

## Verification Results

### ❌ CRITICAL FAIL: Missing Cluster in Upstream Map

**Criterion**: Nine-cluster kubectl-proxy map with all required clusters  
**Status**: **FAIL**  
**Evidence**:
```bash
$ grep -c "iad-native-ads" declarative-config/k8s/rs-manager/seam/routes/k8s/k8s-api-proxy.yaml
0
```

**Details**: The upstream map in `k8s-api-proxy.yaml` contains only 8 of 9 required clusters:
- ✅ ardenone-cluster
- ✅ ardenone-manager  
- ✅ apexalgo-iad
- ✅ iad-options
- ✅ iad-kalshi
- ❌ **iad-native-ads (MISSING)**
- ✅ iad-ci
- ✅ ord-devimprint
- ✅ rs-manager (observer)
- ✅ rs-manager-admin (admin endpoint)

**Impact**: Phase 5 is incomplete. The missing `iad-native-ads` cluster means agents cannot reach this cluster through SEAM.

### ❌ CRITICAL FAIL: Fragment Schema Validation Error

**Criterion**: Fragment must validate against route-fragment-schema.json  
**Status**: **FAIL**  
**Evidence**:
```bash
$ ./seam lint --fragments-dir declarative-config/k8s/rs-manager/seam/routes/ --schema-path spec/route-fragment-schema.json
ERROR [fragment.schema] declarative-config/k8s/rs-manager/seam/routes/k8s/k8s-api-proxy.yaml: fragment does not satisfy route-fragment-schema.json: jsonschema validation failed with 'file:///home/coding/SEAM/seam-lint-schema.json#'
- at '': 'allOf' failed
  - at '': 'not' failed
```

**Root Cause**: The fragment violates `constraint-passthrough-has-no-probe` in the schema. The constraint states:
> IF (NOT (has x-vault-path OR has x-inject-as)) THEN (NOT (has x-credential-probe))

**The Problem**: The k8s-api-proxy.yaml fragment has:
- ❌ No fragment-level `x-vault-path`
- ❌ No fragment-level `x-inject-as`  
- ✅ Has `x-credential-probe`
- ✅ Has `x-upstream-map` with per-instance credentials

This combination violates the constraint, which assumes any fragment without fragment-level vault credentials is a "pass-through" route and cannot have a credential probe.

**Why This Is Wrong**: The constraint doesn't account for x-upstream-map fragments where credentials are provided per-instance (in each upstream map entry), not at the fragment level. The per-instance entries have both `vaultPath` and `injectAs`, so the fragment does have credentials - they're just structured differently.

**Impact**: Even with the missing cluster fixed, the fragment will not load due to this schema validation bug. The constraint needs to be updated to recognize x-upstream-map fragments as having credentials via per-instance configuration.

**Schema Analysis**:
```json
// From spec/route-fragment-schema.json
{
  "if": {
    "not": {
      "anyOf": [
        { "required": ["x-vault-path"] },
        { "required": ["x-inject-as"] }
      ]
    }
  },
  "then": {
    "not": {
      "required": ["x-credential-probe"]
    }
  }
}
```

This constraint is logically: "If fragment has neither x-vault-path nor x-inject-as, then it cannot have x-credential-probe."

The k8s-api-proxy.yaml violates this because:
1. It has no fragment-level x-vault-path
2. It has no fragment-level x-inject-as  
3. But it DOES have x-credential-probe
4. And it has x-upstream-map with per-instance credentials

**The constraint should be**: "If fragment has neither fragment-level NOR per-instance credentials, then it cannot have x-credential-probe."

### ❌ FAIL: Missing Tailscale Connectors

**Criterion**: Adding a Tailscale Connector per cluster on rs-manager as needed  
**Status**: **FAIL**  
**Evidence**:
```bash
$ find /home/coding/SEAM/declarative-config/k8s -name "*connector*" -o -name "*tailscale*"
# No output - no connector configuration found
```

**Details**: Phase 5 requires Tailscale Connectors for the 6 clusters not already reachable from rs-manager. According to plan.md:
> Connector work in Phase 5 is therefore the six not already reachable (apexalgo-iad, iad-options, iad-kalshi, iad-native-ads, iad-ci, ord-devimprint)

**Current rs-manager connectivity**: Only 3 clusters (ardenone-cluster, ardenone-hub, ardenone-manager)

**Required Connectors for Phase 5**:
1. ❌ apexalgo-iad - NO CONNECTOR
2. ❌ iad-options - NO CONNECTOR  
3. ❌ iad-kalshi - NO CONNECTOR
4. ❌ iad-native-ads - NO CONNECTOR (also missing from fragment)
5. ❌ iad-ci - NO CONNECTOR
6. ❌ ord-devimprint - NO CONNECTOR

**Impact**: Even if the fragment loaded and had all 9 clusters, SEAM could not reach 6 of them because rs-manager lacks Tailscale egress to those clusters. Phase 5 cannot function without this infrastructure.

### ❌ FAIL: Allowlist Missing Host Entry

**Criterion**: Each bare-MagicDNS host must be in `seam-upstream-allowlist`  
**Status**: **FAIL**  
**Evidence**:
```bash
$ grep "iad-native-ads" declarative-config/k8s/rs-manager/seam/configmap-allowlist.yaml
# No output - cluster missing from allowlist
```

**Details**: The allowlist has 8 of 9 required bare-MagicDNS hostnames. Missing `traefik-iad-native-ads:8001` (if following the naming pattern).

**Current allowlist entries**:
- ✅ traefik-ardenone-cluster:8001
- ✅ traefik-apexalgo-iad:8001  
- ✅ traefik-ardenone-manager:8001
- ✅ traefik-rs-manager:8001
- ✅ traefik-iad-ci:8001
- ✅ kubectl-proxy-iad-kalshi:8001
- ✅ traefik-ord-devimprint:8001
- ✅ traefik-iad-options:8001
- ❌ **traefik-iad-native-ads:8001 (MISSING)**

### ✅ PASS: Multi-Instance Fragment Structure

**Criterion**: One parametrized multi-instance fragment with `x-instance-param: cluster`  
**Status**: **PASS**  
**Evidence**:
```yaml
# From declarative-config/k8s/rs-manager/seam/routes/k8s/k8s-api-proxy.yaml
x-seam-schema: v1
x-seam-owner: k8s
x-api-version: v1
x-instance-param: cluster    # ✅ Correctly set
```

**Details**: The fragment correctly declares `x-instance-param: cluster` as required by Phase 5.

### ✅ PASS: Per-Instance Scope Separation

**Criterion**: Admin-credentialed instances carry distinct per-instance `requiredScope` from observer instances  
**Status**: **PASS**  
**Evidence**:
```yaml
# Observer instances (k8s-ro:get):
ardenone-cluster:
  requiredScope: k8s-ro:get
  
apexalgo-iad:
  requiredScope: k8s-ro:get

# Admin instance (k8s-rw:get):
rs-manager-admin:
  requiredScope: k8s-rw:get    # ✅ Distinct scope
```

**Details**: The fragment correctly separates observer scope (`k8s-ro:get`) from admin scope (`k8s-rw:get`) as required.

### ✅ PASS: Bare MagicDNS Hostnames

**Criterion**: Upstream hosts use bare MagicDNS names  
**Status**: **PASS**  
**Evidence**: All configured clusters use the correct bare MagicDNS format:
- `http://traefik-ardenone-cluster:8001`
- `http://traefik-apexalgo-iad:8001`
- `http://kubectl-proxy-iad-kalshi:8001`
- etc.

**Details**: Hostnames match the fleet convention per CLAUDE.md.

### ❌ CRITICAL FAIL: Binary Cannot Load Any Fragments (YAML Parsing Bug)

**Criterion**: Nine-cluster map served through SEAM with per-instance breakers  
**Status**: **FAIL**  
**Evidence**:
```bash
$ timeout 10s ./seam serve --fragment-mode --fragments-dir declarative-config/k8s/rs-manager/seam/routes/ --caller-port 18080 --operator-port 18081 2>&1 | grep -A 2 "failed to load fragment"
[Fragment] Warning: failed to load fragment declarative-config/k8s/rs-manager/seam/routes/argocd-ro/argocd-read-only-proxy.yaml: failed to parse JSON: invalid character '#' looking for beginning of value
[Fragment] Warning: failed to load fragment declarative-config/k8s/rs-manager/seam/routes/k8s/k8s-api-proxy.yaml: failed to parse JSON: invalid character '#' looking for beginning of value
[Fragment] Warning: failed to load fragment declarative-config/k8s/rs-manager/seam/routes/twitterapi/twitterapi-proxy.yaml: failed to parse JSON: invalid character '#' looking for beginning of value
[Fragment] Warning: failed to load fragment declarative-config/k8s/rs-manager/seam/routes/zai/zai-glm-proxy.yaml: failed to parse JSON: invalid character '#' looking for beginning of value
[Fragment] Fragment loading complete: 0 loaded, 4 errors
```

**Root Cause**: The SEAM binary is attempting to parse YAML files as JSON. All fragments fail with "invalid character '#' looking for beginning of value" - the '#' is a YAML comment character, not JSON syntax.

**Impact**: **Runtime functionality cannot be verified at all**. The binary fails to load ANY fragments (Phase 4 zai/GLM, twitterapi.io, Phase 5 k8s, or argocd-ro), so:
- Cannot test nine-cluster map resolution
- Cannot test per-instance credential injection
- Cannot test per-instance breakers
- Cannot test multi-instance routing

**Additional Binary Bug**: Server crashes with duplicate route registration:
```
panic: pattern "/whoami" (registered at /home/coding/SEAM/internal/server/server.go:407) conflicts with pattern "/whoami" (registered at /home/coding/SEAM/internal/server/server.go:398)
```

This is a separate code bug preventing the server from starting even if fragments could load.

## Summary

**Pass**: 2/8 criteria  
**Fail**: 6/8 criteria  
**Critical Blockers**: 4 (missing cluster, schema validation, YAML parsing bug, missing Tailscale Connectors)

### Assessment

Phase 5 was marked complete (umbrella bead `seam-4ca576db` closed 2026-08-27/28) but **never actually verified against a working binary**. The compilation errors that started 2026-08-30 (99 accumulated errors) prevented any runtime testing, and multiple critical gaps were never discovered:

1. **Missing cluster** (`iad-native-ads`) from both upstream map and allowlist
2. **Schema validation bug** preventing fragment loading even after YAML parsing is fixed
3. **YAML parsing bug** preventing ANY fragments from loading in the binary
4. **Missing Tailscale Connectors** for 6 of 9 required clusters
5. **Binary crash bug** with duplicate route registration

The nine-cluster requirement was explicitly stated in plan.md but never checked against the actual implementation.

### Failed Criteria Requiring New Beads

1. **seam-missing-iad-native-ads** - Add `iad-native-ads` cluster to upstream map and allowlist
2. **seam-schema-constraint-fix** - Fix constraint-passthrough-has-no-probe to account for x-upstream-map fragments
3. **seam-yaml-parsing-bug** - Fix YAML fragment loading (binary treats YAML as JSON)
4. **seam-tailscale-connectors** - Add 6 missing Tailscale Connectors to rs-manager
5. **seam-duplicate-route-bug** - Fix duplicate /whoami route registration crash
6. **seam-phase5-reverification** - Re-verify Phase 5 after all fixes applied (this bead)

### Infrastructure Blockers

Phase 5 cannot complete until:
- rs-manager has Tailscale egress to all 6 non-local clusters
- SEAM binary can load and validate YAML fragments
- Schema constraint permits x-upstream-map with per-instance credentials
- All 9 clusters are present in upstream map and allowlist

### Why This Wasn't Caught Earlier

From plan.md acceptance criteria:
> Functionality. The acceptance suite above passes; **both Phase 4 pilot upstreams** (z.ai/GLM and twitterapi.io) and the **nine-cluster Phase 5 kubectl-proxy map** are served through SEAM

The verification that never happened:
1. **No runtime testing** - Compilation errors prevented binary from running
2. **No fragment counting** - 8 vs 9 clusters was never noticed
3. **No infrastructure verification** - Tailscale Connectors never checked
4. **No schema validation** - constraint logic never exercised
5. **No acceptance suite** - No automated tests to catch these gaps

### Root Cause Analysis

Phase 5 was marked complete (seam-4ca576db closed 2026-08-27/28) but:
- Never verified against the actual binary (compilation errors 2026-08-30)
- Missing cluster (`iad-native-ads`) never caught
- Schema validation error prevented fragment from loading

The nine-cluster requirement was explicitly stated in plan.md but not checked during Phase 5 acceptance.

## Commands Used

```bash
# Check for missing cluster
grep -c "iad-native-ads" declarative-config/k8s/rs-manager/seam/routes/k8s/k8s-api-proxy.yaml

# Verify fragment schema
./seam lint --fragments-dir declarative-config/k8s/rs-manager/seam/routes/ --schema-path spec/route-fragment-schema.json

# Check allowlist entries
grep "traefik-" declarative-config/k8s/rs-manager/seam/configmap-allowlist.yaml | wc -l

# Extract upstream hosts
grep -E "traefik-|kubectl-proxy-" declarative-config/k8s/rs-manager/seam/routes/k8s/k8s-api-proxy.yaml | grep -oE 'http://[a-z0-9:-]+' | sort -u
```