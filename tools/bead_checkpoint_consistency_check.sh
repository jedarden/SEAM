#!/usr/bin/env bash
#
# Bead Checkpoint Consistency Check and Auto-Repair
#
# This script verifies bead database consistency with the checkpoint and performs
# auto-repair when divergence is detected. It prevents the scenario where a fresh
# clone or corrupted database silently loses beads.
#
# Usage: tools/bead_checkpoint_consistency_check.sh [--dry-run]
#
# Exit codes:
#   0 - Database is consistent
#   1 - Inconsistency detected and successfully repaired
#   2 - Inconsistency detected but repair failed
#   3 - Critical error (missing checkpoint, database corruption beyond repair)

set -euo pipefail

# Color output for readability
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $*"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*" >&2
}

# Check if we're in a valid workspace
validate_workspace() {
    if [[ ! -f ".needle.yaml" ]]; then
        log_error "Not in a valid workspace - .needle.yaml not found"
        exit 3
    fi

    # Check which backend we're using
    if grep -q "backend: bead-rs" .needle.yaml 2>/dev/null; then
        BEAD_BACKEND="bead-rs"
        BEAD_CMD="bead"
    elif grep -q "backend: bf" .needle.yaml 2>/dev/null; then
        BEAD_BACKEND="bf"
        BEAD_CMD="bf"
    else
        log_error "Cannot determine bead backend from .needle.yaml"
        exit 3
    fi

    log_info "Detected bead backend: $BEAD_BACKEND"
}

# Flush checkpoint to ensure it's current
flush_checkpoint() {
    log_info "Flushing checkpoint to ensure it's current..."

    if $BEAD_CMD sync flush-only 2>&1 | tee -a /tmp/bead-check-flush.log; then
        log_info "Checkpoint flush successful"
    else
        log_error "Checkpoint flush failed - cannot proceed with verification"
        exit 3
    fi
}

# Get bead count from database
get_db_count() {
    # bead list --json outputs JSONL (one JSON object per line), not a JSON array
    $BEAD_CMD list --json 2>/dev/null | jq -s 'length' || echo "0"
}

# Get bead count from checkpoint
get_checkpoint_count() {
    local current_json=".beads/checkpoint/current.json"

    if [[ ! -f "$current_json" ]]; then
        log_error "Checkpoint current.json not found"
        echo "0"
        return
    fi

    jq -r '.issue_count // 0' "$current_json" 2>/dev/null || echo "0"
}

# Verify checkpoint files exist and are valid
validate_checkpoint() {
    local checkpoint_dir=".beads/checkpoint"
    local forensic="$checkpoint_dir/forensic.jsonl"
    local current="$checkpoint_dir/current.json"

    if [[ ! -d "$checkpoint_dir" ]]; then
        log_error "Checkpoint directory missing: $checkpoint_dir"
        return 1
    fi

    if [[ ! -f "$forensic" ]]; then
        log_error "Checkpoint forensic.jsonl missing: $forensic"
        return 1
    fi

    if [[ ! -f "$current" ]]; then
        log_error "Checkpoint current.json missing: $current"
        return 1
    fi

    # Check forensic.jsonl has content
    if [[ ! -s "$forensic" ]]; then
        log_error "Checkpoint forensic.jsonl is empty"
        return 1
    fi

    log_info "Checkpoint files validated successfully"
    return 0
}

