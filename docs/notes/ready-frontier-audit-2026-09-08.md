# Ready-frontier audit — 2026-09-08

**Bead:** seam-77cdf611 (unravel alternative for seam-a26a360e, "Starvation alert: beads
invisible in"). **Measured:** 2026-09-08T17:23–17:41Z. **HEAD pinned:**
`6357cf6bb404bec196ee727db61d44de21640ad7`. All inputs are read-only `bead` commands
plus repo files; the working tree was dirty (906 entries from concurrent workers) but no
input derives from the tree, so the dirt does not touch these numbers.

## Verdict

**The alert's premise is false.** The unexplained set is empty (0 of 13). Every
open-but-unclaimable bead has a machine-verified, live-checked reason, and the frontier
is offering 6 claimable beads right now. There is no starvation to repair.

## Premise re-derivation (live, not from the alert body)

The alert body self-contradicts ("Open beads: 0" while claiming open beads exist), so
every number below was re-derived:

| Quantity | Value | Evidence command | Cap check |
|---|---|---|---|
| Open beads | **19** | `bead list --status open --json --limit 500` | 19 rows at `--limit 500`, identical at the default limit — the 100-row cap does not bind (19 < 100) |
| Claimable (ready) | **6** | `bead list --ready --json --limit 500` | same check |
| Open-but-unclaimable (open − ready) | **13** | set difference of the two JSONL outputs | — |

Independent cross-check: the frontier's own accounting, written to
`.beads/diagnostics/pluck-diagnostics.json` at `2026-09-08T17:23:41Z` as a side effect of
the audit's own `bead list --ready` call, reports `total_open_beads: 19`,
`exclusion_criteria: {has_assignee: 0, manually_blocked: 3, has_dependencies: 10,
resource_conflicts: 0}`, `final_candidate_count: 6` — matching the derivation above on
every axis.

The 6 claimable beads right now: seam-63dc8615, seam-9afca4bd, seam-8c59a4ae,
seam-f2f4bbf0, seam-ef880c84, seam-202735dc.

## Classification rule (mechanical)

The primary cause for each bead is the frontier's own machine-recorded exclusion reason
(`pluck-diagnostics.json`, generated during this audit), mapped onto the bead's (a)–(d)
taxonomy:

- excluded for `has_dependencies` → **(a)** unresolved blocking dependency — verified
  per-edge against live blocker status, not assumed;
- excluded for `manually_blocked` → **(a)** disposition (blocked, correct resting state,
  no action) with the mechanism recorded as an operator manual block. The bead's taxonomy
  omits this mechanism; it is called out explicitly rather than folded silently;
- **(b)** requires an assignee — `has_assignee: 0` across all 19 open beads, so (b) is
  empty by construction;
- **(c)** strike labels were tested and rejected as a cause: the ready set *contains*
  beads carrying `deferred`, `failure-count:*` and an expired
  `quarantine-until:2026-09-08T06:44` label (seam-63dc8615, seam-9afca4bd, seam-8c59a4ae,
  seam-f2f4bbf0) and they are claimable. Strike labels do not gate this frontier;
- **(d)** whatever remains — nothing remains.

## The 13 open-but-unclaimable beads

Chain A — 9 beads in a single linear chain rooted at the quarantined seam-80040f8e
(no cycles; every edge verified live):

| Bead | Cause | Mechanism (evidence) | Evidence command / excerpt |
|---|---|---|---|
| seam-80040f8e | (a) manual-block | `manual_blocked: true` (operator quarantine), root of chain A | `bead show seam-80040f8e --json` → `status=open effective=blocked mb=True labels=['cycling','deferred','failure-count:8','quarantine: false-close-detected-after-8-tries','umbrella','verification-failed']` |
| seam-a882fe8a | (a) dependency | blocked by seam-80040f8e (open) | `bead show seam-a882fe8a --json` → `dependencies: [{blocker: seam-80040f8e, kind: blocks}]`; blocker live `status=open` |
| seam-7fdee15d | (a) dependency | blocked by seam-a882fe8a (open) | edge `seam-7fdee15d --blocked-by--> seam-a882fe8a`; blocker live `status=open` |
| seam-9c80a071 | (a) dependency | blocked by seam-7fdee15d (open) | edge `seam-9c80a071 --blocked-by--> seam-7fdee15d`; blocker live `status=open` |
| seam-d7aac6fe | (a) dependency | blocked by seam-9c80a071 (open) | edge `seam-d7aac6fe --blocked-by--> seam-9c80a071`; blocker live `status=open` |
| seam-c27f3555 | (a) dependency | blocked by seam-d7aac6fe (open) | edge `seam-c27f3555 --blocked-by--> seam-d7aac6fe`; blocker live `status=open` |
| seam-7f22419c | (a) dependency | blocked by seam-c27f3555 (open) | edge `seam-7f22419c --blocked-by--> seam-c27f3555`; blocker live `status=open` |
| seam-46884014 | (a) dependency | blocked by seam-7f22419c (open) | edge `seam-46884014 --blocked-by--> seam-7f22419c`; blocker live `status=open` |
| seam-4bf77f64 | (a) dependency | blocked by seam-46884014 (open) | edge `seam-4bf77f64 --blocked-by--> seam-46884014`; blocker live `status=open` |

Chain B — 2 beads, pending on work that is claimable *today* (the chain root is in the
ready set, so this is ordinary pipeline depth, not starvation):

| Bead | Cause | Mechanism (evidence) | Evidence command / excerpt |
|---|---|---|---|
| seam-ab455030 | (a) dependency | blocked by seam-8c59a4ae — which is **open and ready** | edge `seam-ab455030 --blocked-by--> seam-8c59a4ae`; blocker live `status=open` and present in the `--ready` output |
| seam-71ec8a57 | (a) dependency | blocked by seam-ab455030 (open) | edge `seam-71ec8a57 --blocked-by--> seam-ab455030`; blocker live `status=open` |

Operator-manual-blocked (no dependency edges at all):

| Bead | Cause | Mechanism (evidence) | Evidence command / excerpt |
|---|---|---|---|
| seam-7358ce50 | (a)-disposition, manual-block | `manual_blocked: true` — operator-gated (consolidated-prefix data move is operator-only) | `bead show seam-7358ce50 --json` → `status=open effective=blocked mb=True pri=1 labels=['deferred','openbao','seam-consolidation']`; frontier excludes it as `manually_blocked` |
| seam-a2413e9d | (a)-disposition, manual-block | `manual_blocked: true`, no deps, no strike labels — blocked by operator action alone | `bead show seam-a2413e9d --json` → `status=open effective=blocked mb=True pri=2 labels=['seam-consolidation']`; frontier excludes it as `manually_blocked` |

**Totals: (a) 13 [10 dependency-gated + 3 operator-manual-blocked] · (b) 0 · (c) 0 ·
(d) 0.**

## Secondary observations (recorded, not acted on)

- **Heartbeats:** `.beads/heartbeats.jsonl` exists (60,978 bytes), last entry
  `2026-09-08T01:19:10Z` (worker `glm-seam`, state `idle`) — roughly 16 h before this
  audit, despite workers being active now. The (b) mechanism is vacuous today
  (`has_assignee: 0`), but if an assigned-but-open bead appeared, a 16 h heartbeat gap
  would flag every assignee as stale. Worth keeping the heartbeat emitter in mind when
  reading any future (b)-classification.
- **Quarantine expiry:** seam-f2f4bbf0's `quarantine-until:2026-09-08T06:44` has passed
  and the bead is already back in the ready set — quarantine release works.
- **Relation to the prior audit:** seam-466ac8ae (closed, peer dispatch) measured 16 open
  / 3 ready / 13 delta at 02:34Z today and shipped
  `.beads/diagnostics/SEAM-visibility-audit.json`. Sixteen hours later the open and ready
  counts both moved (+3 / +3) while the delta stayed 13 with the same 3-way split — the
  unclaimable population is stable and explained, not drifting into starvation. Three of
  today's ready beads are sibling unravel proposals off the same alert
  (seam-f2f4bbf0, seam-ef880c84, seam-202735dc).
- **Chain-A root is the structural hinge:** the 9-bead chain starves only until
  seam-80040f8e is closed or its quarantine is lifted by an operator. That is the same
  finding seam-466ac8ae recorded; it remains the single highest-leverage unlock, and it
  is operator-bound, not agent-actionable.

## Decision-rule outcome

The bead's rule: *empty unexplained set ⇒ the alert's premise is false, close it citing
the audit.* Unexplained set = ∅. Nothing is handed to a repair task, because no bead in
the delta lacks an explanation. The stale-claim repair sibling (seam-ef880c84) has no
live subject today — zero assigned-but-open beads exist — and should idle unless the
heartbeat-gap observation above changes the picture.
