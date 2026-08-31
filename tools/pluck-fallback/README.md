# Pluck Fallback Tool

Enhanced bead plucking with fallback query strategies for NEEDLE workers.

## Problem

When NEEDLE workers attempt to pluck beads using `bead list --ready`, the primary query can sometimes return zero candidates even when beads actually exist. This creates a starvation condition where:

1. Workers report "no candidates available"
2. Open/in-progress beads exist but are invisible to the ready query
3. Work stops despite available work

Common causes:
- Database corruption or inconsistency
- Query-layer bugs in `bead list --ready`
- Checkpoint out of sync with database
- Stale assignments blocking visibility

## Solution

The pluck fallback tool implements **multiple query strategies** with automatic fallback. If the primary query returns no candidates, it automatically tries alternative query methods to detect and recover from visibility bugs.

## Query Strategies

Executed in order until one succeeds:

### 1. Primary Query (Standard)
```bash
bead list --ready --json
```
The standard NEEDLE pluck query. Returns beads that are truly ready for workers to claim.

### 2. Fallback 1: Open Status Query
```bash
bead list --status open --json
```
Checks if ANY open beads exist. This bypasses the ready frontier logic to detect if the issue is with ready calculation vs. basic visibility.

### 3. Fallback 2: Direct Database Query
```bash
sqlite3 .beads/beads.db 'SELECT id, title, status, assignee, priority FROM issues WHERE status IN (0, 1) LIMIT 50'
```
Queries the database directly, bypassing the bead CLI entirely. Useful when the CLI has a bug but the database is intact.

### 4. Fallback 3: Checkpoint Query
```bash
jq '.issues[] | select(.status == 0 or .status == 1)' .beads/checkpoint/current.json
```
Reads from the durable checkpoint, which is git-tracked and never affected by database corruption. This is the last resort.

## Installation

### Option 1: Build from source (Go)

```bash
cd tools/pluck-fallback
make build
make install  # Requires sudo for /usr/local/bin
```

### Option 2: Use shell script directly

```bash
cd tools/pluck-fallback
chmod +x pluck-fallback.sh
sudo ln -s $(pwd)/pluck-fallback.sh /usr/local/bin/pluck-fallback
```

## Usage

### Basic Usage

```bash
# In any workspace directory
pluck-fallback

# Specify workspace
pluck-fallback --workspace /home/coding/NEEDLE

# Get multiple candidates
pluck-fallback --count 5
```

### JSON Output

```bash
pluck-fallback --json
```

Returns JSON with candidates, which strategy was used, and any discrepancies detected:

```json
{
  "strategy_used": "open_status",
  "candidates": [
    {
      "id": "need-abc12345",
      "title": "Fix memory leak",
      "status": "open",
      "priority": 1,
      "labels": ["bug", "high-priority"],
      "query_source": "open_status"
    }
  ],
  "total_available": 1,
  "discrepancies": [
    "2026-08-31T12:34:56Z - Visibility bug detected: primary query returned 0, but open status query returned 1 candidates"
  ]
}
```

### Diagnostic Mode

```bash
# Enable verbose logging
pluck-fallback --verbose

# Create a diagnostic bead when fallback is triggered
pluck-fallback --create-diagnostic-bead
```

When `--create-diagnostic-bead` is used and a fallback strategy succeeds, the tool automatically creates a bead documenting:
- Timestamp of visibility bug
- Which strategy recovered the candidates
- List of recovered beads
- Relevant metadata

### Shell Script Options

```bash
./pluck-fallback.sh \
    --workspace /home/coding/SEAM \
    --count 3 \
    --json \
    --verbose \
    --create-diagnostic-bead \
    --diagnostic-log .beads/diagnostics/pluck-fallback.log
```

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success, primary query worked (no fallback needed) |
| 2 | Fallback was triggered (visibility bug detected and recovered) |
| 3 | No candidates found by any strategy (true starvation or database corruption) |

## Integration with NEEDLE

### Option 1: Replace pluck command in NEEDLE

Modify NEEDLE's pluck logic to use this tool instead of calling `bead list --ready` directly:

```go
// Instead of:
cmd := exec.Command("bead", "list", "--ready", "--json")

// Use:
cmd := exec.Command("pluck-fallback", "--workspace", workspace, "--json")
```

### Option 2: Shell wrapper

Create a wrapper script that NEEDLE calls:

```bash
#!/usr/bin/env bash
# NEEDLE pluck wrapper

WORKSPACE="$1"
RESULT=$(pluck-fallback --workspace "$WORKSPACE" --json --create-diagnostic-bead)

EXIT_CODE=$?

echo "$RESULT"

# Exit codes 0 and 2 both mean we got candidates
if [[ $EXIT_CODE -le 2 ]]; then
    exit 0
else
    exit 1  # No candidates
fi
```

## Monitoring

### Diagnostic Log

