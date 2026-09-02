# SEAM Plan Phases — Per-Phase Breakdown Tables

**Generated:** 2026-09-02 (bead `seam-64ae2dd4`)
**Reconciled:** 2026-09-02 against `.beads/phase-verdict-summary.md` (bead `seam-5f0b2f9e`)
**Source:** `docs/plan/plan.md` implementation-status checkboxes (lines 868–962)
**Blocker data:** `.beads/phase-verdict-summary.md` and the per-phase evidence files
**Total:** 17 phases — 6 complete, 11 incomplete

Phase names follow `plan.md`; where a plan title runs into a parenthetical
sentence (Phases 4, 5, 14), it is trimmed at the natural name boundary.
Descriptions are condensed from each checkbox line's own text — the plan lines
run several hundred words each, so these carry the first clause plus the
distinctive requirements rather than the whole line.

---

## Complete Phases (6)

| Phase | Line | Verdict | Description |
|-------|-----:|---------|-------------|
| **Phase 6a** — Deploy SEAM to rs-manager via declarative-config | 891 | ✅ PASS | Single replica per Version Migration Strategy §1; per-service route `configMap` volumes with `kustomization.yaml` (configMapGenerator, `disableNameSuffixHash`); ServiceAccount with OpenBao authentication; Tailscale node + ACL grant for the pod tag; liveness/readiness probes and metrics; the two-listener split (8080 caller-facing, 8081 operator-only). |
| **Phase 8** — Version migration tooling | 912 | ✅ PASS | Conditional `Deprecation`/`Sunset` header emission for `x-seam-deprecated` fragments; `x-adapter` schema and transform executor; `X-SEAM-API-Version` request-header selection (defaults to oldest supported); version-aware `/docs/route`; the per-route-version request-count metric exported at `/_seam/metrics` (mirrors Kubernetes' `apiserver_requested_deprecated_apis`); `/changes` diff endpoint with ring buffer. |
| **Phase 9b** — Fragment authoring convenience | 949 | ✅ PASS | `seam diff` (renders the effective merged-spec change a PR would cause) and `seam import --from-url` (bootstraps a fragment from an upstream's published OpenAPI spec). |
| **Phase 11** — Passive route health | 959 | ✅ PASS | Per-route last-2xx tracking surfaced in `/docs` with three-state rendering (no attempt since last restart / no success in N attempts since last restart, carrying the last error / last succeeded, with `source: probe` labeling sentinel traffic); per-upstream circuit breaker under the default policy (failures are transport faults and 5xx only — no 4xx, no 401); structured `503` naming the upstream with `openedAt`, `lastError` and a derived `Retry-After`; `x-breaker` fragment configuration. |
| **Phase 13** — Per-route guards | 961 | ✅ PASS | `x-loop-guard` loop breaker with request-hash canonicalization; `x-cost-per-call`/`x-quota` cost governor with `X-SEAM-Budget-Remaining` response header; `X-SEAM-Dry-Run: true` validation-only mode. |
| **Phase 14** — Non-tailnet (foreign-worker) ingress authentication | 962 | ✅ PASS | Cloudflare Access JWT validation at the gateway; service-token→scopes mapping keyed on the verified token's subject; `X-SEAM-Scopes` header stripping on ingress; default-deny unless explicitly enabled. |

All six carry a ✅ PASS verdict in `.beads/phase-verdict-summary.md`
(6a: 8/10 criteria with 2 pending manual ACL verification; the rest fully
verified against code and tests).

---

## Incomplete Phases (11)

Status is the checkbox state in `plan.md` (`[ ]` on every row) combined with
the verdict from `.beads/phase-verdict-summary.md` (NO EVIDENCE / FAIL / PASS),
with that file's own headline qualifier in parentheses where it adds one;
blocker type classifies what is holding the phase.

| Phase | Line | Status | Blocker Type | Blocker Detail |
|-------|-----:|--------|--------------|----------------|
| **Phase 1a** — Gateway scaffold (Go, ADR-001) | 868 | `[ ]` — NO EVIDENCE | Upstream decision + missing evidence | No evidence file; ADR-001 language decision still pending, so the scaffold cannot start. |
| **Phase 1b** — Fragment merge | 881 | `[ ]` — NO EVIDENCE | Missing evidence | No evidence file; merge/validation engine unverified. |
| **Phase 2** — Secret injection | 882 | `[ ]` — NO EVIDENCE | Missing evidence | No evidence file; OpenBao k8s-auth injection path unverified. |
| **Phase 3** — ConfigMap-mounted route fragments | 888 | `[ ]` — FAIL | Deployment config | Hot-reload flag exists but is not enabled in `deployment.yaml`, blocking the whole fragment lifecycle. |
| **Phase 4** — Onboard z.ai/GLM and twitterapi.io proxy fragments | 889 | `[ ]` — FAIL | Deployment gap | Fragment YAMLs authored but not mounted in the deployment; OpenBao secrets absent; routes not served. |
| **Phase 5** — Onboard kubectl-proxy endpoints (multi-instance) | 890 | `[ ]` — FAIL | Missing infrastructure + parser bug | `iad-native-ads` cluster absent, 6 Tailscale Connectors missing, plus schema-validation and YAML-parsing bugs in fragment loading. |
| **Phase 6b** — Agent cutover | 896 | `[ ]` — FAIL (BLOCKED) | YAML loading bug | Gateway binary accepts JSON only; production fragments die with `invalid character '#'`. |
| **Phase 7** — Per-agent tool scoping | 897 | `[ ]` — FAIL (INCOMPLETE) | Compilation errors + missing integration | Compilation failures, placeholder test-mode identity instead of real Tailscale `tsnet` integration, no live testing. |
| **Phase 9a** — `seam lint` CI gate | 931 | `[ ]` — NO EVIDENCE | Missing evidence | No evidence file; cannot verify the CLI/declarative-config gate wiring. |
| **Phase 10** — Multi-instance routes | 950 | `[ ]` — FAIL (CRITICAL FAILURE) | Startup crash | SEAM crashes on startup: duplicate `/whoami` route registration. |
| **Phase 12** — Credential health sentinel | 960 | `[ ]` — FAIL (cannot verify) | Compilation errors | 99 compilation errors in `internal/server` prevent build and runtime verification. |

### Blocker types at a glance

| Blocker type | Phases | Count |
|--------------|---------|------:|
| Missing evidence | 1a, 1b, 2, 9a | 4 |
| Deployment config / deployment gap | 3, 4 | 2 |
| Missing infrastructure + parser bug | 5 | 1 |
| YAML loading bug | 6b | 1 |
| Compilation errors / missing integration | 7, 12 | 2 |
| Startup crash | 10 | 1 |

Cross-cutting: the YAML-loading bug (3, 5, 6b) and the `internal/server`
compilation errors (7, 12) each block more than one phase; the verdict summary
lists both as the top remediation targets.

---

## Verification

Counts and line numbers re-checked 2026-09-02 against the working tree with
`grep -nE '^\s*- \[[ x]\]' docs/plan/plan.md`:

- Complete `[x]` (6): lines 891, 912, 949, 959, 961, 962
- Incomplete `[ ]` (11): lines 868, 881, 882, 888, 889, 890, 896, 897, 931, 950, 960

6 + 11 = 17 — every phase checkbox in the file appears in exactly one table.
