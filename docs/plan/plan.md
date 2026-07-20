# SEAM Plan

## Overview

SEAM (Self-documenting Endpoint Access Mediator) is a single unified endpoint that acts as a proxy to multiple authenticated services. It injects the required secret into a request received from an agent, sends the request on to the destination service, and passes the response back — the agent never sees the secret. It also self-documents: a merged OpenAPI spec drives `/docs` and `/openapi.json`, and malformed requests get a structured error naming the bad field and pointing at the right shape, instead of a bare 400.

## Motivation

Today, each backend service (ArgoCD read-only proxy, per-cluster kubectl-proxy, the z.ai/GLM proxy, the twitterapi.io proxy) is documented by hand in CLAUDE.md with its own discovery and auth pattern. This doesn't scale: every new service needs new hand-written prose, agents have no standard way to discover what's available, and there's no mechanism to guide an agent that sends a malformed request back toward the correct one.

## Architecture

- Single gateway process (reverse proxy) in front of N upstream services.
- Route table = one OpenAPI spec, assembled at startup (and reloaded on change) by merging multiple fragment files. Each fragment is an OpenAPI paths/components fragment plus custom extension fields — `x-vault-path` (which OpenBao secret to fetch) and `x-inject-as` (header / bearer / query param — how to inject it) — before proxying upstream.
- Route fragments are delivered as Kubernetes ConfigMaps, one per upstream service, mounted into the gateway pod via a `projected` volume at `/etc/gateway/routes.d/*.yaml`. Each fragment is owned and synced independently from its own service's path in declarative-config — no single monolithic file to edit for every new route.
- Route reloads are **in-process and sub-second** (decided 2026-07-16, superseding the earlier restart-via-`reloader` idea): the gateway file-watches `/etc/gateway/routes.d/`, re-merges and re-validates on change, and atomically swaps the route table — in-flight requests finish against the old table, new requests see the new one, zero dropped connections. With dozens of concurrent workers hitting this authentication layer, restart-based reload would be the disruptive option; no blue/green machinery or Traefik-side intelligent routing is needed for config changes, because nothing about the pod changes. Kubelet propagates projected-ConfigMap updates on its sync period (git→pod latency up to ~1 min after ArgoCD sync); if that latency ever matters, the upgrade path is watching ConfigMaps via the Kubernetes API (informer) instead of file mounts. Gateway **binary** deploys remain ordinary rolling updates with connection draining (see Version Migration Strategy §1) — for a stateless proxy that already provides blue/green's guarantee without running a second fleet.
- `/docs` serves a human-readable rendering of the merged spec (Redoc/Swagger UI); `/openapi.json` serves the raw merged spec for agents to fetch programmatically.
- Incoming requests are validated against the merged spec. A malformed request gets a structured 400 naming the bad field, what was expected, and a link to `/docs/{route}`.
- Secret injection reads from OpenBao (the central secrets store — the 4th instance runs on rs-manager) at request time, and never surfaces secret values to the calling agent.

## Hosting

- Candidate host: rs-manager (already runs ArgoCD, OpenBao, `reloader`, `kubernetes-reflector`, and a Tailscale operator with Connector-based egress).
- rs-manager currently has Tailscale Connector egress to 3 clusters only: ardenone-cluster, ardenone-hub, ardenone-manager. Reaching additional clusters (apexalgo-iad, iad-options, iad-kalshi, iad-native-ads, iad-ci, iad-acb, ord-devimprint) requires adding a Connector per cluster, following the pattern of the 3 existing ones.
- Both initial pilot targets (z.ai/GLM proxy, twitterapi.io proxy) already live on ardenone-cluster, which rs-manager already reaches — no new Connector needed for those two.

## Components

- **Gateway service** — HTTP server, written in **Go** (decided 2026-07-20, ADR-001 below): OpenAPI merge/validation (pb33f `libopenapi`/`libopenapi-validator`), secret injection, `/docs` + `/openapi.json`.
- **Route fragments** — per-service ConfigMaps, each an OpenAPI fragment + injection metadata, authored alongside the service they describe.
- **Tailscale Connectors** — per-cluster egress, added incrementally as new upstreams are onboarded.
- **`seam` CLI** — the gateway's merge/validation engine packaged as a command-line tool for CI and fragment authoring (`lint`, `diff`, `import`); see Fragment Toolchain below.

## Data Models

