# Benchmark Infrastructure

**Created:** 2026-08-16
**Purpose:** Document SEAM's benchmark framework and execution patterns

## Overview

SEAM uses Go's built-in benchmark framework (`testing.B`) for performance measurement. Benchmarks are organized in the `benches/` directory and integrated into both local development and CI/CD workflows.

## Architecture

### Directory Structure

```
SEAM/
├── benches/
│   ├── README.md                    # Benchmark usage documentation
│   ├── bench_test.go                # Proxy forwarding and allocation benchmarks
│   ├── openbao_latency_benchmark_test.go
│   └── memory_concurrency_benchmark_test.go
├── benchmarks/
│   ├── baseline/                    # Baseline capture and comparison library
│   └── baselines/                   # Committed category baselines
├── cmd/baseline/                    # Baseline CLI used by Makefile and CI
├── benchmark-results/               # Historical benchmark data
│   └── run-<timestamp>/            # Timestamped result sets
└── Makefile                         # Benchmark targets (benchmark, benchmark-ci, etc.)
```

### Benchmark Types

**Core benchmarks** (current):
- `BenchmarkProxyForwarding`: Request construction, upstream forwarding, and response copy
- `BenchmarkWithAllocation`: Memory allocation measurement
- `BenchmarkParallel`: Concurrent execution patterns
- `BenchmarkOpenBao*`: OpenBao retrieval, cache, and proxy integration latency
- `BenchmarkMemory*`, `BenchmarkConcurrent*`, and `BenchmarkConnectionScaling`: Memory and load behavior

## CI/CD Integration

### Workflow: seam-benchmark

**Location:** `declarative-config/k8s/iad-ci/argo-workflows/seam-benchmark.yaml`

**Execution:**
```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  create -f declarative-config/k8s/iad-ci/argo-workflows/seam-benchmark.yaml
```

**Behavior:**
- Clones SEAM from Forgejo
- Runs `go test -bench=. -benchmem -benchtime=5s -json ./benches/...`
- Stores results in `benchmark-results/run-<timestamp>/`
- Retries on failure (max 2 attempts, exponential backoff)

## Local Development

### Quick Benchmark

```bash
make benchmark
```

### Profiled Runs

**CPU profiling:**
```bash
make benchmark-cpu
go tool pprof benchmark-results/cpu.prof
# Interactive: (pprof) top10
```

**Memory profiling:**
```bash
make benchmark-mem
go tool pprof benchmark-results/mem.prof
# Interactive: (pprof) top10
```

### Comparison Runs

Compare optimization impact using `benchstat`:
```bash
go install golang.org/x/perf/cmd/benchstat@latest
# Before change
make benchmark > before.txt
# After change
make benchmark > after.txt
benchstat before.txt after.txt
```

## Benchmark Writing Guide

### Basic Template

```go
func BenchmarkFeature(b *testing.B) {
    // Setup (NOT timed)
    data := prepareTestData()
    
    b.ResetTimer()  // Start timing here
    
    for i := 0; i < b.N; i++ {
        // Code to benchmark
        processFeature(data)
    }
}
```

### Parallel Execution

```go
func BenchmarkConcurrent(b *testing.B) {
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            // Concurrent-safe benchmark
            doWork()
        }
    })
}
```

### Sub-benchmarks

```go
func BenchmarkVariants(b *testing.B) {
    b.Run("small", func(b *testing.B) {
        for i := 0; i < b.N; i++ { processSmall() }
    })
    b.Run("large", func(b *testing.B) {
        for i := 0; i < b.N; i++ { processLarge() }
    })
}
```

## Results Interpretation

### Output Format

```
BenchmarkOpenBaoRead-8    5000    345678 ns/op    2048 B/op    12 allocs/op
```

**Components:**
- `BenchmarkOpenBaoRead-8`: Benchmark name and GOMAXPROCS (8)
- `5000`: Iterations completed
- `345678 ns/op`: Average nanoseconds per iteration (345.7 μs)
- `2048 B/op`: Average heap bytes allocated per iteration
- `12 allocs/op`: Average memory allocations per iteration

### Anti-patterns

**Don't:**
- Include setup in timed section (use `b.ResetTimer()`)
- Benchmark code with side effects that accumulate
- Forget to verify benchmarks actually run (check output)
- Benchmark while profiling (use separate runs)

**Do:**
- Keep benchmark loops minimal and focused
- Use sub-benchmarks for variants
- Profile separately (`-cpuprofile`, `-memprofile`)
- Document what's being measured and why

## Baseline Management

### Overview

SEAM includes automated baseline capture and regression detection infrastructure. Baselines are committed to `benchmarks/baselines/` and used to detect performance regressions in CI.

### Directory Structure

```
benchmarks/
├── baselines/
│   ├── .gitkeep                      # Directory marker
│   ├── openbao-latency-baseline.json # OpenBao latency baselines
│   ├── memory-concurrency-baseline.json # Memory footprint baselines
│   └── throughput-baseline.json      # Throughput baselines
└── baseline/
    └── baseline.go                   # Baseline management package
```

### Baseline File Format

Each baseline file contains:

