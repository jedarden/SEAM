# Versioning & Migration Policy

## Problem

NEEDLE workers run perpetually-live sessions (hours+), each learning SEAM's API contract once early in its session (via `/openapi.json`) and continuing to call routes on that understanding for the session's duration. No client can be assumed to ever restart or refetch the spec on a schedule. Route contracts still need to evolve, so SEAM needs **three** separate migration mechanisms — one for the gateway's own code (§A), one for route contracts (§B), and one for helping a perpetually-live caller *notice* a contract change mid-session (§C, adopted 2026-07-16 and folded in here as the third mechanism rather than an addendum to the second; corrected 2026-07-20 — this read "two", predating §C).

> **Section mapping.** `docs/plan/plan.md` Version Migration Strategy numbers these §1/§2/§3, mapping one-to-one onto §A/§B/§C here. A `§`-reference means the same thing in either document, and the plan instructs readers to cite them interchangeably — so where this note and the plan disagree on a decision, **the plan is the authority** and the disagreement is a bug in this note.

## A. Gateway process deploys (code changes, no contract change)

Standard Kubernetes rolling-update mechanics: readiness probe gates traffic before a new pod accepts requests, graceful `SIGTERM` handling (stop accepting new connections, finish in-flight requests, exit within `terminationGracePeriodSeconds`). This covers "SEAM's own binary changed" only — it does not cover a route's contract changing underneath a caller.

**SEAM runs as a single replica** (amended 2026-07-20 — this section previously said "≥2 replicas, `PodDisruptionBudget` `minAvailable: 1`", which the plan has since reversed; `docs/plan/plan.md` Version Migration Strategy §1 is the authority and carries the full rationale). The reversal matters enough to restate rather than merely link, because deploying two replicas on the strength of the old text **silently doubles every threshold in the plan**: SEAM is emphatically *not* stateless — loop-breaker counters, cost-governor budgets, last-2xx tracking and circuit-breaker state are all per-process and in-memory by design — so two pods behind one Service turn a loop-guard threshold of 10 into 20, a `$50` `x-quota` into `$100` against a metered upstream (twitterapi.io credits, the z.ai quota — real money), make `X-SEAM-Budget-Remaining` depend on which pod answered, and open the breaker on one replica while the other keeps hammering a dead upstream.

- **`replicas: 1`**, with `strategy.rollingUpdate: {maxSurge: 0, maxUnavailable: 1}` — a rollout never runs two SEAM pods at once, so there is no overlap window at all.
- **No `PodDisruptionBudget`** demanding an available replica: at one replica it would only deadlock node drains.
- The cost is a **sub-second gap during a binary deploy**, which a retrying agent absorbs and which is cheap because binary deploys are rare — route and contract changes are ConfigMap edits that hot-reload without restarting the pod.
- The invariant is load-bearing: **no feature may be built assuming it can be replicated**, and raising `replicas` is not a valid operational response to load. If HA is ever genuinely required, the documented path is a shared state store for guard counters, quota ledgers and breaker state (Redis or equivalent) plus leader election for the sentinel — scoped work, not a config bump. The credential sentinel's probe loop is leader-elected via a Kubernetes `Lease` regardless, since duplicated probe traffic is the one stateful failure visible *outside* SEAM.

## B. Route contract versioning (breaking changes to a route's shape)

Borrows two precedents directly (full detail in `docs/research/version-migration-precedent.md`):

- **From Kubernetes' API deprecation policy:** coexistence is the mechanism, not a transitional inconvenience — old and new versions of a route serve simultaneously for as long as anything is calling the old one.
- **From Stripe's dated versioning:** rather than maintaining N full parallel implementations, implement a route's logic once against the newest contract, and add a thin adapter (request transform in, response transform out) for each older version still being served.

Concretely, in SEAM's ConfigMap-per-route-fragment model:

