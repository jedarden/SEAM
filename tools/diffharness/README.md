# SEAM Differential Harness

The differential capture + replay tool for testing SEAM route conformance. This is the highest-value test asset in the SEAM plan: it records real request/response pairs from an incumbent proxy, replays them against both the incumbent and SEAM, and verifies response equivalence modulo the enumerated expected diffs.

## Overview

The harness consists of two tools:

### `seam-capture` - Recording Proxy

A proxy that sits in front of an incumbent proxy and captures request/response pairs into a corpus file.

```bash
seam-capture \
  --incumbent https://argocd.example.com \
  --service argocd \
  --corpus testdata/corpus-argocd.json \
  --listen :8080
```

- Listens on `--listen` (default `:8080`)
- Forwards requests to `--incumbent`
- Captures the full request and forwarded response (status, headers, and body) into `--corpus`
- Redacts credential-bearing headers before persisting the corpus; add secret references manually for replay
- Can be disabled with `--capture-enabled=false` or `SEAM_CAPTURE_ENABLED=false` while retaining transparent forwarding
- Use `X-Seam-Capture-Skip` header to skip capture for health checks

### `seam-replay` - Conformance Tester

Replays a corpus against both incumbent and SEAM, then differentially compares responses.

```bash
seam-replay \
  --incumbent https://argocd.example.com \
  --seam http://localhost:9000 \
  --corpus testdata/corpus-argocd.json \
  --secrets testdata/secrets-argocd.local.json \
  --report testdata/report-argocd.json
```

- Loads corpus and secrets
- Replays each entry against both targets
- Compares responses for equivalence
- Outputs JSON report and human-readable summary
- Exits non-zero on any FAIL

## Corpus Format

A corpus is a JSON file containing captured request/response pairs:

```json
{
  "schema": "seam-diff-corpus/v1",
  "service": "argocd",
  "incumbent": "https://argocd.example.com",
  "capturedAt": "2026-07-27T10:00:00Z",
  "description": "ArgoCD API corpus",
  "entries": [
    {
      "id": "list-apps-get",
      "description": "List all applications",
      "request": {
        "method": "GET",
        "path": "/api/v1/applications",
        "query": "",
        "headers": {"Accept": ["application/json"]},
        "bodyB64": "",
        "bodyContentType": ""
      },
      "response": {
        "statusCode": 200,
        "headers": {"Content-Type": ["application/json"]},
        "bodyB64": "eyJvayI6dHJ1ZX0=",
        "bodyContentType": "application/json"
      },
      "secrets": [
        {
          "ref": "vault:seam/routes/argocd/ro-token",
          "injectAs": {"kind": "bearer"}
        }
      ],
      "expect": {
        "ignoreHeaders": ["Date", "Server"]
      }
    }
  ]
}
```

### Entry Structure

- `id`: Stable identifier for the entry
- `timestamp`: RFC3339 timestamp for the captured exchange
- `description`: What this entry exercises
- `request`: The caller's request (replayed verbatim)
  - `method`: HTTP method
  - `path`: Path only (no query)
  - `query`: Query string without leading `?`
  - `headers`: Request headers (canonicalized keys)
  - `bodyB64`: Base64-encoded body (empty if no body)
  - `bodyContentType`: Content-Type header for body
- `response`: The incumbent response observed during capture
  - `statusCode`: HTTP status code
  - `headers`: Response headers (credential-bearing values are redacted)
  - `bodyB64`: Base64-encoded response body
  - `bodyContentType`: Content-Type header for body
- `secrets`: References to injected credentials (never literal values)
- `expect`: Per-entry comparison overrides
  - `status`: Expected status for SEAM (if different from incumbent)
  - `ignoreHeaders`: Headers to ignore (volatile headers)
  - `ignoreBody`: Skip body comparison (for non-deterministic responses)
  - `skip`: Skip this entry with reason

## Secrets Resolution

Secrets are resolved at replay time from a local file or environment:

### File Format (`--secrets`)

```json
{
  "vault:seam/routes/argocd/ro-token": "my-secret-token",
  "vault:seam/routes/kalshi/api-key": "kalshi-key-123"
}
```

### Environment Variables

If a ref isn't in the secrets file, it's resolved from environment:

```
vault:seam/routes/argocd/ro-token → SEAM_DIFF_SECRET_vault_seam_routes_argocd_ro_token
```

## Differential Comparison Rules

The comparison engine enforces the plan's security invariants:

### Expected Diffs (Never Flagged)

- `X-SEAM-*` response headers added by SEAM
- `Deprecation`, `Sunset`, `Link` (deprecation headers)
- `Retry-After` (rate-limit headers)
- Injected credentials redacted to `[REDACTED-BY-SEAM]`

### Security Invariant (Hard FAIL)

A corpus entry returning byte-identically INCLUDING an echoed secret is a **FAILURE**.

The leak check runs first and independently, so it can never be masked by expected-diff allowances.

### Structural Comparison

After redaction, responses must match:

- **Status**: Must match (unless `expect.status` pins SEAM to a different status)
- **Headers**: Must match after ignoring volatile headers and SEAM additions
- **Body**: Must match after secret redaction (unless `expect.ignoreBody`)

## Building

```bash
cd tools/diffharness
go build -o seam-capture ./cmd/seam-capture
go build -o seam-replay ./cmd/seam-replay
```

## Testing

```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./...

# Run specific package
go test ./internal/compare
```

## Example Workflow

### 1. Capture a Corpus

```bash
# Start the capture proxy
seam-capture \
  --incumbent https://argocd.example.com \
  --service argocd \
  --corpus argocd-corpus.json \
  --listen :8080

# In another terminal, exercise the incumbent proxy through the capture proxy
curl http://localhost:8080/api/v1/applications
curl http://localhost:8080/api/v1/applications/myapp

# Press Ctrl+C to stop capturing
```

### 2. Edit the Corpus

Add secret references and per-entry expectations:

```bash
# Edit the corpus to add secrets
vim argocd-corpus.json

# Create secrets file (git-ignored)
cat > argocd-secrets.local.json <<EOF
{
  "vault:seam/routes/argocd/ro-token": "your-actual-token"
}
EOF
```

### 3. Run Replay

```bash
# Test against SEAM
seam-replay \
  --incumbent https://argocd.example.com \
  --seam http://localhost:9000 \
  --corpus argocd-corpus.json \
  --secrets argocd-secrets.local.json \
  --report argocd-report.json
```

### 4. Attach Report to Cutover PR

The JSON report is attachable to the service's Phase 6b cutover PR as evidence of conformance.

## Report Format

```json
{
  "corpus": "argocd",
  "corpusPath": "argocd-corpus.json",
  "incumbent": "https://argocd.example.com",
  "seam": "http://localhost:9000",
  "runAt": "2026-07-27T10:05:00Z",
  "durationSeconds": 2.45,
  "passCount": 8,
  "failCount": 0,
  "skipCount": 1,
  "entries": [
    {
      "id": "list-apps-get",
      "description": "List all applications",
      "verdict": "PASS",
      "secretLeaked": false,
      "reasons": []
    }
  ]
}
```

## Design Principles

1. **Standalone**: No SEAM code dependency; workable now for capture
2. **Security**: Never writes secret values to disk; only refs
3. **Deterministic**: Header canonicalization, stable ordering
4. **Reproducible**: Same corpus + secrets → same report

## Status

- [x] Corpus schema and loader
- [x] Differential comparison engine with leak detection
- [x] Secret reference resolver
- [x] Capture proxy
- [x] Replay tool with reporting
- [ ] Integration with SEAM route-fragment parser
- [ ] Automated corpus capture from production traffic
