# PluckFallback Integration

## Overview

The PluckFallback system has been integrated into SEAM's starvation recovery loop to provide resilient bead querying with automatic fallback strategies.

## Architecture

### Components

1. **internal/pluckfallback/pluck.go** - Core PluckFallback package with 4 query strategies:
   - Primary: `bead list --ready` (standard query)
   - Fallback 1: `bead list --status open` (open status query)
   - Fallback 2: Direct SQLite query (bypasses CLI)
   - Fallback 3: Checkpoint JSON query (git-tracked data)

2. **internal/pluckfallback/diagnostic.go** - Diagnostic bead creation and logging

3. **internal/server/starvation_recovery_loop.go** - Integration point for SEAM's starvation recovery

4. **tools/pluck-fallback/main.go** - CLI tool using the internal package

### Key Features

- **Automatic fallback**: Progressively tries alternative query strategies when primary fails
- **Discrepancy detection**: Logs when primary query returns 0 but fallback strategies succeed
- **Diagnostic bead creation**: Automatically creates beads documenting visibility bugs
- **Graceful degradation**: Falls back to direct query if PluckFallback fails

## Usage

### Enabling PluckFallback in Starvation Recovery

When creating the starvation recovery loop, enable PluckFallback:

```go
loop, err := NewStarvationRecoveryLoop(server.RecoveryConfig{
    WorkspaceRoot: "/home/coding",
    ValidateScript: validateScript,
    LeaseLeader: leaseLeader,
    CheckInterval: 5 * time.Minute,
    MaxAttemptsPerBead: 3,
    EnablePluckFallback: true, // Enable PluckFallback
    PluckFallbackDiagnosticLog: "/home/coding/.beads/diagnostics/pluck-fallback.log",
    OnRecoveryComplete: onRecoveryComplete,
})
```

### Configuration Options

- `EnablePluckFallback` (bool) - Enable/disable PluckFallback (default: false)
- `PluckFallbackDiagnosticLog` (string) - Path to diagnostic log file (default: `.beads/diagnostics/pluck-fallback.log`)

### Behavior

When `EnablePluckFallback = true`:

1. **Normal operation**: Primary query (`bead list --ready`) succeeds
   - Returns count of ready beads
   - No discrepancies logged

2. **Visibility bug detected**: Primary query returns 0, but fallback succeeds
   - Logs discrepancy with timestamp
   - Returns count of recovered beads
   - Creates diagnostic bead (optional)
   - Continues normal operation

3. **Complete failure**: All strategies fail
   - Returns error
   - Falls back to direct query method
   - May indicate database corruption

## Diagnostic Output

### Log Format

```
[2026-08-31T12:34:56Z] Visibility bug detected: primary query returned 0, but open_status returned 15 candidates
  - Recovered bead: seam-abc123 (Title of bead)
  - Recovered bead: seam-def456 (Another bead title)
```

### Exit Codes (CLI tool)

- `0` - Primary query succeeded (no fallback needed)
- `2` - Fallback was triggered (visibility bug detected)
- `3` - No candidates found by any strategy

## Testing

### Manual Testing with CLI Tool

```bash
# Test with verbose output
tools/pluck-fallback/main.go --workspace /home/coding/SEAM --verbose --json

# Test with diagnostic bead creation
tools/pluck-fallback/main.go --workspace /home/coding/SEAM --create-diagnostic-bead

# Check for visibility bugs
if tools/pluck-fallback/main.go --workspace /home/coding/SEAM --json; then
    echo "Primary query succeeded"
else
    exit_code=$?
    if [ $exit_code -eq 2 ]; then
        echo "Visibility bug detected and recovered"
    elif [ $exit_code -eq 3 ]; then
        echo "No candidates found - may indicate true starvation"
    fi
fi
```

### Integration Testing

The integration is tested through the starvation recovery loop:

1. Start SEAM with PluckFallback enabled
2. Monitor diagnostic log for discrepancies
3. Verify fallback strategies recover invisible beads
4. Confirm diagnostic beads are created when issues detected

## Implementation Details

### Strategy Execution Order

1. **PrimaryQueryStrategy** - Fastest, uses ready frontier logic
2. **OpenStatusQueryStrategy** - Checks all open beads, bypasses ready frontier
3. **DirectDBQueryStrategy** - Direct database access, bypasses CLI
4. **CheckpointQueryStrategy** - Last resort, reads git-tracked checkpoint

### Error Handling

- Strategy failures are logged (if verbose) and don't stop execution
- Last strategy error is returned if all fail
- PluckFallback.Close() must be called to release diagnostic log file

### Performance Considerations

- Primary query remains fast (no overhead when working correctly)
- Fallback strategies add latency only when primary fails
- Diagnostic logging is asynchronous (file writes don't block)

## Migration Notes

### From Direct Query to PluckFallback

Before:
```go
count, err := l.countReadyBeadsDirect(ctx, workspacePath)
```

After:
```go
count, err := l.countReadyBeads(ctx, workspacePath) // Uses PluckFallback if enabled
```

### Backward Compatibility

- PluckFallback is opt-in via `EnablePluckFallback` config
- Existing code continues to use direct query method
- No breaking changes to existing APIs

## Future Enhancements

Potential improvements:
1. Add configurable strategy execution order
2. Implement strategy timeout and cancellation
3. Add metrics/monitoring for fallback usage
4. Create alerting for persistent visibility bugs
5. Integrate with existing diagnostic dashboards

## Related Files

- `internal/pluckfallback/pluck.go` - Core PluckFallback implementation
- `internal/pluckfallback/diagnostic.go` - Diagnostic bead creation
- `internal/server/starvation_recovery_loop.go` - Starvation recovery integration
- `tools/pluck-fallback/main.go` - CLI tool for standalone testing
- `tools/pluck-fallback/pluck-fallback.sh` - Bash implementation with identical functionality
