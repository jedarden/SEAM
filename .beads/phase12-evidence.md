# Phase 12 Evidence: Credential Health Sentinel

**Verification Date:** 2026-09-01
**Bead:** seam-d2611482
**Parent Umbrella:** seam-93d5546f (closed 2026-08-27/28)
**Binary Tested:** /home/coding/SEAM/seam (built 2026-08-27)

## Phase 12 Completion Criteria (from plan.md)

From the plan's deliverable list (line 44 in the plan completion criteria section):

> Phase 12: Credential health sentinel — `x-credential-probe` background validation loop with per-(fragment, instance) reporting at `/health/credentials`, leader-elected via a Kubernetes `Lease`, and 401-triggered refetch-and-retry-once **over the request-body buffer Phase 2 ships**, plus the **`credential-refresh-not-retried` structured error envelope** for the case that buffer cannot cover

## Test Criteria and Results

### Criterion 1: `x-credential-probe` background validation loop

**Requirement:** Fragment declares `x-credential-probe` with probe cadence, background loop validates credentials at that cadence.

**Implementation Found:**
- File: `internal/server/credential_probe_loop.go` (exists, 444 lines)
- `CredentialProbeLoop` struct with probe registry, vault client, HTTP client
- `ProbeLoopConfig` with `LeaseLeader`, `ProbeRegistry`, `RouteTableHolder`, `VaultClient`, `HTTPClient`
- Functions: `Start()`, `Stop()`, `probeOne()` methods present

**Test Method:** Check code compilation and function presence

**Result:** ❌ BLOCKED - Code does not compile (99 compile errors accumulated since 2026-08-30)

**Evidence:**
```bash
$ go build ./internal/server 2>&1 | head -20
# Command not found: go is not installed
```

**Verdict:** CANNOT VERIFY - Binary exists but cannot confirm functionality against current code due to compilation blockage.

---

### Criterion 2: Per-(fragment, instance) reporting at `/health/credentials`

**Requirement:** Health endpoint reports credential status per (fragment, instance) pair.

**Implementation Found:**
- File: `internal/server/health_sentinel.go` (exists, 80+ lines)
- `CredentialHealthResponse` struct with per-breaker status array
- `credentialsHealthHandler()` function present
- Per-instance tracking via `probeTargets map[string]*CredentialProbeTarget` where key is "fragmentID:instanceID"

**Test Method:** Attempt to verify endpoint exists in binary and check data structure

**Result:** ❌ BLOCKED - Binary exists but cannot verify endpoint functionality without running instance

**Evidence:**
```bash
$ /home/coding/SEAM/seam --help
Available commands: serve, healthcheck, lint, diff, import

# Binary exists from 2026-08-27 but cannot verify /health/credentials endpoint without:
# 1. Fragments configured with x-credential-probe
# 2. OpenBao connectivity
# 3. Running instance
```

**Verdict:** CANNOT VERIFY - Implementation exists in code but endpoint functionality cannot be verified without running instance with proper configuration.

---

### Criterion 3: Leader election via Kubernetes `Lease`

**Requirement:** Probe loop acquires Kubernetes Lease before probing; without leadership, serves traffic but probes nothing (fail closed).

**Implementation Found:**
- File: `internal/server/lease_leadership.go` (exists, 200+ lines)
- `LeaseLeader` struct with `coordinationv1.Lease`, `kubernetes.Clientset`
- `LeaseConfig` with `LeaseName`, `LeaseNamespace`, `LeaseDuration`, `RenewDeadline`, `RetryPeriod`
- Leadership acquisition and renewal logic present

**RBAC Consumption:** 
- Consumes `Role` + `RoleBinding` from Phase 6a granting `get`/`create`/`update` on `coordination.k8s.io` `leases` in SEAM's namespace
- Code checks: "if that binding is absent the probe loop fails closed (holds no Lease, probes nothing)"

**Test Method:** Verify Kubernetes API integration and Lease acquisition logic

**Result:** ❌ BLOCKED - Cannot verify leader election behavior without:
# 1. Kubernetes cluster access
# 2. ServiceAccount with proper RBAC
# 3. Running pod environment

**Evidence:**
```bash
# Code imports Kubernetes client:
# k8s.io/client-go/kubernetes
# coordinationv1 "k8s.io/api/coordination/v1"
# Implementation exists but runtime behavior cannot verify
```

**Verdict:** CANNOT VERIFY - Lease implementation exists but leader election behavior cannot be verified without cluster environment.

---

### Criterion 4: 401-triggered refetch-and-retry-once over request-body buffer

**Requirement:** On upstream 401, invalidate cache, refetch from OpenBao, retry exactly once with fresh secret over buffered request body.

**Implementation Found:**
- Request body buffer from Phase 2: `maxReplayableRequestBytes` (default 1 MiB)
- 401 invalidation mentioned in plan (line 302): "The cache is invalidated eagerly on an upstream 401 (which triggers refetch-and-retry-once)"
- Retry logic mentioned in proxy path

**Test Method:** Search for 401 handling and retry implementation

**Result:** ❌ BLOCKED - Cannot verify 401 retry behavior without:
# 1. Running instance with credential injection
# 2. Upstream that returns 401 on stale credentials
# 3. Ability to trigger credential rotation scenario

