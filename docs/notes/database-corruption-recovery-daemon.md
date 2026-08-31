# Database Corruption Recovery Daemon

## Overview

The Database Corruption Recovery Daemon (`database-recovery-daemon`) is an automated monitoring and recovery system that detects and repairs database corruption in bead workspaces. It specifically targets the "detection anomaly" pattern where the CLI reports open beads but direct SQLite queries return 0.

## Purpose

This daemon addresses the starvation alert pattern where:
- The bead CLI reports open beads exist (count > 0)
- Direct SQLite queries on `beads.db` return 0 (count == 0)
- This discrepancy indicates database corruption or sync issues

## How It Works

### 1. Detection Phase

Every 5 minutes (configurable), the daemon:

1. Scans all workspaces with `.beads/beads.db`
2. Runs dual queries on each workspace:
   - **CLI Query:** `bead list --status open --json`
   - **DB Query:** `sqlite3 beads.db "SELECT COUNT(*) FROM issues WHERE status = 0"`
3. Identifies the anomaly pattern: `CLI > 0 AND DB == 0`

### 2. Recovery Phase

When the anomaly is detected, the daemon executes a staged recovery:

#### Stage 1: Diagnostics
- Runs `bead doctor --json` to assess database health
- Captures diagnostic output for analysis

#### Stage 2: Repair
- Runs `bead doctor --repair` to fix common issues:
  - Index corruption
  - Missing indexes
  - Schema inconsistencies
  - Stale temporary files

#### Stage 3: Checkpoint Rebuild (if repair fails)
- Backs up corrupted database to `beads.db.backup`
- Reinitializes: `bead init`
- Imports from checkpoint: `bead sync import-only --input .beads/checkpoint/forensic.jsonl --restore-into-empty --actor system-recovery`
- Verifies recovery success

#### Stage 4: Verification
- Re-runs both CLI and DB queries
- Confirms: `CLI count > 0 AND DB count > 0`
- Runs `bead doctor --json` to verify database health

### 3. Cleanup Phase

On successful recovery:
- Closes all starvation-alert beads in the workspace
- Uses reason: `database-corruption-recovered: Database corruption detected and recovered via checkpoint rebuild. CLI-vs-DB anomaly resolved.`
- Creates a diagnostic bead documenting:
  - Detection anomaly details
  - Recovery method used
  - Diagnostics before/after
  - Alerts closed
  - Recovery outcome

## Usage

### Command Line

```bash
seam database-recovery-daemon [flags]
```

### Flags

- `--workspace-root` (default: `/home/coding`): Root directory containing all workspaces
- `--check-interval` (default: `5m`): How often to check for corruption
- `--state-path` (default: `.beads/database-recovery-state.json`): Path to daemon state JSON file
- `--diagnostic-log-path` (default: `.beads/diagnostics/database-recovery.log`): Path to diagnostic log file
- `--lease-name` (default: `seam-database-recovery`): Kubernetes Lease resource name
- `--lease-namespace` (default: `seam`): Kubernetes Lease namespace

### Example

```bash
seam database-recovery-daemon \
  --workspace-root /home/coding \
  --check-interval 5m \
  --lease-name seam-database-recovery \
  --lease-namespace seam
```

## Architecture

### Components

1. **Detection Engine**
   - `detectAnomalyPattern()`: Runs CLI and DB queries, compares results
   - `countBeadsByStatus()`: Counts beads via CLI
   - `countOpenBeadsDirectDB()`: Counts beads via SQLite

2. **Recovery Engine**
   - `runBeadDoctorDiagnostics()`: Runs diagnostics in read-only mode
   - `runBeadDoctorRepair()`: Executes bead doctor --repair
   - `runCheckpointRebuild()`: Rebuilds database from checkpoint

3. **Cleanup Engine**
   - `closeStarvationAlerts()`: Closes starvation-alert beads
   - `createDiagnosticBead()`: Creates recovery documentation

4. **State Management**
   - `saveState()`: Persists daemon state to disk
   - Tracks: total recoveries, per-workspace counts, last check time

### Integration with Other Daemons

This daemon complements existing SEAM daemons:

- **StarvationDiagnosticDaemon**: Diagnoses root causes; this daemon acts on the specific database-corruption pattern
- **BeadHealthDaemon**: Repairs bead-level issues (assigned-but-open); this daemon repairs database-level corruption
- **StarvationRecoveryLoop**: General starvation recovery; this daemon is specialized for database corruption

## State Persistence

The daemon maintains state in:

1. **State File** (`database-recovery-state.json`):
   ```json
   {
     "last_check_time": "2026-08-31T12:00:00Z",
     "total_recoveries": 5,
     "workspace_recoveries": {
       "SEAM": 3,
       "NEEDLE": 2
     },
     "last_recoveries": [...]
   }
   ```

2. **Diagnostic Log** (`database-recovery.log`):
   ```
   [2026-08-31T12:00:00Z] SEAM: detected=true cli=5 db=0 method=checkpoint-rebuild success=true alerts_closed=2 beads_created=1
   ```

## Kubernetes Deployment

### Lease-Based Leadership

The daemon uses Kubernetes Lease for distributed coordination:

```yaml
apiVersion: coordination.k8s.io/v1
kind: Lease
metadata:
  name: seam-database-recovery
  namespace: seam
spec:
  holderIdentity: ""
  leaseDurationSeconds: 15
```

Only one daemon instance (the lease holder) performs active checks.

### Example Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: seam-database-recovery
  namespace: seam
