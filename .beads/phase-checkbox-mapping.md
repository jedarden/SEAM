# SEAM Phase Verdict to Plan.md Checkbox Mapping

**Generated:** 2026-09-01  
**Purpose:** Map each phase's verification verdict to its corresponding checkbox in `docs/plan/plan.md`

---

## Overview

This document provides a structural mapping between:
1. **Phase number and title** (from plan.md)
2. **Checkbox location** (line number in plan.md)
3. **Current checkbox state** (`[ ]` unchecked vs `[x]` checked)
4. **Evidence verdict** (from `.beads/phase*-evidence.md` files)
5. **Alignment status** (whether checkbox state matches evidence verdict)

---

## Phase-by-Phase Mapping

### Phase 1a: Gateway scaffold
- **Plan.md line:** 868
- **Checkbox state:** `[ ]` (unchecked)
- **Evidence file:** ❌ NOT FOUND
- **Verdict:** ⚠️ **NO EVIDENCE**
- **Alignment:** ⚠️ No evidence to verify completion
- **Should checkbox be ticked?** Cannot determine without evidence

---

### Phase 1b: Fragment merge
- **Plan.md line:** 881
- **Checkbox state:** `[ ]` (unchecked)
- **Evidence file:** ❌ NOT FOUND
- **Verdict:** ⚠️ **NO EVIDENCE**
- **Alignment:** ⚠️ No evidence to verify completion
- **Should checkbox be ticked?** Cannot determine without evidence

---

### Phase 2: Secret injection
- **Plan.md line:** 882
- **Checkbox state:** `[ ]` (unchecked)
- **Evidence file:** ❌ NOT FOUND
- **Verdict:** ⚠️ **NO EVIDENCE**
- **Alignment:** ⚠️ No evidence to verify completion
- **Should checkbox be ticked?** Cannot determine without evidence

---

### Phase 3: ConfigMap-mounted route fragments
- **Plan.md line:** 888
- **Checkbox state:** `[ ]` (unchecked)
- **Evidence file:** `.beads/phase3-evidence.md`
- **Verdict:** ❌ **FAIL** (3/6 pass, 2/6 fail, 1/6 blocked)
- **Alignment:** ✅ CORRECT (unchecked matches failing verdict)
- **Summary:** Hot reload flag exists but NOT enabled in deployment.yaml; fragment lifecycle cannot be verified without hot reload
- **Should checkbox be ticked?** ❌ NO - phase is incomplete

---

### Phase 4: Credential Injection (z.ai/GLM and twitterapi.io)
- **Plan.md line:** 889
- **Checkbox state:** `[ ]` (unchecked)
- **Evidence file:** `.beads/phase4-evidence.md`
- **Verdict:** ❌ **FAIL** (1/7 pass, 6/7 fail/cannot-test)
- **Alignment:** ✅ CORRECT (unchecked matches failing verdict)
- **Summary:** Fragment YAML files exist in declarative-config, but deployment incomplete (fragments not mounted in SEAM pod), routes not served, OpenBao secrets missing. Credential injection end-to-end cannot be demonstrated.
- **Blockers:**
  - ConfigMap volumes for Phase 4 fragments not added to SEAM deployment
  - OpenBao secrets `seam/routes/twitterapi/api-key` and `seam/routes/zai/api-key` do not exist (403 on metadata read)
  - No `/zai/*` or `/twitterapi/*` routes available in `/openapi.json`
  - Cost governor and credential sentinel cannot be tested without routes
- **Should checkbox be ticked?** ❌ NO - phase is incomplete

---

### Phase 5: Multi-Cluster kubectl-proxy Map
- **Plan.md line:** 890
- **Checkbox state:** `[ ]` (unchecked)
- **Evidence file:** `.beads/phase5-evidence.md`
- **Verdict:** ❌ **FAIL** (2/8 pass, 6/8 fail, 4 critical blockers)
- **Alignment:** ✅ CORRECT (unchecked matches failing verdict)
- **Blockers:**
  - Missing cluster `iad-native-ads` from upstream map (8/9 clusters present)
  - Schema validation error - fragment fails `constraint-passthrough-has-no-probe`
  - YAML parsing bug - binary treats YAML as JSON, preventing ALL fragments from loading
  - Missing Tailscale Connectors for 6 of 9 required clusters
  - Allowlist missing `iad-native-ads` host entry
  - Binary crash bug with duplicate `/whoami` route registration
