# SEAM Plan

## Overview

SEAM (Self-documenting Endpoint Access Mediator) is a single unified endpoint that acts as a proxy to multiple authenticated services. It injects the required secret into a request received from an agent, sends the request on to the destination service, and passes the response back — the agent never sees the secret. It also self-documents: a merged OpenAPI spec drives `/docs` and `/openapi.json`, and malformed requests get a structured error naming the bad field and pointing at the right shape, instead of a bare 400.

## Motivation

Today, each backend service (ArgoCD read-only proxy, per-cluster kubectl-proxy, the z.ai/GLM proxy, the twitterapi.io proxy) is documented by hand in CLAUDE.md with its own discovery and auth pattern. This doesn't scale: every new service needs new hand-written prose, agents have no standard way to discover what's available, and there's no mechanism to guide an agent that sends a malformed request back toward the correct one.

## Architecture

- Single gateway process (reverse proxy) in front of N upstream services.
- Route table = one OpenAPI spec, assembled at startup (and reloaded on change) by merging multiple fragment files. Each fragment is an OpenAPI paths/components fragment plus custom extension fields — `x-vault-path` (which OpenBao secret to fetch) and `x-inject-as` (header / bearer / query param — how to inject it) — before proxying upstream.
- Route fragments are delivered as Kubernetes ConfigMaps, one per upstream service, mounted into the gateway pod via a `projected` volume at `/etc/gateway/routes.d/*.yaml`. Each fragment is owned and synced independently from its own service's path in declarative-config — no single monolithic file to edit for every new route.
- `reloader.stakater.com/auto: "true"` on the gateway Deployment triggers a rolling restart when a mounted ConfigMap changes, so new/changed routes take effect without a separate watch loop.
- `/docs` serves a human-readable rendering of the merged spec (Redoc/Swagger UI); `/openapi.json` serves the raw merged spec for agents to fetch programmatically.
- Incoming requests are validated against the merged spec. A malformed request gets a structured 400 naming the bad field, what was expected, and a link to `/docs/{route}`.
- Secret injection reads from OpenBao (the central secrets store — the 4th instance runs on rs-manager) at request time, and never surfaces secret values to the calling agent.

## Hosting

- Candidate host: rs-manager (already runs ArgoCD, OpenBao, `reloader`, `kubernetes-reflector`, and a Tailscale operator with Connector-based egress).
- rs-manager currently has Tailscale Connector egress to 3 clusters only: ardenone-cluster, ardenone-hub, ardenone-manager. Reaching additional clusters (apexalgo-iad, iad-options, iad-kalshi, iad-native-ads, iad-ci, iad-acb, ord-devimprint) requires adding a Connector per cluster, following the pattern of the 3 existing ones.
- Both initial pilot targets (z.ai/GLM proxy, twitterapi.io proxy) already live on ardenone-cluster, which rs-manager already reaches — no new Connector needed for those two.

## Components

- **Gateway service** — HTTP server: OpenAPI merge/validation, secret injection, `/docs` + `/openapi.json`.
- **Route fragments** — per-service ConfigMaps, each an OpenAPI fragment + injection metadata, authored alongside the service they describe.
- **Tailscale Connectors** — per-cluster egress, added incrementally as new upstreams are onboarded.
- **`seam` CLI** — the gateway's merge/validation engine packaged as a command-line tool for CI and fragment authoring (`lint`, `diff`, `import`); see Fragment Toolchain below.

## Data Models

- Route fragment schema (OpenAPI 3.1 fragment + extension fields) — not yet designed; needs a concrete JSON Schema before the first fragment is written. Extension fields accepted so far:
  - `x-vault-path` — which OpenBao secret to fetch
  - `x-inject-as` — how to inject it (header / bearer / query param)
  - `x-required-scope` — scope(s) a caller must hold (per the per-agent scoping design below)
  - `x-credential-probe` — a designated safe request (path + method) the credential sentinel uses to validate this fragment's secret against its upstream
  - `x-loop-guard` — per-route repeated-identical-request thresholds (max repeats, window)
  - `x-cost-per-call` / `x-quota` — per-route cost annotation and per-caller budget; guards are configured individually per endpoint, never SEAM-wide
  - `x-upstream-map` — for multi-instance routes: maps an `{instance}` path parameter to upstream base URLs; the reserved instance `_all` fans out

## Future: Per-Agent Tool Scoping

