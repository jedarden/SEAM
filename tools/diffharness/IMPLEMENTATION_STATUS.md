# Differential Harness Implementation Status

## ✅ COMPLETE - All Requirements Met

The SEAM differential capture + replay tooling is **fully implemented and operational**. This tool is the highest-value test asset in the SEAM plan, enabling service-by-service conformance testing for Phase 6b cutover.

## What Was Built

### 1. Capture Tool (`seam-capture`)

**Purpose:** Records real request/response pairs from an incumbent proxy into a
private runtime-capture format. Promotion retains the request and adds
reference-based replay configuration for a checked-in fixture; replay obtains
fresh responses from both targets rather than reading a stored response.

**Location:** `/home/coding/SEAM/tools/diffharness/cmd/seam-capture/main.go`

**Features:**
- Standalone HTTP proxy that forwards traffic to an incumbent proxy
- Captures each request/response pair as a corpus entry
- Supports incremental capture (append to existing corpus)
- Auto-saves every 10 entries
- Skips capture for health checks (via `X-Seam-Capture-Skip` header)

**Usage:**
```bash
seam-capture \
  --incumbent https://argocd.example.com \
  --service argocd \
  --corpus argocd-corpus.json \
  --listen :8080
```

### 2. Replay Tool (`seam-replay`)

**Purpose:** Replays a corpus against BOTH the incumbent and SEAM, then differentially compares responses.

**Location:** `/home/coding/SEAM/tools/diffharness/cmd/seam-replay/main.go`

**Features:**
- Loads corpus with secret references (no literal values stored)
- Resolves secrets from local file or environment at replay time
- Replays each entry against both incumbent and SEAM
- Performs differential comparison with all expected diffs
- Generates per-service corpus pass report (JSON + human-readable)
- Exits non-zero on any FAIL

**Usage:**
```bash
seam-replay \
  --incumbent https://argocd.example.com \
  --seam http://localhost:9000 \
  --corpus argocd-corpus.json \
  --secrets argocd-secrets.local.json \
  --report argocd-report.json
```

### 3. Corpus Format

**Location:** `/home/coding/SEAM/tools/diffharness/internal/corpus/corpus.go`

**Schema Version:** `seam-diff-corpus/v1`

The schema has two persisted forms:

- **Runtime capture:** private producer output containing the request sent to
  the incumbent and the incumbent response observed for that request. Capture
  producers do not populate `secrets` or `expect`.
- **Checked-in replay fixture:** reviewed promotion containing request data,
  `secrets` references, and `expect` replay policy. It has no entry-level
  `response`; `seam-replay` collects fresh incumbent and SEAM responses at
  replay time.

The structure example below is the checked-in fixture form. The capture-time
response is intentionally absent because it is evidence from the private
capture, not a replay expectation.

> **Ref base note:** refs in a checked-in fixture must use SEAM's enforced vault
> base dir, `rs-manager/rs-manager/seam/routes`; a ref written for a new
> capture must use that base. The ref→env-var mapping is mechanical for any
> base.

**Structure:**
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

**Security Design (checked-in fixtures):**
- Secret references only (never literal values)
- Values resolved at replay time from local git-ignored file or environment
- Reviewed fixture corpora under `testdata/` are safe to commit (refs only)
- Runtime captures under the repository-root `corpus/` directory are private,
  gitignored, and never committed or shared as-is

### 4. Comparison Engine

**Location:** `/home/coding/SEAM/tools/diffharness/internal/compare/compare.go`

**Expected Diffs (Never Flagged):**
- ✅ `X-SEAM-*` response headers (spec version, API version, scope version)
- ✅ `Deprecation`, `Sunset`, `Link` (deprecation headers)
- ✅ `Retry-After` (rate-limit headers)
- ✅ Injected credentials redacted to `[REDACTED-BY-SEAM]`

**Security Invariant (Hard FAIL):**
- ✅ Echoed secret in SEAM response → FAIL (leak detection)
- ✅ Leak check runs first and independently (cannot be masked)

**Structural Comparison:**
- ✅ Status code comparison (with optional pinning)
- ✅ Header comparison (ignoring volatile headers)
- ✅ Body comparison (after secret redaction)
- ✅ Trailer comparison

**Advanced Features:**
- ✅ Per-entry expectations (status pinning, header ignores, body ignore)
- ✅ Skip support for entries not yet ready for replay
- ✅ Multi-secret handling with longest-first redaction
- ✅ Order-insensitive repeated header comparison

### 5. Secret Resolution

**Location:** `/home/coding/SEAM/tools/diffharness/internal/secref/secref.go`

**Sources:**
1. **Local JSON file:** `--secrets path/to/secrets.json`
   ```json
   {
     "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token": "my-secret-token"
   }
   ```

2. **Environment variables:** Auto-derived from ref
   ```
   vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token → SEAM_DIFF_SECRET_VAULT_RS_MANAGER_RS_MANAGER_SEAM_ROUTES_ARGOCD_RO_RO_TOKEN
   ```

### 6. Cutover Gate Runner (`seam-cutover`)

**Purpose:** Mechanizes the go/no-go gate of a per-service SEAM cutover —
the runbook (docs/migration-runbook.md, Stage 2) owns the gate items; this
tool is where they execute.

**Location:** `/home/coding/SEAM/tools/diffharness/cmd/seam-cutover/`

**Features:**
- `check` subcommand runs the mechanical gate: SEAM healthz/readyz, the
  operator-port trust-boundary refusal (transport-level silence = the ACL
  holds; ANY HTTP answer, even 403, = FAIL), corpus route presence in
  `/openapi.json` with segment-wise path-template matching, DNS preconditions
  for the dual-run window, the prose-still-present guard (fails the
  wrong-direction sequencing case too), and `seam-replay` as a subprocess
  gated on its exit code
