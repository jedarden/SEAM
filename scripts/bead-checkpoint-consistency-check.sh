#!/usr/bin/env bash
set -euo pipefail

# bead-checkpoint-consistency-check.sh
#
# Automated health check that verifies bead database consistency with the checkpoint.
# This script implements a count-based verification approach:
#
# 1. Run `bead sync flush-only` to ensure checkpoint is current
# 2. Compare bead counts between the database and checkpoint
# 3. If counts diverge or beads.db is missing/corrupt, run automated recovery
# 4. Verify restoration succeeded before proceeding
#
# Usage:
#   ./scripts/bead-checkpoint-consistency-check.sh [--dry-run] [--verbose] [--force-repair]
#
# Options:
#   --dry-run      Print what would be done without executing
#   --verbose      Show detailed output
#   --force-repair Force recovery even if counts match (useful for corruption detection)
#
# Exit codes:
#   0  Verification passed, database is consistent
#   1  Verification or recovery failed
#   2  bead CLI not found or workspace not initialized
#   3  Recovery required but incomplete
#

DRY_RUN=false
VERBOSE=false
FORCE_REPAIR=false
SCRIPT_NAME="$(basename "$0")"

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
    --force-repair)
      FORCE_REPAIR=true
      shift
      ;;
    *)
      echo "Unknown option: $1"
      echo "Usage: $0 [--dry-run] [--verbose] [--force-repair]"
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
    return 0
  else
    log_verbose "Executing: $*"
    "$@"
  fi
}

log "=== Bead Checkpoint Consistency Health Check ==="
log "Script: $SCRIPT_NAME"
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

# Check if jq exists
if ! command -v jq &> /dev/null; then
  log "❌ ERROR: jq not found. Install with: sudo apt install jq"
  exit 1
fi

log "✓ All prerequisites met (bead CLI, jq)"
echo ""

# Step 1: Ensure checkpoint is current
log "Step 1: Flushing checkpoint to ensure it's current..."
run_cmd bead sync flush-only
FLUSH_EXIT=$?

if [ $FLUSH_EXIT -ne 0 ]; then
  log "❌ ERROR: Failed to flush checkpoint (exit code: $FLUSH_EXIT)"
  log "   The database may be corrupted or bead CLI is not working"

  # If flush failed, we likely need recovery
  log "   Triggering recovery due to flush failure..."
  FORCE_REPAIR=true
else
  log "✓ Checkpoint flushed successfully"
fi
echo ""

# Step 2: Check if beads.db exists and is readable
DATABASE_PATH=".beads/beads.db"
CHECKPOINT_PATH=".beads/checkpoint/forensic.jsonl"

if [ ! -f "$DATABASE_PATH" ]; then
  log "❌ CRITICAL: beads.db is missing at $DATABASE_PATH"
  log "   Triggering recovery..."
  FORCE_REPAIR=true
elif [ ! -r "$DATABASE_PATH" ]; then
  log "❌ CRITICAL: beads.db exists but is not readable"
  log "   Triggering recovery..."
  FORCE_REPAIR=true
fi

# Step 3: Count beads in database
DB_Bead_COUNT=0
DB_COUNT_VALID=false

if [ "$FORCE_REPAIR" = false ]; then
  log "Step 2: Counting beads in database..."

  # Try to query the database using bead list with high limit to get all beads
  # bead list --json outputs JSONL (one JSON object per line), use jq -s to read as array
  DB_QUERY_RESULT=$(bead list --limit 10000 --json 2>&1) || DB_QUERY_RESULT=""

  # Check if the result is valid JSONL and count entries
  if echo "$DB_QUERY_RESULT" | jq -s 'if type == "array" then length else empty end' > /dev/null 2>&1; then
    DB_Bead_COUNT=$(echo "$DB_QUERY_RESULT" | jq -s 'length')
    DB_COUNT_VALID=true
    log "✓ Database bead count: $DB_Bead_COUNT"
  else
    log "❌ ERROR: Failed to query database (corruption detected)"
    log "   Bead CLI error: $DB_QUERY_RESULT"
    log "   Triggering recovery..."
    FORCE_REPAIR=true
  fi
  echo ""
fi

# Step 4: Count beads in checkpoint
CHECKPOINT_Bead_COUNT=0
CHECKPOINT_COUNT_VALID=false

if [ "$FORCE_REPAIR" = false ]; then
  log "Step 3: Counting beads in checkpoint..."

  if [ ! -f "$CHECKPOINT_PATH" ]; then
    log "❌ CRITICAL: Checkpoint file missing at $CHECKPOINT_PATH"
    log "   Cannot restore from checkpoint"
    exit 1
  fi

  # Count issue records in forensic.jsonl
  CHECKPOINT_QUERY_RESULT=$(jq -r 'select(.record_type == "issue") | .issue.id' "$CHECKPOINT_PATH" | wc -l)

  if [ -n "$CHECKPOINT_QUERY_RESULT" ] && [[ "$CHECKPOINT_QUERY_RESULT" =~ ^[0-9]+$ ]]; then
    CHECKPOINT_Bead_COUNT="$CHECKPOINT_QUERY_RESULT"
    CHECKPOINT_COUNT_VALID=true
    log "✓ Checkpoint bead count: $CHECKPOINT_Bead_COUNT"
  else
    log "❌ ERROR: Failed to count beads in checkpoint"
    log "   Checkpoint may be corrupted"
    exit 1
  fi
  echo ""
fi