All visibility discrepancies are logged to `.beads/diagnostics/pluck-fallback.log`:

```
2026-08-28T22:08:15.754444944Z | Fallback triggered | Recovered beads: seam-0f0172a0
2026-08-31T12:34:56.123456789Z | Visibility bug detected | Strategy: open_status | Count: 3
```

### Promtail/Grafana Integration

Monitor fallback triggers:

```promql
# Rate of fallback triggers
rate(pluck_fallback_triggered_total[5m])

# Beads recovered per fallback strategy
sum by (strategy) (pluck_fallback_beads_recovered_total)
```

## How It Works

1. **Primary Query**: Executes `bead list --ready --json`
   - If candidates found → return them, exit 0 (success)
   - If no candidates → proceed to fallback

2. **Fallback 1**: Executes `bead list --status open --json`
   - If candidates found → log discrepancy, return them, exit 2 (fallback triggered)
   - If no candidates → proceed to next fallback

3. **Fallback 2**: Queries SQLite database directly
   - If candidates found → log discrepancy, return them, exit 2 (fallback triggered)
   - If no candidates → proceed to next fallback

4. **Fallback 3**: Reads checkpoint JSON
   - If candidates found → log discrepancy, return them, exit 2 (fallback triggered)
   - If no candidates → exit 3 (true failure)

## Automated Recovery

When a fallback is triggered:

1. **Discrepancy logged** to `.beads/diagnostics/pluck-fallback.log`
2. **Diagnostic bead created** (if `--create-diagnostic-bead` enabled)
3. **Candidates returned** to NEEDLE for work to continue
4. **Alert raised** for human investigation

The work continues despite the bug, and the issue is automatically documented for later triage.

## Testing

```bash
# Test primary query success
cd /home/coding/SEAM
pluck-fallback --count 1 --verbose

# Test fallback (simulate empty ready frontier)
# First, create a test scenario:
cd /home/coding/SEAM
bead create --title "Test visibility bug" --priority 2

# Then test plucking (should use fallback if ready is empty)
./tools/pluck-fallback/pluck-fallback.sh --workspace . --verbose

# Test JSON output
./tools/pluck-fallback/pluck-fallback.sh --workspace . --json | jq .

# Test shell script
./tools/pluck-fallback/pluck-fallback.sh --workspace . --count 1
```

## Troubleshooting

### "All strategies failed"

This means no query method found any candidates:

1. Check database exists: `ls -la .beads/beads.db`
2. Check checkpoint exists: `ls -la .beads/checkpoint/current.json`
3. Run `bead doctor` to check for corruption
4. Run `bead list` manually to see CLI errors

### "Fallback triggered but candidates still not picked up by NEEDLE"

This indicates the integration isn't working correctly:

1. Verify NEEDLE is calling `pluck-fallback` instead of `bead list --ready`
2. Check JSON output format matches what NEEDLE expects
3. Look for NEEDLE worker logs showing what it received

### High rate of fallback triggers

If fallbacks are happening frequently:

1. Review diagnostic beads for patterns
2. Check if `bead list --ready` consistently returns different results than fallbacks
4. Consider filing bug against bead CLI if primary query is unreliable

## Architecture

```
┌─────────────────┐
│   NEEDLE Worker │
└────────┬────────┘
         │ requests bead
         ▼
┌─────────────────────────────────────┐
│      Pluck Fallback Tool            │
│                                     │
│  ┌─────────────────────────────┐   │
│  │ Strategy 1: Primary Query    │   │
│  │ bead list --ready --json    │   │
│  └──────────┬──────────────────┘   │
│             │ No candidates?        │
│             ▼                      │
│  ┌─────────────────────────────┐   │
│  │ Strategy 2: Open Status     │   │
│  │ bead list --status open     │   │
│  └──────────┬──────────────────┘   │
│             │ No candidates?        │
│             ▼                      │
│  ┌─────────────────────────────┐   │
│  │ Strategy 3: Direct DB       │   │
│  │ sqlite3 .beads/beads.db     │   │
│  └──────────┬──────────────────┘   │
│             │ No candidates?        │
│             ▼                      │
│  ┌─────────────────────────────┐   │
│  │ Strategy 4: Checkpoint       │   │
│  │ jq current.json              │   │
│  └──────────┬──────────────────┘   │
│             │                      │
└─────────────┼──────────────────────┘
              │
              ▼
      ┌───────────────┐
      │ Return beads │
      │ to NEEDLE    │
      └───────────────┘
```

## See Also

- `tools/starvation-recovery/` - Automated recovery for starvation conditions
- `tools/bead_visibility_diagnostic.py` - Diagnostic tool for visibility issues
- `internal/server/starvation_recovery_loop.go` - SEAM-integrated recovery loop
- `docs/notes/transient-starvation-backoff-implementation.md` - NEEDLE's starvation handling
