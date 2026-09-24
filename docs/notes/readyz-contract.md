# `/_seam/readyz` — the readiness contract

Specified 2026-09-24 (bead `seam-7ee1e4e8`). Implementation: `internal/server/readyz.go`
(dependency set landed in `30a7592`, bead `seam-1a5a3c12`). This document is the
single description of the endpoint's wire contract; the tests named in
"Test inventory" enforce every clause of it.

`/_seam/readyz` answers one question: **may this pod receive proxy traffic?**
It is the target of the Deployment's readiness probe (Phase 6a), so a 503
removes the pod from the Service while `/_seam/healthz` keeps the pod alive —
readiness and liveness are separate concerns (see "Relationship to liveness").

## Request

`GET /_seam/readyz` on the caller-facing port (8080). Any other method is
refused with the structured `method_not_allowed` error response (HTTP 405) —
not the readiness map, since a non-GET request is a caller mistake, not a
readiness verdict.

## Response

For a GET the handler always sets `Content-Type: application/json` and writes
a **flat JSON object in which every value is a boolean**, then the status:

| Status | When |
|---|---|
| `200 OK` | every dependency satisfied (`ready: true`) |
| `503 Service Unavailable` | any dependency unmet (`ready: false`) |
| `405 Method Not Allowed` | any method other than GET (structured error body) |

The body is exactly five keys — the enumeration is **closed**:

| Key | Reports |
|---|---|
| `route_table` | the merged route table can serve traffic |
| `allowlist` | allowlist enforcement is not fail-closed |
| `openbao` | the startup Kubernetes-auth login has completed |
| `credential_probe` | every configured credential probe carries a fresh verification |
| `ready` | logical AND of the four dependencies above |

Shape rules, all load-bearing for consumers:

- **Flat and boolean only.** Every value is `true`/`false`; there are no
  nested objects, counts, or timestamps. This preserves the historical
  `map[string]bool` decode for existing probe consumers.
- **A 503 still names every dependency.** The failed key reads `false` and the
  others read `true`, so an operator reading a 503 from Deployment events can
  tell a missing route table from a pending OpenBao login without querying
  further endpoints.
- **No extra keys, ever.** Diagnostic detail does not belong here — the plan
  fixes these endpoints as "a bare serving/not-serving verdict"; conditions
  are named at `/config/status` and `/health/*` instead.

## Dependency semantics

### `route_table`

Satisfied when the current route table holds at least one route, i.e. at
least one valid fragment loaded and merged. An empty table — no fragment yet
merged, or a reload that quarantined every fragment — fails it. A fully
quarantined fleet therefore takes the pod out of the Service instead of
serving 404s, while liveness stays up so a restart cannot make it worse.

### `allowlist`

Satisfied when vault-path and upstream-host allowlist enforcement is **not
fail-closed**. A server with no enforcer attached (local/dev runs without
enforcement) is satisfied. An allowlist that is absent, empty, or unparseable
fails closed — no host permitted, every fragment naming an upstream
quarantined (plan EC-14) — and fails this key.

### `openbao`

A **one-way startup gate**: false until the asynchronous first Kubernetes-auth
login succeeds, true from then on. It is never reset by a mid-life OpenBao
outage — withdrawing readiness mid-life would pull the whole gateway,
pass-through routes and cached routes included, out of the Service because
one dependency of *some* routes is down, converting a partial degradation into
a total one (Architecture, secret-store outage behavior). A mid-life outage
surfaces at `/config/status` and `/health/credentials` instead. The login loop
retries until success, so an OpenBao outage at startup degrades readiness
(503) rather than crash-looping the container.

### `credential_probe`

Satisfied when no credential probe is configured (no registry attached), when
the attached registry has no results yet (the probe loop is in cold start —
failing here would recreate the startup-503 class the `openbao` gate exists
for), or when **every** tracked probe result carries a successful verification
no older than **twice its own cadence plus a 5-minute grace**. The window
scales with each probe's configured interval because fragment intervals
legitimately range from minutes to a day; `LastVerified` is stamped only by a
2xx probe, so a probe that has never verified is never fresh. Readiness gates
on the *freshness of the verification signal*, not on any single credential's
health — an unhealthy credential is reported at `/health/credentials` and does
not by itself remove the pod from the Service.

## Independence

Dependencies evaluate independently: a failure in one never forces another
key false, and the body always reports all of them. `ready` is a plain AND —
any combination of failures collapses to the same 503, with the failing set
readable from the body.

## Relationship to liveness

`/_seam/healthz` (and the `/health/*` operator surface) keep answering while
`/_seam/readyz` is 503. Liveness reports the process is alive and its
listeners are bound; it deliberately does not fail on a quarantined fragment,
a pending login, or a stale probe, because restarting the pod fixes none of
them. Only readiness gates Service traffic.

## Test inventory

| Test | Contract clause pinned |
|---|---|
| `internal/server/readyz_contract_test.go` — `TestReadyzDependencyStateMatrix` | every dependency state: each key failing alone (independence), satisfiable states (no registry, cold start, fresh verification), combined failures (aggregate AND); status code, `application/json` content type, exact closed key set per state |
| `internal/server/readyz_test.go` | per-dependency failing→satisfied transitions, freshness window boundaries |
| `internal/server/reserved_endpoints_test.go` | 405 for non-GET; 200 body shape with `ready: true` |
| `internal/server/health_alias_test.go` | liveness stays 200 while readyz is 503 (empty route table) |
| `internal/server/openbao_readiness_test.go` | startup sequence: 503 until login, 200 after |

## Change discipline

Adding a readiness dependency means three changes in one commit: the new
constant and evaluation in `readyz.go`, a failing-state row in
`TestReadyzDependencyStateMatrix`, and a row in the key table above. The
matrix's closed-key-set assertion fails until all three agree, so the wire
contract and its spec cannot drift apart silently.
