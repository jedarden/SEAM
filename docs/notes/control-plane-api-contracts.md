# Control-plane API contracts

**Status:** implemented. The machine-readable contract is the OpenAPI 3.0
document served at **`/docs/control-plane`** (raw JSON with
`Accept: application/json`, Scalar HTML otherwise) and built by
`internal/server/control_plane_openapi.go`. This note is the prose
distillation; when they disagree, the served document is wrong and the code
that pins it (`control_plane_openapi_test.go`) should fail.

## What "control plane" means here

SEAM serves two different APIs and documents them separately:

| Surface | Documents | Source of the document |
|---|---|---|
| `/docs`, `/openapi.json`, `/docs/route`, `/docs/paths` | The **upstream** API the gateway proxies | The loaded route fragments, scope-filtered to the caller |
| `/docs/control-plane` | The **gateway's own** endpoints listed below | Compiled into the binary (`controlPlaneOpenAPIDocument`) |

The split is deliberate: merging gateway paths into `/openapi.json` would
change the shape of the upstream document under every pinned consumer, and a
compiled-in control-plane document stays served even when no fragment loaded —
exactly the situation where an operator needs the control plane most.

## Listener model

Every contract names its listener. The caller-facing port serves everything in
the control-plane document (`x-seam-listener: caller`). The operator-only port
(`/_seam/metrics`, `/config/status`, `/health/credentials`,
`/health/upstreams`, `/_seam/capture/*`, `/_seam/cache/*`) is a separate
surface gated on `seam:ops:read` by the operator scope middleware; it is
pinned by its own contract tests and is deliberately **not** in the
control-plane document.

## Authentication (no endpoint accepts a credential)

- Stage 2 strips inbound `X-SEAM-*` headers, so a client cannot assert
  identity or scope state by header.
- Stage 3 resolves the caller from the inbound tailnet connection via
  Tailscale WhoIs; scope claims come from the node's grant capabilities.
- An unresolvable caller is **default-denied** with `403 forbidden`
  ("Identity resolution failed") before any handler runs.
- The one exemption: `/_seam/health`, `/_seam/healthz`, `/_seam/readyz`,
  `/_seam/metrics` arrive over the pod network where no identity can exist.

## Endpoint contracts

| Endpoint | Method | Scope gate | Notes |
|---|---|---|---|
| `/whoami` | GET | none (returns the caller's own view) | Identity, effective scopes, `scope_version`; also sets `X-SEAM-Scope-Version` |
| `/scopes` | GET | `seam:scopes:read-all` for `?all=1` only | Two merged sources: `spec` (fragment `x-required-scope`) + `builtin`; default output filtered to the caller's scopes |
| `/changes` | GET | none; route entries scope-filtered | `level` ∈ {1,2} else 400; `since` unknown/evicted is `200` + `since_known:false`, not an error; `scope-since` adds `scope_changes` (currently `change_type: unknown`); reserved by exact path — `/changes/…` sub-paths are not migration endpoints and 404 `route_not_found` |
| `/api/v1/tailscale/ephemeral-key` | POST | `seam:tailscale:key-create` | Body `{worker_id}`; response carries the **live tskey secret**; 503 inside the client hold-down with `retry_after` |
| `/docs` | GET | none (JSON branch is scope-filtered) | `Accept: application/json` → raw filtered spec; otherwise Scalar HTML |
| `/docs/route` | GET | 404-oracle scope filtering | `path` required, `version` grammar `^v[1-9][0-9]*$` or `_unversioned`; HTML Accept → `302 /docs#anchor` |
| `/docs/paths` | GET | none | Every path with last-2xx status; `Cache-Control: no-store` |
| `/openapi.json` | GET | none (document is scope-filtered) | `?version=<spec hash>` delegates to the archive |

`X-SEAM-Scope-Version` is stamped on every caller-listener response by the
scope-version middleware; `X-Request-ID` by the request-id middleware.

## Error envelope

Every error on every endpoint — control plane and proxied — is the same
`ErrorResponse` envelope, written by `ErrorResponse.Write`:

```json
{
  "error": "forbidden",
  "message": "...",
  "details": {"required_scope": "seam:scopes:read-all"},
  "validation_errors": [],
  "docs_url": "/docs",
  "request_id": "..."
}
```

- `error` + `message` are always present; `details`, `validation_errors`,
  `docs_url`, `request_id` are optional.
- `error` is a closed enum — exactly the taxonomy in `HTTPStatusMapping`
  (`internal/server/errors.go`). An unknown code is normalized to
  `internal_server_error` before serialization.
- Every error response also carries `Cache-Control: no-store` and
  `X-Content-Type-Options: nosniff`.
- Internal causes never appear in the envelope; they go to the log keyed by
  `request_id`.

## Why compiled-in, not a fragment

A fragment would make the control-plane contract load-bearing on the very
subsystem it exists to explain, and would leak gateway paths into the route
table, the `/scopes` spec-derived source, and `/changes` output. The compiled
builder depends on nothing but the configured base URL, so the contract
renders identically with zero fragments, a broken spec, or no upstream.

## Test map

| Test | Pins |
|---|---|
| `TestControlPlaneOpenAPIContractDocument` | Paths present, `x-seam-listener` on every operation, every non-2xx response `$ref`s the envelope, envelope enum == `HTTPStatusMapping` |
| `TestDocsControlPlaneJSONContract` | JSON negotiation branch over the real caller pipeline |
| `TestDocsControlPlaneHTMLShell` | HTML branch: agentation wiring (one import map, before the mounting module), nav, embedded spec |
| `TestDocsControlPlaneLinkedFromDocs` | `/docs` links to `/docs/control-plane`, still exactly one import map |
| `TestControlPlaneErrorEnvelopeIntegration` | The four endpoints' documented error paths through the pipeline: 405/403/400 envelopes with details and no-store/nosniff |
| `TestWhoamiContractThroughCallerPipeline` | `/whoami` headers and response key set end to end |
