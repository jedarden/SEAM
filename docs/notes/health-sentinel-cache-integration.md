# Health Sentinel and Cache Integration

## Overview

Health sentinel probes and other control-plane endpoints have special handling in SEAM's middleware stack. This document explains how health probe traffic interacts with the caching layer, quota enforcement system, and circuit-breaker state.

## Health Sentinel Probe Traffic Patterns

Health sentinel probes are infrastructure health checks from Kubernetes, load balancers, and orchestrators. They expect instant, reliable responses and must never be rate-limited or cached.

### Standard Health Sentinel Paths

SEAM recognizes these paths as health sentinel probes:

- **`/_seam/health`** - General health check endpoint
- **`/_seam/healthz`** - Liveness probe (returns 200 OK)
- **`/_seam/readyz`** - Readiness probe (returns JSON with `ready: true`)
- **`/health/*`** - Health check endpoints (prefix match)
  - `/health/credentials` - Credential status check
  - `/health/upstreams` - Upstream connectivity check

### Other Control-Plane Paths

Additional paths that bypass normal processing:

- **`/_seam/*`** - Internal SEAM control plane (metrics, cache status, etc.)
- **`/docs`** - API documentation UI
- **`/docs/route`** - Route-specific documentation
- **`/openapi.json`** - OpenAPI specification
- **`/config/*`** - Configuration management endpoints
- **`/approvals/*`** - Approval workflow endpoints

## Cache Integration

### What Gets Cached

✅ **Cached** (GET requests with TTL > 0):
- Regular API endpoints with configured cache TTL
- Successfully cached responses are marked with header: `X-SEAM-Cache: HIT`

❌ **NOT Cached** (bypass cache middleware entirely):
- All health sentinel probe paths (`/_seam/health*`, `/health/*`)
- Control-plane endpoints (`/docs`, `/openapi.json`, `/_seam/*`)
- Non-GET requests (POST, PUT, DELETE, etc.)
- 5xx server error responses (evict existing cache entries instead)
- Requests with TTL = 0 (deduplication only via single-flight)

### Cache Behavior for Health Probes

Health sentinel probes bypass the cache middleware completely:

```
Request → Cache Middleware → Reserved Path Check → Bypass Cache → Execute Fresh
```

**Key behaviors:**
1. **No cache lookup** - Health probes never check the cache
2. **No cache storage** - Health probe responses are never stored
3. **No cache metrics** - No hit/miss metrics recorded for health probe traffic
4. **Always fresh execution** - Each health probe executes the handler directly
5. **No cache headers** - Responses never include `X-SEAM-Cache` headers

**Why this matters:**
- Health probes must detect actual system health, not stale cached responses
- Caching health checks would mask failures (e.g., cached "OK" during actual outage)
- Cache metrics should reflect application traffic, not infrastructure noise

## Circuit-breaker State

`/health/credentials` is a read-only operator health sentinel. It renders a
fresh snapshot of the in-process, origin-keyed circuit-breaker registry on each
GET request. The response includes an aggregate `circuit_breaker` object and,
when origins have been registered, the corresponding `circuit_breakers` list.
Only operational metadata is returned; credential values are never included.

```json
{
  "status": "unhealthy",
  "credentials": {"available": true},
  "circuit_breaker": {
    "enabled": true,
    "state": "open",
    "consecutive_failures": 5,
    "retry_after_seconds": 21
  },
  "circuit_breakers": [
    {
      "origin": "https://upstream.example",
      "state": "open",
      "enabled": true,
      "consecutive_failures": 5,
      "source": "caller"
    }
  ]
}
```

An open breaker makes the credential health status `unhealthy`; a half-open
breaker makes it `degraded`. The endpoint still returns HTTP 200 so operators
can inspect the structured state. It sends `Cache-Control: no-store` and is a
reserved path, so a state transition is visible immediately and never becomes
a stale cached health response. The endpoint itself only reads state: breaker
admission, failure counting, and probe labeling remain responsibilities of the
circuit-breaker and credential-sentinel implementations.

The registry is process-local and restart-scoped. A future per-origin breaker
publishes updates through the server's circuit-breaker state registry; this
keeps health reporting separate from request caching while preserving the
state needed by operators.

## Quota/Cost Counter Integration

### Impact on Quota Counters

Health sentinel probes bypass quota checking entirely:

✅ **No quota deduction for:**
- All health sentinel probe paths
- Control-plane endpoints
- Any reserved path

❌ **Quota deducted for:**
- Regular API endpoints (unless cache hit)

### Cost Accumulation Behavior

| Traffic Type | Cost Deduction | Headers Added | Metrics Recorded |
|--------------|-----------------|----------------|------------------|
| Health probes (reserved paths) | **None** | No quota headers | No cost metrics |
| Cache hits (regular routes) | **None** | `X-Quota-Bypassed: cache-hit` | `seam_quota_bypassed_total` |
| Cache misses (regular routes) | **Full cost** | `X-Quota-Cost-Per-Call`, `X-Quota-Remaining` | `seam_quota_cost_total` |

### Quota Bypass Logic

The quota middleware implements two bypass mechanisms:

1. **Reserved path bypass** (`isReservedPath()` check):
   - Health probes never reach quota checking logic
   - Zero cost deduction at middleware entry
   - No quota headers in response

2. **Cache hit bypass** (`isCacheHit()` check):
   - Regular routes with cache hits bypass quota deduction
   - Cost set to 0 for quota checking (full check, no deduction)
   - Response includes `X-Quota-Bypassed: cache-hit` header

