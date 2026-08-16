# Cache and Health Sentinel Integration Design

## Document Metadata

- **Created:** 2026-08-09
- **Bead:** bf-3787s (documentation)
- **Status:** Design Documented
- **Related Components:**
  - Cache middleware (`internal/server/cache_middleware.go`)
  - Quota middleware (`internal/server/quota_middleware.go`)
  - Circuit-breaker state registry (`internal/server/circuit_breaker_health.go`)
  - Reserved paths (`internal/server/server.go`)

## Overview

This document explains the interaction between SEAM's caching layer and health sentinel probe traffic, including what gets cached, what doesn't, and how probe traffic affects quota and cost counters.

## Design Principles

1. **Probe Traffic Transparency:** Health sentinel probes never consume quota or cache resources
2. **Cache Hit Bypass:** Successful cache hits bypass quota checking entirely
3. **Control Plane Separation:** Reserved paths are explicitly excluded from caching and quota enforcement
4. **Observability:** All bypass events are tracked with dedicated metrics and headers

## Health Sentinel Probe Traffic

### What is Health Sentinel Traffic?

Health sentinel traffic consists of probes from monitoring systems, load balancers, and orchestration platforms that verify service health and readiness. These probes are:

- **High-frequency:** Often sent every 1-10 seconds
- **Lightweight:** Typically simple GET requests
- **Critical:** Failures trigger pod restarts, load balancer removal, or alerts
- **Internal:** Sent by infrastructure components, not end users

### Health Sentinel Endpoints

SEAM provides several health sentinel endpoints:

| Endpoint | Purpose | Response |
|----------|---------|----------|
| `/_seam/health` | Liveness probe | `200 OK` with body `"OK"` |
| `/_seam/healthz` | Liveness probe (alias) | `200 OK` with body `"OK"` |
| `/_seam/readyz` | Readiness probe | `200 OK` (future: dependency checks) |
| `/health/credentials` | Credential health | `200 OK` JSON with aggregate and per-origin circuit-breaker state |
| `/health/upstreams` | Upstream health | `200 OK` (future: route table health) |

`/health/credentials` is an operator-only, read-only sentinel. It renders a
fresh snapshot of breaker state and sends `Cache-Control: no-store`; it is
also a reserved path, so cache and quota middleware bypass it even if a TTL
is configured for the path. An open breaker is reported as `status: "unhealthy"`,
a half-open breaker as `"degraded"`, and the endpoint remains
HTTP 200 so operators can inspect the structured response. No credential
values are returned.

### Traffic Pattern

```
┌─────────────────┐
│ Kubernetes /    │
│ Load Balancer   │
└────────┬────────┘
         │ HTTP GET /_seam/health
         │ (every 5-10 seconds)
         ▼
┌─────────────────────────┐
│  Reserved Path Check     │ ← isReservedPath("/_seam/health")
│  (Control Plane)         │   returns true
└────────┬────────────────┘
         │ Bypass cache middleware
         │ Bypass quota middleware
         ▼
┌─────────────────────────┐
│  healthzHandler         │ ← Simple handler returning 200 OK
└────────┬────────────────┘
         │ Response: 200 OK
         ▼
┌─────────────────────────┐
│  No quota consumed      │
│  No cache interaction   │
│  No cost applied        │
└─────────────────────────┘
```

## Caching Layer Behavior

### What Gets Cached

The caching layer follows these rules:

1. **Method-based filtering:** Only `GET` requests are cached
2. **Reserved path exclusion:** Reserved paths bypass caching entirely
3. **Route-specific TTL:** Each route can have a configurable cache TTL
4. **TTL=0 means dedup only:** Routes with TTL=0 use single-flight coalescing but don't cache

**Caching Decision Tree:**

```
Incoming Request
       │
       ▼
Is it a GET request?
  ├─ No → Pass through (no caching)
  └─ Yes → Is it a reserved path?
      ├─ Yes → Pass through (no caching)
      └─ No → Check cache TTL
          ├─ TTL > 0 → Check cache → Hit/Miss logic
          └─ TTL = 0 → Single-flight only, no caching
```

