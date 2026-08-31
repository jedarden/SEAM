# Automated Starvation Recovery Tool

A comprehensive automated solution for detecting, diagnosing, and recovering from bead starvation conditions in NEEDLE workspaces.

## Overview

Bead starvation occurs when open beads exist but `bead list --ready` (Pluck) finds no candidates, causing workers to spin idle despite available work. This tool eliminates human intervention by:

1. **Detecting** starvation conditions automatically
2. **Diagnosing** root causes (stale checkpoints, stuck assignments, database corruption)
3. **Executing** appropriate recovery actions
4. **Validating** recovery success
5. **Closing** starvation alert beads on successful recovery

## Features

- ✅ Runs `bead doctor --rehearse` for safe diagnostics
- ✅ Checks for open beads vs ready beads (starvation detection)
- ✅ Identifies root causes:
  - Stale checkpoints (checkpoint not updated recently)
  - Stuck assignments (beads assigned to inactive workers)
  - Database corruption (integrity check failures)
- ✅ Executes targeted recovery:
  - `bead sync flush-only` for stale checkpoints
  - `bead update --clear-assignee` for stuck assignments
  - `bead sync import-only` for checkpoint rebuild
  - `bead doctor --repair` for database repair
- ✅ Validates recovery by re-checking ready bead count
- ✅ Auto-closes starvation alert beads on success
- ✅ Can run as cron job or systemd service
- ✅ Comprehensive logging and audit trail

## Installation

```bash
# The tool is in tools/automated-starvation-recovery/
cd /home/coding/SEAM/tools/automated-starvation-recovery

# Make executable
chmod +x automated_recovery.py

# Install as systemd service (optional)
sudo systemctl link $(pwd)/automated-starvation-recovery.service
sudo systemctl enable --now automated-starvation-recovery
```

## Usage

### One-shot mode (single run)

```bash
# Run once and exit
./automated_recovery.py --once --verbose

# With custom workspace
./automated_recovery.py --once --workspace /path/to/workspace

# Dry run (show what would be done)
./automated_recovery.py --once --dry-run
```

### Loop mode (continuous monitoring)

```bash
# Run continuously with 5-minute intervals
./automated_recovery.py --interval 5

# With custom interval and log file
./automated_recovery.py --interval 10 --log-file /var/log/starvation-recovery.log
```

### Cron mode (recommended for production)

Add to crontab:

```bash
# Run every 5 minutes
*/5 * * * * /home/coding/SEAM/tools/automated-starvation-recovery/automated_recovery.py --once >> /var/log/starvation-recovery.log 2>&1
```

## Root Cause Analysis

The tool identifies these common starvation causes:

### 1. Stale Checkpoint
**Symptom**: Checkpoint file not updated within 1 hour
**Cause**: Database state diverged from git-tracked checkpoint
**Recovery**: `bead sync flush-only` to publish current database state

### 2. Stuck Assignments
**Symptom**: Open beads with assignees not updated in 24+ hours
**Cause**: Worker crashed or disconnected without releasing beads
**Recovery**: `bead update --clear-assignee` to make beads available again

### 3. Database Corruption
**Symptom**: `PRAGMA integrity_check` fails
**Cause**: SQLite database corruption or locking issues
**Recovery**: `bead doctor --repair` or checkpoint rebuild from forensic.jsonl

## Exit Codes

- `0`: Recovery successful or no starvation detected
- `1`: Recovery failed (manual intervention may be required)

## Monitoring

Check logs for activity:

```bash
# If using systemd
journalctl -u automated-starvation-recovery -f

# If using cron
tail -f /var/log/starvation-recovery.log
```

## Integration with Existing Tools

This tool integrates several existing diagnostic tools:

- `bead doctor` - Core diagnostics and repair
- `bead_visibility_diagnostic.py` - Deep visibility analysis
- `automated_bead_recovery.sh` - Checkpoint consistency checks
- `starvation-alert-revaluator` - Alert re-evaluation and closure

## Safety Features

- **Dry-run mode**: Test before executing changes
- **Backups**: Database backed up before rebuild
- **Incremental recovery**: Try least invasive actions first
- **Validation**: Verify recovery success before closing alerts
- **Audit trail**: All actions logged with timestamps

## Troubleshooting

### Tool fails to detect starvation

1. Check workspace path is correct
2. Verify `.beads/beads.db` exists
3. Run with `--verbose` for detailed output
4. Check logs for database access errors

### Recovery actions fail

1. Run `bead doctor --rehearse` manually
2. Check database permissions
3. Verify checkpoint directory is writable
4. Check for file locks or concurrent operations

### Alert beads not closing

1. Verify recovery actually succeeded (ready beads > 0)
2. Check alert bead labels include "starvation"
3. Run with `--dry-run` to test closure logic
4. Check logs for closure errors

## Dependencies

- Python 3.8+
- `bead` CLI (bead-rs backend)
- SQLite3
- Standard library only (no external Python packages)

## License

Part of the SEAM project - same license as parent repository.