spec:
  replicas: 3
  selector:
    matchLabels:
      app: seam-database-recovery
  template:
    metadata:
      labels:
        app: seam-database-recovery
    spec:
      containers:
      - name: daemon
        image: ronaldraygun/seam:v1.2.3
        command: ["seam", "database-recovery-daemon"]
        args:
        - --workspace-root=/home/coding
        - --check-interval=5m
        - --lease-name=seam-database-recovery
        - --lease-namespace=seam
```

## Monitoring

### Metrics

The daemon provides the following observable metrics:

- **Detection Events**: CLI vs DB discrepancy detected
- **Recovery Attempts**: Recovery started
- **Recovery Success**: Recovery completed successfully
- **Recovery Method**: Which recovery strategy worked (repair vs rebuild)
- **Alerts Closed**: Number of starvation-alert beads closed
- **Beads Created**: Number of diagnostic beads created

### Log Patterns

```
[DatabaseRecovery] Starting database corruption recovery daemon (check interval: 5m)
[DatabaseRecovery] Checking 3 workspaces for database corruption
[DatabaseRecovery] ✓ Anomaly detected in SEAM (CLI=5 open, DB=0 open)
[DatabaseRecovery] ✓ Recovery succeeded in SEAM (CLI=5, DB=5)
[DatabaseRecovery] ✓ Closed starvation alert seam-abc123: Starvation Alert
[DatabaseRecovery] ✓ Created diagnostic bead def456 in SEAM
[DatabaseRecovery] Check complete: 1 recoveries this run, 5 total
```

## Failure Modes

### Detection Failures

- **CLI Query Fails**: Logs error, continues to next workspace
- **DB Query Fails**: Logs warning, uses CLI count (may miss corruption)

### Recovery Failures

- **bead doctor --repair Fails**: Escalates to checkpoint rebuild
- **Checkpoint Rebuild Fails**: Restores backup, marks recovery as failed
- **Verification Fails**: Does not close alerts or create diagnostic bead

### State Persistence Failures

- **Save State Fails**: Logs error, continues operation
- **Load State Fails**: Starts fresh with zero counts

## Safety Features

1. **Backup Before Rebuild**: Corrupted databases are backed up before rebuild
2. **Restore on Failure**: If rebuild fails, backup is automatically restored
3. **Verification Required**: Recovery is only considered successful if verification passes
4. **No False Positives**: Only acts on specific CLI > 0 AND DB == 0 pattern
5. **Read-Only Diagnostics**: First stage is diagnostics, not mutation

## Testing

### Unit Tests

Located in `internal/server/database_corruption_recovery_daemon_test.go`:

- `TestDatabaseCorruptionRecoveryDaemon_DetectAnomalyPattern`: Verifies detection logic
- `TestDatabaseCorruptionRecoveryDaemon_RunBeadDoctorDiagnostics`: Tests diagnostics
- `TestDatabaseCorruptionRecoveryDaemon_RunCheckpointRebuild`: Tests rebuild
- `TestDatabaseCorruptionRecoveryDaemon_CloseStarvationAlerts`: Tests alert cleanup
- `TestDatabaseCorruptionRecoveryDaemon_CreateDiagnosticBead`: Tests bead creation
- `TestDatabaseCorruptionRecoveryDaemon_SaveState`: Tests state persistence

### Running Tests

```bash
# Run all tests
go test ./internal/server/...

# Run specific test
go test ./internal/server/... -run TestDatabaseCorruptionRecoveryDaemon_DetectAnomalyPattern

# Run with verbose output
go test ./internal/server/... -v -run TestDatabaseCorruptionRecoveryDaemon
```

## Troubleshooting

### Daemon Not Starting

**Symptom**: Command fails with "Failed to create daemon"

**Causes**:
- Invalid workspace root path
- Permission denied on state file
- Lease creation failed (if using distributed mode)

**Solution**:
```bash
# Verify workspace root exists
ls /home/coding

# Check permissions on .beads directory
ls -la /home/coding/SEAM/.beads

# Verify lease exists (if using distributed mode)
kubectl get lease seam-database-recovery -n seam
```

### Detection Not Working

**Symptom**: Daemon runs but never detects corruption

**Causes**:
- Check interval too long
- Wrong workspace root
- Database not actually corrupted

**Solution**:
```bash
# Check diagnostic log
tail -f /home/coding/.beads/diagnostics/database-recovery.log

# Manually verify anomaly pattern
cd /home/coding/SEAM
bead list --status open --json | jq '. | length'
sqlite3 .beads/beads.db "SELECT COUNT(*) FROM issues WHERE status = 0"
```

### Recovery Not Completing

**Symptom**: Detection succeeds but recovery fails

**Causes**:
- Checkpoint file missing or corrupted
- Insufficient disk space for backup
- Database locked by another process

**Solution**:
```bash
# Verify checkpoint exists
ls -la /home/coding/SEAM/.beads/checkpoint/forensic.jsonl

# Check disk space
df -h /home/coding

# Check for database locks
lsof +D /home/coding/SEAM/.beads/beads.db
```

## Related Documentation

- [Bead Health Daemon](./bead-health-daemon.md) - Bead-level repair automation
- [Starvation Diagnostic Daemon](./starvation-diagnostic-daemon.md) - Root cause analysis
- [Starvation Recovery Loop](./starvation-recovery-loop.md) - General starvation recovery
- [Checkpoint System](../research/checkpoint-architecture.md) - Checkpoint format and rebuild

## Changelog

### 2026-08-31
- Initial implementation
- 5-minute check interval
- Two-stage recovery (repair → rebuild)
- Automatic alert closure and diagnostic bead creation
- State persistence and diagnostic logging
