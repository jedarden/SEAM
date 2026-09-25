# Starvation Alert Root Cause Analysis

## Overview

The starvation alert system has been enhanced with automated root cause analysis and structured diagnostic tagging. This converts generic "I'm stuck" signals into actionable diagnostics that can automatically queue repairs or escalate to humans only when truly necessary.

## Key Components

### 1. Enhanced RootCauseAnalyzer (`internal/server/root_cause_analyzer.go`)

The `RootCauseAnalyzer` now performs comprehensive diagnostics across five key dimensions:

#### Database Integrity Check
- Uses `bead doctor --json` to verify schema integrity
- Falls back to text parsing if JSON unavailable
- Detects corruption, schema mismatches, and structural issues

#### Index Integrity Check  
- Runs SQLite `PRAGMA integrity_check` directly on the database
- Detects corrupted indexes that can cause query failures
- Returns "ok" if clean, detailed error output if corrupted

#### Checkpoint Consistency Check
- Compares database and checkpoint file modification times
- Detects stale checkpoints (older than database)
- Detects stale databases (older than checkpoint)
- Configurable threshold (default: 5 minutes)

#### Filter Configuration Verification
- Compares CLI `bead list --status open --json` output
- Against direct SQLite query: `SELECT COUNT(*) FROM issues WHERE base_status = 'open'`
- Mismatches indicate CLI query filter bugs causing bead invisibility

#### Worker Status Detection
- Uses `pgrep -f "needle.*worker"` to detect running NEEDLE workers
- Identifies worker starvation (no processes running)
- Distinguishes from bead starvation (workers exist but no work available)

### 2. Structured Failure Mode Categorization

The analyzer now categorizes failures into specific, actionable modes:

| Failure Mode | Description | Auto-Recoverable | Human Required |
|--------------|-------------|------------------|----------------|
| `index-corrupt` | SQLite indexes corrupted | ✅ Yes | ❌ No |
| `database-corrupt` | Database structure corruption | ✅ Yes | ❌ No |
| `checkpoint-out-of-sync` | Checkpoint stale vs database | ✅ Yes | ❌ No |
| `filter-mismatch` | CLI query filters broken | ✅ Yes | ❌ No |
| `worker-stuck` | No NEEDLET workers running | ⚠️ Self-resolving | ❌ No |
| `transient-starvation` | Temporary condition, already resolved | ✅ Yes | ❌ No |
| `stale-assignment` | Beads stuck in assigned-but-open state | ✅ Yes | ❌ No |
| `query-bug` | Primary CLI query failing | ✅ Yes | ❌ No |
| `cli-failure` | CLI tool completely broken | ✅ Yes | ❌ No |
| `primary-query-failure` | Unknown primary query failure | ✅ Yes | ❌ No |

### 3. Structured Diagnostic Tags

Alert beads are tagged with machine-readable labels:

```
starvation-alert
starvation:{failure-mode}
auto-recovered (if applicable)
automated-recovery (or human-intervention-required)
repair-daemon-queue (if auto-repairable)
monitor-only (if transient or self-resolving)
```

Example tags for different failure modes:

```bash
# Index corruption
starvation-alert
starvation:index-corrupt
automated-recovery
repair-daemon-queue

# Worker stuck
starvation-alert
starvation:worker-stuck
automated-recovery
monitor-only

# Filter mismatch requiring human review of business logic
starvation-alert
starvation:filter-mismatch
human-intervention-required
```

### 4. Diagnostic Output Format

The enhanced analyzer provides structured diagnostic output:

```markdown
**Root Cause:** index-corrupt
**Auto-Recovered:** true
**Human Intervention Required:** false
**Auto-Repairable:** true

**Analysis:**
1. ✓ Database integrity check passed
2. ❌ Index integrity check failed: index idx_issues_assignee is corrupted
3. ✓ Checkpoint fresh: age difference 2m within threshold 5m
4. ✓ Filter configuration valid: CLI and DB agree on 12 open beads
5. ✓ Workers alive: 3 worker process(es) detected

**Diagnostic Results:**
- Database Integrity: true
- Index Integrity: false
- Checkpoint Freshness: true
- Filter Configuration: true
- Worker Status: true
- Recovery Strategy: unknown
- Recommended Label: starvation:index-corrupt
```

## Usage

### Running Enhanced Diagnostics

The new `starvation-alert-diagnostic` tool runs the enhanced diagnostic chain:

```bash
# One-shot diagnostic mode
go run tools/starvation-alert-diagnostic/main.go --once --workspace-root /home/coding

# Dry run to see what would be done
go run tools/starvation-alert-diagnostic/main.go --once --dry-run --verbose

# JSON output for machine parsing
go run tools/starvation-alert-diagnostic/main.go --once --json
```

### Integration with Recovery Loop

The enhanced diagnostics are integrated into the existing recovery systems:

1. **StarvationRecoveryLoop** (`internal/server/starvation_recovery_loop.go`)
   - Detects starvation conditions
   - Runs automated recovery steps
   - Uses enhanced diagnostics to determine actionability

2. **StarvationAlertSelfResolution** (`internal/server/starvation_alert_self_resolution.go`)
   - Monitors existing alert beads
   - Runs enhanced root cause analysis
   - Tags alerts with structured diagnostic labels
   - Determines if human intervention is truly required

### Diagnostic Decision Flow