- **Should checkbox be ticked?** ❌ NO - phase is incomplete

---

### Phase 6a: Deployment and Infrastructure
- **Plan.md line:** 891
- **Checkbox state:** `[x]` (checked)
- **Evidence file:** `.beads/phase6a-evidence.md`
- **Verdict:** ✅ **SUBSTANTIALLY COMPLETE** (8/10 pass, 2/10 pending manual verification)
- **Alignment:** ✅ CORRECT (checked matches substantially complete verdict)
- **Passing criteria:** Single replica deployment, ConfigMaps with proper kustomization.yaml, ServiceAccount with OpenBao auth, Tailscale node, liveness/readiness probes, metrics scrape configuration, listener ports and base URL configured
- **Pending manual verification:** Tag-restricted ACL grant in Tailscale policy, two-listener split ACL grant
- **Should checkbox be ticked?** ✅ YES - phase is substantially complete

---

### Phase 6b: Service Cutover
- **Plan.md line:** 896
- **Checkbox state:** `[ ]` (unchecked)
- **Evidence file:** `.beads/phase6b-evidence.md`
- **Verdict:** ❌ **BLOCKED** (cannot complete due to fragment loading issues)
- **Alignment:** ✅ CORRECT (unchecked matches blocked verdict)
- **Critical blocker:** Fragment loader only supports JSON format, but production fragments are YAML; affects ArgoCD read-only proxy, Kubernetes API proxy, and test service
- **Should checkbox be ticked?** ❌ NO - phase is blocked

---

### Phase 7: Identity Resolution (Tailscale + Cloudflare Access)
- **Plan.md line:** 897
- **Checkbox state:** `[ ]` (unchecked)
- **Evidence file:** ❌ NOT FOUND
- **Verdict:** ⚠️ **NO EVIDENCE**
- **Alignment:** ⚠️ No evidence to verify completion
- **Should checkbox be ticked?** Cannot determine without evidence

---

### Phase 8: API Versioning and Deprecation
- **Plan.md line:** 912
- **Checkbox state:** `[x]` (checked)
- **Evidence file:** `.beads/phase8-evidence.md`
- **Verdict:** ✅ **PASSING** (all 7 criteria verified)
- **Alignment:** ✅ CORRECT (checked matches passing verdict)
- **Passing criteria:** Conditional deprecation/sunset header emission, x-adapter schema and transform vocabulary, X-SEAM-API-Version selection, version-aware `/docs/route`, per-route-version request-count metric, `/changes` diff endpoint with ring buffer, retirement evaluator tool
- **Should checkbox be ticked?** ✅ YES - phase is complete

---

### Phase 9a: `seam lint` CI Gate
- **Plan.md line:** 931
- **Checkbox state:** `[ ]` (unchecked)
- **Evidence file:** ❌ NOT FOUND
- **Verdict:** ⚠️ **NO EVIDENCE**
- **Alignment:** ⚠️ No evidence to verify completion
- **Should checkbox be ticked?** Cannot determine without evidence

---

### Phase 9b: Diff and Import Commands
- **Plan.md line:** 949
- **Checkbox state:** `[x]` (checked)
- **Evidence file:** `.beads/phase9b-evidence.md`
- **Verdict:** ✅ **COMPLETE** (both commands fully implemented)
- **Alignment:** ✅ CORRECT (checked matches complete verdict)
- **Passing criteria:** `seam diff` renders effective merged-spec changes, `seam import --from-url` produces curatable fragments from OpenAPI specs
- **Should checkbox be ticked?** ✅ YES - phase is complete

---

