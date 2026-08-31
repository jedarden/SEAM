# Transient Starvation Backoff Monitor

## Overview

This daemon implements exponential backoff retry for starvation conditions before creating alert beads. Most starvation is transient and resolves within the first retry interval. This daemon significantly reduces false-positive alert noise.

## How It Works

1. **Detection**: Monitors workspaces for starvation conditions (ready=0 with open beads > 0)
2. **Infrastructure Health Check**: Runs all infrastructure checks (database, checkpoint, index, filter, workers)
3. **Backoff Sequence**:
   - If infrastructure is **unhealthy**: Escalate immediately (no backoff)
   - If infrastructure is **healthy**: Start backoff tracking
4. **Retry Intervals**: 30s → 2m → 5m → 15m
5. **Resolution**:
   - If starvation resolves at any point: Clear tracking (no alert created)
   - If starvation persists through all intervals: Create alert bead

## Benefits

- **Reduces false positives**: Transient issues resolve before alerts are created
- **Maintains responsiveness**: Real infrastructure failures still alert immediately
- **Minimizes noise**: Only persistent starvation conditions create beads
- **Automatic resolution**: Self-resolving conditions never create alerts

## Backoff Intervals

| Interval | Duration | Purpose |
|---------|----------|---------|
| 0       | 30s      | First retry - catches brief database locks |
| 1       | 2m       | Second retry - catches worker restarts |
| 2       | 5m       | Third retry - catches checkpoint sync delays |
| 3       | 15m      | Final retry - catches persistent transient issues |

Total backoff duration: **22 minutes** before alert creation

## Installation

```bash
cd tools/starvation-backoff-monitor
make install
```

## Usage

### Basic Usage

```bash
starvation-backoff-monitor --workspace-root /home/coding
```

### With Custom Interval

```bash
starvation-backoff-monitor --workspace-root /home/coding --interval 1m
```

### With Leader Election (Kubernetes)

```bash
starvation-backoff-monitor \
  --enable-lease \
  --lease-namespace seam-monitoring \
  --lease-name starvation-backoff-monitor \
  --workspace-root /home/coding
```

### All Options

```
--workspace-root DIR
    Root directory containing all workspaces (default: /home/coding)

--interval DURATION
    Check interval between workspace scans (default: 30s)

--enable-lease
    Enable Kubernetes Lease leader election

--lease-namespace NS
    Kubernetes Lease namespace (default: seam-monitoring)

--lease-name NAME
    Kubernetes Lease name (default: starvation-backoff-monitor)

--lease-identity ID
    Kubernetes Lease identity (default: hostname)

--verbose
    Enable verbose logging with microsecond timestamps
```

## Kubernetes Deployment

### ServiceAccount

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: starvation-backoff-monitor
  namespace: seam-monitoring
```

### Role

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: starvation-backoff-monitor
  namespace: seam-monitoring
rules:
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["get", "create", "update"]
```

### RoleBinding

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: starvation-backoff-monitor
  namespace: seam-monitoring
subjects:
- kind: ServiceAccount
  name: starvation-backoff-monitor
  namespace: seam-monitoring
roleRef:
  kind: Role
  name: starvation-backoff-monitor
  apiGroup: rbac.authorization.k8s.io
```

### Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: starvation-backoff-monitor
  namespace: seam-monitoring
spec:
  replicas: 2
  selector:
    matchLabels:
      app: starvation-backoff-monitor
  template:
    metadata:
      labels:
        app: starvation-backoff-monitor
    spec:
      serviceAccountName: starvation-backoff-monitor
      containers:
      - name: monitor
        image: ronaldraygun/starvation-backoff-monitor:latest
        args:
        - --enable-lease
        - --lease-namespace=seam-monitoring
        - --workspace-root=/home/coding
        - --verbose
        volumeMounts:
        - name: workspaces
          mountPath: /home/coding
          readOnly: true
      volumes:
      - name: workspaces
        hostPath:
          path: /home/coding
          type: Directory
```

## Alert Bead Structure

When an alert bead is finally created (after exhausting backoff), it includes:

```markdown
**PERSISTENT STARVATION CONDITION**

Workspace: <name>
First detected: <timestamp>
Alert created: <timestamp>

**Current State:**
- Open beads: N
- Ready beads: 0
- Infrastructure: HEALTHY

**Backoff History:**
- Total backoff duration: X minutes
- Retry intervals attempted: 4
- Backoff intervals: 30s, 2m, 5m, 15m

**Retry Checks:**
  1. <timestamp>: ready=0, open=N, infra_ok=true
  2. <timestamp>: ready=0, open=N, infra_ok=true
  3. <timestamp>: ready=0, open=N, infra_ok=true
  4. <timestamp>: ready=0, open=N, infra_ok=true
  5. <timestamp>: ready=0, open=N, infra_ok=true

**Analysis:**
This starvation condition persisted through all exponential backoff intervals.
All infrastructure checks passed (database, checkpoint, index, filter, workers).
This suggests a persistent issue rather than a transient problem.

**Action Required:**
Manual investigation may be needed. The starvation-alert-self-resolution daemon
will continue to monitor and auto-resolve if work becomes available.

Created by: TransientStarvationBackoff daemon after exhausting backoff sequence.
```

