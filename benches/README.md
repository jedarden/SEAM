# SEAM Benchmarks

Performance benchmarks for the SEAM proxy server.

## Overview

This directory contains comprehensive benchmarks measuring SEAM's performance across multiple dimensions:

- **OpenBao Latency**: Secret retrieval latency with OpenBao integration
- **Memory & Concurrency**: Memory footprint and concurrent request handling
- **Throughput**: Connection scaling and request throughput

## Running Benchmarks

### Quick Run

```bash
# Run all benchmarks with 10s duration
make benchmark

# Run with verbose output
go test -bench=. -benchmem -benchtime=10s -v ./benches/...
```

### Specific Benchmark Types

```bash
# OpenBao latency benchmarks
go test -bench=OpenBao -benchmem ./benches/...

# Memory and concurrency benchmarks
go test -bench='Memory|Concurrent' -benchmem ./benches/...

# Throughput benchmarks
go test -bench='Scaling|Throughput' -benchmem ./benches/...
```

### With Profiling

```bash
# CPU profiling
make benchmark-cpu
go tool pprof benchmark-results/cpu.prof

# Memory profiling
make benchmark-mem
go tool pprof benchmark-results/mem.prof
```

## Baseline Management

SEAM includes automated baseline capture and regression detection.

### Capture Baseline

```bash
# Save current performance as baseline
make benchmark-save-baseline TYPE=openbao
make benchmark-save-baseline TYPE=memory
make benchmark-save-baseline TYPE=throughput
```

This creates baseline files in `benchmarks/baselines/` with:
- Timestamp
- Commit SHA
- Commit message
- Benchmark metrics

### Check for Regressions

```bash
# Compare against baseline (10% threshold by default)
make benchmark-check-regression TYPE=openbao

# Custom threshold (e.g., 15%)
make benchmark-check-regression TYPE=memory THRESHOLD=15

# Check all benchmark types
make benchmark-ci-regression
```

Exit codes:
- `0`: No regressions
- `1`: Regressions detected

### Update Baseline

After intentional performance changes:

```bash
make benchmark-update-baseline TYPE=openbao
```

See [Baseline Management Workflow](#baseline-management-workflow) for details.

## Benchmark Files

- `openbao_latency_benchmark_test.go` - OpenBao secret retrieval latency
- `memory_concurrency_benchmark_test.go` - Memory and concurrency behavior
- `bench_test.go` - General benchmark utilities

## Interpreting Results

### Output Format

```
BenchmarkOpenBaoColdCache/Size-small-8    1000    1234567 ns/op    2048 B/op    12 allocs/op
```

**Components:**
- `BenchmarkOpenBaoColdCache/Size-small-8`: Benchmark name and GOMAXPROCS count
- `1000`: Iterations completed
- `1234567 ns/op`: Average nanoseconds per iteration (~1.2ms)
- `2048 B/op`: Average heap bytes allocated per iteration
- `12 allocs/op`: Average memory allocations per iteration

### Key Metrics

| Metric | What It Measures | Good Direction |
|--------|------------------|----------------|
| `ns/op` | Latency per operation | Lower |
| `bytes/op` | Memory allocated per operation | Lower |
| `allocs/op` | Number of allocations per operation | Lower |
| `ns/request` | Custom latency metric | Lower |

## CI Integration

Benchmarks run automatically in CI via the `seam-benchmark` WorkflowTemplate.

**Manual trigger:**
```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  create -f declarative-config/k8s/iad-ci/argo-workflows/seam-benchmark.yaml
```

**Regression check:**
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

## Baseline Management Workflow

### 1. Initial Setup

First time capturing baselines:

```bash
# Run benchmarks for each type and save as baseline
make benchmark-save-baseline TYPE=openbao
make benchmark-save-baseline TYPE=memory
make benchmark-save-baseline TYPE=throughput

# Commit baselines to git
git add benchmarks/baselines/
git commit -m "perf: establish performance baselines"
git push
```

### 2. Development Workflow

During development, check for regressions:

```bash
# After making changes, check for regressions
make benchmark-ci-regression

# If regressions are detected:
# 1. Investigate the cause (profiling, code review)
# 2. If unexpected, fix the regression
# 3. If intentional (optimization), update the baseline
```

### 3. Intentional Performance Changes

When you intentionally change performance:

```bash
# Example: After optimizing OpenBao caching
go test -bench=OpenBao -benchmem ./benches/... | \
  go run ./cmd/baseline -type=openbao -check-regression

# Verify the improvement is as expected
# Then update the baseline:
make benchmark-update-baseline TYPE=openbao

# Commit with explanation
git add benchmarks/baselines/openbao-latency-baseline.json
git commit -m "perf: update OpenBao baseline after cache optimization

Reduced cold cache latency by 30% through credential pre-fetching.
Baseline commit: abc1234 -> def5678"
git push
```

### 4. Handling False Positives

If CI reports regressions that are environmental noise:

```bash
# Option 1: Increase threshold for noisy benchmarks
make benchmark-check-regression TYPE=openbao THRESHOLD=15

# Option 2: Update baseline if environment changed permanently
make benchmark-update-baseline TYPE=openbao

# Option 3: Investigate and fix the noise source
make benchmark-cpu
make benchmark-mem
```

## Writing Benchmarks

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

### With Sub-benchmarks

```go
func BenchmarkVariants(b *testing.B) {
    sizes := []int{100, 1000, 10000}

    for _, size := range sizes {
        b.Run(fmt.Sprintf("Size-%d", size), func(b *testing.B) {
            data := generateData(size)
            b.ResetTimer()

            for i := 0; i < b.N; i++ {
                processData(data)
            }
        })
    }
}
```

### With Custom Metrics

```go
func BenchmarkWithCustomMetrics(b *testing.B) {
    b.ResetTimer()

    for i := 0; i < b.N; i++ {
        start := time.Now()
        result := doWork()
        elapsed := time.Since(start)

        // Report custom metric (captured by baseline tool)
        b.ReportMetric(float64(elapsed.Nanoseconds()), "ns/work")
    }
}
```

## Troubleshooting

### Benchmark Fails with "no OpenBao"

OpenBao benchmarks require the OpenBao binary. Install it:

```bash
# Check if OpenBao is available
openbao version

# If not available, benchmarks will skip automatically
```

### Baseline File Not Found

```
Error: failed to read baseline file benchmarks/baselines/openbao-latency-baseline.json
```

**Solution:** Create the baseline first:
```bash
make benchmark-save-baseline TYPE=openbao
```

### Regression Detected But Change Is Expected

1. Verify the change with profiling
2. If intentional, update the baseline:
   ```bash
   make benchmark-update-baseline TYPE=<type>
   ```
3. Commit with explanation of the performance change

### Results Are Noisy

- Increase `-benchtime` for more stable results
- Use `b.RunParallel()` for concurrent benchmarks
- Run multiple times and use `benchstat`

## Further Reading

- [Go Testing - Benchmarks](https://golang.org/pkg/testing/#hdr-Benchmarks)
- [Go Blog - Profiling Go Programs](https://go.dev/blog/pprof)
- [docs/notes/benchmark-infrastructure.md](../../docs/notes/benchmark-infrastructure.md) - Detailed infrastructure documentation
- [docs/notes/performance-benchmark-guide.md](../../docs/notes/performance-benchmark-guide.md) - Performance benchmarking guide
