# Stale-assignee sweep — 2026-09-08 (bead `seam-ef880c84`)

**Proposal bead:** `seam-ef880c84` (child of `seam-a26a360e`)
**Remit:** clear the assignee on every open bead that (a) has no unresolved
blocking dependency, (b) carries an assignee whose worker name has no
`.beads/heartbeats.jsonl` entry in the trailing 24 h, and (c) whose revision has
not advanced in that window.
**Sweep time:** 2026-09-08T17:31Z–17:41Z. **Result: 0 beads cleared.**

## Why zero

The eligibility predicate is a conjunction of three conditions, and the first is
vacuously false across the whole open set:

| Class | Count | Beads |
|---|---|---|
| Open, **no assignee** | 17 | 4 ready-frontier + 3 manually blocked + 10 dependency-blocked |
| Assigned, open | **0** | — |

The only three assigned beads in the workspace are `in_progress` dispatches
whose revisions advanced minutes before the sweep — skipped per the
mid-dispatch stand-down guard, and ineligible on status anyway:

| Bead | Assignee | Rev | Last update |
|---|---|---|---|
| `seam-77cdf611` | `claude-code-glm-5.3-flash-glm-seam` | 2 | 17:12:56Z |
| `seam-ef880c84` | `claude-code-glm-5.3-flash-glm-spaxel2` (this dispatch) | 2 | 17:27:46Z |
| `seam-202735dc` | `claude-code-glm-5.3-flash-glm-trail` | 2 | 17:28:45Z |

No mutation satisfies the guards, so none was issued.

## Full open-set classification (17 beads)

Reconciles exactly: 4 + 3 + 10 = 17.

- **Ready frontier (4, unassigned + unblocked):** `seam-63dc8615`,
  `seam-9afca4bd`, `seam-8c59a4ae`, `seam-f2f4bbf0` (joined 06:39Z).
  Post-clear count = 4 — unchanged, because there was nothing to clear.
- **Manually blocked (3):** `seam-7358ce50` (rev 76), `seam-a2413e9d` (rev 4),
  `seam-80040f8e` (rev 22, updated 06:24Z).
- **Dependency-blocked (10):** members of the chain rooted at
  `seam-80040f8e` (`seam-46884014`, `seam-4bf77f64`, `seam-c27f3555`,
  `seam-7f22419c`, `seam-7fdee15d`, `seam-9c80a071`, `seam-a882fe8a`,
  `seam-d7aac6fe`) and the chain rooted at `seam-8c59a4ae`
  (`seam-71ec8a57`, `seam-ab455030`). Unassigned-and-blocked is the correct
  resting state; never touched.

## Corroboration and caveats

- The committed visibility audit
  (`.beads/diagnostics/SEAM-visibility-audit.json`, commit `6357cf6`,
  generated 06:30Z — ~11 h earlier) independently reports `has_assignee: 0`
  and `mechanical_fixes_applicable: 0`. This sweep reproduces both at a
  17:31Z snapshot.
- **Heartbeat liveness is not a safe standalone signal here.**
  `.beads/heartbeats.jsonl`'s newest entry is `glm-seam` at
  2026-09-08T01:19:10Z; the actively-running workers named above have **zero**
  heartbeat entries, so heartbeat-only liveness would misclassify live peers as
  dead. The revision/updated_at ≥ 24 h staleness guard is the operative
  protection and is what was applied.
- This is consistent with the standing finding that "Starvation alert: beads
  invisible in …" beads are stale NEEDLE emissions of a malformed pre-`865484e4`
  pluck output whose emitter was removed upstream — close, don't build a fix
  chain.

## Method

```bash
bead list --json --limit 999999 | grep '^{' | python3 …   # enumerate, classify
bead show <id> --json                                      # revision field: "revision" (array of 1)
bead list --ready --json | grep '^{' | python3 …           # ready frontier
```

`grep '^{'` is required: `bead list` prints a non-JSON diagnostics line to
stdout. `--limit 999999` is required: `bead list` silently caps at 100 rows.
