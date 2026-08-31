#!/usr/bin/env bash
#
# integration_example.sh
#
# Example: How to integrate the cross-repo precondition validator
# into your workflow or automation.
#
# This script shows three integration patterns:
# 1. Pre-work validation (before starting work)
# 2. Periodic validation (cron/scheduled job)
# 3. Post-starvation-alert recovery
#

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
VALIDATOR="$SCRIPT_DIR/validate_cross_repo_preconditions.sh"

log() {
    echo "[$(date -Iseconds)] $*" >&2
}

# Pattern 1: Pre-work validation
# Run this before claiming beads or starting work
pre_work_validation() {
    log "Running pre-work precondition validation..."

    # Dry run to see what would be blocked
    if "$VALIDATOR" --dry-run; then
        log "✓ All preconditions validated - safe to proceed"
        return 0
    else
        log "✗ Some preconditions are unmet - review before proceeding"
        return 1
    fi
}

# Pattern 2: Periodic validation
# Add this to crontab: */5 * * * * /path/to/this-script.sh periodic
periodic_validation() {
    log "Running periodic precondition validation..."

    if "$VALIDATOR"; then
        log "✓ Validation complete"
    else
        log "⚠ Validation completed with errors - check logs"
    fi
}

# Pattern 3: Post-starvation-alert recovery
# Run this after receiving a starvation alert to check if
# unmet cross-repo preconditions are the cause
starvation_recovery() {
    log "Running precondition validation after starvation alert..."

    # Run with verbose output for diagnosis
    if "$VALIDATOR" --verbose; then
        log "✓ Validation complete"
        log "If beads remain unclaimable, check bead dependencies and assignees"
    else
        log "⚠ Validation failed - manual investigation required"
    fi

    # Show current state
    log "Current ready beads:"
    bead list --ready || true
}

# Main dispatcher
case "${1:-help}" in
    pre-work)
        pre_work_validation
        ;;
    periodic)
        periodic_validation
        ;;
    starvation-recovery)
        starvation_recovery
        ;;
    help|*)
        cat <<EOF
Usage: $0 <pattern>

Integration patterns for cross-repo precondition validation:

Patterns:
  pre-work            Run validation before starting work
  periodic            Run validation on a schedule (e.g., cron)
  starvation-recovery Run after receiving a starvation alert

Examples:
  # Before claiming work
  $0 pre-work

  # Add to crontab: */5 * * * * /path/to/integration_example.sh periodic
  $0 periodic

  # After starvation alert
  $0 starvation-recovery
EOF
        exit 1
        ;;
esac
