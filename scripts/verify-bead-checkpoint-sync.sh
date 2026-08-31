#!/usr/bin/env bash
set -euo pipefail

# verify-bead-checkpoint-sync.sh
#
# Automated verification and reconciliation of bead checkpoint sync.
#
# This script checks that the SQLite database (beads.db) matches the git-tracked
# checkpoint (.beads/checkpoint/current.json and .beads/checkpoint/forensic.jsonl).
# When a mismatch is detected, it automatically runs the appropriate reconciliation:
#
# - If database is behind (has unflushed work): runs `bead sync flush-only`
# - If checkpoint is ahead (remote-advanced): runs `bead sync reconcile`
# - If checkpoint is ahead but failed integrity: rebuilds database from checkpoint
#
# Usage:
#   ./scripts/verify-bead-checkpoint-sync.sh [--dry-run] [--verbose]
#
# Options:
#   --dry-run    Print what would be done without executing
#   --verbose    Show detailed output
#
# Exit codes:
#   0  Verification passed, no action needed (or successfully reconciled)
#   1  Verification or reconciliation failed
#   2  bead CLI not found or workspace not initialized

DRY_RUN=false
VERBOSE=false

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --dry-run)
      DRY_RUN=true
      shift
      ;;
    --verbose)
      VERBOSE=true
      shift
      ;;
    *)
      echo "Unknown option: $1"
      echo "Usage: $0 [--dry-run] [--verbose]"
      exit 2
      ;;
  esac
done

# Function to log messages
log() {
  echo "[$(date -Iseconds)] $1"
}

# Function to log verbose messages
log_verbose() {
  if [ "$VERBOSE" = true ]; then
    echo "[$(date -Iseconds)] [VERBOSE] $1"
  fi
}

# Function to execute or dry-run a command
run_cmd() {
  if [ "$DRY_RUN" = true ]; then
    echo "[DRY-RUN] Would execute: $*"
  else
    log_verbose "Executing: $*"
    "$@"
  fi
}

log "=== Bead Checkpoint Sync Verification and Reconciliation ==="
echo ""

# Check if bead CLI exists
if ! command -v bead &> /dev/null; then
  log "❌ ERROR: bead CLI not found"
  exit 2
fi

# Check if we're in a bead workspace
if [ ! -f .beads/config.json ]; then
  log "❌ ERROR: Not in a bead workspace (no .beads/config.json)"
  exit 2
fi

log "✓ bead CLI found and workspace initialized"
echo ""

# Get checkpoint status in JSON format
log "Checking checkpoint sync status..."
STATUS_JSON=$(bead sync status --format json 2>&1)
EXIT_CODE=$?

if [ $EXIT_CODE -ne 0 ]; then
  log "❌ ERROR: Failed to get checkpoint status"
  log "Output: $STATUS_JSON"
  exit 1
fi

log_verbose "Raw status output:"
log_verbose "$STATUS_JSON"
echo ""

# Parse JSON fields using jq
if ! command -v jq &> /dev/null; then
  log "❌ ERROR: jq not found. Install with: sudo apt install jq"
  exit 1
fi

RELATIONSHIP=$(echo "$STATUS_JSON" | jq -r '.relationship')
DIRTY=$(echo "$STATUS_JSON" | jq -r '.dirty')
ROOT_VERIFIED=$(echo "$STATUS_JSON" | jq -r '.root_verified')
VIEW_AGREES=$(echo "$STATUS_JSON" | jq -r '.view_agrees')
READY_TO_COMMIT=$(echo "$STATUS_JSON" | jq -r '.ready_to_commit')
CHECKPOINT_PRESENT=$(echo "$STATUS_JSON" | jq -r '.checkpoint_present')

log "Checkpoint Status:"
log "  Relationship:        $RELATIONSHIP"
log "  Dirty:               $DIRTY"
log "  Root verified:       $ROOT_VERIFIED"
log "  View agrees:         $VIEW_AGREES"
log "  Ready to commit:     $READY_TO_COMMIT"
log "  Checkpoint present:  $CHECKPOINT_PRESENT"
echo ""

