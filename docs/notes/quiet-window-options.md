# Quiet-Window Options for Deprecated-Fragment Retirement

Status: options for review, 2026-07-16. Decides *when a deprecated route fragment that has stopped seeing traffic may actually be removed*. Context: retirement is usage-gated, never calendar-gated (`docs/notes/versioning.md` §B) — this document is about how long "quiet" must last, and what supplements the raw counter. The options are not all mutually exclusive; a recommended composition is at the end.

## Option A — Fixed calendar window

Remove after the per-route-version request counter reads zero for a fixed N days (7 was the illustrative default).

- **Pro:** trivial to implement and reason about; one number.
- **Con:** the window must exceed the longest plausible *inter-call gap* of any live caller, which nobody actually knows. A worker with a weekly rhythm (e.g. driven by a weekly external event) silently breaks under a 7-day window. Picking N safely means picking it large, which slows all retirements to the speed of the most sluggish imaginable caller.
- **Failure mode:** breaks exactly the caller the usage-gate exists to protect, just on a longer fuse.

## Option B — Traffic-adaptive window

Window per route-version = k × the maximum inter-request gap ever observed on that route-version (e.g. k = 3). A route that historically saw hourly traffic can retire after quiet days; one with weekly bursts waits a month.

- **Pro:** self-sizing from real behavior; no global guess. Directly addresses Option A's failure mode.
- **Con:** needs per-route-version gap history (small, but persistent to be meaningful across gateway restarts); cold routes with little history degenerate to a floor value (i.e. fall back to Option A's number anyway).
- **Failure mode:** a caller whose rhythm *changes* (was hourly, becomes monthly) can still be outwaited incorrectly — rarer than A's failure but not zero.

## Option C — Brownout probes (GitHub-style)

Before real removal, the deprecated route is intentionally served as `410 Gone` for short scheduled windows (e.g. 5 minutes daily during the last week), with a structured body pointing at the replacement and `/changes`. Silent callers that the counter somehow missed reveal themselves as errors during a brownout — and, being LLM agents, actually read the 410 body and can self-migrate.

- **Pro:** converts "we believe nobody calls this" into an actively tested claim; agents get a recoverable rehearsal instead of a permanent surprise. Well-precedented (GitHub API deprecations).
- **Con:** deliberately injects failures; any caller hitting the brownout has a degraded run even though the route still exists. Needs a small scheduler in the gateway.
- **Failure mode:** a caller whose retry logic treats 410 as permanent may abandon the route mid-rehearsal — arguably the desired outcome, but earlier than strictly necessary.

## Option D — PR-gated human retirement

Removal is already a git operation (deleting the fragment from declarative-config). So the "workflow" is: when the quiet condition is met, SEAM (or a small cron) **opens the declarative-config PR** with the traffic evidence in the description; a human merges. Nothing is ever removed autonomously.

- **Pro:** the enforcement point is the merge button — auditable, reversible, zero new trust granted to automation; fits the existing GitOps rule that all cluster changes go through declarative-config anyway.
- **Con:** adds human latency to every retirement (arguably a feature at this fleet size); a pile of unreviewed retirement PRs is possible.
- **Failure mode:** none for callers — worst case is a deprecated route living longer than needed.

## Option E — Caller-complete drain (needs Phase 7)

With per-worker tsnet identity, retire when **every identity that ever called v-old** has since either (a) successfully called v-new, or (b) disappeared from the tailnet (ephemeral tsnet nodes vanish on disconnect, so "worker gone" is a real signal, not an inference).

- **Pro:** the precise version of what the quiet window approximates — not "no traffic for a while" but "no *possible* caller remains." Fastest safe retirement: could be hours, not days.
- **Con:** needs per-(caller × route-version) bookkeeping and Phase 7 identity; "ever called" needs a bounded lookback so a worker from months ago doesn't hold retirement hostage.
- **Failure mode:** a long-lived caller that *should* migrate but hasn't blocks retirement indefinitely — visible and attributable (you know exactly which worker), so it converts a silent risk into a nameable nag.

## Recommended composition

- **Always D as the gate:** retirement is a PR a human merges, whatever triggers it. This is cheap insurance and matches the GitOps posture.
- **Window = B with A as its floor** (e.g. max(3× observed max gap, 7 days)) until Phase 7 exists.
- **C in the final week** before the PR merges, as the active smoke test.
- **Upgrade to E once Phase 7 lands** — the per-caller metric breakdown is already planned there; drain-tracking is a small step beyond it, and it shrinks retirement latency from weeks to days.
