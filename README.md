# SEAM

Self-documenting Endpoint Access Mediator — a single unified HTTP endpoint that proxies to multiple authenticated backend services, injecting the required secret into each request server-side (from OpenBao) before forwarding it on, then passing the response back. Calling agents never see the secret; malformed requests are guided toward the correct shape via a self-describing OpenAPI spec instead of a bare error.

## Structure

- `docs/notes/` — features, constraints, design decisions
- `docs/research/` — external reference material and prior art
- `docs/plan/plan.md` — complete application plan

## Running SEAM

### Starting the Server

```bash
seam serve [flags]
```

### Configuration Flags

#### Server Ports
- `--caller-port` (default: `8080`) - Port for the caller-facing listener
- `--operator-port` (default: `8081`) - Port for the operator-only listener

#### Server Configuration
- `--base-url` (default: `http://localhost:8080`) - Base URL for the caller-facing interface
- `--spec-dir` (default: `./spec`) - Directory containing local OpenAPI spec files

#### Corpus Capture
- `--capture-enabled` (default: `false`) - Enable HTTP request/response capture for corpus collection
- `--corpus-dir` (default: `corpus`) - Directory to store captured corpus files

### Environment Variables

All configuration flags can be set via environment variables with the `SEAM_` prefix:

- `SEAM_CALLER_PORT` - Caller-facing port
- `SEAM_OPERATOR_PORT` - Operator-only port
- `SEAM_BASE_URL` - Base URL for caller interface
- `SEAM_SPEC_DIR` - OpenAPI spec directory
- `SEAM_CAPTURE_ENABLED` - Enable/disable corpus capture (`true`/`false` or `1`/`0`)
- `SEAM_CORPUS_DIR` - Corpus storage directory

### Examples

#### Basic Usage (capture disabled)
```bash
seam serve
```

#### Enable Corpus Capture
```bash
# Via command-line flag
seam serve --capture-enabled --corpus-dir ./my-corpus

# Via environment variable
SEAM_CAPTURE_ENABLED=true SEAM_CORPUS_DIR=./my-corpus seam serve
```

#### Custom Ports
```bash
seam serve --caller-port 9000 --operator-port 9001
```

### Capture Status Endpoint

When capture is enabled, you can check the status via the operator endpoint:

```bash
curl http://localhost:8081/_seam/capture/status
```

Response:
```json
{
  "enabled": true,
  "entry_count": 42,
  "corpus_dir": "corpus"
}
```

### Manual Corpus Save

Trigger an immediate save of the corpus:

```bash
curl -X POST http://localhost:8081/_seam/capture/save
```

Response:
```json
{
  "status": "saved",
  "entry_count": 42
}

## Running Benchmarks

SEAM includes a benchmark suite to measure performance characteristics.

### Quick Start

Run all benchmarks with default settings:
```bash
make benchmark
```

### Benchmark Modes

**Standard run** (10s per benchmark, memory stats):
```bash
go test -bench=. -benchmem ./benches/...
```

**CPU profiling**:
```bash
make benchmark-cpu
# Analyze with: go tool pprof benchmark-results/cpu.prof
```

**Memory profiling**:
```bash
make benchmark-mem
# Analyze with: go tool pprof benchmark-results/mem.prof
```

**CI mode** (JSON output for automated collection):
```bash
make benchmark-ci
# Results: benchmark-results/latest.json
```

### Interpreting Results

Benchmark output shows:
```
BenchmarkProxyForwarding/GET-8    1000000    1234 ns/op    512 B/op    8 allocs/op
```

- `1000000`: Iterations executed
- `1234 ns/op`: Nanoseconds per operation
- `512 B/op`: Bytes allocated per operation  
- `8 allocs/op`: Number of memory allocations per operation

For detailed benchmark documentation, see [`benches/README.md`](benches/README.md).

### Baselines and Regression Checks

Capture a baseline for a benchmark category, then compare later runs against
it. The check exits non-zero when a metric regresses by more than 10% (or the
`THRESHOLD` supplied by the caller).

```bash
make benchmark-save-baseline TYPE=throughput
make benchmark-check-regression TYPE=throughput
make benchmark-check-regression TYPE=memory THRESHOLD=15
```

## Development

### Building

```bash
make build
```

### Testing

```bash
make test
```

### Dependencies

```bash
make deps
```
