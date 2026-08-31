# Starvation Alert Re-evaluator

Automated re-evaluation and closure of starvation alert beads when work becomes available.

## Purpose

When starvation is detected (no ready beads but open/invisible beads exist), a starvation alert bead is created to flag the condition. This tool periodically checks all open starvation alert beads and automatically closes them when the ready frontier regains candidates, implementing a cleanup mechanism for transient starvation.

## How It Works

1. **Periodic scanning**: Every 7 minutes (configurable), scans all workspaces for open starvation alert beads
2. **Ready frontier check**: For each workspace with alerts, queries the current ready frontier
3. **Auto-closure**: If candidates now exist, closes all starvation alert beads with reason `'Starvation condition resolved - work now available'`
4. **Audit logging**: Records every resolution to an audit log for compliance tracking

## Alert Identification

Starvation alert beads are identified by:
- **Primary method**: Label `starvation-alert` (recommended for new alerts)
- **Fallback**: Beads with "starvation" in the title (backwards compatibility)

## Usage

### One-shot mode (single check)
```bash
./starvation-alert-revaluator --once
```

### Loop mode (continuous monitoring)
```bash
./starvation-alert-revaluator --interval 7m
```

### Dry-run (see what would happen)
```bash
./starvation-alert-revaluator --dry-run --verbose
```

### With audit logging
```bash
./starvation-alert-revaluator --log-file /var/log/seam/alert-resolutions.jsonl
```

## Flags

- `--workspace-root`: Root directory containing all workspaces (default: `/home/coding`)
- `--interval`: Check interval (default: `7m`)
- `--alert-label`: Label identifying starvation alert beads (default: `starvation-alert`)
- `--log-file`: Path to audit log file (JSONL format)
- `--dry-run`: Show what would be done without making changes
- `--verbose`: Enable verbose logging
- `--once`: Run once and exit

## Audit Log Format

Each resolution is logged as a JSONL entry:
```json
{
  "alert_id": "seam-abc123",
  "workspace": "SEAM",
  "timestamp": "2026-08-30T23:45:00Z",
  "resolved": true,
  "ready_count": 5,
  "closed_with_reason": "Starvation condition resolved - work now available"
}
```

## Integration

The revaluator should run as a background service alongside the starvation recovery loop:
- **Starvation recovery loop**: Detects starvation and runs automated recovery
- **Alert revaluator**: Cleans up resolved alerts when work becomes available

Both services can run simultaneously without conflict - the recovery loop creates and manages alerts, while the revaluator closes them when appropriate.

## Building

```bash
cd tools/starvation-alert-revaluator
go build -o starvation-alert-revaluator .
```

## Deployment

Deploy as a systemd service (see `starvation-alert-revaluator.service`):
- Should run continuously in loop mode
- Requires access to workspace root and bead CLI
- Should log to a persistent location for audit