### Phase 10: Multi-Instance Routes (x-instance-param, x-upstream-map)
- **Plan.md line:** 950
- **Checkbox state:** `[ ]` (unchecked)
- **Evidence file:** `.beads/phase10-evidence.md`
- **Verdict:** ❌ **CRITICAL FAILURE** (4/7 pass code-level, 3/7 blocked runtime)
- **Alignment:** ✅ CORRECT (unchecked matches critical failure verdict)
- **Summary:** Code-level implementation verified (x-instance-param, x-upstream-map resolution, status code derivation, envelope size rule), but runtime testing blocked by duplicate `/whoami` route registration causing SEAM to crash on startup
- **Blockers:**
  - SEAM crashes on startup with duplicate `/whoami` route registration (panic)
  - Cannot verify path rewriting, `_all` fan-out envelope, or lint warnings without running binary
  - Fix applied in code but binary not rebuilt (Go toolchain unavailable)
- **Should checkbox be ticked?** ❌ NO - phase has critical failure (runtime blocked)

---

### Phase 11: Passive Route Health
- **Plan.md line:** 959
- **Checkbox state:** `[x]` (checked)
- **Evidence file:** `.beads/phase11-evidence.md`
- **Verdict:** ✅ **PASS** (all 6 criteria verified)
- **Alignment:** ✅ CORRECT (checked matches passing verdict)
- **Passing criteria:** Three-state rendering in `/docs`, `/health/upstreams` three-state rendering and aggregation, per-upstream circuit breaker under default policy, structured 503 responses, `x-breaker` fragment configuration
- **Should checkbox be ticked?** ✅ YES - phase is complete

---

### Phase 12: Credential Health Sentinel
- **Plan.md line:** 960
- **Checkbox state:** `[ ]` (unchecked)
- **Evidence file:** `.beads/phase12-evidence.md`
- **Verdict:** ❌ **CANNOT VERIFY** (99 compile errors, runtime requirements)
- **Alignment:** ✅ CORRECT (unchecked matches unverifiable verdict)
- **Blockers:** 99 compile errors accumulated since 2026-08-30 prevent code verification; runtime verification requires Kubernetes, OpenBao, configured fragments, running SEAM instance
- **Should checkbox be ticked?** ❌ NO - phase cannot be verified

---

### Phase 13: Per-Route Guards
- **Plan.md line:** 961
- **Checkbox state:** `[x]` (checked)
- **Evidence file:** `.beads/phase13-evidence.md`
- **Verdict:** ✅ **PASS** (all 3 criteria verified)
- **Alignment:** ✅ CORRECT (checked matches passing verdict)
- **Passing criteria:** Loop breaker (`x-loop-guard`) with 429 responses, Cost governor (`x-cost-per-call`/`x-quota`) with 402 responses and budget tracking, Dry-run mode (`X-SEAM-Dry-Run`)
- **Should checkbox be ticked?** ✅ YES - phase is complete

---

### Phase 14: Non-Tailnet (Foreign-Worker) Ingress Authentication
- **Plan.md line:** 962
- **Checkbox state:** `[x]` (checked)
- **Evidence file:** `.beads/phase14-evidence.md`
- **Verdict:** ✅ **PASS** (all 4 rules implemented)
- **Alignment:** ✅ CORRECT (checked matches passing verdict)
- **Passing criteria:** Cloudflare Access JWT validation at gateway, Service-token→scopes mapping keyed on verified JWT subject, X-SEAM-Scopes header stripping, Default-deny on mode (403 before route matching)
- **Test coverage:** 35 tests total (26 in cloudflare_jwt_middleware_test.go + 9 in cloudflare_header_stripping_test.go)
- **Should checkbox be ticked?** ✅ YES - phase is complete

---

## Summary Statistics

### Checkbox Alignment

| Alignment Status | Count | Phases |
|------------------|-------|--------|
| ✅ CORRECT | 9 | Phase 3, 5, 6a, 6b, 8, 9b, 10, 11, 13, 14 |
| ⚠️ NO EVIDENCE | 4 | Phase 1a, 1b, 2, 4, 7, 9a |

### Evidence-Based Recommendations

