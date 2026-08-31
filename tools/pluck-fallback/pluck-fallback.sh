#!/usr/bin/env bash
#
# pluck-fallback.sh
#
# Enhanced bead plucking with fallback query strategies.
#
# When the primary query (bead list --ready) returns no candidates, this script
# falls back to alternative query methods to detect and recover from visibility bugs.
#
# Usage: pluck-fallback.sh [--workspace /path] [--count N] [--json] [--verbose] [--create-diagnostic-bead]
#

set -euo pipefail

# Configuration
WORKSPACE="${WORKSPACE:-.}"
COUNT="${COUNT:-1}"
JSON_OUTPUT=false
VERBOSE=false
CREATE_BEAD=false
DIAGNOSTIC_LOG="${DIAGNOSTIC_LOG:-.beads/diagnostics/pluck-fallback.log}"
TIMESTAMP=$(date -Iseconds)

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging functions
log() {
    if [[ "$VERBOSE" == "true" ]]; then
        echo -e "${BLUE}[$(date -Iseconds)]${NC} $*" >&2
    fi
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*" >&2
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*" >&2
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $*" >&2
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --workspace)
            WORKSPACE="$2"
            shift 2
            ;;
        --count)
            COUNT="$2"
            shift 2
            ;;
        --json)
            JSON_OUTPUT=true
            shift
            ;;
        --verbose)
            VERBOSE=true
            shift
            ;;
        --create-diagnostic-bead)
            CREATE_BEAD=true
            shift
            ;;
        --diagnostic-log)
            DIAGNOSTIC_LOG="$2"
            shift 2
            ;;
        --help|-h)
            cat <<EOF
Usage: $0 [OPTIONS]

Enhanced bead plucking with fallback query strategies.

Options:
  --workspace PATH         Workspace directory (default: current directory)
  --count N                Number of beads to return (default: 1)
  --json                   Output results in JSON format
  --verbose                Enable verbose logging
  --create-diagnostic-bead Create a diagnostic bead when fallback is triggered
  --diagnostic-log PATH    Path to diagnostic log file (default: .beads/diagnostics/pluck-fallback.log)
  --help, -h               Show this help message

Query Strategies (executed in order):
  1. Primary:   bead list --ready --json
  2. Fallback 1: bead list --status open --json
  3. Fallback 2: sqlite3 .beads/beads.db
  4. Fallback 3: jq .beads/checkpoint/current.json

Exit Codes:
  0 - Primary query succeeded (no fallback needed)
  2 - Fallback was triggered (visibility bug detected)
  3 - No candidates found by any strategy

Example:
  $0 --workspace /home/coding/SEAM --count 1 --json
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

# Ensure diagnostic log directory exists
mkdir -p "$(dirname "$DIAGNOSTIC_LOG")"

# ============================================================================
# Strategy 1: Primary query (bead list --ready)
# ============================================================================

log "Strategy 1: Primary query (bead list --ready --json)"
PRIMARY_RESULTS=$(bead list --ready --json 2>/dev/null || echo "")
PRIMARY_COUNT=$(echo "$PRIMARY_RESULTS" | jq -r 'length' 2>/dev/null | tr -d '\n' || echo "0")
PRIMARY_COUNT=$(echo "$PRIMARY_COUNT" | grep -oE '^[0-9]+$' || echo "0")

log "Primary query returned: $PRIMARY_COUNT candidates"

if [[ "$PRIMARY_COUNT" -gt 0 ]]; then
    log_success "Primary query succeeded"

    if [[ "$JSON_OUTPUT" == "true" ]]; then
        # Convert JSONL to array for JSON output
        echo "$PRIMARY_RESULTS" | jq -s --arg strategy "primary" --argjson total "$PRIMARY_COUNT" \
            '{
                strategy_used: $strategy,
                candidates: .,
                total_available: $total,
                discrepancies: []
            }'
    else
        echo "Strategy used: primary"
        echo "Candidates returned: $PRIMARY_COUNT"
        echo "$PRIMARY_RESULTS" | jq -r '"\(.id) [\(.status)] - \(.title) (priority \(.priority))"' 2>/dev/null || \
            echo "$PRIMARY_RESULTS"
    fi

    exit 0
fi

# ============================================================================
# Strategy 2: Fallback to open status query
# ============================================================================

