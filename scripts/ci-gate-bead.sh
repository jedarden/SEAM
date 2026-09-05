#!/usr/bin/env bash
# Open or close the bead-level seam-ci red gate.
#
# `bead list --ready` excludes beads with an open blocker, so one open "gate"
# bead, wired as a blocker of every ready bead, empties the claim frontier.
# This is what "a red seam-ci halts work" means at the bead level: there is
# nothing left to claim except the gate itself. See AGENTS.md,
# "The seam-ci gate".
#
# Usage:
#   scripts/ci-gate-bead.sh open     # gate is red: block the ready frontier
#   scripts/ci-gate-bead.sh close    # gate is green: release the frontier
#   scripts/ci-gate-bead.sh status   # report gate + frontier state
#
# `open` is idempotent -- re-run it after creating beads while the gate is red
# so they pick up the blocker edge too. `close` refuses while ci-gate.sh still
# reports red; the frontier is not released by hand.

set -euo pipefail

GATE_TITLE='GATE: seam-ci is red - do not claim SEAM beads'
GATE_REF='seam:ci-red-gate'
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export GATE_TITLE

# Print the gate bead's id, or nothing if it does not exist.
gate_id() {
  bead list --limit 999999 --json 2>/dev/null | python3 -c '
import json, os, sys
want = os.environ["GATE_TITLE"]
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        b = json.loads(line)
    except ValueError:
        continue
    if b.get("title") == want:
        print(b["id"])
        break
'
}

gate_status_value() {
  # status of the gate bead, or "missing"
  local id
  id="$(gate_id || true)"
  if [[ -z "${id:-}" ]]; then
    echo "missing"
    return
  fi
  bead show "$id" --json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["status"])'
}

ready_beads() {
  # "<id>\t<is-gate-bead:0|1>" per ready bead. Note `--ready` already excludes
  # blocked beads, so anything listed here has no blocker edge at all yet.
  bead list --ready --limit 999999 --json 2>/dev/null | python3 -c '
import json, os, sys
want = os.environ["GATE_TITLE"]
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        b = json.loads(line)
    except ValueError:
        continue
    print("{}\t{}".format(b["id"], 1 if b.get("title") == want else 0))
' 2>/dev/null || true
}

case "${1:-status}" in
  open)
    id="$(gate_id || true)"
    if [[ -z "${id:-}" ]]; then
      id="$(bead create --title "$GATE_TITLE" --unique-ref "$GATE_REF" --priority 0 \
        --description 'Opened by scripts/ci-gate-bead.sh because the seam-ci gate is red. This bead blocks every ready bead, so there is nothing to claim while the tree at main does not pass verify. Do not claim or close it by hand: scripts/ci-gate-bead.sh close releases the frontier, and it refuses while scripts/ci-gate.sh still reports red. See AGENTS.md, "The seam-ci gate".')"
      echo "created gate bead $id"
    fi

    case "$(gate_status_value)" in
      closed)
        bead reopen "$id" >/dev/null
        echo "reopened gate bead $id"
        ;;
      open) : ;;
      *) echo "gate bead $id is $(gate_status_value) -- leaving status alone" ;;
    esac

    # The gate bead must never be claimable: as a plain open P0 it was claimed
    # by a fleet worker within seconds of creation. Deferred keeps it out of
    # the ready frontier entirely while its blocker edges stay live -- an edge
    # stops blocking only when the blocker CLOSES, not when it goes quiet.
    if [[ "$(gate_status_value)" != "deferred" ]]; then
      bead update "$id" --status deferred --notes \
        'Deferred on purpose: this is the red-gate marker, not a work item. It must never sit in the ready frontier, or a worker claims (and may close) the very bead that is holding the frontier shut.' \
        >/dev/null || echo "  (could not defer the gate bead -- it may be claimable)" >&2
    fi

    wired=0
    while IFS=$'\t' read -r bid is_gate; do
      [[ -z "${bid:-}" ]] && continue
      [[ "$is_gate" == "1" ]] && continue
      if bead dep add "$bid" "$id" >/dev/null; then
        wired=$((wired + 1))
        echo "  blocked $bid"
      else
        echo "  (could not block $bid -- skipped)" >&2
      fi
    done < <(ready_beads)

    remaining="$(ready_beads | awk -F'\t' '$2 == 0' | wc -l)"
    echo "gate bead $id open; $wired ready bead(s) newly blocked; $remaining non-gate bead(s) still ready"
    ;;

  close)
    if ! "$SCRIPT_DIR/ci-gate.sh"; then
      echo "refusing to close the gate bead: scripts/ci-gate.sh does not report green" >&2
      exit 1
    fi
    id="$(gate_id || true)"
    if [[ -z "${id:-}" ]]; then
      echo "no gate bead to close"
      exit 0
    fi
    if [[ "$(gate_status_value)" == "closed" ]]; then
      echo "gate bead $id already closed"
      exit 0
    fi
    if [[ "$(gate_status_value)" == "deferred" ]]; then
      # deferred keeps the gate bead out of the frontier; closing needs it
      # reachable from open first
      bead update "$id" --status open >/dev/null
    fi
    bead close "$id" --reason "seam-ci Succeeded for the tree at main; frontier released by scripts/ci-gate-bead.sh close"
    echo "closed gate bead $id"
    ;;

  status)
    "$SCRIPT_DIR/ci-gate.sh" || true
    id="$(gate_id || true)"
    if [[ -z "${id:-}" ]]; then
      echo "gate bead: none exists"
    else
      echo "gate bead: $id status=$(gate_status_value)"
    fi
    echo "ready frontier: $(ready_beads | awk -F'\t' '$2 == 0' | wc -l) non-gate bead(s)"
    ;;

  *)
    echo "usage: $0 {open|close|status}" >&2
    exit 2
    ;;
esac
