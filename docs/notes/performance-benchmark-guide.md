# SEAM Performance Benchmark Suite

## Overview

This guide documents the performance benchmark suite for SEAM proxy overhead measurement. The benchmarks measure critical performance characteristics needed for capacity planning, regression detection, and performance optimization.

## Benchmark Categories

### 1. Baseline Performance
These benchmarks establish the fundamental performance floor of SEAM without external dependencies.

- **`BenchmarkBaselineProxyPerformance`** - Measures basic HTTP request handling overhead
- **Target:** < 100µs per simple health check request
- **Use Case:** Establish proxy baseline latency

### 2. OpenBao Integration Overhead
These benchmarks measure the cost of secret retrieval and injection.

- **`BenchmarkOpenBaoSecretRetrieval`** - Direct OpenBao read operation latency
- **`BenchmarkEndToEndOpenBaoFlow`** - Complete secret injection pipeline
- **Target:** < 5ms for cached secrets, < 50ms for uncached
- **Use Case:** Capacity planning for secret-intensive workloads

### 3. Caching Performance
Measures cache effectiveness and overhead.

- **`BenchmarkCacheHitPerformance`** - Cache read latency
- **`BenchmarkCacheMissPerformance`** - Cache miss overhead
- **`BenchmarkResponseCaching`** - End-to-end caching performance
- **Target:** Cache hits < 10µs, 100x faster than misses
- **Use Case:** Cache configuration tuning and TTL optimization

### 4. Concurrency and Scaling
Tests performance under concurrent load.

- **`BenchmarkConcurrentRequests`** - Performance at 1, 10, 50, 100 concurrent connections
- **`BenchmarkConcurrentSingleFlight`** - Single-flight coalescing effectiveness
- **Target:** Linear scaling up to 50 concurrent connections
- **Use Case:** Connection pool sizing and horizontal scaling planning

### 5. Memory Footprint
Measures memory usage per connection and overall memory efficiency.

- **`BenchmarkMemoryFootprintPerConnection`** - Memory per concurrent connection
- **Target:** < 100KB per active connection
- **Use Case:** Memory sizing for production deployments

### 6. Throughput and Saturation
Identifies maximum sustainable throughput and saturation points.

- **`BenchmarkThroughputSaturation`** - Requests per second at 100, 500, 1000, 5000 RPS targets
- **Target:** > 5000 RPS before degradation
- **Use Case:** Rate limiting configuration and autoscaling thresholds

### 7. Latency Distribution
Measures tail latency and establishes SLA baselines.

- **`BenchmarkRequestLatencyDistribution`** - p50, p95, p99 latency percentiles
- **Target:** p95 < 1ms for baseline operations, p99 < 10ms for OpenBao operations
- **Use Case:** SLA target setting and latency anomaly detection

### 8. Middleware Overhead
Measures individual middleware component overhead.

- **`BenchmarkQuotaTrackingOverhead`** - Rate limiting check overhead
- **`BenchmarkSingleFlightOverhead`** - Duplicate request coalescing cost
- **Target:** < 1µs per middleware component
- **Use Case:** Middleware optimization and feature toggling decisions

## Running Benchmarks

### Quick Start

Run the complete benchmark suite with default settings:

```bash
./scripts/run_benchmarks.sh
```

### Custom Configurations

Run specific benchmarks with custom parameters:

```bash
# Run only OpenBao and caching benchmarks
./scripts/run_benchmarks.sh --benchmarks "OpenBao,Cache"

# Run with longer duration for more accurate results
./scripts/run_benchmarks.sh --duration 5s --iterations 10

# Generate HTML report and open in browser
./scripts/run_benchmarks.sh --format html --open-report

# Compare against previous run
./scripts/run_benchmarks.sh --compare ./benchmark-results/latest
```

### Direct Go Benchmarks

For development and debugging, run benchmarks directly:

```bash
# Run all benchmarks
go test -bench=. -benchmem ./internal/server

# Run specific benchmark
go test -bench=BenchmarkOpenBaoSecretRetrieval -benchmem ./internal/server

# Run with verbose output
go test -bench=. -benchmem -v ./internal/server

# Custom benchmark duration
go test -bench=. -benchmem -benchtime=10s ./internal/server
```

## Interpreting Results

### Output Format

The benchmark runner generates three types of reports:

1. **Text Summary** (`benchmark-summary.txt`) - Human-readable overview
2. **JSON Results** (`benchmark-results.json`) - Machine-parseable detailed data
3. **HTML Report** (`benchmark-report.html`) - Visual dashboard (optional)

### Key Metrics

