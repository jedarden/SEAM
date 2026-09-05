#!/usr/bin/env bash
# Unified Definition of Done for SEAM
#
# This script is the single source of truth for "is this work acceptable?"
# It is invoked identically by:
#   - Pre-commit hook (fast lane only, with --count-bypass)
#   - CI verify step (both fast and slow lanes)
#   - NEEDLE validation gate (fast lane only)
#
# Lanes:
#   - Fast: gofmt, go vet, golangci-lint (seconds, run locally)
#   - Slow: go test -race, seam lint, benchmark gate (requires more time)
#
# Behavior: Aggregates all failures rather than aborting on first.
# Returns non-zero if ANY check fails, with all failures reported.
#
# Usage:
#   scripts/definition-of-done.sh [--fast|--slow|--all] [--count-bypass]

set -euo pipefail

# Script directory for path resolution
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

# Default to fast lane
LANE="fast"
COUNT_BYPASS=false

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --fast)
      LANE="fast"
      shift
      ;;
    --slow)
      LANE="slow"
      shift
      ;;
    --all)
      LANE="all"
      shift
      ;;
    --count-bypass)
      COUNT_BYPASS=true
      shift
      ;;
    *)
      echo "Error: Unknown argument: $1" >&2
      echo "Usage: $0 [--fast|--slow|--all] [--count-bypass]" >&2
      exit 1
      ;;
  esac
done

# Check for Go installation
if ! command -v go >/dev/null 2>&1; then
  echo "Error: go is required but not installed" >&2
  exit 1
fi

# Bypass counting
BYPASS_LOG="${REPO_ROOT}/.beads/bypasses.jsonl"
if [[ "$COUNT_BYPASS" == "true" ]]; then
  mkdir -p "$(dirname "$BYPASS_LOG")"
  echo "{\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"lane\":\"$LANE\",\"pwd\":\"$(pwd -P)\"}" >> "$BYPASS_LOG"
fi

# Failure tracking
declare -a FAILURES=()
declare -a CHECKS=()

# Helper to run a check and record failure
run_check() {
  local name="$1"
  shift
  CHECKS+=("$name")

  echo "Running: $name..."

  if output=$("$@" 2>&1); then
    echo "✓ $name passed"
    return 0
  else
    local exit_code=$?
    echo "✗ $name failed (exit code: $exit_code)"
    FAILURES+=("$name: exit code $exit_code")
    echo "Failure details for $name (last 100 lines):"
    echo "$output" | tail -n 100
    return 0
  fi
}

# Emit a marker for the NEEDLE verification gate handler
echo "NEEDLE_VERIFICATION_GATE: definition-of-done"

# Fast lane checks (seconds, run locally)
if [[ "$LANE" == "fast" ]] || [[ "$LANE" == "all" ]]; then
  echo "=== Fast Lane Checks ==="

  # gofmt check
  run_check "gofmt" bash -c '
    UNFORMATTED="$(gofmt -l .)"
    if [ -n "$UNFORMATTED" ]; then
      echo "gofmt would reformat the following files:"
      echo "$UNFORMATTED"
      exit 1
    fi
  '

  # go vet ./...
  run_check "go vet ./..." go vet ./...

  # golangci-lint (fast lane only - basic linters)
  # Install via Go module proxy for reliability
  run_check "golangci-lint" bash -c '
    if ! command -v golangci-lint >/dev/null 2>&1; then
      echo "Installing golangci-lint..."
      go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
    fi
    "$(go env GOPATH)/bin/golangci-lint" run \
      --max-issues-per-linter=0 --max-same-issues=0 ./...
  '
fi

# Slow lane checks (integration tests, linting, benchmarks)
if [[ "$LANE" == "slow" ]] || [[ "$LANE" == "all" ]]; then
  echo "=== Slow Lane Checks ==="

  # go test -race
  run_check "go test -race" timeout 600 go test -race ./...

  # seam lint (fragment validation)
  run_check "seam lint" bash -c '
    go build -o /tmp/seam ./cmd/seam

    # Lint all fragments with absent allowlist
    /tmp/seam lint --json > /tmp/seam-lint.json || true

    # Parse and report findings
    ERROR_COUNT=$(jq -r ".errors | length" /tmp/seam-lint.json)
    WARNING_COUNT=$(jq -r ".warnings | length" /tmp/seam-lint.json)

    echo "Files checked: $(jq -r ".files" /tmp/seam-lint.json)"
    echo "Errors: $ERROR_COUNT"
    echo "Warnings: $WARNING_COUNT"

    if [ "$ERROR_COUNT" -gt 0 ]; then
      echo ""
      echo "=== SEAM LINT ERRORS ==="
      jq -r ".errors[] | \"ERROR [\(.code)] \(.file): \(.message)\"" /tmp/seam-lint.json
      echo ""
      echo "seam lint failed with $ERROR_COUNT error(s)"
      exit 1
    fi

    if [ "$WARNING_COUNT" -gt 0 ]; then
      echo ""
      echo "=== SEAM LINT WARNINGS ==="
      jq -r ".warnings[] | \"WARNING [\(.code)] \(.file): \(.message)\"" /tmp/seam-lint.json
      echo ""
      echo "seam lint passed with $WARNING_COUNT warning(s) (review required)"
    else
      echo "seam lint passed cleanly"
    fi
  '

  # benchmark gate (if baseline exists)
  if [ -f bench/baseline.txt ]; then
    run_check "benchmark gate" bash -c '
      # Both stages below pipe through tee, so without pipefail a bench run
      # that fails to build would report tee success and compare stale data.
      set -o pipefail
      go test -bench=. -benchmem -run=^$ ./... | tee /tmp/bench-new.txt

      # Install benchstat; go install puts it in GOPATH/bin, which is not on
      # PATH (in CI least of all), so invoke it by absolute path.
      go install golang.org/x/perf/cmd/benchstat@9e4b9ddef5b6a4371594ec978cb4b8088bec845d
      "$(go env GOPATH)/bin/benchstat" bench/baseline.txt /tmp/bench-new.txt | tee /tmp/benchstat.txt

      # benchstat compares only benchmarks that appear in BOTH files with the
      # same name and GOMAXPROCS suffix. When nothing matches -- placeholder
      # baseline, or a baseline recorded on a different machine/core count --
      # it prints no percentage deltas at all, so say so instead of silently
      # passing an empty comparison.
      python3 - <<PY
import re, sys
worst = None
for line in open("/tmp/benchstat.txt"):
    m = re.search(r"([+-]\d+(?:\.\d+)?)%", line)
    if m:
        delta = float(m.group(1))
        worst = delta if worst is None else max(worst, delta)
if worst is None:
    print("No matched benchmarks between baseline and this run -- "
          "comparison skipped, not passed.")
    sys.exit(0)
print(f"Worst regression: {worst:.1f}%")
sys.exit(1 if worst > 20.0 else 0)
PY
    '
  else
    echo "Skipping benchmark gate - no committed bench/baseline.txt yet"
  fi
fi

# Summary report
echo ""
echo "=== Definition of Done Summary ==="
echo "Lane: $LANE"
echo "Checks run: ${#CHECKS[@]}"
echo "Failures: ${#FAILURES[@]}"

if [[ ${#FAILURES[@]} -gt 0 ]]; then
  echo ""
  echo "Failed checks:"
  for failure in "${FAILURES[@]}"; do
    echo "  - $failure"
  done
  echo ""
  echo "❌ Definition of NOT done"
  exit 1
else
  echo "✓ Definition of Done"
  exit 0
fi
