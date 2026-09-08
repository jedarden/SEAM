#!/usr/bin/env bash
set -euo pipefail

# SEAM ready-frontier visibility audit
#
# Replaces the starvation signal lost with NEEDLE 865484e4 (2026-08-30, the
# "make starvation a terminal verdict" change removed the alert emitter) for
# THIS workspace, without resurrecting the emitter. The old emitter filed
# malformed alert beads ("Starvation alert: beads invisible in  " with a
# blank workspace and zero-count body — see bead seam-a26a360e and the 13
# stale beads that class accumulated), so this script is REPORT-ONLY:
#
#   It never creates, updates, closes, claims, or labels a bead. Every
#   `bead` invocation is funnelled through bead_ro(), which whitelists the
#   read-only subcommands, so the no-mutation constraint is enforced
#   mechanically rather than by discipline. Any action the report implies is
#   taken by a normally dispatched task that reads the report.
#
#   (Contrast scripts/starvation-watch.py in the ARMOR workspace, which
#   deliberately DOES file dedup-guarded alert beads. SEAM's dispatchers
#   re-fire on every unsettled bead, so an auto-emitted alert here becomes
#   a permanent open loop — that is exactly the stale bead class this
#   script replaces. Hence report-only.)
#
# Per run it:
#   1. Collects the full bead list (paging past the 100-row default cap with
#      --limit 999999) and the ready frontier.
#   2. Classifies every open-but-unclaimable bead using the schema pluck
#      already emits, with one cause per bead so the causes sum to the
#      open-vs-ready delta:
#        manually_blocked > has_assignee > has_dependencies >
#        resource_conflicts > unclassified
#      (all applicable flags are still recorded per bead).
#   3. Analyses the open-bead dependency graph: cycles (a deadlock that is
#      otherwise invisible) and chains rooted at a manually blocked bead
#      (the structural starvation shape found by the 2026-09-08 audit —
#      see .beads/diagnostics/SEAM-visibility-audit.json at commit 6357cf6).
#   4. Writes docs/notes/frontier-audit-<UTC>.md and overwrites the single
#      status line in docs/notes/frontier-audit-status.txt, and echoes the
#      status line to stdout (so journalctl keeps the history).
#   5. Prunes old reports, keeping the newest --keep (default 52) and never
#      deleting a git-tracked report.
#
# Side effect to know about: `bead list --ready` makes bead-rs rewrite its
# own .beads/diagnostics/pluck-diagnostics.json. That is a derived
# diagnostics artifact the CLI refreshes on every ready query (any dispatch
# does the same), not bead data; this script writes nothing under .beads/.
#
# Verdict: starved == (ready == 0 while open > 0) OR unclassified > 0 OR
# cycles > 0. A frontier with claimable work and a recorded cause for every
# invisible bead is NOT starved — that is the audit's terminal condition.
#
# Usage: scripts/frontier-audit.sh [--workspace DIR] [--keep N] [--quiet]
# Exit: 0 when a report was written (regardless of verdict — the report IS
#       the signal); non-zero on operational failure (missing bead CLI,
#       wrong backend, unreadable store), which surfaces as a failed
#       systemd service.

KEEP=52
QUIET=0
WORKSPACE_ARG=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --workspace) WORKSPACE_ARG="$2"; shift 2 ;;
    --keep)      KEEP="$2"; shift 2 ;;
    --quiet)     QUIET=1; shift ;;
    -h|--help)   sed -n '2,58p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $1 (see --help)" >&2; exit 64 ;;
  esac
done

# --- locate the workspace ------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
WORKSPACE="${WORKSPACE_ARG:-$REPO_ROOT}"
cd "$WORKSPACE"

# --- guards --------------------------------------------------------------
BEAD_BIN="${BEAD_BIN:-$(command -v bead || true)}"
if [[ -z "$BEAD_BIN" ]]; then
  for candidate in "$HOME/.local/bin/bead" "/home/coding/.local/bin/bead"; do
    [[ -x "$candidate" ]] && BEAD_BIN="$candidate" && break
  done
fi
[[ -n "$BEAD_BIN" ]] || { echo "frontier-audit: bead CLI not found" >&2; exit 69; }

# Bead-rs only (this repo's backend). A bf store (.beads/config.yaml, flat
# issues.jsonl) has a different schema — refuse rather than misread it.
# See CLAUDE.md "Beads": running the wrong CLI against a store silently
# corrupts the other tool's schema.
if [[ ! -f .beads/config.json ]]; then
  echo "frontier-audit: $WORKSPACE is not a bead-rs workspace (.beads/config.json missing)" >&2
  exit 65
