# SEAM green-gate evidence — HEAD 5b18455 (seam-71e6eec3 terminal re-verify)

Recorded 2026-09-04T22:23Z by dispatch `seam-c9b66772` (child 5 of 5 of the `seam-71e6eec3`
split). Measured by `git archive HEAD | tar -x` into a mktemp dir — the shared checkout was
dirty with **121** entries belonging to other workers at measurement time, so HEAD, not the
working tree, is what was measured. `-buildvcs=false` throughout (the extract has no `.git`).
Go: `go1.26.6 linux/amd64`.

**Pinned HEAD:** `5b18455332991d424068854605def1185033a09b` (`5b18455`)

## Acceptance criteria — all three commands exit 0

| command | exit |
|---|---|
| `go build -buildvcs=false ./...` | **0** |
| `go vet -buildvcs=false ./...` | **0** |
| `go test ./internal/server/ -count=1 -v -run '<the parent's 24 names>'` | **0** |

```
PASS
ok  	github.com/ardenone/seam/internal/server	3.584s
```

No `--- FAIL` and no `--- SKIP` line anywhere in the verbose output.

## The 24 named tests — every one PASS

Run regex was the parent's own unanchored alternation of the 24 names (left exactly as the
parent wrote it). Accounting diffed the top-level `=== RUN` set against the parent's named
list: **MISSING_FROM_RUN: none, EXTRA_TOPLEVEL_RUNNED: none** — so no name was silently
deselected and no unrelated test is doing the work.

| test | family | child whose fix covers it |
|---|---|---|
| TestConfigStatusHandler | 403 → control-plane | seam-614ef06a (51f446b) |
| TestConfigStatusHandlerWrongMethod | 403 → control-plane | seam-614ef06a (51f446b) |
| TestReservedEndpointsComprehensive | 403 → reserved | seam-614ef06a (51f446b) |
| TestReservedPathComprehensiveMatrix | 403 → reserved | seam-614ef06a (51f446b) |
| TestCredentialHealthSentinelIncludesCircuitBreakerState | 403 → health sentinel | seam-614ef06a (51f446b) |
| TestCredentialHealthSentinelCacheBypassIsFresh | 403 → health sentinel | seam-614ef06a (51f446b) |
| TestCredentialHealthSentinelRejectsNonGet | 403 → health sentinel | seam-614ef06a (51f446b) |
| TestVersionValidationIsWiredToBothListeners | 403 → version | seam-614ef06a (51f446b) |
| TestValidVersionAcceptedOnAllEndpoints | 404 → version/docs | seam-614ef06a + seam-51e3832b (5b18455) |
| TestScopesHandler | 403 → control-plane | seam-614ef06a (51f446b) |
| TestPhase2Scenario1SecretInjectionAndScrubbingEndToEnd | 403 → phase2 | seam-614ef06a (51f446b) |
| TestProxyCaptureEnabled | 403 → proxy-capture | seam-2aa53afd (b10d559) |
| TestProxyCaptureDisabled | 403 → proxy-capture | seam-2aa53afd (b10d559) |
| TestProxyCaptureModesReturnSameResponse | 403 → proxy-capture | seam-2aa53afd (b10d559) |
| TestProxyCaptureLatencyByPayloadSize | 403 → proxy-capture (load-sensitive) | seam-2aa53afd (b10d559) |
| TestProxyCaptureEnabledResponseTimeOverhead | 403 → proxy-capture | seam-2aa53afd (b10d559) |
| TestProxyCaptureEnabledPreservesSuccessfulResponsePair | 403 → proxy-capture pairs | seam-2aa53afd (b10d559) |
| TestProxyCaptureEnabledPreservesErrorResponsePair | 403 → proxy-capture pairs | seam-2aa53afd (b10d559) |
| TestOperatorScopeMiddleware_ErrorResponseFormat | under-scoped identity | seam-9e0f3b9e (66e190d) |
| TestDocsRouteHandlerValidRoute | 404 → docs route | seam-51e3832b (5b18455) |
| TestDocsRouteHandlerAllMethods | 404 → docs route | seam-51e3832b (5b18455) |
| TestDocsRouteHandlerVersionParameter | 404 → docs route | seam-51e3832b (5b18455) |
| TestDocsRouteFixtureIncludesRouteSliceAndExample | 404 fixture metadata | seam-51e3832b (5b18455) |
| TestValidationIntegration_DocsRouteEndpoint_Verified | 404 → docs route | seam-51e3832b (5b18455) |

24/24 PASS, 0 FAIL, 0 SKIP, 0 absent. 166 subtests ran; `TestProxyCaptureLatencyByPayloadSize`
passed this run, consistent with its recorded load-sensitivity rather than a code change.

## Parent's security criterion — identity resolution is NOT weakened

The parent requires that no production endpoint which currently requires identity loses that
requirement. Checked at the same pinned HEAD in a second clean extract:

- `go test ./internal/server/ -count=1 -run 'TestIdentityResolution'` → **exit 0**, including
  `TestIdentityResolutionExemptsInfraProbePaths`, which pins that `/docs` from an
  unresolvable caller **stays 403** — the Stage-3 default-deny is intact.
- `newLoopbackTestIdentityResolver` appears **29** times, all under `internal/server/*_test.go` —
  the loopback override never reached production code.

Net production change across the chain is one commit, `5b18455` ("let the docs route registry
read a resolved identity"): it gives the docs-route registry the *already-resolved* identity
instead of reading it as absent; it does not widen who can resolve. Everything else in the
chain (`51f446b`, `b10d559`, `66e190d`) is test-only wiring.

## Conclusion

Nothing is red at `5b18455`. All five children of the `seam-71e6eec3` split are closed with
their fixes on origin/main, and the parent's full 24-name list passes at a clean pinned HEAD
with build and vet green. `seam-c9b66772` and the umbrella `seam-71e6eec3` are both closeable
on this evidence. Raw logs: `~/scratch/seam-c9b66772/` (build.log, vet.log, test-output.txt,
supp-identity.txt).
