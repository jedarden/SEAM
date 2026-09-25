# SEAM Migration Runbook — Legacy Proxy → SEAM Cutover

**Version:** 1.0
**Last Updated:** 2026-08-15
**Purpose:** The per-service playbook for cutting a legacy proxy (the ArgoCD
read-only proxy, the z.ai/GLM and twitterapi.io proxies, the kubectl-proxy
fleet) over to SEAM with no downtime, validating behavior equivalence before
and after, handling name/DNS propagation, and rolling back when something is
wrong.

This is the operational companion to `docs/plan/plan.md` Phase 6b. The plan
decides *what* cutover is (service-by-service, never big-bang; each service's
CLAUDE.md prose deleted in the same change that cuts its agents over); this
runbook is the *how*, stage by stage, with the tooling that mechanizes the
go/no-go gate.

The gateway-side operational material (health endpoints, failure modes,
OpenBao debugging) lives in `docs/operational-runbook.md`. Deployment/binary
rollback mechanics are owned by the plan's Version Migration Strategy §1 and
summarized in §Rollback below — where this runbook and the operational
runbook disagree, this runbook and the plan win.

---

## Table of Contents

1. [The Cutover Model](#the-cutover-model)
2. [Timeline Overview](#timeline-overview)
3. [Stage 0 — Capture](#stage-0--capture)
4. [Stage 1 — Fragment and Shadow](#stage-1--fragment-and-shadow)
5. [Stage 2 — Validate (Go/No-Go)](#stage-2--validate-go-no-go)
6. [Stage 3 — Cutover](#stage-3--cutover)
7. [Stage 4 — Soak (Dual-Run)](#stage-4--soak-dual-run)
8. [Stage 5 — Decommission the Incumbent](#stage-5--decommission-the-incumbent)
9. [Names and DNS Propagation](#names-and-dns-propagation)
10. [Rollback](#rollback)
11. [Per-Service Annex](#per-service-annex)
12. [Evidence Bundle](#evidence-bundle)
13. [Tooling Reference](#tooling-reference)

---

## The Cutover Model

Cutover is **not a load-balancer flip**. Agents (NEEDLE workers, Claude
sessions) discover each proxy's address from hand-written CLAUDE.md/AGENTS.md
prose at session start, and perpetually-live sessions keep that address in
context for hours without re-reading anything. So "cutting over" a service
means exactly two things, shipped as one change (plan, Adoption):

1. The service's route fragment is live and serving on SEAM.
2. A commit deletes that service's discovery-and-auth prose from the
   agent-facing docs, leaving the SEAM endpoint pointer.

Nothing is interrupted: from the moment the fragment is live until the
incumbent is decommissioned, **both proxies serve concurrently** against the
same upstreams. Sessions started before the prose deletion keep calling the
incumbent; sessions started after call SEAM. Traffic migrates at the rate
sessions turn over — which is why "no downtime" is a structural property
here, not a orchestration feat:

- **No request is ever re-routed mid-flight.** A session's proxy choice is
  made once, at session start, from the prose it read.
- **The only availability gaps are SEAM's own rollouts** — onboarding a
  service (one pod-template change, `maxSurge: 0`, typically 5–30 s) and
  binary deploys — and the plan's ≥60-second caller retry-with-backoff
  requirement exists precisely to absorb them (Version Migration Strategy
  §1). Verifying that retry budget is a Stage 2 gate item, not an
  aspiration.
- **Rollback is cheap only because decommission is late.** The incumbent
  stays fully functional through Stage 4; restoring the prose is then a
  one-commit revert. See §Rollback.

Never cut over more than one service in one change, and never batch a
cutover with a SEAM binary deploy — a regression then has two candidate
causes and the rollback ladder loses its granularity.

## Timeline Overview

```
Stage 0        Stage 1         Stage 2         Stage 3      Stage 4         Stage 5
CAPTURE  --->  FRAGMENT   ---> VALIDATE  --->  CUTOVER ---> SOAK       ---> DECOMMISSION
(record at     (author +       (go/no-go       (delete      (dual-run;      (only after
 incumbent)    shadow live)     gate)           the prose)   watch decay)    incumbent quiet)

incumbent:  ==================== serves all traffic ====================>  retired
SEAM:                     [onboard rollout]  fragment live, unpointed-at -> serves new sessions -> serves all
agents:                   still 100% on incumbent        old sessions: incumbent | new sessions: SEAM
```

Each stage's exit criteria gate the next; a red differential corpus blocks
that one service and nothing else (plan, Conformance / differential
harness).

---

## Stage 0 — Capture

**Goal:** record the incumbent's exact observed behavior as the oracle every
later stage is judged against — "the very behavior agents were built
against, not a hand-written expectation of it" (plan, Testing Strategy).

Run `seam-capture` in front of the incumbent and exercise the proxy the way
agents actually do — through real working sessions if possible, or by
replaying the documented CLAUDE.md recipes by hand:

```bash
cd tools/diffharness && go build -o ../../seam-capture ./cmd/seam-capture
cd ../..
./scripts/capture-argocd.sh start      # or the service's capture script
# ...drive the incumbent's documented usage through the capture port...
./scripts/capture-argocd.sh stop
```

Corpus hygiene rules:

- **Every route the prose documents must have at least one entry.** A route
  with no corpus entry is a route whose equivalence is never tested — if it
  cannot be exercised safely (destructive writes, cost), capture it with
  `expect.skip` set and a reason, so the gap is visible in the report rather
  than absent.
- **Cover the error shapes agents branch on**: 400 on a malformed request,
  401/403 where the incumbent produces them, 404 for a missing resource.
  Agents switch on status codes; equivalence of the happy path alone proves
  nothing about the failure paths that drive their retry logic.
- **Secrets are refs, never values.** The capture tool records
  `vault:seam/routes/<svc>/...` references; values are resolved at replay
  time from a git-ignored file or environment (diffharness README, Secrets
  Resolution). Never commit a secrets file.
- **Re-capture if the incumbent changes.** The corpus is the oracle for a
  *specific* incumbent behavior; an incumbent deploy that alters responses
  invalidates the capture.

Corpora live at `corpus/<service>/corpus.json` (per-cluster corpora for the
kubectl-proxy fleet at `corpus/kubectl-proxies/<cluster>/`). The four
services in Phase 6b scope already have captured corpora.

**Exit criteria:** corpus committed covering every documented route and the
error shapes; no literal secret values in the corpus file.

---

## Stage 1 — Fragment and Shadow

**Goal:** the service's fragment is authored, linted, onboarded, and serving
correctly on SEAM — while **no agent has been pointed at it**. This is the
zero-risk live validation window: a broken fragment here inconveniences
nobody.

1. **Author the fragment** at `k8s/rs-manager/seam/routes/<svc>/` in
   `declarative-config` (one directory per service, N fragment files —
   plan, Architecture, "Where fragments live"). The corpus is the source of
   truth for paths, methods, and response shapes.
2. **`seam lint` must pass in CI before the fragment ships** (Phase 9a gate;
   no fragment ships through an ungated path, including the Phase 3 pilot).
3. **If the fragment names a new upstream host**, add it to
   `seam-upstream-allowlist` in the same change — a non-allowlisted host
   quarantines the fragment fleet-wide (Phase 5's bare-MagicDNS entries are
   the worked example).
4. **Onboarding is a rollout**, not a ConfigMap edit: adding the service's
   `configMap` volume touches the pod template, so expect the one
   typically-5–30 s gap (plan, Architecture, "onboarding" exception). Schedule
   it like a deploy: the ≥60 s caller retry budgets must already be in
   place — but note nothing depends on them yet, since no agent calls SEAM
   for this service.
5. **Shadow-validate live:** with the fragment mounted, SEAM serves the
   service's routes to anyone who asks — so ask. Spot-check a few corpus
   entries by hand against SEAM while the incumbent still serves real
   traffic.

Verify at the operator port (`/config/status`): the fragment is loaded, not
quarantined, and its upstream is allowlisted.

**Exit criteria:** fragment live and unquarantined on SEAM; incumbent
untouched and still serving 100% of the service's traffic; no prose changed
anywhere.

---

## Stage 2 — Validate (Go/No-Go)

**Goal:** mechanical proof that SEAM's behavior is equivalent to the
incumbent's, plus the two trust checks the plan makes mandatory. This is the
gate; nothing in Stage 3 happens until every item is green or attested.

### The gate items (plan Phase 6b + Conformance harness)

| # | Item | Mechanism | Blocking? |
|---|------|-----------|-----------|
| 1 | Differential corpus green against the live SEAM build | `seam-replay` — the shipped-commit rule applies: the pass must be on the exact build that will serve traffic | **Hard** |
| 2 | Trust-boundary ACL at **both** ports: worker tags reach the caller port, and are **refused on the operator port** | `seam-cutover check` from a worker-vantage host; the refusal half is the item easiest to skip because nothing fails when it is wrong | **Hard** |
| 3 | Every agent of this service retries with backoff over **≥60 s** | manual attestation in the cutover PR (see below) | **Hard** |
| 4 | SEAM healthy and ready; service's routes present in `/openapi.json` | `seam-cutover check` | Hard |
| 5 | Incumbent still up and its name resolvable (dual-run precondition) | `seam-cutover check` | Hard |
| 6 | Metered services only: Phase 13 cost governor live | plan gating ("no agent is cut over onto them until Phase 13") | **Hard** for z.ai/GLM, twitterapi.io |

Run the mechanized portion:

```bash
seam-cutover check \
  --service argocd \
  --seam https://seam-rs-manager-ts.ardenone.com:8444 \
  --operator https://seam-rs-manager-ts.ardenone.com:8445 \
  --incumbent https://argocd-ro-ardenone-manager-ts.ardenone.com:8444 \
  --corpus corpus/argocd-proxy/corpus.json \
  --replay-bin ./seam-replay \
  --secrets corpus/argocd-proxy/secrets.local.json \
  --agent-doc /home/coding/CLAUDE.md \
  --agent-doc-contains 'argocd-ro-ardenone-manager-ts' \
  --report corpus/argocd-proxy/cutover-check.json
```

Run it **from a worker-vantage host** (ex44 or the lab, holding a
worker-tagged tailnet identity) — item 2 is only meaningful from where the
agents actually sit. Exit code 0 = no mechanical failures; MANUAL items
(the retry budget, metered-service gating) are printed and must be attested
in the cutover PR description.

The `--agent-doc-contains` flag enforces sequencing mechanically: it fails
if the incumbent prose has already been deleted from the agent docs, which
guards the "prose is not deleted until the corpus passes" ordering from the
wrong direction too.

### On the ≥60 s retry-budget attestation (item 3)

This is the one gate item that cannot be probed from SEAM, and the plan is
explicit that it is a checklist item, not an aspiration: SEAM is single-replica
with `maxSurge: 0`, every future deploy is a real 5–30 s gap, and an agent
cut over with a one-second retry budget will fail across every SEAM deploy
thereafter — the first deploy after cutover is when it surfaces. Attest it
by naming, in the cutover PR, where each affected agent population's retry
behavior is configured (harness settings, wrapper scripts) and the budget
it configures. "Probably fine" is not an attestation.

**Exit criteria:** `seam-cutover check` exit 0 with the report attached;
retry-budget attestation written; for metered services, Phase 13 confirmed
live.

---

## Stage 3 — Cutover

**Goal:** new sessions start on SEAM. This is a **documentation commit** —
no cluster change, no SEAM change, no DNS change.

The cutover commit, in one change (plan, Adoption):

1. **Delete** the service's hand-written discovery-and-auth prose from the
   agent-facing docs (the per-service `kubectl`/`curl` recipes in CLAUDE.md
   and any repo-level AGENTS.md that repeats them).
2. Leave (or add) **only the SEAM endpoint pointer** in its place — the end
   state is a CLAUDE.md that carries the pointer, not a per-service recipe.
3. The commit message names the service and the bead, so the revert target
   for L1 rollback is findable (`git log --grep`).

Sequencing rules:

- The commit lands **only after** Stage 2 is fully green. The corpus pass
  and the prose deletion must not share a commit — one is evidence, the
  other is the action; reviewing them apart is the point.
- **No other change rides along.** No fragment edits, no SEAM deploy, no
  other service's prose. When rollback needs to revert this commit, it must
  revert *only* cutover.
- Old sessions notice nothing. They hold the incumbent's address in context
  and keep using it — correctly — through Stage 4.

**Exit criteria:** prose deleted in one revertable commit; SEAM untouched;
incumbent untouched.

---

## Stage 4 — Soak (Dual-Run)

**Goal:** watch real traffic migrate and catch what the corpus could not.
Both proxies serve; nothing may be decommissioned.

- **Watch SEAM's side:** per-route error rate and latency from
  `/_seam/metrics` (VictoriaMetrics, scraped from Phase 6a). A post-cutover
  regression observable at the boundary — error-rate step, p99 breach — is
  a rollback trigger (§Rollback), exactly as after a binary deploy.
- **Watch the incumbent's side:** its traffic should only ever decay. The
   incumbent proxies have no per-route metrics, so use their access logs
   (or capture a window with `seam-capture`, which records what still
   calls them). A corpus re-run against the live build after any SEAM
   deploy during soak re-establishes the equivalence claim on the exact
   bytes serving traffic (the all-gates-same-commit rule).
- **Incidents during soak** roll back at L1 (§Rollback) — restore the
  prose, new sessions return to the incumbent, SEAM sessions drain
  naturally. Fix SEAM, re-run Stage 2, cut over again.

**Exit criteria:** SEAM serving the service with no unexplained error-rate
step; incumbent traffic monotonically decayed.

---

## Stage 5 — Decommission the Incumbent

**Goal:** retire the incumbent only when nothing calls it — usage-gated,
never calendar-gated, on the same principle as the plan's route retirement
(Phase 8): a calendar term may only ever *lengthen* the wait, never trigger
a removal on its own.

The incumbent has no per-route-version counters, so quiet is measured from
its access logs:

> **Quiet window = max(3 × the longest observed inter-request gap on the
> incumbent, 7 days) after the cutover commit** — i.e. decommission no
> earlier than 7 days after cutover, and later if the incumbent's own
> traffic pattern says a slow caller might still return (a worker with a
> weekly rhythm silently breaks under a bare 7-day window).

Then:

1. Confirm **zero incumbent requests** across the whole quiet window from
   the logs, not from absence of complaints.
2. Remove the incumbent's manifests from `declarative-config` (git commit →
   push → ArgoCD sync — never a live `kubectl delete`; orphan cleanup only
   for objects ArgoCD does not own).
3. **Keep** the corpus, the fragment, and the OpenBao credential paths —
   the corpus remains the regression oracle for the SEAM route, and the
   credential is now SEAM's to inject.
4. Leave the incumbent's DNS name unallocated for a further grace period
   (§Names and DNS Propagation) so a stale cached address fails loudly
   rather than reaching whatever might later claim the name.

Decommissioning is irreversible-ish (re-provisioning the incumbent is real
work) — that is precisely why it is the last stage and gated on evidence.

---

## Names and DNS Propagation

**The rule: SEAM gets its own name; an incumbent's name is never repointed.**

SEAM's MagicDNS name and its tailnet ACL grants are provisioned in Phase 6a,
before any cutover (the running example in this runbook is
`https://seam-rs-manager-ts.ardenone.com:8444`; the concrete name is fixed
in the Phase 6a manifest, following the `argocd-ro-ardenone-manager-ts`
entrypoint pattern). Cutover then changes **no DNS at all** — agents are
moved by the prose commit, not by a name flip. Three reasons this is
load-bearing rather than tidy:

1. **The effective DNS TTL here is the session lifetime.** A perpetually-live
   agent session holds the incumbent's *URL* in its context — re-resolving
   the name would not help it, because nothing tells it to. Repointing a
   name therefore does not migrate sessions; it only breaks the ones that
   cached it, hours to days later, at a moment nobody chose. The prose
   commit migrates sessions at a moment they chose (their own startup).
2. **Dual-run requires both names to resolve simultaneously** for the whole
   soak window. One MagicDNS name cannot front two backends; two names
   front two backends trivially.
3. **Rollback requires the incumbent reachable under its old name,
   instantly.** L1 rollback is "revert one commit" *because* the incumbent
   never moved.

Operational consequences:

- **Do not rename or re-address anything mid-soak.** The incumbent's name is
  load-bearing until Stage 5 completes, and stale-resolution grace (item 4
  above) extends past it.
- **MagicDNS resolver caching is a non-issue by comparison** — resolver TTLs
  are seconds-to-minutes; the plan-level propagation unit is the session.
  When adding SEAM's own name in Phase 6a, verify resolution from a worker
  vantage once (`seam-cutover check` does this as part of its DNS checks);
  that is all the DNS verification cutover needs.
- **TLS never has to be re-issued or re-SAN'd**, because names are never
  shared or moved. Each name carries its own cert for its lifetime.

### If a caller cannot be re-pointed by prose (last resort)

A caller with the incumbent's URL hardcoded (not learned from docs) will not
migrate at the prose commit. Do **not** solve this by repointing the shared
name while the incumbent still carries traffic. In order of preference:

1. **Fix the caller** — hardcoded proxy addresses in scripts/config are a
   bug the migration is exposing; point them at the SEAM endpoint.
2. **Wait it out** — if the caller is itself short-lived (a cron, a script),
   it crosses over the next time it re-reads its config; keep the incumbent
   alive until it has demonstrably stopped calling.
3. **Name takeover, scheduled** — only when 1 and 2 are impossible: wait for
   zero incumbent traffic (Stage 5 evidence), *then* move the old name to
   SEAM as a deliberate, scheduled change. Any straggler flips hard at its
   next resolution; the ≥60 s retry budget is what absorbs it. This is a
   DNS change with a maintenance window, not a cutover technique.

---

## Rollback

Three levels, escalating scope. **Always start at the lowest level that
covers the symptom** — each higher level affects more services.

| Level | Scope | Trigger | Mechanism | Cost |
|-------|-------|---------|-----------|------|
| **L1 — agent traffic** | one service | post-cutover errors/regression on that service from SEAM | `git revert` the prose-deletion commit | one commit; zero SEAM changes; SEAM sessions drain naturally |
| **L2 — fragment** | one service, all its callers | fragment misbehaving for every caller (quarantine-worthy fault in the route itself) | `git revert` the fragment commit in `declarative-config` (or remove the fragment file) | hot-reload via ConfigMap, **no pod restart**, no gap; other services unaffected |
| **L3 — binary** | **all services** | new pod never ready (readyz still failing at 2 min), or ready-but-wrong (corpus/acceptance failure, error-rate step, p99 breach) | `git revert` the image-digest commit in `declarative-config`, push, force the ArgoCD Application sync if it lags | another single-replica rollout: one more typically-5–30 s gap, absorbed by the ≥60 s retry budgets |

`seam-cutover rollback --service <svc> [--level agent|fragment|binary|all]`
prints the concrete sequence for the service, with the revert-finding
commands filled in.

Hard rules, from the plan (Version Migration Strategy §1) and the org
GitOps policy — repeated here because they are the part most tempting to
violate mid-incident:

- **The mechanism is always a git revert in `declarative-config`, never a
  live mutation.** `kubectl rollout undo`, `kubectl apply`, `kubectl edit`,
  `kubectl rollout restart` are **not the path**: ArgoCD `selfHeal` reverts
  live mutations, so an imperative rollback fights the controller and
  loses. Forcing the ArgoCD Application sync is the sanctioned accelerator —
  it *applies* the repo; it does not bypass it.
- **Never force-push** the revert. Reconcile with a merge commit if
  diverged.
- **No state capture is needed before an L3 rollback, and that is a designed
  property** — every in-process store (guard counters, quota ledgers,
  breaker state, last-2xx, cache) is restart-scoped by construction. The one
  caveat: tumbling quota windows reset on the rollback exactly as on any
  deploy — the known brake-not-ledger tradeoff.
- **The 2-minute readyz trigger is mechanical, not negotiable.** A `/_seam/readyz`
  still failing at 2 minutes after rollout start is a rollback, not a
  debugging session — debugging happens on the reverted-to-working gateway's
  time, never on the fleet's.

### What each level does to in-flight traffic

- **L1** never touches SEAM. Sessions already on SEAM for that service
  finish naturally against a fragment that stays live (fix forward through
  Stage 2 again for the *next* cutover); new sessions return to the
  incumbent. This is why the incumbent must not be decommissioned early.
- **L2** hot-swaps the route table: in-flight requests complete against the
  old snapshot, new requests see the fragment gone (404 at SEAM's boundary
  — which is why L2 is paired with L1 whenever the fragment is *wrong*
  rather than merely *risky*: revert the prose first, then the fragment).
- **L3** is a full pod replacement with the known gap. It is fleet-wide and
  therefore the last resort; if a single service is symptomatic, L1/L2
  almost always isolate it faster and for fewer people.

### Recovering the pre-cutover state of the prose

L1 assumes the prose-deletion commit is findable. That is why Stage 3
requires the commit message to name the service and bead:

```bash
git -C <docs-repo> log --oneline --grep='seam' -- <prose-file>   # find the deletion commit
git -C <docs-repo> show <commit> -- <prose-file>                 # confirm it is the right one
git -C <docs-repo> revert <commit>
```

If the prose file is not git-tracked (a host-local CLAUDE.md), the cutover
must instead keep a copy of the deleted prose (in the PR description or the
bead) — restoring it is then a paste, and the bead is the audit trail.

---

## Per-Service Annex

### ArgoCD read-only proxy (Phase 3 pilot)

- **Incumbent:** `https://argocd-ro-ardenone-manager-ts.ardenone.com:8444`
  (read-only API proxy, injects its own RO bearer token server-side).
- **Fragment:** pass-through, **no injection** — the pilot deliberately
  exercises the fragment/reload/quarantine path, not the injection path
  (plan GAP 8); credential injection is first proved end-to-end in Phase 4.
- **Unmetered, read-only** — lowest blast radius; needs neither the cost
  governor nor caller auth to be safe, which is exactly why it is the pilot.
- **Corpus:** `corpus/argocd-proxy/corpus.json`; capture helper
  `scripts/capture-argocd.sh`.
- **Watch for:** the incumbent is the fleet's ArgoCD *read* path used by
  operators and automation — during soak, monitor both the agent traffic
  and any operator tooling that reads the same prose.

### z.ai/GLM proxy and twitterapi.io proxy (Phase 4)

- **Both metered.** No agent is cut over onto either until Phase 13's cost
  governor is live (plan Phase 4/6b gating) — Stage 2 item 6 is a hard gate
  here, and the gate is per-service: cutting over argocd does not wait for
  it, cutting over these does.
- **First real injection:** these are the first fragments to exercise the
  full `x-vault-path` → `x-inject-as` path. The differential corpus's
  leak-check (an echoed secret in a SEAM response is a hard FAIL; a
  `[REDACTED-BY-SEAM]` redaction is a PASS) is the whole point — run it
  against the real credential shapes, not stand-ins, before cutover.
- **Budget semantics:** until Phase 7, `x-quota` degrades to a single
  route-wide shared pool and `X-SEAM-Budget-Remaining` reports the shared
  remainder — set soak expectations accordingly (one heavy agent can 402
  the route for everyone; that is the designed fail-closed behavior, not a
  regression).
- **Corpora:** `corpus/zai-proxy/corpus.json`,
  `corpus/twitterapi-proxy/corpus.json`; capture helper
  `scripts/capture-twitterapi-proxy.sh`.

### kubectl-proxy fleet (Phase 5)

- **One parametrized multi-instance fragment**, `x-instance-param: cluster`,
  declaring `/k8s/{cluster}/…`; the nine-entry upstream map and the
  per-instance allowlist lines are fixed by the plan (Phase 5).
- **Corpora are per-cluster** (`corpus/kubectl-proxies/<cluster>/`) —
  equivalence must hold per cluster, because each incumbent proxy is its
  own deployment with its own RBAC. A fleet-wide corpus pass is the
  conjunction of per-cluster passes; there is no averaged answer.
- **`_all` fan-out entries expect `207`** on partial success — set
  `expect.status` accordingly (diffharness corpus format), and remember a
  fan-out is billed per dispatched instance.
- **Mixed privilege in one map:** the ~3-day `cloudspace-admin` read/write
  proxies and the long-lived read-only observers carry distinct
  per-instance `requiredScope` (enforced from Phase 7). Until then, treat
  the whole fleet as cutting over at the privilege level of its most
  privileged instance, and prefer cutting over observer instances first,
  admin-credentialed instances last.
- **Admin-credential expiry cadence** (~3-day OIDC tokens) does not change
  at cutover — SEAM injects what OpenBao holds; the rotation workflow
  stays the operator's.

---

## Evidence Bundle

Every claim in a cutover is evidence, not belief (plan, Evidence bundle).
The cutover PR for a service attaches, at minimum:

1. **The differential-corpus pass report** (`seam-replay` JSON output) —
   produced on the exact build that will serve traffic.
2. **The `seam-cutover check` report** (JSON) — health/readyz, both-port
   ACL result from a worker vantage, route-presence, DNS preconditions.
3. **The retry-budget attestation** — where each agent population's retry
   behavior is configured and the ≥60 s budget it configures.
4. For metered services: **confirmation Phase 13 is live** (link to the
   bead/commit).
5. The PR diff itself, showing **only** the prose deletion for that one
   service.

The same bundle re-attaches after any soak-window rollback-and-retry.

---

## Tooling Reference

All three tools live in `tools/diffharness` (standalone Go module,
stdlib-only; build with `go build ./cmd/<name>` from that directory — full
usage in its README):

| Tool | Stage | Role |
|------|-------|------|
| `seam-capture` | 0 | recording proxy in front of the incumbent; writes the corpus (secret refs only) |
| `seam-replay` | 2, 4 | replays the corpus against incumbent **and** SEAM; differential comparison with leak-detection; non-zero exit on any FAIL — the conformance gate |
| `seam-cutover` | 2 | go/no-go gate runner (`check`): healthz/readyz, operator-port refusal from a worker vantage, corpus-route presence in `/openapi.json`, DNS preconditions, prose-still-present guard, and the `seam-replay` subprocess; `rollback` prints the per-service rollback runbook (§Rollback) with concrete commands |

Exit-code contract for `seam-cutover check`: **0** = no mechanical failures
(MANUAL items still require operator attestation); **1** = at least one
mechanical FAIL; **2** = usage error. A NO-GO never blocks other services —
it blocks this one, matching the service-by-service granularity Phase 6b is
built on.
