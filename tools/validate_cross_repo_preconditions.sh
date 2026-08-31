#!/usr/bin/env bash
#
# validate_cross_repo_preconditions.sh
#
# Scans beads for documented cross-repo preconditions and validates them.
# Beads with unmet preconditions are marked as manual_blocked=true.
#
# Usage: tools/validate_cross_repo_preconditions.sh [--dry-run] [--verbose]
#
# Precondition format in bead descriptions:
#   ## Precondition (cross-repo, cannot be a blocks edge)
#   <workspace> bead "<pattern>" is merged/closed
#
# Examples:
#   declarative-config bead "github-commit-status: post iad-ci results" is merged
#   NEEDLE bead "needle-abc123" is closed
#

set -euo pipefail

DRY_RUN=false
VERBOSE=false
WORKSPACE_ROOT="/home/coding"
CURRENT_WORKSPACE="$(basename "$PWD")"

log() {
    echo "[$(date -Iseconds)] $*" >&2
}

verbose() {
    if [[ "$VERBOSE" == "true" ]]; then
        log "$@"
    fi
}

usage() {
    cat <<EOF
Usage: $0 [--dry-run] [--verbose]

Scans beads for documented cross-repo preconditions and validates them.
Beads with unmet preconditions are marked as manual_blocked=true.

Options:
  --dry-run    Show what would be done without making changes
  --verbose    Show detailed processing information

Precondition format in bead descriptions:
  ## Precondition (cross-repo, cannot be a blocks edge)
  <workspace> bead "<pattern>" is merged/closed
EOF
    exit 1
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --verbose)
            VERBOSE=true
            shift
            ;;
        --help|-h)
            usage
            ;;
        *)
            log "Unknown option: $1"
            usage
            ;;
    esac
done

log "Starting cross-repo precondition validation"
log "Current workspace: $CURRENT_WORKSPACE"

# Create temporary file for results tracking
RESULT_FILE=$(mktemp)
echo "TOTAL_BEADS=0" > "$RESULT_FILE"
echo "BEADS_WITH_PRECONDS=0" >> "$RESULT_FILE"
echo "PRECONDS_MET=0" >> "$RESULT_FILE"
echo "PRECONDS_UNMET=0" >> "$RESULT_FILE"
echo "ERRORS=0" >> "$RESULT_FILE"

increment_stat() {
    local stat_name="$1"
    local current_value
    current_value=$(grep "^${stat_name}=" "$RESULT_FILE" | cut -d= -f2)
    local new_value=$((current_value + 1))
    # Update the line in place
    sed -i "s/^${stat_name}=.*/${stat_name}=${new_value}/" "$RESULT_FILE"
}

# Function to check if a bead exists in another workspace
# Returns: 0 if bead exists and is closed, 1 if exists but open, 2 if not found, 3 on error
check_cross_workspace_bead() {
    local workspace="$1"
    local pattern="$2"

    verbose "Checking workspace '$workspace' for pattern '$pattern'"

    local workspace_path="$WORKSPACE_ROOT/$workspace"

    # Check if workspace exists
    if [[ ! -d "$workspace_path" ]]; then
        log "Warning: Workspace '$workspace' not found at $workspace_path"
        return 2
    fi

    # Check if workspace has bead system
    if [[ ! -f "$workspace_path/.beads/beads.db" ]]; then
        verbose "Workspace '$workspace' has no bead database"
        return 2
    fi

    # Try to find the bead by ID or title pattern
    # First, check if pattern looks like a bead ID (e.g., "needle-abc123" or "declarative-abc123")
    if [[ "$pattern" =~ ^[a-z]+-[a-f0-9]{6,}$ ]]; then
        verbose "Pattern looks like bead ID: $pattern"
        if (cd "$workspace_path" && bead show "$pattern" &>/dev/null); then
            verbose "Found bead by ID: $pattern"
            # Check if closed
            if (cd "$workspace_path" && bead show "$pattern" 2>/dev/null | grep -q "^Status: Closed"); then
                verbose "Bead $pattern is closed"
                return 0
            else
                verbose "Bead $pattern exists but is not closed"
                return 1
            fi
        fi
    fi

    # Try to find by title pattern
    verbose "Searching for beads matching title pattern: $pattern"
    local bead_ids
    bead_ids=$(cd "$workspace_path" && bead list 2>/dev/null | grep -i "$pattern" | awk '{print $2}' | tr -d ':' || true)

    if [[ -z "$bead_ids" ]]; then
        verbose "No beads found matching pattern: $pattern"
        return 2
    fi

    # Check each matching bead
    while IFS= read -r bead_id; do
        [[ -z "$bead_id" ]] && continue
        verbose "Checking bead: $bead_id"

        if (cd "$workspace_path" && bead show "$bead_id" 2>/dev/null | grep -q "^Status: Closed"); then
            log "Found closed bead: $workspace/$bead_id matching pattern '$pattern'"
            return 0
        fi
    done <<< "$bead_ids"

    verbose "Found matching beads but none are closed"
    return 1
}