| Phase | Current Checkbox | Should Be Checked? | Reason |
|-------|------------------|-------------------|--------|
| 1a | `[ ]` | ⚠️ UNKNOWN | No evidence file |
| 1b | `[ ]` | ⚠️ UNKNOWN | No evidence file |
| 2 | `[ ]` | ⚠️ UNKNOWN | No evidence file |
| 3 | `[ ]` | ❌ NO | FAIL - hot reload not enabled |
| 4 | `[ ]` | ⚠️ UNKNOWN | No evidence file |
| 5 | `[ ]` | ❌ NO | FAIL - multiple critical blockers |
| 6a | `[x]` | ✅ YES | SUBSTANTIALLY COMPLETE |
| 6b | `[ ]` | ❌ NO | BLOCKED - YAML fragments cannot load |
| 7 | `[ ]` | ⚠️ UNKNOWN | No evidence file |
| 8 | `[x]` | ✅ YES | PASSING |
| 9a | `[ ]` | ⚠️ UNKNOWN | No evidence file |
| 9b | `[x]` | ✅ YES | COMPLETE |
| 10 | `[ ]` | ❌ NO | CRITICAL FAILURE - SEAM crashes on startup |
| 11 | `[x]` | ✅ YES | PASS |
| 12 | `[ ]` | ❌ NO | CANNOT VERIFY - compilation errors |
| 13 | `[x]` | ✅ YES | PASS |
| 14 | `[x]` | ✅ YES | PASS |


---

## Critical Path Analysis

### Phases Currently Blocking Overall Completion

1. **Phase 3 (FAIL)** - Hot reload not enabled in deployment.yaml
   - **Impact:** Core Phase 3 functionality cannot work
   - **Fix:** Enable `-enable-hot-reload` flag in deployment.yaml

2. **Phase 5 (FAIL)** - Multiple critical blockers
   - **Impact:** Nine-cluster map functionality incomplete
   - **Fixes needed:**
     - Add missing cluster `iad-native-ads` to upstream map and allowlist
     - Fix YAML parsing bug (binary treats YAML as JSON)
     - Fix schema validation for constraint-passthrough-has-no-probe
     - Add 6 missing Tailscale Connectors
     - Fix binary crash on duplicate `/whoami` route registration

3. **Phase 6b (BLOCKED)** - YAML fragment loading failure
   - **Impact:** Cannot proceed with service-by-service cutover
   - **Fix:** Fragment loader must support YAML format (currently JSON-only)

4. **Phase 12 (CANNOT VERIFY)** - Compilation errors
   - **Impact:** Credential health sentinel cannot be verified
   - **Fix:** Resolve 99 compile errors accumulated since 2026-08-30

---

## Evidence File Gaps

The following phases have **no evidence files** and cannot be verified:

- **Phase 1a:** Gateway scaffold
- **Phase 1b:** Fragment merge
- **Phase 2:** Secret injection
- **Phase 4:** Credential Injection (z.ai/GLM and twitterapi.io)
- **Phase 7:** Identity Resolution (Tailscale + Cloudflare Access)
- **Phase 9a:** `seam lint` CI Gate

**Recommendation:** Create evidence files for these phases to enable verification.

---

## Next Steps

1. **High Priority Fixes:**
   - Fix YAML fragment loading to unblock Phase 6b
   - Enable hot reload in deployment.yaml to complete Phase 3
   - Add missing cluster `iad-native-ads` to Phase 5 upstream map and allowlist
   - Resolve compilation errors to enable Phase 12 verification

2. **Evidence Collection:**
   - Create evidence files for missing phases (1a, 1b, 2, 4, 7, 9a)

3. **Manual Verification:**
   - Verify Phase 6a ACL policies (requires Tailscale admin access)

---

**Generation Notes:**
- Evidence files analyzed: 11 files (Phase 10 evidence file now included)
- Phases with evidence: 3, 5, 6a, 6b, 8, 9b, 10, 11, 12, 13, 14
- Phases without evidence: 1a, 1b, 2, 4, 7, 9a
- Generated by: Bead seam-4aa9a212
- Updated: 2026-09-01 (Phase 10 evidence file discovered and integrated)
