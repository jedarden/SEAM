# SEAM Build Evidence - 2026-09-01

## Environment
- **Go Version**: go1.26.0 linux/amd64
- **Go Binary**: /home/coding/.local/go/bin/go
- **OS**: NixOS 26.05
- **Date**: 2026-09-01

## Command Results

### 1. go build ./...

```
Exit code: 1

# github.com/ardenone/seam/internal/server
internal/server/lease_leadership.go:179:26: cannot use int32(l.leaseDuration.Seconds()) (value of type int32) as *int32 value in struct literal
internal/server/server.go:412:56: s.exclusionReportHandler undefined (type *Server has no field or method exclusionReportHandler)
internal/server/server.go:413:57: s.exclusionAllReportsHandler undefined (type *Server has no field or method exclusionAllReportsHandler)
internal/server/server.go:414:57: s.exclusionAnalyzeHandler undefined (type *Server has no field or method exclusionAnalyzeHandler)
internal/server/server.go:415:57: s.exclusionSummaryHandler undefined (type *Server has no field or method exclusionSummaryHandler)
internal/server/server.go:416:56: s.exclusionAlertsHandler undefined (type *Server has no field or method exclusionAlertsHandler)
internal/server/server.go:417:63: s.exclusionActiveAlertsHandler undefined (type *Server has no field or method exclusionActiveAlertsHandler)
internal/server/server.go:418:64: s.exclusionResolveAlertHandler undefined (type *Server has no field or method exclusionResolveAlertHandler)
internal/server/quota_middleware.go:8:2: "strings" imported and not used
```

**Build Status**: ❌ FAILED

### 2. go vet ./...

```
Exit code: 1

internal/server/worker_identity_integration_test.go:12:2: package seam/internal/tailscale is not in std (/home/coding/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.0.linux-amd64/src/seam/internal/tailscale)
# github.com/ardenone/seam/internal/tailscale [github.com/ardenone/seam/internal/tailscale.test]
internal/tailscale/cache_test.go:186:2: declared and not used: shortTTL
internal/tailscale/client_test.go:351:2: declared and not used: key3
# github.com/ardenone/seam/internal/server
internal/server/lease_leadership.go:179:26: cannot use int32(l.leaseDuration.Seconds()) (value of type int32) as *int32 value in struct literal
internal/server/server.go:412:56: s.exclusionReportHandler undefined (type *Server has no field or method exclusionReportHandler)
internal/server/server.go:413:57: s.exclusionAllReportsHandler undefined (type *Server has no field or method exclusionAllReportsHandler)
internal/server/server.go:414:57: s.exclusionAnalyzeHandler undefined (type *Server has no field or method exclusionAnalyzeHandler)
internal/server/server.go:415:57: s.exclusionSummaryHandler undefined (type *Server has no field or method exclusionSummaryHandler)
internal/server/server.go:416:56: s.exclusionAlertsHandler undefined (type *Server has no field or method exclusionAlertsHandler)
internal/server/server.go:417:63: s.exclusionActiveAlertsHandler undefined (type *Server has no field or method exclusionActiveAlertsHandler)
internal/server/server.go:418:64: s.exclusionResolveAlertHandler undefined (type *Server has no field or method exclusionResolveAlertHandler)
internal/server/quota_middleware.go:8:2: "strings" imported and not used
```

**Vet Status**: ❌ FAILED

### 3. go test ./...