## Labels

Alert beads created by this daemon include:
- `starvation-alert`: Identifies the bead as a starvation alert
- `persistent-starvation`: Indicates starvation persisted through backoff
- `starvation:transient-failed`: Specific root cause label

## Monitoring

The daemon maintains internal state for all pending backoff events. To inspect:

```bash
# The daemon logs all state transitions
# Watch for these patterns:

[BackoffMonitor] SEAM: open=10, ready=0, infra_ok=true, tracking=false
[BackoffMonitor] SEAM: Starvation detected with healthy infrastructure, starting backoff (interval 0: 30s)
[BackoffMonitor] SEAM: Starvation persists, advancing to backoff interval 1: 2m
[BackoffMonitor] SEAM: Starvation persisted through 4 backoff intervals, creating alert bead
[BackoffMonitor] ✓ Created starvation alert seam-abc123 for SEAM after 5 retries (22.5 min)

# Or when starvation resolves before alert:
[BackoffMonitor] SEAM: Starvation resolved (ready=5, open=10), clearing pending event
```

## Comparison with Other Daemons

| Daemon | Purpose | Backoff | Infrastructure Check |
|--------|---------|---------|----------------------|
| **starvation-backoff-monitor** | PREVENT false positives | Yes (30s, 2m, 5m, 15m) | Yes - healthy vs unhealthy |
| **starvation-alert-self-resolution** | RESOLVE existing alerts | No (5min check interval) | Yes - root cause analysis |
| **starvation-alert-contradiction-detector** | DETECT contradictions | No | Checks immediate state |

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  Transient Starvation Backoff Monitor                        │
│  (Pre-Alert Phase)                                           │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
                    ┌─────────────────┐
                    │ Check Workspaces │
                    │  (every 30s)     │
                    └─────────────────┘
                              │
                              ▼
                    ┌─────────────────┐
                    │ Starvation?     │
                    │ (ready=0,       │
                    │  open>0)         │
                    └─────────────────┘
                         │         │
                    No  │         │ Yes
                         │         │
                         │         ▼
                         │    ┌─────────────────┐
                         │    │ Infrastructure  │
                         │    │ Healthy?         │
                         │    └─────────────────┘
                         │         │
                         │    No   │    Yes
                         │    │    │     │
                         │    ▼    │     ▼
                         │  Escalate│  Start Backoff
                         │  Now     │  (30s → 2m → 5m → 15m)
                         │         │         │
                         │         │         ├─ Resolves? → Clear (no alert)
                         │         │         │
                         │         │         └─ Persists? → Create Alert
                         │         │
                         ▼         ▼
                      (No Action)  (Alert Bead Created)
                                       │
                                       ▼
                        ┌────────────────────────┐
                        │ Starvation Alert       │
                        │ Self-Resolution Daemon │
                        └────────────────────────┘
```

## Testing

Run the test suite:

```bash
cd tools/starvation-backoff-monitor
make test
```

Run with coverage:

```bash
make test-coverage
```

## Troubleshooting

### Daemon not creating alerts

1. Check if infrastructure is healthy:
   ```bash
   bead doctor --rehearse
   ```

2. Verify starvation condition exists:
   ```bash
   bead list --ready --json | jq '. | length'
   bead list --status open --json | jq '. | length'
   ```

3. Check logs for backoff progress:
   ```
   [BackoffMonitor] <workspace>: Starvation persists, advancing to backoff interval N: <duration>
   ```

### Alerts created too quickly

If you're seeing alerts created without full backoff:
1. Check infrastructure health - unhealthy infrastructure skips backoff
2. Verify intervals are configured correctly
3. Check for repeated rapid restarts (state is in-memory)

### Alerts never created

If starvation persists but no alerts are created:
1. Verify daemon is running: `ps aux | grep starvation-backoff-monitor`
2. Check for stuck workers (infrastructure check fails)
3. Review logs for infrastructure failures

## Development

Build locally:

```bash
cd tools/starvation-backoff-monitor
make build
```

Run with verbose output:

```bash
./starvation-backoff-monitor --workspace-root /home/coding --verbose
```

## Related Documentation

- [Starvation Alert Self-Resolution](../../internal/server/starvation_alert_self_resolution.go)
- [Root Cause Analyzer](../../internal/server/root_cause_analyzer.go)
- [Bead Doctor Documentation](https://github.com/bead-rs/bead-rs)
