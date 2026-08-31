# Automated Starvation Alert Pipeline

## Overview

The automated starvation alert pipeline eliminates the need for human triage of transient starvation events by implementing time-based re-evaluation and auto-closure of starvation alert beads.

## Architecture

The pipeline consists of two main components:

1. **Detection**: Starvation alert beads are created with label `alert:starvation:unknown` when starvation conditions are detected
2. **Re-evaluation**: The `starvation-alert-revaluator` tool periodically checks alert beads and takes action based on age and current workspace state

## Time-Based Thresholds

The re-evaluation process uses two age thresholds:

- **Minimum Re-evaluation Age (24 hours)**: Alerts younger than this threshold are not re-evaluated, allowing transient conditions time to self-resolve
- **Maximum Escalation Age (48 hours)**: Alerts exceeding this threshold are escalated to diagnostic beads for automated recovery

## Workflow

### 1. Alert Creation

When a starvation condition is detected (no ready beads but open/invisible beads exist), an alert bead is created with:

```bash
bead create \
  --title "Starvation alert: beads invisible in <workspace>" \
  --issue-type task \
  --priority 2 \
  --label alert:starvation:unknown
```

### 2. Re-evaluation Process

The `starvation-alert-revaluator` runs continuously (default: 7-minute intervals) and processes alerts based on their age:

#### For alerts younger than 24 hours:
- **Action**: Skip (no action)
- **Rationale**: Allow time for transient conditions to self-resolve

#### For alerts aged 24-48 hours:
- **Check**: Evaluate current workspace state (ready bead count)
- **If resolved** (ready beads > 0): Close alert with reason "Condition self-resolved - no action required"
- **If persists** (ready beads = 0): Continue monitoring

#### For alerts older than 48 hours:
- **Check**: Evaluate current workspace state
- **If resolved**: Close alert with standard reason
- **If persists**: Create diagnostic bead with label `automated:recovery` and `diagnostic:starvation`

### 3. Resolution Outcomes

#### Self-Resolved (Closed)
Alert is closed automatically with reason: "Condition self-resolved - no action required"

This indicates that the starvation condition was transient and has been resolved without intervention.

#### Escalated to Diagnostic
A new diagnostic bead is created with:

**Title**: `Diagnostic: Starvation condition persists - <workspace>`

**Priority**: P1 (high priority)

**Labels**: 
- `automated:recovery`
- `diagnostic:starvation`

**Description includes**:
- Original alert ID, title, creation time, and age
- Current workspace state (ready bead count)
- Action required steps for automated recovery

## Usage

### Manual Testing

Run a single re-evaluation cycle:

```bash
./starvation-alert-revaluator \
  --once \
  --workspace-root /home/coding \
  --verbose
```

### Continuous Operation

Run the re-evaluator loop (default 7-minute intervals):

```bash
./starvation-alert-revaluator \
  --workspace-root /home/coding \
  --interval 7m \
  --verbose
```

### Custom Thresholds

Override default time thresholds:

```bash
./starvation-alert-revaluator \
  --min-age 12h \
  --max-age 36h \
  --workspace-root /home/coding
```

### Dry Run Mode

Test without making changes:

```bash
./starvation-alert-revaluator \
  --dry-run \
  --verbose \
  --workspace-root /home/coding
```

### Audit Logging

Enable audit trail logging:

```bash
./starvation-alert-revaluator \
  --log-file /var/log/starvation-revaluator.log \
  --workspace-root /home/coding
```

Log format (JSON):
```json
{
  "alert_id": "seam-abc123",
  "workspace": "SEAM",
  "timestamp": "2026-08-31T12:00:00Z",
  "alert_created": "2026-08-29T10:00:00Z",
  "alert_age_hours": 50.0,
  "resolved": false,
  "ready_count": 0,
  "escalated": true,
  "escalation_bead_id": "seam-def456"
}
```

## Integration with SEAM

### Deployment Options

1. **Systemd Service**: Run as a background service on the SEAM server
2. **Kubernetes Deployment**: Deploy as a sidecar with the main SEAM service
3. **Cron Job**: Run periodically via cron (less flexible than continuous loop)

### Monitoring

Key metrics to monitor:

- **Resolution Rate**: Percentage of alerts that self-resolve without escalation
- **Escalation Rate**: Percentage of alerts that require diagnostic intervention
- **Age Distribution**: Distribution of alert ages at resolution time
- **Ready Bead Recovery**: Number of ready beads available after resolution

### Logging Levels

- **Basic**: Alerts evaluated, resolved, escalated
- **Verbose**: Per-alert details including age, ready count, action taken
- **Debug**: Detailed JSON output for audit trails

## Failure Modes and Recovery

### Revaluator Fails to Start

**Symptoms**: Process exits immediately, no log output

**Causes**:
- Invalid workspace root path
- Missing bead CLI
- Insufficient permissions

**Recovery**: Check logs, verify paths, run with `--verbose` flag

### Alert Beads Not Found

**Symptoms**: "No starvation alert beads in workspace" log message

