# SEAM green-gate evidence — HEAD 45ba788 (run 4)

Measured 2026-09-04 (~10:45Z) by `git archive HEAD | tar -x` into a mktemp dir: the shared
checkout was dirty with 125 entries belonging to other workers, so HEAD — not the working
tree — is what was measured. `-buildvcs=false` (the extract has no .git).

| command | exit |
|---|---|
| `go build ./...` | **0** |
| `go vet ./...` | **0** |
| `go test ./...` | **1** |

Build and vet are green for the first time in this bead's history. Runs 2 and 3 at
`b6117fba` had vet exit 1 on a missing `seam/internal/tailscale` import; that is fixed at
HEAD and internal/server now compiles, so **all 48 remaining failures are runtime
failures in one package**, not compile errors. `cmd/seam` and `internal/tailscale` now
pass, matching their closed beads seam-8f5949ad and seam-36f9cc28.

## Package summary

```
ok  	github.com/ardenone/seam/benches	0.012s
ok  	github.com/ardenone/seam/benchmarks/baseline	0.005s
?   	github.com/ardenone/seam/cmd/baseline	[no test files]
ok  	github.com/ardenone/seam/cmd/seam	0.124s
ok  	github.com/ardenone/seam/corpus	2.102s
ok  	github.com/ardenone/seam/internal/buildinfo	0.005s
ok  	github.com/ardenone/seam/internal/fanout	0.217s
?   	github.com/ardenone/seam/internal/pluckfallback	[no test files]
FAIL
FAIL	github.com/ardenone/seam/internal/server	108.003s
ok  	github.com/ardenone/seam/internal/spec	1.056s
ok  	github.com/ardenone/seam/internal/tailscale	0.148s
ok  	github.com/ardenone/seam/internal/testutil	(cached) [no tests to run]
?   	github.com/ardenone/seam/internal/testutil/openbao	[no test files]
ok  	github.com/ardenone/seam/internal/testutil/stubupstream	0.763s
ok  	github.com/ardenone/seam/internal/vault	0.046s
ok  	github.com/ardenone/seam/internal/version	0.007s
?   	github.com/ardenone/seam/internal/watcher	[no test files]
?   	github.com/ardenone/seam/scratch	[no test files]
FAIL
```

## All 48 failing tests, each with its first assertion

