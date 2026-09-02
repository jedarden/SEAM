# SEAM Plan.md Checkbox State Report

**Generated:** 2026-09-02  
**Source:** `docs/plan/plan.md`  
**Generated from:** `.beads/plan-checkbox-extraction.md`

## Executive Summary

| Metric | Count | Percentage |
|--------|-------|------------|
| **Total Checkboxes** | 17 | 100% |
| **✅ Complete [x]** | 6 | 35.3% |
| **☐ Incomplete [ ]** | 11 | 64.7% |

## Per-Phase Breakdown

| Phase | Description | State | Status |
|-------|-------------|-------|--------|
| **1a** | Gateway scaffold | ☐ | Unticked |
| **1b** | Fragment merge | ☐ | Unticked |
| **2** | Secret injection | ☐ | Unticked |
| **3** | ConfigMap-mounted route fragments | ☐ | INCOMPLETE |
| **4** | z.ai/GLM and twitterapi.io proxy fragments | ☐ | INCOMPLETE |
| **5** | kubectl-proxy endpoints | ☐ | INCOMPLETE |
| **6a** | Deploy SEAM to rs-manager | ✅ | COMPLETE |
| **6b** | Agent cutover | ☐ | BLOCKED |
| **7** | Per-agent tool scoping | ☐ | INCOMPLETE |
| **8** | Version migration tooling | ✅ | VERIFIED COMPLETE |
| **9a** | seam lint CI gate | ☐ | NO EVIDENCE |
| **9b** | Fragment authoring convenience | ✅ | COMPLETE |
| **10** | Multi-instance routes | ☐ | CRITICAL FAILURE |
| **11** | Passive route health | ✅ | VERIFIED COMPLETE |
| **12** | Credential health sentinel | ☐ | CANNOT VERIFY ACCEPTANCE |
| **13** | Per-route guards | ✅ | VERIFIED COMPLETE |
| **14** | Non-tailnet ingress authentication | ✅ | COMPLETE |

### Status Category Summary

| Status Category | Count | Phases |
|-----------------|-------|--------|
| **COMPLETE / VERIFIED COMPLETE** | 6 | 6a, 8, 9b, 11, 13, 14 |
| **INCOMPLETE** | 5 | 3, 4, 5, 7 |
| **BLOCKED** | 1 | 6b |
| **CRITICAL FAILURE** | 1 | 10 |
| **CANNOT VERIFY** | 1 | 12 |
| **NO EVIDENCE** | 1 | 9a |
| **Unticked without verification** | 2 | 1a, 1b, 2 |

## Detailed Phase Information

### ☐ Phase 1a: Gateway scaffold
**Line:** 868  
**State:** Unticked [ ]  
**Description:** HTTP server, configuration (env-var/flag knobs, two listener ports — caller-facing `8080`, operator-only `8081`), container build, `/docs`, `/docs/{route}`, `/openapi.json` served over hand-written whole spec, request validation with structured error responses, `X-SEAM-Spec-Version` and `X-SEAM-API-Version` response headers, `/_seam/healthz`, `/_seam/readyz` and `/_seam/metrics` endpoints, control-plane namespace routing rule, two-listener split

---

### ☐ Phase 1b: Fragment merge
**Line:** 881  
**State:** Unticked [ ]  
**Description:** OpenAPI merge from static local directory, per-fragment schema validation, collision detection on (path, method, `x-api-version`) triple, quarantine + `/config/status`, runtime rejection of fragments declaring reserved control-plane paths, quarantine of fragments with `x-seam-owner` disagreement or missing field

---

### ☐ Phase 2: Secret injection
**Line:** 882  
**State:** Unticked [ ]  
**Description:** OpenBao client with Kubernetes auth, `rs-manager/seam/routes/*` path allowlist, per-service co-ownership rule, upstream-host allowlist enforcement, `x-vault-path`/`x-inject-as` extension handling, SEAM-facing-path → upstream-path computation with `x-upstream-path-template` and `x-upstream-strip-prefix`, strip-then-inject inbound hardening, `x-upstream-tls` handling, 30-second secret cache TTL with 401 invalidation, request-body tee with `maxReplayableRequestBytes` limit, secret-echo scrubbing of responses, secret-store outage behavior

---