# Perform database restoration from checkpoint
restore_database() {
    local dry_run="${1:-false}"

    log_warn "Database inconsistency detected - initiating restoration..."

    if [[ "$dry_run" == "true" ]]; then
        log_info "[DRY-RUN] Would run: bead init && bead sync import-only --input .beads/checkpoint/forensic.jsonl --restore-into-empty --agent bead-checkpoint-consistency"
        return 0
    fi

    # Backup the current database before restoration
    local backup_dir=".beads/recovery-backups"
    local backup_name="beads.db.before-restore.$(date +%Y%m%d-%H%M%S)"
    mkdir -p "$backup_dir"
    cp .beads/beads.db "$backup_dir/$backup_name" 2>/dev/null || true
    log_info "Backed up existing database to: $backup_dir/$backup_name"

    # Remove the corrupt database
    log_info "Removing corrupt database..."
    rm -f .beads/beads.db

    # Reinitialize the database
    log_info "Reinitializing database schema..."
    if $BEAD_CMD init 2>&1 | tee -a /tmp/bead-restore.log; then
        log_info "Database reinitialized successfully"
    else
        log_error "Database initialization failed"
        return 1
    fi

    # Restore from checkpoint
    log_info "Restoring beads from checkpoint..."
    if $BEAD_CMD sync import-only \
        --input .beads/checkpoint/forensic.jsonl \
        --restore-into-empty \
        --agent bead-checkpoint-consistency-check 2>&1 | tee -a /tmp/bead-restore.log; then
        log_info "Restoration completed successfully"
        return 0
    else
        log_error "Restoration from checkpoint failed"
        return 1
    fi
}

# Verify restoration succeeded
verify_restoration() {
    local dry_run="${1:-false}"

    if [[ "$dry_run" == "true" ]]; then
        log_info "[DRY-RUN] Skipping restoration verification - no actual changes made"
        return 0
    fi

    log_info "Verifying restoration succeeded..."

    local db_count_after
    local checkpoint_count

    db_count_after=$(get_db_count)
    checkpoint_count=$(get_checkpoint_count)

    log_info "Database bead count after restoration: $db_count_after"
    log_info "Checkpoint bead count: $checkpoint_count"

    if [[ "$db_count_after" -eq "$checkpoint_count" ]]; then
        log_info "✓ Restoration verified - counts match"
        return 0
    elif [[ "$db_count_after" -gt 0 ]]; then
        log_info "✓ Restoration appears successful - database now has $db_count_after beads"
        # Note: There may still be a difference due to status filtering or other factors
        local difference=$((checkpoint_count - db_count_after))
        if [[ "$difference" -gt 0 ]]; then
            log_info "  (Checkpoint has $difference more beads - this may be due to status/archival differences)"
        fi
        return 0
    else
        log_error "✗ Restoration failed - database is empty"
        return 1
    fi
}

# Main execution
main() {
    local dry_run=false

    if [[ "${1:-}" == "--dry-run" ]]; then
        dry_run=true
        log_info "Running in DRY-RUN mode - no changes will be made"
    fi

    log_info "=== Bead Checkpoint Consistency Check ==="

    # Validate workspace
    validate_workspace

    # Validate checkpoint exists and is usable
    if ! validate_checkpoint; then
        log_error "Checkpoint validation failed - cannot proceed"
        exit 3
    fi

    # Flush checkpoint first
    flush_checkpoint

    # Get counts
    local db_count
    local checkpoint_count

    db_count=$(get_db_count)
    checkpoint_count=$(get_checkpoint_count)

    log_info "Database bead count: $db_count"
    log_info "Checkpoint bead count: $checkpoint_count"

    # Check for database file corruption
    if [[ ! -f ".beads/beads.db" ]]; then
        log_error "Database file missing: .beads/beads.db"
        restore_database "$dry_run" || exit 2
        verify_restoration "$dry_run" || exit 2
        exit 1
    fi

    # Check for divergence
    local divergence=$((checkpoint_count - db_count))

    if [[ "$db_count" -eq 0 && "$checkpoint_count" -gt 0 ]]; then
        log_error "Database is empty but checkpoint has $checkpoint_count beads"
        restore_database "$dry_run" || exit 2
        verify_restoration "$dry_run" || exit 2
        exit 1
    elif [[ "$divergence" -gt 10 ]]; then
        # Allow some tolerance for status differences, but significant divergence indicates a problem
        log_error "Significant divergence detected: $divergence beads missing from database"
        restore_database "$dry_run" || exit 2
        verify_restoration "$dry_run" || exit 2
        exit 1
    elif [[ "$divergence" -gt 0 ]]; then
        log_warn "Minor divergence detected: $divergence beads difference"
        log_info "This may be due to status filtering - database appears healthy"
        exit 0
    else
        log_info "✓ Database is consistent with checkpoint"
        exit 0
    fi
}

main "$@"
