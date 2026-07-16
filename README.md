# SEAM

Self-documenting Endpoint Access Mediator — a single unified HTTP endpoint that proxies to multiple authenticated backend services, injecting the required secret into each request server-side (from OpenBao) before forwarding it on, then passing the response back. Calling agents never see the secret; malformed requests are guided toward the correct shape via a self-describing OpenAPI spec instead of a bare error.

## Structure

- `docs/notes/` — features, constraints, design decisions
- `docs/research/` — external reference material and prior art
- `docs/plan/plan.md` — complete application plan
