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

Every `serve` configuration flag can also be set via an environment variable with the `SEAM_` prefix:

| Variable | Flag | Default |
|---|---|---|
| `SEAM_CALLER_PORT` | `--caller-port` | `8080` |
| `SEAM_OPERATOR_PORT` | `--operator-port` | `8081` |
| `SEAM_BASE_URL` | `--base-url` | `http://localhost:8080` |
| `SEAM_SPEC_DIR` | `--spec-dir` | `./spec` |
| `SEAM_FRAGMENT_MODE` | `--fragment-mode` | `false` |
| `SEAM_SCHEMA_PATH` | `--schema-path` | `./spec/route-fragment-schema.json` |
| `SEAM_CAPTURE_ENABLED` | `--capture-enabled` | `false` |
| `SEAM_CORPUS_DIR` | `--corpus-dir` | `corpus` |
| `SEAM_FRAGMENTS_DIR` | `--fragments-dir` | `./fragments` |
| `SEAM_UPSTREAM_CA_DIR` | `--upstream-ca-dir` | built-in CA directory (refused in-cluster) |
| `SEAM_UPSTREAM_ALLOWLIST` | `--allowlist-file` | none (refused in-cluster) |
| `SEAM_VAULT_BASE_DIR` | `--vault-base-dir` | `rs-manager/rs-manager/seam/routes` |
| `SEAM_MAX_REPLAYABLE_REQUEST_BYTES` | `--max-replayable-request-bytes` | `1048576` |
| `SEAM_MAX_BUFFERED_RESPONSE_BYTES` | `--max-buffered-response-bytes` | `1048576` |
| `SEAM_HOT_RELOAD_ENABLED` | `--enable-hot-reload` | `false` |

`--max-buffered-response-bytes` bounds only how much of a response SEAM may hold in memory for whole-body secret scrubbing; it is never a scrubbability limit and never rejects a response. A response whose declared `Content-Length` is at or under the cap is scrubbed whole and returned with its (recomputed) `Content-Length`. A response over the cap — or with no declared length, or whose *decoded* size exceeds the cap even when its encoded size does not — is scrubbed incrementally with bounded memory and streamed to the caller chunked (`Content-Length` removed) with the same status, headers, trailers, and content-encoding; nothing is truncated. A non-positive value falls back to the default. The two size caps are independent knobs: this one governs responses only, `--max-replayable-request-bytes` governs request replay only, and tuning one never moves the other.

#### Precedence (serve)

**Flags win over the environment.** An explicitly passed command-line flag takes precedence over a non-empty corresponding `SEAM_*` variable. Environment values fill flags that were not passed, and built-in defaults fill everything left unset. This gives operators an environment-based deployment default while preserving an explicit CLI override. `seam healthcheck` follows the same rule for `SEAM_CALLER_PORT`, so an explicit probe flag wins while an environment-only port override is still honoured.

A variable set to the **empty string counts as unset**: the flag value survives.

#### Invalid values

- **Integer variables** (`*_PORT`, `*_BYTES`) parse as an optional sign followed by digits. Leading whitespace is skipped and anything after the integer prefix is ignored: `SEAM_CALLER_PORT=8080abc` configures `8080`, and `0x10` configures `0`. A value with **no leading integer** is rejected — the previous environment/default value is kept and a warning is logged. An explicit flag still wins without parsing the environment value. There is no range validation at configuration time: an out-of-range port such as `-5` or `99999` is applied and fails later, when the listener binds.
- **Boolean variables** (`SEAM_FRAGMENT_MODE`, `SEAM_CAPTURE_ENABLED`, `SEAM_HOT_RELOAD_ENABLED`) recognize exactly `true` and `1`, lowercase. Any other non-empty value — including `TRUE`, `yes`, `0` and `false` — means **false** when the environment supplies the setting; an explicit flag still wins. `SEAM_HOT_RELOAD_ENABLED` is deliberately asymmetric: only `true`/`1` changes an unset flag, so an environment value can enable hot reload but cannot override an explicit flag.
- **`SEAM_VAULT_BASE_DIR`** is whitespace-trimmed, fills an omitted flag, and falls back to the shared default when neither names a prefix.

#### lint / diff explicit-flag tracking

`seam lint` and `seam diff` follow the same flag-over-environment rule for `SEAM_FRAGMENTS_DIR`, `SEAM_SCHEMA_PATH` and `SEAM_UPSTREAM_ALLOWLIST`: the environment fills the corresponding flag only while it is **still at its default**. (Corollary: passing the default value explicitly, e.g. `--fragments-dir ./fragments`, is indistinguishable from omitting the flag.) `seam import` reads no `SEAM_*` configuration.

Other `SEAM_*` variables (`SEAM_OPENBAO_ADDR`, `SEAM_OPENBAO_SA_TOKEN_PATH`, `SEAM_TEST_IDENTITY_MODE`, …) are server-runtime knobs, not CLI configuration.

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

---

Part of [jedarden.com](https://jedarden.com)

*This GitHub repo is a read-only mirror of git.ardenone.com/jedarden/SEAM — issues and PRs are welcome here either way.*
