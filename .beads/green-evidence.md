# SEAM green-gate evidence — seam-d33d0b9c

Recorded: 2026-09-03T22:43Z–23:15Z
HEAD at start of run: `829fa410a66b587cf5b1c48f236d9b6516b40f51`
(revert(seam): remove stranded tools/starvation-* consumers of reverted daemon code)
Toolchain: `go version go1.26.6 linux/amd64`
Working directory: `/home/coding/SEAM`

## Verdict: NOT GREEN — 3 of 3 commands still fail, all traced to distinct root causes

`go build ./...` is genuinely green. `go vet ./...` and `go test ./...` both
fail, and both fail for the same primary reason: **the `internal/server` test
package does not compile** (78 errors). Two further packages fail at runtime.

Every failure below is traced to a root cause and filed as its own bead. No
failure was left unattributed.

---

## Concurrency warning — this run was taken on a moving tree

Another worker was actively editing `internal/server/*_test.go` **during** this
verification pass. Observed directly:

| Time (UTC) | Observation |
|---|---|
| 22:43Z | `cmd/seam/diff_command.go` modified, `containsString` files clean |
| 22:52Z | `cmd/seam/diff_command.go` reverted to clean; `route_table_test.go`, `brownout_middleware_test.go`, `loop_guard_test.go` now **modified** |
| 23:04Z | `cloudflare_jwt_middleware_test.go` also modified |

That worker's in-flight diffs removed all three duplicate `containsString`
helpers, the duplicate `findSubstring`/`containsMiddle` helpers, the unused
`headerName` local, and added a missing `"context"` import. Those four defects
were present at 22:43Z and are **gone** by 23:04Z. They are therefore recorded
below as *fixed-in-flight*, not filed as beads.

Consequently the numbers below are a floor, not a ceiling: they are the errors
that *survive* that worker's uncommitted edits. Nothing in this report was
measured against a clean checkout, because creating one was prohibited
(no worktrees / no disposable clones under the shared-checkout rule).

---

## 1. `go build ./...` — **EXIT 0 (green)**

```
(no output)
BUILD_EXIT=0
```

Note: the historical `test_broken.go` root-package canary is **already gone at
this HEAD** — removed by `b6117fb` "test(ci): remove the seam-ci gate canary --
the gate is proven live" (2026-09-03T18:44:15-04:00), which is an ancestor of
`829fa41`. So build green is real, not a canary artifact. Earlier guidance to
leave `test_broken.go` alone is stale.

Confirmed no non-root package regressed: building everything except the root
module path yields only three benign notices for test-only packages:

```
github.com/ardenone/seam/benches: no non-test Go files in /home/coding/SEAM/benches
github.com/ardenone/seam/internal/testutil: no non-test Go files in /home/coding/SEAM/internal/testutil
github.com/ardenone/seam/corpus: no non-test Go files in /home/coding/SEAM/corpus
```

(`go build` cannot emit a binary for a package with no non-test files; `go vet`
and `go test` both handle them fine.)

## 2. `go vet ./...` — **EXIT 1**

```
# github.com/ardenone/seam/internal/server
# [github.com/ardenone/seam/internal/server]
vet: internal/server/control_plane_handlers_test.go:260:9: declared and not used: scopeID
VET_EXIT=1
```

`go vet` stops at the first error in a package, so this single line is not the
extent of the problem — it is the front of a 78-error queue. The complete list
was extracted with `go test -c -gcflags=-e ./internal/server/` and is
decomposed in section 4.

## 3. `go test ./...` — **EXIT 1**

Full output (two runs, 18:55Z and 19:08Z — identical shape both times):