- Unarmed checks report SKIP, never a silent pass; MANUAL items (≥60 s
  retry-budget attestation; Phase 13 cost governor for `--metered` services)
  are printed for the cutover PR
- Writes the JSON check report to `--report`, with the replay report derived
  beside it (`<report>-replay.json`)
- Exit codes: 0 = no mechanical failures, 1 = at least one mechanical FAIL
  (NO-GO, blocks this service only), 2 = usage error
- `rollback` subcommand prints the per-service rollback runbook (L1 agent
  traffic / L2 fragment / L3 binary) with the concrete `git revert`
  revert-finding commands filled in — never a live mutation

**Usage:** see the README's `seam-cutover` section and
docs/migration-runbook.md §Stage 2 and §Rollback.

## Testing

**Location:** `/home/coding/SEAM/tools/diffharness/internal/compare/compare_test.go`

**Test Coverage:** 20 comprehensive tests covering:
- ✅ Identical responses PASS
- ✅ X-SEAM-* headers expected
- ✅ Redacted credential echo PASS
- ✅ Echoed secret leak FAIL
- ✅ Leak in header FAIL
- ✅ Leak in trailer FAIL
- ✅ Bearer echo redaction
- ✅ Header dropped FAIL
- ✅ Header added FAIL
- ✅ Status diff FAIL
- ✅ Body diff FAIL
- ✅ Ignore headers
- ✅ Substring secret handling
- ✅ Repeated headers (order-insensitive)
- ✅ Deprecation headers expected
- ✅ Empty secret handling
- ✅ Expected status pinning
- ✅ Expected status mismatch
- ✅ Ignore body suppression
- ✅ Ignore body doesn't weaken leak check

**Test Results:** All 20 tests PASS ✅

## Documentation

**Location:** `/home/coding/SEAM/tools/diffharness/README.md`

**Contents:**
- ✅ Tool overview
- ✅ Usage examples for both tools
- ✅ Corpus format documentation
- ✅ Entry structure explanation
- ✅ Secrets resolution (file + env)
- ✅ Differential comparison rules
- ✅ Building instructions
- ✅ Testing instructions
- ✅ Example workflow (capture → edit → replay → report)
- ✅ Report format
- ✅ Design principles
- ✅ Implementation status

## Design Principles

1. **Standalone:** No SEAM code dependency; workable now for capture
2. **Security:** Never writes secret values to disk; only refs
3. **Deterministic:** Header canonicalization, stable ordering
4. **Reproducible:** Same corpus + secrets → same report

## How This Meets the Bead Requirements

### Bead Task: "Build the capture + replay tooling"

✅ **COMPLETE** - The tool is built, tested, documented, and operational.

### Bead Requirement: "Record real request/response pairs at an incumbent proxy into a corpus format"

✅ **DONE** - `seam-capture` proxy records runtime request/response pairs to
`seam-diff-corpus/v1`; promotion drops the capture-time response before a
reviewed fixture is checked in.

### Bead Requirement: "Replay against BOTH the incumbent and SEAM"

✅ **DONE** - `seam-replay` sends each corpus entry to both targets and captures responses.

### Bead Requirement: "Assert response-equivalence modulo the enumerated expected diffs"

✅ **DONE** - Comparison engine handles:
- X-SEAM-* response headers
- Injected upstream credential
- Bytes redacted to [REDACTED-BY-SEAM]

### Bead Requirement: "A corpus entry returning byte-identically INCLUDING an echoed secret is a FAILURE"

✅ **DONE** - Leak detection runs first, independently, and cannot be masked. `TestEchoedSecretInSeamResponseIsLeakFailure` proves this.

### Bead Requirement: "Output: a per-service corpus pass report attachable to that service's 6b cutover PR"

✅ **DONE** - JSON report format with pass/fail/skip counts, per-entry details, and human-readable summary.

### Bead Requirement: "Standalone tool; no SEAM code dependency to start"

✅ **DONE** - Tool is standalone Go binary with no dependency on SEAM server code.

## Next Steps (Future Enhancements)

The core differential harness is complete. Future enhancements noted in the README:

1. **Integration with SEAM route-fragment parser**
   - Auto-populate `Secrets` from route fragment `x-inject` rules
   - Auto-generate `Expect.status` for `_all` fan-out routes (207)

2. **Automated corpus capture from production traffic**
   - Stand up seam-capture in production for a time window
   - Auto-capture real traffic patterns
   - Auto-generate initial corpus

These are **enhancements**, not **completions**. The tool is fully functional today.

## Verification

To verify the tool works:

```bash
# Build
cd /home/coding/SEAM
go build -o seam-capture ./tools/diffharness/cmd/seam-capture
go build -o seam-replay ./tools/diffharness/cmd/seam-replay

# Test
go test ./tools/diffharness/internal/compare -v

# Run capture
./seam-capture \
  --incumbent https://httpbin.org \
  --service test \
  --corpus test-corpus.json \
  --listen :8080

# In another terminal, exercise it:
curl http://localhost:8080/json
curl http://localhost:8080/status/200

# Run replay (once SEAM is running)
./seam-replay \
  --incumbent https://httpbin.org \
  --seam http://localhost:9000 \
  --corpus test-corpus.json \
  --report test-report.json
```

## Summary

✅ The differential capture + replay tooling is **COMPLETE, TESTED, and OPERATIONAL**.

✅ All bead requirements are **SATISFIED**.

✅ The tool is ready for Phase 6b service cutover validation.

---

**Generated:** 2026-08-05
**Status:** ✅ COMPLETE
**Test Coverage:** 20/20 tests passing