## Middleware Pipeline

Request flow through SEAM's middleware stack:

```
┌─────────────────────────────────────────────────────────────────┐
│                     Caller-Facing Port                           │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  1. Capture Middleware (if enabled)                             │
│     └─ Bypasses: Reserved paths (health probes, control plane)  │
│                                                                  │
│  2. Validation Middleware                                        │
│     └─ Validates: OpenAPI spec, authentication                  │
│                                                                  │
│  3. Cache Middleware                                             │
│     ┌─ Reserved path check → Bypass cache entirely               │
│     └─ Regular GET request → Check cache → Single-flight          │
│                                                                  │
│  4. Quota Middleware                                             │
│     ├─ Reserved path check → Bypass quota entirely              │
│     └─ Regular request → Cache hit → Cost = 0 (bypass deduction)│
│                           Cache miss → Full quota check          │
│                                                                  │
│  5. Route Handler (mux.ServeHTTP)                                │
│     └─ Reserved paths → Static handlers (healthz, readyz)      │
│     └─ Regular routes → Route table lookup → Proxy to upstream  │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

## Code Integration Points

### Cache Middleware Bypass

**File:** `internal/server/cache_middleware.go` (lines 26-35)

```go
// Skip caching for reserved paths (control plane endpoints)
if isReservedPath(r.URL.Path) {
    next.ServeHTTP(w, r)
    return
}
```

**Effect:** Health probes never reach cache lookup/storage logic.

### Quota Middleware Bypass

**File:** `internal/server/quota_middleware.go` (lines 23-27)

```go
// Skip quota for reserved paths (health checks, etc.)
if isReservedPath(r.URL.Path) {
    next.ServeHTTP(w, r)
    return
}
```

**Effect:** Health probes never reach quota checking logic.

### Capture Middleware Bypass

**File:** `internal/server/capture.go` (lines 86-96)

```go
// Skip capture for reserved paths
if isReservedPath(r.URL.Path) {
    next.ServeHTTP(w, r)
    return
}
```

**Effect:** Health probes are not captured to corpus.

### Reserved Path Detection

**File:** `internal/server/server.go` (lines 75-87)

```go
func isReservedPath(path string) bool {
    // Check exact matches first
    if reservedPaths.exact[path] {
        return true
    }
    // Check prefix matches
    for _, prefix := range reservedPaths.prefixes {
        if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
            return true
        }
    }
    return false
}
```

## Metrics Impact

### Metrics NOT Recorded for Health Probes

❌ **No cache metrics:**
- `seam_cache_hits_total` - Not incremented for health probes
- `seam_cache_misses_total` - Not incremented for health probes

❌ **No quota metrics:**
- `seam_quota_cost_total` - No cost accumulated for health probes
- `seam_quota_bypassed_total` - Not incremented for reserved paths (only cache hits)
- `seam_quota_exceeded_total` - Not applicable (bypassed entirely)

### Metrics Recorded for Health Probes

✅ **Process metrics:**
- Go runtime metrics (via Prometheus handler)
- HTTP request latency (if instrumented at server level)

## Testing

### Integration Tests

**Test file:** `internal/server/quota_enforcement_integration_test.go` (lines 400-418)

Tests that reserved paths bypass quota enforcement even when quota is exceeded.

**Test file:** `internal/server/cache_integration_test.go` (lines 481-514)

Tests that reserved paths always bypass cache, even when TTL is configured.

**Test file:** `internal/server/health_sentinel_test.go`

Tests that `/health/credentials` reports a live breaker transition when
wrapped in the cache middleware, without adding cache entries, hit/miss
counters, or `X-SEAM-Cache` headers.

### Test Coverage

✅ **Verified behaviors:**
1. Health probes bypass cache middleware (no cache lookup/storage)
2. Health probes bypass quota middleware (no quota checking)
3. Multiple health probe requests always execute fresh (no caching)
4. Health probe responses lack cache headers
5. Reserved paths succeed even when quota is exceeded for regular routes
6. `/health/credentials` reports open and half-open breaker state without
   caching the response

## Design Rationale

### Why Health Probes Bypass Cache

1. **Freshness guarantee** - Health checks must reflect current system state
2. **Failure detection** - Caching would mask actual service degradation
3. **Infrastructure semantics** - K8s/liveness probes expect immediate execution
4. **Metrics purity** - Cache metrics should reflect application traffic patterns

### Why Health Probes Bypass Quota

1. **Infrastructure reliability** - Health probes must not be rate-limited
2. **Orchestrator expectations** - K8s expects health checks to always succeed
3. **Cost separation** - Infrastructure overhead vs. application usage
4. **Operational safety** - Prevents health check quota exhaustion from taking down service

## Related Documentation

- `docs/notes/route-fragment-schema.md` - Cache TTL configuration via `x-cache-ttl`
- `docs/notes/bf-19bc-summary.md` - Quota enforcement via `x-quota`
- `internal/server/cache_middleware.go` - Cache implementation
- `internal/server/quota_middleware.go` - Quota enforcement implementation
- `internal/server/circuit_breaker_health.go` - Breaker state registry
- `internal/server/health_sentinel.go` - Credential health response
- `internal/server/server.go` - Reserved path definitions and detection

## Implementation Reference

- **Bead:** `bf-3787s` - Documentation task for health sentinel and cache integration
- **Dependencies:** Requires cache hit bypass wired into cost-governed route checks
- **Status:** ✅ Complete (health state, cache bypass, tests, and interaction documented)
