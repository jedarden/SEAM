# SEAM Retirement Evaluator

**Phase 8.5**: Autonomous retirement evaluation with GitHub PR opening

## Overview

The `seam-retirement-evaluator` is a deployable component that evaluates route versions for retirement eligibility based on observed traffic patterns. It operates as a separate deployment from SEAM, with its own ServiceAccount and OpenBao role, ensuring complete isolation from SEAM's credential paths.

## Architecture

```
seam-retirement-evaluator (Deployment)
├── ServiceAccount: seam-retirement-evaluator
├── OpenBao Role: seam-retirement-evaluator-policy
│   ├── Reads: secret/evaluators/seam-retirement-evaluator/*
│   ├── Reads: secret/monitoring/victoriametrics/*
│   └── Explicitly DENIED: secret/seam/routes/* (isolation from SEAM)
└── GitHub Token: secret/evaluators/seam-retirement-evaluator/github-token
```

### Key Isolation Guarantees

- **Dedicated OpenBao Path**: `secret/evaluators/seam-retirement-evaluator/*` is NOT readable by SEAM
- **Explicit Deny Policy**: SEAM policy explicitly denies access to evaluator paths
- **Separate SA**: Independent ServiceAccount with minimal RBAC
- **No Cross-Access**: Evaluator cannot read SEAM route secrets; SEAM cannot read evaluator credentials

## Functionality

### 1. Traffic Analysis

The evaluator queries VictoriaMetrics for per-route-version traffic metrics:

```promql
# Per-route-version request counter (Phase 8.4 metric)
seam_route_version_requests_total{route="...", spec_version="..."}
```

For each `(route, x-api-version)` combination, it calculates:

- **Observed max inter-request gap**: Maximum time between requests
- **Quiet-since**: Timestamp of last observed request
- **Total history**: Duration of available metrics

### 2. Retirement Eligibility

A route version is eligible for retirement when:

1. **Zero observed traffic** in evaluation window (NECESSARY condition)
2. Quiet period exceeds evaluation window:
   ```
   evaluation_window = max(3 x observed_max_gap, 7 days)
   ```
3. Sufficient history exists (≥14 days), otherwise 7-day floor applies

**Nothing retires autonomously** - the evaluator only OPENS PRs; humans merge.

### 3. GitHub PR Opening

For eligible route versions, the evaluator opens a PR to `jedarden/declarative-config`:

```yaml
# Added to fragment:
x-seam-deprecated:
  since: "2026-08-28"
  sunset: "2026-11-26"
  brownouts:
    - start: "2026-09-27T00:00:00Z"
      end: "2026-10-04T00:00:00Z"
    - start: "2026-10-27T00:00:00Z"
      end: "2026-11-03T00:00:00Z"
    - start: "2026-11-19T00:00:00Z"
      end: "2026-11-26T00:00:00Z"
```

### PR Structure

- **Title**: `feat(seam): deprecate <route> (version <v>) - zero traffic for <duration>`
- **Body**: Detailed eligibility metrics, timeline, and verification steps
- **Labels**: `seam`
- **Branch**: `seam-deprecate-<route>-<version>`

### 4. The Verdict Channel

The deprecation verdict travels through SEAM's existing hot-reload path:

1. PR merged → ConfigMap updated → ArgoCD syncs
2. SEAM hot-reloads fragment with `x-seam-deprecated`
3. DeprecationMiddleware emits Deprecation/Sunset headers
4. BrownoutScheduler returns 410 Gone during windows
5. `/changes` endpoint lists deprecation status

**No deployment required** - the verdict IS the fragment.

## Configuration

### Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `VICTORIAMETRICS_ENDPOINT` | No | `http://victorialogs-single...` | VictoriaMetrics query endpoint |
| `GITHUB_OWNER` | No | `jedarden` | GitHub repository owner |
| `GITHUB_REPO` | No | `declarative-config` | GitHub repository name |
| `GITHUB_TOKEN` | **Yes** | - | GitHub PAT (from OpenBao) |
| `DECLARATIVE_CONFIG_PATH` | No | `k8s/rs-manager/seam/routes.d` | Fragment path in repo |
| `EVALUATION_INTERVAL_HOURS` | No | `1` | Evaluation loop interval |

### OpenBao Setup

The evaluator requires a GitHub PAT stored at:

```
secret/evaluators/seam-retirement-evaluator/github-token
```

Token requirements:
- **Scope**: `repo` (full repository access)
- **Permissions**: Pull requests, Contents, Metadata
- **Source**: Created manually at https://github.com/settings/tokens

**Never** read this token through SEAM's paths - isolation is enforced by OpenBao policy.

## Deployment

The evaluator deploys to `k8s/rs-manager/seam-retirement-evaluator/` (deliberately OUTSIDE `seam/`):

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: seam-retirement-evaluator
  namespace: seam
spec:
  replicas: 1
  template:
    spec:
      serviceAccountName: seam-retirement-evaluator
      containers:
      - name: evaluator
        image: ronaldraygun/seam-retirement-evaluator:0.1.0
        env:
        - name: GITHUB_TOKEN
          valueFrom:
            secretKeyRef:
              name: seam-retirement-evaluator-github-token
              key: token
```

### Internal Scheduling Loop

Per fleet rules, **no CronJobs** - the evaluator uses an internal scheduling loop:

```go
ticker := time.NewTicker(1 * time.Hour)
for range ticker.C {
    evaluator.RunEvaluation(ctx)
}
```

## Safety Guarantees

### 1. Zero Traffic Requirement

Routes with ANY observed requests are NEVER retired - this is a necessary condition, not a heuristic.

### 2. Human Gate

All PRs require human approval. The mechanical checks validate:
- Fragment path ownership (`routes/<service>/-only`)
- GitHub token validity

The human merge gate bounds blast radius.

### 3. Rollback Safety

If a PR is merged in error:
1. Caller-appears events immediately invalidate the verdict
2. Follow-up PR removing the block is mechanical
3. Hot-reload path means no deployment rollback

### 4. No Autonomous Sunset

The evaluator only OPENS PRs - it never merges, never pushes directly, never bypasses review.

## Integration with SEAM Phases

- **Phase 8.1**: Version selection (oldest default) → creates retired versions
- **Phase 8.2**: x-adapter (version migration) → provides replacement paths
- **Phase 8.3**: Deprecation/brownout scheduler → enforces the verdict
- **Phase 8.4**: Per-route-version metrics → fuels the evaluation
- **Phase 8.5** (this): Retirement evaluator → generates the verdict

## Development

### Building

```bash
cd tools/seam-retirement-evaluator
go build -o seam-retirement-evaluator .
```

### Testing

```bash
go test -v ./...
```

### Local Development

Set environment variables for local testing:

```bash
export VICTORIAMETRICS_ENDPOINT="http://localhost:8428"
export GITHUB_TOKEN="ghp_..."
export GITHUB_OWNER="jedarden"
export GITHUB_REPO="declarative-config"
./seam-retirement-evaluator
```

## Troubleshooting

### "GitHub token is required but was not provided"

The OpenBao secret is not populated or the Kubernetes Secret is not synced. Run:

```bash
# Check OpenBao
bao kv get secret/evaluators/seam-retirement-evaluator/github-token

# Check Kubernetes
kubectl get secret seam-retirement-evaluator-github-token -n seam
```

### "Failed to query route traffic"

VictoriaMetrics endpoint is unreachable. Check:
- Endpoint URL is correct
- Network policies allow traffic
- VictoriaMetrics is healthy

### "Failed to open PR"

GitHub API issues:
- Token has `repo` scope
- Token is not expired
- Repository exists and is accessible

## Future Enhancements

- **Caller-appears detection**: Automatic PR reversal when traffic resumes
- **Metrics refinement**: Better gap detection algorithms
- **Configurable windows**: Per-service custom evaluation windows
- **Dry-run mode**: Evaluation-only without PR creation
- **Fragment write**: The PR flow stops short of editing the fragment. Route
  fragments in declarative-config are JSON entries inside one whole ConfigMap
  per route owner (`k8s/rs-manager/seam/configmap-routes-*.yaml`), not files
  under `DECLARATIVE_CONFIG_PATH`, and the fragment schema
  (`configmap-fragment-schema.json`) does not know `x-seam-deprecated` — so
  there is nothing to write yet. `GitHubClient.OpenPR` refuses to open a pull
  request with no file change (GitHub rejects a zero-diff PR), so candidates
  currently log an error instead of opening an unusable PR.