- **ns/op** - Nanoseconds per operation (lower is better)
- **bytes/op** - Memory allocated per operation (lower is better)
- **allocs/op** - Number of allocations per operation (lower is better)
- **req/sec** - Requests per second (higher is better)
- **µs_p50/p95/p99** - Latency percentiles (lower is better)
- **MB_total** - Total memory used
- **MB_per_connection** - Memory per concurrent connection

### Performance Targets

| Benchmark Category | Target | Critical Threshold |
|-------------------|--------|-------------------|
| Baseline Proxy | < 100µs | > 500µs |
| OpenBao Read (cached) | < 5ms | > 20ms |
| OpenBao Read (uncached) | < 50ms | > 200ms |
| Cache Hit | < 10µs | > 50µs |
| Memory per Connection | < 100KB | > 500KB |
| Throughput | > 5000 RPS | < 1000 RPS |
| p95 Latency | < 1ms | > 5ms |
| p99 Latency | < 10ms | > 50ms |

### Regression Detection

Compare results across builds to detect performance regressions:

```bash
# Run benchmarks on current code
./scripts/run_benchmarks.sh --output-dir ./results-current

# Compare against previous baseline
./scripts/run_benchmarks.sh --compare ./results-baseline/latest --output-dir ./results-comparison
```

**Regression Indicators:**
- ⚠️  **Significant change** (>10% degradation) - Investigate immediately
- ⚡ **Moderate change** (5-10% change) - Monitor trend
- ✓ **Stable** (<5% change) - Within acceptable variance

## Integration with CI/CD

### GitHub Actions (Disabled)

Per organizational policy, GitHub Actions are **disabled** across all repos. Do not create `.github/workflows/*` files.

### Argo Workflows (Recommended)

All CI/CD runs on Argo Workflows in the `iad-ci` cluster. Create a WorkflowTemplate in `declarative-config/k8s/iad-ci/argo-workflows/`:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: seam-benchmark
  namespace: argo-workflows
spec:
  entrypoint: benchmark
  templates:
    - name: benchmark
      container:
        image: golang:1.25.7
        command: [sh, -c]
        args: |
          git clone https://git.ardenone.com/jedarden/SEAM.git
          cd SEAM
          ./scripts/run_benchmarks.sh --format json --output-dir /tmp/results
        volumeMounts:
          - name: results
            mountPath: /tmp/results
      volumes:
        - name: results
          emptyDir: {}
```

Submit the workflow:

```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: seam-benchmark-
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: seam-benchmark
EOF
```

### Local Development

Run benchmarks before committing performance-sensitive changes:

```bash
# Quick smoke test
./scripts/run_benchmarks.sh --duration 100ms --iterations 3

# Full verification before major releases
./scripts/run_benchmarks.sh --duration 5s --iterations 10 --format html
```

## Profiling and Optimization

### CPU Profiling

Generate CPU profiles for detailed optimization:

```bash
go test -bench=BenchmarkOpenBaoSecretRetrieval -cpuprofile=cpu.prof ./internal/server
go tool pprof cpu.prof
```

### Memory Profiling

Generate memory profiles to identify allocations:

```bash
go test -bench=BenchmarkOpenBaoSecretRetrieval -memprofile=mem.prof ./internal/server
go tool pprof mem.prof
```

### Flame Graphs

Generate flame graphs for visual performance analysis:

```bash
go test -bench=. -cpuprofile=cpu.prof ./internal/server
go tool pprof -http=:8080 cpu.prof
# Open http://localhost:8080 in browser
```

## Capacity Planning

### Using Benchmark Data for Sizing

1. **Determine peak RPS** from traffic data
2. **Apply safety factor** (2-3x for headroom)
3. **Calculate required throughput** using `BenchmarkThroughputSaturation`
4. **Size memory** using `BenchmarkMemoryFootprintPerConnection`
5. **Validate latency targets** using `BenchmarkRequestLatencyDistribution`

**Example Calculation:**
- Peak traffic: 1000 RPS
- Safety factor: 3x
- Required throughput: 3000 RPS
- If benchmarks show 5000 RPS saturation → Single instance sufficient
- Memory at 3000 RPS: 3000 × 100KB = 300MB baseline

### Horizontal Scaling

Use concurrency benchmarks to determine when to scale horizontally:

```bash
# Check if single instance can handle projected load
./scripts/run_benchmarks.sh --benchmarks "Throughput,Concurrent"
```

**Scaling Indicators:**
- Throughput degradation > 20% at target concurrency
- Memory per connection > target threshold
- Latency percentiles exceed SLA targets

## Troubleshooting

### High OpenBao Latency

**Symptoms:** `BenchmarkOpenBaoSecretRetrieval` shows > 50ms

**Investigation:**
```bash
# Check OpenBao connectivity
./scripts/run_benchmarks.sh --benchmarks "OpenBao" --verbose

