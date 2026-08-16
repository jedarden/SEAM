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

## Memory and Concurrency Benchmarks

SEAM includes comprehensive benchmarks for measuring memory footprint, throughput under concurrent load, and GC impact.

### Running Memory and Concurrency Benchmarks

Run all memory and concurrency benchmarks:
```bash
go test -bench=Memory -benchmem -benchtime=10s ./benches/...
go test -bench=Concurrent -benchmem -benchtime=10s ./benches/...
go test -bench=GC -benchmem -benchtime=10s ./benches/...
go test -bench=Scaling -benchmem -benchtime=10s ./benches/...
```

Run specific benchmark scenarios:
```bash
# Memory per connection
go test -bench=BenchmarkMemoryPerConnection -benchmem ./benches/...

# Concurrent throughput
go test -bench=BenchmarkConcurrentThroughput -benchmem ./benches/...

# Memory growth over time
go test -bench=BenchmarkMemoryGrowth -benchmem ./benches/...

# GC impact under load
go test -bench=BenchmarkGCImpact -benchmem ./benches/...

# Connection scaling behavior
go test -bench=BenchmarkConnectionScaling -benchmem ./benches/...

# Memory at rest (idle connections)
go test -bench=BenchmarkMemoryAtRest -benchmem ./benches/...
```

### Benchmark Scenarios

#### 1. Memory Per Connection (`BenchmarkMemoryPerConnection`)
Measures memory footprint per connection at rest with different connection counts:
- **Connection levels**: 1, 10, 50, 100, 500, 1000 concurrent connections
- **Metrics reported**: Bytes per connection, heap size in KB, active goroutines
- **Purpose**: Establish baseline memory footprint for active connections

#### 2. Concurrent Throughput (`BenchmarkConcurrentThroughput`)
Tests throughput under concurrent load with detailed metrics:
- **Connection levels**: 1, 10, 50, 100, 500, 1000 concurrent connections
- **Per connection**: 10 sequential requests
- **Metrics reported**:
  - Requests per second (req/s)
  - Memory per connection (bytes/conn)
  - Latency percentiles: p50, p95, p99 (ms)
  - Heap size in KB
  - GC cycles during test
- **Purpose**: Measure how performance degrades under increasing concurrency

#### 3. Memory Growth (`BenchmarkMemoryGrowth`)
Measures memory growth patterns over sustained load:
- **Connection levels**: 10, 50, 100 concurrent connections
- **Duration**: 10 seconds, sampling every second
- **Metrics reported**:
  - Growth percentage (should be sub-linear, ideally < 10%)
  - Final heap size in KB
  - Growth classification: sub-linear (1), linear (0), super-linear (-1)
- **Purpose**: Detect memory leaks and verify GC effectiveness under sustained load

#### 4. GC Impact (`BenchmarkGCImpact`)
Measures garbage collector performance under concurrent load:
- **Connection levels**: 10, 50, 100, 500, 1000 concurrent connections
- **Per connection**: 100 sequential requests (ensures GC cycles)
- **Metrics reported**:
  - GC cycles during test
  - Average GC pause time (ms)
  - CPU time spent in GC (percentage)
  - Heap size in KB
  - Next GC threshold (KB)
- **Purpose**: Understand GC overhead and tuning opportunities

#### 5. Connection Scaling (`BenchmarkConnectionScaling`)
Tests performance scaling across different connection ranges:
- **Low scale**: 1, 5, 10 connections
- **Medium scale**: 25, 50, 100 connections
- **High scale**: 250, 500, 1000 connections
- **Per connection**: 5 sequential requests
- **Metrics reported**:
  - Requests per second (req/s)
  - Success rate (percentage)
- **Purpose**: Verify performance scales reasonably with connection count

#### 6. Memory At Rest (`BenchmarkMemoryAtRest`)
Measures memory footprint with idle connections:
- **Connection counts**: 0, 1, 10, 50, 100, 500, 1000 idle connections
- **Process**: Establish connections, allow 100ms for idle state
- **Metrics reported**:
  - Bytes per idle connection
  - Total system memory in KB
  - Heap size in KB
  - Active goroutines