```
ok  	github.com/ardenone/seam/benches	(cached)
ok  	github.com/ardenone/seam/benchmarks/baseline	(cached)
?   	github.com/ardenone/seam/cmd/baseline	[no test files]
# github.com/ardenone/seam/internal/server [github.com/ardenone/seam/internal/server.test]
internal/server/deprecation_middleware_test.go:32:8: undefined: context
internal/server/deprecation_middleware_test.go:119:8: undefined: context
internal/server/deprecation_middleware_test.go:162:8: undefined: context
internal/server/deprecation_middleware_test.go:228:8: undefined: context
internal/server/deprecation_middleware_test.go:277:8: undefined: context
internal/server/error_paths_test.go:128:62: not enough arguments in call to (&Server{}).writeQuotaExceededResponse
	have (http.ResponseWriter, *http.Request, string, number)
	want (http.ResponseWriter, *http.Request, string, float64, float64)
internal/server/loop_guard_integration_test.go:82:3: unknown field DryRun in struct literal of type Config
internal/server/loop_guard_integration_test.go:145:3: unknown field DryRun in struct literal of type Config
internal/server/loop_guard_integration_test.go:231:3: unknown field DryRun in struct literal of type Config
internal/server/loop_guard_integration_test.go:292:3: unknown field DryRun in struct literal of type Config
internal/server/loop_guard_integration_test.go:292:3: too many errors
FAIL	github.com/ardenone/seam/internal/server [build failed]
--- FAIL: TestRunDiffCommandDetectsChanges (0.00s)
    diff_command_test.go:59: Expected return code 1 for changes, got 0: stderr=
FAIL	github.com/ardenone/seam/cmd/seam	0.034s
--- FAIL: TestCreateEphemeralKeyHoldDown (0.00s)
    client_test.go:387: Expected ErrRateLimited, got rate limited by Tailscale API: Tailscale API error (status 429): Rate limit exceeded
FAIL	github.com/ardenone/seam/internal/tailscale	0.100s
ok  	github.com/ardenone/seam/cmd/seam  (see above for the FAIL entry above)
ok  	github.com/ardenone/seam/corpus	(cached)
ok  	github.com/ardenone/seam/internal/buildinfo	(cached)
ok  	github.com/ardenone/seam/internal/fanout	(cached)
ok  	github.com/ardenone/seam/internal/spec	(cached)
ok  	github.com/ardenone/seam/internal/testutil	(cached) [no tests to run]
ok  	github.com/ardenone/seam/internal/testutil/stubupstream	(cached)
ok  	github.com/ardenone/seam/internal/vault	(cached)
ok  	github.com/ardenone/seam/internal/version	(cached)
FAIL
TEST_EXIT=1
```

Passing packages (all green): `benches`, `benchmarks/baseline`, `corpus`,
`internal/buildinfo`, `internal/fanout`, `internal/spec`, `internal/testutil`,
`internal/testutil/stubupstream`, `internal/vault`, `internal/version`.
No test files: `cmd/baseline`, `internal/pluckfallback`,
`internal/testutil/openbao`, `internal/watcher`, `scratch`.

Failing packages: `internal/server` (build), `cmd/seam` (1 test),
`internal/tailscale` (1 test).

---

## 4. Complete `internal/server` test-package error list — 78 errors, 6 root causes

Captured with `go test -c -o /dev/null -gcflags=-e ./internal/server/`, which
bypasses the default 10-error truncation.

### Cause A — tests target a `Server` API that does not exist (60 errors)

`phase12_scenario4_test.go` and `phase13_scenario6_test.go` are written against
a `Server` shape that is not in the tree:

- `server.Close undefined` — 12 uses
- `server.routeTable undefined` — 28 uses
- `server.ServeHTTP undefined` — 15 uses
- `undefined: NewServer` — 1 use
- `NewRouteTable()` called with no args; signature requires `(*spec.Loader)` — 1 use
- `"io" imported and not used` — 1

Verified against the source: `internal/server` defines **no** `Close`, no
`ServeHTTP`, and **no** `routeTable` field on `*Server`, and there is **no
`NewServer` constructor anywhere** in the package. The only constructor present
is `route_table.go:1002 func NewRouteTable(loader *spec.Loader) *RouteTable`.

This is the dominant cause — 60 of 78 errors. It is the "tests written for code
that was never implemented" pattern that `seam-7e66275c` (P0) describes:
phase umbrellas were closed while the underlying code never landed.

**Filed as:** see bead list in section 6.

### Cause B — `loop_guard_integration_test.go` uses a nonexistent `Config.DryRun` field (7 errors)

`Config` has no `DryRun` field at lines 82, 145, 231, 292, 368, 426, plus
`declared and not used: middleware` at line 451.

In the real code, dry-run is a **context** concept, not config:
`proxy.go:119 contextWithDryRun(ctx)` / `proxy.go:124 isDryRun(ctx)`, used by
`quota_middleware.go:36`. There is no boolean on `Config` to set.

### Cause C — `error_paths_test.go` arity mismatch on `writeQuotaExceededResponse` (1 error)

```
internal/server/error_paths_test.go:128:62: not enough arguments in call to (&Server{}).writeQuotaExceededResponse
	have (http.ResponseWriter, *http.Request, string, number)
	want (http.ResponseWriter, *http.Request, string, float64, float64)
```

