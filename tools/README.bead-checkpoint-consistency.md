# Bead Checkpoint Consistency Check

Automated health check and repair tool for bead database consistency with checkpoint files.

## Overview

This tool prevents the scenario where a fresh clone or corrupted database silently loses beads. It verifies bead database consistency with the checkpoint and performs automated restoration when divergence is detected.

## Problem Statement

In bead-rs workspaces, the **SQLite database (`.beads/beads.db`)** is the live store, while the **checkpoint (`.beads/checkpoint/`)** is the durable backup. When the database becomes corrupted or a fresh clone is missing the database, beads can be silently lost.

This script detects and repairs such scenarios automatically.

## What It Does

1. **Flushes checkpoint** - Runs `bead sync flush-only` to ensure checkpoint is current
2. **Compares counts** - Checks bead count between database and checkpoint
3. **Detects divergence** - Identifies significant differences (>10 beads)
4. **Auto-restores** - Runs recovery if divergence detected:
   - Backs up existing database
   - Reinitializes database schema (`bead init`)
   - Restores from checkpoint (`bead sync import-only`)
5. **Verifies repair** - Confirms restoration succeeded

## Usage

### Basic Check

```bash
./tools/bead_checkpoint_consistency_check.sh
```

### Dry-Run Mode (Recommended First)

```bash
./tools/bead_checkpoint_consistency_check.sh --dry-run
```

Dry-run shows what would happen without making changes.

### Exit Codes

- `0` - Database is consistent, OR repair succeeded
- `1` - Divergence detected and successfully repaired
- `2` - Divergence detected but repair failed
- `3` - Critical error (missing checkpoint, beyond repair)

## Integration Examples

### Manual Health Check

Run periodically or when you suspect database issues:

```bash
# Quick check
./tools/bead_checkpoint_consistency_check.sh

# Full check with repair
./tools/bead_checkpoint_consistency_check.sh
```

### Automated Checks

#### Cron Job

Add to crontab for daily checks:

```cron
# Daily bead consistency check at 2 AM
0 2 * * * cd /home/coding/SEAM && ./tools/bead_checkpoint_consistency_check.sh >> /var/log/bead-health.log 2>&1
```

#### Git Pre-Push Hook

Prevent pushing with corrupted database:

```bash
# .git/hooks/pre-push
./tools/bead_checkpoint_consistency_check.sh --dry-run || exit 1
```

#### NEEDLE Worker Health Check

Integrate into worker startup:

```bash
# Before claiming beads
./tools/bead_checkpoint_consistency_check.sh --dry-run || {
    echo "Database inconsistent - cannot claim beads"
    exit 1
}
```

## How It Works

### Checkpoint Structure

The checkpoint contains:

- `current.json` - Metadata including `issue_count`
- `forensic.jsonl` - Complete bead history (JSONL format)
- `objects/` - Individual bead snapshots

### Restoration Process

When divergence is detected:

1. **Backup** - Save `beads.db` to `.beads/recovery-backups/beads.db.before-restore.TIMESTAMP`
2. **Remove** - Delete corrupt database
3. **Reinitialize** - Run `bead init` to create fresh schema
4. **Import** - Run `bead sync import-only --input .beads/checkpoint/forensic.jsonl --restore-into-empty`
5. **Verify** - Confirm database has beads

### Divergence Threshold

The script allows a tolerance of 10 beads difference to account for:
- Status filtering (checkpoint includes all, database query may filter)
- Archival differences
- Race conditions during active updates

Divergence >10 beads triggers restoration.

## Troubleshooting

### "Database is empty but checkpoint has N beads"

Database file exists but has no beads. Restoration is required.

### "Significant divergence detected: N beads missing"

Database has significantly fewer beads than checkpoint. Restoration is required.

### "Checkpoint validation failed"

The checkpoint files are missing or corrupted. This is critical:
- Check if `.beads/checkpoint/` exists
- Verify `forensic.jsonl` has content
- If checkpoint is missing, database cannot be recovered

### Restoration fails

Check logs:
- `/tmp/bead-check-flush.log` - Checkpoint flush output
- `/tmp/bead-restore.log` - Restoration output

Common failures:
- `bead init` fails - Database file locked or permissions issue
- `bead sync import-only` fails - Checkpoint corruption or disk full

## Design Decisions

### Why Check Counts Instead of Content?

Comparing counts is fast and sufficient for detecting corruption:
- Database corruption typically results in zero or very low counts
- Fresh clones have empty databases
- Full content comparison would be prohibitively slow for large workspaces

### Why Tolerance of 10 Beads?

Prevents false positives from:
- Status filtering differences
- Concurrent updates during check
- Normal archival operations

### Why Auto-Repair?

Silent failures are dangerous:
- Manual intervention may not happen in time
- Workers claiming from corrupted database silently lose work
- Automated repair prevents bead loss

## Related Files

- `.beads/checkpoint/current.json` - Checkpoint metadata
- `.beads/checkpoint/forensic.jsonl` - Complete bead history
- `.beads/beads.db` - Live bead database
- `CLAUDE.md` - Bead backend detection (bead-rs vs bf)

## See Also

- [Bead-rs documentation](https://github.com/jedarden/bead-rs)
- [NEEDLE fleet dispatch](NEEDLE/docs/adr/015-concurrent-same-repo-worker-isolation.md)
- [SEAM project README](README.md)
