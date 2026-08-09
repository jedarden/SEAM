# ADR-001: Language Choice for SEAM Gateway

**Date:** 2026-07-20  
**Status:** Ratified  
**Decision:** Adopt **Go** as the SEAM gateway's implementation language

---

## Decision

Adopt **Go** as the SEAM gateway's implementation language, per the research's primary recommendation in `docs/research/language-runtime-choice.md`.

---

## Rationale

### 1. The validator is the product
SEAM's differentiating feature is **schema-validated, structured, field-level 400 responses**. 

- **pb33f `libopenapi` + `libopenapi-validator`** is the only mature, off-the-shelf OpenAPI 3.1 request validator found across all four languages surveyed (Go, Rust, TypeScript/Node, Python)
- Its error model (JSON pointer + spec line/col + `HowToFix`) maps directly onto SEAM's structured-error feature — it is **better than what we would hand-design**
- Every other language means either building this by hand (Rust) or accepting a wiring/bus-factor risk on SEAM's core value proposition (Python, Node)

### 2. Tailscale support is first-party and production-grade in Go alone
- `tsnet` (embedded node) and `client/local` (WhoIs against a sidecar) are maintained by Tailscale itself
- Directly relevant to the pilot phases and to Phase 7's per-agent Grant-based scoping
- The Rust equivalent (`tailscale-rs`) is explicitly experimental with no WhoIs API

### 3. The proxy layer's security posture comes free
- stdlib `httputil.ReverseProxy`'s `Rewrite` hook strips hop-by-hop and client-spoofable `X-Forwarded-*` headers **before user code runs**, by design
- Closes the exact secret/header-injection pitfall flagged in `docs/research/secret-injection-gateways.md`
- SEAM does not need to reimplement that hardening itself

### 4. The cost is real but bounded and one-time
- Go is a new toolchain in a shop whose CI is otherwise Rust-centric (`rust-verify` on iad-ci)
- That cost is paid **once at setup**, not per-route the way a hand-rolled validator's maintenance would recur

---

## Alternatives Considered

### Rust (runner-up)
- **Pros:** The shop's primary language; `rust-verify` remote CI already exists
- **Clears:** 6 of 7 requirements with best-in-class parts (axum/hyper/tower proxy stack; `jsonschema` 0.48; best footprint at ~10-25MB)
- **Gap:** The product's core feature — no mature off-the-shelf 3.1 request validator exists (sole candidate is a 45-star crate with no crates.io release)
- **Cost:** Building one is a scoped, bounded effort (~1-2k LOC, 1-2 weeks) — puts SEAM's differentiator on our own maintenance budget instead of an actively-maintained upstream's
- **Kept as the fallback** (see Consequences)

### Python
- **Pros:** Every requirement clears; verified by live execution on this host (openapi-core 0.23.1: 0.59ms/request validation)
- **Ruled out on:**
  - Footprint: 170-200MB image vs. Go's 15-30MB
  - Bus-factor risk: openapi-core is a single-maintainer dependency with documented breaking 0.x API churn

### TypeScript/Node
- **Pros:** Ajv2020 is the most battle-tested 2020-12 schema engine surveyed
- **Ruled out on:**
  - Every library wrapping it for OpenAPI made an overstated 3.1 claim (openapi-backend silently runs draft-07-mode Ajv despite 3.1 checkbox; express-openapi-validator is Express-bound)
  - WebSocket proxy support is self-described "partial" (first subprotocol only)
  - No footprint advantage over Go

---

## Consequences

### Immediate effects
- **The LANGUAGE blocker is closed** — this resolves the primary open question blocking all implementation phases
- **Phase 1a is unblocked** (Go scaffold, HTTP server, `/docs` + `/docs/{route}` + `/openapi.json` over a hand-written whole spec, structured validation errors)
- **Phase 1b remains gated** on the route-fragment schema (Open Question 1, bead `bf-2wt`) — this is now the critical path for the project

### CI/CD impact
- SEAM becomes the **second language** in this shop's container fleet alongside Rust
- The existing `cargo`/`cargo-remote` CI-offload path does not apply
- A Go build path is needed once code exists — tracked as a follow-on bead (e.g., `seam-ci` Argo WorkflowTemplate on iad-ci)
- SEAM ships as a plain container regardless of build tooling

### Architectural implications
- The validator boundary is **narrow and replaceable**: parse + merge + validate (~1-2k LOC, per the comparison matrix in `docs/research/language-runtime-choice.md`)
- If Go proves to be the wrong call in practice, replacing that one component (or, worst case, the whole gateway in Rust per the runner-up analysis) is a **bounded rewrite, not a redesign** of SEAM's architecture

---

## Reference

- Full ADR-001 text: See `docs/plan/plan.md` section starting at line 1054
- Research backing: `docs/research/language-runtime-choice.md` (2026-07-16)