Near-future requirement (see `docs/notes/objective.md`): configure specifically which routes a given agent/NEEDLE-worker may call, rather than every caller getting every route. Researched in detail in `docs/research/` (four threads: secret-injection prior art, agent tool-access-control state of the art, Tailscale-native identity mechanisms, NEEDLE's current identity model). Combined recommendation, from `docs/research/README.md`:

1. **Identity:** each NEEDLE worker/agent gets its own `tsnet`-embedded ephemeral Tailscale node, tagged (e.g. `tag:needle-worker`), so it's a first-class tailnet peer calling SEAM directly. Required, not optional — today's shared Tailscale Connector/egress proxy pods collapse every worker in a cluster into one indistinguishable caller from SEAM's `WhoIs` point of view. No SPIFFE/SPIRE or custom PKI — Tailscale already provides workload identity for this topology (precedent: Tailscale's own TailSQL/Setec/Golink).
2. **Authorization data:** scope claims (`service:action`, e.g. `k8s-ro:get`, `argocd:sync`) travel in the Tailscale Grant's opaque `app` capability field, read via `WhoIs` on the inbound connection. No separate JWT-issuing auth server needed for tailnet-native callers; fall back to a bearer-claim-in-header model only for callers that can't be first-class tailnet peers.
3. **Enforcement:** every SEAM route is tagged with its required scope(s) (`x-required-scope` in the route fragment); the gateway checks the caller's Grant-supplied scopes against them, default-deny. Never rely on the calling agent's own tool list as the boundary — client-side restriction is UX, not security (OWASP Agentic Top 10 consensus, and a live AutoGen cross-agent-tool-leak issue found in research).
4. **Gap to close first:** NEEDLE currently mints no identity/credential for spawned agents at all (`docs/research/needle-identity-model.md`). This needs new NEEDLE-side work in `run_process` (`~/NEEDLE/src/dispatch/mod.rs:703`) — provision a per-worker `tsnet` identity, inject it via the existing `adapter.environment` passthrough (or a new template variable) — before SEAM-side scoping can be enforced against anything real.
5. **Self-service surface:** authorization must be discoverable, not opaque. `/whoami` returns the caller's resolved tailnet identity, tags, and effective scopes; `/openapi.json` and `/docs` filter to the routes the caller may actually invoke, so agents never spend context on unreachable routes; and a 403 names the exact missing scope plus the Grant snippet that would fix it — authorization debugging becomes "ask the gateway," not "read the tailnet policy file."

## Version Migration Strategy

NEEDLE workers run perpetually-live sessions — a session may learn the API contract once (fetch `/openapi.json` early) and keep calling routes on that understanding for hours, with no guarantee it ever restarts or refetches the spec. Full design in `docs/notes/versioning.md`; precedent research in `docs/research/version-migration-precedent.md`. Two distinct problems:

