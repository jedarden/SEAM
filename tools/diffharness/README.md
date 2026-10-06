# SEAM Differential Harness

The differential capture + replay tool for testing SEAM route conformance. This is the highest-value test asset in the SEAM plan: it records real request/response pairs from an incumbent proxy, promotes reviewed request data into replay fixtures, then replays those requests against both the incumbent and SEAM to verify response equivalence modulo the enumerated expected diffs.

> **The contract lives in
> [docs/design/differential-replay-contract.md](../../docs/design/differential-replay-contract.md).**
> That document is the canonical statement of the canonicalization rules
> (C), per-dimension comparison rules (D), nondeterministic-field handling
> (N), secret-reference rules (S), failure semantics (F), exit codes (X),
> and how the cutover workflow consumes the replay result (G) — each rule
> pinned by a named test (the index at the bottom of the document). This
> README is the operator manual; on any disagreement the contract wins.

## Overview

The harness consists of three tools:

### `seam-capture` - Recording Proxy

A proxy that sits in front of an incumbent proxy and captures request/response pairs into a corpus file.

```bash
seam-capture \
  --incumbent https://argocd.example.com \
  --service argocd \
  --corpus corpus/argocd-proxy/corpus.json \
  --listen :8080
```

- Listens on `--listen` (default `:8080`)
- Forwards requests to `--incumbent`
- Captures the full request and forwarded response (status, headers, and body) into the private runtime capture at `--corpus`
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

- Loads a checked-in fixture (or a private runtime capture) and secrets
- Replays each entry against both targets
- Compares responses for equivalence
- Outputs JSON report and human-readable summary
- Exit codes (contract X1): `0` no FAIL, `1` any FAIL or harness failure
  (unreadable corpus/secrets, all-skipped corpus, unwritable report), `2`
  usage error. `seam-cutover` gates on this contract alone (contract G1).

### `seam-cutover` - Cutover Gate Runner

Mechanizes the go/no-go gate of a per-service SEAM cutover. The gate items,
their blocking rules, and the exit-code contract are owned by the
[migration runbook](../../docs/migration-runbook.md) (Stage 2); this tool is
where they execute.

```bash
seam-cutover check \
  --service argocd \
  --seam https://seam-rs-manager-ts.ardenone.com:8444 \
  --operator https://seam-rs-manager-ts.ardenone.com:8445 \
  --incumbent https://argocd-ro-ardenone-manager-ts.ardenone.com:8444 \
  --corpus corpus/argocd-proxy/corpus.json \
  --replay-bin ./seam-replay \
  --secrets corpus/argocd-proxy/secrets.local.json \
  --agent-doc /home/coding/CLAUDE.md \
  --agent-doc-contains 'argocd-ro-ardenone-manager-ts' \
  --report corpus/argocd-proxy/cutover-check.json
```

- `check` runs the mechanical gate: SEAM healthz/readyz, the operator-port
  trust-boundary refusal (must be probed from a worker-vantage host), corpus
  route presence in `/openapi.json` (path templates matched segment-wise),
  DNS preconditions for the dual-run window, the prose-still-present guard
  (`--agent-doc` must still contain the incumbent pointer — it fails the
  wrong-direction sequencing case too), and `seam-replay` as a subprocess
  gated on its exit code
- Unarmed checks (`--operator`, `--replay-bin`, `--agent-doc` not given)
  report SKIP, never a silent pass; `--metered` adds the Phase 13
  cost-governor MANUAL item
- Writes the JSON report to `--report`; the replay report is derived beside
  it (`<report>-replay.json`) so a cutover PR attaches both
- Exit codes: `0` no mechanical failures (MANUAL items still need operator
  attestation in the cutover PR), `1` at least one mechanical FAIL (NO-GO —
  blocks this service only), `2` usage error
- `rollback --service <svc> [--level agent|fragment|binary|all]` prints the
  per-service rollback runbook with the concrete revert-finding commands
  filled in — the mechanism everywhere is `git revert` in
  declarative-config, never a live mutation

