# Starvation Alert Self-Resolution Daemon

## Overview

The starvation alert self-resolution daemon (`StarvationAlertSelfResolution`) automatically monitors and resolves starvation alert beads when the underlying condition is fixed. It uses PluckFallback to verify work availability and tracks consecutive checks before escalating to human review.

## Purpose

This daemon eliminates the need for manual triage of starvation alerts that have already been fixed by automated recovery systems. It continuously monitors open starvation-alert beads and closes them when PluckFallback finds candidates, indicating the starvation condition has resolved.

## Key Features

### PluckFallback Integration

The daemon uses `PluckFallback` to check for available candidates across multiple strategies:

1. **Primary strategy**: `bead list --ready`
2. **Open unassigned strategy**: Open beads without assignee
3. **Open status strategy**: All open beads
4. **Direct DB strategy**: Direct SQLite query
5. **Checkpoint strategy**: Read from checkpoint file

This multi-strategy approach ensures that even if the primary query fails due to database issues, the daemon can still detect when work becomes available.

### Consecutive Check Tracking

The daemon tracks consecutive checks for each alert:

- **Default threshold**: 3 consecutive checks
- **Check interval**: 5 minutes (configurable)
- **Escalation time**: ~15 minutes (3 checks × 5 minutes)

After 3 consecutive checks without finding candidates, the daemon creates a human-review bead for manual investigation.

### Detailed Recovery Documentation

When an alert is resolved, the daemon adds a note documenting:

- Recovery timestamp
- Number of candidates found
- Strategy used to find candidates
- Total consecutive checks performed
- Time to resolution

Example note:
```
Automated recovery at 2026-08-31T12:34:56Z

Recovery Details:
- Candidates found: 5
- Strategy used: open_unassigned
- Consecutive checks: 2
- Time to resolution: 10.5 minutes
```

### Escalation to Human Review

After 3 consecutive checks without resolution, the daemon creates a diagnostic bead with:

- **Priority**: P1 (high priority)
- **Labels**: `human-review-required`, `starvation-escalation`
- **Details**: Full context about the persistent condition including:
  - Alert ID and workspace
  - Check history (first/last check times, total checks)
  - Current ready bead count
  - Automated recovery attempts made
  - Suggested investigation steps

## Architecture

### Configuration

```go
type SelfResolutionConfig struct {
    WorkspaceRoot          string
    CheckInterval          time.Duration  // Default: 5 minutes
    AlertLabel             string         // Default: "starvation-alert"
    EnablePluckFallback    bool           // Default: true
    PluckFallbackDiagnosticLog string
    MaxConsecutiveChecks   int            // Default: 3
    OnResolution          func(*AlertResolution)
}
```

### Check History

The daemon maintains check history for each tracked alert:

```go
type CheckHistory struct {
    AlertID        string
    Workspace      string
    FirstCheck     time.Time
    LastCheck      time.Time
    CheckCount     int
    LastReadyCount int
    LastStrategy   string
    Resolved       bool
    Escalated      bool
}
```

### Resolution Types

The daemon can produce three types of outcomes:

1. **Resolved**: Candidates found, alert closed with detailed reason
2. **Escalated**: 3 checks without resolution, human-review bead created
3. **Pending**: Still checking, below escalation threshold

## Usage

### Standalone Tool

```bash
# Run continuously
./starvation-alert-self-resolution \
  --workspace-root /home/coding \
  --interval 5m \
  --alert-label starvation-alert \
  --enable-pluck-fallback \
  --diagnostic-log /var/log/seam/starvation-resolution.log

# Run once (for testing)
./starvation-alert-self-resolution --once
```

### Flags

- `--workspace-root`: Root directory containing workspaces (default: `/home/coding`)
- `--interval`: Check interval (default: `5m`)
- `--alert-label`: Label identifying starvation alerts (default: `starvation-alert`)
- `--enable-pluck-fallback`: Enable PluckFallback (default: `true`)
- `--diagnostic-log`: Path to PluckFallback diagnostic log
- `--max-consecutive-checks`: Checks before escalation (default: `3`)
- `--once`: Run once and exit
- `--verbose`: Enable verbose logging

## Integration with Kubernetes

The daemon can be deployed as a Kubernetes Deployment with leadership election via Lease:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: starvation-alert-self-resolution
  namespace: seam-observer
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: resolver
        image: ronaldraygun/seam:latest
        command: ["/starvation-alert-self-resolution"]
        args:
          - "--workspace-root=/home/coding"
          - "--interval=5m"
          - "--enable-pluck-fallback"
          - "--diagnostic-log=/tmp/starvation-resolution.log"
```

## Comparison with starvation-alert-revaluator

The existing `starvation-alert-revaluator` tool has some differences:

| Feature | starvation-alert-revaluator | starvation-alert-self-resolution |
|---------|----------------------------|----------------------------------|
| Candidate detection | `bead list --ready` only | PluckFallback with 5 strategies |
| Escalation trigger | Age-based (48 hours) | Consecutive checks (3) |
| Recovery notes | None | Detailed timestamp + strategy |
| Strategy documentation | None | Strategy name in close reason |
| Escalation threshold | 48 hours | ~15 minutes (3 checks) |

The new daemon is faster to escalate (15 minutes vs 48 hours) and provides more detailed recovery information through PluckFallback integration.

## Files

- `internal/server/starvation_alert_self_resolution.go`: Core daemon implementation
- `internal/server/starvation_alert_self_resolution_test.go`: Unit tests
- `tools/starvation-alert-self-resolution/main.go`: Standalone CLI tool

## Future Enhancements

Potential improvements:

1. **Configurable escalation thresholds**: Allow different thresholds per workspace
2. **Metrics export**: Prometheus metrics for resolution rates
3. **Webhook notifications**: Send alerts on resolution/escalation
4. **Historical tracking**: Store resolution history for analysis
5. **Multi-workspace coordination**: Share check history across instances
