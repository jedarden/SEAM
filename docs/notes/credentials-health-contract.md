# `/health/credentials` — the credential health contract

Specified 2026-09-24 (bead `seam-bb4a2a57`). Implementation: `internal/server/health_sentinel.go`
(response types, sentinel, handler), `internal/server/circuit_breaker_health.go`
(published breaker state and registry), route registration in `setupRoutes`
(`internal/server/server.go`). This document is the single description of the
endpoint's wire contract; the tests named in "Test inventory" enforce every
clause of it.

`/health/credentials` answers one question for the operator: **can SEAM reach
the credentials its routes inject?** It answers purely from the per-origin
breaker state that request handling publishes — the sentinel performs no probe
of its own and therefore cannot mutate breaker state. The probe that discovers
credential death independently of traffic is a different signal and reports to
`/_seam/readyz` (`credential_probe`, see `readyz-contract.md`).

## Request

`GET /health/credentials` on the **operator listener** (port 8081). Any other
method is refused with the structured `method_not_allowed` error response
(HTTP 405) — a non-GET request is a caller mistake, not a health verdict. The
listener-wide `?version=` validation (stage 2) applies as to every endpoint: an
invalid version is a 400 `invalid_version_parameter` carrying
`X-SEAM-Spec-Version`.

| Status | When |
|---|---|
| `200 OK` | every authorized GET — the verdict lives in the JSON `status`, never the status code |
| `403 Forbidden` | stage 3 resolves no identity, or the identity lacks `seam:ops:read` |
| `405 Method Not Allowed` | any method other than GET (structured error body) |
| `400 Bad Request` | invalid `?version=` parameter (stage 2, listener-wide) |

## Listener and authorization

The path is a **control-plane surface, operator-only default-denied tier**
(plan, two-tier rule). It is registered only on the operator mux, wrapped in
`operatorScopeMiddleware("seam:ops:read", ...)`, and `Start()` serves that mux
only from the listener bound to `OperatorPort`. Stage 3 (identity resolution)
sits outside the mux and runs first; the scope gate inside the mux is
default-deny, with two distinct 403 bodies:

- **Caller resolved, scope missing** — `error: "forbidden"`, message
  `Operator endpoint requires scope: seam:ops:read`. The message names the
  required scope and nothing else.
- **Caller unresolved** — stage 3 denies before the gate: `error: "forbidden"`,
  message `Identity resolution failed`. It names no scope because no identity
  was resolved to evaluate one against.

On the **caller-facing listener** (port 8080) the endpoint does not exist: the
path is registered on no caller route, so the request falls to the proxy
catch-all and returns the structured `route_not_found` 404 echoing only the
method and path. That 404 is stable by construction, not an artifact of the
current fragment fleet — the path is in the spec loader's reserved set
(`internal/spec/fragment.go`), so a fragment declaring it is quarantined and
`internal/spec/lint.go` rejects it. No fragment can ever grow into the path.

## Response

`Content-Type: application/json`. A single JSON object whose key set is
**closed**:

| Key | Reports |
|---|---|
| `status` | the verdict: `healthy` \| `degraded` \| `unhealthy` |
| `timestamp` | RFC 3339 UTC time of the observation |
| `credentials` | availability statement for the credential subsystem (below) |
| `circuit_breaker` | aggregate over every published per-origin breaker |
| `circuit_breakers` | per-origin records, sorted by origin; **omitted entirely** while no breaker has published state |

`credentials` (availability metadata only):

| Key | Reports |
|---|---|
| `available` | always `true` today: the endpoint is served by a live process. It is **not** per-credential verification — that signal is the breaker state |
| `last_refresh` | reserved, currently never emitted (omitempty) |

`circuit_breaker` (aggregate):

| Key | Reports |
|---|---|
| `enabled` | any published breaker is enabled |
| `state` | worst state across origins (mapping below) |
| `consecutive_failures` | the winning record's failure count |
| `opened_at` | present when the winning record published it |
| `last_error` | upstream **transport** error text from the winning record; omitted unless published |
| `retry_after_seconds` | present when the winning record published it |

Each `circuit_breakers[]` entry: `origin`, `state`, `enabled`,
`consecutive_failures`, plus `opened_at`, `last_error`, `retry_after_seconds`,
`source` when published. The state spelling (`closed` / `open` / `half_open`)
is wire-stable — health tooling consumes it.