```
--- FAIL: TestArgoCDProxyBaselineResponseTimes (2.12s)
            capture_baseline_test.go:260: Baseline Response Times for /docs:
--- FAIL: TestScopesHandler (0.00s)
            control_plane_handlers_test.go:286: Should return error for missing seam:scopes:read-all
--- FAIL: TestCostPerCallMetric_CacheHitBypass (0.15s)
        cost_per_call_metric_test.go:66: expected seam_quota_cost_total metric for /api/test after cache miss
--- FAIL: TestCostPerCallMetric_CacheMissIncrements (0.05s)
        cost_per_call_metric_test.go:178: route /api/test1: expected cost metric of $0.25, got $0.00
--- FAIL: TestCostPerCallMetric_MixedHitsAndMisses (0.08s)
        cost_per_call_metric_test.go:270: route /api/mixed1: expected cost of $0.20 (1 miss), got $0.00
--- FAIL: TestErrorTaxonomyWritesDocumentedEnvelopeAndStatus (0.00s)
        errors_test.go:43: taxonomy contains 29 codes, test covers 22
--- FAIL: TestOpenAPIErrorSchemaMatchesRuntimeTaxonomy (0.09s)
        errors_test.go:237: OpenAPI has 22 unique codes, runtime has 29
--- FAIL: TestCredentialHealthSentinelIncludesCircuitBreakerState (0.07s)
        health_sentinel_test.go:40: expected health status 200, got 403
--- FAIL: TestCredentialHealthSentinelCacheBypassIsFresh (0.03s)
        health_sentinel_test.go:76: first health request: expected 200, got 403
--- FAIL: TestCredentialHealthSentinelRejectsNonGet (0.02s)
        health_sentinel_test.go:118: expected POST health request to return 405, got 403
--- FAIL: TestLoopGuardHash_NestedJSON (0.00s)
        loop_guard_hash_test.go:217: Hashes with reordered nested JSON should match: got 4bb74be63024d408 vs af4f824d63e6bffb
--- FAIL: TestLoopGuardMiddleware_BlocksRepeatedFailures (0.00s)
        loop_guard_integration_test.go:144: Request 4 should be blocked by loop guard, got status 500
--- FAIL: TestLoopGuardMiddleware_DifferentRequestsIndependent (0.00s)
        loop_guard_integration_test.go:286: hash1 should be blocked, got status 500
--- FAIL: TestLoopGuardMiddleware_ResponseIncludesDetails (0.00s)
        loop_guard_integration_test.go:347: Expected 429 status, got 500
--- FAIL: TestLoopGuardMiddleware_QueryParamsInHash (0.00s)
        loop_guard_integration_test.go:419: Request with foo=bar should be blocked, got status 500
--- FAIL: TestLoopGuardConfig_ParseWindow (0.00s)
            loop_guard_test.go:75: Unexpected error: maxRepeats must be at least 1, got 0
--- FAIL: TestLoopGuard_BlockAfterThreshold (0.00s)
        loop_guard_test.go:162: Request at threshold should be allowed
--- FAIL: TestLoopGuard_SuccessClearsCounter (0.00s)
        loop_guard_test.go:217: Request at threshold should be allowed
--- FAIL: TestLoopGuard_WindowRolling (0.00s)
        loop_guard_test.go:272: Failed to create loop guard: invalid window format: 100ms (expected format: ^[0-9]+(s|m|h|d)$)
--- FAIL: TestOperatorScopeMiddleware_ErrorResponseFormat (0.00s)
        operator_scope_test.go:292: expected code=forbidden, got <nil>
--- FAIL: TestPhase2Scenario1SecretInjectionAndScrubbingEndToEnd (2.59s)
        phase2_acceptance_test.go:118: response status = 403, want 401
--- FAIL: TestProxyCaptureEnabledPreservesSuccessfulResponsePair (0.01s)
        proxy_capture_pairs_test.go:28: expected proxied status 200, got 403
--- FAIL: TestProxyCaptureEnabledPreservesErrorResponsePair (0.01s)
        proxy_capture_pairs_test.go:79: expected proxied status 502, got 403
--- FAIL: TestProxyCaptureLatencyByPayloadSize (0.35s)
            proxy_capture_performance_test.go:170: baseline warmup request 3 failed (status 200): unexpected EOF
--- FAIL: TestProxyCaptureDisabled (0.03s)
        proxy_capture_test.go:271: get capture status: capture status returned 403
--- FAIL: TestProxyCaptureEnabled (0.01s)
        proxy_capture_test.go:304: get capture status: capture status returned 403
--- FAIL: TestProxyCaptureModesReturnSameResponse (10.02s)
            proxy_capture_test.go:369: expected status 200, got 403
--- FAIL: TestProxyCaptureEnabledResponseTimeOverhead (0.04s)
        proxy_capture_test.go:410: get capture status: capture status returned 403
--- FAIL: TestQuotaBypass_CacheHit (0.01s)
        quota_bypass_test.go:66: first request should have quota cost header (cache miss)
--- FAIL: TestQuotaBypass_CacheMissDeducts (0.01s)
        quota_bypass_test.go:183: route /api/test: expected accumulated quota of $0.25, got $0.00
--- FAIL: TestQuotaBypass_ConsecutiveHits (0.03s)
        quota_bypass_test.go:274: expected accumulated quota of $0.10, got $0.00
--- FAIL: TestQuotaBypass_MultipleRoutes (0.04s)
        quota_bypass_test.go:403: route /api/users: expected $0.15 accumulated, got $0.00
--- FAIL: TestQuotaEnforcement_CacheMissIntegration (0.02s)
        quota_enforcement_integration_test.go:92: fourth request: expected status 429 (quota exceeded), got 200
--- FAIL: TestQuotaEnforcement_CacheHitsBypassEnforcement (0.01s)
        quota_enforcement_integration_test.go:197: third request (cache miss, different route): expected status 429, got 200
--- FAIL: TestQuotaEnforcement_GlobalScope (0.02s)
        quota_enforcement_integration_test.go:275: third request: expected status 429 (global quota exceeded), got 200
--- FAIL: TestQuotaEnforcement_ResponseHeaders (0.06s)
        quota_enforcement_integration_test.go:340: first request should have X-Quota-Cost-Per-Call header
--- FAIL: TestDocsRouteFixtureIncludesRouteSliceAndExample (0.00s)
        request_validation_test.go:115: docs route status = 404, want 404: {"details":{"method":"","path":"/widgets/{id}"},"error":"route_not_found","message":"No route found for  /widgets/{id}","metadata":{"docs_url":"/docs","whoami":"/whoami"}}
--- FAIL: TestReservedEndpointsComprehensive (0.59s)
                    reserved_endpoints_test.go:321: expected status 200, got 403
--- FAIL: TestReservedPathComprehensiveMatrix (0.10s)
                reserved_endpoints_test.go:839: POST to /config/status should return 405, got 403
--- FAIL: TestScopeVersionCachePerIdentityCap (0.00s)
        scope_version_test.go:146: Version 4 (hash 70ebb9b003e83103a4aa844f8779bb93294f8d04d293edcec908f8bdd7f9c297) should still be in cache
--- FAIL: TestConfigStatusHandler (0.04s)
        server_test.go:166: expected status 200, got 403
--- FAIL: TestConfigStatusHandlerWrongMethod (0.02s)
        server_test.go:203: expected status 405, got 403
--- FAIL: TestDocsRouteHandlerValidRoute (0.01s)
        server_test.go:640: expected status 200, got 404
--- FAIL: TestDocsRouteHandlerAllMethods (0.08s)
        server_test.go:707: expected status 200, got 404
--- FAIL: TestDocsRouteHandlerVersionParameter (0.09s)
        server_test.go:815: expected status 200, got 404
--- FAIL: TestValidationIntegration_DocsRouteEndpoint_Verified (0.10s)
            validation_integration_test.go:375: Expected status 200, got 404. Body: {"details":{"method":"GET","path":"/test/get"},"error":"route_not_found","message":"No route found for GET /test/get","metadata":{"docs_url":"/docs","whoami":"/whoami"}}
--- FAIL: TestVersionValidationIsWiredToBothListeners (0.01s)
            version_validation_test.go:129: status = 403, want 400
--- FAIL: TestValidVersionAcceptedOnAllEndpoints (0.06s)
            version_validation_test.go:228: status = 404, want 200
```

