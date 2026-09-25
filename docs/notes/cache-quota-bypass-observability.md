# Cache and Quota Bypass Observability Contract

## Document Metadata

- **Created:** 2026-09-24
- **Bead:** seam-8856c2b2
- **Status:** Contract pinned by tests
- **Companion design doc:** `docs/design/cache-health-sentinel-integration.md` (interaction rationale)
- **Pinning tests:** `internal/server/bypass_observability_test.go` (`TestBypassObservability_ReservedRequestsEmitNoSignals`, `TestBypassObservability_CacheHitSignalContract`)

This note is the canonical statement of *which observable signals — Prometheus
series, response headers, quota state — each request class produces*. The
design doc explains why the bypasses exist; it historically named the signals
only loosely (wrong label sets, a `MISS` header value that is never emitted,
example header values that `formatCost` would never render). Where the two
disagree, this note wins.

## Request classes

| Class | Definition | Examples |
|---|---|---|
| **Control-plane** | `isReservedPath` exact matches that are served | `/docs`, `/docs/route`, `/docs/paths`, `/openapi.json`, `/whoami`, `/scopes`, `/changes`, `/health/credentials`, `/health/upstreams`, `/config/status` |
| **Health sentinel** | The `/_seam/` reserved prefix | `/_seam/health`, `/_seam/healthz`, `/_seam/readyz`, `/_seam/metrics` |
| **Reserved prefix** | Any path under a reserved prefix: `/docs/`, `/health/`, `/config/`, `/approvals/`, `/_seam/` | `/health/deep/nested`, `/config/anything`, `/approvals/pending` |
| **Successful cache hit** | Non-reserved GET served from the response cache | second `GET` on a TTL>0 route |
| Charged classes, for contrast | Cache miss, non-GET, quota refusal | first `GET` on a route; `POST`; `GET` over limit |

Classes 1–3 are the same predicate (`isReservedPath`, `internal/server/server.go`)
evaluated at three layers; they are listed separately because deployments reach
them on different ports (see coverage below), not because the gate differs.

## Where the signals are produced

Caller-port chain, outermost first (the layers that carry signals in bold):

```
version/request-id → Cloudflare JWT → capture →
  metricsMiddleware → validation → scope-version → authorization →
  identity → header-stripping → brownout →
  cacheMiddleware → quotaMiddleware → caller mux
```

- **metricsMiddleware** (`server.go`) short-circuits reserved paths before any
  label context is attached or any counter child is created: reserved traffic
  appears in **no** `seam_http_*` or `seam_route_version_*` series.
- **cacheMiddleware** (`cache_middleware.go`) short-circuits reserved paths
  before any cache lookup: no `seam_cache_*` series, nothing stored.
- **quotaMiddleware** (`quota_middleware.go`) short-circuits reserved paths
  before any check or charge: no `seam_quota_*` series, no `X-Quota-*` headers,
  `$0` accumulated.

Operator-port chain: the cache, quota and metrics middleware are **not wired
at all** (`Server.Start`). Every operator-port endpoint — `/_seam/metrics`,
`/config/status`, `/health/credentials`, `/health/upstreams`, `/_seam/capture/*`,
`/_seam/cache/*` — is therefore signal-free by construction, and the metrics
scrape is never self-counted.

## Signal matrix

