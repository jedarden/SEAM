# Plan.md Checkbox State Extraction

**Extracted:** 2026-09-02  
**Source:** `/home/coding/SEAM/docs/plan/plan.md`  
**Total checkboxes:** 17

## Summary Statistics

| State | Count | Percentage |
|-------|-------|------------|
| ✅ Ticked [x] | 6 | 35.3% |
| ☐ Unticked [ ] | 11 | 64.7% |

## Detailed Checkbox List

### Phase 1a - Gateway scaffold
**Line:** 868  
**State:** ☐ Unticked [ ]  
**Phase:** Phase 1a  
**Description:** Gateway scaffold (Go, ADR-001) — HTTP server, configuration, container build, plus `/docs`, `/docs/{route}`, and `/openapi.json` served over a hand-written whole spec

---

### Phase 1b - Fragment merge
**Line:** 881  
**State:** ☐ Unticked [ ]  
**Phase:** Phase 1b  
**Description:** Fragment merge — OpenAPI merge from a static local directory, per-fragment schema validation, collision detection, quarantine + `/config/status`

---

### Phase 2 - Secret injection
**Line:** 882  
**State:** ☐ Unticked [ ]  
**Phase:** Phase 2  
**Description:** Secret injection — OpenBao client with Kubernetes auth, upstream-host allowlist, `x-vault-path`/`x-inject-as` handling, upstream path computation, TLS handling

---

### Phase 3 - ConfigMap-mounted route fragments
**Line:** 888  
**State:** ☐ Unticked [ ]  
**Phase:** Phase 3  
**Description:** ConfigMap-mounted route fragments with hot reload and atomic swap. Pilot: migrate ArgoCD read-only proxy  
**Status:** INCOMPLETE (verified 2026-09-01) - hot reload flag exists but NOT enabled; 3/6 criteria pass, 2/6 fail, 1/6 blocked

---

### Phase 4 - z.ai/GLM and twitterapi.io proxy fragments
**Line:** 889  
**State:** ☐ Unticked [ ]  
**Phase:** Phase 4  
**Description:** Onboard z.ai/GLM proxy and twitterapi.io proxy fragments. First end-to-end credential injection proof  
**Status:** INCOMPLETE (verified 2026-09-01) - fragments exist but NOT mounted; routes not served; OpenBao secrets do not exist

---

### Phase 5 - kubectl-proxy endpoints
**Line:** 890  
**State:** ☐ Unticked [ ]  
**Phase:** Phase 5  
**Description:** Onboard kubectl-proxy endpoints for additional clusters as parametrized multi-instance fragment  
**Status:** INCOMPLETE (verified 2026-09-01) - missing iad-native-ads cluster; schema/YAML bugs prevent loading; 6 Tailscale Connectors missing

---

### Phase 6a - Deploy SEAM to rs-manager
**Line:** 891  
**State:** ✅ Ticked [x]  
**Phase:** Phase 6a  
**Description:** Deploy SEAM to rs-manager via declarative-config — single replica, ConfigMaps, ServiceAccount, OpenBao auth, probes, metrics  
**Status:** COMPLETE (verified 2026-09-01) - 8/10 criteria verified PASS; 2 require manual ACL verification

---

### Phase 6b - Agent cutover
**Line:** 896  
**State:** ☐ Unticked [ ]  
**Phase:** Phase 6b  
**Description:** Agent cutover — service-by-service migration, CLAUED.md cleanup, ACL verification, retry with backoff  
**Status:** BLOCKED (verified 2026-09-01) - YAML fragments cannot load; binary treats YAML as JSON

---

### Phase 7 - Per-agent tool scoping
**Line:** 897  
**State:** ☐ Unticked [ ]  
**Phase:** Phase 7  
**Description:** Per-agent tool scoping — NEEDLE-side tsnet identity, scope enforcement, `/whoami`, `/scopes` endpoints  
**Status:** INCOMPLETE (verified 2026-09-01) - uses placeholder test mode; real Tailscale LocalClient WhoIs integration TODO

---

