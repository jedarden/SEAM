# Bead Visibility Diagnostic and Repair Daemon

## Overview

The Bead Visibility Diagnostic Daemon is a background service that periodically queries the bead database using multiple methods (CLI, direct SQLite, checkpoint) and compares results to detect visibility bugs. When discrepancies are detected, it automatically attempts recovery and creates diagnostic beads for persistent issues.

## Purpose

This daemon addresses "bead starvation" issues where:
- The `bead list --ready` CLI command returns 0 results
- But the SQLite database contains open, unassigned, unblocked beads
- Or beads are stuck in invalid states (assigned-but-open)

These discrepancies prevent NEEDLE workers from finding work, causing starvation.

## How It Works

### Query Strategies

The daemon queries beads using three independent methods:

1. **CLI queries**: `bead list --status open --json` and `bead list --ready --json`
2. **Direct SQLite**: Queries `beads.db` for `base_status='open'` and ready conditions
3. **Checkpoint**: Parses `.beads/checkpoint/forensic.jsonl` for open beads

### Discrepancy Detection

The daemon detects four types of visibility bugs:

1. **cli_open_zero_sqlite_positive**: CLI returns 0 open beads, but SQLite has records
2. **cli_ready_zero_sqlite_positive**: CLI returns 0 ready beads, but SQLite has ready records
3. **checkpoint_desync**: Checkpoint significantly out of sync with SQLite (>5 bead difference)
4. **assigned_but_open**: Beads stuck in assigned-but-open state (should be in_progress or released)

### Automated Recovery

When discrepancies are detected:

1. **Flush checkpoint**: Runs `bead sync flush-only` to refresh the checkpoint
2. **Re-verify**: Queries all strategies again to check if the issue is resolved
3. **Release stuck beads**: For assigned-but-open beads, runs `bead release <id>`
4. **Create diagnostic bead**: If the issue persists, creates a bead with full diagnostic details

## Installation

### Method 1: Systemd User Service (Recommended)

```bash
# Copy the service file to systemd user directory
cp declarative-config/k8s/rs-manager/seam-observer-serviceaccounts/bead-visibility-diagnostic.service \
   ~/.config/systemd/user/

# Reload systemd
systemctl --user daemon-reload

# Enable and start the service
systemctl --user enable bead-visibility-diagnostic.service
systemctl --user start bead-visibility-diagnostic.service

# Check status
systemctl --user status bead-visibility-diagnostic.service

# View logs
journalctl --user -u bead-visibility-diagnostic.service -f
```

### Method 2: Manual Testing

```bash
# Run once to verify
./tools/bead_visibility_diagnostic.py --once

# Run in foreground for testing
./tools/bead_visibility_diagnostic.py --interval 1
```

### Method 3: Background Process

```bash
# Start in background with nohup
nohup ./tools/bead_visibility_diagnostic.py --interval 5 \
  --log-dir .beads/diagnostics > /dev/null 2>&1 &

# Check it's running
ps aux | grep bead_visibility_diagnostic
```

## Configuration

### Command Line Options

```bash
./tools/bead_visibility_diagnostic.py [OPTIONS]

Options:
  --workspace PATH      Path to workspace (default: /home/coding/SEAM)
  --interval MINUTES   Check interval in minutes (default: 5)
  --once               Run once and exit (don't loop)
  --log-dir PATH       Directory for log files (default: /tmp/bead-visibility-diagnostics)
```

### Systemd Service Configuration

The service file includes:
- **Restart on failure**: Automatic restart if the script crashes
- **Security hardening**: ProtectSystem, ProtectHome, NoNewPrivileges
- **Resource limits**: MemoryMax=512M, CPUQuota=50%
- **5-minute check interval**: Configurable via ExecStart

To change the interval:
```bash
# Edit the service file
vim ~/.config/systemd/user/bead-visibility-diagnostic.service

# Change the --interval value in ExecStart
ExecStart=/home/coding/SEAM/tools/bead_visibility_diagnostic.py --interval 10 ...

# Reload and restart
systemctl --user daemon-reload
systemctl --user restart bead-visibility-diagnostic.service
```

## Output and Logs

### Log Files

Logs are written to `.beads/diagnostics/` (or configured log directory):

```
.beads/diagnostics/
├── visibility-diagnostic-20260831-034946.log    # Detailed run log
├── check-results-20260831-034946.json          # Machine-readable results
└── visibility-diagnostic-*.log                  # Historical logs
```

### JSON Results Format

Each check produces a JSON file with:
```json
{
  "timestamp": "2026-08-31T03:49:46.134229",
  "discrepancies": [
    {
      "type": "cli_open_zero_sqlite_positive",
      "cli_open_count": 0,
      "sqlite_open_count": 15,
      "sqlite_open_ids": ["seam-xxx1", "seam-xxx2", ...]
    }
  ],
  "repairs_attempted": ["checkpoint_flush", "release_seam-xxx"],
  "diagnostic_beads_created": ["cli_open_zero_sqlite_positive"]
}
```