- Route fragment schema (OpenAPI 3.1 fragment + extension fields) — not yet designed; needs a concrete JSON Schema before the first fragment is written. Extension fields accepted so far:
  - `x-vault-path` — which OpenBao secret to fetch
  - `x-inject-as` — how to inject it (header / bearer / query param)
  - `x-required-scope` — scope(s) a caller must hold (per the per-agent scoping design below)
  - `x-credential-probe` — a designated safe request (path + method + **per-fragment probe interval**; decided 2026-07-16 — some credentials rotate every ~3 days, others essentially never, so cadence is per-fragment, not a global setting) the credential sentinel uses to validate this fragment's secret against its upstream
  - `x-seam-schema` — an explicit fragment-schema version marker (e.g. `v1`) carried by every fragment from day one, so the fragment schema itself can evolve detectably — `seam lint` and the gateway can distinguish a v1 fragment from a v2 instead of guessing from shape
  - `x-loop-guard` — per-route repeated-identical-request thresholds (max repeats, window)
  - `x-cost-per-call` / `x-quota` — per-route cost annotation and per-caller budget; guards are configured individually per endpoint, never SEAM-wide
  - `x-upstream-map` — for multi-instance routes: maps an `{instance}` path parameter to upstream base URLs; the reserved instance `_all` fans out

## Future: Per-Agent Tool Scoping

Near-future requirement (see `docs/notes/objective.md`): configure specifically which routes a given agent/NEEDLE-worker may call, rather than every caller getting every route. Researched in detail in `docs/research/` (four threads: secret-injection prior art, agent tool-access-control state of the art, Tailscale-native identity mechanisms, NEEDLE's current identity model). Combined recommendation, from `docs/research/README.md`:

1. **Identity:** each NEEDLE worker/agent gets its own `tsnet`-embedded ephemeral Tailscale node, tagged (e.g. `tag:needle-worker`), so it's a first-class tailnet peer calling SEAM directly. Required, not optional — today's shared Tailscale Connector/egress proxy pods collapse every worker in a cluster into one indistinguishable caller from SEAM's `WhoIs` point of view. No SPIFFE/SPIRE or custom PKI — Tailscale already provides workload identity for this topology (precedent: Tailscale's own TailSQL/Setec/Golink).
2. **Authorization data:** scope claims (`service:action`, e.g. `k8s-ro:get`, `argocd:sync`) travel in the Tailscale Grant's opaque `app` capability field, read via `WhoIs` on the inbound connection. No separate JWT-issuing auth server needed for tailnet-native callers; fall back to a bearer-claim-in-header model only for callers that can't be first-class tailnet peers. The fallback bucket is real, not hypothetical (decided 2026-07-16): **"foreign" NEEDLE workers running outside the tailnet** will still need these tools, likely ingressing via something like a Cloudflare Tunnel — so the bearer-claim path is a first-class (if secondary) auth mode, and the design must cover how that non-tailnet ingress itself authenticates (e.g. Cloudflare Access service tokens) before any scope claim is trusted.
3. **Enforcement:** every SEAM route is tagged with its required scope(s) (`x-required-scope` in the route fragment); the gateway checks the caller's Grant-supplied scopes against them, default-deny. Never rely on the calling agent's own tool list as the boundary — client-side restriction is UX, not security (OWASP Agentic Top 10 consensus, and a live AutoGen cross-agent-tool-leak issue found in research).
4. **Gap to close first:** NEEDLE currently mints no identity/credential for spawned agents at all (`docs/research/needle-identity-model.md`). This needs new NEEDLE-side work in `run_process` (`~/NEEDLE/src/dispatch/mod.rs:703`) — provision a per-worker `tsnet` identity, inject it via the existing `adapter.environment` passthrough (or a new template variable) — before SEAM-side scoping can be enforced against anything real.
5. **Self-service surface:** authorization must be discoverable, not opaque. `/whoami` returns the caller's resolved tailnet identity, tags, and effective scopes; `/openapi.json` and `/docs` filter to the routes the caller may actually invoke, so agents never spend context on unreachable routes; and a 403 names the exact missing scope plus the Grant snippet that would fix it — authorization debugging becomes "ask the gateway," not "read the tailnet policy file."
6. **Scope taxonomy:** grows route-by-route as fragments declare `x-required-scope` — no up-front `service:action` catalog (decided 2026-07-16). The live taxonomy must stay dynamic and discoverable: enumerable from the merged spec (e.g. a `/scopes` view) rather than maintained as a separate document. A future SEAM shows different taxonomy slices to different agents, RBAC-style — an agent sees only the portion of the taxonomy its grants intersect, consistent with the scope-filtered spec in point 5.

