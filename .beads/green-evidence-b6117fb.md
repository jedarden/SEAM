# SEAM green-gate evidence — seam-d33d0b9c — measured at a clean HEAD

Recorded: 2026-09-03T23:27Z–23:29Z
Commit measured: `b6117fba4f50e06d5ab39959bc9bb7d5d6f1c12b`
(test(ci): remove the seam-ci gate canary -- the gate is proven live)
Toolchain: `go version go1.26.6 linux/amd64`
Host: hetzner-ex44, 14 CPUs
Verified by: NEEDLE worker dispatch `seam-d33d0b9c` (claude-code-glm-5.3-flash, glm-coned)

## Why this run is different from the other evidence file in this bead

`.beads/green-evidence.md` records a pass taken on the **shared working tree**
while another worker was editing `internal/server/*_test.go` mid-run; that
report says so itself and calls its own numbers "a floor, not a ceiling".

This run was **not** taken on the working tree. HEAD was materialized with
`git archive HEAD | tar -x` into a throwaway directory — no `git worktree`, no
clone, no `.git`, removed after the run — so every byte compiled below is
exactly commit `b6117fb`, unpolluted by the five concurrent dispatches of this
same bead that were active during the window (started 22:40Z, 22:46Z, 23:03Z,
23:12Z and 23:17Z UTC).

## Verdict: NOT GREEN — build passes, vet and test fail

| Command | Exit | Failing packages |
|---|---|---|
| `go build ./...` | **0** | — (green; `go build` does not compile `_test.go` files) |
| `go vet ./...` | **1** | `internal/server` (test package does not compile) |
| `go test ./... -count=1` | **1** | `internal/server` (setup failed), `cmd/seam`, `internal/tailscale` |

Three root causes, detailed below. Two of them are **new findings** — they are
not covered by any of the four beads this bead filed on 2026-09-01, all four of
which are now Closed.

---

## 1. `go build ./...` — EXIT 0

Full output, verbatim:

```
$ go build ./...
BUILD_EXIT=0
```

Green, and genuinely so: the historical `test_broken.go` root-package canary is
gone at this commit (removed by `b6117fb` itself). `go build` does not compile
test files, so this says nothing about the test packages — see below.

## 2. `go vet ./...` — EXIT 1

Full output, verbatim:

```
$ go vet ./...
internal/server/worker_identity_integration_test.go:12:2: package seam/internal/tailscale is not in std (/nix/store/3yh8g6m3balbhzxsx77jlyyx443bxqii-go-1.26.6/share/go/src/seam/internal/tailscale)
VET_EXIT=1
```

One error, and it is the *first* defect in a package that has many: vet stops
loading `internal/server` at the unresolvable import and never type-checks the
rest of the package. The go.mod module path is `github.com/ardenone/seam`; the
file imports `seam/internal/tailscale`.

## 3. `go test ./... -count=1` — EXIT 1

Full output (53 lines, verbatim, unedited):

