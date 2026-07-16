# Future Feature: Per-Caller Version Pinning

Status: parked as a future feature 2026-07-16 — deliberately **not** in `docs/plan/plan.md`'s phases.

## What

Stripe's model, applied to SEAM: each caller identity is locked to whatever merged-spec version was live at its **first call**. The gateway transparently applies that version's request/response adapters on every subsequent call, so from the caller's perspective the contract literally never changes until the caller itself opts into a newer version (or restarts and re-pins).

## Why it's stronger than what the plan has

The shipped mechanisms (usage-gated retirement + `Deprecation`/`Sunset` headers + `X-SEAM-Spec-Version` drift signaling with progressive `/changes`) all assume the caller *eventually notices and adapts*. Pinning removes the assumption: a perpetually-live worker **cannot** be broken by a contract change, even one that ignores every advisory signal — the strongest possible guarantee for the "session learns the contract once and never refetches" problem.

## Why it's parked

- **First stateful thing in SEAM.** Pins must survive gateway restarts, so this introduces a persistent per-identity registry — until now the gateway is fully stateless (route table from ConfigMaps, secrets from OpenBao, health tracking deliberately restart-scoped).
- **Adapter tail grows unboundedly** unless pins expire. Usage-gated retirement bounds the adapter set by *traffic*; pinning bounds it by *the oldest living pinned identity*, which for perpetually-live workers may be "forever." Needs a pin-expiry story (e.g. pins die when the tsnet ephemeral node disappears from the tailnet).
- **Interacts with retirement:** a single pinned caller keeps a version alive indefinitely, defeating the quiet-window mechanism unless pin-expiry is solved first.
- **Depends on Phase 7** (identity — you can't pin a caller you can't distinguish) **and Phase 8** (version machinery).

## Revisit trigger

Adopt only if drift signaling + usage-gated retirement prove insufficient in practice — concretely: a worker breaks mid-session on a contract change *despite* `/changes` having been available and the deprecated fragment still being served. If that never happens, this feature never earns its state.