fi
if [[ -f .beads/config.yaml ]]; then
  echo "frontier-audit: $WORKSPACE looks like a bf store (.beads/config.yaml present); refusing" >&2
  exit 65
fi

# Read-only enforcement: the ONLY bead subcommands this script may run.
bead_ro() {
  local sub="$1"; shift
  case "$sub" in
    list|show) "$BEAD_BIN" "$sub" "$@" ;;
    *)
      echo "frontier-audit: refusing non-read-only bead subcommand '$sub'" >&2
      exit 70
      ;;
  esac
}

# --- provenance ----------------------------------------------------------
NOW_COMPACT="$(date -u +%Y%m%dT%H%M%SZ)"
NOW_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
GIT_REV="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
GIT_DIRTY="$(git status --porcelain 2>/dev/null | wc -l | tr -d ' ' || echo unknown)"
BEAD_VERSION="$("$BEAD_BIN" --version 2>/dev/null | head -1 || echo unknown)"

# --- collect (read-only) -------------------------------------------------
TMPDIR_RUN="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_RUN"' EXIT

# --limit 999999: `bead list --json` silently caps at 100 rows.
bead_ro list --json --limit 999999 > "$TMPDIR_RUN/all.jsonl"
bead_ro list --ready --json --limit 999999 > "$TMPDIR_RUN/ready.jsonl"

# Phase 1: counts + which open beads are invisible. Emits one id per line.
python3 - "$TMPDIR_RUN" <<'PY'
import json, sys, pathlib
run = pathlib.Path(sys.argv[1])

def load(p):
    rows = []
    for line in p.read_text().splitlines():
        line = line.strip()
        if line:
            rows.append(json.loads(line))
    return rows

all_rows = load(run / "all.jsonl")
ready_rows = load(run / "ready.jsonl")

open_beads = [r for r in all_rows if r.get("status") == "open"]
in_prog    = [r for r in all_rows if r.get("status") == "in_progress"]
ready_ids  = {r["id"] for r in ready_rows}
invisible  = [r for r in open_beads if r["id"] not in ready_ids]

# Defensive: a ready row that is not status=open would break the delta math.
anomalies = [r["id"] for r in ready_rows if r.get("status") != "open"]

(run / "invisible.txt").write_text("".join(f"{i}\n" for i in sorted(r["id"] for r in invisible)))
(run / "summary.json").write_text(json.dumps({
    "total_beads": len(all_rows),
    "nonclosed": len(all_rows) - sum(1 for r in all_rows if r.get("status") == "closed"),
    "open": len(open_beads),
    "in_progress": len(in_prog),
    "ready": len(ready_rows),
    "ready_anomalies": anomalies,
    "delta": len(open_beads) - len(ready_rows),
}, indent=1))
PY

# Phase 2: live per-bead truth for every invisible bead (bead show --json is
# the only live record; list rows can lag a mutation in flight).
mkdir -p "$TMPDIR_RUN/show"
while IFS= read -r bead_id; do
  [[ -n "$bead_id" ]] || continue
  # A bead deleted between the list and here must not abort the run.
  if ! bead_ro show "$bead_id" --json > "$TMPDIR_RUN/show/$bead_id.json" 2>"$TMPDIR_RUN/show/$bead_id.err"; then
    : > "$TMPDIR_RUN/show/$bead_id.json"
  fi
done < "$TMPDIR_RUN/invisible.txt"

# --- compute + write the report ------------------------------------------
REPORT="docs/notes/frontier-audit-${NOW_COMPACT}.md"
STATUS_FILE="docs/notes/frontier-audit-status.txt"

cat > "$TMPDIR_RUN/audit.py" <<'PY'
import json, os, sys, pathlib

run, report, status_file, repo_root = (pathlib.Path(p) for p in sys.argv[1:5])
pluck_path = repo_root / ".beads" / "diagnostics" / "pluck-diagnostics.json"
meta = json.loads((run / "summary.json").read_text())
now_iso = os.environ["FA_NOW_ISO"]
git_rev, git_dirty, bead_version = (os.environ[k] for k in ("FA_REV", "FA_DIRTY", "FA_BEAD_VERSION"))

def load(p):
    rows = []
    for line in p.read_text().splitlines():
        line = line.strip()
        if line:
            rows.append(json.loads(line))
    return rows

