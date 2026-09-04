# SEAM green-gate evidence — HEAD f5962b7 (run 5)

Measured 2026-09-04T15:40Z by `git archive f5962b7 | tar -x` into a mktemp dir, all three
commands in one shell invocation, `-count=1`, `-buildvcs=false` (the extract has no .git).
The shared checkout was dirty with other workers' in-flight edits, so HEAD — not the
working tree — is what was measured.

`f5962b7` is docs-only over `45ba788` (`git diff --name-only 45ba788 f5962b7` = the one
file `.beads/green-evidence-45ba788.md`), so this measurement covers the identical source
tree run 4 measured.

| command | exit |
|---|---|
| `go build ./...` | **0** |
| `go vet ./...` | **0** |
| `go test ./...` | **1** |

## Verdict: still red — go test is not exit 0, so the gate stays open

46 runtime failures, all in `internal/server`, none a compile error. Every one is already
filed and linked to seam-d33d0b9c; **no new bead was filed**. Per-bead accounting of the
46 (nothing unaccounted for):

| bead | scope | failing now |
|---|---|---|
| seam-e2549287 | quota + cost-per-call records $0.00 | 11 |
| seam-a117a092 | loop-guard sub-second window + nested-JSON hash | 5 |
| seam-71e6eec3 | stage-3 identity 403s / docs-route 404s on control-plane, reserved, capture | 23 |
| seam-1a440bb7 | taxonomy vs OpenAPI drift, scope-version LRU eviction order | 3 |
| seam-f2c119f2 | loop-guard middleware route-match stub fail-open | 4 |
| **total** | | **46** |

## Delta vs run 4 (48 → 46): two load-sensitive tests passed this time, neither was fixed

Source is byte-identical to the run-4 measurement point, so a disappearing failure cannot
be a fix. Both missing tests are in the failed set of run 4 and both are timing-sensitive:

- `TestArgoCDProxyBaselineResponseTimes` — seam-1a440bb7's own description flags it
  "Load-sensitive. Re-measure on an idle box": run 4 measured 20.936ms average against a
  <20ms threshold. It passed this run; the threshold still needs the evidence-based
  decision that bead calls for.
- `TestProxyCaptureLatencyByPayloadSize` — failed at run 4 with `baseline warmup request 3
  failed (status 200): unexpected EOF` (proxy_capture_performance_test.go:170), a
  network-flake shape, not an assertion. It belongs to seam-71e6eec3's stage-3 scope and is
  expected to reappear under load.

Treat both as flaky-under-load members of their beads' scopes, not as progress. The stable
red set is 46.

## Chain status, read 2026-09-04T15:37Z (immediately before the measurement)

seam-e2549287 in_progress (glm-vista, rev 2); seam-a117a092, seam-71e6eec3, seam-1a440bb7,
seam-f2c119f2 open. **seam-56744617 — the terminal child whose whole job is to record the
green evidence — is CLOSED at rev 2 with no notes and no evidence file, while all five of
its blockers are open or in_progress.** That is a phantom tail closure: its own acceptance
criteria require it to stay open while anything is red. It is being reopened with this run
as the reason.

## Package summary

````
ok  	github.com/ardenone/seam/benches	0.004s
ok  	github.com/ardenone/seam/benchmarks/baseline	0.002s
?   	github.com/ardenone/seam/cmd/baseline	[no test files]
ok  	github.com/ardenone/seam/cmd/seam	0.049s
ok  	github.com/ardenone/seam/corpus	0.489s
ok  	github.com/ardenone/seam/internal/buildinfo	0.002s
ok  	github.com/ardenone/seam/internal/fanout	0.165s
?   	github.com/ardenone/seam/internal/pluckfallback	[no test files]
FAIL
FAIL	github.com/ardenone/seam/internal/server	94.650s
ok  	github.com/ardenone/seam/internal/spec	0.210s
ok  	github.com/ardenone/seam/internal/tailscale	0.087s
ok  	github.com/ardenone/seam/internal/testutil	0.002s [no tests to run]
?   	github.com/ardenone/seam/internal/testutil/openbao	[no test files]
ok  	github.com/ardenone/seam/internal/testutil/stubupstream	0.718s
ok  	github.com/ardenone/seam/internal/vault	0.005s
ok  	github.com/ardenone/seam/internal/version	0.002s
?   	github.com/ardenone/seam/internal/watcher	[no test files]
?   	github.com/ardenone/seam/scratch	[no test files]
FAIL
```

## All 46 failing tests, each with its assertion lines

```
--- FAIL: TestScopesHandler (0.00s)
    --- FAIL: TestScopesHandler/all_scopes_denied_without_seam:scopes:read-all (0.00s)
        control_plane_handlers_test.go:286: Should return error for missing seam:scopes:read-all