- **Purpose**: Measure baseline memory overhead of idle connection pools

### Memory Metrics Explained

The benchmarks capture Go runtime memory statistics:

- **Alloc**: Current heap allocation (bytes)
- **TotalAlloc**: Cumulative heap allocation (bytes)
- **Sys**: Total memory obtained from OS (bytes)
- **HeapAlloc**: Current heap allocation (bytes)
- **HeapSys**: Heap memory obtained from OS (bytes)
- **HeapInuse**: Heap in-use bytes (busy)
- **HeapIdle**: Heap idle bytes (waiting for use)
- **HeapObjects**: Number of allocated objects
- **StackInuse**: Stack memory in-use (bytes)
- **Goroutines**: Number of active goroutines
- **NumGC**: Number of garbage collection cycles
- **PauseTotalNs**: Total GC pause time (nanoseconds)
- **GCCPUFraction**: Fraction of CPU time used by GC

### Interpreting Memory and Concurrency Results

#### Memory Per Connection
- **Expected**: Linear or sub-linear growth with connection count
- **Ideal**: < 10KB per active connection
- **Warning**: Super-linear growth indicates connection pool issues

#### Throughput Scaling
- **Expected**: Throughput increases with concurrency up to a point
- **Ideal**: Near-linear scaling up to 100-500 connections
- **Warning**: Performance degradation above 100 concurrent connections suggests bottlenecks

#### Memory Growth Over Time
- **Expected**: Sub-linear growth (< 10% over 10 seconds)
- **Ideal**: Flat or decreasing memory (effective GC)
- **Warning**: Growth > 50% suggests memory leaks

#### GC Impact
- **Expected**: GC pauses increase with load but remain acceptable
- **Ideal**: < 1ms average GC pause under 100 concurrent connections
- **Warning**: > 10ms GC pauses or > 30% CPU in GC indicates GC pressure

#### Latency Percentiles
- **P50 (median)**: Typical user experience
- **P95**: 95th percentile - SLA target
- **P99**: 99th percentile - worst-case experience
- **Ideal**: P99 < 2× P50 (consistent performance)
- **Warning**: P99 > 10× P50 indicates tail latency problems

### Example Output

```
BenchmarkMemoryPerConnection/Connections-1-8          1000    5000000 ns/op    8192 B/op     8 allocs/op
    bytes/conn: 8192    KB/heap: 1024    goroutines: 12
BenchmarkMemoryPerConnection/Connections-1000-8           10   500000000 ns/op  8192000 B/op  8000 allocs/op
    bytes/conn: 8192    KB/heap: 10240    goroutines: 2012

BenchmarkConcurrentThroughput/Concurrent-10-8         1000     2000000 ns/op    4096 B/op    10 allocs/op
    req/s: 5000    bytes/conn: 4096    ms/p50: 2.5    ms/p95: 5.0    ms/p99: 10.0
    KB/heap: 2048    gc_cycles: 2

BenchmarkMemoryGrowth/Concurrent-50-8                 100    100000000 ns/op   20480 B/op   100 allocs/op
    growth_percent: 5.2    KB/final_heap: 5120    sub_linear: 1

BenchmarkGCImpact/Concurrent-100-8                    100     50000000 ns/op   10240 B/op    50 allocs/op
    gc_cycles: 15    ms/gc_avg: 0.8    cpu_gc_percent: 12.5    KB/heap: 4096    KB/next_gc: 8192

BenchmarkConnectionScaling/Low/Connections-10-8       1000     1500000 ns/op    2048 B/op     5 allocs/op
    req/s: 6666    success_rate: 100

BenchmarkMemoryAtRest/IdleConnections-100-8           100     10000000 ns/op    3072 B/op    20 allocs/op
    bytes/idle_conn: 2048    KB/sys: 4096    KB/heap: 1024    goroutines: 112
```

### Continuous Monitoring

Memory and concurrency benchmark results should be tracked over time to detect:
- Memory leaks (growth_percent increasing across runs)
- Performance regression (req/s decreasing)
- GC degradation (ms/gc_avg increasing)
- Scaling issues (performance degradation at lower concurrency levels)
