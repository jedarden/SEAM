# Control-plane endpoint contract matrix

This is the cross-endpoint contract for the SEAM control plane. It is the
quick reference for callers and operators; the focused handler notes linked
below remain the detailed semantic contracts. Every row is exercised by
`internal/server/control_plane_endpoint_matrix_test.go` or by the per-endpoint
tests named in its test inventory.

## Matrix

`no-store` means the response must not be stored by an intermediary. “Reserved
bypass” means the endpoint is also skipped by SEAM's response-cache and quota
middleware; it is not merely a response-header convention.

| Endpoint | Listener | Method | Authorization | Status codes | Successful response shape | Cache policy |
|---|---|---|---|---|---|---|
| `/config/status` | operator | `GET` | `seam:ops:read` | `200`; `400` invalid `?version=`; `403` under-scoped/unresolved; `405` wrong method; wrong listener `503 no_upstream_configured` (the path is also present in the upstream document) | JSON object with `config`, `spec`, `routes`, `scrubbing`, `corpus`, `cache`, `quota`, and `health` sections; optional diagnostics are additive | `Cache-Control: no-store`; operator listener is outside the caller cache and the reserved path is a reserved bypass |
| `/_seam/capture/save` | operator | `POST` | `seam:ops:read` | `200`; `400` invalid `?version=`; `403`; `405`; `500 capture_failed`; `503` capture disabled; wrong listener `404` | `{"status":"saved","entry_count":N}` | `Cache-Control: no-store`; operator listener plus reserved bypass |
| `/_seam/capture/status` | operator | `GET` | `seam:ops:read` | `200` even when capture is disabled; `400` invalid `?version=`; `403`; `405`; wrong listener `404` | `{"enabled":bool,"entry_count":int,"corpus_dir":string}` | `Cache-Control: no-store`; operator listener plus reserved bypass |
| `/_seam/cache/status` | operator | `GET` | `seam:ops:read` | `200`; `400` invalid `?version=`; `403`; `405`; wrong listener `404` | JSON object with `enabled`, `size`, `hits`, `misses`, `evictions`, `hit_rate`, `routes_with_cache`, and `single_flight.{active_requests,total_calls,deduped_calls,coalesce_rate}` | `Cache-Control: no-store`; operator listener plus reserved bypass |
| `/_seam/cache/cleanup` | operator | `POST` | `seam:ops:read` | `200`; `400` invalid `?version=`; `403`; `405`; wrong listener `404` | `{"status":"cleanup_complete","size":int,"evictions":int}`; `evictions` is cumulative | `Cache-Control: no-store`; operator listener plus reserved bypass |
| `/health/credentials` | operator | `GET` | `seam:ops:read` | `200` for every authorized health verdict; `400` invalid `?version=`; `403`; `405`; wrong listener `404` | JSON object with `status`, `timestamp`, `credentials`, `circuit_breaker`, and optional `circuit_breakers`; unhealthy state is represented in the body, not as HTTP 5xx | `Cache-Control: no-store`; fresh sentinel snapshot, never cached |
| `/health/upstreams` | operator | `GET` | `seam:ops:read` | `200`; `400` invalid `?version=`; `403`; `405`; wrong listener `404` | JSON object with `timestamp`, `upstreams[]` (`upstream`, `last_2xx`, `circuit_breaker`, `healthy`), and `route_table` | `Cache-Control: no-store`; fresh sentinel snapshot, never cached |
| `/whoami` | caller | `GET` | no additional scope; stage 3 must resolve the caller | `200`; `403` identity resolution failure; `405` wrong method | JSON object with `identity`, `effective_scopes`, `scope_version`, and `resolved`; successful responses also carry `X-SEAM-Scope-Version` | `Cache-Control: no-store`; reserved caller path bypasses response-cache lookup/storage and quota |
| `/scopes` | caller | `GET` | no additional scope by default; `?all=1` additionally requires `seam:scopes:read-all` | `200`; `403` unresolved or `?all=1` under-scoped; `405` wrong method | JSON object with `scopes`, `filtered`, `total_scopes`, `returned`, and `effective_count`; successful responses also carry `X-SEAM-Scope-Version` | `Cache-Control: no-store`; reserved caller path bypasses response-cache lookup/storage and quota |
| `/api/v1/tailscale/ephemeral-key` | caller | `POST` | `seam:tailscale:key-create` | `200`; `400` malformed/missing `worker_id`; `403` unresolved/under-scoped; `405`; `500` Tailscale client failure; `503` unconfigured/hold-down | JSON object with `key`, `id`, `expires`, and `description`; `key` is the issued secret and must not be logged or echoed | `Cache-Control: no-store`; reserved caller path bypasses response-cache lookup/storage and quota |

