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
│   └── bench_test.go                # Benchmark implementations
├── benchmark-results/               # Historical benchmark data
│   └── run-<timestamp>/            # Timestamped result sets
└── Makefile                         # Benchmark targets (benchmark, benchmark-ci, etc.)
```

### Benchmark Types

**Placeholder Benchmarks** (current):
- `BenchmarkExample`: Basic structure demonstration
- `BenchmarkWithAllocation`: Memory allocation measurement
- `BenchmarkParallel`: Concurrent execution patterns

**Future Benchmarks** (planned):
- OpenBao latency (seam-9554a4de child bead)
- Request proxy throughput
- Spec validation performance
- Capture subsystem overhead

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
go tool pprof cpu.prof
# Interactive: (pprof) top10
```

**Memory profiling:**
```bash
make benchmark-mem
go tool pprof mem.prof
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

## Future Enhancements

**Planned:**
- Trend analysis dashboard (benchmark history comparison)
- Regression detection (CI fails on significant slowdowns)
- OpenBao latency benchmarks (child bead of seam-9554a4de)
- Proxy throughput benchmarks under load

**Considered:**
- Integration with pprof for automated profiling
- Custom result formatting for historical tracking
- Benchmark result archival in external store

## References

- [Go Testing - Benchmarks](https://golang.org/pkg/testing/#hdr-Benchmarks)
- [Go Blog - Profiling Go Programs](https://go.dev/blog/pprof)
- `benches/README.md` - Usage documentation
- `declarative-config/k8s/iad-ci/argo-workflows/seam-benchmark.yaml` - CI workflow
