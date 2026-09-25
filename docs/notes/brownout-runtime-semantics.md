# Deprecated-Route Brownout: Runtime Semantics

Status: implemented 2026-09-17; deprecation response headers 2026-09-25;
window metrics specified 2026-09-25.
This document is the authority on how `x-seam-deprecated` behaves at runtime:
brownout windows, the metrics a window's responses produce, and the
Deprecation/Sunset response headers every
otherwise-normal response for a deprecated route carries. The code comments
in `internal/server/brownout_middleware.go` and
`internal/server/deprecation_middleware.go` summarize the same contract; the
tests named at the bottom pin it.

## What a brownout window is

A deprecated route fragment may declare scheduled outage windows:

```yaml
x-seam-deprecated:
  since: 2024-01-01          # required, ISO date
  sunset: 2024-12-31         # required when brownout is present, ISO date
  brownout:
    - start: 2024-06-15T02:00:00+02:00   # RFC 3339, any offset
      end:   2024-06-15T04:00:00+02:00
    - start: 2024-07-01T00:00:00Z
      end:   2024-07-01T02:00:00Z
```

While a window is active the gateway serves a structured `410 Gone` instead
of proxying the route. Between windows the route serves normally. The intent
(Phase 8.3 / plan AP-08) is scheduled, visible breakage: callers are forced
onto the replacement before the route disappears for good.

## Where enforcement runs

`Server.brownoutMiddleware` is wired into the caller-facing chain as the
outermost of the cache/quota/brownout trio (quota innermost, then cache,
then brownout — see `Server.Start`), so a request only reaches cache and
quota after the brownout check passed:

- a window 410 consumes **no quota**,
- the 410 itself is **never cached** (it is generated outside the cache
  middleware), and
- a cached pre-window response **cannot mask** an active window — the
  window check runs before the cache is consulted.

