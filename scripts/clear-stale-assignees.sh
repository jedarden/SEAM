#!/usr/bin/env bash
# SEAM stale-assignee sweep
#
# Clears assignees on assigned-but-open beads whose worker shows no liveness
# signal, returning them to the ready frontier.
#
# Why this exists
# ---------------
# A bead in status=open with a non-empty assignee is skipped by the dispatcher
# whenever a worker with that name is alive, and --count 1 workers relaunch
# under the same name forever, so the bead is never claimable. `bead show` and
# `bead doctor` both report the state as healthy (2026-08-16 fleet sweep: 583
# beads across 47 of 66 workspaces, ten workspaces fully starved). This is
# the action counterpart to scripts/frontier-audit.sh, which is deliberately
# REPORT-ONLY: frontier-audit classifies the has_assignee cause per bead, and
# a dispatched task reading the report — this script — takes the action.
#
# This supersedes the liveness half of scripts/bead-starvation-recovery.sh,
# which decides staleness from `ps aux` on the local host. A ps grep misses
# workers running on other hosts and any process whose argv does not match.
# This script instead reads the durable record the dispatcher itself consults:
#
#   .beads/heartbeats.jsonl   {worker, state, ts, last_strand}
#   .beads/events.jsonl       {worker, event, ts, strand, outcome, ...}
#
# A worker name is ALIVE if EITHER file has an entry for it within
# --stale-minutes (default 30). Otherwise every open bead carrying that name
# is stale and gets its assignee cleared.
#
# Safety properties
# -----------------
#   * status filter: only status==open beads are candidates. An in_progress
#     bead is live work and is never touched.
#   * re-verify before acting: each candidate is re-read via
#     `bead show <id> --json` immediately before its mutation; if the status
#     left open or the assignee changed since enumeration, the bead is
#     skipped instead of cleared.
#   * optimistic concurrency: the clear is
#       bead update <id> --clear-assignee --if-revision <rev>
#     with the revision read in the same pass. On a revision conflict
#     (bead-rs exits 4) the bead is re-read and retried once, then skipped.
#   * verify after acting: the bead is re-read after every mutation and the
#     report records what the store says afterwards, not just the exit code.
#   * --dry-run performs every read and liveness check but no mutation.
#
# bead-rs auto-publishes the checkpoint after every committed mutation
# (R026, 2026-08-21); a `bead sync flush-only` is still run once after any
# real mutation as belt-and-braces.
#
# Like frontier-audit.sh, exit 0 means "the sweep ran" — the report is the
# signal. Non-zero exit is reserved for operational failure (missing bead
# CLI, wrong backend, unreadable store).
#
# Usage: scripts/clear-stale-assignees.sh [--workspace DIR] [--stale-minutes N]
#                                         [--dry-run] [--report FILE] [--quiet]

set -euo pipefail

usage() {
    # Print the header comment block (everything after the shebang up to the
    # first non-comment line), with the "# " prefix stripped.
    awk 'NR > 1 && !/^#/ {exit} NR > 1 {print substr($0, 3)}' "$0"
}

STALE_MINUTES=30
DRY_RUN=0
QUIET=0
REPORT_FILE=""
WORKSPACE_ARG=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --workspace)     WORKSPACE_ARG="$2"; shift 2 ;;
        --stale-minutes) STALE_MINUTES="$2"; shift 2 ;;
        --dry-run)       DRY_RUN=1; shift ;;
        --report)        REPORT_FILE="$2"; shift 2 ;;
        --quiet)         QUIET=1; shift ;;
        -h|--help)       usage; exit 0 ;;
        *) echo "unknown argument: $1 (see --help)" >&2; exit 64 ;;
    esac
done

if ! [[ "$STALE_MINUTES" =~ ^[0-9]+$ ]] || [[ "$STALE_MINUTES" -eq 0 ]]; then
    echo "--stale-minutes must be a positive integer" >&2
    exit 64
fi

# --- locate the workspace ------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
WORKSPACE="${WORKSPACE_ARG:-$REPO_ROOT}"
cd "$WORKSPACE"

# --- guards --------------------------------------------------------------
BEAD_BIN="${BEAD_BIN:-$(command -v bead || true)}"
if [[ -z "$BEAD_BIN" ]]; then
    echo "bead CLI not found on PATH" >&2
    exit 1
fi
if [[ ! -f .beads/config.json ]]; then
    echo "$WORKSPACE is not a bead-rs workspace (no .beads/config.json)" >&2
    exit 1
fi

