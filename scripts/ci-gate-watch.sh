#!/usr/bin/env bash
# Reconcile the bead-store gate with live seam-ci state, unattended.
#
# scripts/ci-gate.sh answers "is the gate red?" and scripts/ci-gate-bead.sh
# opens/closes the gate bead that empties the claim frontier -- but both only
# act when someone runs them. That is the gap this closes: a red gate that
# nobody notices is exactly how 25+ commits landed on a broken tree between
# 2026-08-27 and 08-31. This script is the loop that notices. Run it from
# tools/ci-gate-watch.timer (every 5 minutes) -- see
# tools/install_ci_gate_watch.sh -- though it is safe to run by hand too.
#
# Mapping (ci-gate.sh exit -> action):
#   0 green    -> ci-gate-bead.sh close   (release the frontier)
#   1 red      -> ci-gate-bead.sh open    (empty the frontier)
#   3 pending  -> hold: a run is in flight or untested; the last completed
#                 verdict stands until a new one lands, so a push never
#                 flaps the frontier open and shut while verify runs
#   2 error    -> hold: never block or release work because the cluster is
#                 unreachable; leave the frontier as it was
#
# ci-gate-bead.sh close re-checks ci-gate.sh itself and refuses when it does
# not report green, so a race between this script's check and its close still
# fails safe.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATE_DIR="${SEAM_CI_GATE_STATE_DIR:-$HOME/.local/state/seam-ci-gate}"
LOG_FILE="$STATE_DIR/watch.log"
# Outside PrivateTmp so a manual run and the timer's service run contend on
# the same lock instead of silently serializing against different files.
LOCK_FILE="$STATE_DIR/watch.lock"

mkdir -p "$STATE_DIR"

log() {
    echo "[$(date -Iseconds)] $*" >> "$LOG_FILE"
}

# One instance at a time: the timer and a manual run must not interleave two
# open/close passes against the same bead store.
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
    log "skip: another watch pass is already running"
    exit 0
fi

set +e
"$SCRIPT_DIR/ci-gate.sh" > "$STATE_DIR/last-check.txt" 2>&1
gate_rc=$?
set -e

case "$gate_rc" in
    0)
        if "$SCRIPT_DIR/ci-gate-bead.sh" close >> "$LOG_FILE" 2>&1; then
            log "green: frontier release attempted ($(head -1 "$STATE_DIR/last-check.txt"))"
        else
            log "green: close failed -- see above; frontier left as it was"
        fi
        ;;
    1)
        if "$SCRIPT_DIR/ci-gate-bead.sh" open >> "$LOG_FILE" 2>&1; then
            log "red: frontier blocked ($(head -1 "$STATE_DIR/last-check.txt"))"
        else
            log "red: open failed -- beads remained claimable; will retry next pass"
        fi
        ;;
    3)
        # Hold the previous verdict. Nothing to do, nothing to log on every
        # pass -- a pending gate is the normal state for the ~15 minutes a
        # run is in flight.
        :
        ;;
    *)
        log "hold: ci-gate.sh exit $gate_rc ($(head -1 "$STATE_DIR/last-check.txt"))"
        ;;
esac