all_rows = load(run / "all.jsonl")
ready_rows = load(run / "ready.jsonl")
by_id = {r["id"]: r for r in all_rows}
open_beads = sorted((r for r in all_rows if r.get("status") == "open"), key=lambda r: r["id"])
invisible_ids = [l.strip() for l in (run / "invisible.txt").read_text().splitlines() if l.strip()]

# pluck-diagnostics.json was rewritten by this run's own ready query, so it
# is same-run data: use it for resource_conflicts (not in `bead show`) and
# as an independent cross-check of the counts.
pluck = {}
pluck_note = "unavailable"
try:
    pd = json.loads(pluck_path.read_text())
    pluck = {e["bead_id"]: e for e in pd.get("excluded_beads", [])}
    pluck_note = f"timestamp {pd.get('timestamp')}"
except Exception as exc:  # missing or unparseable: degrade, never fail
    pluck_note = f"unavailable ({exc.__class__.__name__})"

# --- per-bead classification (one cause each; precedence documented) -----
def classify(bid):
    """Return (cause, flags, details) for one invisible bead."""
    path = run / "show" / f"{bid}.json"
    try:
        rec = json.loads(path.read_text())
        rec = rec[0] if isinstance(rec, list) and rec else None
    except Exception:
        rec = None
    if rec is None:
        # Vanished between list and show, or unreadable output: surface it,
        # never silently drop it.
        return "unclassified", [], {"error": f"bead show returned nothing ({path.stem}.err)"}

    row = by_id.get(bid, {})
    deps = rec.get("dependencies") or []
    blockers = [d.get("blocker") for d in deps if d.get("blocker")]
    statuses = {i: (by_id.get(i) or {}).get("status", "unknown") for i in blockers}
    open_blockers = sorted(i for i in blockers if statuses.get(i) != "closed")
    closed_blockers = sorted(i for i in blockers if statuses.get(i) == "closed")
    pe = pluck.get(bid, {})
    flags, details = [], {
        "assignee": rec.get("assignee"),
        "manual_blocked": bool(rec.get("manual_blocked")),
        "blockers": blockers,
        "open_blockers": open_blockers,
        "closed_blockers": closed_blockers,
        "conflict_count": pe.get("conflict_count", 0),
        "pluck_seen": bid in pluck,
    }
    if rec.get("manual_blocked"):
        flags.append("manually_blocked")
    if rec.get("assignee"):
        flags.append("has_assignee")
    if open_blockers:
        flags.append("has_dependencies")
    if pe.get("has_resource_conflicts"):
        flags.append("resource_conflicts")
    cause = flags[0] if flags else "unclassified"
    return cause, flags, details

# --- dependency graph over non-closed beads ------------------------------
nonclosed = [r for r in all_rows if r.get("status") != "closed"]
edges = {}  # id -> sorted list of non-closed blockers
for r in nonclosed:
    bl = sorted({d.get("blocker") for d in (r.get("dependencies") or [])
                 if d.get("blocker") and by_id.get(d["blocker"], {}).get("status") != "closed"})
    edges[r["id"]] = bl

# Roots and depths: memoised walk to a bead with no non-closed blockers.
memo = {}
def ancestors(bid, trail):
    if bid in memo:
        return memo[bid]
    if bid in trail:                 # cycle: stop, report it separately
        return (set(), True)
    bl = edges.get(bid, [])
    if not bl:
        memo[bid] = (set(), False)
        return memo[bid]
    roots, cyc = set(), False
    for b in bl:
        r, c = ancestors(b, trail | {bid})
        roots |= r if r else {b}
        cyc = cyc or c
    memo[bid] = (roots, cyc)
    return memo[bid]

# Tarjan SCC for cycle detection over the non-closed subgraph.
index, low, on, stack, counter, sccs = {}, {}, set(), [], [0], []
def strongconnect(v):
    index[v] = low[v] = counter[0]; counter[0] += 1
    stack.append(v); on.add(v)
    for w in edges.get(v, []):
        if w not in index:
            strongconnect(w); low[v] = min(low[v], low[w])
        elif w in on:
            low[v] = min(low[v], index[w])
    if low[v] == index[v]:
        comp = []
        while True:
            w = stack.pop(); on.discard(w); comp.append(w)
            if w == v:
                break
        if len(comp) > 1 or v in edges.get(v, []):
            sccs.append(sorted(comp))
for v in sorted(edges):
    if v not in index:
        strongconnect(v)