## Corpus Format

The `seam-diff-corpus/v1` envelope has two persisted forms with different field
ownership:

- A **runtime capture** is private producer output. Each entry is one complete
  request/response pair: the request sent to the incumbent and the incumbent
  response observed for that request. Capture producers do not add `secrets`
  or `expect`.
- A **checked-in replay fixture** is the reviewed promotion of a runtime
  capture. It retains request data plus `secrets` references and `expect`
  replay policy, but has no entry-level `response`. Replay obtains fresh
  responses from both the incumbent and SEAM; it never reads a capture-time
  response as an oracle.

Promotion is the boundary: review the request, add only reference-based
  secrets and explicit replay policy, and drop the capture-time `response`
  before moving the document to `tools/diffharness/testdata/`. The detailed
  field contract is in
  [aligned-capture-replay-schema-contract.md](../../docs/design/aligned-capture-replay-schema-contract.md).

> **Ref base note:** refs in a checked-in fixture must use SEAM's enforced
> vault base dir, `rs-manager/rs-manager/seam/routes`; a ref written for a
> new capture must use that base. The ref→env-var mapping — derived by
> `internal/secref` from the ref string alone — is mechanical for any base.
> The full reference syntax — scheme handling, base containment, the
> fragment `x-vault-path` boundary, serialization rules — is defined in
> [docs/notes/credential-reference-syntax.md](../notes/credential-reference-syntax.md).

### Runtime capture

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
      }
    }
  ]
}
```

### Checked-in replay fixture

The promoted fixture keeps the request and adds replay configuration. It does
not carry the runtime capture's `response`:

```json
{
  "schema": "seam-diff-corpus/v1",
  "service": "argocd-ro",
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
      "secrets": [
        {
          "ref": "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token",
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
- `timestamp`: RFC3339 timestamp for the captured exchange; informational
  metadata that replay never reads and promotion may retain or omit
- `description`: What this entry exercises
- `request`: The caller's request (retained and replayed verbatim in both forms)
  - `method`: HTTP method
  - `path`: Path only (no query)
  - `query`: Query string without leading `?`
  - `headers`: Request headers (canonicalized keys)
  - `bodyB64`: Base64-encoded body (empty if no body)
  - `bodyContentType`: Content-Type header for body
- `response`: Runtime-capture-only incumbent response observed during capture;
  promotion drops it, and replay collects fresh responses from both targets
  - `statusCode`: HTTP status code
  - `headers`: Response headers (credential-bearing values are redacted)
  - `bodyB64`: Base64-encoded response body
  - `bodyContentType`: Content-Type header for body
- `secrets`: Fixture-only references to injected credentials (never literal
  values), added during promotion
- `expect`: Fixture-only per-entry comparison overrides, added during promotion
  - `status`: Expected status for SEAM (if different from incumbent)
  - `ignoreHeaders`: Headers to ignore (volatile headers)
  - `ignoreBody`: Skip body comparison (for non-deterministic responses)
  - `skip`: Skip this entry with reason

The response fields describe capture evidence, not fixture expectations. The
fresh responses collected during replay are transient comparison inputs and
are summarized only in the replay report.

## Secrets Resolution

Secrets are resolved at replay time from a local file or environment:

### File Format (`--secrets`)

```json
{
  "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token": "my-secret-token",
  "vault:rs-manager/rs-manager/seam/routes/kalshi/api-key": "kalshi-key-123"
}
```

### Environment Variables

If a ref isn't in the secrets file, it's resolved from environment:

```
vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token → SEAM_DIFF_SECRET_VAULT_RS_MANAGER_RS_MANAGER_SEAM_ROUTES_ARGOCD_RO_RO_TOKEN
```

The whole ref is upper-cased — scheme included — and every run of
non-`[A-Z0-9_]` characters collapses to a single `_`.

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
go build -o seam-cutover ./cmd/seam-cutover
```

The built binaries are git-ignored (repo `.gitignore`) — never commit them.

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
  "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token": "your-actual-token"
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