### Diagnostic Beads

If issues persist after automated repair attempts, the daemon creates diagnostic beads with:
- **Title**: `[Visibility Bug] <type> detected at <timestamp>`
- **Priority**: 3 (medium)
- **Type**: task
- **Description**: Full diagnostic details including:
  - Detection timestamp
  - Query results from all strategies
  - Discrepancy details
  - Recommended actions

## Monitoring

### Check Service Status

```bash
# Systemd service status
systemctl --user status bead-visibility-diagnostic.service

# Recent logs
journalctl --user -u bead-visibility-diagnostic.service -n 50

# Follow logs in real-time
journalctl --user -u bead-visibility-diagnostic.service -f
```

### Check Recent Results

```bash
# View most recent check results
cat .beads/diagnostics/check-results-*.json | jq -s '.[-1]'

# Check for discrepancies in last hour
find .beads/diagnostics -name "check-results-*.json" -mmin -60 \
  -exec jq -r '.discrepancies[]?.type' {} \; | sort | uniq -c
```

### Alert on Persistent Issues

The daemon itself creates diagnostic beads, but you can also monitor:

```bash
# Check if service is running
if ! systemctl --user is-active --quiet bead-visibility-diagnostic.service; then
    echo "WARNING: bead-visibility-diagnostic service is not running"
fi

# Check for recent discrepancies
recent_discrepancies=$(find .beads/diagnostics -name "check-results-*.json" -mmin -60 \
  -exec jq -r '.discrepancies | length' {} \; | awk '{s+=$1} END {print s}')
if [ "$recent_discrepancies" -gt 5 ]; then
    echo "WARNING: $recent_discrepancies discrepancies detected in last hour"
fi
```

## Troubleshooting

### Service Won't Start

```bash
# Check the service file syntax
systemd-analyze verify ~/.config/systemd/user/bead-visibility-diagnostic.service

# Check if the script is executable
ls -l tools/bead_visibility_diagnostic.py

# Try running manually first
./tools/bead_visibility_diagnostic.py --once
```

### No Logs Appearing

```bash
# Check if log directory exists and is writable
ls -ld .beads/diagnostics

# Check journal for service errors
journalctl --user -u bead-visibility-diagnostic.service --since "5 minutes ago"
```

### Beads Not Being Detected

```bash
# Verify database path
ls -l .beads/beads.db

# Check database directly
python3 -c "
import sqlite3
conn = sqlite3.connect('.beads/beads.db')
c = conn.cursor()
c.execute('SELECT COUNT(*) FROM issues WHERE base_status=\"open\"')
print('Open beads:', c.fetchone()[0])
conn.close()
"

# Check CLI
bead list --status open | wc -l
```

### Too Many False Positives

The daemon may detect legitimate discrepancies during:
- Active bead operations (create, update, close)
- Checkpoint refresh in progress
- Database migration or repair

To reduce noise:
1. Increase the check interval (e.g., 10 or 15 minutes)
2. Add hysteresis (only alert after N consecutive failures)
3. Filter by discrepancy type

## Integration with Existing Tools

This daemon complements:
- **`tools/integrated_starvation_recovery.sh`**: Higher-level starvation recovery
- **`bead doctor`**: Database integrity checks and repairs
- **`bead sync flush-only`**: Checkpoint refresh

The visibility daemon is a **continuous monitor** that catches issues early, while integrated_starvation_recovery.sh is a **reactive repair** tool for full starvation conditions.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ bead_visibility_diagnostic.py                                │
│                                                               │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │ CLI Queries  │  │ SQLite Query │  │Checkpoint     │      │
│  │              │  │              │  │Query          │      │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘      │
│         │                 │                 │               │
│         └─────────────────┼─────────────────┘               │
│                           │                                  │
│                    ┌──────▼──────┐                           │
│                    │  Compare &  │                           │
│                    │  Detect     │                           │
│                    │  Discrepancy│                           │
│                    └──────┬──────┘                           │
│                           │                                  │
│                    ┌──────▼──────┐                           │
│                    │ Automated   │                           │
│                    │ Recovery    │                           │
│                    └──────┬──────┘                           │
│                           │                                  │
│                    ┌──────▼──────┐                           │
│                    │ Create      │                           │
│                    │ Diagnostic  │                           │
│                    │ Bead        │                           │
│                    └─────────────┘                           │
└─────────────────────────────────────────────────────────────┘
```

## Future Enhancements

Potential improvements:
1. **Metrics integration**: Export Prometheus metrics for visibility status
2. **Multi-workspace support**: Monitor multiple workspaces in one daemon
3. **Webhook alerts**: Send notifications for persistent issues
4. **Auto-repair escalation**: Try multiple repair strategies in sequence
5. **Historical tracking**: Track discrepancy patterns over time
6. **Machine learning**: Predict visibility issues before they occur

## License

Part of the SEAM project. See project LICENSE for details.
