# Future Feature: Per-Caller Version Pinning

Status: parked as a future feature 2026-07-16 — deliberately **not** in `docs/plan/plan.md`'s phases.

## What

Stripe's model, applied to SEAM: each caller identity is locked to whatever merged-spec version was live at its **first call**. The gateway transparently applies that version's request/response adapters on every subsequent call, so from the caller's perspective the contract literally never changes until the caller itself opts into a newer version (or restarts and re-pins).

## Why it's stronger than what the plan has

The shipped mechanisms (usage-gated retirement + `Deprecation`/`Sunset` headers + `X-SEAM-Spec-Version` drift signaling with progressive `/changes`) all assume the caller *eventually notices and adapts*. Pinning removes the assumption: a perpetually-live worker **cannot** be broken by a contract change, even one that ignores every advisory signal — the strongest possible guarantee for the "session learns the contract once and never refetches" problem.

## Why it's parked

- **First state in SEAM that must *survive a restart*** (rationale corrected 2026-07-20 — this previously argued "first stateful thing in SEAM… until now the gateway is fully stateless", a premise `docs/plan/plan.md` has since reversed: SEAM is emphatically **not** stateless, and guard counters, quota ledgers, last-2xx and breaker state are per-process and load-bearing enough to forbid a second replica). The correct distinction is stronger, not weaker. SEAM already holds plenty of state — but every piece of it is **per-process, restart-scoped, and deliberately losable**: a restart resets budget windows and loop counters, drops breaker and last-2xx state, and empties the spec ring buffer, and each of those degrades to an already-defined safe behavior. Pins cannot work that way. A pin that evaporates on restart silently re-pins the caller to whatever is newest, which is precisely the contract change pinning exists to prevent — so pins must be **durable**, making this the first thing in SEAM that needs a PVC or an external store **in the request path**. That is exactly what plan §1 refuses ("no request-path state, no PVC") and what the single-replica invariant is built around, so adopting pinning is not adding a feature to a stateless proxy but changing the deployment's storage posture.
- **Adapter tail grows unboundedly** unless pins expire. Usage-gated retirement bounds the adapter set by *traffic*; pinning bounds it by *the oldest living pinned identity*, which for perpetually-live workers may be "forever." Needs a pin-expiry story (e.g. pins die when the tsnet ephemeral node disappears from the tailnet).
- **Interacts with retirement:** a single pinned caller keeps a version alive indefinitely, defeating the quiet-window mechanism unless pin-expiry is solved first.
- **Depends on Phase 7** (identity — you can't pin a caller you can't distinguish) **and Phase 8** (version machinery).

## Revisit trigger

Adopt only if drift signaling + usage-gated retirement prove insufficient in practice — concretely: a worker breaks mid-session on a contract change *despite* `/changes` having been available and the deprecated fragment still being served. If that never happens, this feature never earns its state.
