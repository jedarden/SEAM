# Integrated Starvation Recovery and Auto-Closure System

## Overview

The Integrated Starvation Recovery System is a comprehensive automated solution for detecting, diagnosing, and recovering from bead starvation conditions in NEEDLE workspaces. It eliminates human intervention by automatically:

1. **Detecting** starvation conditions (open beads exist but Pluck finds no candidates)
2. **Diagnosing** root causes (stale checkpoints, stuck assignments, worker disconnection)
3. **Executing** appropriate recovery operations (flush, release, rebuild)
4. **Validating** recovery success by re-running Pluck
5. **Auto-closing** starvation alert beads on successful recovery

## Architecture

### Components

```
integrated-starvation-recovery.service (systemd user service)
    ↓
integrated-starvation-recovery (main script)
    ↓
├── bead doctor --rehearse (diagnostics)
├── automated-bead-recovery (recovery operations)
├── bead sync flush-only (checkpoint synchronization)
├── bead doctor --repair (database repair)
└── bead close (auto-closure of alert beads)
```

### Detection Logic

**Starvation Condition:**
```bash
bead list --ready returns 0 candidates AND
bead list --status open returns >0 beads
```

**Alert Bead Detection:**
```bash
bead list --status open --json |
jq '.labels[] | select(. == "starvation-alert" or startswith("alert:starvation"))'
```

## Installation

### Quick Install

```bash
cd /home/coding/SEAM
install-integrated-recovery
```

### Manual Installation

```bash
# 1. Install tools to ~/.local/bin
ln -sf $(pwd)/tools/integrated_starvation_recovery.sh ~/.local/bin/integrated-starvation-recovery
ln -sf $(pwd)/tools/automated_bead_recovery.sh ~/.local/bin/automated-bead-recovery
ln -sf $(pwd)/tools/starvation-recovery/starvation-recovery.sh ~/.local/bin/starvation-recovery
ln -sf $(pwd)/tools/pluck-fallback/pluck-fallback.sh ~/.local/bin/pluck-fallback

# 2. Install systemd user service
mkdir -p ~/.config/systemd/user
cp tools/integrated-starvation-recovery.service ~/.config/systemd/user/

# 3. Enable and start service
systemctl --user daemon-reload
systemctl --user enable integrated-starvation-recovery.service
systemctl --user start integrated-starvation-recovery.service

# 4. Verify service is running
systemctl --user status integrated-starvation-recovery.service
```

## Usage

### Manual One-Shot Execution

```bash
# Run once with verbose output
integrated-starvation-recovery --once --verbose

# Run in dry-run mode (no actual changes)
integrated-starvation-recovery --once --dry-run --verbose
```

### Continuous Monitoring (systemd)

```bash
# Start service
systemctl --user start integrated-starvation-recovery.service

# Stop service
systemctl --user stop integrated-starvation-recovery.service

# Check status
systemctl --user status integrated-starvation-recovery.service

# View logs
journalctl --user -u integrated-starvation-recovery.service -f

# Enable auto-start on login
systemctl --user enable integrated-starvation-recovery.service
```

### Custom Intervals

```bash
# Run every 10 minutes instead of default 5
integrated-starvation-recovery --interval 10 --verbose

# Or modify the service file
ExecStart=/home/coding/.local/bin/integrated-starvation-recovery --interval 10m --verbose
```

## Recovery Process

### Step 1: Detection
The system continuously monitors for:
- **Ready beads = 0** (no candidates available for workers)
- **Open beads > 0** (beads exist but are invisible)
- **Starvation alert beads present** (previous unresolved conditions)

### Step 2: Diagnostics
```bash
bead doctor --rehearse
```
- Validates database integrity
- Checks checkpoint freshness
- Identifies schema issues
- Detects stale assignments

### Step 3: Recovery Operations
```bash
# Safe auto-repair
bead doctor --repair

# Flush checkpoint to ensure consistency
bead sync flush-only

# If database is corrupt, rebuild from checkpoint
bead init
bead sync import-only --input .beads/checkpoint/forensic.jsonl --restore-into-empty
```

### Step 4: Validation
```bash
# Verify ready beads are now available
bead list --ready --json | jq '. | length'
```

### Step 5: Auto-Closure
```bash
# Close starvation alert beads with resolution reason
bead close <alert-id> --reason "Automated recovery successful - beads now visible"
```

## Starvation Alert Beads

### Detection
Alert beads are identified by labels:
- `starvation-alert`
- `alert:starvation:*` (e.g., `alert:starvation:unknown`)

### Auto-Closure Conditions
An alert bead is automatically closed when:
1. **Condition self-resolved:** Ready beads > 0 without recovery
2. **Recovery successful:** Recovery operations restore ready beads > 0

### Closure Reasons
- **Self-resolved:** "Condition self-resolved - beads now visible to workers (ready beads: N)"
- **Recovery successful:** "Automated recovery successful - beads now visible (ready beads: N)"

## Troubleshooting

### Service Not Running

```bash
# Check service status
systemctl --user status integrated-starvation-recovery.service

# View recent logs
journalctl --user -u integrated-starvation-recovery.service -n 50

# Restart service
systemctl --user restart integrated-starvation-recovery.service
```

### Recovery Not Working