## Version Migration Strategy

NEEDLE workers run perpetually-live sessions — a session may learn the API contract once (fetch `/openapi.json` early) and keep calling routes on that understanding for hours, with no guarantee it ever restarts or refetches the spec. Full design in `docs/notes/versioning.md`; precedent research in `docs/research/version-migration-precedent.md`. Two distinct problems:

1. **Gateway process deploys** (SEAM's own code changes, contract unchanged) — standard Kubernetes rolling-update mechanics: ≥2 replicas, `PodDisruptionBudget minAvailable: 1`, readiness-gated rollout, graceful `SIGTERM` draining. Solved by ordinary deployment hygiene, not a SEAM-specific feature.
2. **Route contract versioning** (a route's params/response/injection shape changes in a breaking way) — borrows Kubernetes' API deprecation policy (old and new versions coexist as long as anything calls the old one — this falls out of the existing ConfigMap-per-fragment model for free, a breaking change just ships as a new fragment rather than an in-place edit) and Stripe's adapter model (implement the route once against the newest contract, add a thin request/response adapter per older version still being served, rather than N parallel full implementations). Deprecated fragments emit `Deprecation`/`Sunset` headers (advisory only) and are retired when a per-route-version request-count metric has read zero for a quiet window — usage-gated, never calendar-gated.
3. **Passive drift signaling** (helping perpetually-live callers notice contract change mid-session) — every response carries an `X-SEAM-Spec-Version` header (a hash of the currently-served merged spec), so a worker that cached the contract hours ago sees the version change on a call it was already making, at no extra request cost. The catch-up diff is **progressively discoverable** (decided 2026-07-16), giving schema-diff fidelity without the token cost of dumping full schemas: level 1, `/changes?since=<version>`, returns a token-cheap route-level list (path, verb, change kind, one-line summary, drill-down link); level 2, `/changes?since=<version>&route=<path>`, returns the full field-level schema diff for that one route; prior full specs stay fetchable at `/openapi.json?version=<v>`. An agent notices the header change, pulls level 1, and drills into only the routes it actually uses. Backed by a ring buffer of the last N merged specs; this gives the advisory `Deprecation`/`Sunset` headers the actionable catch-up path they lacked, and usage-gated retirement remains the enforcement backstop. Full design in `docs/notes/versioning.md`.

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

- The gateway records the timestamp of the most recent successful (2xx) response per route (its "last-200"). `/docs` renders it per path — "last succeeded 2m ago" — an honest signal distinguishing verified-working from merely-documented. A route with no successful call renders an **intentionally empty** last-success value, presented as "no attempt since last restart" (decided 2026-07-16) — never "never succeeded," which reads as broken for a freshly added route. Tracking is in-memory and restart-scoped, deliberately not persisted.
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

- [ ] Phase 1: Gateway core (Go, ADR-001) — OpenAPI merge from a static local directory, `/docs` + `/openapi.json`, request validation with structured error responses. Includes per-fragment quarantine + `/config/status`, and the `X-SEAM-Spec-Version` response header (hash of the merged spec). No secret injection or k8s yet — provable standalone.
- [ ] Phase 2: Secret injection — OpenBao client, `x-vault-path`/`x-inject-as` extension handling, inject-then-proxy.
- [ ] Phase 3: ConfigMap-mounted route fragments — projected volume, in-process file-watch hot reload with atomic route-table swap (see Architecture), first fragment (pilot: migrate the existing hand-rolled ArgoCD read-only proxy).
- [ ] Phase 4: Onboard z.ai/GLM proxy and twitterapi.io proxy fragments (no new Tailscale Connector needed).
- [ ] Phase 5: Onboard kubectl-proxy endpoints for additional clusters, adding a Tailscale Connector per cluster as needed.
- [ ] Phase 6: Deploy to rs-manager via declarative-config; cut agents over **service by service** (decided 2026-07-16) — as each service's fragment goes live, its hand-written CLAUDE.md proxy prose is deleted in the same change; never a big-bang cutover. The end state flips the default from "here are your prescribed tools" to "here is your tool interface — list the routes and choose from that list": CLAUDE.md eventually carries only the SEAM endpoint pointer.
- [ ] Phase 7: Per-agent tool scoping — NEEDLE-side per-worker `tsnet` identity provisioning, `x-required-scope` route tagging, Grant-based scope enforcement at the gateway, plus the self-service surface (`/whoami`, scope-filtered `/openapi.json` + `/docs`, 403s naming the missing scope and the Grant snippet that would fix it). Depends on Phases 1–3 at minimum.
- [ ] Phase 8: Version migration tooling — `Deprecation`/`Sunset` header emission for fragments marked `deprecated: true`, per-route-version request-count metric, usage-gated retirement workflow, and the `/changes?since=<spec-version>` diff endpoint backed by a ring buffer of prior merged specs (see Version Migration Strategy above). Depends on Phase 1 (route fragment loading + spec-version header) at minimum; the per-caller breakdown enhancement depends on Phase 7.
- [ ] Phase 9: Fragment toolchain — `seam lint` / `seam diff` as a declarative-config CI gate, `seam import --from-url` fragment bootstrapping. Depends on Phase 1 (reuses the merge/validation engine); runtime quarantine itself ships with Phase 1.
- [ ] Phase 10: Multi-instance routes — `{instance}` path parameter + `x-upstream-map` resolution, then `_all` fan-out with instance-labeled merged results. Depends on Phase 1; should land before (or with) Phase 5 so kubectl-proxy onboarding uses one parametrized fragment instead of one per cluster.
- [ ] Phase 11: Passive route health — per-route last-2xx tracking surfaced in `/docs`, `/health/upstreams` aggregation, per-upstream circuit breaker with structured 503s. Depends on Phases 1–2 (needs real proxied traffic).
- [ ] Phase 12: Credential health sentinel — `x-credential-probe` background validation loop, `/health/credentials`, 401-triggered refetch-and-retry-once, secret-echo scrubbing of responses. Depends on Phase 2.
- [ ] Phase 13: Per-route guards — `x-loop-guard` loop breaker, `x-cost-per-call`/`x-quota` cost governor with `X-SEAM-Budget-Remaining`, `X-SEAM-Dry-Run` validation-only mode. Depends on Phase 1; per-caller keying sharpens once Phase 7 identity exists.

## Open Questions

(Resolved questions are folded into their sections above with "decided 2026-07-16" markers; per-caller version pinning is parked in `docs/notes/per-caller-version-pinning.md`, quiet-window options are compared in `docs/notes/quiet-window-options.md`. Language/runtime choice resolved 2026-07-20 — see ADR-001 below.)

- Exact route-fragment schema (OpenAPI fragment shape + extension fields) — not yet designed. High-stakes: once fragments exist across declarative-config, a schema change ripples through every onboarded service's ConfigMap — SEAM's own versioning problem, with no adapter layer to hide behind. Design once, before the first fragment; the `x-seam-schema` version marker (see Data Models) is the escape hatch if evolution is needed anyway.
- Fan-out (`_all`) partial-failure semantics — pending decision between fail-closed (any instance error fails the whole request), fail-open 200 with per-instance error entries plus hard-to-miss summary counts, or 207-style multi-status. See discussion of consequences in the 2026-07-16 review.
- Loop-guard keying before Phase 7: with callers indistinguishable behind shared egress, guards key on (route, request-hash) only — are per-route thresholds still safe to enable, or should `x-loop-guard` wait for identity? (Not yet reviewed.)

## ADR-001: 2026-07-20 — Ratify Go as the SEAM Gateway Implementation Language

### Context

Phase 1 (gateway core) has been blocked since the repo was scaffolded on 2026-07-15 — as of this writing (2026-07-20) the repo contains only `docs/`, zero implementation code. `docs/research/language-runtime-choice.md` (2026-07-16) benchmarked Go, Rust, TypeScript/Node, and Python against SEAM's seven hard requirements (3.1 fragment merge, genuine 3.1 request validation with structured field-level errors, reverse-proxy with streaming/WebSocket passthrough, OpenBao client, Tailscale WhoIs/tsnet, file-watch hot reload with atomic swap, small-footprint k8s container), with library-version and 3.1-conformance claims checked against actual registries/repos/changelogs rather than recalled from memory, and the Python findings additionally executed live on this host. The plan's Open Questions section has carried "decision pending review" since that research landed, and every phase from 1 onward depends on it. This ADR closes that gap.

### Decision

Adopt **Go** as the SEAM gateway's implementation language, per the research's primary recommendation.

1. **The validator is the product.** SEAM's differentiating feature is schema-validated, structured, field-level 400 responses. pb33f `libopenapi` + `libopenapi-validator` is the only mature, off-the-shelf OpenAPI 3.1 request validator found across all four languages surveyed, and its error model (JSON pointer + spec line/col + `HowToFix`) maps directly onto SEAM's structured-error feature — it is better than what we would hand-design. Every other language means either building this by hand (Rust) or accepting a wiring/bus-factor risk on SEAM's core value proposition (Python, Node).
2. **Tailscale support is first-party and production-grade in Go alone.** `tsnet` (embedded node) and `client/local` (WhoIs against a sidecar) are maintained by Tailscale itself, directly relevant to the pilot phases and to Phase 7's per-agent Grant-based scoping. The Rust equivalent (`tailscale-rs`) is explicitly experimental with no WhoIs API as of this research.
3. **The proxy layer's security posture comes free.** stdlib `httputil.ReverseProxy`'s `Rewrite` hook strips hop-by-hop and client-spoofable `X-Forwarded-*` headers before user code runs, by design — closing the exact secret/header-injection pitfall flagged in `docs/research/secret-injection-gateways.md`, rather than requiring SEAM to reimplement that hardening itself.
4. **The cost is real but bounded and one-time.** Go is a new toolchain in a shop whose CI is otherwise Rust-centric (`rust-verify` on iad-ci), but that cost is paid once at setup, not per-route the way a hand-rolled validator's maintenance would recur.

### Alternatives Considered

- **Rust** (the shop's primary language; `rust-verify` remote CI already exists) — clears six of seven requirements with best-in-class parts (axum/hyper/tower proxy stack maps cleanly onto validate→strip→inject→forward; `jsonschema` 0.48 is the most actively maintained 2020-12 engine surveyed; best footprint at ~10-25MB). The one gap is the product's core feature: no mature off-the-shelf 3.1 request validator exists (the sole candidate is a 45-star crate with no crates.io release). Building one is a scoped, bounded effort (~1-2k LOC, 1-2 weeks, per the research) — a defensible runner-up if "no new language in this shop" is weighted above "don't own the validator," but it puts SEAM's differentiator on our own maintenance budget instead of an actively-maintained upstream's. Kept as the fallback (see Consequences).
- **Python** — every requirement clears, and uniquely among the four candidates was verified by live execution on this host (openapi-core 0.23.1: 0.59ms/request validation, 16ms full spec rebuild; a real WhoIs call over `tailscaled`'s Unix socket). Ruled out on footprint (170-200MB image vs. Go's 15-30MB) and on openapi-core being a single-maintainer dependency with a documented history of breaking 0.x API churn — a bus-factor risk on the validator, the same category of risk this decision is trying to avoid.
- **TypeScript/Node** — Ajv2020 is the most battle-tested 2020-12 schema engine surveyed, but every library wrapping it for OpenAPI made an overstated 3.1 claim under inspection (openapi-backend silently runs draft-07-mode Ajv despite its README's 3.1 checkbox; express-openapi-validator is Express-bound and drags in `multer`). WebSocket proxy support across the viable options is self-described "partial" (first subprotocol only). No footprint advantage over Go to compensate.

### Consequences

- Phase 1 (gateway core) is unblocked and can start as a Go scaffold; `seam lint`/`seam diff`/`seam import` (Phase 9, Fragment Toolchain) are Go CLI tools by the same extension.
- SEAM becomes the second language in this shop's container fleet alongside Rust. The existing `cargo`/`cargo-remote` CI-offload path (`~/.local/bin/cargo`, iad-ci `rust-verify`) does not apply; a Go build path is needed once code exists — tracked as a follow-on bead (see beads filed alongside this ADR), not a blocker to starting Phase 1, since SEAM ships as a plain container per the Hosting section regardless of build tooling.
- The validator boundary is narrow (parse + merge + validate, per the comparison matrix in `docs/research/language-runtime-choice.md`) — if Go proves to be the wrong call in practice, replacing that one component (or, worst case, the whole gateway in Rust per the runner-up analysis) is a bounded rewrite, not a redesign of SEAM's architecture.