### Cache Middleware Flow

```go
// From: internal/server/cache_middleware.go
func (s *Server) cacheMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // 1. Skip non-GET requests
        if !ShouldUseCache(r) {
            next.ServeHTTP(w, r)
            return
        }

        // 2. Skip reserved paths (health sentinel, control plane)
        if isReservedPath(r.URL.Path) {
            next.ServeHTTP(w, r)
            return
        }

        // 3. Generate cache key from method + path + query
        cacheKey := GenerateCacheKey(r.Method, r.URL.Path, r.URL.Query())

        // 4. Check cache
        if cachedResponse, found := s.cache.Get(cacheKey); found {
            // CACHE HIT PATH
            ctx := context.WithValue(r.Context(), cacheHitKey, true)
            r = r.WithContext(ctx)
            s.serveCachedResponse(w, r, cachedResponse, true)
            return
        }

        // 5. Cache miss - use single-flight to coalesce concurrent requests
        ttl := s.getRouteCacheTTL(r.URL.Path)
        result, err, _ := s.singleFlight.Do(r.Context(), cacheKey, func(ctx context.Context) (*cachedResponse, error) {
            return s.executeAndCacheRequest(ctx, next, w, r, cacheKey, ttl)
        })

        // 6. Serve the fresh response
        if result != nil {
            ctx := context.WithValue(r.Context(), cacheHitKey, false)
            r = r.WithContext(ctx)
            s.serveCachedResponse(w, r, result, false)
        }
    })
}
```

### Reserved Paths (Never Cached)

The following paths bypass caching entirely:

**Exact matches:**
- `/docs` - API documentation UI
- `/docs/route` - Route-specific documentation
- `/openapi.json` - OpenAPI specification
- `/whoami` - Authentication debug endpoint
- `/scopes` - Available scopes list
- `/changes` - Changelog
- `/health/credentials` - Credential health check
- `/health/upstreams` - Upstream health check
- `/config/status` - Configuration status

**Prefix matches:**
- `/health/` - All health check endpoints
- `/config/` - All configuration endpoints
- `/approvals/` - Approval workflow endpoints
- `/_seam/` - Internal SEAM endpoints (metrics, health, ready)

## Quota and Cost Counter Behavior

### Quota Bypass Mechanisms

There are **two distinct bypass mechanisms** in SEAM:

#### 1. Reserved Path Bypass (Health Sentinel)

Health sentinel probes bypass quota enforcement entirely:

```go
// From: internal/server/quota_middleware.go
func (s *Server) quotaMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Skip quota for reserved paths (health checks, etc.)
        if isReservedPath(r.URL.Path) {
            next.ServeHTTP(w, r)
            return  // ← No quota check, no cost applied
        }

        // ... normal quota enforcement for non-reserved paths
    })
}
```

**Impact:**
- Quota is **not checked** (not even for validation)
- Quota is **not deducted**
- No metrics recorded for quota checks
- Response headers: No `X-Quota-*` headers

#### 2. Cache Hit Bypass (User Traffic)

When user traffic hits the cache, quota is checked but not deducted:

```go
// From: internal/server/quota_middleware.go
func (s *Server) quotaMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Skip quota for reserved paths
        if isReservedPath(r.URL.Path) {
            next.ServeHTTP(w, r)
            return
        }

        // Check if this is a cache hit
        cacheHit := isCacheHit(r)

        // Get the cost per call for this route
        costPerCall := s.getCostPerCall(route)

        // If cache hit, use zero cost (bypasses quota deduction)
        cost := costPerCall
        if cacheHit {
            cost = 0  // ← Quota check happens, but cost = 0
        }

        // Check quota (cache hits check without deducting)
        allowed, remaining, err := s.quotaTracker.CheckAndRecordQuota(r.Context(), route, cost, token, user)

        // ... rest of quota logic
    })
}
```