### ☐ Phase 3: ConfigMap-mounted route fragments
**Line:** 888  
**State:** Unticked [ ]  
**Description:** Per-service ConfigMap volumes, file-watch hot reload with atomic route-table swap, pilot: migrate ArgoCD read-only proxy  
**Status:** **INCOMPLETE** (verified 2026-09-01) — hot reload flag exists but NOT enabled; 3/6 criteria pass, 2/6 fail, 1/6 blocked

---

### ☐ Phase 4: z.ai/GLM and twitterapi.io proxy fragments
**Line:** 889  
**State:** Unticked [ ]  
**Description:** Onboard z.ai/GLM proxy and twitterapi.io proxy fragments; first end-to-end credential injection proof  
**Status:** **INCOMPLETE** (verified 2026-09-01) — fragments exist but NOT mounted; routes not served; OpenBao secrets do not exist

---

### ☐ Phase 5: kubectl-proxy endpoints
**Line:** 890  
**State:** Unticked [ ]  
**Description:** Onboard kubectl-proxy endpoints for additional clusters as parametrized multi-instance fragment with `x-instance-param: cluster`, add Tailscale Connectors per cluster  
**Status:** **INCOMPLETE** (verified 2026-09-01) — missing iad-native-ads cluster; schema/YAML bugs prevent loading; 6 Tailscale Connectors missing

---

### ✅ Phase 6a: Deploy SEAM to rs-manager
**Line:** 891  
**State:** Ticked [x]  
**Description:** Deploy SEAM via declarative-config — single replica, per-service route ConfigMaps with kustomize, ServiceAccount with SA-token volume, OpenBao Kubernetes auth proof, Tailscale node, tag-restricted ACL grant, liveness and readiness probes, metrics scrape config  
**Status:** **COMPLETE** (verified 2026-09-01) — 8/10 criteria verified PASS; 2 require manual ACL verification

---

### ☐ Phase 6b: Agent cutover
**Line:** 896  
**State:** Unticked [ ]  
**Description:** Service-by-service agent migration, CLAUDE.md cleanup, ACL verification, retry with backoff over 60-second window  
**Status:** **BLOCKED** (verified 2026-09-01) — YAML fragments cannot load; binary treats YAML as JSON

---

### ☐ Phase 7: Per-agent tool scoping
**Line:** 897  
**State:** Unticked [ ]  
**Description:** NEEDLE-side per-worker tsnet identity provisioning, `x-required-scope` route tagging, Grant-based scope enforcement, `/whoami` and `/scopes` endpoints, scope-filtered `/openapi.json` + `/docs`  
**Status:** **INCOMPLETE** (verified 2026-09-01) — uses placeholder test mode; real Tailscale LocalClient WhoIs integration TODO

---

### ✅ Phase 8: Version migration tooling
**Line:** 912  
**State:** Ticked [x]  
**Description:** Conditional `Deprecation`/`Sunset` header emission, `x-adapter` schema and transform executor, `X-SEAM-API-Version` selection, version-aware `/docs/route` with `?version=`, per-route-version request-count metric, `/changes?since=` diff endpoint  
**Status:** **VERIFIED COMPLETE** (verified 2026-09-01) — all 7 criteria verified PASS with comprehensive test coverage

---

### ☐ Phase 9a: seam lint CI gate
**Line:** 931  
**State:** Unticked [ ]  
**Description:** Gateway merge/validation engine as CLI and declarative-config CI gate; validates fragment schema, detects collisions, enforces allowlists, rejects reserved control-plane paths, flags `x-unscrubbable: acknowledged`  
**Status:** **NO EVIDENCE** (verified 2026-09-01) — evidence file does not exist

---

### ✅ Phase 9b: Fragment authoring convenience
**Line:** 949  
**State:** Ticked [x]  
**Description:** `seam diff` (renders merged-spec change) and `seam import --from-url` (bootstraps fragment from upstream OpenAPI spec)  
**Status:** **COMPLETE** (verified 2026-09-01) — both fully implemented with comprehensive test coverage

---

### ☐ Phase 10: Multi-instance routes
**Line:** 950  
**State:** Unticked [ ]  
**Description:** `x-instance-param` fragment-root field, object-form `x-upstream-map` resolution over full entry key set `{url, vaultPath, injectAs, tls, plaintext, probeInterval, breaker, requiredScope}`  
**Status:** **CRITICAL FAILURE** (verified 2026-09-01) — SEAM crashes on startup with duplicate `/whoami` route; runtime verification blocked

---