1. **Gateway process deploys** (SEAM's own code changes, contract unchanged) — standard Kubernetes rolling-update mechanics: ≥2 replicas, `PodDisruptionBudget minAvailable: 1`, readiness-gated rollout, graceful `SIGTERM` draining. Solved by ordinary deployment hygiene, not a SEAM-specific feature.
2. **Route contract versioning** (a route's params/response/injection shape changes in a breaking way) — borrows Kubernetes' API deprecation policy (old and new versions coexist as long as anything calls the old one — this falls out of the existing ConfigMap-per-fragment model for free, a breaking change just ships as a new fragment rather than an in-place edit) and Stripe's adapter model (implement the route once against the newest contract, add a thin request/response adapter per older version still being served, rather than N parallel full implementations). Deprecated fragments emit `Deprecation`/`Sunset` headers (advisory only) and are retired when a per-route-version request-count metric has read zero for a quiet window — usage-gated, never calendar-gated.
3. **Passive drift signaling** (helping perpetually-live callers notice contract change mid-session) — every response carries an `X-SEAM-Spec-Version` header (a hash of the currently-served merged spec), so a worker that cached the contract hours ago sees the version change on a call it was already making, at no extra request cost. It can then hit `/changes?since=<version>` for a compact machine-readable diff (routes added / removed / changed) between that spec version and the current one, backed by a ring buffer of the last N merged specs. This gives the advisory `Deprecation`/`Sunset` headers the actionable catch-up path they lacked; usage-gated retirement remains the enforcement backstop. Full design in `docs/notes/versioning.md`.

## Credential Health Sentinel

SEAM holds both the secret (`x-vault-path`) and a way to test it (the upstream), so it can detect dead credentials before an agent does. Recurring pain this targets: cloudspace-admin OIDC tokens expiring every ~3 days, and a dead ESO static token going unnoticed for 16 days.

- A background loop validates each fragment's credential against its upstream via the fragment's `x-credential-probe` (a designated safe authenticated request), on a per-fragment cadence.
- `/health/credentials` reports per secret: last verified, current status, known expiry — never secret values.
- Self-healing on rotation: an upstream `401` invalidates the cached secret, refetches from OpenBao, and retries once (safe for any method — a 401 means the upstream rejected the request before processing it).
- Secret-echo scrubbing: upstream responses are scanned for the exact injected secret value and redacted before returning to the caller (some APIs echo tokens back in error bodies).

## Per-Route Guards: Loop Breaker and Cost Governor

SEAM is the sole chokepoint for metered upstreams (twitterapi.io per-call credits; the z.ai/GLM quota that has no queryable API) and for runaway agent retry loops (precedent: NEEDLE re-dispatching one failing bead 310×/$500). Guards are **configured individually per endpoint** in the route fragment — there are no SEAM-wide defaults, and a route without guard fields gets no guard.

- **Loop breaker** (`x-loop-guard`): counts repeated identical requests keyed by (caller, route, normalized request hash); exceeding the route's threshold returns a structured 429 telling the agent, in plain language, that it has sent the same failing request N times and should stop and re-read `/docs/{route}`. Before Phase 7 identity exists, callers behind shared egress are indistinguishable, so the key degrades to (route, hash) — thresholds must be set with that in mind.
- **Cost governor** (`x-cost-per-call`, `x-quota`): per-caller budgets on routes that cost money; responses carry `X-SEAM-Budget-Remaining` so agents can self-throttle. Doubles as the usage meter z.ai never shipped. Per-caller precision also sharpens with Phase 7.
- **Dry-run** (`X-SEAM-Dry-Run: 1` request header): validate a request against the spec and return the verdict without injecting a secret, proxying, or spending quota — agents can test their contract understanding against expensive routes for free.

## Multi-Instance Routes and Fan-Out

Many upstreams are N near-identical instances of the same resource — the per-cluster kubectl-proxies all expose a byte-identical API. Instead of N copy-paste fragments, one fragment declares an `{instance}` path parameter (e.g. `/k8s/{cluster}/...`) resolved against an `x-upstream-map` lookup table, collapsing the config N:1.

- The reserved instance `_all` fans the request out to every mapped upstream concurrently and returns one merged result labeled by instance — e.g. `/k8s/_all/api/v1/pods?fieldSelector=status.phase!=Running` answers "what's broken across the entire fleet" in one call.
- Partial-failure semantics for fan-out (some instances error or time out) are an open question below.

## Passive Route Health — Last-Success Tracking

`/docs` shouldn't just describe routes; it should demonstrate them. The signal is passive, derived from live traffic rather than synthetic probes:

- The gateway records the timestamp of the most recent successful (2xx) response per route (its "last-200"). `/docs` renders it per path — "last succeeded 2m ago" vs. "no successful call observed yet" — an honest signal distinguishing verified-working from merely-documented.
- `/health/upstreams` aggregates the same data per upstream into a single status page for the whole mesh: most recent success across the upstream's routes plus current breaker state.
- Per-upstream circuit breaker: consecutive failures open the breaker, and callers get a structured 503 naming the upstream, when it started failing, the last error seen, and a retry-after — so agents stop burning tokens retrying a dead service.
- Synergy with the credential sentinel: its probe traffic keeps at least one authenticated route per upstream warm, bounding staleness of the signal for probed upstreams.

## Fragment Toolchain (`seam` CLI)

The config surface is YAML fragments authored largely by agents and merged at runtime — one bad file must never break the gateway, and mistakes should be caught before merge, not at reload.

- **Runtime quarantine:** a fragment that fails schema validation or collides with an existing path is skipped, logged, and surfaced at `/config/status` (loaded fragments, versions, quarantined ones with their errors) — never fatal to the remaining routes.
- **`seam lint`:** the gateway's own merge/validation engine packaged as a CLI, run as a CI gate in declarative-config — validates fragment schema and detects path collisions before a ConfigMap ever ships.
- **`seam diff`:** renders the effective merged-spec change a PR would cause, for review.
- **`seam import --from-url`:** bootstraps a fragment from an upstream's own published OpenAPI spec (ArgoCD and Kubernetes both publish one), filtered to selected paths/verbs; injection metadata (`x-vault-path` etc.) is then curated by hand. Onboarding a new upstream drops from authoring YAML from scratch to curating a generated file.

## Implementation Phases

- [ ] Phase 1: Gateway core — OpenAPI merge from a static local directory, `/docs` + `/openapi.json`, request validation with structured error responses. Includes per-fragment quarantine + `/config/status`, and the `X-SEAM-Spec-Version` response header (hash of the merged spec). No secret injection or k8s yet — provable standalone.
- [ ] Phase 2: Secret injection — OpenBao client, `x-vault-path`/`x-inject-as` extension handling, inject-then-proxy.
- [ ] Phase 3: ConfigMap-mounted route fragments — projected volume, `reloader` annotation, first fragment (pilot: migrate the existing hand-rolled ArgoCD read-only proxy).
- [ ] Phase 4: Onboard z.ai/GLM proxy and twitterapi.io proxy fragments (no new Tailscale Connector needed).
- [ ] Phase 5: Onboard kubectl-proxy endpoints for additional clusters, adding a Tailscale Connector per cluster as needed.
- [ ] Phase 6: Deploy to rs-manager via declarative-config; cut agents over from hand-documented CLAUDE.md proxy instructions to SEAM routes.
- [ ] Phase 7: Per-agent tool scoping — NEEDLE-side per-worker `tsnet` identity provisioning, `x-required-scope` route tagging, Grant-based scope enforcement at the gateway, plus the self-service surface (`/whoami`, scope-filtered `/openapi.json` + `/docs`, 403s naming the missing scope and the Grant snippet that would fix it). Depends on Phases 1–3 at minimum.
- [ ] Phase 8: Version migration tooling — `Deprecation`/`Sunset` header emission for fragments marked `deprecated: true`, per-route-version request-count metric, usage-gated retirement workflow, and the `/changes?since=<spec-version>` diff endpoint backed by a ring buffer of prior merged specs (see Version Migration Strategy above). Depends on Phase 1 (route fragment loading + spec-version header) at minimum; the per-caller breakdown enhancement depends on Phase 7.
- [ ] Phase 9: Fragment toolchain — `seam lint` / `seam diff` as a declarative-config CI gate, `seam import --from-url` fragment bootstrapping. Depends on Phase 1 (reuses the merge/validation engine); runtime quarantine itself ships with Phase 1.
- [ ] Phase 10: Multi-instance routes — `{instance}` path parameter + `x-upstream-map` resolution, then `_all` fan-out with instance-labeled merged results. Depends on Phase 1; should land before (or with) Phase 5 so kubectl-proxy onboarding uses one parametrized fragment instead of one per cluster.
- [ ] Phase 11: Passive route health — per-route last-2xx tracking surfaced in `/docs`, `/health/upstreams` aggregation, per-upstream circuit breaker with structured 503s. Depends on Phases 1–2 (needs real proxied traffic).
- [ ] Phase 12: Credential health sentinel — `x-credential-probe` background validation loop, `/health/credentials`, 401-triggered refetch-and-retry-once, secret-echo scrubbing of responses. Depends on Phase 2.
- [ ] Phase 13: Per-route guards — `x-loop-guard` loop breaker, `x-cost-per-call`/`x-quota` cost governor with `X-SEAM-Budget-Remaining`, `X-SEAM-Dry-Run` validation-only mode. Depends on Phase 1; per-caller keying sharpens once Phase 7 identity exists.

## Open Questions

- Exact route-fragment schema (OpenAPI fragment shape + extension fields, including `x-required-scope`) — not yet designed.
- Hot-reload vs. rolling-restart-via-reloader: is a sub-second reload worth building, or is a pod restart on ConfigMap change acceptable?
- Language/runtime choice — not yet decided (needs an OpenAPI-validation library with good ecosystem support and a Tailscale `tsnet`/`client/local` library; Go is the strongest candidate since both `tsnet` and `client/local` are native Go packages — worth weighing against Node/TypeScript or Python before committing).
- How gradual should the migration off CLAUDE.md-documented proxy instructions be, and when do the old hand-rolled proxies get retired?
- Does the Phase 7 scope-claim design need a full `service:action` taxonomy defined up front, or can it grow route-by-route as fragments are added?
- For non-tailnet-native callers that need the header-based scope fallback (per Future section, point 2) — which callers, if any, actually fall into this bucket once NEEDLE workers have `tsnet` identities? May turn out to be zero in practice.
- Should SEAM eventually adopt Stripe-style per-caller version pinning (each identity locked to whatever spec version was live at its first call) once Phase 7 identity exists? Stronger guarantee than usage-gated retirement, but more machinery — not required for an MVP.
- What's the right length for the "quiet window" before a deprecated fragment is actually removed (7 days was used as an illustrative default in `docs/notes/versioning.md`, not a decided value)?
- Fan-out (`_all`) partial-failure semantics — what does the merged response look like when some instances error or time out (per-instance error entries? what overall status code?)?
- Loop-guard keying before Phase 7: with callers indistinguishable behind shared egress, guards key on (route, request-hash) only — are per-route thresholds still safe to enable, or should `x-loop-guard` wait for identity?
- `/changes` diff granularity — route-level added/removed/changed flags, or full field-level schema diffs?
- How should a route with no observed traffic render in last-success tracking — "never succeeded" reads as broken for a freshly added route?
- Credential probe cadence — a per-fragment field with a sane default, or one global interval?