# Profile the bottleneck
go test -bench=BenchmarkOpenBaoSecretRetrieval -cpuprofile=openbao.prof ./internal/server
go tool pprof openbao.prof
```

**Solutions:**
- Increase cache TTL to reduce OpenBao calls
- Use connection pooling for OpenBao client
- Check network latency to OpenBao server
- Verify OpenBao server performance

### Poor Cache Performance

**Symptoms:** `BenchmarkCacheHitPerformance` > 50µs or cache misses dominate

**Investigation:**
```bash
# Check cache effectiveness
curl http://localhost:8081/_seam/cache/status

# Profile cache operations
go test -bench=BenchmarkCacheHitPerformance -memprofile=cache.prof ./internal/server
go tool pprof cache.prof
```

**Solutions:**
- Review cache key generation for collision rate
- Increase cache size limits
- Optimize cache TTL configuration
- Check for cache stampede patterns

### Memory Bloat

**Symptoms:** `BenchmarkMemoryFootprintPerConnection` > 500KB

**Investigation:**
```bash
# Generate memory profile
go test -bench=BenchmarkMemoryFootprintPerConnection -memprofile=mem.prof ./internal/server
go tool pprof --alloc_objects mem.prof

# Check for goroutine leaks
go test -bench=. -blockprofile=block.prof ./internal/server
go tool pprof block.prof
```

**Solutions:**
- Review response buffer sizes
- Check for connection pool leaks
- Verify single-flight cleanup
- Profile memory allocations for hotspots

### Throughput Saturation

**Symptoms:** `BenchmarkThroughputSaturation` shows degradation < 2000 RPS

**Investigation:**
```bash
# Check for blocking operations
go test -bench=BenchmarkThroughputSaturation -blockprofile=block.prof ./internal/server
go tool pprof block.prof

# Profile mutex contention
go test -bench=. -mutexprofile=mutex.prof ./internal/server
go tool pprof mutex.prof
```

**Solutions:**
- Increase worker pool sizes
- Optimize middleware chain order
- Reduce lock contention
- Check for goroutine bounds

## Best Practices

### Benchmark Development

1. **Isolate variables** - Each benchmark should test one specific aspect
2. **Use realistic data** - Match production request/response sizes
3. **Account for warmup** - Reset timers after initialization
4. **Report allocations** - Always include `-benchmem` flag
5. **Document targets** - Comment expected performance ranges

### Performance Monitoring

1. **Run daily** - Automated benchmark runs in CI/CD
2. **Track trends** - Use comparison reports to detect degradation
3. **Set alerts** - Notify on performance regression > 10%
4. **Profile quarterly** - Deep optimization sessions with profiling tools
5. **Document changes** - Note benchmark improvements in commit messages

### Regression Prevention

1. **Add benchmarks** for new features affecting performance
2. **Run before merging** - Gate performance-sensitive changes
3. **Compare baselines** - Always reference previous successful run
4. **Investigate outliers** - Any benchmark > 2x slower than baseline
5. **Document optimizations** - Record what improved and why

## Benchmark Maintenance

### Adding New Benchmarks

When adding new benchmarks to `internal/server/benchmark_test.go`:

1. Follow naming convention: `Benchmark<DescriptiveName>`
2. Add to appropriate category in documentation
3. Document expected performance range
4. Include in relevant test groups
5. Update comparison baselines

### Updating Targets

Review and update performance targets quarterly:
1. Analyze production performance data
2. Adjust targets based on real requirements
3. Document rationale for changes
4. Update CI/CD thresholds
5. Communicate changes to team

### Archive Strategy

Keep benchmark results for 6 months:
```bash
# Archive old results
tar -czf benchmark-results-$(date +%Y%m).tar.gz benchmark-results/
rm -rf benchmark-results/run-2025*
```

## Resources

### Internal Documentation
- [SEAM Architecture](./SEAM_ARCHITECTURE.md) - System design and components
- [OpenBao Integration](./OPENBAO_INTEGRATION.md) - Secret retrieval pipeline
- [Caching Strategy](./CACHING_STRATEGY.md) - Cache configuration and tuning

### External References
- [Go Benchmark Best Practices](https://dave.cheney.net/2013/06/30/how-to-write-benchmarks-in-go)
- [HTTP/2 Benchmarking Methodology](https://http2-explained.haxx.se/content/en/benchmarking)
- [Performance Testing Guidelines](https://github.com/golang/go/wiki/Benchmarking)

## Support

For benchmark-related questions or issues:
1. Check troubleshooting section above
2. Review benchmark code comments
3. Run with `--verbose` flag for detailed output
4. Create GitHub issue with benchmark results and system specs

---

**Last Updated:** 2025-08-15
**Maintained By:** SEAM Development Team
