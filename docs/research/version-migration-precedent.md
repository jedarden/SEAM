# Version-Migration Precedent for Perpetually-Live Clients

Research conducted 2026-07-16. SEAM's clients (NEEDLE workers) can run perpetually-live sessions — a session may learn the API contract once (fetching `/openapi.json` early) and keep calling routes on that understanding for hours, with no guarantee it ever restarts or refetches the spec. This asks how other systems solve "evolve the contract without breaking clients that never predictably reconnect."

## 1. Kubernetes API Deprecation Policy

Rules differ sharply by maturity tier. **Alpha** APIs may be removed in any release with zero notice. **Beta** APIs must be deprecated no later than 9 months or 3 minor releases after introduction (whichever is longer), and kept serving for 9 months/3 minor releases after that deprecation. **GA** APIs may be marked deprecated but **must never be removed within a major version** — there's no numeric release countdown; Kubernetes has no current plan for a major version that drops GA APIs at all.

Critically, coexistence isn't incidental — it's structurally required. The apiserver serves multiple versions of the same resource (e.g., `v1`, `v1beta1`, `v2beta2`) simultaneously against one internal storage version, with a mandatory round-trip guarantee: converting any object between versions must lose no information. The "preferred"/storage version can only advance once a release ships that supports *both* old and new simultaneously — **coexistence is the migration mechanism, not a transitional inconvenience.** Deprecation is signaled three ways: an HTTP `Warning` header (RFC 7234) on affected responses (since v1.19), an audit-log annotation, and a Prometheus gauge (`apiserver_requested_deprecated_apis`) labeled with the exact `removed_release`, letting operators detect live traffic against a doomed version before pulling it.

## 2. RFC 8594 `Sunset` and RFC 9745 `Deprecation` Headers

`Sunset` (RFC 8594) is a single HTTP-date value signaling a resource will become unresponsive at that point; it's explicitly for the *decommissioning* stage only, not for "no longer preferred, still works." `Deprecation` (RFC 9745, finalized March 2025) is an Item Structured Header carrying a Unix-timestamp Date (or literal `true` if the date is unknown) marking when deprecation began; a `Sunset` value on the same response must not predate it. Both explicitly are **hints, not guarantees** — RFC 8594 states clients "should function without Sunset information," and neither RFC defines a polling or push protocol. Suited to automated clients that check headers per-call, but neither spec assumes or mandates that behavior.

## 3. Stripe Dated Versioning

Each account pins to the API version live at first call (overridable per-request via `Stripe-Version`). Internally Stripe writes business logic against one current schema only. A **Request Gate** layer filters/adapts incoming params for the caller's pinned version; a **Response Transformation** layer builds the latest response, then applies small reverse-chronological transform modules ("Gates") to walk it back to whatever shape the pinned version expects. Cost: every breaking change adds one permanent transform module that must be maintained until that dated version is formally sunset — a linearly growing but decoupled adapter chain, not N parallel implementations.

## 4. Agent-Survives-Mid-Session Precedent

**No solid precedent exists.** Only adjacent, non-matching material was found: LangGraph's checkpointing addresses crash/restart durability, not live contract drift; Anthropic's long-running-agent guidance covers context/session continuity, not API versioning; a known OpenAI Agents SDK breaking change during handoffs illustrates the failure mode exists but offers no resolution pattern. This is a genuine gap — SEAM's policy (below) synthesizes from adjacent precedent rather than adopting one wholesale.

## Recommendation

Since SEAM's routes are already versionable config fragments: (1) borrow Stripe's adapter model — implement only the newest contract internally, keep thin translation adapters for each old fragment still receiving traffic; (2) borrow Kubernetes' coexistence rule — old and new fragments serve simultaneously, and removal is gated on **observed zero traffic against the old fragment for a quiet window**, not a calendar date, since clients can't be forced to refetch; (3) emit `Deprecation` + `Sunset` headers on old fragments as best-effort signal, but treat them as advisory only, per both RFCs — the real safety net is usage-based gating, not client cooperation, since no precedent shows agents reliably act on headers mid-session.

See `docs/notes/versioning.md` for how this becomes SEAM's concrete policy, and `docs/plan/plan.md` for the phased build-out.
