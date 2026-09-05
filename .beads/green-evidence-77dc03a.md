# Green evidence — gate run 7 (seam-56744617, terminal child of seam-d33d0b9c)

**Result: GREEN. All three commands exit 0 at `77dc03a`.**

| Field | Value |
|---|---|
| HEAD sha (full) | `77dc03ab5dab8304ba54c5cc10fee0e87b9f31d7` |
| HEAD sha (short) | `77dc03a` |
| Measured at (UTC) | 2026-09-05T00:06:32Z |
| Toolchain | go version go1.26.6 linux/amd64 |
| `go build ./...` exit | **0** |
| `go vet ./...` exit | **0** |
| `go test ./...` exit | **0** |
| FAIL lines in test output | **0** (`grep -c 'FAIL'` = 0; `--- FAIL` = 0; `panic` = 0) |
| Packages ok | 13 |
| Packages with no test files | 5 |

## Why a run 7 exists

This is a re-verification of run 6, not a new gate attempt. Run 6 closed this
child and the parent gate green at `04b94dd` (rev 6 / rev 423); the loop
verifier subsequently re-opened this child with `build-red`,
`verification-failed` and `failure-count:1`. Its verification runs in the
shared checkout, which had **117 dirty paths from concurrent workers** at the
time of this measurement — the strike is an artefact of measuring the dirty
tree, and the parent gate remains closed.

Between `04b94dd` (run 6's rev) and this HEAD the only commit is `77dc03a`
itself — the run-6 evidence commit, which touches `.beads/` and docs and no
Go source. Package inputs are unchanged, so the green result is expected to
carry over; it was re-measured from scratch anyway.

## Method

Measured in a **clean extract of HEAD**, never the working tree. Export and
measurement ran in a single shell invocation with a trap removing the extract
afterward.

```
REV=$(git rev-parse HEAD)            # 77dc03ab5dab8304ba54c5cc10fee0e87b9f31d7
EX=$(mktemp -d /tmp/seam-56744617-run7-77dc03a-XXXXXX)
git archive "$REV" | tar -x -C "$EX"
cd "$EX"
go build -buildvcs=false ./...
go vet ./...
go test -count=1 -buildvcs=false ./...
```

`-buildvcs=false` is passed to `build` and `test` (the extract has no `.git`,
so VCS stamping would otherwise error); `go vet` does not accept that flag, so
vet ran without it — recorded here so the invocation is reproducible.

**`-count=1` is new relative to run 6** and is the point of this run: run 6's
`go test` was allowed to use the result cache, this one was not. Every package
genuinely executed at this HEAD — `internal/server`, the package carrying all
46 of run 5's stable failures, ran for **89.278s** and reported `ok`, and no
line in the output carries a `(cached)` marker. A pass here cannot be a
replay of run 6's results.

## Full output

```
HEAD_SHA=77dc03ab5dab8304ba54c5cc10fee0e87b9f31d7
extract_dir=/tmp/seam-56744617-run7-77dc03a-nZJmFU
measured_at_utc=2026-09-05T00:06:32Z
toolchain=go version go1.26.6 linux/amd64
dirty_paths_in_shared_checkout=117

===== go build -buildvcs=false ./... =====
BUILD_EXIT=0

===== go vet ./... (no -buildvcs: vet does not accept it) =====
VET_EXIT=0

===== go test -count=1 -buildvcs=false ./... =====
ok  	github.com/ardenone/seam/benches	0.004s
ok  	github.com/ardenone/seam/benchmarks/baseline	0.002s
?   	github.com/ardenone/seam/cmd/baseline	[no test files]
ok  	github.com/ardenone/seam/cmd/seam	0.034s
ok  	github.com/ardenone/seam/corpus	0.472s
ok  	github.com/ardenone/seam/internal/buildinfo	0.001s
ok  	github.com/ardenone/seam/internal/fanout	0.164s
?   	github.com/ardenone/seam/internal/pluckfallback	[no test files]
ok  	github.com/ardenone/seam/internal/server	89.278s
ok  	github.com/ardenone/seam/internal/spec	0.209s
ok  	github.com/ardenone/seam/internal/tailscale	0.093s
ok  	github.com/ardenone/seam/internal/testutil	0.002s [no tests to run]
?   	github.com/ardenone/seam/internal/testutil/openbao	[no test files]
ok  	github.com/ardenone/seam/internal/testutil/stubupstream	0.731s
ok  	github.com/ardenone/seam/internal/vault	0.006s
ok  	github.com/ardenone/seam/internal/version	0.002s
?   	github.com/ardenone/seam/internal/watcher	[no test files]
?   	github.com/ardenone/seam/scratch	[no test files]
TEST_EXIT=0
```

## Accounting

All five beads in the run-5 failure chain remain closed with their fixes at or
before this HEAD (per `.beads/green-evidence-04b94dd.md`): seam-e2549287 →
`0787563`, seam-a117a092 → `5b03b45`, seam-71e6eec3 → `51f446b`/`b10d559`/
`5b18455`/`04b94dd`, seam-1a440bb7 → `42b17c1`, seam-f2c119f2 → `2c07541`.
The two tests run 5 flagged as load-flaky
(`TestArgoCDProxyBaselineResponseTimes`, `TestProxyCaptureLatencyByPayloadSize`)
live in `internal/server`, which re-executed for real under `-count=1` above —
both passed in that run; run 6 additionally confirmed them by name in isolation.

`.beads/green-evidence.md` was deliberately **not** touched (contended by
concurrent runs); this file is the sha-suffixed evidence for run 7.