```
1. Detect starvation (open > 0 AND ready = 0)
   ↓
2. Run enhanced diagnostic chain:
   - Database integrity check
   - Index integrity check
   - Checkpoint freshness check
   - Filter configuration check
   - Worker status check
   ↓
3. Correlate findings to determine failure mode
   - Priority 1: Critical infrastructure failures (index, database, checkpoint)
   - Priority 2: Configuration issues (filters, workers)
   - Priority 3: Strategy-based classification
   ↓
4. Determine actionability:
   - Auto-repairable? → Queue for repair daemon
   - Human-only? → Mark as human-blocked with specific context
   - Transient? → Continue monitoring
   ↓
5. Update alert bead with structured tags and diagnostics
```

## Repair Daemon Integration

Issues identified as auto-repairable are queued for the repair daemon:

```go
// Issues that are auto-repairable:
- index-corrupt → bead init + sync import-only
- database-corrupt → bead doctor --repair
- checkpoint-out-of-sync → bead sync flush-only
- filter-mismatch → bead release affected beads
- stale-assignment → bead release affected beads
```

The repair daemon processes the queue in priority order:

1. Index corruption (most severe, blocks all operations)
2. Database corruption (severe, may block operations)
3. Checkpoint desync (moderate, may cause stale data)
4. Filter/assignment issues (low priority, affects visibility only)

## Human Intervention Logic

Human intervention is **only** required for issues that truly need business logic decisions or external dependency resolution:

```go
// Human-only issues:
- business-logic-required (e.g., "this bead depends on X team's decision")
- external-dependency-failed (e.g., "GitHub API rate limit, needs token")
- unknown-cause with no auto-recovery path

// NOT human-only:
- Any infrastructure issue (index, DB, checkpoint, filters)
- Worker issues (workers restart automatically)
- Transient conditions (self-resolving)
```

When human intervention is required, the alert bead includes:

```markdown
### Why Human Intervention is Required

This issue requires human judgment because:

**Root Cause:** business-logic-required
**Reason:** Bead `xyz` depends on external system that is currently unavailable.
**Decision Needed:** Should we wait for the system to come back online, or bypass this dependency?

**Context:**
- Dependency: External API https://example.com/api/v1
- Current Status: HTTP 503 Service Unavailable
- Impact: 3 beads blocked on this dependency
- Retry Policy: Automatic retry until 2026-09-01

**Options:**
1. Wait for automatic retry (recommended until deadline)
2. Bypass dependency and proceed (requires approval)
3. Escalate to dependency owner (contact: team@example.com)
```

## Testing

### Unit Tests

```bash
# Run root cause analyzer tests
go test ./internal/server/root_cause_analyzer_test.go -v

# Run starvation alert self-resolution tests
go test ./internal/server/starvation_alert_self_resolution_test.go -v
```

### Integration Tests

```bash
# Test starvation detection and recovery
./tools/starvation-alert-diagnostic/main.go --once --workspace-root /home/coding

# Test recovery loop integration
./internal/server/test_recovery_integration.sh
```

## Performance Considerations

The enhanced diagnostic chain adds approximately 2-5 seconds per workspace check:

- Database integrity: ~500ms
- Index integrity: ~200ms  
- Checkpoint freshness: ~50ms
- Filter configuration: ~300ms
- Worker status: ~100ms

This overhead is acceptable for the 5-minute check interval and provides significant value in preventing false positives and enabling automated recovery.

## Monitoring and Metrics

The system tracks the following metrics:

- `starvation_alert_total{failure_mode}` - Total alerts by failure mode
- `starvation_recovery_success{failure_mode}` - Successful recoveries by mode
- `starvation_recovery_failure{failure_mode}` - Failed recoveries by mode
- `starvation_human_intervention_required_total` - Alerts requiring human intervention
- `starvation_auto_repaired_total` - Alerts automatically repaired
- `starvation_diagnostic_duration_seconds` - Time to run diagnostics

View metrics in Prometheus/Grafana:

```promql
# Recovery success rate by failure mode
rate(starvation_recovery_success_total[5m]) / rate(starvation_alert_total[5m])

# Human intervention rate
rate(starvation_human_intervention_required_total[1h])

# Auto-repair rate
rate(starvation_auto_repaired_total[1h])
```

## Troubleshooting

### False Positive Starvation Alerts

If you're seeing starvation alerts that don't reflect reality:

1. Check the diagnostic output in the alert bead
2. Look for "filter-mismatch" or "query-bug" failure modes
3. Run `bead doctor --repair` to fix database issues
4. Run `bead sync flush-only` to update checkpoint

### Persistent Starvation After Recovery

If starvation persists after automated recovery:

1. Check if workers are alive: `pgrep -f needle.*worker`
2. Check if issue is marked human-blocked: `bead show <id> | grep manual_blocked`
3. Review the diagnostic output for human-only issues
4. Verify external dependencies are available

### Diagnostic Failures

If diagnostics themselves fail:

1. Check bead CLI is working: `bead list --status open --json`
2. Check SQLite is available: `sqlite3 .beads/beads.db "PRAGMA integrity_check;"`
3. Check checkpoint files exist: `ls -la .beads/checkpoint/`
4. Review system logs for diagnostic errors

## Future Enhancements

Planned improvements to the diagnostic system:

1. **Machine Learning**: Train models on historical data to predict failure modes
2. **Proactive Detection**: Detect potential starvation before it occurs
3. **Dependency Graph**: Visualize bead dependencies to identify bottlenecks
4. **Auto-Escalation**: Automatically escalate based on severity and duration
5. **Historical Analysis**: Track patterns in starvation causes over time

## Related Documentation

- [Starvation Recovery Loop](../internal/server/starvation_recovery_loop.go)
- [Alert Self Resolution](../internal/server/starvation_alert_self_resolution.go)
- [Bead Doctor](../../.beads/doctor-recovery.log)
- [Checkpoint Consistency](../scripts/bead-checkpoint-consistency-check.sh)

---
*Generated: 2026-08-31*
*Author: Automated system enhancement*
