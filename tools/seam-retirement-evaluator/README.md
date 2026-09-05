# SEAM Retirement Evaluator

**Phase 8.5**: Detection-only retirement evaluation. The evaluator finds route
versions that have gone quiet and reports them; it never writes to a git host.

## Overview

The `seam-retirement-evaluator` is a deployable component that evaluates route
versions for retirement eligibility based on observed traffic patterns. It
operates as a separate deployment from SEAM, with its own ServiceAccount and
OpenBao role, ensuring complete isolation from SEAM's credential paths.

**The evaluator is detection-only.** Its entire output is one structured log
record and one Prometheus counter per deprecation candidate. The
`x-seam-deprecated` block it proposes is landed by a human as an ordinary
commit to `main` in `declarative-config`, and reverted the same way if a caller
appears. There is no PR, no branch, no token, and no write path of any kind.

## Architecture

```
seam-retirement-evaluator (Deployment)
├── ServiceAccount: seam-retirement-evaluator
├── OpenBao Role: seam-retirement-evaluator-policy
│   ├── Reads: secret/evaluators/seam-retirement-evaluator/*
│   ├── Reads: secret/monitoring/victoriametrics/*
│   └── Explicitly DENIED: secret/seam/routes/* (isolation from SEAM)
└── VictoriaMetrics endpoint (read-only query, no credential)
```

### Key Isolation Guarantees

- **Dedicated OpenBao Path**: `secret/evaluators/seam-retirement-evaluator/*` is NOT readable by SEAM
- **Explicit Deny Policy**: SEAM policy explicitly denies access to evaluator paths
- **Separate SA**: Independent ServiceAccount with minimal RBAC
- **No Cross-Access**: Evaluator cannot read SEAM route secrets; SEAM cannot read evaluator credentials
- **No write path**: the evaluator holds no git-host credential and cannot reach one

## Functionality

### 1. Traffic Analysis

The evaluator queries VictoriaMetrics for per-route-version traffic metrics:

```promql
# Per-route-version request counter (Phase 8.4 metric)
max_over_time(seam_route_version_requests_total[14d])
```

For each `(route, x-api-version)` combination, it reads the observed request
count and derives:

- **Observed max inter-request gap**: Maximum time between requests
- **Quiet-since**: Timestamp of last observed request
- **Total history**: Duration of available metrics

An unreadable sample is treated as *traffic*, not as quietness — a count the
evaluator cannot read is never allowed to make a route eligible.

### 2. Retirement Eligibility

A route version is eligible for retirement when:

1. **Zero observed traffic** (NECESSARY condition)
2. Quiet period exceeds evaluation window:
   ```
   evaluation_window = max(3 x observed_max_gap, 7 days)
   ```
3. Sufficient history exists (≥14 days), otherwise 7-day floor applies

### 3. Detection Emission

For each eligible route version the evaluator emits, as a structured zap
record and a Prometheus counter:

- route, `x-api-version`, spec version
- quiet-since timestamp and evaluation window
- the reason the route qualified
- the proposed sunset date (90 days out)
- the computed brownout windows
- the path of the route fragment the proposal applies to
- the `x-seam-deprecated` block itself, fragment-shaped and ready to paste
- the human-readable proposal text

```
seam_retirement_deprecation_candidates_total{route=...,api_version=...,spec_version=...}
seam_retirement_evaluation_runs_total{result="success"|"error"}
seam_retirement_routes_evaluated
```

`seam_retirement_deprecation_candidates_total` accumulates per route version,
so a route that stays quiet keeps counting up across evaluation runs.

### 4. The Verdict Channel

The deprecation verdict travels through SEAM's existing hot-reload path:

1. A human adds the `x-seam-deprecated` block to the route fragment and commits
   to `main`; ArgoCD syncs
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
| `DECLARATIVE_CONFIG_PATH` | No | `k8s/rs-manager/seam/routes.d` | Fragment path prefix reported in findings |
| `LISTEN_ADDRESS` | No | `:8080` | Health/ready/metrics listen address. This server carries the evaluator's only output, so a bind failure exits rather than losing the metric silently |

There is no credential. The evaluator is configurable from an empty
environment; the defaults above are the whole configuration. The evaluation
interval is a compile-time constant (`EvaluationInterval`, 1 hour).

## Deployment

The evaluator deploys to `k8s/rs-manager/seam-retirement-evaluator/` (deliberately OUTSIDE `seam/`).
It needs only network reach to VictoriaMetrics and nothing mounted.

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

### 2. Reversibility Is The Gate

There is no review gate and none is needed. Landing a proposal is a one-line
block committed directly to `main`, reversible with a revert commit, and ArgoCD
re-syncs. The evaluator's own blast radius is zero: it cannot write anywhere.

### 3. Rollback Safety

If a deprecation lands in error:
1. Caller-appears events immediately invalidate the verdict
2. A revert commit removes the block
3. Hot-reload path means no deployment rollback

### 4. No Autonomous Sunset

The evaluator only reports. It never edits a repository, never opens a PR, and
never holds a credential that could.

## Integration with SEAM Phases

- **Phase 8.1**: Version selection (oldest default) → creates retired versions
- **Phase 8.2**: x-adapter (version migration) → provides replacement paths
- **Phase 8.3**: Deprecation/brownout scheduler → enforces the verdict
- **Phase 8.4**: Per-route-version metrics → fuels the evaluation
- **Phase 8.5** (this): Retirement evaluator → detects the verdict

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

```bash
export VICTORIAMETRICS_ENDPOINT="http://localhost:8428"
./seam-retirement-evaluator
```

## Troubleshooting

### "Failed to query route traffic"

VictoriaMetrics endpoint is unreachable. Check:
- Endpoint URL is correct
- Network policies allow traffic
- VictoriaMetrics is healthy

### No candidates, but a route is definitely quiet

Zero observed traffic is necessary but not sufficient. The route also needs a
quiet period longer than `evaluation_window` (`max(3 x observed_max_gap, 7
days)`), and an unreadable sample counts as traffic. Check
`seam_retirement_routes_evaluated` to confirm the route was considered at all,
and the run's log records for the eligibility reason per route version.

## Future Enhancements

- **Caller-appears detection**: Automatic flagging when traffic resumes on a
  deprecated route, so the revert is proposed rather than noticed
- **Metrics refinement**: Better gap detection algorithms (QuietSince and
  MaxGap currently need a range query this instant query does not perform)
- **Configurable windows**: Per-service custom evaluation windows
- **Fragment write**: The evaluator does not edit fragments. Route fragments in
  declarative-config are JSON entries inside one whole ConfigMap per route
  owner (`k8s/rs-manager/seam/configmap-routes-*.yaml`), not files under
  `DECLARATIVE_CONFIG_PATH`, and the fragment schema
  (`configmap-fragment-schema.json`) does not know `x-seam-deprecated`. The
  reported path is therefore a proposal locator, not a writable target — the
  edit is a human commit to `main`.