```
$ go test ./... -count=1
# github.com/ardenone/seam/internal/server
internal/server/worker_identity_integration_test.go:12:2: package seam/internal/tailscale is not in std (/nix/store/3yh8g6m3balbhzxsx77jlyyx443bxqii-go-1.26.6/share/go/src/seam/internal/tailscale)
FAIL	github.com/ardenone/seam/internal/server [setup failed]
ok  	github.com/ardenone/seam/benches	0.004s
ok  	github.com/ardenone/seam/benchmarks/baseline	0.002s
?   	github.com/ardenone/seam/cmd/baseline	[no test files]
2026/09/03 19:27:42 [Fragment] Loading fragments from directory: /tmp/TestRunDiffCommandDetectsChanges1775077499/002
2026/09/03 19:27:42 [Fragment] Loading fragment file: /tmp/TestRunDiffCommandDetectsChanges1775077499/002/owner/route.yaml
2026/09/03 19:27:42 [Fragment] Warning: failed to load fragment /tmp/TestRunDiffCommandDetectsChanges1775077499/002/owner/route.yaml: failed to parse JSON: invalid character 'x' looking for beginning of value
2026/09/03 19:27:42 [Fragment] Fragment loading complete: 0 loaded, 1 errors
2026/09/03 19:27:42 [Fragment] Merging 0 fragments into single OpenAPI 3.1 spec
2026/09/03 19:27:42 [Fragment] No fragments to merge, creating minimal empty document
2026/09/03 19:27:42 [Fragment] Loading fragments from directory: /tmp/TestRunDiffCommandDetectsChanges1775077499/001
2026/09/03 19:27:42 [Fragment] Loading fragment file: /tmp/TestRunDiffCommandDetectsChanges1775077499/001/owner/route.yaml
2026/09/03 19:27:42 [Fragment] Warning: failed to load fragment /tmp/TestRunDiffCommandDetectsChanges1775077499/001/owner/route.yaml: failed to parse JSON: invalid character 'x' looking for beginning of value
2026/09/03 19:27:42 [Fragment] Fragment loading complete: 0 loaded, 1 errors
2026/09/03 19:27:42 [Fragment] Merging 0 fragments into single OpenAPI 3.1 spec
2026/09/03 19:27:42 [Fragment] No fragments to merge, creating minimal empty document
--- FAIL: TestRunDiffCommandDetectsChanges (0.00s)
    diff_command_test.go:59: Expected return code 1 for changes, got 0: stderr=
2026/09/03 19:27:42 [Fragment] Loading fragments from directory: /tmp/TestRunDiffCommandNoChanges1900761234/001
2026/09/03 19:27:42 [Fragment] Loading fragment file: /tmp/TestRunDiffCommandNoChanges1900761234/001/owner/route.yaml
2026/09/03 19:27:42 [Fragment] Warning: failed to load fragment /tmp/TestRunDiffCommandNoChanges1900761234/001/owner/route.yaml: failed to parse JSON: invalid character 'x' looking for beginning of value
2026/09/03 19:27:42 [Fragment] Fragment loading complete: 0 loaded, 1 errors
2026/09/03 19:27:42 [Fragment] Merging 0 fragments into single OpenAPI 3.1 spec
2026/09/03 19:27:42 [Fragment] No fragments to merge, creating minimal empty document
2026/09/03 19:27:42 [Fragment] Loading fragments from directory: /tmp/TestRunDiffCommandNoChanges1900761234/001
2026/09/03 19:27:42 [Fragment] Loading fragment file: /tmp/TestRunDiffCommandNoChanges1900761234/001/owner/route.yaml
2026/09/03 19:27:42 [Fragment] Warning: failed to load fragment /tmp/TestRunDiffCommandNoChanges1900761234/001/owner/route.yaml: failed to parse JSON: invalid character 'x' looking for beginning of value
2026/09/03 19:27:42 [Fragment] Fragment loading complete: 0 loaded, 1 errors
2026/09/03 19:27:42 [Fragment] Merging 0 fragments into single OpenAPI 3.1 spec
2026/09/03 19:27:42 [Fragment] No fragments to merge, creating minimal empty document
FAIL
FAIL	github.com/ardenone/seam/cmd/seam	0.068s
ok  	github.com/ardenone/seam/corpus	0.569s
ok  	github.com/ardenone/seam/internal/buildinfo	0.007s
ok  	github.com/ardenone/seam/internal/fanout	0.167s
?   	github.com/ardenone/seam/internal/pluckfallback	[no test files]
ok  	github.com/ardenone/seam/internal/spec	0.175s
--- FAIL: TestCreateEphemeralKeyHoldDown (0.00s)
    client_test.go:387: Expected ErrRateLimited, got rate limited by Tailscale API: Tailscale API error (status 429): Rate limit exceeded
FAIL
FAIL	github.com/ardenone/seam/internal/tailscale	0.090s
ok  	github.com/ardenone/seam/internal/testutil	0.002s [no tests to run]
?   	github.com/ardenone/seam/internal/testutil/openbao	[no test files]
ok  	github.com/ardenone/seam/internal/testutil/stubupstream	0.719s
ok  	github.com/ardenone/seam/internal/vault	0.006s
ok  	github.com/ardenone/seam/internal/version	0.002s
?   	github.com/ardenone/seam/internal/watcher	[no test files]
?   	github.com/ardenone/seam/scratch	[no test files]
FAIL
TEST_EXIT=1
```

13 of 16 packages with tests pass. The three that fail are unrelated to each
other.

---

## Root cause A — `internal/server` test package does not compile

The `[setup failed]` above is one error out of many. Compiling the test binary
directly enumerates them (`go test -c -o /dev/null ./internal/server/`; the
last line is the compiler giving up, not the end of the list):