| Request class | `seam_http_*` series | Cache series | Quota series | Response headers (beyond the always-on version headers) | Quota state |
|---|---|---|---|---|---|
| Control-plane / health sentinel / reserved prefix (either port) | none | none | none | none of the bypass or quota headers | unchanged ($0) |
| Cache miss, cost > 0 | counted, `status="200"` | `seam_cache_misses_total` +1 | `seam_quota_cost_total` += cost | `X-Quota-Cost-Per-Call`, `X-Quota-Remaining`, `X-SEAM-Budget-Remaining` | accumulated += cost |
| Cache miss, cost = 0 (no cost configured) | counted | `seam_cache_misses_total` +1 | none (no charge, no headers) | none of the quota headers | unchanged |
| Successful cache hit | counted, `status="200"` | `seam_cache_hits_total` +1 | `seam_quota_bypassed_total` +1 | `X-SEAM-Cache: HIT`, `X-Quota-Bypassed: cache-hit`; every admission-time quota header stripped | unchanged |
| TTL=0 lookup (dedup-only) | counted | `seam_cache_misses_total` +1 per request | charged like a miss | like a miss | accumulated += cost |
| Non-GET | counted | none (cache never consulted) | charged like a miss | like a miss | accumulated += cost |
| Quota refusal | counted, `status="429"` | miss recorded (the lookup happened) | `seam_quota_exceeded_total` +1 | `Retry-After`, `X-SEAM-Budget-Remaining`, `quota_exceeded` error envelope | unchanged (refusal charges nothing) |

The refusal status is **429** — the `ErrCodeQuotaExceeded` → HTTP status
mapping in `internal/server/errors.go`. Phase-13 comments describing a 402
refusal do not match the shipped mapping and have been corrected.

## Metric reference

Recorded only where the table says; every recorder is a method on `Metrics`
(`internal/server/metrics.go`), one registry per server.

| Metric | Type | Labels | Recording site | Emitters |
|---|---|---|---|---|
| `seam_http_requests_total` | counter | `route`, `method`, `version`, `status` | `metricsMiddleware` defer | every non-reserved caller request — misses, hits, refusals |
| `seam_http_request_duration_seconds` | histogram | `route`, `method`, `version` | `metricsMiddleware` defer | same as above; reserved paths excluded |
| `seam_http_requests_in_flight` | gauge | `route`, `method`, `version` | `metricsMiddleware` inc/dec | `0` at rest for every non-reserved route that has served ≥1 request |
| `seam_route_version_requests_total` | counter | `route`, `spec_version` | `metricsMiddleware` defer | same as `seam_http_requests_total`; `spec_version=""` when the ring buffer has no current version |
| `seam_cache_hits_total` | counter | `route`, `version` | `cacheMiddleware`, served-from-cache branch | successful cache hits only |
| `seam_cache_misses_total` | counter | `route`, `version` | `cacheMiddleware`, per GET lookup that misses | misses, including TTL=0 dedup-only lookups |
| `seam_quota_cost_total` | counter | `route` | `quotaMiddleware` admission, **only when cost > 0** | charged misses and non-GETs |
| `seam_quota_bypassed_total` | counter | `route` | `serveCachedResponse`, per actual hit | successful cache hits |
| `seam_quota_exceeded_total` | counter | `route` | `writeQuotaExceededResponse` | quota refusals |
| `seam_quota_remaining` | gauge | `scope` | — | **registered but never populated.** No code path writes it; the exposition carries no samples for it. Do not alert on it. (The operational runbook previously suggested otherwise; that reference is corrected.) |
| `seam_cache_hit_rate` | gauge | none | `stateMetricsCollector` at scrape time | always present; process-wide hits/(hits+misses), `0` before any lookup |

## Label-key rules

Two different strings occupy the `route` label, and the split is deliberate to
pin:

- **Cache and HTTP families** take the *metric-route context* attached by
  `metricsMiddleware`: the OpenAPI path template when the route table matches
  (bounded cardinality), otherwise the literal `unmatched` with version
  `unknown`. The cache middleware reads this context
  (`metricLabelsFromRequest`); without it — e.g. under direct test composition
  — it falls back to the concrete path and the `X-SEAM-API-Version` header or
  `_unversioned`.
- **Quota families** take the *concrete request path* (`r.URL.Path`) — the same
  string `SetQuota`/`SetCostPerCall`/`GetQuotaStatus` are keyed on. Known
  cardinality exception: a templated fragment (`/widgets/{id}`) accumulates one
  quota series per concrete ID served. Bounded by traffic shape; do not "fix"
  by switching quota keys to templates without migrating the configuration
  surface at the same time.