in_cycle = {m for scc in sccs for m in scc}

beads_out, counts = {}, {"has_assignee": 0, "manually_blocked": 0, "has_dependencies": 0,
                         "resource_conflicts": 0, "unclassified": 0}
for bid in invisible_ids:
    cause, flags, details = classify(bid)
    counts[cause] = counts.get(cause, 0) + 1
    roots, cyc_walk = ancestors(bid, set()) if bid in edges else (set(), False)
    roots = sorted(roots)
    blocked_roots = [r for r in roots if (by_id.get(r) or {}).get("manual_blocked")]
    chain = []
    cur, seen = bid, set()
    while cur and cur not in seen:
        seen.add(cur); chain.append(cur)
        nxt = edges.get(cur, [])
        cur = nxt[0] if len(nxt) == 1 else (nxt[0] if nxt else None)
        if len(seen) > 64:
            break
    beads_out[bid] = {
        "title": (by_id.get(bid) or {}).get("title", ""),
        "priority": (by_id.get(bid) or {}).get("priority"),
        "status": (by_id.get(bid) or {}).get("status"),
        "effective_status": (by_id.get(bid) or {}).get("effective_status"),
        "in_ready_frontier": False,
        "cause": cause,
        "all_flags": flags,
        "action": "recorded",   # this tool never mutates; a dispatched task acts
        "details": {**details,
                    "chain_roots": roots,
                    "root_manually_blocked": bool(blocked_roots),
                    "in_cycle": bid in in_cycle or cyc_walk,
                    "chain": chain},
    }

delta = meta["delta"]
unclassified = counts["unclassified"]
ready_count = meta["ready"]
open_count = meta["open"]
cycles = len(sccs)
starved = (ready_count == 0 and open_count > 0) or unclassified > 0 or cycles > 0

# Chains rooted at a manually blocked bead: the structural starvation shape.
blocked_roots = {}
for bid, b in beads_out.items():
    for r in b["details"]["chain_roots"]:
        if (by_id.get(r) or {}).get("manual_blocked"):
            blocked_roots.setdefault(r, set()).add(bid)

L = []
w = L.append
w(f"# SEAM ready-frontier visibility audit — {now_iso}")
w("")
w("Generated by `scripts/frontier-audit.sh` (report-only: it never creates,")
w("updates, or closes beads — any action implied here is taken by a normally")
w("dispatched task that reads this report). Replaces the starvation emitter")
w("removed in NEEDLE 865484e4.")
w("")
w(f"- measured at: git rev `{git_rev}` ({git_dirty} dirty/untracked files in the shared"
  f" checkout; the counts come from the bead store, not git)")
w(f"- bead CLI: {bead_version}; backend bead-rs")
w(f"- pluck diagnostics cross-check: {pluck_note}")
w("")
w("## Frontier health")
w("")
w("| metric | value |")
w("|---|---|")
w(f"| total beads | {meta['total_beads']} |")
w(f"| non-closed | {meta['nonclosed']} |")
w(f"| open | {open_count} |")
w(f"| in_progress | {meta['in_progress']} |")
w(f"| ready frontier | {ready_count} |")
w(f"| open-vs-ready delta (invisible) | {delta} |")
for k in ("manually_blocked", "has_assignee", "has_dependencies", "resource_conflicts", "unclassified"):
    w(f"| cause: {k} | {counts[k]} |")
w(f"| dependency cycles | {cycles} |")
w(f"| **starved** | **{'YES' if starved else 'no'}** |")
w("")
if meta["ready_anomalies"]:
    w(f"Anomaly: ready rows with a non-open status: {', '.join(sorted(meta['ready_anomalies']))}")
    w("")
w(f"Verdict: {'STARVED' if starved else 'NOT STARVED'} — "
  f"the frontier holds {ready_count} claimable bead(s); "
  f"{delta - unclassified}/{delta or 0} invisible bead(s) have a recorded cause, "
  f"{unclassified} unclassified, {cycles} cycle(s).")