HEARTBEATS=".beads/heartbeats.jsonl"
EVENTS=".beads/events.jsonl"
for f in "$HEARTBEATS" "$EVENTS"; do
    if [[ ! -f "$f" ]]; then
        echo "warning: $f missing — it contributes no liveness signal" >&2
    fi
done

log() {
    if [[ "$QUIET" -eq 0 ]]; then
        echo "$@"
    fi
}

# --- clear one assignee, guarded and verified ----------------------------
# Prints an action string: cleared... | skipped... | failed...
clear_assignee() {
    local id="$1" expected_assignee="$2" rev="$3"
    local attempt out rc post

    for attempt in 1 2; do
        set +e
        out="$(bead update "$id" --clear-assignee --if-revision "$rev" 2>&1)"
        rc=$?
        set -e

        if [[ $rc -eq 0 ]]; then
            # Verify what the store actually says now.
            post="$(bead show "$id" --json \
                | jq -r '.[0] | (.assignee // "none") + " / " + .status')"
            if [[ "$post" == "none / open" ]]; then
                echo "cleared (rev $rev, verified)"
            else
                echo "cleared (rev $rev) but re-read shows $post"
            fi
            return 0
        fi

        if [[ $rc -eq 4 && "$attempt" -eq 1 ]]; then
            # Revision conflict: someone mutated the bead under us. Re-read
            # and retry once if it is still an assigned-open candidate.
            local info status_now assignee_now
            info="$(bead show "$id" --json \
                | jq -c '.[0] | {status, assignee: (.assignee // ""), revision}')"
            status_now="$(jq -r '.status' <<<"$info")"
            assignee_now="$(jq -r '.assignee' <<<"$info")"
            if [[ "$status_now" == "open" && "$assignee_now" == "$expected_assignee" ]]; then
                rev="$(jq -r '.revision' <<<"$info")"
                continue
            fi
            echo "skipped (rev conflict; bead now $status_now / ${assignee_now:-none})"
            return 0
        fi

        echo "failed (exit $rc: $(head -c 200 <<<"$out" | tr '\n' ' '))"
        return 0
    done
}

# --- enumerate assigned-but-open beads -----------------------------------
# bead list --json silently caps at 100 rows; pass --limit explicitly.
# TSV rows: id <tab> assignee <tab> title
CANDIDATES="$(bead list --json --limit 999999 \
    | jq -r 'select(.status == "open" and .assignee != null and .assignee != "")
             | [.id, .assignee, .title] | @tsv')"

if [[ -z "$CANDIDATES" ]]; then
    log "stale-assignee sweep: 0 assigned-but-open beads — nothing to do."
    exit 0
fi

# --- liveness ------------------------------------------------------------
# One pass over both JSONL files for the distinct assignee set. Data crosses
# by file path (never stdin, so the heredoc script owns stdin; never an
# output file, so nothing the script reads gets clobbered). Output TSV:
#   worker <tab> age_seconds|never <tab> latest_ts <tab> source
TMPDIR_LOCAL="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_LOCAL"' EXIT

awk -F'\t' '{print $2}' <<<"$CANDIDATES" | sort -u > "$TMPDIR_LOCAL/assignees"

python3 - "$TMPDIR_LOCAL/assignees" "$HEARTBEATS" "$EVENTS" <<'PY' \
    > "$TMPDIR_LOCAL/liveness"
import json
import re
import sys
import time
from datetime import datetime, timezone

assignees_file, heartbeats, events = sys.argv[1], sys.argv[2], sys.argv[3]

with open(assignees_file, encoding="utf-8") as f:
    assignees = {line.rstrip("\n") for line in f if line.strip()}

# RFC3339 with nanosecond precision; datetime.fromisoformat only accepts up
# to microseconds before 3.11, so truncate the fractional part.
TS_RE = re.compile(
    r"^(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2})(\.\d+)?"
    r"(Z|[+-]\d{2}:?\d{2})$"
)


def parse_ts(ts):
    m = TS_RE.match(ts.strip())
    if not m:
        raise ValueError(f"unparseable timestamp: {ts!r}")
    base, frac, off = m.groups()
    frac = (frac or ".0")[:7]          # keep at most 6 fractional digits
    if off == "Z":
        off = "+00:00"
    elif len(off) == 5:                # +0000 -> +00:00
        off = off[:3] + ":" + off[3:]
    dt = datetime.fromisoformat(base + frac + off)
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.timestamp()


latest = {}  # worker -> (epoch, ts_str, source)