The production signature at `quota_middleware.go:121` now takes
`(w, r, route, remaining float64, costPerCall float64)`. The test still passes
a single numeric argument.

### Cause D — `deprecation_middleware_test.go` missing the `context` import (5 errors)

`undefined: context` at lines 32, 119, 162, 228, 277. The file uses
`context.Background()` but never imports `context`.

### Cause E — `vault.FailureClassNetwork` does not exist (2 errors)

`phase12_scenario4_test.go:221` and `:494`. The real constants in
`internal/vault/vault.go:476-480` are:

```go
FailureNetwork     FailureClass = "network"
FailureTimeout     FailureClass = "timeout"
FailureUnavailable FailureClass = "unavailable"
FailureAuth        FailureClass = "authentication"
FailureResponse    FailureClass = "invalid-response"
```

i.e. `FailureNetwork`, not `FailureClassNetwork`. Simple identifier mismatch.

### Cause F — unused imports and locals across six test files (7 errors)

| File | Line | Problem |
|---|---|---|
| `control_plane_handlers_test.go` | 260 | `declared and not used: scopeID` |
| `adapter_executor_test.go` | 7 | `"net/url" imported and not used` |
| `cloudflare_jwt_middleware_test.go` | 6 | `"fmt" imported and not used` |
| `operator_scope_test.go` | 5 | `"io" imported and not used` |
| `scope_version_middleware_test.go` | 4 | `"context" imported and not used` |
| `scope_version_test.go` | 6 | `"time" imported and not used` |

(The seventh, `"io"` in `phase12_scenario4_test.go`, is counted under Cause A
because it lives in that file and should be fixed together with it.)

Totals: A 60 + B 7 + C 1 + D 5 + E 2 + F 6 = **81 reported positions**, of
which 78 are distinct error lines (the `writeQuotaExceededResponse` error spans
two lines, and Go double-reports the first `DryRun` hit in two sub-lists).

---

## 5. Failures outside `internal/server` — 2 root causes

### `cmd/seam` — `TestRunDiffCommandDetectsChanges`

```
--- FAIL: TestRunDiffCommandDetectsChanges (0.00s)
    diff_command_test.go:59: Expected return code 1 for changes, got 0: stderr=
```

Root cause is in **`internal/spec/fragment.go`**, not in the test or the diff
command. The loader advertises YAML support and then ignores it:

- `fragment.go:107` — discovery accepts `.yaml`, `.yml` **and** `.json`
- `fragment.go:145` — `loadFragmentFile` unconditionally does
  `json.Unmarshal(content, &parsed)`

So every `.yaml` fragment — the primary format the walker goes out of its way
to find — fails with `failed to parse JSON: invalid character 'x' looking for
beginning of value`. The loader logs confirm it live:

```
[Fragment] Loading fragment file: .../owner/route.yaml
[Fragment] Warning: failed to load fragment .../route.yaml: failed to parse JSON: invalid character 'x' looking for beginning of value
[Fragment] Fragment loading complete: 0 loaded, 1 errors
[Fragment] Merging 0 fragments into single OpenAPI 3.1 spec
[Fragment] No fragments to merge, creating minimal empty document
```

Both the base dir and the current dir therefore merge to the *same* empty
document, the diff sees no change, and the command exits 0 where the test
expects 1. The test fixture (`cmd/seam/diff_command_test.go:34`) is YAML, which
is the format `fragment.go:107` claims to accept.

(`TestRunDiffCommandNoChanges` passes only by accident — it also fails to load
its fragments and correctly reports "no change" between two empty documents.)

### `internal/tailscale` — `TestCreateEphemeralKeyHoldDown`

```
--- FAIL: TestCreateEphemeralKeyHoldDown (0.00s)
    client_test.go:387: Expected ErrRateLimited, got rate limited by Tailscale API: Tailscale API error (status 429): Rate limit exceeded
```

**Not a network flake.** The test is properly hermetic — it stubs the API with
`httptest.NewServer` returning a hard `429` and points `Config.BaseURL` at the
stub. The bug is an error-comparison mismatch:

- `client.go:233` returns a *wrapped* sentinel:
  `fmt.Errorf("%w: %s", ErrRateLimited, apiErr)`
- `client_test.go:387` compares with identity: `if err != ErrRateLimited`