```
internal/server/loop_guard_test.go:491:6: containsString redeclared in this block
	internal/server/brownout_middleware_test.go:403:6: other declaration of containsString
internal/server/route_table_test.go:339:6: containsString redeclared in this block
	internal/server/brownout_middleware_test.go:403:6: other declaration of containsString
internal/server/route_table_test.go:345:6: containsMiddle redeclared in this block
	internal/server/loop_guard_test.go:495:6: other declaration of containsMiddle
internal/server/brownout_middleware_test.go:37:8: undefined: context
internal/server/brownout_middleware_test.go:98:8: undefined: context
internal/server/brownout_middleware_test.go:173:8: undefined: context
internal/server/brownout_middleware_test.go:238:8: undefined: context
internal/server/brownout_middleware_test.go:360:8: undefined: context
internal/server/cloudflare_header_stripping_test.go:163:8: declared and not used: headerName
internal/server/cloudflare_jwt_middleware_test.go:383:3: unknown field email in struct literal of type CloudflareAccessClaims, but does have Email
internal/server/cloudflare_jwt_middleware_test.go:383:3: too many errors
```

Distinct defects, grouped:

1. `worker_identity_integration_test.go:12` imports `seam/internal/tailscale`;
   the module path is `github.com/ardenone/seam`, so the package cannot resolve.
2. `containsString` is declared three times (loop_guard_test.go:491,
   brownout_middleware_test.go:403, route_table_test.go:339) and
   `containsMiddle` twice (loop_guard_test.go:495, route_table_test.go:345) —
   copy-pasted helpers that were never deduplicated.
3. `brownout_middleware_test.go` and `deprecation_middleware_test.go` use
   `context.` without importing `context`.
4. `cloudflare_header_stripping_test.go:163` declares `headerName` in a range
   statement and never uses it.
5. `cloudflare_jwt_middleware_test.go` sets and reads an `email` field;
   `CloudflareAccessClaims` (cloudflare_jwt_middleware.go:94) has `Email`, and
   the compiler truncates the remaining errors here.

**The fix for every one of these already exists — as uncommitted working-tree
edits.** `git diff` at the time of this run shows eight `internal/server`
`*_test.go` files carrying exactly the corrections above: the import path, the
three `containsString`/`containsMiddle` copies deleted, both `context` imports
added, `headerName` replaced by `for range`, and `email` -> `Email`. Bead
`seam-80488ddd` ("Fix internal/server compilation errors (9 failures)", P0) was
Closed with that work still sitting in the working tree, so at HEAD nothing has
landed and the package is exactly as broken as it was on 2026-08-30.

## Root cause B — `cmd/seam` `TestRunDiffCommandDetectsChanges`

The fragment loader collects `.yaml` and `.yml` files but parses every one of
them as JSON. `internal/spec/fragment.go:146` calls `json.Unmarshal` on content
that its own struct comment (line 28) calls "Original YAML/JSON content", and
the directory walk at line 106 deliberately includes `.yaml`/`.yml`.

So a YAML fragment — the format the loader's own layout comment at line 66
documents as `fragments.d/<service>/<fragment-name>.yaml` — can never load:

```
failed to load fragment .../owner/route.yaml: failed to parse JSON:
    invalid character 'x' looking for beginning of value
```

Zero fragments load, the diff therefore sees no change, and `runDiffCommand`
returns 0 where the test expects 1. This is a product defect with a test
correctly catching it, not a flaky test. **Not covered by any existing bead.**

## Root cause C — `internal/tailscale` `TestCreateEphemeralKeyHoldDown`

```
client_test.go:387: Expected ErrRateLimited, got rate limited by Tailscale API:
    Tailscale API error (status 429): Rate limit exceeded
```

The client wraps the sentinel instead of returning it:
`internal/tailscale/client.go:233` does `fmt.Errorf("%w: %s", ErrRateLimited,
apiErr)`, while the test at `client_test.go:387` compares with
`err != ErrRateLimited` — interface identity, which a wrapped error never
satisfies. One-line fix on one side or the other (`errors.Is`). The test uses a
local `httptest` stub (`BaseURL: server.URL`), so this is deterministic and not
environment-dependent. **Not covered by any existing bead** — the Closed
`seam-cd76c007` was about unused variables in this package, a different defect.

---

## Failure-bead accounting

| Failure | Bead | State after this run |
|---|---|---|
| A: internal/server test compile | `seam-80488ddd` | Closed prematurely — reopened |
| B: YAML fragments unparseable | new | filed, blocks this bead |
| C: ErrRateLimited identity compare | new | filed, blocks this bead |

`internal/fanout` (seam-1b4d7810) and `internal/spec` (seam-39516e24) now pass
at HEAD — those two closures are confirmed genuine.

## Reproducing this run

```bash
D=$(mktemp -d /tmp/seam-head-evidence-XXXXXX)
git -C /home/coding/SEAM archive b6117fb | tar -x -C "$D"
cd "$D"
go build ./... ; echo "BUILD_EXIT=$?"
go vet ./...   ; echo "VET_EXIT=$?"
go test ./... -count=1 ; echo "TEST_EXIT=$?"
cd / && rm -rf "$D"
```