The `400 invalid ?version=` row is produced by the listener-wide version
validation middleware before the endpoint handler. All structured errors use
the shared JSON envelope with at least `error` and `message`; errors also carry
`Cache-Control: no-store`. A wrong method is evaluated after listener and
authorization routing, so an under-scoped operator caller receives `403`
before a handler can return `405`.

## Listener and authorization rules

- Operator rows are registered only on the operator mux and served only from
  `OperatorPort`. The caller listener never returns an operator payload. It
  returns `404 route_not_found` for paths absent from the upstream document;
  `/config/status` is the deliberate `503 no_upstream_configured` exception
  because the upstream document contains that path without an upstream target.
- Caller rows are registered only on the caller mux. The operator listener's
  catch-all returns `404 operator endpoint not found` for them.
- Operator rows require a resolved identity with `seam:ops:read`. A resolved
  identity with any other scopes, an unresolved identity, or no identity in the
  operator mux context is denied with `403 forbidden`.
- `/api/v1/tailscale/ephemeral-key` has its own `seam:tailscale:key-create`
  gate. `/scopes` is callable without a scope in its filtered default mode;
  only `?all=1` requires `seam:scopes:read-all`.
- No endpoint accepts a caller-supplied credential or scope header. Identity
  and scopes come from stage-3 Tailscale WhoIs resolution; forged `X-SEAM-*`
  headers are stripped.

## Cache policy

The nine paths in the matrix are reserved paths. They do not perform a cache
lookup, do not populate the response cache, do not consume caller quota, and
do not emit cache-hit headers. The operator mux is not wrapped by the caller's
cache middleware at all. The explicit `no-store` header is defense in depth
for direct clients and intermediary caches.

The detailed capture/cache behavior is in
[operator-capture-cache-contracts.md](operator-capture-cache-contracts.md);
credential and upstream health shapes are expanded in
[credentials-health-contract.md](credentials-health-contract.md) and
`docs/design/cache-health-sentinel-integration.md`.

## Test inventory

| Test | Contract pinned |
|---|---|
| `TestPublishedControlPlaneEndpointMatrix` | This matrix is present in the published note, names all nine paths, and has the listener/method/authorization/status/shape/cache columns. |
| `TestControlPlaneEndpointMatrixSuccessShapes` | Each row reaches its owning mux with an authorized identity, returns the documented success status and JSON shape, and sends `Cache-Control: no-store`. |
| `TestControlPlaneEndpointMatrixMethodsAndScopeDenials` | Wrong methods return `405` for authorized callers; under-scoped operator/key callers return `403` with the required scope and no payload. |
| `TestControlPlaneEndpointMatrixListenerIsolation` | Operator paths on the caller mux and caller paths on the operator mux return `404` without leaking the other listener's response shape. |
| `TestControlPlaneEndpointMatrixUsesReservedCacheBypass` | Every matrix path is reserved, and the caller-owned paths are exercised through the cache middleware with no cache-hit header or cached response. |
| Existing per-endpoint contract suites | Failure-specific details, health-state transitions, capture persistence, cache counters, ephemeral-key hold-down, and version validation. |
