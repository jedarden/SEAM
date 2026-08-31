#!/usr/bin/env bash
#
# starvation-recovery.sh
#
# Automated diagnostic and recovery for bead starvation conditions.
# Detects when no candidates are available but open/invisible beads exist,
# and runs automated recovery before escalating to human intervention.
#
# Usage:
#   starvation-recovery.sh [--once] [--interval MIN] [--dry-run] [--verbose] [--json]
#

set -euo pipefail

# Default configuration
WORKSPACE_ROOT="${WORKSPACE_ROOT:-/home/coding}"
VALIDATE_SCRIPT="${VALIDATE_SCRIPT:-$WORKSPACE_ROOT/SEAM/tools/validate_cross_repo_preconditions.sh}"
ONCE=false
INTERVAL_MINUTES=5
DRY_RUN=false
VERBOSE=false
JSON_OUTPUT=false

# Logging functions
log() {
    echo "[$(date -Iseconds)] $*" >&2
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
        --json)
            JSON_OUTPUT=true
            shift
            ;;
        --help|-h)
            cat <<EOF
Usage: $0 [OPTIONS]

Automated diagnostic and recovery for bead starvation conditions.

Options:
  --once                 Run once and exit (default: loop mode)
  --interval MIN         Check interval in minutes (default: 5)
  --dry-run              Show what would be done without making changes
  --verbose              Enable verbose logging
  --json                 Output results in JSON format
  --help, -h             Show this help message

Environment variables:
  WORKSPACE_ROOT         Root directory containing workspaces (default: /home/coding)
  VALIDATE_SCRIPT        Path to validate_cross_repo_preconditions.sh

Example:
  $0 --once --verbose
EOF
            exit 0
            ;;
        *)
            log "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Validate that scripts exist
if [[ ! -f "$VALIDATE_SCRIPT" ]]; then
    log "ERROR: Validate script not found: $VALIDATE_SCRIPT"
    exit 1
fi

# Count beads by status in a workspace
count_beads() {
    local workspace="$1"
    local status="$2"

    cd "$workspace"
    local count=0

    # bead list outputs newline-separated JSON objects
    while IFS= read -r line; do
        [[ -z "$line" ]] && continue
        count=$((count + 1))
    done < <(bead list --status "$status" --json 2>/dev/null)

    echo "$count"
}

# Count invisible beads (assigned but not in_progress, or manually blocked)
count_invisible_beads() {
    local workspace="$1"

    cd "$workspace"
    local count=0

    # bead list outputs newline-separated JSON objects
    while IFS= read -r line; do
        [[ -z "$line" ]] && continue

        # Check if bead has assignee or is manual_blocked
        if echo "$line" | jq -e 'select(.assignee != null and .assignee != "" or .manual_blocked == true)' >/dev/null 2>&1; then
            count=$((count + 1))
        fi
    done < <(bead list --status open --json 2>/dev/null)

    echo "$count"
}

# Count ready beads
count_ready_beads() {
    local workspace="$1"

    cd "$workspace"
    local count=0

    # bead list outputs newline-separated JSON objects
    while IFS= read -r line; do
        [[ -z "$line" ]] && continue
        count=$((count + 1))
    done < <(bead list --ready --json 2>/dev/null)

    echo "$count"
}

# Run bead doctor repair
run_bead_doctor() {
    local workspace="$1"

    if [[ "$DRY_RUN" == "true" ]]; then
        log "[DRY-RUN] Would run: bead doctor --repair in $workspace"
        return 0
    fi

    cd "$workspace"
    if bead doctor --repair &>/dev/null; then
        verbose "bead doctor --repair succeeded in $workspace"
        return 0
    else
        log "bead doctor --repair failed in $workspace"
        return 1
    fi
}

# Validate cross-repo preconditions
run_validate_preconditions() {
    local workspace="$1"

    if [[ "$DRY_RUN" == "true" ]]; then
        log "[DRY-RUN] Would run: $VALIDATE_SCRIPT in $workspace"
        return 0
    fi

    cd "$workspace"
    if bash "$VALIDATE_SCRIPT" --verbose &>/dev/null; then
        verbose "Precondition validation succeeded in $workspace"
        return 0
    else
        log "Precondition validation failed in $workspace"
        return 1
    fi
}

# Check if NEEDLE workers are alive
check_workers_alive() {
    if pgrep -f "needle.*worker" >/dev/null 2>&1; then
        verbose "NEEDLE workers are alive"
        return 0
    else
        verbose "No NEEDLE workers found"
        return 1
    fi
}

