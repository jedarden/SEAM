#!/usr/bin/env bash
# seam-ci gate status
#
# One check for "is SEAM's CI gate red?". seam-ci runs on iad-ci for every push
# to main; when its latest run for the pushed revision Failed, work on this
# repo halts (see AGENTS.md, "The seam-ci gate") instead of landing more
# commits on top of a known-broken tree -- which is exactly what happened
# 2026-08-27..31, when 25+ commits landed while verify was red.
#
# Usage: scripts/ci-gate.sh [--revision <sha>]      (default: origin/main)
#
# Exit codes:
#   0  green   -- the latest seam-ci run for <revision> Succeeded
#   1  red     -- the latest seam-ci run for <revision> Failed; stand down
#   2  error   -- kubectl missing, cluster unreachable, or unparsable output
#   3  pending -- no completed run for <revision> yet (push not tested, or in flight)
#
# A red gate is a stop signal, not a nuisance: it means the tree at main does
# not build or fails its own Definition of Done, so any work started now
# builds on sand. Fix the gate (or revert what broke it) before claiming more
# work. `--no-verify` skips the pre-commit backstop but never this script.

set -euo pipefail

KUBECTL_SERVER="${SEAM_CI_KUBECTL_SERVER:-http://traefik-iad-ci:8001}"
NAMESPACE="${SEAM_CI_NAMESPACE:-argo-workflows}"
WORKFLOW_PREFIX="seam-ci-"

REVISION=""
if [[ "${1:-}" == "--revision" && -n "${2:-}" ]]; then
  REVISION="$2"
fi

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd -P)"
cd "$repo_root"

# The gate is a property of main, not of a working copy: CI runs on what was
# pushed, so ask the remote where main is rather than trusting a possibly
# stale local ref.
if [[ -z "$REVISION" ]]; then
  if REVISION="$(git ls-remote origin refs/heads/main 2>/dev/null | cut -f1)" && [[ -n "$REVISION" ]]; then
    :
  else
    # Offline from Forgejo: fall back to the last fetched ref.
    REVISION="$(git rev-parse origin/main 2>/dev/null || true)"
  fi
fi
if [[ -z "$REVISION" ]]; then
  echo "GATE error revision=unknown (cannot resolve origin/main)"
  exit 2
fi

if ! command -v kubectl >/dev/null 2>&1; then
  echo "GATE error revision=${REVISION:0:9} (kubectl not installed)"
  exit 2
fi

# seam-ci runs carry the events.argoproj.io/trigger=seam-ci label set by the
# Argo Events sensor, which selects them exactly; the revision they gate on is
# the webhook payload's revision, recorded in the workflow's arguments.
runs_json="$(kubectl --server="$KUBECTL_SERVER" get workflows -n "$NAMESPACE" \
  -l events.argoproj.io/trigger=seam-ci -o json 2>/dev/null)" || {
  echo "GATE error revision=${REVISION:0:9} (iad-ci unreachable via $KUBECTL_SERVER)"
  exit 2
}

# `if !` rather than a bare assignment: under `set -e` a failing command
# substitution aborts the script with the substitution's own exit code, which
# here would be the parser's 1 -- indistinguishable from a red gate. Capture
# inside the conditional so a dead parser can be reported as exit 2 instead.
if ! status="$(printf '%s' "$runs_json" | REVISION="$REVISION" python3 -c '
import json, sys, os

rev = os.environ["REVISION"]

# Any payload this cannot confidently judge must land on "error", never "red".
# Under `set -e` an uncaught exception here would exit 1, and both
# ci-gate-watch and .githooks/pre-commit read exit 1 as "block work" -- so a
# mangled kubectl response would halt every SEAM bead and log a traceback
# nobody could act on. Exit 2 (hold) is the documented answer for "I could
# not tell". The guard covers the parse *and* the shape: valid JSON whose
# `items` is not a list, or whose items are not mappings, is exactly as
# undecidable as JSON that does not parse at all.
try:
    items = json.load(sys.stdin).get("items", [])
    runs = []
    for wf in items:
        params = (wf.get("spec", {}).get("arguments", {}) or {}).get("parameters", []) or []
        run_rev = next((p.get("value", "") for p in params if p.get("name") == "revision"), "")
        if run_rev != rev:
            continue
        runs.append((
            (wf.get("metadata") or {}).get("creationTimestamp", ""),
            (wf.get("metadata") or {}).get("name", "?"),
            (wf.get("status") or {}).get("phase", "Unknown"),
        ))
except Exception:
    print("error|none|workflow list from kubectl was unparsable or had an unexpected shape")
    sys.exit(0)

if not runs:
    print("pending|none|no seam-ci run for this revision yet")
else:
    created, name, phase = max(runs)
    print(phase.lower() + "|" + name + "|" + phase)
')" ; then
  echo "GATE error revision=${REVISION:0:9} workflow=none (gate parser failed)"
  exit 2
fi

# Net for anything the parser's own guard did not anticipate: a parser that
# died must read as "could not tell" (exit 2), never as an empty status that
# could be misread downstream as a verdict.
if [[ -z "$status" ]]; then
  echo "GATE error revision=${REVISION:0:9} workflow=none (gate parser produced no verdict)"
  exit 2
fi

phase="${status%%|*}"
rest="${status#*|}"
workflow="${rest%%|*}"
detail="${rest#*|}"

case "$phase" in
  succeeded)
    echo "GATE green revision=${REVISION:0:9} workflow=$workflow phase=Succeeded"
    exit 0
    ;;
  failed)
    echo "GATE red revision=${REVISION:0:9} workflow=$workflow phase=Failed"
    echo "  Latest verify failed on the tree at main. Do not claim SEAM beads or"
    echo "  commit on top of it -- reproduce with: scripts/definition-of-done.sh --all"
    echo "  Override (records a bypass): SEAM_ALLOW_RED_GATE=1"
    exit 1
    ;;
  running|pending)
    echo "GATE pending revision=${REVISION:0:9} workflow=$workflow phase=$detail"
    exit 3
    ;;
  none)
    echo "GATE pending revision=${REVISION:0:9} workflow=none ($detail)"
    exit 3
    ;;
  *)
    # error, or a phase this script does not know
    echo "GATE error revision=${REVISION:0:9} workflow=$workflow phase=$detail"
    exit 2
    ;;
esac