## Breaker-state mapping

| Published per-origin state | `status` | aggregate `circuit_breaker.state` |
|---|---|---|
| nothing published | `healthy` | `closed`, `enabled: false` |
| `closed` | `healthy` | `closed` |
| `half_open` | `degraded` | `half_open` |
| `open` | `unhealthy` | `open` |

- **Worst state wins**: `open` outranks `half_open` regardless of the order
  records appear in the snapshot — one open origin is enough for `unhealthy`;
  a `half_open` origin alone is `degraded`.
- Ties on state rank break by higher `consecutive_failures`; the aggregate
  mirrors the winning record's `opened_at` / `last_error` /
  `retry_after_seconds`.
- **No latching**: the snapshot is rebuilt on every call. A breaker recovering
  to `closed` restores `healthy` on the next observation.

## Cache headers

Every response carries `Cache-Control: no-store` and never an `X-SEAM-Cache`
header. Defense in depth: the handler sets `no-store` itself, and the path is
an exact reserved path (`isReservedPath`), so the cache middleware bypasses
lookup and storage and does not touch its hit/miss counters. The freshness
consequence is pinned: breaker state flipped between two requests is visible
on the second response, and nothing enters the cache in between.

## No-secret-value guarantee

The sentinel's **only** input is the breaker-state registry snapshot. It reads
no vault path, no credential value, no injection metadata. Concretely:

- The response key set is closed and pinned by test — a field added for a
  credential value, its vault path, or any other secret-bearing metadata fails
  the pin.
- `credentials` carries availability metadata only; it never carries a value.
- `last_error` carries upstream transport error text only.
- The 403 bodies name the scope or the resolution failure, never payload.

## Relationship to readiness

`/_seam/readyz` decides whether the pod receives traffic;
`/health/credentials` is observability. An `unhealthy` verdict here never
removes the pod from the Service — by design, since one dead origin must not
take down the gateway for every other route (`readyz-contract.md`,
"Relationship to liveness"). Readiness's `credential_probe` key tracks probe
verification freshness, a different signal from live breaker state.

## Test inventory

| Test | Contract clause pinned |
|---|---|
| `internal/server/credentials_health_contract_test.go` — `TestCredentialsHealthStateMatrix` | one row per breaker state: 200 + `application/json` + `no-store` + no `X-SEAM-Cache`, exact closed top-level key set, `credentials` availability shape, exact aggregate key set and status/aggregate mapping per state, `open` outranking `half_open`, per-origin entry count |
| `internal/server/credentials_health_contract_test.go` — `TestCredentialsHealthCallerAccessRejected` | caller listener → `route_not_found` 404 echoing no operator payload; resolved caller without `seam:ops:read` → 403 naming the scope; unresolved caller → stage-3 403; neither 403 echoes payload |
| `internal/server/health_sentinel_test.go` — `...ResponseCarriesNoCredentialValues` | closed key-set walk over the whole body (no-secret guarantee) |
| `internal/server/health_sentinel_test.go` — `...CacheBypassIsFresh` | reserved-path cache bypass; state flip visible; counters untouched |
| `internal/server/health_sentinel_test.go` — `...StatusMappingOverBreakerLifecycle` | closed→open→recovered sequence end to end; empty-registry shape |
| `internal/server/health_sentinel_test.go` — `...RejectsNonGet` | 405 for non-GET |
| `internal/server/operator_scope_test.go` — `..._HealthCredentials` | scope gate denies and names `seam:ops:read` |
| `internal/server/operator_listener_isolation_test.go` | real two-listener server: operator port serves the sentinel, caller port serves no operator payload |
| `internal/server/version_validation_test.go` | 400 `invalid_version_parameter` on this path |

## Change discipline

Adding a response field means three changes in one commit: the struct field and
JSON tag in `health_sentinel.go` (or `circuit_breaker_health.go`), the allowed
key set in `TestCredentialHealthSentinelResponseCarriesNoCredentialValues`, and
the schema table above. Changing the status mapping or the aggregate rule means
the matrix rows in `TestCredentialsHealthStateMatrix` and the mapping table
here. The closed-key-set assertions fail until the code and this document
agree, so the wire contract and its spec cannot drift apart silently.