# Run recovery on a single workspace
run_recovery() {
    local workspace="$1"
    local workspace_name
    workspace_name="$(basename "$workspace")"
    local start_time
    start_time="$(date -Iseconds)"

    log "[$workspace_name] Checking workspace for starvation..."

    # Get initial state
    local open_beads invisible_beads ready_beads
    open_beads="$(count_beads "$workspace" "open")"
    invisible_beads="$(count_invisible_beads "$workspace")"
    ready_beads="$(count_ready_beads "$workspace")"

    verbose "[$workspace_name] State: open=$open_beads, invisible=$invisible_beads, ready=$ready_beads"

    # Check if starvation condition exists
    local has_starvation=false
    if [[ "$ready_beads" -eq 0 ]] && [[ "$open_beads" -gt 0 || "$invisible_beads" -gt 0 ]]; then
        has_starvation=true
    fi

    if [[ "$has_starvation" == "false" ]]; then
        verbose "[$workspace_name] No starvation detected (ready beads available)"
        return 0
    fi

    log "[$workspace_name] Starvation detected! Running automated recovery..."

    # Recovery steps
    local bead_doctor_run=false
    local bead_doctor_success=false
    local preconds_run=false
    local preconds_success=false
    local workers_alive=false
    local ready_after=0
    local errors=()

    # Step 1: Run bead doctor
    if run_bead_doctor "$workspace"; then
        bead_doctor_run=true
        bead_doctor_success=true
    else
        bead_doctor_run=true
        bead_doctor_success=false
        errors+=("bead doctor failed")
    fi

    # Step 2: Validate preconditions
    if run_validate_preconditions "$workspace"; then
        preconds_run=true
        preconds_success=true
    else
        preconds_run=true
        preconds_success=false
        errors+=("precondition validation failed")
    fi

    # Step 3: Check workers
    if check_workers_alive; then
        workers_alive=true
    else
        workers_alive=false
        errors+=("no workers found")
    fi

    # Step 4: Re-evaluate ready frontier
    ready_after="$(count_ready_beads "$workspace")"

    local end_time
    end_time="$(date -Iseconds)"

    # Determine success
    local success=false
    if [[ "$ready_after" -gt 0 ]]; then
        success=true
        log "[$workspace_name] Recovery succeeded! Ready beads: $ready_after (was $ready_beads)"
    else
        log "[$workspace_name] Recovery incomplete. Ready beads: $ready_after. Manual intervention may be needed."
        if [[ ${#errors[@]} -gt 0 ]]; then
            log "[$workspace_name] Errors: $(IFS='; '; echo "${errors[*]}")"
        fi
    fi

    # Output result
    if [[ "$JSON_OUTPUT" == "true" ]]; then
        jq -n \
            --arg workspace "$workspace_name" \
            --arg start_time "$start_time" \
            --arg end_time "$end_time" \
            --argjson open_before "$open_beads" \
            --argjson invisible_before "$invisible_beads" \
            --argjson ready_before "$ready_beads" \
            --argjson ready_after "$ready_after" \
            --argjson success "$success" \
            --argjson bead_doctor_run "$bead_doctor_run" \
            --argjson bead_doctor_success "$bead_doctor_success" \
            --argjson preconds_run "$preconds_run" \
            --argjson preconds_success "$preconds_success" \
            --argjson workers_alive "$workers_alive" \
            --argjson errors "$(printf '%s\n' "${errors[@]}" | jq -R . | jq -s .)" \
            '{
                workspace: $workspace,
                start_time: $start_time,
                end_time: $end_time,
                open_beads_before: $open_before,
                invisible_before: $invisible_before,
                ready_beads_before: $ready_before,
                ready_beads_after: $ready_after,
                success: $success,
                bead_doctor_run: $bead_doctor_run,
                bead_doctor_success: $bead_doctor_success,
                preconds_run: $preconds_run,
                preconds_success: $preconds_success,
                workers_alive: $workers_alive,
                errors: $errors
            }'
    else
        echo ""
        echo "Workspace: $workspace_name"
        echo "  Open beads: $open_beads"
        echo "  Invisible: $invisible_beads"
        echo "  Ready before: $ready_beads"
        echo "  Ready after: $ready_after"
        echo "  Result: $([ "$success" == "true" ] && echo "✓ SUCCESS" || echo "✗ INCOMPLETE")"
    fi

    return 0
}

# Find all workspaces with bead databases
find_workspaces() {
    for dir in "$WORKSPACE_ROOT"/*/; do
        if [[ -f "$dir/.beads/beads.db" ]]; then
            echo "${dir%/}"
        fi
    done
}

# Main loop
main() {
    log "Starting starvation recovery (interval: ${INTERVAL_MINUTES}m)"

    if [[ "$ONCE" == "true" ]]; then
        log "Running one-shot recovery..."
        if [[ "$JSON_OUTPUT" == "true" ]]; then
            echo '['
        fi

        local first=true
        for workspace in $(find_workspaces); do
            if [[ "$JSON_OUTPUT" == "true" ]] && [[ "$first" == "false" ]]; then
                echo ','
            fi
            run_recovery "$workspace"
            first=false
        done

        if [[ "$JSON_OUTPUT" == "true" ]]; then
            echo ']'
        fi
    else
        # Loop mode
        while true; do
            for workspace in $(find_workspaces); do
                run_recovery "$workspace"
            done

            log "Sleeping for ${INTERVAL_MINUTES} minutes..."
            sleep "$((INTERVAL_MINUTES * 60))"
        done
    fi
}

main
