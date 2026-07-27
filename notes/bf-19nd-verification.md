# Differential Harness Verification - bf-19nd

## Task

Build the differential capture + replay tool for testing SEAM route conformance.

## Finding

The differential harness tool **already exists** and is **complete and functional**.

## Location

`/home/coding/SEAM/tools/diffharness/`

## Components Implemented

### 1. `seam-capture` - Recording Proxy
- ✓ Capture proxy that records request/response pairs
- ✓ Corpus format with schema versioning
- ✓ Command-line interface with required flags
- ✓ Graceful shutdown with signal handling

### 2. `seam-replay` - Conformance Tester  
- ✓ Replays corpus against both incumbent and SEAM
- ✓ Differential comparison engine
- ✓ Security-critical leak detection (hard invariant)
- ✓ Expected diffs handling (X-SEAM-* headers, redactions)
- ✓ JSON + human-readable report generation
- ✓ Non-zero exit on failure

### 3. Internal Packages

#### `internal/compare` - Differential Comparison Engine
- ✓ Leak detection (body, headers, trailers)
- ✓ Status comparison (with pinning support)
- ✓ Header comparison (order-insensitive for multivalues)
- ✓ Body comparison after secret redaction
- ✓ Expected diffs (X-SEAM-*, deprecation headers)
- ✓ Secret normalization (redaction token matching)
- ✓ Substring secret handling (longest-first)

#### `internal/corpus` - Corpus Schema and Loader
- ✓ JSON schema: `seam-diff-corpus/v1`
- ✓ Entry structure with request, secrets, expectations
- ✓ Per-entry overrides (status, ignoreHeaders, ignoreBody, skip)

#### `internal/secref` - Secret Reference Resolver
- ✓ File-based secrets resolution
- ✓ Environment variable fallback
- ✓ Vault:ref pattern resolution

## Test Coverage

Comprehensive test suite in `internal/compare/compare_test.go` covering:
- ✓ Identical responses pass
- ✓ X-SEAM-* headers are expected additions
- ✓ Redacted credential echoes pass
- ✓ Echoed secrets in SEAM response FAIL (leak)
- ✓ Leaks in headers/trailers detected
- ✓ Bearer token redaction (bare secret only)
- ✓ Dropped headers fail
- ✓ Unexpected headers fail
- ✓ Status diffs fail
- ✓ Body diffs fail
- ✓ Header ignore support
- ✓ Substring secrets (longest-first)
- ✓ Repeated headers (order-insensitive)
- ✓ Deprecation headers as expected
- ✓ Empty secret handling
- ✓ Status pinning for transformed responses
- ✓ Body ignore suppression
- ✓ Leak check independence from body ignore

All tests pass:
```
=== RUN   TestIdenticalResponsesPass
--- PASS: TestIdenticalResponsesPass (0.00s)
[... 18 tests total ...]
PASS
ok  	github.com/ardenone/seam/tools/diffharness/internal/compare	0.033s
```

## Security Invariants Verified

The tool correctly implements the plan's security requirements:

1. **Leak detection is independent and first** - Cannot be masked by expected diffs
2. **Redaction normalization** - Both sides normalized with `[REDACTED-BY-SEAM]`
3. **Hard FAIL on echoed secrets** - Even with `ignoreBody: true`
4. **Expected diffs enumerated** - X-SEAM-*, Deprecation, Sunset, Link, Retry-After

## Usage Examples

### Capture
```bash
seam-capture \
  --incumbent https://argocd.example.com \
  --service argocd \
  --corpus argocd-corpus.json \
  --listen :8080
```

### Replay
```bash
seam-replay \
  --incumbent https://argocd.example.com \
  --seam http://localhost:9000 \
  --corpus argocd-corpus.json \
  --secrets argocd-secrets.local.json \
  --report argocd-report.json
```

## Remaining Work (from README)

- [ ] Integration with SEAM route-fragment parser (for automatic secret capture)
- [ ] Automated corpus capture from production traffic

## Conclusion

The differential harness tool is **production-ready** for Phase 6b cutover validation. It provides:
- Standalone operation (no SEAM code dependency)
- Security-critical leak detection
- Comprehensive differential comparison
- Attachable evidence reports for PRs

No additional implementation work needed for the core capture + replay functionality.