## Header reference

Canonical wire values (Go canonicalizes names; values are exact):

| Header | Class | Value |
|---|---|---|
| `X-Seam-Spec-Version`, `X-Seam-Api-Version` | every response, both ports | spec hash; `_unversioned` |
| `X-SEAM-Cache` | cache hit only | `HIT` — the only value ever emitted; a miss carries no cache header |
| `X-Quota-Bypassed` | cache hit only | `cache-hit` |
| `X-Quota-Cost-Per-Call` | charged admission, cost > 0 | `formatCost(cost)` — USD with trailing zeros trimmed (`$0.1`, not `$0.10`) |
| `X-Quota-Remaining` | charged admission, cost > 0 | `formatCost(remaining)` |
| `X-SEAM-Budget-Remaining` | charged admission and 429 refusal | `amount=$X unit=call window=Y resets=<RFC3339>` |
| `Retry-After` | 429 refusal | seconds until the quota window resets |

**Stripping rule on hits:** the response cached on the charged miss stores its
admission-time quota headers. `serveCachedResponse` replays the cached headers
but deletes `X-Quota-Cost-Per-Call`, `X-Quota-Remaining` **and
`X-SEAM-Budget-Remaining`** before writing: a bypassed request was never
charged, and replaying the miss's budget snapshot would report a remaining
budget that is stale by the age of the cache entry. Setting the two bypass
headers and deleting these three is the complete hit-header contract.

## Reserved endpoint coverage

| Endpoint | Port | Why it emits no signals |
|---|---|---|
| `/_seam/health`, `/_seam/healthz`, `/_seam/readyz` | caller | `isReservedPath` short-circuit in metrics, cache and quota middleware |
| `/openapi.json`, `/docs`, `/docs/route`, `/docs/paths`, `/whoami`, `/scopes`, `/changes` | caller | same |
| `/health/credentials`, `/health/upstreams`, `/config/status`, `/_seam/metrics`, `/_seam/capture/*`, `/_seam/cache/*` | operator | operator chain wires no cache/quota/metrics middleware |

Both routes to "no signals" are pinned: the caller-port paths by
`TestBypassObservability_ReservedRequestsEmitNoSignals` (through the production
middleware order), the operator-port ones by construction
(`Server.Start` composes the operator handler without those layers).

## Non-contract behaviour, recorded to prevent re-litigation

- `quotaMiddleware`'s own cache-hit branch (bypass header + bypass metric) is
  unreachable in the shipped composition: the cache middleware serves a hit
  directly and never calls into quota. It is retained for direct composition
  and covered by `TestQuotaBypass_ContextPropagation`; the shipped hit signal
  originates in `serveCachedResponse`, exactly once per hit.
- A refused request is still counted by `metricsMiddleware` (it runs outside
  quota) at `status="429"`, and its cache lookup still records a miss.
- `seam_cache_hit_rate` derives from the response cache's own counters at
  scrape time, not from the `seam_cache_{hits,misses}_total` series; they move
  together for GET lookups but are distinct instruments.

## Test index

| Test | Pins |
|---|---|
| `TestBypassObservability_ReservedRequestsEmitNoSignals` | reserved classes: zero samples in any signal family, no bypass/quota headers, fresh execution, $0; quota-refused sanity path proves the config bites |
| `TestBypassObservability_CacheHitSignalContract` | hit headers incl. budget-header stripping; hit/miss/bypass/cost metric values; label-key split; request still counted with `status="200"` |
| `TestReservedPathInteraction_BypassesCacheQuotaAndMetrics` | reserved bypass through the cache→quota pair alone |
| `TestCacheHitBypassInteraction_MetricsAndHeaders` | hit bypass metrics/headers without the metrics layer |
| `TestQuotaEnforcement_ReservedPaths` | reserved paths never refused while a non-reserved path is |
| `TestSeamHealthAliasReceivesReservedPathTreatment` | `/_seam/health` alias reserved treatment |
