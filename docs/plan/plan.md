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

- Route fragment schema (OpenAPI 3.1 fragment + `x-vault-path`, `x-inject-as`) — not yet designed; needs a concrete JSON Schema before the first fragment is written.

## Implementation Phases

- [ ] Phase 1: Gateway core — OpenAPI merge from a static local directory, `/docs` + `/openapi.json`, request validation with structured error responses. No secret injection or k8s yet — provable standalone.
- [ ] Phase 2: Secret injection — OpenBao client, `x-vault-path`/`x-inject-as` extension handling, inject-then-proxy.
- [ ] Phase 3: ConfigMap-mounted route fragments — projected volume, `reloader` annotation, first fragment (pilot: migrate the existing hand-rolled ArgoCD read-only proxy).
- [ ] Phase 4: Onboard z.ai/GLM proxy and twitterapi.io proxy fragments (no new Tailscale Connector needed).
- [ ] Phase 5: Onboard kubectl-proxy endpoints for additional clusters, adding a Tailscale Connector per cluster as needed.
- [ ] Phase 6: Deploy to rs-manager via declarative-config; cut agents over from hand-documented CLAUDE.md proxy instructions to SEAM routes.

## Open Questions

- Exact route-fragment schema (OpenAPI fragment shape + extension fields) — not yet designed.
- Hot-reload vs. rolling-restart-via-reloader: is a sub-second reload worth building, or is a pod restart on ConfigMap change acceptable?
- Auth model for the gateway itself: same "Tailscale mesh is the auth boundary, no additional auth" pattern as the existing ArgoCD read-only proxy, or does SEAM need per-route scoping (e.g. some routes read-only, others read/write)?
- Language/runtime choice — not yet decided (needs an OpenAPI-validation library with good ecosystem support; candidates include Go, Node/TypeScript, Python).
- How gradual should the migration off CLAUDE.md-documented proxy instructions be, and when do the old hand-rolled proxies get retired?