# Step 5: Compare counts
COUNTS_MATCH=false
RECOVERY_NEEDED=false

if [ "$FORCE_REPAIR" = true ]; then
  log "Step 4: Skipping count comparison (force repair mode)"
  RECOVERY_NEEDED=true
elif [ "$DB_COUNT_VALID" = false ] || [ "$CHECKPOINT_COUNT_VALID" = false ]; then
  log "Step 4: Cannot compare counts (validation failed)"
  RECOVERY_NEEDED=true
else
  log "Step 4: Comparing counts..."
  log "  Database beads:    $DB_Bead_COUNT"
  log "  Checkpoint beads:  $CHECKPOINT_Bead_COUNT"

  COUNT_DIFF=$((DB_Bead_COUNT - CHECKPOINT_Bead_COUNT))

  if [ "$COUNT_DIFF" -eq 0 ]; then
    COUNTS_MATCH=true
    log "✓ PASS: Counts match ($DB_Bead_COUNT beads)"
  elif [ "$COUNT_DIFF" -gt 0 ]; then
    log "⚠️  WARNING: Database has $COUNT_DIFF more beads than checkpoint"
    log "   This indicates unflushed work or database corruption"
    RECOVERY_NEEDED=true
  else
    log "⚠️  WARNING: Checkpoint has $((COUNT_DIFF * -1)) more beads than database"
    log "   This indicates database corruption or data loss"
    RECOVERY_NEEDED=true
  fi
fi
echo ""

# Step 6: Recovery if needed
if [ "$RECOVERY_NEEDED" = true ]; then
  log "Step 5: Recovery required - initiating automated repair..."
  log "  This will rebuild the database from checkpoint"
  log "  Checkpoint source: $CHECKPOINT_PATH"
  echo ""

  if [ "$DRY_RUN" = true ]; then
    log "[DRY-RUN] Would run recovery procedure"
    exit 0
  fi

  # Backup the current database if it exists
  if [ -f "$DATABASE_PATH" ]; then
    BACKUP_PATH="${DATABASE_PATH}.before-recovery.$(date +%s)"
    log "  Backing up current database to: $BACKUP_PATH"
    run_cmd cp "$DATABASE_PATH" "$BACKUP_PATH"
  fi

  # Run bead init to rebuild schema
  log "  Running: bead init (rebuild schema, keep workspace identity)"
  run_cmd bead init
  INIT_EXIT=$?

  if [ $INIT_EXIT -ne 0 ]; then
    log "❌ ERROR: bead init failed (exit code: $INIT_EXIT)"
    log "   Recovery incomplete"
    exit 3
  fi
  log "✓ Schema rebuilt successfully"
  echo ""

  # Import from checkpoint
  log "  Running: bead sync import-only --restore-into-empty"
  run_cmd bead sync import-only \
    --input "$CHECKPOINT_PATH" \
    --restore-into-empty \
    --actor "$SCRIPT_NAME"
  IMPORT_EXIT=$?

  if [ $IMPORT_EXIT -ne 0 ]; then
    log "❌ ERROR: Checkpoint import failed (exit code: $IMPORT_EXIT)"
    log "   Recovery incomplete - database may be in inconsistent state"
    exit 3
  fi
  log "✓ Checkpoint imported successfully"
  echo ""

  # Step 7: Verify restoration
  log "Step 6: Verifying restoration succeeded..."

  # Count beads in restored database using bead list with high limit to get all beads
  # bead list --json outputs JSONL (one JSON object per line), use jq -s to read as array
  RESTORED_QUERY_RESULT=$(bead list --limit 10000 --json 2>&1) || RESTORED_QUERY_RESULT=""

  # Check if the result is valid JSONL and count entries
  if echo "$RESTORED_QUERY_RESULT" | jq -s 'if type == "array" then length else empty end' > /dev/null 2>&1; then
    RESTORED_COUNT=$(echo "$RESTORED_QUERY_RESULT" | jq -s 'length')
    log "  Restored database bead count: $RESTORED_COUNT"

    # Compare with checkpoint count
    if [ "$CHECKPOINT_COUNT_VALID" = true ]; then
      if [ "$RESTORED_COUNT" -eq "$CHECKPOINT_Bead_COUNT" ]; then
        log "✓ SUCCESS: Restoration verified - counts match"
        log "  Database rebuilt from checkpoint successfully"
        echo ""
        log "=== Health Check Complete ==="
        log "Status: RECOVERY_SUCCESSFUL"
        log "Action: Database rebuilt from checkpoint"
        log "Restored beads: $RESTORED_COUNT"
        exit 0
      else
        log "⚠️  WARNING: Restoration count mismatch"
        log "  Expected: $CHECKPOINT_Bead_COUNT"
        log "  Got:      $RESTORED_COUNT"
        log "  Recovery may be incomplete"
        exit 3
      fi
    else
      log "✓ Database restored (checkpoint count unavailable for verification)"
      exit 0
    fi
  else
    log "❌ ERROR: Failed to verify restoration"
    log "  Bead CLI error: $RESTORED_QUERY_RESULT"
    log "  Recovery may have failed"
    exit 3
  fi
else
  # No recovery needed - counts match
  log "Step 5: No recovery needed - counts are consistent"
  echo ""
  log "=== Health Check Complete ==="
  log "Status: CONSISTENT"
  log "Database beads: $DB_Bead_COUNT"
  log "Checkpoint beads: $CHECKPOINT_Bead_COUNT"
  exit 0
fi