### Phase 8 - Version migration tooling
**Line:** 912  
**State:** ✅ Ticked [x]  
**Phase:** Phase 8  
**Description:** Version migration tooling — Deprecation/Sunset headers, x-adapter schema, version-aware docs, request-count metrics, `/changes` endpoint  
**Status:** VERIFIED COMPLETE (verified 2026-09-01) - all 7 criteria verified PASS with comprehensive test coverage

---

### Phase 9a - seam lint CI gate
**Line:** 931  
**State:** ☐ Unticked [ ]  
**Phase:** Phase 9a  
**Description:** `seam lint` — gateway merge/validation engine as CLI and declarative-config CI gate  
**Status:** NO EVIDENCE (verified 2026-09-01) - evidence file does not exist

---

### Phase 9b - Fragment authoring convenience
**Line:** 949  
**State:** ✅ Ticked [x]  
**Phase:** Phase 9b  
**Description:** Fragment authoring convenience — `seam diff` and `seam import --from-url`  
**Status:** COMPLETE (verified 2026-09-01) - both fully implemented with comprehensive test coverage

---

### Phase 10 - Multi-instance routes
**Line:** 950  
**State:** ☐ Unticked [ ]  
**Phase:** Phase 10  
**Description:** Multi-instance routes — `x-instance-param`, object-form `x-upstream-map` resolution  
**Status:** CRITICAL FAILURE (verified 2026-09-01) - SEAM crashes on startup with duplicate `/whoami` route; runtime verification blocked

---

### Phase 11 - Passive route health
**Line:** 959  
**State:** ✅ Ticked [x]  
**Phase:** Phase 11  
**Description:** Passive route health — per-route last-2xx tracking, three-state rendering, circuit breaker, structured 503  
**Status:** VERIFIED COMPLETE (verified 2026-09-01) - all 6 criteria verified PASS via code examination and test coverage

---

### Phase 12 - Credential health sentinel
**Line:** 960  
**State:** ☐ Unticked [ ]  
**Phase:** Phase 12  
**Description:** Credential health sentinel — `x-credential-probe` background validation, leader-elected Lease, 401-triggered refetch  
**Status:** CANNOT VERIFY ACCEPTANCE (verified 2026-09-01) - code compilation failure (99 errors); implementation exists but runtime testing blocked

---

### Phase 13 - Per-route guards
**Line:** 961  
**State:** ✅ Ticked [x]  
**Phase:** Phase 13  
**Description:** Per-route guards — `x-loop-guard` loop breaker, `x-cost-per-call`/`x-quota` cost governor, dry-run mode  
**Status:** VERIFIED COMPLETE (verified 2026-09-01) - all 3 criteria PASS based on code and test inspection

---

### Phase 14 - Non-tailnet ingress authentication
**Line:** 962  
**State:** ✅ Ticked [x]  
**Phase:** Phase 14  
**Description:** Non-tailnet (foreign-worker) ingress authentication — four security rules implemented verbatim  
**Status:** COMPLETE (verified 2026-09-01) - all 4 criteria implemented with 35 tests; static analysis confirms implementation matches spec

---

## Verification Notes

- **Total checkbox count:** 17 (verified via `grep -c '^\s*[-*]\s*\[[ x]\]'`)
- **All checkboxes extracted:** Yes - grep output captured all 17 items
- **Line numbers:** Accurate as of 2026-09-02
- **Phase coverage:** Phases 1a through 14 (no gaps in sequence)
- **States:** 6 ticked [x], 11 unticked [ ]

## Evidence File References

Several phases include inline verification references to evidence files in `.beads/`:
- `phase3-evidence.md`
- `phase4-evidence.md`
- `phase5-evidence.md`
- `phase6a-evidence.md`
- `phase6b-evidence.md`
- `phase7-evidence.md`
- `phase8-evidence.md`
- `phase9b-evidence.md`
- `phase10-evidence.md`
- `phase11-evidence.md`
- `phase12-evidence.md`
- `phase13-evidence.md`
- `phase14-evidence.md`
- `phase-verdict-summary.md`

These evidence files contain detailed verification results from 2026-09-01 assessments.