```
Exit code: 1

# github.com/ardenone/seam/internal/server
internal/server/worker_identity_integration_test.go:12:2: package seam/internal/tailscale is not in std (/home/coding/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.0.linux-amd64/src/seam/internal/tailscale)
FAIL	github.com/ardenone/seam/internal/server [setup failed]
# github.com/ardenone/seam/internal/tailscale [github.com/ardenone/seam/internal/tailscale.test]
internal/tailscale/cache_test.go:186:2: declared and not used: shortTTL
internal/tailscale/client_test.go:351:2: declared and not used: key3
# github.com/ardenone/seam/internal/server
internal/server/lease_leadership.go:179:26: cannot use int32(l.leaseDuration.Seconds()) (value of type int32) as *int32 value in struct literal
internal/server/server.go:412:56: s.exclusionReportHandler undefined (type *Server has no field or method exclusionReportHandler)
internal/server/server.go:413:57: s.exclusionAllReportsHandler undefined (type *Server has no field or method exclusionAllReportsHandler)
internal/server/server.go:414:57: s.exclusionAnalyzeHandler undefined (type *Server has no field or method exclusionAnalyzeHandler)
internal/server/server.go:415:57: s.exclusionSummaryHandler undefined (type *Server has no field or method exclusionSummaryHandler)
internal/server/server.go:416:56: s.exclusionAlertsHandler undefined (type *Server has no field or method exclusionAlertsHandler)
internal/server/server.go:417:63: s.exclusionActiveAlertsHandler undefined (type *Server has no field or method exclusionActiveAlertsHandler)
internal/server/server.go:418:64: s.exclusionResolveAlertHandler undefined (type *Server has no field or method exclusionResolveAlertHandler)
internal/server/quota_middleware.go:8:2: "strings" imported and not used

ok  	github.com/ardenone/seam/benches	0.009s
ok  	github.com/ardenone/seam/benchmarks/baseline	0.005s
?   	github.com/ardenone/seam/cmd/baseline	[no test files]
FAIL	github.com/ardenone/seam/cmd/seam [build failed]
ok  	github.com/ardenone/seam/corpus	0.515s
ok  	github.com/ardenone/seam/internal/buildinfo	0.002s

--- FAIL: TestDispatch_ContextCancellation (0.00s)
    dispatcher_test.go:433: Expected status error for cancelled context, got timeout
FAIL
FAIL	github.com/ardenone/seam/internal/fanout	0.165s
?   	github.com/ardenone/seam/internal/pluckfallback	[no test files]

--- FAIL: TestCheckDeprecation_InvalidSince (0.00s)
    deprecation_lint_test.go:65: Expected at least 1 error for invalid since date, got 0
--- FAIL: TestCheckDeprecation_BrownoutWithoutSunset (0.00s)
    deprecation_lint_test.go:140: Expected error code 'deprecation.brownout-without-sunset', got "deprecation.brownout-invalid"
--- FAIL: TestCheckDeprecation_OverlappingBrownouts (0.00s)
    deprecation_lint_test.go:174: Expected error code 'deprecation.brownout-overlapping', got "deprecation.brownout-invalid"
--- FAIL: TestCheckDeprecation_BrownoutEndBeforeStart (0.00s)
    deprecation_lint_test.go:204: Expected error code 'deprecation.brownout-end-before-start', got "deprecation.brownout-invalid"
--- FAIL: TestCheckDeprecation_BrownoutOutOfRange (0.00s)
    deprecation_lint_test.go:234: Expected error code 'deprecation.brownout-out-of-range', got "deprecation.brownout-invalid"
--- FAIL: TestCheckDeprecation_ValidBrownout (0.00s)
    deprecation_lint_test.go:264: Expected no errors for valid brownout windows, got 1
    deprecation_lint_test.go:266: Error: deprecation.brownout-invalid - x-seam-deprecated.brownout must be an array
--- FAIL: TestIsValidISODate (0.00s)
    --- FAIL: TestIsValidISODate/Invalid_month (0.00s)
        deprecation_lint_test.go:292: isValidISODate("2024-13-01") = true, want false
    --- FAIL: TestIsValidISODate/Invalid_day (0.00s)
        deprecation_lint_test.go:292: isValidISODate("2024-01-32") = true, want false

FAIL
FAIL	github.com/ardenone/seam/internal/spec	0.310s
FAIL	github.com/ardenone/seam/internal/tailscale [build failed]
ok  	github.com/ardenone/seam/internal/testutil	(cached) [no tests to run]
?   	github.com/ardenone/seam/internal/testutil/openbao	[no test files]
ok  	github.com/ardenone/seam/internal/testutil/stubupstream	0.717s
ok  	github.com/ardenone/seam/internal/vault	(cached)
ok  	github.com/ardenone/seam/internal/version	(cached)
?   	github.com/ardenone/seam/internal/watcher	[no test files]
?   	github.com/ardenone/seam/scratch	[no test files]
?   	github.com/ardenone/seam/tools/starvation-alert-contradiction-detector	[no test files]
FAIL	github.com/ardenone/seam/tools/starvation-alert-diagnostic [build failed]
FAIL	github.com/ardenone/seam/tools/starvation-alert-human-monitor [build failed]
FAIL	github.com/ardenone/seam/tools/starvation-alert-self-resolution [build failed]
FAIL	github.com/ardenone/seam/tools/starvation-backoff-monitor [build failed]
FAIL	github.com/ardenone/seam/tools/starvation-diagnostic-daemon [build failed]
```

