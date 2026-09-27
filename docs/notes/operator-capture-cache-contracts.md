# Operator capture/cache endpoint contracts

**Status:** implemented. These four endpoints are the operator port's
capture-workflow and response-cache surfaces. This note is their per-endpoint
contract; the listener split and the exclusion from `/docs/control-plane` are
recorded in [control-plane-api-contracts.md](control-plane-api-contracts.md).
Routes are registered in `setupRoutes` (`internal/server/server.go:450-453`);
every claim here is pinned by a test named in the test map at the bottom.

## Endpoints

| Endpoint | Method | Purpose |
|---|---|---|
| `/_seam/capture/save` | POST | Flush the capture buffer to `<corpus_dir>/corpus.json` |
| `/_seam/capture/status` | GET | Report capture state and pending entry count |
| `/_seam/cache/status` | GET | Report response-cache statistics and request coalescing |
| `/_seam/cache/cleanup` | POST | Evict expired response-cache entries now |

## Listener placement

All four are registered on the **operator mux only**. The caller mux has no
route for them: a request to any of these paths on the caller port falls
through to the dispatch catch-all and returns `404 route_not_found`. The
operator port serves them from the listener bound to `OperatorPort`, which the
tailnet ACL restricts to operator-tier callers. Pinned end to end over real
sockets by `TestOperatorListenerIsolationEndToEnd` /
`TestCallerListenerServesOwnEndpoints`.

## Authorization

Every route is wrapped in `operatorScopeMiddleware("seam:ops:read")`, which
runs **before** any handler logic:

- No identity in context (stage 3 could not resolve the caller), an unresolved
  identity, or an identity without `seam:ops:read` →
  `403 forbidden` envelope, message
  `Operator endpoint requires scope: seam:ops:read`. The required scope is
  named in the `message`; the envelope carries no `details` object.
- Scope claims come from the node's grant capabilities resolved by Tailscale
  WhoIs on the inbound connection. There is no header- or body-supplied
  credential path — stage 2 strips inbound `X-SEAM-*` headers.
- Ordering: scope denial wins over method checking. A `GET` on
  `/_seam/capture/save` from a scopeless caller is a `403`, not a `405`.

## Semantics common to all four

- **Request bodies are never read.** No endpoint takes a payload or query
  parameter; a request body, if sent, is ignored. The contract is
  method-plus-path only.
- **Every response carries `Cache-Control: no-store`** — set explicitly by the
  success paths in each handler, and by `ErrorResponse.Write` on every error
  and denial path. Operator state must never be cached by an intermediary.
- **Errors use the shared `ErrorResponse` envelope** (`error`, `message`,
  `request_id`, …; see control-plane-api-contracts.md). Internal causes are
  logged keyed by `request_id`, never serialized.
- **Method mismatch is `405 method_not_allowed`**, message
  `Only POST method is allowed` or `Only GET method is allowed` as
  appropriate — the message names the one accepted method.
- Success responses are `application/json`; errors are `application/json` and
  additionally carry `X-Content-Type-Options: nosniff`.
- None of the four touches an upstream, the route table, or a credential, so
  beyond the 403/405 cases below they have no dependency failure mode.

## `/_seam/capture/save` — POST

Flush point of the capture workflow: writes the accumulated capture buffer to
disk as the diff corpus consumed by `tools/diffharness`.

- **`200`** — body `{"status":"saved","entry_count":N}`. `N` is the size of
  the snapshot just written, i.e. the middleware's pending entry count at
  flush time. The response is written only after the file is durable.
- **Snapshot, not draining.** `CaptureMiddleware.Save()` serializes the whole
  current entry set and rewrites `<corpus_dir>/corpus.json`
  (`seam-diff-corpus/v1`, `0644`, trailing newline). The in-memory buffer is
  **not** cleared, so `/_seam/capture/status` keeps reporting those entries
  until process restart. `entry_count` in the save response therefore equals
  the status endpoint's count, not a count of newly flushed entries.
- **Idempotent.** Repeated saves with no intervening traffic rewrite the same
  entry set — entries are never duplicated in the file and never lost.
- **`503 service_unavailable`** — `Capture middleware not enabled`: capture
  was not enabled at server start (`captureMiddleware == nil`). This is the
  only "wrong state" failure; there is no runtime enable/disable toggle.