# Get all open beads
log "Fetching all open beads..."
local_beads_json=$(bead list --status open --json 2>/dev/null || echo "[]")

if [[ "$local_beads_json" == "[]" ]] || [[ -z "$local_beads_json" ]]; then
    log "No open beads found"
    rm -f "$RESULT_FILE"
    exit 0
fi

# Normalize JSON to always be an array (handle single object case)
json_type=$(echo "$local_beads_json" | jq -r 'type' 2>/dev/null || echo "unknown")
if [[ "$json_type" == "object" ]]; then
    # Single bead - wrap in array
    local_beads_json=$(echo "$local_beads_json" | jq -c '[.]' 2>/dev/null)
fi

# Use jq to process and output a CSV format we can easily read
echo "$local_beads_json" | jq -r '.[] | [.id, .title, .description, (.manual_blocked // false)] | @csv' 2>/dev/null | while IFS=, read -r bead_id_csv bead_title_csv bead_desc_csv bead_blocked_csv; do
    # Remove quotes from CSV fields
    bead_id=$(echo "$bead_id_csv" | tr -d '"')
    bead_title=$(echo "$bead_title_csv" | tr -d '"')
    bead_desc=$(echo "$bead_desc_csv" | sed 's/^"//;s/"$//' | sed 's/\\"/"/g')
    bead_blocked=$(echo "$bead_blocked_csv" | tr -d '"')

    verbose "Checking bead: $bead_id - $bead_title"

    # Increment total count
    increment_stat "TOTAL_BEADS"

    # Check if description contains precondition section
    if ! echo "$bead_desc" | grep -qi "precondition.*cross-repo"; then
        verbose "No cross-repo precondition found in $bead_id"
        continue
    fi

    increment_stat "BEADS_WITH_PRECONDS"
    log "Found cross-repo precondition in $bead_id"

    # Extract precondition lines
    # Pattern: "<workspace> bead "<pattern>" is merged/closed"
    precond_lines=$(echo "$bead_desc" | grep -iA5 "precondition.*cross-repo" | grep -iE "^[[:space:]]*[a-z-]+[[:space:]]+bead[[:space:]]+\".*\"[[:space:]]+is[[:space:]]+(merged|closed)" || true)

    if [[ -z "$precond_lines" ]]; then
        verbose "Could not parse precondition pattern from $bead_id"
        increment_stat "ERRORS"
        continue
    fi

    # Parse each precondition line
    precond_met=true
    precond_details=()

    while IFS= read -r line; do
        [[ -z "$line" ]] && continue

        verbose "Parsing precondition line: $line"

        # Extract workspace and pattern
        # Format: "declarative-config bead "github-commit-status: ..." is merged"
        if [[ "$line" =~ ^[[:space:]]*([a-z0-9-]+)[[:space:]]+bead[[:space:]]+\"([^\"]+)\"[[:space:]]+is[[:space:]]+(merged|closed) ]]; then
            workspace="${BASH_REMATCH[1]}"
            pattern="${BASH_REMATCH[2]}"
            condition="${BASH_REMATCH[3]}"

            log "Parsed precondition: $workspace bead \"$pattern\" is $condition"

            # Check if precondition is met
            if check_cross_workspace_bead "$workspace" "$pattern"; then
                increment_stat "PRECONDS_MET"
                precond_details+=("✓ $workspace: \"$pattern\"")
                verbose "Precondition met: $workspace/$pattern"
            else
                case $? in
                    1)
                        precond_met=false
                        precond_details+=("✗ $workspace: \"$pattern\" (exists but not $condition)")
                        log "Precondition NOT met: $workspace/$pattern exists but is not $condition"
                        ;;
                    2)
                        precond_met=false
                        precond_details+=("✗ $workspace: \"$pattern\" (not found)")
                        log "Precondition NOT met: $workspace/$pattern not found"
                        ;;
                    3)
                        increment_stat "ERRORS"
                        precond_details+=("⚠ $workspace: \"$pattern\" (error checking)")
                        log "Error checking precondition: $workspace/$pattern"
                        ;;
                esac
                increment_stat "PRECONDS_UNMET"
            fi
        else
            verbose "Could not parse line: $line"
            increment_stat "ERRORS"
        fi
    done <<< "$precond_lines"

    # Take action based on precondition status
    if [[ "$precond_met" == "false" ]]; then
        if [[ "$bead_blocked" == "true" ]]; then
            log "Bead $bead_id already marked as manual_blocked"
        else
            log "Bead $bead_id has unmet preconditions, marking as manual_blocked"
            echo "Precondition details for $bead_id:"
            printf '  %s\n' "${precond_details[@]}"

            if [[ "$DRY_RUN" == "false" ]]; then
                # Build notes with precondition details
                notes="Automatically blocked by cross-repo precondition validator. Unmet preconditions:
$(printf '  %s\n' "${precond_details[@]}")
Run: tools/validate_cross_repo_preconditions.sh to recheck"

                if bead update "$bead_id" --manual-blocked --notes "$notes"; then
                    log "✓ Marked $bead_id as manual_blocked"
                else
                    log "✗ Failed to mark $bead_id as manual_blocked"
                    increment_stat "ERRORS"
                fi
            else
                log "[DRY-RUN] Would mark $bead_id as manual_blocked"
            fi
        fi
    else
        log "✓ All preconditions met for $bead_id"
        if [[ "$bead_blocked" == "true" ]]; then
            log "Bead $bead_id is manual_blocked but preconditions are now met"
            if [[ "$DRY_RUN" == "false" ]]; then
                if bead update "$bead_id" --clear-manual-blocked --notes "Preconditions revalidated and met. Bead is now eligible for work."; then
                    log "✓ Cleared manual_blocked on $bead_id"
                else
                    log "✗ Failed to clear manual_blocked on $bead_id"
                    increment_stat "ERRORS"
                fi
            else
                log "[DRY-RUN] Would clear manual_blocked on $bead_id"
            fi
        fi
    fi
done

# Read final statistics
TOTAL_BEADS=$(grep "^TOTAL_BEADS=" "$RESULT_FILE" | cut -d= -f2)
BEADS_WITH_PRECONDS=$(grep "^BEADS_WITH_PRECONDS=" "$RESULT_FILE" | cut -d= -f2)
PRECONDS_MET=$(grep "^PRECONDS_MET=" "$RESULT_FILE" | cut -d= -f2)
PRECONDS_UNMET=$(grep "^PRECONDS_UNMET=" "$RESULT_FILE" | cut -d= -f2)
ERRORS=$(grep "^ERRORS=" "$RESULT_FILE" | cut -d= -f2)

# Clean up temp file
rm -f "$RESULT_FILE"

# Print summary
log "=== Validation complete ==="
log "Total beads processed: $TOTAL_BEADS"
log "Beads with preconditions: $BEADS_WITH_PRECONDS"
log "Preconditions met: $PRECONDS_MET"
log "Preconditions unmet: $PRECONDS_UNMET"
log "Errors: $ERRORS"

if [[ "$DRY_RUN" == "true" ]]; then
    log "[DRY-RUN] No changes were made"
fi

exit 0