**Impact:**
- Quota **is checked** (validation happens)
- Quota is **not deducted** (cost = 0)
- `recordQuotaBypassed(route)` metric is recorded
- Response headers: `X-Quota-Bypassed: cache-hit`

### Cost Counter Examples

| Scenario | Cost Applied | Quota Checked | Headers | Metrics |
|----------|--------------|---------------|---------|---------|
| Health sentinel probe (`/_seam/health`) | No | No | None | None |
| Cache miss (`/api/users`, first request) | Yes | Yes | `X-Quota-Cost-Per-Call`, `X-Quota-Remaining` | `metricQuotaCost` |
| Cache hit (`/api/users`, subsequent request) | No | Yes | `X-Quota-Bypassed: cache-hit` | `metricQuotaBypassed` |
| Quota exceeded (`/api/users`, over limit) | N/A | Yes | `Retry-After: 60` | `metricQuotaExceeded` |

## Integration Points

### 1. Reserved Path Detection

**Location:** `internal/server/server.go:44`

```go
// isReservedPath checks if a given path is in the reserved control-plane set.
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

**Reserved paths structure:**
```go
var reservedPaths = struct {
    exact    map[string]bool
    prefixes []string
}{
    exact: map[string]bool{
        "/_seam/health":     true,  // Health sentinel
        "/_seam/healthz":   true,  // Health sentinel (alias)
        "/_seam/readyz":    true,  // Readiness probe
        "/openapi.json":    true,
        "/docs":            true,
        // ... more exact paths
    },
    prefixes: []string{
        "/health/",         // All health endpoints
        "/config/",         // All config endpoints
        "/_seam/",          // All internal endpoints
        // ... more prefixes
    },
}
```

### 2. Cache Middleware Integration

**Location:** `internal/server/cache_middleware.go:26`

```go
// Skip caching for reserved paths (control plane endpoints)
if isReservedPath(r.URL.Path) {
    next.ServeHTTP(w, r)
    return  // ← Health sentinel traffic passes through unchanged
}
```

### 3. Quota Middleware Integration

**Location:** `internal/server/quota_middleware.go:24`

```go
// Skip quota for reserved paths (health checks, etc.)
if isReservedPath(r.URL.Path) {
    next.ServeHTTP(w, r)
    return  // ← Health sentinel traffic bypasses quota entirely
}
```

### 4. Cache Hit Quota Bypass

**Location:** `internal/server/quota_middleware.go:30`

```go
// Check if this is a cache hit from the context set by cache middleware
cacheHit := isCacheHit(r)

// Get the cost per call for this route
costPerCall := s.getCostPerCall(route)

// If cache hit, use zero cost (bypasses quota deduction)
cost := costPerCall
if cacheHit {
    cost = 0  // ← Cache hits pay no quota cost
    log.Printf("[Quota] Cache hit for %s - bypassing quota deduction", route)
}
```

### 5. Quota Bypass Headers and Metrics

**Location:** `internal/server/quota_middleware.go:73`

```go
// Cache hits get a special header and metric
if cacheHit {
    w.Header().Set("X-Quota-Bypassed", "cache-hit")
    recordQuotaBypassed(route)  // ← Prometheus metric
}
```

**Location:** `internal/server/cache_middleware.go:137`

```go
// Add cache status header only if this is an actual cache hit
if isActualHit {
    w.Header().Set("X-SEAM-Cache", "HIT")
    w.Header().Set("X-Quota-Bypassed", "cache-hit")
    // Remove quota cost headers for cache hits
    w.Header().Del("X-Quota-Cost-Per-Call")
    w.Header().Del("X-Quota-Remaining")
    // Record metrics
    recordCacheHit(r.URL.Path)
    recordQuotaBypassed(r.URL.Path)  // ← Prometheus metric
}
```

## Metrics and Observability

### Prometheus Metrics

All bypass events are tracked with dedicated Prometheus metrics:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `seam_cache_hits_total` | Counter | `route` | Total cache hits by route |
| `seam_cache_misses_total` | Counter | `route` | Total cache misses by route |
| `seam_cache_hit_rate` | Gauge | - | Overall cache hit rate (0-1) |
| `seam_quota_bypassed_total` | Counter | `route` | Total quota bypasses due to cache hit |
| `seam_quota_cost_total` | Counter | `route` | Total accumulated cost in USD |
| `seam_quota_exceeded_total` | Counter | `route` | Total quota exceeded errors |

### Response Headers

**Cache hit headers:**
```
X-SEAM-Cache: HIT
X-Quota-Bypassed: cache-hit
```

**Cache miss headers:**
```
X-SEAM-Cache: MISS
X-Quota-Cost-Per-Call: $0.10
X-Quota-Remaining: $0.90
```

**Health sentinel headers (no special headers):**
```
HTTP/1.1 200 OK
Content-Type: text/plain