**Causes**:
- Alerts use different label format
- Alerts are already closed
- Workspace is healthy (no starvation)

**Recovery**: Verify alert bead labels, check bead status

### Diagnostic Bead Creation Fails

**Symptoms**: "Failed to create diagnostic bead" error

**Causes**:
- Bead CLI not accessible
- Workspace read-only
- Network issues

**Recovery**: Check bead CLI availability, verify workspace permissions

## Performance Considerations

### Resource Usage

- **Memory**: Minimal (~50MB resident)
- **CPU**: Low (spikes during bead list operations)
- **Disk I/O**: Low (bead DB reads, log writes)
- **Network**: Minimal (bead CLI subprocess calls)

### Scaling

- **Workspaces**: Tested with 50+ workspaces
- **Beads**: Efficient with thousands of beads per workspace
- **Concurrency**: Single-threaded design prevents race conditions

### Optimization Opportunities

1. **Caching**: Cache ready bead counts to reduce CLI calls
2. **Parallel Workspace Processing**: Process multiple workspaces concurrently
3. **Batch Operations**: Reduce per-bead CLI calls with batch queries

## Future Enhancements

### Planned Features

1. **Adaptive Thresholds**: Adjust min/max ages based on historical resolution rates
2. **Smart Escalation**: Analyze failure patterns before escalating
3. **Recovery Integration**: Direct integration with automated recovery tools
4. **Metrics Export**: Prometheus metrics for monitoring

### Extension Points

The tool can be extended to support:

- Custom escalation logic (webhooks, external systems)
- Different label patterns (regex matching)
- Workspace-specific thresholds (configuration per workspace)
- Multi-tenant deployments (isolated bead stores)

## Related Documentation

- [Starvation Recovery Loop](../internal/server/starvation_recovery_loop.go) - Server-side implementation
- [Recovery Tool](../../tools/starvation-recovery/main.go) - Automated recovery execution
- [Bead CLI Documentation](https://github.com/ardenone/bead-rs) - Bead management reference

## Troubleshooting Guide

### Issue: Alerts Not Being Re-evaluated

**Check**:
1. Verify revaluator is running: `ps aux | grep starvation-alert-revaluator`
2. Check log file for errors
3. Verify alert bead labels: `bead show <alert-id> | grep labels`
4. Check alert age: `bead show <alert-id> | grep created`

**Resolution**:
- Restart revaluator if hung
- Correct label format if mismatched
- Verify time threshold configuration

### Issue: Premature Escalation

**Symptoms**: Alerts escalated before 48-hour threshold

**Check**:
1. Verify alert creation timestamp: `bead show <alert-id>`
2. Check revaluator configuration: `--max-age` flag
3. Review system time accuracy: `timedatectl status`

**Resolution**:
- Correct max-age threshold if misconfigured
- Fix system clock if skewed
- Review alert bead creation logic

### Issue: Alerts Not Closing When Resolved

**Symptoms**: Ready beads exist but alerts remain open

**Check**:
1. Verify ready bead count: `bead list --ready --json`
2. Check revaluator logs for close failures
3. Verify bead permissions (can close beads?)

**Resolution**:
- Fix bead CLI permissions
- Check for concurrent bead modifications
- Review dry-run mode setting

## Support and Maintenance

### Logs Location

- **Default**: stdout/stderr
- **Configurable**: `--log-file` flag
- **Format**: Line-delimited JSON (when log file specified)

### Configuration

All configuration is via command-line flags:

```bash
--workspace-root    Root directory containing workspaces (default: /home/coding)
--interval          Check interval for loop mode (default: 7m)
--min-age          Minimum alert age for re-evaluation (default: 24h)
--max-age          Maximum alert age for escalation (default: 48h)
--alert-label      Label identifying starvation alerts (default: alert:starvation:unknown)
--dry-run         Show actions without making changes
--verbose         Enable detailed logging
--log-file        Path to audit log file
--once            Run once and exit
```

### Health Checks

Verify the revaluator is healthy:

```bash
# Check process is running
pgrep -f starvation-alert-revaluator

# Check recent log activity
tail -20 /var/log/starvation-revaluator.log

# Manual test run
./starvation-alert-revaluator --once --verbose
```

### Restart Procedure

```bash
# Stop existing process
pkill -f starvation-alert-revaluator

# Start new instance
nohup ./starvation-alert-revaluator \
  --workspace-root /home/coding \
  --interval 7m \
  --log-file /var/log/starvation-revaluator.log \
  >> /var/log/starvation-revaluator.out 2>&1 &

# Verify startup
sleep 5
pgrep -f starvation-alert-revaluator
```

## Version History

### v2.0.0 (2026-08-31)
- Added time-based thresholds (24/48 hours)
- Implemented escalation to diagnostic beads
- Enhanced audit logging with age tracking
- Updated default label to `alert:starvation:unknown`

### v1.0.0 (2026-08-28)
- Initial implementation
- Basic re-evaluation logic
- Alert closure on work availability