```bash
# Run manually with verbose output
integrated-starvation-recovery --once --verbose

# Check log files
ls -la /tmp/integrated-recovery-*/
cat /tmp/integrated-recovery-*/recovery.log

# Verify tools are installed
which integrated-starvation-recovery automated-bead-recovery bead
```

### Bead Database Issues

```bash
# Run diagnostics
bead doctor --rehearse

# Check checkpoint freshness
stat .beads/beads.db .beads/checkpoint/current.json

# Manual checkpoint flush
bead sync flush-only

# Rebuild from checkpoint if needed
bead init
bead sync import-only --input .beads/checkpoint/forensic.jsonl --restore-into-empty --actor <user>
```

### Starvation Persists After Recovery

```bash
# Check for stale assignments
bead list --status open --json | jq '. | select(.assignee != null)'

# Clear stale assignees
bead update <bead-id> --clear-assignee

# Check for manual blocks
bead list --status open --json | jq '. | select(.manual_blocked == true)'

# Verify worker connectivity
ps aux | grep needle.*worker
```

## Monitoring

### Health Check Commands

```bash
# Check if system is healthy (ready beads > 0)
[[ $(bead list --ready --json | jq '. | length') -gt 0 ]] && echo "Healthy" || echo "Starvation"

# Count starvation alert beads
bead list --status open --json | jq '[.labels[] | select(. == "starvation-alert" or startswith("alert:starvation"))] | length'

# Check service uptime
systemctl --user show integrated-starvation-recovery.service -p ActiveEnterTimestamp --value
```

### Log Locations

- **Systemd journal:** `journalctl --user -u integrated-starvation-recovery.service`
- **Recovery logs:** `/tmp/integrated-recovery-*/recovery.log`
- **Bead doctor logs:** `/tmp/integrated-recovery-*/bead_doctor.txt`
- **Automated recovery logs:** `/tmp/integrated-recovery-*/automated_recovery.txt`

## Integration with Existing Tools

This system integrates several existing tools:

### `automated-bead-recovery`
Comprehensive 8-step recovery process including:
- Bead visibility diagnostic
- Database integrity check
- Checkpoint freshness verification
- Safe auto-repair
- Database rebuild (if needed)
- Recovery verification

### `starvation-recovery`
Monitoring daemon for continuous starvation detection across multiple workspaces.

### `pluck-fallback`
Enhanced bead plucking with multiple query strategies and automatic fallback when primary query fails.

## Configuration

### Environment Variables

```bash
WORKSPACE=/home/coding/SEAM  # Workspace to monitor
```

### Service Configuration

Edit `~/.config/systemd/user/integrated-starvation-recovery.service`:

```ini
[Service]
# Check interval (default: 5m)
ExecStart=/home/coding/.local/bin/integrated-starvation-recovery --interval 5m --verbose

# Custom workspace
Environment=WORKSPACE=/path/to/workspace

# Log level
ExecStart=/home/coding/.local/bin/integrated-starvation-recovery --verbose
```

## Cron Alternative

For systems without systemd user services:

```cron
# Check every 5 minutes
*/5 * * * * cd /home/coding/SEAM && /home/coding/.local/bin/integrated-starvation-recovery --once >> /tmp/starvation-recovery.log 2>&1
```

Add to `crontab -e` or create `/etc/cron.d/integrated-starvation-recovery`.

## Security Considerations

### Service Hardening

The systemd service includes:
- `NoNewPrivileges=true` - Prevent privilege escalation
- `PrivateTmp=true` - Isolate /tmp
- `ProtectSystem=strict` - Read-only system directories
- `ProtectHome=read-only` - Read-only home directory
- `ReadWritePaths=/home/coding /tmp` - Limited write access

### Bead Database Protection

- Database backups created before rebuild: `.beads/beads.db.backup-*`
- Checkpoint files are git-tracked for version control
- Recovery operations are atomic and reversible

## Performance Impact

### Resource Usage

- **CPU:** Minimal (periodic checks every 5 minutes)
- **Memory:** Negligible (script-based, no resident processes)
- **Disk:** Minimal (log files in `/tmp/integrated-recovery-*`)

### Database Load

- Read operations every 5 minutes
- Write operations only during recovery
- Checkpoint flush preserves data integrity

## Future Enhancements

### Planned Features

1. **Multi-workspace support:** Monitor multiple workspaces simultaneously
2. **Metrics collection:** Track starvation frequency and recovery success rates
3. **Alert thresholds:** Configure custom thresholds for escalation
4. **Rollback support:** Automatic rollback if recovery fails
5. **Distributed mode:** Leader election for multi-instance deployments

### Extension Points

The modular design allows for:
- Custom diagnostic plugins
- Additional recovery strategies
- Alternative alert mechanisms
- Integration with external monitoring systems

## License and Maintenance

This integrated system is part of the SEAM workspace and follows the same maintenance and version control policies.

## Support

For issues or questions:
1. Check service logs: `journalctl --user -u integrated-starvation-recovery.service -n 100`
2. Run manual test: `integrated-starvation-recovery --once --verbose`
3. Review troubleshooting section above
4. Check existing bead diagnostic tools in `tools/` directory

---

**System Version:** 1.0.0
**Last Updated:** 2026-08-31
**Maintained By:** SEAM Workspace
