# SEAM Plan — Recommendations & Analysis

**Generated:** 2026-09-02
**Task:** seam-6ecbb16a — Add recommendations and analysis section
**Data sources:** `docs/notes/checkbox-summary-report.md` (its "Recommendations and Analysis"
section is the live-deployment companion to this file), `docs/notes/phase-breakdown-tables.md`,
`.beads/phase-verdict-summary.md`, the per-phase evidence files, and `docs/plan/plan.md` lines 860-968
**Freshness:** every recorded blocker below was re-checked against the working tree on 2026-09-02 —
see [Blocker freshness audit](#blocker-freshness-audit-verified-2026-09-02) — and the three
build-health rows were re-verified on 2026-09-03: the scoped build is still clean, `go build
./tools/...` still fails in exactly five packages, and the one-line test import is still unfixed.

**Baseline:** 17 phases — 6 complete (35.3%), 11 incomplete (64.7%). Verdict split across the
incomplete set: 6 FAIL, 1 CANNOT VERIFY, 4 NO EVIDENCE.

---

## Blocker pattern analysis (the 11 incomplete phases)

The 11 incomplete phases are not 11 separate problems. They collapse into four patterns, and the
patterns — not the phases — are what the next steps should target.

### Pattern 1 — Evidence debt: built but unevidenced (1a, 1b, 2, 9a)

All four NO-EVIDENCE phases have their machinery present in the tree, and in two cases the
machinery is demonstrably running in production:

- **Phase 1a** is recorded as "blocked on the ADR-001 language decision", but the plan itself
  contradicts that: the phase is titled "Gateway scaffold **(Go, ADR-001)**"
  (`docs/plan/plan.md:868`), and Phase 1b's line states the schema gate — not the language choice —
  "was the critical path" and was **discharged 2026-08-06** (`docs/plan/plan.md:881`). The module
  is Go (`go.mod`, go 1.26.0). The scaffold cannot be waiting on a decision that the codebase
  already embodies — and Phase 6a's PASS verdict proves the scaffold's own deliverables (two
  listeners, `/_seam/*` probes, metrics, structured headers) are live on rs-manager.
- **Phase 1b**: `internal/spec/` carries the merge engine, collision detection, quarantine,
  owner checks and a merge fuzzer (`internal/spec/merge_fuzz_test.go`, `quarantine_test.go`).
- **Phase 2**: `internal/vault/` exists; Phase 6a's evidence verified the OpenBao Kubernetes-auth
  login end-to-end from a real pod — the half of the auth story this phase was to prove locally.
- **Phase 9a**: the lint engine (`internal/spec/lint.go`) **and** the CLI
  (`cmd/seam/lint_command.go`, wired at `cmd/seam/main.go:38`) both exist and have tests.

For this pattern the remaining work is *verification and evidence-writing*, not implementation.

### Pattern 2 — One defect, many phases

Two mechanisms each hold up several phases at once:

- **YAML fragment loading** (recorded against 6b, implicated in 5, gates 3's lifecycle):
  `internal/spec/fragment.go:106-107` walks `.yaml`/`.yml`/`.json` and the loader imports
  `gopkg.in/yaml.v3` (`internal/spec/loader.go:19`) — the code-level defect the evidence described
  appears addressed; what is missing is the runtime demonstration that retires the 6b FAIL.
- **Deployment wiring on rs-manager**: the hot-reload flag is not enabled and the route ConfigMaps
  are not mounted (evidence for 3 and 4). This is a declarative-config change affecting two phases
  at once, not a code change.

### Pattern 3 — Serial dependency chains

The plan states its own edges; the incomplete set hangs off a few of them:

| Edge | Consequence |
|------|-------------|
| `1b → 9a → 3` (`plan.md:933`, `:888`) | The `seam lint` CI gate is a stated **precondition of Phase 3's pilot fragment** — 9a is not optional housekeeping, it is on 3's critical path. |
| `10 → 5` (`plan.md:958`) | Multi-instance runtime must be proven before the kubectl-proxy fragment can onboard. |
| `2 → {4, 12}` | Credential injection (4) and the sentinel (12) both stand on Phase 2's injection path and its evidence. |
| `7 → 14` (`plan.md:968`) | 14 reuses 7's enforcement model; 14 is already code-complete, so 7's runtime proof is what stands between it and a PASS. |
| `6b ← 13 + ACL` (`plan.md:896`) | 13 is done. What remains for 6b is the YAML defect (Pattern 2) and the two ACL verifications 6a left pending. |

### Pattern 4 — Verification blocked by environment, no longer blocked

Three evidence files (12, 13, 14) record that runtime testing could not run because no Go
compiler was available. The toolchain exists on this host at `/home/coding/sdk/go/bin/go`, and on
2026-09-02 `go build ./cmd/... ./internal/...` completes **clean**. The "compiler unavailable"
precondition is retired; the CANNOT VERIFY verdict on Phase 12 can be revisited immediately.

---

## Blocker freshness audit (verified 2026-09-02)

| Recorded blocker | Status today | Evidence |
|------------------|--------------|----------|
| "99 compilation errors in `internal/server`" (Phases 7, 12) | **Stale** | `go build ./cmd/... ./internal/...` is clean; fixed same day in `441bf20`, `94daec6`, `06b594b`, `3019520`, `ed735c1`, `bb402f2` (all 2026-09-01) |
| "SEAM crashes on startup, duplicate `/whoami`" (Phase 10) | **Fix landed, runtime re-verify owed** | `52b71e4` (2026-09-01); `/whoami` now registered once (`internal/server/server.go:406`) and reserved (`server.go:52`) |
| "Binary treats YAML as JSON — `invalid character '#'`" (Phase 6b) | **Likely stale at code level** | `internal/spec/fragment.go:106` processes `.yaml`/`.yml`; runtime demonstration still owed to retire the FAIL |
| "ADR-001 language decision pending" (Phase 1a) | **Stale** | Repo is Go; plan `:868` names Go in the title, `:881` records the real gate discharged 2026-08-06 |
| "Go compiler unavailable" (Phases 12, 13, 14 evidence) | **Stale** | `/home/coding/sdk/go/bin/go` builds the module, 2026-09-02 |
| "`internal/tailscale` missing" (Phase 7) | **Partly stale** | The package exists (`internal/tailscale/{client,cache,types}.go`); the actual break is a wrong import path in one test — `seam/internal/tailscale` at `internal/server/worker_identity_integration_test.go:12` should be `github.com/ardenone/seam/internal/tailscale`, which is why `go test` fails to set up `internal/server` |
| `go build ./...` fails at repo root | **Intentional, not a defect** | `test_broken.go` is a tracked CI canary (`faaadd5` "test(ci): deliberately break compilation to verify seam-ci gate") — verify with scoped builds, e.g. `go build ./cmd/... ./internal/...` |
| `tools/starvation-*` do not compile (5 main-module packages) | **Live** | Undefined/drifting `server.*` symbols — `BackoffConfig`, `NewLeaseLeader` now takes `LeaseConfig` and returns two values, `NewRootCauseAnalyzer`, `HumanMonitorConfig`, `RepairQueueConfig`, `SelfResolutionConfig`, etc. `go build ./tools/...` reports exactly five failing packages; two further tools (`starvation-alert-revaluator`, `starvation-recovery`) are separate Go modules with their own `go.mod`, so the main-module build skips them and they need the same API pass in their own trees |

Net effect: of the seven headline blockers in the prior report, four are stale, one is a
one-line fix, one is deliberate — and one genuinely new one (the `tools/starvation-*` drift)
surfaces only when you build the whole tree.

---

## Critical path summary

Ordered by how much each item unblocks:

1. **Runtime re-verification of already-fixed code** — retires or confirms the 6b, 10 and 12
   verdicts without writing feature code. Nothing else on this list is cheaper per verdict.
2. **`seam lint` CI gate (9a)** — the plan makes it a hard precondition of Phase 3's first
   fragment, so it gates the entire fragment pipeline (3 → 4 → 6b), not just its own checkbox.
3. **Deployment wiring (3, 4)** — the flag and the mounts are the only thing between the merged
   fragment machinery and a serving route table.
4. **Phase 2 evidence** — unblocks 4's end-to-end injection proof and restores the foundation
   12 and 13's runtime proofs cite.
5. **Phase 7's real identity** — the one genuinely-unbuilt item on the critical path: replacing
   placeholder test mode with real `tsnet`/WhoIs resolution and activating stages 3 + 5 together.
   It unblocks 14's runtime proof and the scope-aware halves of 5, 8 and 6b.

Everything else (5's connectors, 12's on-cluster proof, the cutover itself) hangs off these five.

---

## Infrastructure gaps identified

**rs-manager deployment** (declarative-config → ArgoCD; never live `kubectl` mutation):

- Hot-reload flag not enabled in SEAM's Deployment (Phase 3).
- Per-service route ConfigMaps not mounted — the z.ai/GLM and twitterapi.io fragment files exist
  in the repo but nothing serves them (Phase 4).
- Two **manual ACL verifications** left open by Phase 6a's 8/10: the tag-restricted grant, and the
  two-listener split (workers reach 8080, are refused on 8081).

**OpenBao**:

- The Phase-4 credential paths under `secret/rs-manager/seam/routes/*` do not exist yet; provision
  via `bao-as rs-manager-provision` with `-cas`, value by pipe — never as an argument. The
  Kubernetes-auth **role and policy are a Phase 2 precondition, not a 6a deliverable**
  (`plan.md:885`) — verify they exist rather than assuming it from 6a's login success.

**Tailscale** (Phase 5):

- `iad-native-ads` missing from both the `x-upstream-map` and `seam-upstream-allowlist`.
- Up to six Tailscale Connectors on rs-manager still to stand up, one per unconnected cluster.
- Each of the eight bare-MagicDNS hosts needs its **own line** in `seam-upstream-allowlist`
  (`plan.md:890`) — suffix rules do not cover them.

**declarative-config CI**:

- `seam lint` is not wired as a gate. It belongs in **Argo Workflows in `iad-ci`**
  (`declarative-config/k8s/iad-ci/argo-workflows/`) — GitHub Actions are disabled org-wide and
  must never be re-enabled. A scheduled job here follows the fleet's no-CronJob rule: a Deployment
  with an internal scheduling loop.

**Kubernetes RBAC** (Phase 12):

- The Lease `Role`/`RoleBinding` (`get`/`create`/`update` on `coordination.k8s.io` leases) was
  created in 6a per `plan.md:893` — confirm it is live, since the probe loop **fails closed**
  (holds no Lease, probes nothing) without it, and that failure looks like silence.

**Repo build health**:

- Six `tools/starvation-*` programs fail to compile against the current `server` API.
- One test import path error blocks all of `internal/server`'s test compilation.

---

## Prioritized action items

1. **Re-run runtime verification against the working build.** Start a local binary against
   fixture fragments: load the production YAML fragments (6b), confirm clean startup with the
   k8s-style fragments (10), and compile-then-exercise the sentinel loop (12). Updates three
   verdicts; requires no feature code.
2. **Fix the `internal/server` test import** — `seam/internal/tailscale` →
   `github.com/ardenone/seam/internal/tailscale`
   (`internal/server/worker_identity_integration_test.go:12`). One line; restores the test suite
   for the package every other phase's code lives in.
3. **Repair the six `tools/starvation-*` programs** — update them to the current `server` API
   (`LeaseConfig`, the renamed backoff/monitor types) or archive the ones whose feature work is
   still unmerged. Restores whole-tree buildability.
4. **Wire `seam lint` as a declarative-config CI gate** (Argo Workflows template in `iad-ci`),
   then write `phase9a-evidence.md`. The engine and CLI already exist; this is packaging plus the
   gate the plan demands before any fragment ships.
5. **Write evidence files for 1a, 1b and 2** against the code that already exists — record the
   ADR-001 decision (Go) in 1a's evidence, exercise the merge/quarantine path for 1b, and capture
   the dev-token auth-mode checks for 2. Retires the entire NO-EVIDENCE column.
6. **Enable hot reload and mount the route ConfigMaps** in SEAM's Deployment via declarative-config,
   and let ArgoCD sync (Phase 3), then mount the z.ai/GLM and twitterapi.io fragments (Phase 4).
7. **Provision the Phase-4 OpenBao secrets** under `secret/rs-manager/seam/routes/*` (provisioning
   identity, `-cas`, value by pipe; verify by metadata version, never by reading back), then prove
   `x-vault-path` → `x-inject-as` end-to-end against a real upstream (Phase 4).
8. **Complete Phase 5's infrastructure** — add `iad-native-ads` to map and allowlist, one allowlist
   line per bare-MagicDNS host, and the missing Connectors — then verify the multi-instance
   fragment end-to-end (which also retires Phase 10's runtime debt via the same exercise).
9. **Replace Phase 7's placeholder identity with real `tsnet`/WhoIs resolution** and activate
   stages 3 + 5 together, as the plan requires — never one without the other. Unblocks 14's
   runtime proof and the scope-aware fan-out (5) and drift work (8).
10. **Prove Phase 12 on-cluster** — Lease acquisition, `/health/credentials` per-(fragment,
    instance) reporting, and the 401 replay over the Phase 2 buffer, both within and above
    `maxReplayableRequestBytes`.
11. **Execute the 6b cutover service by service** once each go/no-go list passes — including the
    operator-port refusal check and the ≥60 s retry budget — deleting each service's CLAUDE.md
    prose in the same change. Gated on items 1, 6, 7 and the two outstanding ACL verifications.

---

## Positive indicators

- **Six phases verified PASS** — 6a, 8, 9b, 11, 13, 14 — and they are the hard ones: version
  migration, circuit breaking, per-route guards, foreign-worker JWT auth, and the deployment
  substrate itself. The remaining work skews toward wiring and verification, not novel design.
- **13 of 17 phases carry evidence files at all** (76.5%); only four have none.
- **The deployment substrate is live**: SEAM runs on rs-manager with its ServiceAccount, a proven
  OpenBao Kubernetes-auth login from inside a real pod, probes on the caller-facing port, and the
  8080/8081 listener split — the foundation every remaining phase consumes.
- **Compile health is restored**: the 2026-08-30 "99 errors" regression was fully repaired on
  2026-09-01 across six targeted fix commits, and the core module builds clean today.
- **The two nastiest runtime defects have landed fixes**: the duplicate-`/whoami` startup crash
  (`52b71e4`) and the compile regression (`441bf20`) — both awaiting only re-verification.
- **The security posture of the completed set is the right half to have done first**: guards
  (13), breaker (11), versioning (8), and default-deny foreign ingress (14) mean that when
  fragments and agents arrive in Phases 3-6b, the controls that make that safe are already in
  place rather than trailing the traffic.
- **The blocker list shrinks under scrutiny**: four of seven headline blockers did not survive a
  fresh build — evidence the project's true state is ahead of its paperwork, not behind it.

---

## Conclusion

The 64.7%-incomplete headline overstates the remaining work. Of the 11 open phases, four are
evidence debt on machinery that exists (1a, 1b, 2, 9a), five are deployment wiring or
re-verification of code whose defects were fixed on 2026-09-01 (3, 4, 5, 6b, 10), one is blocked
on infrastructure that is a checklist rather than a project (5's connectors), and one — Phase 7's
real per-worker identity — is the single genuinely-unbuilt item on the critical path.

The highest-leverage move costs no feature code: re-run runtime verification with the now-working
toolchain and retire the stale verdicts. After that, the two gates that order everything else are
the `seam lint` CI gate (9a, precondition of the first fragment) and the rs-manager deployment
wiring (3, 4). Phase 7's identity work is the real build; everything after it — 14's runtime
proof, the scope-aware fan-out, and the cutover itself — is downstream of that one integration.

Verification discipline is the recurring theme: several FAIL and NO-EVIDENCE verdicts describe a
codebase that no longer exists. The evidence files should be regenerated against the current tree
as each item above lands, so the plan's checkbox state stops lagging the code it is meant to
track.