## Disposition — every failure is now tracked

Parent seam-d33d0b9c was split 2026-09-04 into a chained umbrella. Coverage:

| bead | scope | tests |
|---|---|---|
| seam-e2549287 | quota + cost-per-call accounting records $0.00 | 11 |
| seam-a117a092 | loop-guard sub-second window regex + nested-JSON hash canonicalisation | 5 |
| seam-71e6eec3 | Stage-3 identity 403s blocking control-plane/reserved/docs/capture handlers | 24 |
| seam-1a440bb7 | error taxonomy vs OpenAPI drift, scope-version LRU order, baseline perf threshold | 4 |
| seam-f2c119f2 | (already filed) LoopGuardMiddleware getRouteMatchFromContext stub | 4 |
| seam-56744617 | terminal: re-run the triple at HEAD and record green evidence | 0 |

11 + 5 + 24 + 4 + 4 = 48. No failure is unaccounted for.

### Accounting correction

seam-63dc8615 names 11 tests (TestCaptureNonIntrusion/Completeness/FullLifecycleIntegration/
LatencyIsAcceptable/LatencyUnderLoad/RedactsRouteInjectableNames + TestArgoCDProxyBaseline
{Operation,ResponseTimes,Consistency,CaptureStatusDisabled}) — **all of those pass at HEAD
45ba788** (grep of this run's output: FAIL=0 for each). Its "capture records nothing" scope
was fixed by an earlier commit. Only `TestArgoCDProxyBaselineResponseTimes` still fails and
that is a perf-threshold assertion (avg 20.936ms vs <20ms), tracked in seam-1a440bb7 —
not a capture-path defect.

### Accounting correction (2)

seam-7bef8748's target `TestHTTPErrorPathsUseCommonEnvelope` does not appear in this run's
output at all — it does not fail at HEAD 45ba788.

## Split record

- Chain: seam-e2549287 → seam-a117a092 → seam-71e6eec3 → seam-1a440bb7 → seam-56744617.
- seam-56744617 is additionally blocked by seam-f2c119f2 (its 4 middleware tests are part of the 48).
- seam-d33d0b9c is blocked by seam-56744617 and now carries the `umbrella` label;
  `failure-count:4` was removed so the auto-split loop does not re-fire.
- Sequential chaining is deliberate: all six beads edit the same Go package in a shared
  checkout with a documented git-index race (829fa41).
