# Green evidence — gate run 6 (seam-56744617, terminal child of seam-d33d0b9c)

**Result: GREEN. All three commands exit 0.**

| Field | Value |
|---|---|
| HEAD sha (full) | `04b94dd497c45573c11feee1507d5ebe78b29a32` |
| HEAD sha (short) | `04b94dd` |
| Measured at (UTC) | 2026-09-04T23:42:59Z |
| Toolchain | go version go1.26.6 linux/amd64 |
| `go build ./...` exit | **0** |
| `go vet ./...` exit | **0** |
| `go test ./...` exit | **0** |
| FAIL lines in test output | **0** (`grep -c 'FAIL'` over the raw log = 0; `--- FAIL` = 0; panics = 0) |
| Packages ok | 13 |
| Packages with no test files | 5 |

## Method

Measured in a **clean extract of HEAD**, never the working tree: the shared
checkout had 117 dirty paths from concurrent workers at measurement time.

```
REV=$(git rev-parse HEAD)            # 04b94dd497c45573c11feee1507d5ebe78b29a32
EX=$(mktemp -d /tmp/seam-56744617-XXXXXX)
git archive "$REV" | tar -x -C "$EX"
cd "$EX"
go build -buildvcs=false ./...
go vet ./...
go test  -buildvcs=false ./...
```

Export and measurement ran in a single shell invocation with a trap removing
the extract afterward. `-buildvcs=false` is passed to `build` and `test` (the
extract has no `.git`, so VCS stamping would otherwise error); `go vet` does
not accept that flag, so vet ran without it — recorded here so the invocation
is reproducible. The shared `go build` cache was warm but the test run was
real, not cached: `internal/server` — the package carrying all 46 of run 5's
stable failures — executed for **89.767s** and reported `ok`. Only
`internal/testutil` (which has no tests to run) shows `(cached)`.

## Full output

```
HEAD_SHA=04b94dd497c45573c11feee1507d5ebe78b29a32
extract_dir=/tmp/seam-56744617-8NRCIC
measured_at_utc=2026-09-04T23:42:59Z
toolchain=go version go1.26.6 linux/amd64

===== go build -buildvcs=false ./... =====
BUILD_EXIT=0

===== go vet ./... (no -buildvcs: vet does not accept it) =====
VET_EXIT=0

===== go test -buildvcs=false ./... =====
ok  	github.com/ardenone/seam/benches	0.005s
ok  	github.com/ardenone/seam/benchmarks/baseline	0.002s
?   	github.com/ardenone/seam/cmd/baseline	[no test files]
ok  	github.com/ardenone/seam/cmd/seam	0.044s
ok  	github.com/ardenone/seam/corpus	0.485s
ok  	github.com/ardenone/seam/internal/buildinfo	0.001s
ok  	github.com/ardenone/seam/internal/fanout	0.165s
?   	github.com/ardenone/seam/internal/pluckfallback	[no test files]
ok  	github.com/ardenone/seam/internal/server	89.767s
ok  	github.com/ardenone/seam/internal/spec	0.241s
ok  	github.com/ardenone/seam/internal/tailscale	0.089s
ok  	github.com/ardenone/seam/internal/testutil	(cached) [no tests to run]
?   	github.com/ardenone/seam/internal/testutil/openbao	[no test files]
ok  	github.com/ardenone/seam/internal/testutil/stubupstream	0.743s
ok  	github.com/ardenone/seam/internal/vault	0.004s
ok  	github.com/ardenone/seam/internal/version	0.002s
?   	github.com/ardenone/seam/internal/watcher	[no test files]
?   	github.com/ardenone/seam/scratch	[no test files]
TEST_EXIT=0
```

## Load-sensitive tests confirmed by name

Run 5 flagged two tests as load-flaky rather than fixed, expecting them to
reappear under load. Both were re-run at the same pinned rev in a second
clean extract, `-count=1 -v`:

```
go test -count=1 -v -buildvcs=false \
  -run 'TestArgoCDProxyBaselineResponseTimes|TestProxyCaptureLatencyByPayloadSize' \
  ./internal/server/
```

```
--- PASS: TestArgoCDProxyBaselineResponseTimes (1.16s)
      (subtests: healthz, readyz, openapi, docs)
--- PASS: TestProxyCaptureLatencyByPayloadSize (0.18s)
      (subtests: small, medium, large)
PASS
ok  	github.com/ardenone/seam/internal/server	1.368s
```

Exit 0, zero FAIL lines in that log as well.

## How run 5's 46 stable failures closed

Run 5 (2026-09-04T15:53Z, at f5962b7) measured 46 runtime failures, all in
`internal/server`, with complete per-bead accounting. Every bead in that
chain is now closed with its fix landed at or before this HEAD:

| Bead | Failures then | Fix commit(s) | Status |
|---|---|---|---|
| seam-e2549287 quota + cost-per-call `$0.00` | 11 | 0787563 | closed |
| seam-a117a092 loop-guard window regex + JSON hash | 5 | 5b03b45 | closed |
| seam-71e6eec3 stage-3 identity / docs / capture (split into 5 children) | 23 | 51f446b, b10d559, 5b18455, 04b94dd | closed rev 19 |
| seam-1a440bb7 taxonomy vs OpenAPI + LRU eviction order | 3 | 42b17c1 | closed rev 16 |
| seam-f2c119f2 loop-guard middleware wiring | 4 | 2c07541 | closed rev 9 |

11 + 5 + 23 + 3 + 4 = 46 — nothing unaccounted for, and the two load-flaky
tests are now confirmed passing by name above.

`.beads/green-evidence.md` was deliberately **not** touched (contended by
concurrent runs); this file is the sha-suffixed evidence for run 6.