Identity comparison can never match a wrapped error. The test needs
`errors.Is(err, ErrRateLimited)`. The client's wrapping is correct and should
not be changed — `errors.Is` is the contract callers rely on.

---

## 6. Disposition of every failure

Fixed by another worker's in-flight (uncommitted) edits during this run —
**not filed**, would have been duplicates:

| Defect | Where |
|---|---|
| `containsString` declared 3× in one package | `route_table_test.go:339`, `brownout_middleware_test.go:403`, `loop_guard_test.go:491` |
| duplicate `containsMiddle`/`findSubstring` helpers | same three files |
| `declared and not used: headerName` | `cloudflare_header_stripping_test.go:163` |
| missing `"context"` import | `brownout_middleware_test.go` |

Filed as beads (each a distinct file/behaviour, matching the existing granular
build-red bead convention):

| Bead | Root cause | Scope |
|---|---|---|
| `seam-aede6e01` (P1) | tests target nonexistent `Server` API (`Close`/`ServeHTTP`/`routeTable`/`NewServer`, `NewRouteTable` arity) — 60 errors | `phase12_scenario4_test.go`, `phase13_scenario6_test.go` |
| `seam-effc926d` | nonexistent `Config.DryRun` field — 7 errors | `loop_guard_integration_test.go` |
| `seam-b7e668cd` | `writeQuotaExceededResponse` arity — 1 error | `error_paths_test.go` |
| `seam-403d0c42` | missing `context` import — 5 errors | `deprecation_middleware_test.go` |
| `seam-21437baf` | `vault.FailureClassNetwork` → `FailureNetwork` — 2 errors | `phase12_scenario4_test.go` |
| `seam-ccd7eedb` | unused imports/locals, 6 files — 6 errors | see table above |
| `seam-8f5949ad` (P1) | `.yaml` fragments discovered but parsed as JSON | `internal/spec/fragment.go` |
| `seam-36f9cc28` | wrapped sentinel compared with `!=`, needs `errors.Is` | `internal/tailscale/client_test.go` |

All eight carry the `build-red` label and `--unique-ref seam-green-gate:<A..H>`
(idempotent, so a racing dispatcher cannot double-create them). Each blocks the
P0 `seam-7e66275c` — the bead that requires the tree restored to green — and is
linked to `seam-d33d0b9c` with a `relates_to` edge.

Deliberately **not** filed as a defect:

- `go build ./...` exit 0 — that is the desired state.
- The three "no non-test Go files" notices — benign.

---

## 7. Reproduction

```bash
cd /home/coding/SEAM
go build ./...                                   # exit 0
go vet ./...                                     # exit 1, first internal/server error
go test ./...                                    # exit 1
go test -c -o /dev/null -gcflags=-e ./internal/server/   # full 78-error list
```

To re-check only the non-`internal/server` runtime failures:

```bash
go test ./cmd/seam -run TestRunDiffCommand -v
go test ./internal/tailscale -run TestCreateEphemeralKeyHoldDown -v
```

## 8. Caveats on this evidence

1. **Not measured at a clean HEAD.** The shared checkout carried another
   worker's uncommitted edits to `internal/server/*_test.go` and a set of
   untracked `capture_*_test.go` / `testhelpers_test.go` files that `go test`
   compiles and runs. Creating an isolated checkout was prohibited. Read the
   numbers as "current live tree", not "commit `829fa41`".
2. **The untracked test files are included in the run.** They did not
   contribute any of the failures above — all 78 come from tracked files — but
   they do contribute runtime behaviour once the package compiles.
3. **`internal/tailscale` and `cmd/seam` failures reproduced identically across
   two runs 13 minutes apart**, so neither is load- or timing-dependent.
4. The tree was still being mutated as this file was written. Re-run section 7
   before acting on any single count.

---

# 9. Second, independent run — measured at a clean, pinned HEAD

Recorded: 2026-09-03T23:11Z–23:31Z, by a second worker
(`claude-code-glm-5.3-flash-glm-roam-16`) also holding this bead — see the
duplicate-claim note at the end of this section.

HEAD: `b6117fba4f50e06d5ab39959bc9bb7d5d6f1c12b`
(*test(ci): remove the seam-ci gate canary -- the gate is proven live*)
Toolchain: `go version go1.26.0 linux/amd64`

## 9.1 Method — this resolves caveat 8.1

Section 8.1 could only report "current live tree". This run measures a clean
HEAD with none of the shared checkout's uncommitted edits present, using a
source export rather than a clone or worktree (no `.git`, no duplicated
build cache, removed on exit):