--- FAIL: TestCostPerCallMetric_CacheHitBypass (0.01s)
    cost_per_call_metric_test.go:66: expected seam_quota_cost_total metric for /api/test after cache miss
    cost_per_call_metric_test.go:72: expected initial cost of $0.10, got $0.00
--- FAIL: TestCostPerCallMetric_CacheMissIncrements (0.01s)
    cost_per_call_metric_test.go:178: route /api/test1: expected cost metric of $0.25, got $0.00
    cost_per_call_metric_test.go:178: route /api/test2: expected cost metric of $0.25, got $0.00
--- FAIL: TestCostPerCallMetric_MixedHitsAndMisses (0.01s)
    cost_per_call_metric_test.go:270: route /api/mixed1: expected cost of $0.20 (1 miss), got $0.00
    cost_per_call_metric_test.go:274: route /api/mixed2: expected cost of $0.20 (1 miss), got $0.00
--- FAIL: TestErrorTaxonomyWritesDocumentedEnvelopeAndStatus (0.00s)
    errors_test.go:43: taxonomy contains 29 codes, test covers 22
2026/09/04 11:41:40 [error-response] request_id=request-fallback failed to encode response: json: unsupported type: chan struct {}
--- FAIL: TestOpenAPIErrorSchemaMatchesRuntimeTaxonomy (0.01s)
    errors_test.go:237: OpenAPI has 22 unique codes, runtime has 29
2026/09/04 11:41:40 Loaded spec from ../../spec
--- FAIL: TestCredentialHealthSentinelIncludesCircuitBreakerState (0.01s)
    health_sentinel_test.go:40: expected health status 200, got 403
2026/09/04 11:41:40 Loaded spec from ../../spec
--- FAIL: TestCredentialHealthSentinelCacheBypassIsFresh (0.01s)
    health_sentinel_test.go:76: first health request: expected 200, got 403
2026/09/04 11:41:40 Loaded spec from ../../spec
--- FAIL: TestCredentialHealthSentinelRejectsNonGet (0.01s)
    health_sentinel_test.go:118: expected POST health request to return 405, got 403
2026/09/04 11:41:40 [Stage-3-Identity] Failed to resolve identity for 127.0.0.1:51000: not a Tailscale IP address
--- FAIL: TestLoopGuardHash_NestedJSON (0.00s)
    loop_guard_hash_test.go:217: Hashes with reordered nested JSON should match: got 4bb74be63024d408 vs af4f824d63e6bffb
--- FAIL: TestLoopGuardMiddleware_BlocksRepeatedFailures (0.00s)
    loop_guard_integration_test.go:144: Request 4 should be blocked by loop guard, got status 500
    loop_guard_integration_test.go:149: Blocked request should have Retry-After header
--- FAIL: TestLoopGuardMiddleware_DifferentRequestsIndependent (0.00s)
    loop_guard_integration_test.go:286: hash1 should be blocked, got status 500
--- FAIL: TestLoopGuardMiddleware_ResponseIncludesDetails (0.00s)
    loop_guard_integration_test.go:347: Expected 429 status, got 500
    loop_guard_integration_test.go:353: Expected application/json content type, got 
--- FAIL: TestLoopGuardMiddleware_QueryParamsInHash (0.00s)
    loop_guard_integration_test.go:419: Request with foo=bar should be blocked, got status 500
--- FAIL: TestLoopGuardConfig_ParseWindow (0.00s)
    --- FAIL: TestLoopGuardConfig_ParseWindow/seconds (0.00s)
        loop_guard_test.go:75: Unexpected error: maxRepeats must be at least 1, got 0
--- FAIL: TestLoopGuard_BlockAfterThreshold (0.00s)
    loop_guard_test.go:162: Request at threshold should be allowed