log "Strategy 2: Fallback to open status query"
FALLBACK1_RESULTS=$(bead list --status open --json 2>/dev/null || echo "")
FALLBACK1_COUNT=$(echo "$FALLBACK1_RESULTS" | jq -r 'length' 2>/dev/null | tr -d '\n' || echo "0")
FALLBACK1_COUNT=$(echo "$FALLBACK1_COUNT" | grep -oE '^[0-9]+$' || echo "0")

log "Fallback 1 returned: $FALLBACK1_COUNT candidates"

if [[ "$FALLBACK1_COUNT" -gt 0 ]]; then
    DISCREPANCY="[$TIMESTAMP] Visibility bug detected: primary query returned 0, but open status query returned $FALLBACK1_COUNT candidates"
    log_warning "$DISCREPANCY"
    echo "$DISCREPANCY" >> "$DIAGNOSTIC_LOG"

    # Log recovered beads
    echo "$FALLBACK1_RESULTS" | jq -r '.[] | "  - Recovered bead: \(.id) (\(.title))"' >> "$DIAGNOSTIC_LOG"

    if [[ "$JSON_OUTPUT" == "true" ]]; then
        echo "$FALLBACK1_RESULTS" | jq -s -c --arg strategy "open_status" --argjson total "$FALLBACK1_COUNT" \
            '{
                strategy_used: $strategy,
                candidates: .,
                total_available: $total,
                discrepancies: ["'"$DISCREPANCY"'"]
            }'
    else
        echo "Strategy used: open_status (FALLBACK)"
        echo "Candidates returned: $FALLBACK1_COUNT"
        echo "$FALLBACK1_RESULTS" | jq -r '"\(.id) [\(.status)] - \(.title) (priority \(.priority))"' 2>/dev/null || \
            echo "$FALLBACK1_RESULTS"
        echo ""
        echo "Visibility discrepancies detected:"
        echo "  - $DISCREPANCY"
    fi

    if [[ "$CREATE_BEAD" == "true" ]]; then
        create_diagnostic_bead "open_status" "$DISCREPANCY" "$FALLBACK1_RESULTS"
    fi

    exit 2
fi

# ============================================================================
# Strategy 3: Direct database query
# ============================================================================

log "Strategy 3: Direct database query"

if [[ -f ".beads/beads.db" ]]; then
    # Query for open (status=0) or in_progress (status=1) beads
    FALLBACK2_RESULTS=$(sqlite3 .beads/beads.db "SELECT id, title, status, assignee, priority FROM issues WHERE status IN (0, 1) LIMIT 50;" 2>/dev/null || echo "")
    FALLBACK2_COUNT=$(echo "$FALLBACK2_RESULTS" | wc -l)

    log "Fallback 2 returned: $FALLBACK2_COUNT lines"

    if [[ "$FALLBACK2_COUNT" -gt 0 ]]; then
        DISCREPANCY="[$TIMESTAMP] Visibility bug detected: primary and open status queries returned 0, but direct DB query returned results"
        log_warning "$DISCREPANCY"
        echo "$DISCREPANCY" >> "$DIAGNOSTIC_LOG"

        # Convert DB output to JSON
        FALLBACK2_JSON=$(echo "$FALLBACK2_RESULTS" | while IFS='|' read -r id title status assignee priority; do
            echo "{\"id\":\"$id\",\"title\":\"$title\",\"status\":\"$status\",\"assignee\":\"$assignee\",\"priority\":$priority,\"query_source\":\"direct_db\"}"
        done | jq -s . 2>/dev/null || echo "[]")

        if [[ "$JSON_OUTPUT" == "true" ]]; then
            echo "$FALLBACK2_JSON" | jq -c --arg strategy "direct_db" \
                '{
                    strategy_used: $strategy,
                    candidates: .,
                    total_available: (. | length),
                    discrepancies: ["'"$DISCREPANCY"'"]
                }'
        else
            echo "Strategy used: direct_db (FALLBACK)"
            echo "$FALLBACK2_RESULTS" | while IFS='|' read -r id title status assignee priority; do
                echo "$id [$status] - $title (priority $priority)"
            done
            echo ""
            echo "Visibility discrepancies detected:"
            echo "  - $DISCREPANCY"
        fi

        if [[ "$CREATE_BEAD" == "true" ]]; then
            create_diagnostic_bead "direct_db" "$DISCREPANCY" "$FALLBACK2_JSON"
        fi

        exit 2
    fi