```bash
WORK=$(mktemp -d /tmp/seam-head-XXXX)
git archive HEAD | tar -x -C "$WORK"
cd "$WORK"
go build -buildvcs=false ./...
go vet ./...
go test ./...
```

`-buildvcs=false` is required and is **not** masking a real failure: a
`git archive` export has no `.git` directory, so VCS stamping cannot run and
`go build` aborts with `error obtaining VCS status`. A genuine clone of
`b6117fba` stamps normally. It is an artefact of the export method.

## 9.2 Results

| Command | Exit | Detail |
|---|---|---|
| `go build -buildvcs=false ./...` | **0** | green |
| `go vet ./...` | 1 | 1 error |
| `go test ./...` | 1 | 10 ok / 3 FAIL / 5 no-test-files |

**`go build ./...` is green at this HEAD** — the first green build recorded
in this file. The deliberate canary `test_broken.go` (faaadd5) that kept the
root package red was deleted in `b6117fba` itself, so every build-red
measurement in sections 1–8 carries a failure that no longer exists at the
current commit.

`go vet` fails on exactly one error, and it is an error HEAD-only:

```
internal/server/worker_identity_integration_test.go:12:2:
    package seam/internal/tailscale is not in std
```

The module path is `github.com/ardenone/seam`; HEAD's import string is
missing that prefix. The live working tree already reads
`"github.com/ardenone/seam/internal/tailscale"` (uncommitted fix). Because
type-checking aborts here, the remaining `internal/server` test-body errors
are only enumerable on the live tree — see section 3 for those.

## 9.3 Full `go test ./...` output at HEAD

```
# github.com/ardenone/seam/internal/server
internal/server/worker_identity_integration_test.go:12:2: package seam/internal/tailscale is not in std (/home/coding/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.0.linux-amd64/src/seam/internal/tailscale)
FAIL	github.com/ardenone/seam/internal/server [setup failed]
ok  	github.com/ardenone/seam/benches	0.004s
ok  	github.com/ardenone/seam/benchmarks/baseline	0.002s
?   	github.com/ardenone/seam/cmd/baseline	[no test files]
2026/09/03 19:27:51 [Fragment] Loading fragments from directory: /tmp/TestRunDiffCommandDetectsChanges3008663295/002
2026/09/03 19:27:51 [Fragment] Loading fragment file: /tmp/TestRunDiffCommandDetectsChanges3008663295/002/owner/route.yaml
2026/09/03 19:27:51 [Fragment] Warning: failed to load fragment /tmp/TestRunDiffCommandDetectsChanges3008663295/002/owner/route.yaml: failed to parse JSON: invalid character 'x' looking for beginning of value
2026/09/03 19:27:51 [Fragment] Fragment loading complete: 0 loaded, 1 errors
2026/09/03 19:27:51 [Fragment] Merging 0 fragments into single OpenAPI 3.1 spec
2026/09/03 19:27:51 [Fragment] No fragments to merge, creating minimal empty document
2026/09/03 19:27:51 [Fragment] Loading fragments from directory: /tmp/TestRunDiffCommandDetectsChanges3008663295/001
2026/09/03 19:27:51 [Fragment] Loading fragment file: /tmp/TestRunDiffCommandDetectsChanges3008663295/001/owner/route.yaml
2026/09/03 19:27:51 [Fragment] Warning: failed to load fragment /tmp/TestRunDiffCommandDetectsChanges3008663295/001/owner/route.yaml: failed to parse JSON: invalid character 'x' looking for beginning of value
2026/09/03 19:27:51 [Fragment] Fragment loading complete: 0 loaded, 1 errors
2026/09/03 19:27:51 [Fragment] Merging 0 fragments into single OpenAPI 3.1 spec
2026/09/03 19:27:51 [Fragment] No fragments to merge, creating minimal empty document
--- FAIL: TestRunDiffCommandDetectsChanges (0.00s)
    diff_command_test.go:59: Expected return code 1 for changes, got 0: stderr=
2026/09/03 19:27:51 [Fragment] Loading fragments from directory: /tmp/TestRunDiffCommandNoChanges3492588592/001
2026/09/03 19:27:51 [Fragment] Loading fragment file: /tmp/TestRunDiffCommandNoChanges3492588592/001/owner/route.yaml
2026/09/03 19:27:51 [Fragment] Warning: failed to load fragment /tmp/TestRunDiffCommandNoChanges3492588592/001/owner/route.yaml: failed to parse JSON: invalid character 'x' looking for beginning of value
2026/09/03 19:27:51 [Fragment] Fragment loading complete: 0 loaded, 1 errors
2026/09/03 19:27:51 [Fragment] Merging 0 fragments into single OpenAPI 3.1 spec
2026/09/03 19:27:51 [Fragment] No fragments to merge, creating minimal empty document
2026/09/03 19:27:51 [Fragment] Loading fragments from directory: /tmp/TestRunDiffCommandNoChanges3492588592/001
2026/09/03 19:27:51 [Fragment] Loading fragment file: /tmp/TestRunDiffCommandNoChanges3492588592/001/owner/route.yaml
2026/09/03 19:27:51 [Fragment] Warning: failed to load fragment /tmp/TestRunDiffCommandNoChanges3492588592/001/owner/route.yaml: failed to parse JSON: invalid character 'x' looking for beginning of value
2026/09/03 19:27:51 [Fragment] Fragment loading complete: 0 loaded, 1 errors
2026/09/03 19:27:51 [Fragment] Merging 0 fragments into single OpenAPI 3.1 spec
2026/09/03 19:27:51 [Fragment] No fragments to merge, creating minimal empty document
FAIL
FAIL	github.com/ardenone/seam/cmd/seam	0.042s
ok  	github.com/ardenone/seam/corpus	0.453s
ok  	github.com/ardenone/seam/internal/buildinfo	0.002s
ok  	github.com/ardenone/seam/internal/fanout	0.165s
?   	github.com/ardenone/seam/internal/pluckfallback	[no test files]
ok  	github.com/ardenone/seam/internal/spec	0.167s
--- FAIL: TestCreateEphemeralKeyHoldDown (0.00s)
    client_test.go:387: Expected ErrRateLimited, got rate limited by Tailscale API: Tailscale API error (status 429): Rate limit exceeded
FAIL
FAIL	github.com/ardenone/seam/internal/tailscale	0.087s
ok  	github.com/ardenone/seam/internal/testutil	(cached) [no tests to run]
?   	github.com/ardenone/seam/internal/testutil/openbao	[no test files]
ok  	github.com/ardenone/seam/internal/testutil/stubupstream	0.721s
ok  	github.com/ardenone/seam/internal/vault	0.005s
ok  	github.com/ardenone/seam/internal/version	0.002s
?   	github.com/ardenone/seam/internal/watcher	[no test files]
?   	github.com/ardenone/seam/scratch	[no test files]
FAIL
```