--- FAIL: TestLoopGuard_SuccessClearsCounter (0.00s)
    loop_guard_test.go:217: Request at threshold should be allowed
--- FAIL: TestLoopGuard_WindowRolling (0.00s)
    loop_guard_test.go:272: Failed to create loop guard: invalid window format: 100ms (expected format: ^[0-9]+(s|m|h|d)$)
2026/09/04 11:41:43 Loaded spec from ../../spec
--- FAIL: TestOperatorScopeMiddleware_ErrorResponseFormat (0.00s)
    operator_scope_test.go:292: expected code=forbidden, got <nil>
2026/09/04 11:41:46 [Operator-Scope] Identity has required scope seam:ops:read for /config/status - allowing
--- FAIL: TestPhase2Scenario1SecretInjectionAndScrubbingEndToEnd (2.58s)
    phase2_acceptance_test.go:118: response status = 403, want 401
2026/09/04 11:41:48 Loaded spec from /tmp/TestProxyCaptureEnabledPreservesSuccessfulResponsePair101234609/001/spec
--- FAIL: TestProxyCaptureEnabledPreservesSuccessfulResponsePair (0.00s)
    proxy_capture_pairs_test.go:28: expected proxied status 200, got 403
2026/09/04 11:41:48 Loaded spec from /tmp/TestProxyCaptureEnabledPreservesErrorResponsePair3104825527/001/spec
--- FAIL: TestProxyCaptureEnabledPreservesErrorResponsePair (0.00s)
    proxy_capture_pairs_test.go:79: expected proxied status 502, got 403
2026/09/04 11:41:48 captured: POST /capture-latency (total 1 entries)
--- FAIL: TestProxyCaptureDisabled (0.00s)
    proxy_capture_test.go:271: get capture status: capture status returned 403
2026/09/04 11:41:49 Loaded spec from /tmp/TestProxyCaptureEnabled4176817891/001/spec
--- FAIL: TestProxyCaptureEnabled (0.00s)
    proxy_capture_test.go:304: get capture status: capture status returned 403
2026/09/04 11:41:49 Loaded spec from /tmp/TestProxyCaptureModesReturnSameResponsedisabled872418330/001/spec
--- FAIL: TestProxyCaptureModesReturnSameResponse (10.01s)
    --- FAIL: TestProxyCaptureModesReturnSameResponse/disabled (5.01s)
        proxy_capture_test.go:369: expected status 200, got 403
--- FAIL: TestProxyCaptureEnabledResponseTimeOverhead (0.01s)
    proxy_capture_test.go:410: get capture status: capture status returned 403
2026/09/04 11:41:59 [proxy] Upstream request failed for /request - not charging quota (transport error): Get "http://127.0.0.1:44913/internal/upstream-target/request": dial tcp 127.0.0.1:44913: connect: connection refused
--- FAIL: TestQuotaBypass_CacheHit (0.01s)
    quota_bypass_test.go:66: first request should have quota cost header (cache miss)
    quota_bypass_test.go:75: first request: expected accumulated quota of $0.10, got $0.00
--- FAIL: TestQuotaBypass_CacheMissDeducts (0.01s)
    quota_bypass_test.go:183: route /api/test: expected accumulated quota of $0.25, got $0.00
    quota_bypass_test.go:183: route /api/test2: expected accumulated quota of $0.25, got $0.00
--- FAIL: TestQuotaBypass_ConsecutiveHits (0.01s)
    quota_bypass_test.go:274: expected accumulated quota of $0.10, got $0.00
2026/09/04 11:41:59 Loaded spec from ../../spec
--- FAIL: TestQuotaBypass_MultipleRoutes (0.01s)
    quota_bypass_test.go:403: route /api/users: expected $0.15 accumulated, got $0.00
    quota_bypass_test.go:403: route /api/posts: expected $0.15 accumulated, got $0.00
--- FAIL: TestQuotaEnforcement_CacheMissIntegration (0.01s)
    quota_enforcement_integration_test.go:92: fourth request: expected status 429 (quota exceeded), got 200
    quota_enforcement_integration_test.go:97: quota exceeded response should have Content-Type: application/json
