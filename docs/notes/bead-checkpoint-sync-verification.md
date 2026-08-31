# Bead Checkpoint Sync Verification

## Overview

This document describes the automated verification and reconciliation system for bead checkpoint sync, which ensures the SQLite database (`beads.db`) remains consistent with the git-tracked checkpoint (`.beads/checkpoint/current.json` and `.beads/checkpoint/forensic.jsonl`).

## Problem Statement

The bead-rs system maintains two sources of truth:
1. **SQLite database** (`.beads/beads.db`): Authoritative live state, not committed to git
2. **Checkpoint** (`.beads/checkpoint/`): Portable, durable copy tracked in git

These can become out of sync due to:
- Suppressed automatic publication (`--no-auto-flush` or `checkpoint.auto_flush`)
- Publication failures after committed mutations
- Manual operations on one source without the other
- Corruption or data loss

## Solution

The verification script `scripts/verify-bead-checkpoint-sync.sh` automatically:
1. Checks the sync relationship between database and checkpoint
2. Takes appropriate action based on the relationship
3. Verifies the result after reconciliation

## Usage

### Basic Verification
```bash
./scripts/verify-bead-checkpoint-sync.sh
```

### Verbose Output
```bash
./scripts/verify-bead-checkpoint-sync.sh --verbose
```

### Dry Run (See What Would Happen)
```bash
./scripts/verify-bead-checkpoint-sync.sh --dry-run
```

## Relationships and Actions

The script uses `bead sync status --format json` to determine the relationship:

| Relationship | Meaning | Action |
|-------------|---------|--------|
| `aligned` | ✓ Database and checkpoint match | No action needed |
| `behind` | Database has unflushed work | Run `bead sync flush-only` |
| `remote-advanced` | Checkpoint is ahead (verified) | Run `bead sync reconcile --actor system-automated-repair` |
| `covered-ahead-integrity-failure` | Checkpoint ahead but failed verification | Rebuild database: `bead sync import-only --input .beads/checkpoint/forensic.jsonl --restore-into-empty --actor system-automated-repair` |

## Exit Codes

- **0**: Verification passed (or successfully reconciled)
- **1**: Verification or reconciliation failed
- **2**: bead CLI not found or workspace not initialized

## Integration Points

### Manual Verification
Run the script manually when you suspect sync issues or before important operations.

### Git Pre-commit Hook (Optional)
Add to `.git/hooks/pre-commit` to verify before committing:
```bash
#!/bin/bash
./scripts/verify-bead-checkpoint-sync.sh || exit 1
```

### CI/CD Integration (Optional)
Add to CI workflows to ensure repository state is consistent:
```yaml
- name: Verify bead checkpoint sync
  run: ./scripts/verify-bead-checkpoint-sync.sh
```

### Periodic Monitoring (Optional)
Run via cron or systemd timer for proactive monitoring:
```bash
# Example: Check every hour
0 * * * * cd /path/to/seam && ./scripts/verify-bead-checkpoint-sync.sh
```

## Example Output

### Aligned (Success)
```
[2026-08-30T21:44:05-04:00] === Bead Checkpoint Sync Verification and Reconciliation ===
[2026-08-30T21:44:05-04:00] ✓ bead CLI found and workspace initialized
[2026-08-30T21:44:05-04:00] Checking checkpoint sync status...

[2026-08-30T21:44:05-04:00] Checkpoint Status:
[2026-08-30T21:44:05-04:00]   Relationship:        aligned
[2026-08-30T21:44:05-04:00]   Dirty:               false
[2026-08-30T21:44:05-04:00]   Root verified:       true
[2026-08-30T21:44:05-04:00]   View agrees:         true
[2026-08-30T21:44:05-04:00]   Ready to commit:     true

[2026-08-30T21:44:05-04:00] ✓ PASS: Database and checkpoint are aligned
[2026-08-30T21:44:05-04:00]   No action needed
```

### Behind (Reconciliation Performed)
```
[2026-08-30T21:45:00-04:00] === Bead Checkpoint Sync Verification and Reconciliation ===
[2026-08-30T21:45:00-04:00] ⚠️  WARNING: Database is behind checkpoint (live has unflushed work)
[2026-08-30T21:45:00-04:00]   Action: Running 'bead sync flush-only' to publish checkpoint from database
[2026-08-30T21:45:01-04:00] ✓ SUCCESS: Checkpoint flushed successfully
[2026-08-30T21:45:01-04:00] ✓ VERIFIED: Database and checkpoint are now aligned
```

## Dependencies

- **bead CLI**: Must be installed and available in PATH
- **jq**: Required for JSON parsing (install with `sudo apt install jq`)
- **bash**: Script uses bash features

## Related Commands

### Manual Verification
```bash
bead sync status                    # Human-readable status
bead sync status --format json      # Machine-readable status
```

### Manual Reconciliation
```bash
bead sync flush-only                                          # Database -> Checkpoint
bead sync reconcile --actor <you>                             # Merge ahead checkpoint
bead sync import-only --input .beads/checkpoint/forensic.jsonl --restore-into-empty --actor <you>  # Rebuild from checkpoint
```

### Diagnostics
```bash
bead doctor                        # Comprehensive diagnostics
bead doctor --repair              # Safe auto-repairs
```

## Recovery from Corruption

If the database is corrupted or completely out of sync:
```bash
# 1. Verify checkpoint is intact
bead sync status --format json | jq '.root_verified, .view_agrees'

# 2. Rebuild database from checkpoint
bead sync import-only \
  --input .beads/checkpoint/forensic.jsonl \
  --restore-into-empty \
  --actor system-automated-repair

# 3. Verify rebuild
./scripts/verify-bead-checkpoint-sync.sh --verbose
```

## Troubleshooting

### "jq: command not found"
Install jq: `sudo apt install jq` or `brew install jq`

### "bead: command not found"
Ensure bead-rs is installed and in PATH

### "Not in a bead workspace"
Ensure you're running the script from the repository root (where `.beads/config.json` exists)

### Persistent "covered-ahead-integrity-failure"
This indicates checkpoint corruption. Run `bead doctor` to diagnose and potentially restore from an earlier generation.

## Authoritative Source Principle

The **checkpoint is the authoritative source** for:
- Git history
- Repository state
- Disaster recovery

The database is authoritative only for:
- Live operations
- Uncommitted work
- Temporary state

When in doubt, trust the checkpoint and rebuild the database.
