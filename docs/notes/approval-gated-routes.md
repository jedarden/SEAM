# Future Feature: Approval-Gated Routes

Status: accepted as a future feature 2026-07-16 — deliberately **not** in `docs/plan/plan.md`'s phases yet.

## What

A route fragment may declare `x-requires-approval: true`. SEAM holds such a request instead of proxying it, pushes the fully rendered call (route, params, body, caller identity — never the secret) to a human approval channel, and only injects + proxies on an explicit approve. The calling agent receives a structured 202 with an approval ID and polls **`/_seam/approvals/{id}`** (pending / approved+result / denied / expired). **The endpoint moved under `/_seam/` on 2026-07-20** — it was written here as a bare `/approvals/{id}`, but `docs/plan/plan.md`'s reserved control-plane namespace closes the grandfathered top-level enumeration as of that date, and every control-plane endpoint conceived afterwards lives under `/_seam/` with no exceptions. The bare `/approvals/` prefix stays reserved anyway, as a squat guard rather than a served surface: a fragment occupying the obvious name for an approval verdict would be a forgeable-approval surface, so denying the near-miss name closes the hole in both directions.

## Why

Nearly the entire cluster estate is exposed read-only today, largely because there is no guardrail that would make agent-initiated writes safe. Approval gating is the sudo prompt for the fleet: it changes what agents are *allowed* to do, not just how pleasantly they do it.

## Mechanism sketch

- Hold queue with a per-route timeout; expiry is a default-deny.
- Approval channel: push notification via the existing telegram-claude-bridge (or ntfy) with approve/deny actions.
- The approval prompt must name the requesting agent — which requires Phase 7 per-worker identity; without it every request reads as "someone in the cluster."
- Every approval and denial is audit-logged: who asked, who decided, exactly what was sent.

## Dependencies

- Phase 7 (per-agent identity) — a meaningful approval prompt needs to say who is asking.
- Phase 2 (secret injection) — the held request is injected only after approval.

## Open questions

- Approval TTL, and whether one approval can cover a short batch of related calls vs. strictly one request.
- Per-scope auto-approve (e.g. a trusted agent class skips the prompt on certain routes)?
- Where does the held request body live while pending (memory only vs. persisted), and what does that imply for gateway restarts mid-approval?
