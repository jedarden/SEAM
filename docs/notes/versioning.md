# Versioning & Migration Policy

## Problem

NEEDLE workers run perpetually-live sessions (hours+), each learning SEAM's API contract once early in its session (via `/openapi.json`) and continuing to call routes on that understanding for the session's duration. No client can be assumed to ever restart or refetch the spec on a schedule. Route contracts still need to evolve, so SEAM needs two separate migration mechanisms — one for the gateway's own code, one for route contracts.

## A. Gateway process deploys (code changes, no contract change)

Standard Kubernetes rolling-update mechanics: ≥2 replicas, `PodDisruptionBudget` `minAvailable: 1`, readiness probe gates traffic before a new pod accepts requests, graceful `SIGTERM` handling (stop accepting new connections, finish in-flight requests, exit within `terminationGracePeriodSeconds`). This covers "SEAM's own binary changed" only — it does not cover a route's contract changing underneath a caller.

## B. Route contract versioning (breaking changes to a route's shape)

Borrows two precedents directly (full detail in `docs/research/version-migration-precedent.md`):

- **From Kubernetes' API deprecation policy:** coexistence is the mechanism, not a transitional inconvenience — old and new versions of a route serve simultaneously for as long as anything is calling the old one.
- **From Stripe's dated versioning:** rather than maintaining N full parallel implementations, implement a route's logic once against the newest contract, and add a thin adapter (request transform in, response transform out) for each older version still being served.

Concretely, in SEAM's ConfigMap-per-route-fragment model:

- A breaking route change ships as a **new** fragment (new path segment or `x-api-version`), never as an in-place edit to an existing fragment.
- The old fragment stays mounted, mapped to a thin adapter in front of the single current implementation — not a full duplicate handler.
- Every response served from a fragment marked `deprecated: true` carries `Deprecation` (RFC 9745) and `Sunset` (RFC 8594) headers — advisory only, not the retirement mechanism, since neither RFC guarantees a client acts on them and no precedent shows long-running agents reliably do.
- **Retirement is usage-gated, not calendar-gated:** SEAM exports a per-route-version request-count metric (mirroring Kubernetes' `apiserver_requested_deprecated_apis` gauge). A deprecated fragment is only removed once that counter has been zero for a quiet window (e.g. 7 days) — never on a fixed date, since perpetually-live workers make any calendar guess unsafe.
- Once per-worker identity exists ([[project_seam_gateway|Phase 7]], `tsnet`), the same metric can be broken down by caller, so a stuck worker on a deprecated route can be identified and drained individually instead of just waiting out the aggregate.

## Open question

Should SEAM eventually adopt Stripe's per-caller version pinning (each identity locked to whatever spec version was live at its first call, transformed transparently) once per-worker identity exists? Stronger guarantee than "don't break routes that still see traffic," but meaningfully more machinery and depends on Phase 7. Not required for an MVP.
