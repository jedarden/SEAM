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

## OpenBao Latency Benchmarks

SEAM includes comprehensive benchmarks for measuring OpenBao secret retrieval latency and proxy overhead.

### Running OpenBao Benchmarks

Run all OpenBao latency benchmarks:
```bash
go test -bench=OpenBao -benchmem -benchtime=10s ./benches/...
```

Run specific benchmark scenarios:
```bash
# Cold cache performance
go test -bench=BenchmarkOpenBaoColdCache -benchmem ./benches/...

# Warm cache performance
go test -bench=BenchmarkOpenBaoWarmCache -benchmem ./benches/...

# Full proxy integration
go test -bench=BenchmarkOpenBaoProxyIntegration -benchmem ./benches/...

# Concurrent access patterns
go test -bench=BenchmarkOpenBaoConcurrentAccess -benchmem ./benches/...

# Variable credential sizes
go test -bench=BenchmarkOpenBaoVariableSizedSecrets -benchmem ./benches/...
```

### Benchmark Scenarios

#### 1. Cold Cache (`BenchmarkOpenBaoColdCache`)
Measures secret retrieval latency with an empty cache, testing different credential sizes:
- **Small**: 100 bytes
- **Medium**: 1KB
- **Large**: 10KB

This measures the worst-case scenario when cache is cold and secrets must be fetched from OpenBao.

#### 2. Warm Cache (`BenchmarkOpenBaoWarmCache`)
Measures secret retrieval latency with a populated cache. After an initial read warms the cache, subsequent reads measure cached access performance.

#### 3. Cache Miss (`BenchmarkOpenBaoCacheMiss`)
Measures latency when requesting non-existent secrets, simulating error handling overhead.

#### 4. Direct Connection Baseline (`BenchmarkDirectConnectionBaseline`)
Measures proxy latency **without** OpenBao integration, providing a baseline for comparison.

#### 5. OpenBao Proxy Integration (`BenchmarkOpenBaoProxyIntegration`)
Measures end-to-end latency including:
- OpenBao secret retrieval
- Token injection
- Proxy forwarding to upstream

This is the full production flow benchmark.

#### 6. Concurrent Access (`BenchmarkOpenBaoConcurrentAccess`)
Tests performance under concurrent load with varying concurrency levels (1, 10, 50, 100 parallel goroutines).

#### 7. Variable Sized Secrets (`BenchmarkOpenBaoVariableSizedSecrets`)
Measures latency across different credential sizes:
- 64 bytes (very small)
- 256 bytes (small)
- 1KB (medium)
- 4KB (large)
- 16KB (very large)
- 64KB (extremely large)

### Prerequisites

OpenBao binary must be installed and available in PATH. The benchmark will start a local OpenBao dev server automatically.

Install OpenBao:
```bash
# On Linux/macOS
curl -fsSL https://openbao.org/docs/getting_started/install.md | sh

# Verify installation
openbao version
```

### JSON Output

To save benchmark results in JSON format:
```bash
BENCHMARK_OUTPUT=benchmark-results/openbao-latency.json go test -bench=OpenBao -benchmem -benchtime=10s ./benches/...
```

The JSON output includes:
- `name`: Benchmark name
- `iterations`: Number of iterations executed
- `ns_per_op`: Nanoseconds per operation
- `bytes_per_op`: Memory allocated per operation
- `allocs_per_op`: Number of allocations per operation
- `cache_hit_rate`: Cache hit percentage (if applicable)
- `credential_size`: Size of credential in bytes (if applicable)
- `latency`: Measured latency duration

### Example Output

```
BenchmarkOpenBaoColdCache/Size-small-8           10000    1250000 ns/op    2048 B/op    15 allocs/op
BenchmarkOpenBaoColdCache/Size-medium-8           5000    2500000 ns/op    4096 B/op    20 allocs/op
BenchmarkOpenBaoColdCache/Size-large-8            1000   12500000 ns/op   20480 B/op    50 allocs/op
BenchmarkOpenBaoWarmCache/Size-small-8           50000     250000 ns/op    1024 B/op     8 allocs/op
BenchmarkOpenBaoProxyIntegration-8                2000    3000000 ns/op    3072 B/op    25 allocs/op
```

### Interpreting Results

- **Cold vs Warm**: Warm cache should be significantly faster (10-100x) than cold cache
- **Size Impact**: Larger credentials increase latency and memory usage
- **Baseline Comparison**: Subtract direct connection baseline from OpenBao proxy integration to isolate OpenBao overhead
- **Concurrency**: Monitor performance degradation under high concurrency
- **Cache Hit Rate**: Higher hit rates indicate better cache utilization

### Continuous Monitoring

Benchmark results are stored in `/benchmark-results/` for historical comparison and trend analysis.