- A breaking route change ships as a **new** fragment (new path segment or `x-api-version`), never as an in-place edit to an existing fragment.
- The old fragment stays mounted, mapped to a thin adapter in front of the single current implementation — not a full duplicate handler.
- Every response served from a fragment carrying **`x-seam-deprecated`** carries `Deprecation` (RFC 9745) and, conditionally, `Sunset` (RFC 8594) — advisory only, not the retirement mechanism, since neither RFC guarantees a client acts on them and no precedent shows long-running agents reliably do. **The marker is a SEAM extension and an object, not OpenAPI's `deprecated: true`** (corrected 2026-07-20 — this line previously said "a fragment marked `deprecated: true`", which has no meaning in the spec: OpenAPI's `deprecated` is an operation-level boolean with no document-level equivalent, while SEAM's unit of versioning is the whole fragment). `x-seam-deprecated` is `{since, sunset?}`: `since` is required and populates `Deprecation` directly; `sunset` is optional and advisory. Both headers are date-valued, which is why a boolean could not have driven them. `Deprecation` is emitted from the moment the marker lands; `Sunset` only once the author supplied one or the quiet window has opened and its end resolves to a date — omitted entirely before that. Standard per-operation `deprecated: true` stays valid for deprecating a single operation inside an otherwise-live fragment.
- **Retirement: zero observed traffic is a *necessary condition*; the calendar term may only lengthen the wait** (reworded 2026-07-20 — this previously read "usage-gated, not calendar-gated", and that exact phrasing was misread as forbidding the calendar floor the adopted quiet-window composition depends on). SEAM exports a per-route-version request-count metric (mirroring Kubernetes' `apiserver_requested_deprecated_apis` gauge), **excluding sentinel probe traffic** — a deprecated fragment keeps probing, so a probe-inclusive counter would never read zero and no probed fragment could ever retire. A deprecated fragment is removed only once that counter has been zero for a quiet window. The distinction that was always intended: **no route is ever retired because a date arrived, only because it stopped being called.** A calendar term can lengthen the wait and can never trigger a removal on its own, which is exactly what makes a floor safe to add.
- Once per-worker identity exists ([[project_seam_gateway|Phase 7]], `tsnet`), the same metric can be broken down by caller, so a stuck worker on a deprecated route can be identified and drained individually instead of just waiting out the aggregate.

## C. Passive drift signaling (adopted 2026-07-16)

Neither `Deprecation`/`Sunset` headers nor hoping agents refetch `/openapi.json` gives a perpetually-live worker an actionable path to *catch up* mid-session. Two additions close that gap:

- Every response carries `X-SEAM-Spec-Version` — a hash of the currently-served merged spec. A worker that cached the contract hours ago sees the version change on a call it was already making, at no extra request cost.
- The catch-up diff is **progressively discoverable** (decided 2026-07-16 — full schema diffs are the most useful form but token-expensive, so they're layered):
  - **Level 1:** `/changes?since=<version>` — token-cheap route-level list: path, verb, change kind (added / removed / params-changed / response-changed / deprecated), a one-line summary, and **two drill-down links per route, not one** (respecified 2026-07-20 — they answer different questions and a catching-up agent wants both): **`diffUrl`** → `/changes?since=<version>&route=<path>`, the level-2 field-level diff, answering *what changed*; and **`docsUrl`** → `/docs/{route}?version=<v>`, answering *what the contract now is* — the diff to decide whether it cares, the docs to rewrite its call if it does.
  - **Level 2:** `/changes?since=<version>&route=<path>` — the full field-level schema diff for that single route (changed fields only, before/after).
  - **Level 3:** `/openapi.json?version=<v>` — any archived full spec, for total reconstruction.
  - Agent workflow: notice the header change → pull level 1 → drill into only the routes it actually uses. An unknown or evicted `since` version gets a pointer to the full current spec instead.
- Backed by a ring buffer of the last N merged specs.

Advisory headers and usage-gated retirement (section B) are unchanged — this adds the catch-up mechanism they lacked.

## Parked: per-caller version pinning

Stripe-style per-caller version pinning (each identity locked to whatever spec version was live at its first call) is parked as a future feature — see `docs/notes/per-caller-version-pinning.md` (2026-07-16). The quiet-window length for usage-gated retirement was compared as multiple candidate designs in `docs/notes/quiet-window-options.md` — **resolved 2026-07-20, no longer pending review.** The recommended composition there is adopted verbatim in `docs/plan/plan.md` **Phase 8**, which is now the authority on it: (D) a human-merged declarative-config PR as the always-on gate, opened by a small out-of-band evaluator job rather than by the gateway; (B over A) a traffic-adaptive window `max(3 × observed max inter-request gap, 7 days)`, its gap history read from the metrics store rather than gateway memory; (C) brownout `410`s in the final week, where a caller appearing *cancels* the retirement; and (E) an upgrade to caller-complete drain once Phase 7 identity exists.
