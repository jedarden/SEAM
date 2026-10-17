#!/usr/bin/env bash
set -euo pipefail

# Read-only health check for the live bead store and its published checkpoint.
# Recovery requires an operator to choose a verified generation (see the README).

verbose=false
while (($#)); do
  case "$1" in
    --verbose) verbose=true ;;
    *) echo "Usage: $0 [--verbose]" >&2; exit 2 ;;
  esac
  shift
done

# The host user service does not include Cargo's bin directory in PATH.
if ! command -v bead >/dev/null 2>&1; then
  export PATH="${HOME}/.cargo/bin:${PATH}"
fi

if ! command -v bead >/dev/null 2>&1; then
  echo "bead CLI not found" >&2
  exit 2
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "jq not found" >&2
  exit 2
fi
if [[ ! -f .beads/config.json ]]; then
  echo "not a bead-rs workspace: .beads/config.json is missing" >&2
  exit 2
fi

if ! status_json=$(bead sync status --format json); then
  echo "bead sync status failed; inspect the store before any recovery" >&2
  exit 1
fi
if ! jq -e 'type == "object" and
    (.checkpoint_present | type) == "boolean" and
    (.checkpoint_consistent | type) == "boolean" and
    (.relationship | type) == "string"' >/dev/null <<<"$status_json"; then
  echo "bead sync status returned unexpected JSON" >&2
  exit 1
fi

# Keep the CLI exit code as well as its structured verdict. Doctor emits one
# object containing a checks array, not JSONL records.
doctor_exit=0
doctor_json=$(bead doctor --json) || doctor_exit=$?
if ! jq -e 'type == "object" and
    (.checks | type) == "array" and
    (.has_errors | type) == "boolean" and
    (.has_warnings | type) == "boolean"' >/dev/null <<<"$doctor_json"; then
  echo "bead doctor returned unexpected JSON (exit $doctor_exit)" >&2
  exit 1
fi

if [[ "$verbose" == true ]]; then
  jq -r '"checkpoint: generation=\(.generation_id // "none") relationship=\(.relationship) consistent=\(.checkpoint_consistent)"' <<<"$status_json"
  jq -r '"doctor: checks=\(.checks | length) errors=\(.has_errors) warnings=\(.has_warnings)"' <<<"$doctor_json"
fi

if [[ "$doctor_exit" -ne 0 ]] ||
   [[ $(jq -r '.has_errors' <<<"$doctor_json") == true ]] ||
   [[ $(jq -r '.checkpoint_consistent' <<<"$status_json") != true ]] ||
   [[ $(jq -r '.relationship' <<<"$status_json") != aligned ]]; then
  echo "bead store or checkpoint needs operator inspection; no recovery was attempted" >&2
  exit 1
fi

echo "bead store and checkpoint are consistent"
