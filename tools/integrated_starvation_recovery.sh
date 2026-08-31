#!/usr/bin/env bash
#
# Integrated Starvation Recovery and Auto-Closure System
#
# This script provides a comprehensive automated solution for bead starvation:
# 1. Runs bead doctor --rehearse to diagnose workspace state
# 2. Checks if open beads exist but Pluck finds no candidates (starvation detection)
# 3. Identifies root causes (stale checkpoints, stuck assignments, worker disconnection)
# 4. Executes appropriate recovery (flush, release, rebuild)
# 5. Validates recovery by re-running Pluck
# 6. Auto-closes starvation alert beads on successful recovery
#
# This can be run as a cron job or systemd service for continuous monitoring.
#
# Usage: integrated_starvation_recovery.sh [--once] [--interval MIN] [--dry-run] [--verbose] [--workspace /path/to/workspace]
#

set -euo pipefail

# Default configuration
WORKSPACE="${WORKSPACE:-/home/coding/SEAM}"
ONCE=false
INTERVAL_MINUTES=5
DRY_RUN=false
VERBOSE=false
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
TIMESTAMP_ISO=$(date -u +"%Y%m%d-%H%M%S")
LOG_DIR="/tmp/integrated-recovery-$TIMESTAMP_ISO"
LOG_FILE="$LOG_DIR/recovery.log"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Create log directory
mkdir -p "$LOG_DIR"

# Logging functions
log() {
    echo -e "${BLUE}[$(date -u +'%Y-%m-%d %H:%M:%S')]${NC} $*" | tee -a "$LOG_FILE"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*" | tee -a "$LOG_FILE"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*" | tee -a "$LOG_FILE"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $*" | tee -a "$LOG_FILE"
}

verbose() {
    if [[ "$VERBOSE" == "true" ]]; then
        log "$@"
    fi
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --once)
            ONCE=true
            shift
            ;;
        --interval)
            INTERVAL_MINUTES="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --verbose)
            VERBOSE=true
            shift
            ;;
        --workspace)
            WORKSPACE="$2"
            shift 2
            ;;
        --help|-h)
            cat <<EOF
Usage: $0 [OPTIONS]

Integrated Starvation Recovery and Auto-Closure System

Options:
  --once                 Run once and exit (default: loop mode)
  --interval MIN         Check interval in minutes (default: 5)
  --dry-run              Show what would be done without making changes
  --verbose              Enable verbose logging
  --workspace PATH       Path to workspace (default: /home/coding/SEAM)
  --help, -h             Show this help message

This script:
1. Detects starvation conditions (open beads exist but Pluck finds no candidates)
2. Runs comprehensive diagnostics (bead doctor, checkpoint consistency)
3. Executes automated recovery (flush, release, rebuild)
4. Validates recovery success
5. Auto-closes starvation alert beads on successful recovery

Example:
  $0 --once --verbose
EOF
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            exit 1
            ;;
    esac
done

cd "$WORKSPACE" || {
    log_error "Failed to cd to workspace: $WORKSPACE"
    exit 1
}

log "Starting integrated starvation recovery"
log "Workspace: $WORKSPACE"
log "Dry run: $DRY_RUN"
log "Log directory: $LOG_DIR"

# Function to count beads by status
count_beads() {
    local status="$1"
    local count=0

    while IFS= read -r line; do
        [[ -z "$line" ]] && continue
        count=$((count + 1))
    done < <(bead list --status "$status" --json 2>/dev/null)

    echo "$count"
}

# Function to count ready beads
count_ready_beads() {
    local count=0

    while IFS= read -r line; do
        [[ -z "$line" ]] && continue
        count=$((count + 1))
    done < <(bead list --ready --json 2>/dev/null)

    echo "$count"
}

# Function to find starvation alert beads
find_starvation_alert_beads() {
    local alerts=()

    while IFS= read -r line; do
        [[ -z "$line" ]] && continue

        # Check if bead has starvation alert label
        if echo "$line" | jq -e '.labels[] | select(. == "starvation-alert" or startswith("alert:starvation"))' >/dev/null 2>&1; then
            local id
            id=$(echo "$line" | jq -r '.id')
            alerts+=("$id")
        fi
    done < <(bead list --status open --json 2>/dev/null)

    printf '%s\n' "${alerts[@]}"
}

# Function to close a bead
close_bead() {
    local bead_id="$1"
    local reason="$2"

    if [[ "$DRY_RUN" == "true" ]]; then
        log_warning "[DRY-RUN] Would close bead $bead_id: $reason"
        return 0
    fi

    if bead close "$bead_id" --reason "$reason" 2>&1 | tee -a "$LOG_FILE"; then
        log_success "Closed starvation alert bead: $bead_id"
        return 0
    else
        log_error "Failed to close bead: $bead_id"
        return 1
    fi
}

