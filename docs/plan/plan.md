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

## Data Models

- Route fragment schema (OpenAPI 3.1 fragment + `x-vault-path`, `x-inject-as`, and — per the per-agent scoping design below — `x-required-scope`) — not yet designed; needs a concrete JSON Schema before the first fragment is written.

## Future: Per-Agent Tool Scoping

Near-future requirement (see `docs/notes/objective.md`): configure specifically which routes a given agent/NEEDLE-worker may call, rather than every caller getting every route. Researched in detail in `docs/research/` (four threads: secret-injection prior art, agent tool-access-control state of the art, Tailscale-native identity mechanisms, NEEDLE's current identity model). Combined recommendation, from `docs/research/README.md`:

1. **Identity:** each NEEDLE worker/agent gets its own `tsnet`-embedded ephemeral Tailscale node, tagged (e.g. `tag:needle-worker`), so it's a first-class tailnet peer calling SEAM directly. Required, not optional — today's shared Tailscale Connector/egress proxy pods collapse every worker in a cluster into one indistinguishable caller from SEAM's `WhoIs` point of view. No SPIFFE/SPIRE or custom PKI — Tailscale already provides workload identity for this topology (precedent: Tailscale's own TailSQL/Setec/Golink).
2. **Authorization data:** scope claims (`service:action`, e.g. `k8s-ro:get`, `argocd:sync`) travel in the Tailscale Grant's opaque `app` capability field, read via `WhoIs` on the inbound connection. No separate JWT-issuing auth server needed for tailnet-native callers; fall back to a bearer-claim-in-header model only for callers that can't be first-class tailnet peers.
3. **Enforcement:** every SEAM route is tagged with its required scope(s) (`x-required-scope` in the route fragment); the gateway checks the caller's Grant-supplied scopes against them, default-deny. Never rely on the calling agent's own tool list as the boundary — client-side restriction is UX, not security (OWASP Agentic Top 10 consensus, and a live AutoGen cross-agent-tool-leak issue found in research).
4. **Gap to close first:** NEEDLE currently mints no identity/credential for spawned agents at all (`docs/research/needle-identity-model.md`). This needs new NEEDLE-side work in `run_process` (`~/NEEDLE/src/dispatch/mod.rs:703`) — provision a per-worker `tsnet` identity, inject it via the existing `adapter.environment` passthrough (or a new template variable) — before SEAM-side scoping can be enforced against anything real.

## Implementation Phases

- [ ] Phase 1: Gateway core — OpenAPI merge from a static local directory, `/docs` + `/openapi.json`, request validation with structured error responses. No secret injection or k8s yet — provable standalone.
- [ ] Phase 2: Secret injection — OpenBao client, `x-vault-path`/`x-inject-as` extension handling, inject-then-proxy.
- [ ] Phase 3: ConfigMap-mounted route fragments — projected volume, `reloader` annotation, first fragment (pilot: migrate the existing hand-rolled ArgoCD read-only proxy).
- [ ] Phase 4: Onboard z.ai/GLM proxy and twitterapi.io proxy fragments (no new Tailscale Connector needed).
- [ ] Phase 5: Onboard kubectl-proxy endpoints for additional clusters, adding a Tailscale Connector per cluster as needed.
- [ ] Phase 6: Deploy to rs-manager via declarative-config; cut agents over from hand-documented CLAUDE.md proxy instructions to SEAM routes.
- [ ] Phase 7: Per-agent tool scoping — NEEDLE-side per-worker `tsnet` identity provisioning, `x-required-scope` route tagging, Grant-based scope enforcement at the gateway (see Future section above). Depends on Phases 1–3 at minimum.

## Open Questions

- Exact route-fragment schema (OpenAPI fragment shape + extension fields, including `x-required-scope`) — not yet designed.
- Hot-reload vs. rolling-restart-via-reloader: is a sub-second reload worth building, or is a pod restart on ConfigMap change acceptable?
- Language/runtime choice — not yet decided (needs an OpenAPI-validation library with good ecosystem support and a Tailscale `tsnet`/`client/local` library; Go is the strongest candidate since both `tsnet` and `client/local` are native Go packages — worth weighing against Node/TypeScript or Python before committing).
- How gradual should the migration off CLAUDE.md-documented proxy instructions be, and when do the old hand-rolled proxies get retired?
- Does the Phase 7 scope-claim design need a full `service:action` taxonomy defined up front, or can it grow route-by-route as fragments are added?
- For non-tailnet-native callers that need the header-based scope fallback (per Future section, point 2) — which callers, if any, actually fall into this bucket once NEEDLE workers have `tsnet` identities? May turn out to be zero in practice.
