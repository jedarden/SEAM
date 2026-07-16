# Naming

**SEAM** — Self-documenting Endpoint Access Mediator.

Chosen to complete a sewing-kit theme running through the component naming convention:

- **NEEDLE** — stitches (agent worker fleet)
- **FABRIC** — the material (NEEDLE-telemetry dashboard)
- **CLASP** — fastens shut (deprecated, NEEDLE's predecessor)
- **SEAM** — the joint that holds it together (this gateway)

A seam is where separate pieces get joined — apt for a gateway that joins many independently-authenticated backend services into one access point. "Self-documenting" and "Mediator" name the two defining behaviors: it exposes a self-describing OpenAPI spec (`/docs`, `/openapi.json`) instead of hand-written prose docs, and it mediates every request by injecting the secret server-side so the calling agent never sees it.