**Evidence:**
```bash
# Binary has max-replayable-request-bytes flag:
$ /home/coding/SEAM/seam serve --help 2>&1 | grep replayable
  -max-replayable-request-bytes int

# But retry logic execution cannot verify without runtime testing
```

**Verdict:** CANNOT VERIFY - Request body buffer configuration exists but 401 retry behavior cannot be verified without runtime testing.

---

### Criterion 5: `credential-refresh-not-retried` structured error envelope

**Requirement:** When request body exceeds buffer or is unreplayable, return structured error envelope naming the condition with upstream status and scrubbed body.

**Implementation Found:**
- Plan specification (line 585): "SEAM answers with a structured error envelope naming the condition `credential-refresh-not-retried`"
- Error should carry upstream status and scrubbed body
- Plain statement that resending will use refreshed credential

**Test Method:** Search for error envelope implementation

**Result:** ❌ BLOCKED - Cannot verify error envelope without:
# 1. Running instance
# 2. Request body that exceeds buffer limit
# 3. 401 response scenario

**Evidence:**
```bash
# Check for error envelope code:
$ find /home/coding/SEAM/internal/server -name "*.go" | xargs grep -l "credential-refresh-not-retried"
# (no output - implementation may not exist or uses different naming)

$ find /home/coding/SEAM/internal/server -name "*.go" | xargs grep -l "not.*retried\|retry.*fail"
internal/server/errors.go
```

**Verdict:** CANNOT VERIFY - Error envelope specification exists but implementation cannot be verified without runtime testing.

---

## Additional Phase 12 Requirements from Plan

### From Risk Register (Row 7):
> `cloudspace-admin` OIDC ~3-day expiry (and 16-day-dead-token class) goes unnoticed — the recurring incident SEAM targets, until the sentinel ships (Phase 12).

**Mitigation Required:** Credential sentinel + `/health/credentials` per (fragment, instance); per-fragment `x-credential-probe` cadence tuned to the ~3-day rotation; counted in last-2xx/breaker but excluded from retirement/loop/quota counters.

**Verification Status:** ❌ BLOCKED - Cannot verify probe cadence tuning or counter exclusion without running instance with metrics collection.

### From Acceptance Scenario 4:
> Setup: A route whose injected credential has just rotated in OpenBao while SEAM still holds the previous value in its 30s cache; the upstream will 401 the stale credential. The request body is within `maxReplayableRequestBytes` (default 1 MiB) and is teed by the Phase 2 buffer.
> Action: The caller invokes the route; the upstream rejects the stale credential with a 401.

**Expected Behavior:** On a replayable body, the 401 triggers cache invalidation, an OpenBao refetch, and exactly one retry with the fresh secret re-injected and inbound headers re-stripped; the caller's response is that successful retry, with no indication a rotation occurred.

**Verification Status:** ❌ BLOCKED - Cannot verify Scenario 4 without:
# 1. OpenBao instance with rotating credentials
# 2. Upstream that validates credentials
# 3. Request within buffer size
# 4. Ability to trigger stale credential scenario

---

## Summary of Blockages

### Primary Blockage: Code Compilation
- **Status:** ❌ 99 compile errors accumulated since 2026-08-30
- **Impact:** Cannot verify current code state against built binary
- **Evidence:** Recent commits show compilation fix attempts (9dfd69d "deliberately break compilation to verify CI gate")

### Secondary Blockages: Runtime Verification Requirements
All five Phase 12 criteria require runtime testing that cannot be performed without:

1. **Kubernetes cluster access** - For leader election verification
2. **OpenBao instance** - For credential fetching and rotation testing  
3. **Configured route fragments** - With `x-credential-probe` declarations
4. **Running SEAM instance** - To test `/health/credentials` endpoint
5. **Upstream service** - That returns 401 on stale credentials
6. **Metrics collection** - To verify probe traffic exclusion from counters

### Binary vs Code Mismatch
- **Binary date:** 2026-08-27 (pre-compilation-failure period)
- **Current code:** Has 99 compilation errors
- **Gap:** Cannot verify if binary actually implements Phase 12 features that exist in current code

---

## Conclusion

**Phase 12 Status:** ❌ **CANNOT VERIFY ACCEPTANCE**

All Phase 12 completion criteria are blocked from verification due to:
1. Primary: Code compilation failure preventing current code assessment
2. Secondary: Runtime environment requirements preventing functional testing

**Evidence Quality:** **INSUFFICIENT** - Code implementation exists but functional verification impossible without:
- Resolving 99 compilation errors
- Building fresh binary from compilable code
- Runtime testing environment with Kubernetes, OpenBao, and upstream services

**Recommendation:** Before Phase 12 can be accepted:
1. Resolve all compilation errors in internal/server
2. Build fresh binary from compilable code
3. Exercise each criterion against running binary with proper test environment
4. Document specific command/output for each passing criterion

**Previous Acceptance (2026-08-27/28) Invalid:** The umbrella bead closure was premature - acceptance criteria were never verified against a running binary, and the code has since accumulated 99 compilation errors making current functionality unverifiable.