OK
```

## Traffic Flow Comparison

### Health Sentinel Probe Flow

```
┌──────────────────────┐
│ Kubelet / Load       │
│ Balancer Health      │
│ Check                │
└──────────┬───────────┘
           │ GET /_seam/health
           ▼
┌──────────────────────────────────┐
│ isReservedPath("/_seam/health")? │ → TRUE
└──────────┬───────────────────────┘
           │
           ├──────────────────────────────────────┐
           │                                      │
           ▼                                      ▼
┌─────────────────────┐              ┌─────────────────────┐
│ Cache Middleware    │              │ Quota Middleware    │
│ Bypass (Reserved)   │              │ Bypass (Reserved)   │
│ No cache lookup     │              │ No quota check      │
└─────────┬───────────┘              └─────────┬───────────┘
           │                                    │
           └────────────────┬───────────────────┘
                            │
                            ▼
                   ┌─────────────────┐
                   │ healthzHandler  │
                   │ Return 200 OK   │
                   └─────────┬───────┘
                             │
                             ▼
                   ┌─────────────────────┐
                   │ No quota consumed  │
                   │ No cache interaction│
                   │ No cost applied     │
                   │ No metrics recorded │
                   └─────────────────────┘
```

### Cached User Request Flow (Hit)

```
┌──────────────────────┐
│ Client Request      │
│ GET /api/users      │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────────────────┐
│ isReservedPath("/api/users")?    │ → FALSE
└──────────┬───────────────────────┘
           │
           ▼
┌─────────────────────┐
│ Cache Middleware    │
│ Cache Key Lookup    │
│ Cache HIT!          │
└─────────┬───────────┘
           │
           ├──────────────────────────────────┐
           │ Set context: cacheHitKey = true  │
           └──────────────────────────────────┘
           │
           ▼
┌─────────────────────┐
│ Quota Middleware    │
│ Check cache hit     │
│ context → true      │
│ cost = 0            │
└─────────┬───────────┘
           │
           ▼
┌────────────────────────────────┐
│ Check quota with cost = 0      │
│ Record quota bypass metric     │
│ Set bypass headers             │
└─────────┬──────────────────────┘
           │
           ▼
┌─────────────────────┐
│ Return Cached      │
│ Response           │
│ Headers:           │
│ X-SEAM-Cache: HIT  │
│ X-Quota-Bypassed:  │
│   cache-hit        │
└─────────────────────┘
```

### Uncached User Request Flow (Miss)

```
┌──────────────────────┐
│ Client Request      │
│ GET /api/users      │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────────────────┐
│ isReservedPath("/api/users")?    │ → FALSE
└──────────┬───────────────────────┘
           │
           ▼
┌─────────────────────┐
│ Cache Middleware    │
│ Cache Key Lookup    │
│ Cache MISS          │
└─────────┬───────────┘
           │
           ├──────────────────────────────────┐
           │ Set context: cacheHitKey = false │
           │ Execute via single-flight        │
           │ Cache response (if TTL > 0)      │
           └──────────────────────────────────┘
           │
           ▼