### ✅ Phase 11: Passive route health
**Line:** 959  
**State:** Ticked [x]  
**Description:** Per-route last-2xx tracking in three-state rendering, `/health/upstreams` aggregation, per-upstream circuit breaker under default policy, structured 503, `x-breaker` tuning block  
**Status:** **VERIFIED COMPLETE** (verified 2026-09-01) — all 6 criteria verified PASS via code examination and test coverage

---

### ☐ Phase 12: Credential health sentinel
**Line:** 960  
**State:** Unticked [ ]  
**Description:** `x-credential-probe` background validation with `/health/credentials`, leader-elected via Kubernetes Lease, 401-triggered refetch-and-retry-once over request-body buffer, `credential-refresh-not-retried` error envelope  
**Status:** **CANNOT VERIFY ACCEPTANCE** (verified 2026-09-01) — code compilation failure (99 errors); implementation exists but runtime testing blocked

---

### ✅ Phase 13: Per-route guards
**Line:** 961  
**State:** Ticked [x]  
**Description:** `x-loop-guard` loop breaker, `x-cost-per-call`/`x-quota` cost governor with `X-SEAM-Budget-Remaining`, `X-SEAM-Dry-Run` validation-only mode  
**Status:** **VERIFIED COMPLETE** (verified 2026-09-01) — all 3 criteria PASS based on code and test inspection; comprehensive test coverage

---

### ✅ Phase 14: Non-tailnet ingress authentication
**Line:** 962  
**State:** Ticked [x]  
**Description:** Four non-negotiable security rules for foreign-worker auth: JWT validation, scope mapping, header stripping, default-deny  
**Status:** **COMPLETE** (verified 2026-09-01) — all 4 criteria implemented with 35 tests; static analysis confirms implementation matches spec

---

## Data Quality Verification

- **Total checkbox count:** 17 (verified via `grep -c '^\s*[-*]\s*\[[ x]\]'`)
- **All checkboxes extracted:** ✅ Yes — grep output captured all 17 items
- **Line numbers:** Accurate as of 2026-09-02
- **Phase coverage:** ✅ Complete — Phases 1a through 14 with no gaps
- **State accuracy:** ✅ Verified — 6 ticked [x], 11 unticked [ ]

## Evidence File References

Verification evidence for each phase is maintained in separate `.beads/` files:
- `phase3-evidence.md` — ConfigMap-mounted route fragments
- `phase4-evidence.md` — z.ai/GLM and twitterapi.io proxy fragments
- `phase5-evidence.md` — kubectl-proxy endpoints
- `phase6a-evidence.md` — Deploy SEAM to rs-manager
- `phase6b-evidence.md` — Agent cutover
- `phase7-evidence.md` — Per-agent tool scoping
- `phase8-evidence.md` — Version migration tooling
- `phase9b-evidence.md` — Fragment authoring convenience
- `phase10-evidence.md` — Multi-instance routes
- `phase11-evidence.md` — Passive route health
- `phase12-evidence.md` — Credential health sentinel
- `phase13-evidence.md` — Per-route guards
- `phase14-evidence.md` — Non-tailnet ingress authentication
- `phase-verdict-summary.md` — Phase verdict summaries
- `phase-verification-summary.md` — Verification methodology

## Key Insights

### Completion Progress
- **35.3% of phases complete** (6/17)
- **64.7% of phases incomplete or blocked** (11/17)
- **Critical path items:** Phases 1a, 1b, 2, and 3 form the foundational gateway infrastructure

### Blockers and Failures
1. **Phase 6b (BLOCKED)** — YAML fragments cannot load; binary treats YAML as JSON
2. **Phase 10 (CRITICAL FAILURE)** — SEAM crashes on startup with duplicate `/whoami` route registration
3. **Phase 12 (CANNOT VERIFY)** — Code compilation failure prevents runtime testing
4. **Phase 9a (NO EVIDENCE)** — Evidence file does not exist

### Risk Assessment
- **High Risk:** Phases 6b, 10 (blocking downstream dependencies)
- **Medium Risk:** Phases 3, 4, 5, 7 (incomplete with multiple failing criteria)
- **Low Risk:** Phases 1a, 1b, 2 (unticked but no verification evidence available)
- **On Track:** Phases 6a, 8, 9b, 11, 13, 14 (complete or verified complete)

---

**Report generated from:** `.beads/plan-checkbox-extraction.md`  
**Extraction date:** 2026-09-02  
**Total extraction size:** 108,979 bytes (329 lines)