Dispatch publishes the authoritative route match into the request context
only inside the caller mux (stage 4, `withRouteMatch`) — after every
middleware has run — so the middleware resolves the route itself with a
read-only lookup (`ThreadSafeTableHolder.MatchForBrownout`, the same
no-side-effects contract as the loop guard's). The lookup publishes nothing;
stage 4 still re-matches and republishes with the credential resolver before
anything can inject a secret.

Two classes of request bypass the check entirely:

- **Reserved paths** (`/health`, control plane) — never proxied routes.
- **Credential probes** (`X-SEAM-Probe: true`) — a probe must not observe a
  gateway-side 410 as an upstream failure, or a brownout window would mark
  healthy credentials unhealthy for a decision the gateway itself made. In
  practice probes target upstreams directly (`route.UpstreamTarget` +
  probe path), so they never traverse the caller chain at all; the header
  check is defense-in-depth, and it cannot be forged past it by an external
  caller because stage 2 (`headerStrippingMiddleware`, outside the brownout
  check) deletes `X-SEAM-*` request headers before the window check runs.

## Window semantics

- **Absolute instants.** Windows are compared as instants, never as written
  strings. A window written with a non-UTC offset is exactly equal to its
  UTC rendering, and the gateway's own local timezone never participates:
  `[2024-06-15T02:00:00+02:00, 2024-06-15T04:00:00+02:00]` is exactly
  `[00:00Z, 02:00Z]`, so a request at `2024-06-15T01:30:00Z` is inside it.
  The lenient RFC 3339 spellings lint accepts (space or lower-case `t`
  separator, lower-case `z` zone) are normalized at parse time, so every
  window lint passes is also honored at runtime.
- **Inclusive boundaries.** `[start, end]`: the start instant and the end
  instant are both inside the window.
- **Union across windows.** If ANY window is active, the route serves 410.
  Lint rejects unordered or overlapping windows at fragment validation, so
  union semantics is the belt-and-braces behavior for a fragment that
  reached the gateway without linting — an overlap can only extend an
  outage, never narrow one.
- **First active window names the bounds.** When more than one window is
  active at the same instant (adjacent inclusive boundaries, or an
  unlinted overlap), the FIRST window in array order is the one echoed in
  the response body.
- **Unparseable windows are inert.** A window whose start or end does not
  parse is never active — the fail-safe direction. Lint is the up-front
  gate that rejects malformed windows before they reach the gateway, so an
  inert window at runtime is a defect that should already have been caught.

## Response contract

During an active window the response is:

- **Status:** `410 Gone`.
- **Headers:**

  | Header | Value |
  |---|---|
  | `Content-Type` | `application/json` |
  | `X-SEAM-Brownout` | `active` |
  | `Deprecation` | `since=<since>` |
  | `Sunset` | `<sunset>` (only when set) |
  | `Link` | `<base>/changes>; rel="deprecation"`, plus `<replacement>?version=<v>; rel="alternate"` when a replacement is declared |

- **Body:** structured JSON — `error: "gone"`, a human-readable `message`,
  `brownout: {start, end}` echoing the active window's written values
  verbatim, `deprecation: {since, sunset?}`, and `replacement:
  {path, version?}` when declared.
- Every 410 served is logged (`[brownout] served 410 for route … window …`)
  as the operator-facing record that live traffic appeared inside a window.

## Metrics

The metrics middleware wraps the entire cache/quota/brownout trio (it is the
outermost signal-producing layer — see `Server.Start`), so a window 410 is
ordinary counted traffic, never an accounting blind spot:

- **Counted at its own status.** The 410 lands in
  `seam_http_requests_total{route, method, version, status="410"}` under the
  deprecated route's own labels, and in the
  `seam_http_request_duration_seconds` histogram. An operator graphing status
  codes sees the window as a 410 band, not as traffic that vanished.
- **The retirement counter sees the caller.** The same request increments
  `seam_route_version_requests_total{route, spec_version}`: a caller that
  appears inside a window is real dependency — exactly the traffic that must
  keep the quiet window shut (plan Phase 8: only a real caller resets or
  holds it; probes never traverse the caller chain, so they cannot). A route
  whose only remaining traffic is brownout 410s therefore never retires,
  which is correct: those callers still need it.
- **No cache or quota samples.** The 410 is served before the cache and
  quota middlewares are consulted, so it produces no
  `seam_cache_hits_total` / `seam_cache_misses_total` sample, no
  `seam_quota_cost_total` charge, and no `seam_quota_bypassed_total` /
  `seam_quota_exceeded_total` event — the metering silence is the same
  short-circuit that makes the 410 consume no budget.
- **The log line is the per-event record.** The counters say how many; the
  `[brownout] served 410` log line is the only record naming *which* window
  a given 410 belonged to.

## Sunset behavior

Sunset is **advisory** and never removes a route by itself. Past the last
window — and a fortiori past sunset — the route serves normally until a
human merges the removal PR. Nothing in the gateway deletes or refuses a
route because a date on the fragment passed.

## Deprecation and Sunset response headers

Outside windows a deprecated route serves normally, and every response it
serves announces the deprecation. The header set is written by
`DeprecationHeaders.Apply` from the same enforcement point that serves
window 410s, on the pass-through side, before the cache is consulted — so
cache hits and misses carry it alike:

| Header | Value |
|---|---|
| `Deprecation` | `since=<since>` — the fragment's `since` verbatim |
| `Sunset` | `<sunset>` verbatim, only when the fragment declares one |
| `Link` | `<base>/docs/route?path=<template>&version=<v>; rel="deprecation"`, then `<base>/changes; rel="deprecation"`, then `<replacement>[?version=<v>]; rel="alternate"` when a replacement is declared |

`<base>` comes from `X-Forwarded-Proto`/`X-Forwarded-Host` when present,
else the request's own scheme and host. A route deprecated only by OpenAPI
`deprecated: true` on the operation — no fragment-root `x-seam-deprecated` —
has no since date; `extractDeprecation` records `"unknown"` and the header
reads `Deprecation: since=unknown`.

An active-window 410 never gets this pass-through set layered onto its own:
the window check runs first and serves the 410 directly (see "Response
contract" above), so a 410's Link headers are exactly the two it writes
itself.

### Header names: no `X-` prefix

The gateway emits the unprefixed RFC 9745 `Deprecation` and RFC 8594
`Sunset` fields. Some example fragments
(`examples/fragments/5-complex-multi-instance.yaml`, `docs/examples/…`)
declare `X-Deprecation`/`X-Sunset` as response headers in the example API
document — those describe what an example API advertises in its own response
declarations, not the gateway's emission contract. The schema's own
`$comment`s ("populates Deprecation directly", "emitted as Sunset verbatim")
and `docs/notes/route-fragment-schema-v1.md` ("This field drives the
Deprecation and Sunset HTTP headers (RFC 9745, RFC 8594)") name the
unprefixed fields, and RFC 6648 deprecates the `X-` prefix for new fields.
Callers looking for `X-Deprecation`/`X-Sunset` will not find them; the
unprefixed names are the contract, and the gateway emits no `X-`-prefixed
deprecation headers.

## Lint gate (up front, not runtime)

`internal/spec/lint.go` (`checkDeprecation`) rejects, at fragment
validation:

- `brownout` without `sunset`, an empty `brownout` array, or non-object
  windows;
- unparseable `start`/`end`, `end <= start`;
- windows outside `[since, sunset]` — judged on absolute instants, so an
  end written on the sunset calendar day in a westward offset
  (`2024-12-31T23:00:00-05:00` = `2025-01-01T04:00Z`) is correctly flagged;
- unordered or overlapping windows — also judged on absolute instants, so
  mixed-offset pairs are ordered correctly (`22:00Z` starts after
  `23:30+02:00` ends, despite sorting earlier as a string).

Lint's date-time comparisons were deliberately moved from lexicographic
string comparison to instant comparison for exactly these two mixed-offset
cases; the old code both false-flagged legal adjacency and missed real
overlaps across offsets.

## Test map

| Contract | Pinned by |
|---|---|
| Wiring resolves the route itself (no context match needed) | `TestServerBrownoutMiddleware_ResolvesRouteWithoutContextMatch` |
| Pass-through outside windows, no brownout marker | `TestServerBrownoutMiddleware_OutsideWindowProceeds` |
| Between disjoint windows serves normally (gap edges incl. next start) | `TestServerBrownoutMiddleware_BetweenWindowsServesNormally` |
| Non-UTC offset windows honored as UTC instants, inclusive end | `TestServerBrownoutMiddleware_NonUTCOffsetWindowHonoredInUTC` |
| Probe bypass | `TestServerBrownoutMiddleware_ProbeRequestsBypass` |
| Sunset advisory — serves past sunset | `TestServerBrownoutMiddleware_PastSunsetServesNormally` |
| Unparseable window inert | `TestServerBrownoutMiddleware_UnparseableWindowIsInert` |
| First active window names bounds | `TestServerBrownoutMiddleware_410NamesFirstActiveWindow` |
| Union across overlapping windows | `TestBrownoutScheduler_OverlappingWindowsUnion` |
| Full 410 header/body contract | `TestBrownoutScheduler_ResponseHeadersContract` |
| Window 410 counted (requests_total 410, route-version counter), no cache/quota samples | `TestBrownoutWindow410Metrics` |
| DORMANT / basic 410 / boundaries / replacement | existing `brownout_middleware_test.go` tests |
| Mixed-offset adjacency accepted (no false positive) | `TestCheckDeprecation_MixedOffsetAdjacentWindowsAccepted` |
| Mixed-offset overlap detected (no false negative) | `TestCheckDeprecation_MixedOffsetOverlapDetected` |
| End instant past sunset detected | `TestCheckDeprecation_WindowPastSunsetOffsetDetected` |
| Shared instant parse incl. lenient separators | `TestParseBrownoutInstant` |
| Pass-through header set, self-resolved (no context match) | `TestServerDeprecationMiddleware_EmitsHeadersWithoutContextMatch` |
| Sunset omitted when unset; no alternate link without replacement | `TestServerDeprecationMiddleware_SunsetOmittedWhenUnset` |
| Dormant on non-deprecated route / unmatched path | `TestServerDeprecationMiddleware_DormantOnNonDeprecatedRoute`, `TestServerDeprecationMiddleware_NoRouteMatchIsDormant` |
| Reserved-path bypass precedes header emission | `TestServerDeprecationMiddleware_ReservedPathBypass` |
| Probe bypass covers headers too | `TestServerDeprecationMiddleware_ProbeRequestsBypass` |
| Operation-level `deprecated: true` advertised (`since=unknown`) | `TestServerDeprecationMiddleware_OperationLevelDeprecatedAdvertised` |
| Window 410 carries only its own headers (no pass-through layering) | `TestServerDeprecationMiddleware_ActiveWindow410NotDoubleHeadered` |
| Headers in the gap between windows | `TestServerDeprecationMiddleware_InactiveWindowCarriesHeaders` |
| Headers persist past sunset (sunset advisory) | `TestServerDeprecationMiddleware_PastSunsetStillAdvertised` |