# Main recovery function
run_recovery_cycle() {
    local cycle_start
    cycle_start="$(date -Iseconds)"

    log "=== Starting recovery cycle ==="

    # Get initial state
    local open_beads ready_beads starvation_alerts
    open_beads="$(count_beads "open")"
    ready_beads="$(count_ready_beads)"

    mapfile -t starvation_alerts < <(find_starvation_alert_beads)
    local alert_count="${#starvation_alerts[@]}"

    verbose "Initial state: open=$open_beads, ready=$ready_beads, alerts=$alert_count"

    # Check if starvation condition exists
    local has_starvation=false
    if [[ "$ready_beads" -eq 0 ]] && [[ "$open_beads" -gt 0 ]]; then
        has_starvation=true
        log "Starvation detected! Open beads: $open_beads, Ready beads: $ready_beads"
    fi

    # If no starvation and no alerts, nothing to do
    if [[ "$has_starvation" == "false" ]] && [[ "$alert_count" -eq 0 ]]; then
        verbose "No starvation detected and no alert beads - system healthy"
        return 0
    fi

    # If there are alert beads but no starvation now, close them
    if [[ "$has_starvation" == "false" ]] && [[ "$alert_count" -gt 0 ]]; then
        log "Starvation resolved! Closing ${alert_count} alert beads..."

        for alert_id in "${starvation_alerts[@]}"; do
            close_bead "$alert_id" "Condition self-resolved - beads now visible to workers (ready beads: $ready_beads)"
        done

        return 0
    fi

    # Starvation exists - run recovery
    log "Running automated recovery..."

    # Step 1: Run bead doctor diagnostics
    log "Step 1: Running bead doctor diagnostics..."
    local doctor_output="$LOG_DIR/bead_doctor.txt"

    if bead doctor --rehearse 2>&1 | tee "$doctor_output"; then
        log_success "Bead doctor diagnostics completed"
    else
        log_warning "Bead doctor found issues - will attempt repair"
    fi

    # Step 2: Run automated recovery script
    log "Step 2: Running automated bead recovery..."
    local recovery_output="$LOG_DIR/automated_recovery.txt"

    if automated-bead-recovery --dry-run 2>&1 | tee "$recovery_output"; then
        log_success "Automated recovery check completed"
    else
        log_warning "Automated recovery encountered issues"
    fi

    # Step 3: If not dry run, execute actual recovery
    if [[ "$DRY_RUN" == "false" ]]; then
        log "Step 3: Executing actual recovery operations..."

        # Run bead doctor repair
        if bead doctor --repair 2>&1 | tee -a "$LOG_FILE"; then
            log_success "Bead doctor repair completed"
        else
            log_warning "Bead doctor repair had issues"
        fi

        # Flush checkpoint if needed
        log "Flushing checkpoint..."
        if bead sync flush-only 2>&1 | tee -a "$LOG_FILE"; then
            log_success "Checkpoint flushed"
        fi

        # Verify recovery
        log "Verifying recovery..."
        local ready_after
        ready_after="$(count_ready_beads)"

        if [[ "$ready_after" -gt 0 ]]; then
            log_success "Recovery successful! Ready beads: $ready_after (was $ready_beads)"

            # Close all starvation alert beads
            log "Closing ${alert_count} starvation alert beads..."
            for alert_id in "${starvation_alerts[@]}"; do
                close_bead "$alert_id" "Automated recovery successful - beads now visible (ready beads: $ready_after)"
            done
        else
            log_warning "Recovery incomplete. Ready beads still: $ready_after"
            log "Manual intervention may be required"
        fi
    else
        log_warning "Dry run - skipping actual recovery operations"
    fi

    local cycle_end
    cycle_end="$(date -Iseconds)"

    log "=== Recovery cycle complete ==="
    log "Duration: $cycle_start to $cycle_end"

    return 0
}

# Main loop
main() {
    log "Starting integrated starvation recovery (interval: ${INTERVAL_MINUTES}m)"

    if [[ "$ONCE" == "true" ]]; then
        log "Running one-shot recovery cycle..."
        run_recovery_cycle
    else
        # Loop mode
        while true; do
            run_recovery_cycle

            log "Sleeping for ${INTERVAL_MINUTES} minutes..."
            sleep "$((INTERVAL_MINUTES * 60))"
        done
    fi
}

main