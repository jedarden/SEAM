# SEAM Benchmarks

This directory contains performance benchmarks for SEAM.

## Running Benchmarks

### Local Development

Run all benchmarks with default settings:
```bash
make benchmark
# or
go test -bench=. -benchmem ./benches/...
```

Run with CPU profiling:
```bash
make benchmark-cpu
# or
go test -bench=. -benchmem -cpuprofile=cpu.prof ./benches/...
```

Run with memory profiling:
```bash
make benchmark-mem
# or
go test -bench=. -benchmem -memprofile=mem.prof ./benches/...
```

### CI/CD Pipeline

In CI, benchmarks run with JSON output for automated collection:
```bash
make benchmark-ci
```

## Benchmark Naming Convention

All benchmark functions should follow the pattern:
```go
func Benchmark<Feature>(b *testing.B) {
    // Setup (not timed)
    b.ResetTimer()
    
    // Benchmark loop
    for i := 0; i < b.N; i++ {
        // Code to benchmark
    }
}
```

## Analyzing Results

Results are printed to stdout in the following format:
```
BenchmarkExample-8    1000000    1234 ns/op    512 B/op    8 allocs/op
```

- `1000000`: Number of iterations
- `1234 ns/op`: Nanoseconds per operation
- `512 B/op`: Bytes allocated per operation
- `8 allocs/op`: Number of allocations per operation

## Adding New Benchmarks

1. Create a new file in this directory following the pattern `<name>_test.go`
2. Add benchmark functions with the `Benchmark` prefix
3. Use `b.ResetTimer()` before the actual benchmark loop if you have setup code
4. Use `b.RunParallel()` for concurrent benchmarks

## Profile Analysis

To analyze CPU profiles:
```bash
go tool pprof cpu.prof
```

To analyze memory profiles:
```bash
go tool pprof mem.prof
```

## Continuous Monitoring

Benchmark results are stored in `/benchmark-results/` for historical comparison and trend analysis.
