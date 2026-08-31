# Starvation Recovery Tool

Automated diagnostic and recovery for bead starvation conditions.

## Problem

When NEEDLE workers report no candidates but open/invisible beads exist, it indicates a **starvation condition**. Common causes:

- Database corruption (bead store inconsistency)
- Cross-repo preconditions blocking beads without explicit labeling
- Stale bead assignments preventing worker visibility
- Worker crashes or hangs

This tool automates the detection and recovery of such conditions, transforming a human-blocked alert into an automated recovery workflow.

## How It Works

The recovery process runs in four steps:

1. **Run `bead doctor --repair`** - Fixes database corruption and inconsistency
2. **Validate cross-repo preconditions** - Marks beads with unmet external dependencies as `manual_blocked=true`
3. **Check worker health** - Verifies NEEDLE workers are alive and responsive
4. **Re-evaluate ready frontier** - Counts beads that are now available for work

If work becomes available after these steps, the recovery is **successful** and the starvation alert can be auto-closed. If not, the tool reports incomplete recovery and the situation may need manual intervention.

## Installation

```bash
cd tools/starvation-recovery
go build -o starvation-recovery main.go
sudo mv starvation-recovery /usr/local/bin/
```

## Usage

### One-Shot Mode

Run recovery once and exit:

```bash
starvation-recovery --once
```

### Loop Mode

Run continuously, checking all workspaces every 5 minutes (default):

```bash
starvation-recovery
```

### Options

```
--workspace-root /home/coding    Root directory containing all workspaces (default: /home/coding)
--validate-script <path>         Path to validate_cross_repo_preconditions.sh (default: auto-detected)
--once                           Run once and exit (default: loop mode)
--interval 5m                    Check interval for loop mode (default: 5 minutes)
--verbose                        Enable verbose logging
--json                           Output results in JSON format
--dry-run                        Show what would be done without making changes
```

### Examples

```bash
# Check all workspaces once with verbose output
starvation-recovery --once --verbose

# Run in JSON mode for monitoring
starvation-recovery --once --json

# Dry run to see what would happen
starvation-recovery --once --dry-run --verbose

# Continuous mode with 10-minute interval
starvation-recovery --interval 10m
```

## Output

### Human-Readable Output

```
=== Starvation Recovery Report ===
Time: 2026-08-31T03:15:42Z
Workspaces checked: 3

Workspace: SEAM
  Duration: 1234ms
  Initial state: open=5, invisible=2, ready=0
  Final state: ready=3
  Steps: Running bead doctor --repair, Validating cross-repo preconditions, Checking worker status, Re-evaluating ready frontier
  Result: ✓ SUCCESS

Workspace: NEEDLE
  Duration: 890ms
  Initial state: open=12, invisible=8, ready=0
  Final state: ready=0
  Steps: Running bead doctor --repair, Validating cross-repo preconditions, Checking worker status, Re-evaluating ready frontier
  Result: ✗ INCOMPLETE
  Errors:
    - precondition validation failed: cross-repo bead 'declarative-config:abc123' not found
```

### JSON Output

```json
{
  "workspace": "SEAM",
  "start_time": "2026-08-31T03:15:42.123Z",
  "end_time": "2026-08-31T03:15:43.456Z",
  "duration_ms": 1333,
  "success": true,
  "open_beads_before": 5,
  "invisible_before": 2,
  "ready_before": 0,
  "ready_after": 3,
  "bead_doctor_run": true,
  "bead_doctor_success": true,
  "preconds_run": true,
  "preconds_success": true,
  "workers_alive": true,
  "steps": [
    "Running bead doctor --repair",
    "Validating cross-repo preconditions",
    "Checking worker status",
    "Re-evaluating ready frontier"
  ]
}
```

## Integration with SEAM

The recovery loop can also be integrated directly into SEAM as a background service using the `StarvationRecoveryLoop` type in `internal/server/starvation_recovery_loop.go`. This provides:

- Kubernetes Lease-based leader election for distributed deployment
- Callbacks for recovery completion events
- Integration with SEAM's existing lifecycle management

Example:

```go
recoveryLoop, err := server.NewStarvationRecoveryLoop(server.RecoveryConfig{
    WorkspaceRoot: "/home/coding",
    ValidateScript: "/home/coding/SEAM/tools/validate_cross_repo_preconditions.sh",
    LeaseLeader: leaseLeader,
    CheckInterval: 5 * time.Minute,
    MaxAttemptsPerBead: 3,
    OnRecoveryComplete: func(beadID string, success bool, details string) {
        log.Printf("Recovery complete for %s: success=%v, details=%s", beadID, success, details)
    },
})

// Start the loop (blocks until stopped)
go func() {
    if err := recoveryLoop.Start(ctx); err != nil {
        log.Printf("Recovery loop stopped: %v", err)
    }
}()
```

## Starvation Detection Criteria

A workspace is considered to be in starvation state when:

- **Ready beads = 0**: No beads are available for workers to claim
- **AND** one of the following:
  - Open beads > 0: Beads exist but none are ready
  - Invisible beads > 0: Beads are effectively invisible due to stale assignments or manual blocks

## Common Causes and Fixes

| Cause | Symptom | Automated Fix |
|-------|---------|---------------|
| Database corruption | Bead count mismatches, `bead list` errors | `bead doctor --repair` |
| Cross-repo precondition not met | Bead blocked but no `blocks` edge | `validate_cross_repo_preconditions.sh` |
| Stale assignment | Bead open+assigned but worker gone | `bead doctor --repair` |
| Worker crash | No `pgrep needle.*worker` matches | Manual: restart workers |

## Monitoring

For production deployment, run in loop mode with JSON output and pipe to a monitoring system:

```bash
starvation-recovery --interval 5m --json | jq -c 'select(.success == false)' | \
    while read line; do
        echo "WARNING: Recovery incomplete: $line" | logger -t starvation-recovery
    done
```

## See Also

- `tools/validate_cross_repo_preconditions.sh` - Cross-repo precondition validation
- `docs/notes/transient-starvation-backoff-implementation.md` - NEEDLE's transient starvation handling
- `internal/server/starvation_recovery_loop.go` - SEAM-integrated recovery loop
