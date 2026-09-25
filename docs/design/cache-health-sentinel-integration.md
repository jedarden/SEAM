# Cache and Health Sentinel Integration Design

## Document Metadata

- **Created:** 2026-08-09
- **Bead:** seam-6eb6d780 (documentation) — closed; rehydrated from retired bead-forge ID `bf-3787s`
- **Status:** Design Documented

> **Bead ID provenance:** this document was written against the retired
> bead-forge (`bf-*`) store. The workspace was rehydrated into bead-rs on
> 2026-08-14 (commit 9e9b514); the reference above now names the current
> `seam-*` bead, with the original `bf-*` ID retained as provenance. The
> linked bead is closed — it records design-era context, not open work.
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
| `/_seam/healthz` | Liveness probe | `200 OK` with body `"OK"` |
| `/_seam/health` | Liveness probe (served alias) | `200 OK` with body `"OK"` |
| `/_seam/readyz` | Readiness probe | `200 OK` when every readiness dependency passes; `503` with each dependency's state in the body |
| `/health/credentials` | Credential health | `200 OK` JSON with aggregate and per-origin circuit-breaker state |
| `/health/upstreams` | Upstream health | `200 OK` (future: route table health) |

`/_seam/healthz` is the liveness name the control-plane design enumerates:
the plan's reserved-namespace decision (2026-07-20) names `/_seam/healthz`,
`/_seam/readyz` and `/_seam/metrics` as the first `/_seam/` users and its
closed grandfathered enumeration contains no `/_seam/health`. The server
additionally registers `/_seam/health` on the same handler as a served
alias, and neither path is a `reservedPaths` exact entry — both ride the
already-reserved `/_seam/` prefix, so neither required (or got) its own
reservation.

`/health/credentials` is an operator-only, read-only sentinel. It renders a
fresh snapshot of breaker state and sends `Cache-Control: no-store`; it is
also a reserved path, so cache and quota middleware bypass it even if a TTL
is configured for the path. An open breaker is reported as `status: "unhealthy"`,
a half-open breaker as `"degraded"`, and the endpoint remains
HTTP 200 so operators can inspect the structured response. No credential
values are returned.

### Readiness Dependencies (`/_seam/readyz`)

`/_seam/readyz` evaluates a defined dependency set on every request and
answers `503` while any dependency is unmet. The body is a flat JSON map of
booleans — the aggregate `ready` flag plus one key per dependency — so a
probe consumer reading a 503 from Deployment events can tell which dependency
failed without querying further endpoints. Every value is a boolean, keeping
the historical response shape decodable as `map[string]bool`.

| Key | Dependency | Satisfied when |
|-----|------------|----------------|
| `route_table` | Route table loaded | The current route table carries at least one route — at least one valid fragment loaded and merged. A reload that quarantines every fragment takes the pod out of the Service while `/_seam/health` keeps answering. |
| `openbao` | OpenBao login state | The asynchronous startup Kubernetes-auth login has completed. The login runs in the background so an OpenBao outage degrades readiness instead of crash-looping the container — the gate behind the seam-a155e900 503 regressions, kept gating on purpose: a pod that cannot read credentials must not receive traffic. |
| `credential_probe` | Credential-probe freshness | No credential probe is configured, or every tracked probe carries a successful verification no older than twice its configured cadence plus a 5-minute grace. An attached registry with no results yet counts as fresh — probe-loop cold start must not recreate the startup-503 class of regressions. Readiness gates on the freshness of the verification signal, not on any single credential's health; an unhealthy credential is reported at `/health/credentials` and does not by itself remove the pod from the Service. |
| `allowlist` | Allowlist enforcement | Vault-path and upstream-host allowlist enforcement is not fail-closed (no hosts permitted). |