# Take action based on relationship
case "$RELATIONSHIP" in
  aligned)
    log "✓ PASS: Database and checkpoint are aligned"
    log "  No action needed"
    exit 0
    ;;

  behind)
    log "⚠️  WARNING: Database is behind checkpoint (live has unflushed work)"
    log "  Action: Running 'bead sync flush-only' to publish checkpoint from database"
    echo ""
    run_cmd bead sync flush-only
    EXIT_CODE=$?

    if [ $EXIT_CODE -eq 0 ]; then
      log "✓ SUCCESS: Checkpoint flushed successfully"

      # Verify after action
      log_verbose "Verifying after flush..."
      NEW_STATUS=$(bead sync status --format json)
      NEW_RELATIONSHIP=$(echo "$NEW_STATUS" | jq -r '.relationship')

      if [ "$NEW_RELATIONSHIP" = "aligned" ]; then
        log "✓ VERIFIED: Database and checkpoint are now aligned"
      else
        log "⚠️  WARNING: New relationship is '$NEW_RELATIONSHIP' (expected 'aligned')"
      fi
      exit 0
    else
      log "❌ ERROR: Failed to flush checkpoint (exit code: $EXIT_CODE)"
      exit 1
    fi
    ;;

  remote-advanced)
    log "⚠️  WARNING: Checkpoint is ahead of database (pulled checkpoint is verified superset)"
    log "  Action: Running 'bead sync reconcile' to merge checkpoint into database"
    echo ""
    run_cmd bead sync reconcile --actor system-automated-repair
    EXIT_CODE=$?

    if [ $EXIT_CODE -eq 0 ]; then
      log "✓ SUCCESS: Checkpoint reconciled successfully"

      # Verify after action
      log_verbose "Verifying after reconcile..."
      NEW_STATUS=$(bead sync status --format json)
      NEW_RELATIONSHIP=$(echo "$NEW_STATUS" | jq -r '.relationship')

      if [ "$NEW_RELATIONSHIP" = "aligned" ]; then
        log "✓ VERIFIED: Database and checkpoint are now aligned"
      else
        log "⚠️  WARNING: New relationship is '$NEW_RELATIONSHIP' (expected 'aligned')"
      fi
      exit 0
    else
      log "❌ ERROR: Failed to reconcile checkpoint (exit code: $EXIT_CODE)"
      exit 1
    fi
    ;;

  covered-ahead-integrity-failure)
    log "❌ CRITICAL: Checkpoint is ahead but failed integrity qualification"
    log "  Action: Rebuilding database from checkpoint"
    log "  This will restore from: .beads/checkpoint/forensic.jsonl"
    echo ""
    log "WARNING: This operation will replace the current database with the checkpoint state"
    log "         Any unflushed work in the database will be lost"
    echo ""

    if [ "$DRY_RUN" = true ]; then
      log "[DRY-RUN] Would rebuild database from checkpoint"
      exit 0
    fi

    # Confirm unless in automated context (could add --force flag if needed)
    # For now, proceed automatically as this is an automated repair script
    run_cmd bead sync import-only \
      --input .beads/checkpoint/forensic.jsonl \
      --restore-into-empty \
      --actor system-automated-repair
    EXIT_CODE=$?

    if [ $EXIT_CODE -eq 0 ]; then
      log "✓ SUCCESS: Database rebuilt from checkpoint"

      # Verify after action
      log_verbose "Verifying after restore..."
      NEW_STATUS=$(bead sync status --format json)
      NEW_RELATIONSHIP=$(echo "$NEW_STATUS" | jq -r '.relationship')

      if [ "$NEW_RELATIONSHIP" = "aligned" ]; then
        log "✓ VERIFIED: Database and checkpoint are now aligned"
      else
        log "⚠️  WARNING: New relationship is '$NEW_RELATIONSHIP' (expected 'aligned')"
      fi
      exit 0
    else
      log "❌ ERROR: Failed to rebuild database from checkpoint (exit code: $EXIT_CODE)"
      exit 1
    fi
    ;;

  *)
    log "❌ ERROR: Unknown relationship status: '$RELATIONSHIP'"
    log "  Unable to determine appropriate action"
    exit 1
    ;;
esac