for path, source in ((heartbeats, "heartbeats"), (events, "events")):
    try:
        f = open(path, encoding="utf-8", errors="replace")
    except OSError:
        continue
    with f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except ValueError:
                continue  # tolerate a torn trailing line
            worker = rec.get("worker")
            if worker not in assignees:
                continue
            ts = rec.get("ts")
            if not ts:
                continue
            try:
                epoch = parse_ts(ts)
            except (ValueError, TypeError):
                continue
            cur = latest.get(worker)
            if cur is None or epoch > cur[0]:
                latest[worker] = (epoch, ts, source)

now = time.time()
for worker in sorted(assignees):
    if worker in latest:
        epoch, ts, source = latest[worker]
        print(f"{worker}\t{now - epoch:.0f}\t{ts}\t{source}")
    else:
        print(f"{worker}\tnever\t-\t-")
PY

STALE_SECONDS=$((STALE_MINUTES * 60))

# --- report + act --------------------------------------------------------
if [[ -n "$REPORT_FILE" ]]; then
    : > "$REPORT_FILE"
fi

emit() {
    if [[ -n "$REPORT_FILE" ]]; then
        printf '%s\n' "$1" | tee -a "$REPORT_FILE"
    else
        printf '%s\n' "$1"
    fi
}

log "stale-assignee sweep of $WORKSPACE (stale = no heartbeat/event entry in ${STALE_MINUTES}m)"
if [[ "$DRY_RUN" -eq 1 ]]; then
    log "mode: DRY RUN — no beads will be mutated"
fi
log ""
emit "$(printf '%-18s %-38s %-14s %s' BEAD PRIOR_ASSIGNEE LIVENESS_AGE ACTION)"

CLEARED=0
WOULD_CLEAR=0
KEPT=0
SKIPPED=0
FAILED=0
MUTATED=0
CAND_TOTAL="$(awk 'END{print NR}' <<<"$CANDIDATES")"

while IFS=$'\t' read -r id assignee title; do
    # Reset per-iteration state — loop variables persist across iterations.
    age_h=""

    # liveness line: worker <tab> age|never <tab> ts <tab> source
    liveness="$(awk -F'\t' -v w="$assignee" '$1 == w {print; exit}' \
        "$TMPDIR_LOCAL/liveness")"
    age="$(awk -F'\t' '{print $2}' <<<"$liveness")"

    if [[ "$age" != "never" ]] && [[ "$age" -le "$STALE_SECONDS" ]]; then
        age_h="${age}s"
        action="kept (alive)"
        KEPT=$((KEPT + 1))
    else
        if [[ "$age" == "never" ]]; then
            age_h="never"
        else
            age_h="${age}s"
        fi

        # Re-read immediately before acting; the enumeration snapshot may be
        # stale by now.
        info="$(bead show "$id" --json \
            | jq -c '.[0] | {status, assignee: (.assignee // ""), revision}')"
        status_now="$(jq -r '.status' <<<"$info")"
        assignee_now="$(jq -r '.assignee' <<<"$info")"
        rev="$(jq -r '.revision' <<<"$info")"

        if [[ "$status_now" != "open" ]]; then
            action="skipped (status now $status_now)"
            SKIPPED=$((SKIPPED + 1))
        elif [[ "$assignee_now" != "$assignee" ]]; then
            action="skipped (assignee now ${assignee_now:-none})"
            SKIPPED=$((SKIPPED + 1))
        elif [[ "$DRY_RUN" -eq 1 ]]; then
            action="would-clear (rev $rev)"
            WOULD_CLEAR=$((WOULD_CLEAR + 1))
        else
            action="$(clear_assignee "$id" "$assignee" "$rev")"
            case "$action" in
                cleared*) CLEARED=$((CLEARED + 1)); MUTATED=$((MUTATED + 1)) ;;
                skipped*) SKIPPED=$((SKIPPED + 1)) ;;
                failed*)  FAILED=$((FAILED + 1)) ;;
            esac
        fi
    fi

    emit "$(printf '%-18s %-38s %-14s %s' "$id" "$assignee" "$age_h" "$action")"
done < <(printf '%s\n' "$CANDIDATES")

log ""
log "summary: candidates=$CAND_TOTAL cleared=$CLEARED would_clear=$WOULD_CLEAR kept_alive=$KEPT skipped=$SKIPPED failed=$FAILED"

if [[ "$MUTATED" -gt 0 ]]; then
    bead sync flush-only >/dev/null 2>&1 || true
    log "checkpoint flushed after $MUTATED mutation(s)"
fi

exit 0