**Test Status**: ❌ FAILED

## Summary

### Critical Build Failures (Blocker)

**internal/server** - 9 compilation errors:
1. `lease_leadership.go:179:26` - Type conversion: cannot use `int32(l.leaseDuration.Seconds())` as `*int32` in struct literal
2. `server.go:412-418` - 7 undefined handler methods: `exclusionReportHandler`, `exclusionAllReportsHandler`, `exclusionAnalyzeHandler`, `exclusionSummaryHandler`, `exclusionAlertsHandler`, `exclusionActiveAlertsHandler`, `exclusionResolveAlertHandler`
3. `quota_middleware.go:8:2` - Unused import `"strings"`

**internal/tailscale** - 2 unused variables in tests:
1. `cache_test.go:186:2` - `shortTTL` declared and not used
2. `client_test.go:351:2` - `key3` declared and not used

**Downstream Build Failures** (caused by internal/server failures):
- `cmd/seam` - build failed
- `tools/starvation-alert-diagnostic` - build failed
- `tools/starvation-alert-human-monitor` - build failed
- `tools/starvation-alert-self-resolution` - build failed
- `tools/starvation-backoff-monitor` - build failed
- `tools/starvation-diagnostic-daemon` - build failed

### Test Failures (Non-Blocker)

**internal/fanout**:
1. `TestDispatch_ContextCancellation` - Expected status error for cancelled context, got timeout

**internal/spec**:
7 deprecation lint test failures related to ISO date validation and brownout window validation.

### Successful Packages

- `benches` - PASS
- `benchmarks/baseline` - PASS
- `corpus` - PASS
- `internal/buildinfo` - PASS
- `internal/testutil` - PASS (no tests)
- `internal/testutil/stubupstream` - PASS
- `internal/vault` - PASS
- `internal/version` - PASS

## Root Cause Analysis

The primary issue is in `internal/server` which has been broken since 2026-08-30. This package is a critical dependency for:
- The main `cmd/seam` binary
- All starvation-alert tools
- Integration tests in other packages

The failures fall into three categories:
1. **Type conversion error** - incorrect pointer usage in struct literal
2. **Missing handler methods** - 7 exclusion-related handlers referenced but not defined
3. **Import hygiene** - unused imports and variables

## Next Steps Required

To make the SEAM tree green, the following issues need to be resolved:

1. **Fix lease_leadership.go:179** - Correct the type conversion to use `*int32` properly
2. **Implement 7 missing exclusion handlers in server.go** - Add the missing handler methods
3. **Clean up imports** - Remove unused `"strings"` import and unused test variables
4. **Fix test failures** - Address fanout context cancellation test and spec deprecation lint tests

Each issue should be filed as a separate bead with clear dependencies to ensure proper resolution order.