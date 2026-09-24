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

`seam serve` takes fifteen configuration flags. All fifteen are listed here and all fifteen have a `SEAM_*` environment counterpart (see [Environment Variables](#environment-variables)); the two lists describe the same set.

#### Server Ports
- `--caller-port` (default: `8080`) - Port for the caller-facing listener
- `--operator-port` (default: `8081`) - Port for the operator-only listener

#### Server Configuration
- `--base-url` (default: `http://localhost:8080`) - Base URL for the caller-facing interface
- `--spec-dir` (default: `./spec`) - Directory containing local OpenAPI spec files

#### Fragments & Schema
- `--fragment-mode` (default: `false`) - Enable fragment merge mode (reads from `spec-dir/fragments.d`)
- `--schema-path` (default: `./spec/route-fragment-schema.json`) - Path to the route-fragment JSON schema for validation
- `--fragments-dir` (default: `./fragments`) - Directory containing OpenAPI fragment files
- `--enable-hot-reload` (default: `false`) - Enable file-watch hot reload of route fragments

#### Corpus Capture
- `--capture-enabled` (default: `false`) - Enable HTTP request/response capture for corpus collection
- `--corpus-dir` (default: `corpus`) - Directory to store captured corpus files

#### Upstream Trust
- `--upstream-ca-dir` (default: `/etc/gateway/upstream-ca`) - Directory for upstream CA bundles
- `--allowlist-file` (default: none) - Path to the upstream host allowlist

#### Vault Path & Body Limits
- `--vault-base-dir` (default: `rs-manager/rs-manager/seam/routes`) - Base directory that `x-vault-path` must nest `x-seam-owner` under
- `--max-replayable-request-bytes` (default: `1048576`) - Maximum request body size buffered for replay, in bytes
- `--max-buffered-response-bytes` (default: `1048576`) - Maximum decoded response body size held for whole-response scrubbing, in bytes (see the note below)

**In-cluster refusal (upstream trust).** When both `KUBERNETES_SERVICE_HOST` and `KUBERNETES_PORT` are set, SEAM treats the process as running in-cluster and strips operator-supplied overrides of the two upstream-trust paths: a custom `--upstream-ca-dir` / `SEAM_UPSTREAM_CA_DIR` is refused with a warning and `/etc/gateway/upstream-ca` is used instead, and the allowlist is always the operator-mounted `/etc/gateway/allowlist.yaml` — a supplied `--allowlist-file` / `SEAM_UPSTREAM_ALLOWLIST` can never replace that mounted control inside a pod. The refusal applies to supplied values only; the defaults are exactly those in-cluster paths. Outside a cluster both flags accept any path.

### Environment Variables

Every `serve` configuration flag can also be set via an environment variable with the `SEAM_` prefix — the table covers all fifteen flags above:

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
| `SEAM_UPSTREAM_CA_DIR` | `--upstream-ca-dir` | `/etc/gateway/upstream-ca` (override refused in-cluster) |
| `SEAM_UPSTREAM_ALLOWLIST` | `--allowlist-file` | none (override refused in-cluster) |
| `SEAM_VAULT_BASE_DIR` | `--vault-base-dir` | `rs-manager/rs-manager/seam/routes` |
| `SEAM_MAX_REPLAYABLE_REQUEST_BYTES` | `--max-replayable-request-bytes` | `1048576` |
| `SEAM_MAX_BUFFERED_RESPONSE_BYTES` | `--max-buffered-response-bytes` | `1048576` |
| `SEAM_HOT_RELOAD_ENABLED` | `--enable-hot-reload` | `false` |

The pairing rule is mechanical — `SEAM_` plus the flag name upper-cased with dashes as underscores — and thirteen of the fifteen variables follow it. Two are paired by meaning instead and break the derivation: `SEAM_UPSTREAM_ALLOWLIST` maps to `--allowlist-file` (not `SEAM_ALLOWLIST_FILE`, keeping the allowlist's *upstream* identity in the variable name), and `SEAM_HOT_RELOAD_ENABLED` maps to `--enable-hot-reload` (not `SEAM_ENABLE_HOT_RELOAD`). Where the rule and the table disagree, the table is authoritative.

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

### `seam healthcheck`

Probe the caller-facing liveness endpoint. This is what the container image's `HEALTHCHECK` invokes — the runtime image is `FROM scratch` and has no shell, so the probe must be a real subcommand.

```bash
seam healthcheck [--caller-port <port>] [--timeout <duration>]
```

- `--caller-port` (default: `8080`) - Port of the caller-facing listener to probe
- `--timeout` (default: `2s`) - Probe timeout

Issues `GET http://127.0.0.1:<port>/_seam/healthz` and requires HTTP `200`. `SEAM_CALLER_PORT` fills the port only when `--caller-port` was **not** passed explicitly — the same flag-over-environment rule as `serve` (see [Precedence (serve)](#precedence-serve)) — so a port override configured on the Deployment is honoured without beating an explicit flag.

Exit codes: `0` the gateway answered `200`; `1` the probe failed or timed out; `2` invalid usage.

### `seam lint`

Validate route fragments against `route-fragment-schema.json` plus SEAM's structural checks: the `x-seam-owner` chain (owner must match the fragment's parent directory and be nested by `x-vault-path`), authored `x-api-version` shape and placement, reserved control-plane paths, upstream URLs (well-formed absolute http(s), no IP-literal hosts, membership of the operator allowlist when one is supplied), transport acknowledgements (plaintext upstreams, `insecureSkipVerify`, unscrubbable responses), and route guards (quota unit mismatches, breaker disagreements across same-origin routes). Spec and structural violations are **errors**; acknowledgement items that need human review are **warnings**.

```bash
seam lint [flags] [path ...]
```

- `--fragments-dir`, `--fragments` (default: `./fragments`) - Directory containing route fragments
- `--schema-path`, `--schema` (default: `./spec/route-fragment-schema.json`) - Path to `route-fragment-schema.json`
- `--upstream-allowlist`, `--upstream-allowlist-path`, `--allowlist-path`, `--allowlist` (default: none) - Operator-owned upstream-host allowlist; absent is inert
- `--json` - Emit a machine-readable JSON report (`LintReport`) instead of text

Positional arguments select what is linted: none means the `--fragments-dir` directory, a single directory means that directory, and one or more file paths means exactly those files. Flags may also appear *after* positional paths, so shell-expanded globs behave as expected:

```bash
seam lint                                    # lint ./fragments
seam lint fragments/github-api               # lint one fragment directory
seam lint fragments/*/fragment.yaml --json   # lint explicit files, JSON report
```

`SEAM_FRAGMENTS_DIR`, `SEAM_SCHEMA_PATH` and `SEAM_UPSTREAM_ALLOWLIST` fill the corresponding flag only while it is still at its default (see [lint / diff explicit-flag tracking](#lint--diff-explicit-flag-tracking)).

Exit codes: `0` lint passed (warnings allowed); `1` at least one error finding; `2` usage or setup failure — unknown flag, unreadable path, a schema that does not compile, or an unwritable report.

### `seam diff`

Merge the current route fragments into a spec and compare it against a base version — by default the fragments at git `HEAD`, extracted with `git archive`. Use it to see which routes a fragment change adds, removes, or modifies before pushing.

```bash
seam diff [flags]
```

- `--fragments-dir`, `-f` (default: `./fragments`) - Directory containing route fragments
- `--base`, `-b` (default: git `HEAD`'s `fragments/`) - Base directory to compare against
- `--json`, `-j` - Emit the machine-readable `DiffResult` (`paths_added`, `paths_removed`, `paths_modified`, `summary.has_changes`) instead of the text report
- `--output`, `-o` - Additionally write the merged **current** spec to this file
- `--unified` (default: `true`), `--side-by-side` - Presentation switches; the current writer emits the same structured change report for either

Behaviour worth knowing:

- Positional path arguments are rejected — pass `--fragments-dir` instead.
- Outside a git repository (or if `git archive` fails) there is no implicit base: pass `--base` explicitly or the command exits `2`.
- A `--fragments-dir` that is mistyped or holds no fragments is refused (`2`) — "no fragments loaded" is not the same as "no changes". An empty **base** is allowed, with a warning that every current path will be reported as added.
- `SEAM_FRAGMENTS_DIR` fills `--fragments-dir` only while it is still at its default.

Exit codes: `0` the merged specs are identical; `1` changes detected; `2` setup failure — usage error, no resolvable base, no current fragments, or a load/merge/compare/write failure.

### `seam import`

Fetch an OpenAPI 3.x (or Swagger 2.0) spec over HTTP(S) and generate a *curatable bootstrap* fragment. The generated file carries `x-seam-schema: v1`, `x-seam-owner`, `x-upstream` (derived from the spec URL as `scheme://host[:port]` — the path that located the spec document is not part of the upstream) and the imported paths. The credential and access decisions are deliberately left to the curator: the output reminds you to add `x-vault-path`, `x-inject-as`, `x-required-scope` and any TLS/cache knobs by hand. An `http://` upstream emits `x-upstream-plaintext: acknowledged` with a loud note. Swagger 2.0 sources are accepted; their top-level `definitions`/`parameters` are carried across as `components.schemas`/`components.parameters` so `$ref`s inside imported operations keep resolving. The command reads no `SEAM_*` configuration.

```bash
seam import --from-url <url> [flags]
```

- `--from-url`, `-u` (required) - URL of the OpenAPI spec to import; must be `http` or `https`
- `--owner`, `-o` (default: derived from the URL host) - Owner/service name for the fragment
- `--output`, `-f` (default: `<owner>/fragment.yaml`) - Output fragment file path
- `--paths`, `-p` - Comma-separated list of paths to import (default: all)
- `--methods`, `-m` - Comma-separated list of HTTP methods to import, case-insensitive (default: all)
- `--filter-prefix` - Only import paths with this prefix
- `--strip-prefix` - Strip this prefix from imported paths
- `--add-prefix` - Add this prefix to all imported paths
- `--timeout` (default: `30s`) - HTTP timeout for fetching the spec

```bash
# Whole spec: owner and output path are derived from the URL
seam import --from-url https://api.example.com/openapi.json
#   -> owner api-example-com, written to api-example-com/fragment.yaml

# Curated import into the fragments tree
seam import -u https://internal.example.com/swagger.json \
  --owner github-api --output fragments/github-api/fragment.yaml \
  --filter-prefix /api/v1/ --strip-prefix /api/v1 --methods get,post

# Then validate — seam lint checks x-seam-owner against the parent directory name,
# so keep the fragment in a directory named after the owner
seam lint fragments/github-api/fragment.yaml
```

Exit codes: `0` fragment written; `1` no paths matched the filter criteria; `2` failure — missing or invalid `--from-url`, a non-http(s) scheme, a fetch/HTTP error, a spec that parses as neither JSON nor YAML, or an unwritable output path.

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
```

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