--- FAIL: TestQuotaEnforcement_CacheHitsBypassEnforcement (0.01s)
    quota_enforcement_integration_test.go:197: third request (cache miss, different route): expected status 429, got 200
    quota_enforcement_integration_test.go:220: expected seam_quota_exceeded_total metric for /api/test2
--- FAIL: TestQuotaEnforcement_GlobalScope (0.01s)
    quota_enforcement_integration_test.go:275: third request: expected status 429 (global quota exceeded), got 200
2026/09/04 11:41:59 Loaded spec from ../../spec
--- FAIL: TestQuotaEnforcement_ResponseHeaders (0.01s)
    quota_enforcement_integration_test.go:340: first request should have X-Quota-Cost-Per-Call header
    quota_enforcement_integration_test.go:344: first request should have X-Quota-Remaining header
--- FAIL: TestDocsRouteFixtureIncludesRouteSliceAndExample (0.00s)
    request_validation_test.go:115: docs route status = 404, want 404: {"details":{"method":"","path":"/widgets/{id}"},"error":"route_not_found","message":"No route found for  /widgets/{id}","metadata":{"docs_url":"/docs","whoami":"/whoami"}}
2026/09/04 11:41:59 Loaded spec from ../../spec
--- FAIL: TestReservedEndpointsComprehensive (0.17s)
    --- FAIL: TestReservedEndpointsComprehensive/Requirement_1_All_Reserved_Endpoints_Return_Correct_Responses (0.03s)
        --- FAIL: TestReservedEndpointsComprehensive/Requirement_1_All_Reserved_Endpoints_Return_Correct_Responses/config_status_endpoint_responses (0.01s)
--- FAIL: TestReservedPathComprehensiveMatrix (0.01s)
    --- FAIL: TestReservedPathComprehensiveMatrix/matrix__config_status (0.00s)
        --- FAIL: TestReservedPathComprehensiveMatrix/matrix__config_status/POST_rejected (0.00s)
--- FAIL: TestScopeVersionCachePerIdentityCap (0.00s)
    scope_version_test.go:146: Version 4 (hash 70ebb9b003e83103a4aa844f8779bb93294f8d04d293edcec908f8bdd7f9c297) should still be in cache
    scope_version_test.go:146: Version 5 (hash 7cee304250786a6e9c554c0d484ad0db4f723c1554d476c1cd281148f1ccf1a3) should still be in cache
--- FAIL: TestConfigStatusHandler (0.01s)
    server_test.go:166: expected status 200, got 403
    server_test.go:176: expected fragments_loaded=false, got <nil>
--- FAIL: TestConfigStatusHandlerWrongMethod (0.01s)
    server_test.go:203: expected status 405, got 403
2026/09/04 11:41:59 Loaded spec from ../../spec
--- FAIL: TestDocsRouteHandlerValidRoute (0.01s)
    server_test.go:640: expected status 200, got 404
    server_test.go:651: expected path '/openapi.json', got <nil>
--- FAIL: TestDocsRouteHandlerAllMethods (0.01s)
    server_test.go:707: expected status 200, got 404
    server_test.go:718: expected methods to be present when no method specified
--- FAIL: TestDocsRouteHandlerVersionParameter (0.01s)
    server_test.go:815: expected status 200, got 404
    server_test.go:825: expected version '_unversioned', got <nil>
--- FAIL: TestValidationIntegration_DocsRouteEndpoint_Verified (0.01s)
    --- FAIL: TestValidationIntegration_DocsRouteEndpoint_Verified/GET_/docs/route_for_existing_path (0.00s)
        validation_integration_test.go:375: Expected status 200, got 404. Body: {"details":{"method":"GET","path":"/test/get"},"error":"route_not_found","message":"No route found for GET /test/get","metadata":{"docs_url":"/docs","whoami":"/whoami"}}
--- FAIL: TestVersionValidationIsWiredToBothListeners (0.01s)
    --- FAIL: TestVersionValidationIsWiredToBothListeners/operator (0.00s)
        version_validation_test.go:129: status = 403, want 400
--- FAIL: TestValidVersionAcceptedOnAllEndpoints (0.01s)
    --- FAIL: TestValidVersionAcceptedOnAllEndpoints/route_documentation (0.00s)
        version_validation_test.go:228: status = 404, want 200
```