w("")
w(f"Ready frontier: {', '.join(sorted(r['id'] for r in ready_rows)) or '(empty)'}")
w("")
w("## Classified invisible set")
w("")
if beads_out:
    w("| bead | pri | cause | detail |")
    w("|---|---|---|---|")
    for bid, b in beads_out.items():
        d = b["details"]
        if b["cause"] == "manually_blocked":
            detail = "manual_blocked=true"
        elif b["cause"] == "has_assignee":
            detail = f"assignee=`{d['assignee']}`"
        elif b["cause"] == "has_dependencies":
            detail = f"open blocker(s): {', '.join(f'`{x}`' for x in d['open_blockers'])}"
            if d["root_manually_blocked"]:
                detail += f"; chain root `{d['chain_roots'][0]}` is manually blocked"
        elif b["cause"] == "resource_conflicts":
            detail = f"resource conflicts: {d['conflict_count']}"
        else:
            detail = f"no pluck-compatible cause (flags: {b['all_flags'] or 'none'})"
        if d.get("in_cycle"):
            detail += "; **in dependency cycle**"
        w(f"| `{bid}` | {b['priority'] if b['priority'] is not None else ''} "
          f"| {b['cause']} | {detail} |")
    w("")
    w("Cause precedence when several flags apply: manually_blocked > has_assignee >")
    w("has_dependencies > resource_conflicts; every applicable flag is recorded in the")
    w("per-bead detail below the table.")
else:
    w("None — every open bead is on the ready frontier.")
    w("")

cycles_or_chains = False
if sccs:
    cycles_or_chains = True
    w("")
    w("## Dependency cycles")
    w("")
    for scc in sccs:
        w("- " + " → ".join(f"`{x}`" for x in scc) + " → … (deadlock: no member can become ready)")
    w("")
if blocked_roots:
    cycles_or_chains = True
    w("")
    w("## Chains rooted at a manually blocked bead")
    w("")
    w("These members are unclaimable solely because their chain root is manually")
    w("blocked; they release when the root closes or is unblocked.")
    w("")
    for root, members in sorted(blocked_roots.items()):
        w(f"- root `{root}` (manual_blocked) → {len(members)} member(s): "
          + ", ".join(f"`{m}`" for m in sorted(members)))
    w("")
if not cycles_or_chains:
    w("## Dependency cycles and blocked-root chains")
    w("")
    w("None found.")
    w("")

w("## Per-bead detail")
w("")
w("```json")
w(json.dumps(beads_out, indent=1, sort_keys=True))
w("```")
w("")
w("## Method")
w("")
w("```text")
w("bead list --json --limit 999999          # 100-row default cap bypassed")
w("bead list --ready --json --limit 999999  # the frontier; refreshes pluck-diagnostics.json")
w("bead show <id> --json                    # per invisible bead (live truth)")
w("```")
w("")
w(f"Cross-check vs pluck-diagnostics.json (same run, {pluck_note}): "
  f"pluck open={pd.get('total_open_beads') if pluck_note.startswith('timestamp') else 'n/a'} "
  f"candidates={pd.get('final_candidate_count') if pluck_note.startswith('timestamp') else 'n/a'} "
  f"vs this run open={open_count} ready={ready_count}.")
w("")
report.write_text("\n".join(L) + "\n")

status = (f"frontier-audit {now_iso} rev={git_rev} open={open_count} "
          f"in_progress={meta['in_progress']} ready={ready_count} invisible={delta} "
          f"unclassified={unclassified} cycles={cycles} "
          f"starved={'yes' if starved else 'no'} report={report}")
status_file.write_text(status + "\n")
print(status)
PY

FA_NOW_ISO="$NOW_ISO" FA_REV="$GIT_REV" FA_DIRTY="$GIT_DIRTY" FA_BEAD_VERSION="$BEAD_VERSION" \
  python3 "$TMPDIR_RUN/audit.py" "$TMPDIR_RUN" "$REPORT" "$STATUS_FILE" "$WORKSPACE"

# --- retention: keep the newest --keep reports, never a git-tracked one ---
if command -v git >/dev/null 2>&1; then
  tracked="$(git ls-files -- docs/notes 2>/dev/null | grep -E 'frontier-audit-[0-9]{8}T[0-9]{6}Z\.md$' || true)"
  n=0
  while IFS= read -r f; do
    [[ -n "$f" ]] || continue
    n=$((n + 1))
    if [[ "$n" -gt "$KEEP" ]]; then
      if [[ -n "$tracked" && $'\n'"$tracked"$'\n' == *$'\n'"$f"$'\n'* ]]; then
        continue   # tracked reports are evidence; leave them for git history
      fi
      rm -f "$f"
    fi
  done < <(ls -1t docs/notes/frontier-audit-*.md 2>/dev/null || true)
fi

[[ "$QUIET" -eq 1 ]] || echo "report: $WORKSPACE/$REPORT"