```json
{
  "timestamp": "2026-08-16T12:34:56Z",
  "commit_sha": "abc123...",
  "commit_message": "feat: add caching",
  "version": "v1",
  "metrics": {
    "BenchmarkOpenBaoColdCache": {
      "name": "BenchmarkOpenBaoColdCache",
      "ns_per_op": 1234567.89,
      "bytes_per_op": 2048,
      "allocs_per_op": 12,
      "custom_metrics": {
        "ns/request": 1234567.89
      }
    }
  }
}
```

### Workflow

#### 1. Capture Initial Baseline

Run benchmarks and save the results as the baseline:

```bash
# Save OpenBao latency baseline
make benchmark-save-baseline TYPE=openbao

# Save memory/concurrency baseline
make benchmark-save-baseline TYPE=memory

# Save throughput baseline
make benchmark-save-baseline TYPE=throughput
```

This creates a baseline file in `benchmarks/baselines/` with the current commit SHA and timestamp.

#### 2. Check for Regressions

Compare current benchmark run against the baseline:

```bash
# Check OpenBao benchmarks (default 10% threshold)
make benchmark-check-regression TYPE=openbao

# Check with custom threshold
make benchmark-check-regression TYPE=memory THRESHOLD=15

# Check all benchmark types
make benchmark-ci-regression
```

Exit codes:
- `0`: No regressions detected (all metrics within threshold)
- `1`: Regressions detected (one or more metrics exceeded threshold)

#### 3. Update Baseline (After Intentional Changes)

When you intentionally change performance characteristics (e.g., optimization, feature addition):

```bash
# Update baseline with current results
make benchmark-update-baseline TYPE=openbao
```

This prompts for confirmation, then saves the new baseline. Review and stage
the resulting JSON file explicitly before committing it.

### CI Integration

The `seam-benchmark` WorkflowTemplate includes a `benchmark-regression-check` step:

```yaml
- name: benchmark-regression-check
  # Runs benchmarks and compares against baselines
  # Fails the workflow if regressions exceed threshold
```

**Trigger regression check:**
```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: seam-benchmark-regression-
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: seam-benchmark
  workflowSpec:
    entrypoint: benchmark-regression-check
EOF
```

### Regression Detection Logic

The baseline tool compares three primary metrics:

1. **Latency** (`ns_per_op`, custom latency metrics)
   - Lower is better
   - Regression: increase > threshold

2. **Memory** (`bytes_per_op`)
   - Lower is better
   - Regression: increase > threshold

3. **Allocations** (`allocs_per_op`)
   - Lower is better
   - Regression: increase > threshold

**Threshold behavior:**
- Default: 10%
- Warnings flagged at 5% change
- Customizable per-check via `THRESHOLD` parameter

### Report Format

Regression checks produce formatted output:

```
=== Benchmark Regression Report ===
Baseline Commit: abc12345 (2026-08-15T10:00:00Z)
Current Commit: def67890 (2026-08-16T12:00:00Z)
Regression Threshold: 10.0%

❌ REGRESSIONS DETECTED:
  ⬆ BenchmarkOpenBaoColdCache.ns_per_op: 15.23% (1000000.00 → 1152300.00)
  ⬆ BenchmarkOpenBaoWarmCache.allocs/op: 12.50% (8.00 → 9.00)

⚠️  WARNINGS (significant change below threshold):
  ⬆ BenchmarkMemoryConcurrent.bytes_per_op: 7.50% (4096.00 → 4406.00)

✅ IMPROVEMENTS:
  ⬇ BenchmarkProxyThroughput.ns/op: -8.30% (5000.00 → 4585.00)

❌ FAILED: 2 regression(s) detected
```

### Best Practices

**When to update baselines:**
- After intentional optimizations (improvements)
- After architectural changes that affect performance characteristics
- When adding new benchmark scenarios
- When regression noise exceeds threshold (e.g., due to CI environment changes)

**When NOT to update baselines:**
- To hide real regressions
- Without understanding the root cause of the change
- Without testing on a stable environment

**Committing baselines:**
Baselines are committed to git like any other code change:

```bash
# After updating baseline
git add benchmarks/baselines/
git commit -m "perf: update baseline for OpenBao latency optimization"
git push
```

### Troubleshooting

**No baseline file exists:**
```
Error: failed to read baseline file benchmarks/baselines/openbao-latency-baseline.json
```
**Solution:** Run `make benchmark-save-baseline TYPE=<type>` first.

**Regression detected but change is expected:**
1. Investigate the change (profiling, code review)
2. If intentional, run `make benchmark-update-baseline TYPE=<type>`
3. Commit the new baseline with explanation

**CI environment noise causes false regressions:**
1. Increase threshold for affected metrics
2. Run multiple iterations and use median values
3. Isolate noisy benchmarks to separate CI jobs

## Future Enhancements

**Planned:**
- Trend analysis dashboard (benchmark history comparison)
- Proxy throughput benchmarks under load
- Automated baseline suggestions based on historical variance

**Considered:**
- Integration with pprof for automated profiling
- Custom result formatting for historical tracking
- Benchmark result archival in external store

## References

- [Go Testing - Benchmarks](https://golang.org/pkg/testing/#hdr-Benchmarks)
- [Go Blog - Profiling Go Programs](https://go.dev/blog/pprof)
- `benches/README.md` - Usage documentation
- `benchmarks/baseline/baseline.go` - Baseline management code
- `declarative-config/k8s/iad-ci/argo-workflows/seam-benchmark.yaml` - CI workflow
