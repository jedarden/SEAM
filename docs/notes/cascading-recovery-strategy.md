# Cascading Recovery Strategy for Unknown-Cause Starvation

## Overview

The cascading recovery strategy is an automated system that attempts to recover from starvation incidents when the RootCauseAnalyzer returns 'unknown-cause' or when confidence is below the threshold. This converts unknown failures into educated guesses that often succeed without human intervention.

## Problem Statement

When SEAM's RootCauseAnalyzer cannot determine the root cause of a starvation incident (returns 'unknown-cause'), the previous behavior was to:

1. Mark the bead as `human-intervention-required`
2. Block automated recovery
3. Wait for manual investigation

This created operational overhead and delayed recovery for common, fixable issues.

## Solution: Cascading Recovery

The cascading recovery system tries all documented auto-recovery strategies **in order of severity** (least invasive first). After each attempt, it verifies whether starvation is resolved. If any strategy succeeds, the bead is updated with the inferred root cause and the human intervention label is removed.

## Recovery Strategies (in execution order)

### 1. Database Repair (`database-repair`)
- **Command**: `bead doctor --repair`
- **Inferred Root Cause**: `database-corrupt`
- **Severity**: Low - safe, non-destructive
- **What it fixes**: Database corruption, schema inconsistencies, index issues

### 2. Checkpoint Sync (`checkpoint-sync`)
- **Command**: `bead sync flush-only`
- **Inferred Root Cause**: `checkpoint-out-of-sync`
- **Severity**: Low - safe, idempotent
- **What it fixes**: Checkpoint desync, stale state, inconsistent database/checkpoint relationship

### 3. Bead Release (`bead-release`)
- **Command**: `bead list --status open --json` (then releases stale assignments)
- **Inferred Root Cause**: `stale-assignment`
- **Severity**: Medium - affects multiple beads
- **What it fixes**: Assigned-but-open beads (known starvation bug), stale worker assignments, filter mismatches

### 4. Index Rebuild (`index-rebuild`)
- **Command**: `bead init`
- **Inferred Root Cause**: `index-corrupt`
- **Severity**: **HIGH** - most invasive, rebuilds entire database from checkpoint
- **What it fixes**: Database index corruption, completely broken database structure
- **Note**: Only attempted if all other strategies fail

## Verification After Each Attempt

After each recovery strategy is executed, the system:

1. Runs `bead list --ready --json` to count ready beads
2. Compares the ready count before and after the attempt
3. Considers recovery successful if ready beads are now available (> 0)

This verification ensures that:
- We don't claim success when the issue persists
- We don't waste time on strategies that won't work
- We have concrete evidence of recovery

## Success Path

When cascading recovery succeeds:

1. **StarvationDiagnosticDaemon** detects 'unknown-cause' or low confidence
2. **RootCauseAnalyzer.AttemptCascadingRecovery()** tries strategies in order
3. First successful strategy stops the cascade
4. Bead is updated with:
   - Inferred root cause (e.g., `starvation:database-corrupt`)
   - `automated-recovery` label
   - `human` label **removed** (no longer blocked)
   - Detailed diagnostics including all attempts
5. Ready count is updated in the result
6. Bead may be queued for repair daemon for follow-up

## Failure Path

When all strategies fail:

1. All 4 strategies are attempted and verified
2. Root cause remains `unknown-cause`
3. Bead is updated with:
   - `starvation:unknown-cause` label
   - `human-intervention-required` label
   - `monitor-only` label (not auto-repairable)
   - **Complete diagnostic output** showing every attempt
4. Escalation bead is created with full context

## Integration Points

### RootCauseAnalyzer
- **New Method**: `AttemptCascadingRecovery(ctx, workspacePath) (*CascadingRecoveryResult, error)`
- **New Types**: `RecoveryStrategy`, `CascadingRecoveryResult`
- **New Field**: `confidenceThreshold float64` (future: confidence scoring)

### StarvationDiagnosticDaemon
- **Updated Method**: `diagnoseBead()` now triggers cascading recovery for unknown causes
- **Flow**: Analysis → If unknown/lower confidence → Cascading recovery → Label update

### RepairDaemon
- **No changes needed** - existing repair strategies map to the same commands
- Cascading recovery is a **diagnostic-time** enhancement, not a repair-time change

## Metrics and Observability

The following metrics are recorded during cascading recovery:

1. **Per-strategy execution time**: How long each recovery took
2. **Strategy success rate**: Which strategies most often resolve unknown-cause starvation
3. **Ready bead counts**: Before and after for verification
4. **Total cascade duration**: End-to-end time for all attempts

## Safety Considerations

### Least-Invasive-First Ordering
Strategies are ordered by severity to minimize risk:
- Start with safe, idempotent operations
- Progress to more invasive operations only if needed
- Most invasive (index rebuild) is last resort

### Verification Before Proceeding
After each attempt, we verify recovery success:
- No assumption that a command succeeded = problem fixed
- Concrete proof (ready beads available) before stopping
- Prevents false positives and cascading failures

### Rollback Safety
All strategies are based on existing, tested bead commands:
- `bead doctor --repair`: Safe, designed for recovery
- `bead sync flush-only`: Idempotent, can be re-run
- `bead update --clear-assignee`: Reversible, only clears assignee
- `bead init`: Rebuilds from checkpoint (durable backup)

## Future Enhancements

1. **Confidence Scoring**: Add confidence scores to root cause analysis; trigger cascading recovery below threshold
2. **Parallel Verification**: Run verification checks in parallel with recovery attempts for faster feedback
3. **Adaptive Strategy Ordering**: Learn which strategies work best for which workspace patterns
4. **Rollback Tracking**: Track which strategies succeeded and update historical preferences
5. **Escalation with Evidence**: Attach full diagnostic output to escalation beads for faster human triage

## Example Output

### Successful Recovery
```
[Cascading Recovery for NEEDLE]
Initial ready count: 0

### Strategy 1: database-repair
Description: Run bead doctor --repair to fix database corruption
Command: bead doctor --repair
Success: true
Output: ✓ Database integrity verified
Ready count after: 0
No improvement (ready count still 0)

### Strategy 2: checkpoint-sync
Description: Flush checkpoint to resync database state
Command: bead sync flush-only
Success: true
Output: Checkpoint flushed successfully
Ready count after: 3
**RECOVERY SUCCESSFUL**
Inferred root cause: checkpoint-out-of-sync
Ready beads: 0 → 3
```

### Failed Recovery (All Strategies)
```
[Cascading Recovery for NEEDLE]
Initial ready count: 0

### Strategy 1: database-repair
[... details ...]
No improvement

### Strategy 2: checkpoint-sync
[... details ...]
No improvement

### Strategy 3: bead-release
Released 0 stale assignments
No improvement

### Strategy 4: index-rebuild
[... details ...]
No improvement

**ALL STRATEGIES FAILED**
Requires manual investigation and escalation
```

## Testing

To test cascading recovery:

```bash
# Create a starvation scenario (e.g., desync checkpoint)
cd /home/coding/NEEDLE
bead sync flush-only  # Desync by modifying DB without flushing

# Trigger diagnostic daemon
# The daemon will attempt cascading recovery and resolve the issue
```

## Related Documentation

- [Starvation Alert System](/home/coding/SEAM/docs/notes/starvation-alert-root-cause-analysis.md)
- [Transient Starvation Backoff](/home/coding/SEAM/docs/notes/transient-starvation-backoff-implementation.md)
- [Beads Documentation (bead-rs)](https://github.com/jedarden/bead-rs)