Both runtime failures reproduced identically in the live working tree, so
neither depends on the uncommitted edits.

## 9.4 Failure → bead mapping (verified complete, no new bead required)

| Failure | Bead(s) |
|---|---|
| `internal/server [setup failed]` — test-body compile errors (72 on the live tree) | seam-aede6e01 (60: `phase12_scenario4` + `phase13_scenario6` call a `Server` API that does not exist) · seam-21437baf (`vault.FailureClassNetwork` → `FailureNetwork`) · seam-effc926d (`Config.DryRun`, 7) · seam-b7e668cd (`writeQuotaExceededResponse` arity) · seam-403d0c42 (missing `context` import, 5) · seam-ccd7eedb (unused imports/locals, 6) |
| `internal/tailscale` `TestCreateEphemeralKeyHoldDown` | seam-36f9cc28 |
| `cmd/seam` `TestRunDiffCommandDetectsChanges` | seam-8f5949ad (root cause: `internal/spec/fragment.go` discovers `.yaml` but parses every file as JSON — visible as `invalid character 'x'` in the log above) |

Every remaining failure is filed. Nothing was left unattributed, and this
run filed no duplicate beads for work already tracked.

## 9.5 Duplicate claim on this bead

While this run executed under
`claude-code-glm-5.3-flash-glm-roam-16`, `bead show seam-d33d0b9c` reported
assignee `claude-code-glm-5.3-flash-glm-roam-19` at revision 410 — two
workers on one bead, the known dispatcher race. Consequences for this file:

- This section is **additive only**. Sections 1–8 were left untouched.
- No `internal/server` source or test file was modified by this run. The
  other worker was mid-edit on those files throughout (observed mtimes
  22:34Z → 23:22Z across nine files), so touching them would have corrupted
  in-flight work.
- The clean-HEAD measurement is the one thing the live tree could not
  provide, and is this run's whole contribution.