else
    log "Database file not found: .beads/beads.db"
fi

# ============================================================================
# Strategy 4: Checkpoint query
# ============================================================================

log "Strategy 4: Checkpoint query"

if [[ -f ".beads/checkpoint/current.json" ]]; then
    FALLBACK3_RESULTS=$(jq '.issues[] | select(.status == 0 or .status == 1)' .beads/checkpoint/current.json 2>/dev/null || echo "")
    FALLBACK3_COUNT=$(echo "$FALLBACK3_RESULTS" | jq -s 'length' 2>/dev/null || echo "0")

    log "Fallback 3 returned: $FALLBACK3_COUNT candidates"

    if [[ "$FALLBACK3_COUNT" -gt 0 ]]; then
        DISCREPANCY="[$TIMESTAMP] Visibility bug detected: all bead queries failed, but checkpoint contains $FALLBACK3_COUNT open/in_progress beads"
        log_warning "$DISCREPANCY"
        echo "$DISCREPANCY" >> "$DIAGNOSTIC_LOG"

        # Convert to expected format
        FALLBACK3_JSON=$(echo "$FALLBACK3_RESULTS" | jq '{
            id: .id,
            title: .title,
            status: (if .status == 0 then "open" elif .status == 1 then "in_progress" else "unknown" end),
            assignee: (.assignee // ""),
            priority: .priority,
            labels: (.labels // []),
            query_source: "checkpoint"
        }' | jq -s . 2>/dev/null || echo "[]")

        if [[ "$JSON_OUTPUT" == "true" ]]; then
            echo "$FALLBACK3_JSON" | jq -c --arg strategy "checkpoint" \
                '{
                    strategy_used: $strategy,
                    candidates: .,
                    total_available: (. | length),
                    discrepancies: ["'"$DISCREPANCY"'"]
                }'
        else
            echo "Strategy used: checkpoint (FALLBACK)"
            echo "$FALLBACK3_RESULTS" | jq -r '"\(.id) [\(.status)] - \(.title) (priority \(.priority))"' 2>/dev/null
            echo ""
            echo "Visibility discrepancies detected:"
            echo "  - $DISCREPANCY"
        fi

        if [[ "$CREATE_BEAD" == "true" ]]; then
            create_diagnostic_bead "checkpoint" "$DISCREPANCY" "$FALLBACK3_JSON"
        fi

        exit 2
    fi
else
    log "Checkpoint file not found: .beads/checkpoint/current.json"
fi

# ============================================================================
# All strategies failed
# ============================================================================

log_error "All query strategies failed - no candidates found"

if [[ "$JSON_OUTPUT" == "true" ]]; then
    echo '{"strategy_used":"none","candidates":[],"total_available":0,"discrepancies":[]}'
else
    echo "Strategy used: none"
    echo "Candidates returned: 0"
    echo "ERROR: No candidates found by any query strategy"
fi

exit 3

# ============================================================================
# Helper function: create diagnostic bead
# ============================================================================

create_diagnostic_bead() {
    local strategy="$1"
    local discrepancy="$2"
    local results="$3"

    log "Creating diagnostic bead for visibility bug..."

    BEAD_ID=$(bead create \
        --title "[Visibility Bug] Primary query failed, $strategy recovered candidates" \
        --priority 1 \
        --issue-type task \
        --label visibility-bug,auto-detected,pluck-fallback \
        2>&1 | grep -oE 'seam-[a-f0-9]{8}' | head -1 || true)

    if [[ -n "$BEAD_ID" ]]; then
        log "Created diagnostic bead: $BEAD_ID"

        NOTES="Visibility bug detected at $TIMESTAMP

**Strategy Used:** $strategy

**Discrepancy:**
$discrepancy

**Recovered Beads:**
$results

---
Generated by: tools/pluck-fallback/pluck-fallback.sh
Workspace: $WORKSPACE"

        bead update "$BEAD_ID" --notes "$NOTES" 2>&1 | tee -a >(if [[ "$VERBOSE" == "true" ]]; then cat >&2; fi)
        log_success "Diagnostic bead created: $BEAD_ID"
    else
        log_error "Failed to create diagnostic bead"
    fi
}
