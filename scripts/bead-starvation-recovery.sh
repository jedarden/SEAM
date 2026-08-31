#!/usr/bin/env bash
set -euo pipefail

# SEAM Bead Starvation Recovery Script
#
# Addresses the failure mode where reopened beads retained stale assignees
# and became permanently unclaimable (see CLAUDE.md "NEEDLE Learnings" and
# bead seam-d267f63b).
#
# This script:
# 1. Runs `bead doctor --repair` to fix stale temp files or checkpoint views
# 2. Identifies beads in the 'assigned-but-open' stuck state
# 3. Clears assignees for beads whose worker is not currently running
# 4. Logs all actions taken to .beads/doctor-recovery.log
#
# Usage: scripts/bead-starvation-recovery.sh [--dry-run]

RECOVERY_LOG=".beads/doctor-recovery.log"
WORKSPACE_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
DRY_RUN=false

# Parse arguments
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN=true
      shift
      ;;
    *)
      echo "Usage: $0 [--dry-run]" >&2
      exit 1
      ;;
  esac
done

cd "$WORKSPACE_ROOT"

# Function to log with timestamp
log() {
  local timestamp
  timestamp="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  echo "[$timestamp] $*" | tee -a "$RECOVERY_LOG"
}

# Function to get currently running NEEDLE worker identifiers
get_running_workers() {
  ps aux | grep -E 'needle run.*--identifier' | grep -v grep | sed -E 's/.*--identifier ([^ ]+).*/\1/' | sort -u
}

# Start recovery run
log "=== Starvation Recovery Run at $(date -u +"%Y-%m-%dT%H:%M:%SZ") ==="

if [ "$DRY_RUN" = true ]; then
  log "DRY RUN MODE - No actual changes will be made"
fi

# Step 1: Run bead doctor --repair
log "[1/4] Running bead doctor --repair..."
if bead doctor --repair >> "$RECOVERY_LOG" 2>&1; then
  log "  ✓ bead doctor --repair completed successfully"
else
  log "  ✗ bead doctor --repair failed (exit code: $?)"
fi

# Step 2: Get currently running workers
log "[2/4] Identifying currently running NEEDLE workers..."
RUNNING_WORKERS=()
while IFS= read -r worker; do
  if [[ -n "$worker" ]]; then
    RUNNING_WORKERS+=("$worker")
  fi
done < <(get_running_workers)

if [ ${#RUNNING_WORKERS[@]} -eq 0 ]; then
  log "  ⚠ No running NEEDLE workers detected"
else
  log "  ✓ Found ${#RUNNING_WORKERS[@]} running worker(s): ${RUNNING_WORKERS[*]}"
fi

# Step 3: Query for assigned-but-open beads
log "[3/4] Identifying assigned-but-open beads..."

# Use bead list --json with proper jq handling
STALE_BEADS=()
while IFS= read -r bead; do
  bead_id=$(echo "$bead" | jq -r '.id')
  assignee=$(echo "$bead" | jq -r '.assignee')
  title=$(echo "$bead" | jq -r '.title')

  # Check if assignee is in the running workers list
  is_running=false
  for worker in "${RUNNING_WORKERS[@]}"; do
    if [[ "$assignee" == "$worker" ]]; then
      is_running=true
      break
    fi
  done

  # Also check if assignee matches a running worker pattern
  # Some assignees might have different formats
  if ! $is_running; then
    # Check if assignee contains any of the running worker identifiers
    for worker in "${RUNNING_WORKERS[@]}"; do
      if [[ "$assignee" == *"$worker"* ]]; then
        is_running=true
        break
      fi
    done
  fi

  if ! $is_running; then
    STALE_BEADS+=("$bead_id|$assignee|$title")
    log "  ⚠ Found stale bead: $bead_id (assignee: $assignee, title: $title)"
  fi
done < <(bead list --json 2>&1 | jq -c 'select(.assignee != null and .status == "open")')

if [ ${#STALE_BEADS[@]} -eq 0 ]; then
  log "  ✓ No stale assigned-but-open beads found"
else
  log "  ⚠ Found ${#STALE_BEADS[@]} stale assigned-but-open bead(s)"
fi

# Step 4: Clear assignees for stale beads
log "[4/4] Clearing stale assignees..."
REPAIRED_COUNT=0

for stale_bead in "${STALE_BEADS[@]}"; do
  IFS='|' read -r bead_id assignee title <<< "$stale_bead"

  log "  → Clearing assignee for $bead_id (was: $assignee)"

  if [ "$DRY_RUN" = true ]; then
    log "    [DRY RUN] Would run: bead update $bead_id --clear-assignee"
  else
    if bead update "$bead_id" --clear-assignee >> "$RECOVERY_LOG" 2>&1; then
      log "    ✓ Successfully cleared assignee for $bead_id"
      ((REPAIRED_COUNT++))
    else
      log "    ✗ Failed to clear assignee for $bead_id (exit code: $?)"
    fi
  fi
done

# Final summary
log ""
log "Recovery Summary:"
if [ "$DRY_RUN" = true ]; then
  log "  DRY RUN - No actual changes were made"
fi
log "  Total stale beads identified: ${#STALE_BEADS[@]}"
log "  Total repairs performed: $REPAIRED_COUNT"
log "=== End of Recovery Run ==="
log ""

if [ ${#STALE_BEADS[@]} -gt 0 ] && [ "$DRY_RUN" = false ]; then
  # Flush checkpoint after repairs
  log "Flushing checkpoint to persist repairs..."
  bead sync flush-only >> "$RECOVERY_LOG" 2>&1 || true
  log "✓ Checkpoint flushed"
fi

exit 0