**Deliberately not dependencies.** Per-route circuit-breaker state does not
gate readiness, and neither does any single credential's health. An open
breaker is the definition of a partial degradation: one dead upstream among
many serving routes. Flipping the pod not-ready for it would pull the whole
gateway — pass-through routes and every healthy route included — out of the
Service because one dependency of *some* routes is down, converting a
partial degradation into a total one, the exact outcome the control-plane
design forbids for readiness (`docs/plan/plan.md`: "`/_seam/readyz` is
unaffected by a mid-life outage"). Breaker state surfaces where partial
conditions already live: `/health/credentials` renders per-origin breaker
state, `/health/upstreams` aggregates it per upstream with three-state
last-2xx tracking, and an all-breaker-refused fan-out still collapses to a
503 on the request path. A liveness probe never fails on any of this —
`/_seam/healthz` reports only that the process is alive and its listeners
are bound, since restarting the pod fixes neither a dead upstream nor an
open breaker.

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

When user traffic hits the cache, the request never reaches the quota
middleware at all: `cacheMiddleware` serves the cached response directly and
returns, so no quota check and no deduction happen. (Earlier revisions of this
document described a "check with cost = 0" flow; the shipped chain
short-circuits before `CheckAndRecordQuota` instead. The `cost == 0` branch
inside the quota tracker exists for direct callers, not for this path.)

```go
// From: internal/server/cache_middleware.go — the hit is served HERE; the
// quota middleware downstream never runs for this request.
if cachedResponse, found := s.cache.Get(cacheKey); found {
    s.ensureMetrics().recordCacheHit(metricLabelsFromRequest(r))
    ctx := context.WithValue(r.Context(), cacheHitKey, true)
    r = r.WithContext(ctx)
    s.serveCachedResponse(w, r, cachedResponse, true) // ← writes the response
    return
}
```

**Impact:**
- Quota is **not checked** (the request is short-circuited before the quota middleware)
- Quota is **not deducted**
- `seam_quota_bypassed_total{route}` is recorded once per hit, by `serveCachedResponse`
- Response headers: `X-SEAM-Cache: HIT` and `X-Quota-Bypassed: cache-hit`; the
  admission-time quota headers captured on the charged miss are stripped from
  the replayed response (see the contract note for the exact list)

### Cost Counter Examples

| Scenario | Cost Applied | Quota Checked | Headers | Metrics |
|----------|--------------|---------------|---------|---------|
| Health sentinel probe (`/_seam/health`) | No | No | None | None |
| Cache miss (`/api/users`, first request) | Yes | Yes | `X-Quota-Cost-Per-Call`, `X-Quota-Remaining`, `X-SEAM-Budget-Remaining` | `seam_quota_cost_total` |
| Cache hit (`/api/users`, subsequent request) | No | No (short-circuited before the quota middleware) | `X-SEAM-Cache: HIT`, `X-Quota-Bypassed: cache-hit` | `seam_cache_hits_total`, `seam_quota_bypassed_total` |
| Quota exceeded (`/api/users`, over limit) | N/A | Yes | `Retry-After: 60`, `X-SEAM-Budget-Remaining` | `seam_quota_exceeded_total` |

## Integration Points

### 1. Reserved Path Detection

**Location:** `internal/server/server.go` (`reservedPaths`, `isReservedPath`)

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

**Reserved paths structure** (mirroring `internal/server/server.go`; the
illustration is abridged but every entry shown is real):

```go
var reservedPaths = struct {
    exact    map[string]bool
    prefixes []string
}{
    exact: map[string]bool{
        "/docs":               true,
        "/docs/route":         true,
        "/docs/paths":         true,
        "/openapi.json":       true,
        "/whoami":             true,
        "/scopes":             true,
        "/changes":            true,
        "/health/credentials": true, // Health sentinel: credential status check
        "/health/upstreams":   true, // Health sentinel: upstream connectivity check
        "/config/status":      true,
        // ... plus the exact /api/v1/ control-plane endpoints
    },
    prefixes: []string{
        "/docs/",      // Documentation endpoints (reserved namespace)
        "/health/",    // Health sentinel: all health check endpoints
        "/config/",    // Configuration management endpoints
        "/approvals/", // Approval workflow endpoints (reserved, not served)
        "/_seam/",     // Internal SEAM endpoints (healthz, readyz, metrics, ...)
        // No "/api/v1/" prefix: fragment routes legitimately live there, so
        // the /api/v1/ control-plane endpoints are reserved by exact path.
    },
}
```

Note what is *not* here: `/_seam/health`, `/_seam/healthz` and
`/_seam/readyz` are **not** exact entries. They predate none of the closed
grandfathered enumeration — the plan's reserved-namespace decision
(2026-07-20) fixed that set at `/docs`, `/docs/{route}`, `/openapi.json`,
`/whoami`, `/scopes`, `/changes`, `/health/credentials`,
`/health/upstreams` and `/config/status`, and everything conceived after it
takes the already-reserved `/_seam/` prefix, which is why the healthz,
readyz and metrics endpoints need (and have) no reservation of their own.
This matches the control-plane reserved-path enumeration in
`docs/plan/plan.md`; earlier revisions of this document showed the three
paths as exact entries, which no shipped `reservedPaths` map has ever
contained.

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

**Location:** `internal/server/quota_middleware.go` — the quota middleware's
own cache-hit branch. In the shipped chain this branch is **unreachable**: the
cache middleware serves a hit directly and never invokes the quota middleware.
It is retained for direct composition (and covered by
`TestQuotaBypass_ContextPropagation`); the hit observability callers actually
see comes from `serveCachedResponse` below.

```go
// Cache hits get a special header and metric (defensive; see note above)
if cacheHit {
    w.Header().Set("X-Quota-Bypassed", "cache-hit")
    s.ensureMetrics().recordQuotaBypassed(route)  // ← Prometheus metric
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

The canonical observability contract — exact metric names, label sets and
values, the per-request-class signal matrix, and the header stripping rule —
lives in [`docs/notes/cache-quota-bypass-observability.md`](../notes/cache-quota-bypass-observability.md)
and is pinned by `TestBypassObservability_*` in
`internal/server/bypass_observability_test.go`. Summary:

### Prometheus Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `seam_cache_hits_total` | Counter | `route`, `version` | Total cache hits; `route` is the metric-route context (path template or `unmatched`), not the concrete URL |
| `seam_cache_misses_total` | Counter | `route`, `version` | Total cache misses, including TTL=0 dedup-only lookups |
| `seam_cache_hit_rate` | Gauge | - | Overall cache hit rate (0-1), scraped from the response cache's own counters |
| `seam_quota_bypassed_total` | Counter | `route` | Total quota bypasses due to cache hit; `route` is the concrete request path (the quota key) |
| `seam_quota_cost_total` | Counter | `route` | Total accumulated cost in USD, recorded only when cost > 0 |
| `seam_quota_exceeded_total` | Counter | `route` | Total quota-exceeded refusals (HTTP 429) |
| `seam_quota_remaining` | Gauge | `scope` | **Registered but never populated** — no samples are exposed; do not alert on it |

Reserved-path traffic (control plane, health sentinel) records none of these:
`metricsMiddleware`, `cacheMiddleware` and `quotaMiddleware` each short-circuit
on `isReservedPath` before any series is touched, so probe traffic appears in
no cache, quota or `seam_http_*` family.

### Response Headers

**Cache hit headers** (admission-time quota headers captured on the charged
miss — `X-Quota-Cost-Per-Call`, `X-Quota-Remaining`,
`X-SEAM-Budget-Remaining` — are stripped from the replayed response):
```
X-SEAM-Cache: HIT
X-Quota-Bypassed: cache-hit
```

**Cache miss headers** (a miss carries no `X-SEAM-Cache` header — `HIT` is
the only value ever emitted; `formatCost` trims trailing zeros):
```
X-Quota-Cost-Per-Call: $0.1
X-Quota-Remaining: $0.9
X-SEAM-Budget-Remaining: amount=$0.9 unit=call window=1h resets=<RFC3339>
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
4. **`TestSeamHealthAliasServesSameBodyAsHealthz`** - Pins the served alias: `/_seam/health` is registered on the same handler as `/_seam/healthz` and answers 200 `"OK"` (and refuses non-GET) identically
5. **`TestSeamHealthAliasReceivesReservedPathTreatment`** - Pins the reserved-path treatment of both health names (cache and quota bypass despite a configured TTL and cost) and their deliberate absence from the `reservedPaths` exact enumeration
6. **`TestHealthzAndAliasAnswerWhileReadyzIs503`** - Pins liveness/readiness separation: a quarantined-everything route table 503s `/_seam/readyz` while both health names keep answering
7. **`TestBypassObservability_ReservedRequestsEmitNoSignals`** - Pins the reserved-path observability contract through the production metrics→cache→quota order: zero samples in any `seam_http_*`, `seam_cache_*` or `seam_quota_*` family, no bypass or quota headers, fresh execution and $0 accumulated, with a quota-refused sanity path proving the configuration bites
8. **`TestBypassObservability_CacheHitSignalContract`** - Pins the successful cache-hit contract: bypass headers, stripping of every admission-time quota header (including `X-SEAM-Budget-Remaining`), the hit/miss/bypass/cost metric values, the label-key split between cache and quota families, and that the hit is still counted in `seam_http_requests_total`

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