- **`500 capture_failed`** — `Failed to save corpus`: the snapshot could not
  be persisted (corpus directory could not be created, or the file write
  failed — e.g. `corpus_dir` exists as a regular file). The wrapped cause is
  logged, never returned.

## `/_seam/capture/status` — GET

- **`200`** always (capture enabled or not — status never fails):
  `{"enabled":bool,"entry_count":int,"corpus_dir":string}`.
- `enabled` mirrors `CaptureMiddleware.IsEnabled()`.
- `entry_count` is the number of captured-but-not-yet-flushed-to-disk entries
  held in memory.
- `corpus_dir` is the configured directory; `""` when capture is disabled
  (no middleware exists to have a directory).

## `/_seam/cache/status` — GET

- **`200`** always:

  | Field | Type | Meaning |
  |---|---|---|
  | `enabled` | bool | Constant `true` — the response cache is a fixed pipeline stage, not toggleable |
  | `size` | int | Entries currently held |
  | `hits` / `misses` | int | Cumulative since process start |
  | `evictions` | int | Cumulative since process start (TTL expiry, capacity, and manual cleanup) |
  | `hit_rate` | float | `hits / (hits + misses)`, `0.0` when no traffic |
  | `routes_with_cache` | int | Routes with a configured cache TTL |
  | `single_flight.active_requests` | int | In-flight coalesced upstream calls |
  | `single_flight.total_calls` | int | Total single-flight group invocations |
  | `single_flight.deduped_calls` | int | Calls that joined an in-flight group instead of hitting upstream |
  | `single_flight.coalesce_rate` | float | `deduped_calls / total_calls` |

  The key sets above (top level and `single_flight`) are closed: a test pins
  the exact key set, so adding a field is a deliberate contract change.

## `/_seam/cache/cleanup` — POST

- Runs `cache.Cleanup()` synchronously, then reports.
- **`200`** — body `{"status":"cleanup_complete","size":S,"evictions":E}`.
  `S` is the surviving (unexpired) entry count; `E` is the **cumulative**
  eviction counter, the same counter `/_seam/cache/status` reports — not a
  count of this call's evictions.
- **Idempotent.** A second call immediately after the first finds nothing new
  expired and returns the same `size` and `evictions` as the first.

## Error matrix

| Condition | Any endpoint | capture/save | capture/status | cache/status | cache/cleanup |
|---|---|---|---|---|---|
| No/unresolved/unscoped identity | `403 forbidden` | | | | |
| Wrong method | `405 method_not_allowed` | | | | |
| Capture disabled | — | `503 service_unavailable` | n/a (reports disabled) | — | — |
| Snapshot write fails | — | `500 capture_failed` | — | — | — |

## Test map

| Test | Pins |
|---|---|
| `TestCaptureCacheEndpointsRequireOpsReadScope` | All four routes: 403 (no identity, scopeless identity) naming the scope; scope admits to 200; no-store on denial and success |
| `TestCaptureEndpointsAreOperatorOnly` | Operator-mux status/save responses with a pending entry; caller mux 404 for the capture pair |
| `TestCaptureEndpointsMethodAndStateErrors` | Capture pair: wrong method 405, disabled status shape, disabled save 503, persistence failure 500 |
| `TestCaptureCacheStatusReflectsCaptureState` | Disabled `enabled:false entry_count:0`; pending entries counted before flush |
| `TestCacheCleanupReportsEvictions` | Cleanup evicts the expired entry, reports `cleanup_complete`/size/evictions; cache/status mirrors the counter |
| `TestCacheEndpointsMethodEnforcement` | Cache pair: wrong method 405 envelope naming the allowed method, no-store |
| `TestCaptureSaveFlushIsIdempotent` | Repeated save rewrites one entry, never duplicates; request body ignored |
| `TestCacheCleanupIsIdempotent` | Second cleanup returns identical counters; evictions cumulative, not per-call |
| `TestCacheStatusResponseSchema` | cache/status closed top-level and `single_flight` key sets, types, `enabled` constant |
| `TestOperatorListenerIsolationEndToEnd` / `TestCallerListenerServesOwnEndpoints` | All four operator-only over real sockets; caller port denies without echoing operator payload |
| `TestCacheHitRateStatusEndpoint` | hit_rate/hits/misses math after one miss and two hits |