┌─────────────────────┐
│ Quota Middleware    │
│ Check cache hit     │
│ context → false     │
│ cost = $0.10        │
└─────────┬───────────┘
           │
           ▼
┌────────────────────────────────┐
│ Check quota with cost = $0.10  │
│ Deduct from quota              │
│ Record quota cost metric       │
│ Set quota headers              │
└─────────┬──────────────────────┘
           │
           ▼
┌─────────────────────┐
│ Execute Upstream    │
│ Return Fresh        │
│ Response            │
│ Headers:            │
│ X-Quota-Cost-Per-   │
│   Call: $0.10       │
│ X-Quota-Remaining:  │
│   $0.90             │
└─────────────────────┘
```

## Design Rationale

### Why Health Sentinel Probes Bypass Everything

1. **High Frequency:** Health probes run every 1-10 seconds, accumulating thousands of calls per hour
2. **Zero Business Value:** Probes don't serve user requests or provide business value
3. **Infrastructure Function:** Probes are for the orchestrator, not the application
4. **Quota Distortion:** If probes consumed quota, they'd crowd out legitimate user traffic
5. **Cache Pollution:** Health responses change too frequently to be useful cache entries

### Why Cache Hits Still Check Quota

1. **Validation:** Ensures the caller hasn't been deactivated or exceeded their limit
2. **Audit Trail:** Every request (even cache hits) is validated against quota policy
3. **Future-Proofing:** Allows per-request quotas (not just per-dollar) in future
4. **Metric Accuracy:** Distinguishes between "allowed but cached" vs "not allowed"

### Why Two Different Bypass Mechanisms

**Reserved path bypass** (health sentinel):
- Complete bypass of both cache and quota layers
- No metrics, no headers, no overhead
- For infrastructure control plane endpoints only

**Cache hit bypass** (user traffic):
- Quota validation happens, but cost = 0
- Metrics and headers recorded for observability
- Optimizes legitimate user requests without sacrificing validation

## Testing and Validation

### Integration Tests

1. **`TestQuotaEnforcement_ReservedPaths`** - Verifies reserved paths bypass quota
2. **`TestCacheMiddleware_Integration_ReservedPathsBypass`** - Verifies reserved paths bypass cache
3. **`TestQuotaEnforcement_CacheMissIntegration`** - Verifies cache hits bypass quota deduction

### Manual Testing

```bash
# Health sentinel probe (bypasses everything)
curl -i http://localhost:8080/_seam/health
# Expected: 200 OK, no cache/quota headers

# Cached endpoint (first request, cache miss)
curl -i http://localhost:8080/api/test
# Expected: X-SEAM-Cache: MISS, X-Quota-Cost-Per-Call: $0.10

# Cached endpoint (second request, cache hit)
curl -i http://localhost:8080/api/test
# Expected: X-SEAM-Cache: HIT, X-Quota-Bypassed: cache-hit
```

## Future Considerations

### Potential Enhancements

1. **Per-Caller Health Endpoints:** Custom health probes per caller configuration
2. **Dependency Health Checks:** `/health/upstreams` checks route table connectivity
3. **Cache Warming:** Proactive cache population for high-traffic endpoints
4. **Conditional Bypass:** Configurable bypass for additional control plane paths

### Monitoring Alerts

Consider alerting on:
- High cache bypass rate (indicates cache warming issues)
- Health check latency (indicates handler performance issues)
- Quota bypass percentage (indicates cache effectiveness)

## References

- **Cache Implementation:** `internal/server/cache.go`
- **Cache Middleware:** `internal/server/cache_middleware.go`
- **Quota Middleware:** `internal/server/quota_middleware.go`
- **Metrics:** `internal/server/metrics.go`
- **Server Routes:** `internal/server/server.go`
- **Integration Tests:** `internal/server/*_integration_test.go`
