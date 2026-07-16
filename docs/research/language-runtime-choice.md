# Language/Runtime Choice for the SEAM Gateway

Research conducted 2026-07-16 via four parallel research agents (Go, Rust, TypeScript/Node, Python), each verifying library versions and OpenAPI-3.1-support claims against the actual registries/repos/changelogs — not memory. The Python agent additionally verified its claims **empirically on this host** (ran openapi-core 3.1 validation with JSON Schema 2020-12 features, and a live `tailscaled` WhoIs call over the Unix socket). Decision pending review; recommendation at the end.

## Decision criteria

1. Parse and **merge** multiple OpenAPI 3.1 fragment files into one spec at runtime, re-merging on hot reload.
2. **Validate incoming requests** against the merged spec — genuine 3.1 (JSON Schema 2020-12), with structured field-level errors. This is SEAM's core differentiator, not a checkbox.
3. Reverse-proxy: inbound header stripping, outbound secret injection, streaming, WebSocket passthrough.
4. OpenBao client.
5. Tailscale identity of inbound connections (WhoIs), via embedded tsnet and/or tailscaled-sidecar LocalAPI.
6. File-watch hot reload with atomic route-table swap.
7. Small Kubernetes container serving dozens of concurrent agent callers.

## Comparison matrix

| | Go | Rust | TypeScript/Node | Python |
|---|---|---|---|---|
| 3.1 request validator, off the shelf | **Yes — pb33f libopenapi-validator** (3.0/3.1/3.2-native) | **No** — build ~1–2k LOC on `jsonschema` crate | Partial — Ajv2020 is excellent but library wiring is inconsistent | **Yes — openapi-core** (3.1 verified empirically) |
| Validation-error quality | **Best surveyed**: JSON pointer + spec line/col + `HowToFix` | Excellent raw (`instance_path`/`schema_path`) but you assemble it | Good (Ajv `ErrorObject[]`) once wired correctly | Good (`json_path`/validator/message) after unwrapping exception chain |
| Spec merge | Speakeasy `Join` or hand-rolled map-merge | Hand-rolled (~200–400 LOC) | Hand-rolled (~100 LOC); Redocly `join` is CLI-only | Hand-rolled (~50 LOC), demonstrated working |
| WhoIs via tailscaled sidecar | First-party (`client/local`) | ~100 LOC LocalAPI call (prior art exists) | ~20 LOC unix-socket call | ~10 LOC httpx-over-UDS (**live-tested on this host**) |
| Embedded tailnet node (tsnet) | **Production-grade, first-party** | Experimental only (`tailscale-rs`: env-gated, DERP-only, no WhoIs API) | None | None |
| OpenBao client | **Native** (`openbao/api/v2`) | `vaultrs` 0.8 (official OpenBao support) | node-vault or raw fetch | hvac (sync — thread-wrap) |
| Proxy + WebSocket | stdlib `httputil.ReverseProxy`, WS passthrough since Go 1.12 | axum/hyper/tower + `axum-reverse-proxy` (WS tested) | `@fastify/reply-from` / `http-proxy` (WS "partial") | starlette/httpx + fastapi-proxy-lib (small project) |
| Image / idle RSS | ~15–30 MB / 15–40 MB | **~10–25 MB / 5–30 MB** | ~55 MB base compressed / 50–80 MB | ~170–200 MB / 60–120 MB |
| Team fit (this shop) | No existing Go codebase; modest CI add | **Primary language; `rust-verify` CI exists** | Websites only | Scripts only |
| Key risk | pb33f bus factor (one org; counterweight: weekly releases, 1 open core issue) | Owning the validator — SEAM's core feature — as bespoke code | "3.1 support" claims need per-library conformance testing | Single-maintainer validator with 0.x API churn history |

## Go

**The only ecosystem with a production-grade, genuinely 3.1-native HTTP request validator.**

- **pb33f libopenapi v0.38.7** (2026-07-15; 3.0/3.1/3.2, weekly releases, 1 open issue) + **libopenapi-validator v0.14.0** (2026-07-11): validates `*http.Request` directly against the spec, defaulting to 3.1 semantics (JSON Schema 2020-12 via `santhosh-tekuri/jsonschema`). Error objects carry `Message`, `Reason`, `SpecLine`/`SpecCol`, **`HowToFix`**, and nested schema errors with JSON-pointer locations — the best structured-error model found in any language, and it maps directly onto SEAM's structured-400 feature.
- **kin-openapi** (the famous option) only gained 3.1 in v0.136.0 (2026-04), self-assessed ~60–70% complete (`components.pathItems` silently dropped, `$dynamicRef` unresolved). Fallback, not primary.
- **Merge:** no single merge+validate library — use **speakeasy-api/openapi v1.24** `Join()` (conflict strategies included) or a hand-rolled map-merge feeding the validator, rebuilt behind an `atomic.Pointer` on each reload. Glue code, not a gap.
- **Tailscale:** home turf. `tsnet` v1.100.0 embeds a full node in-process; `client/local` (note: package moved from `client/tailscale` — pre-2025 examples are stale) does WhoIs against a sidecar socket. Both modes first-party.
- **OpenBao:** native `github.com/openbao/openbao/api/v2` client exists.
- **Proxy:** stdlib `httputil.ReverseProxy` — the modern `Rewrite` hook strips hop-by-hop and client-sent `X-Forwarded-*` **before** user code runs (the CVE-class trust-then-overlay pitfall is handled by design); auto-flushed streaming; WebSocket Upgrade passthrough automatic since Go 1.12 (edge cases: `ModifyResponse` not applied to 101s, no TCP half-close on upgraded conns).
- **Footprint:** ~20 MB scratch image sidecar-mode; tsnet-embedded roughly doubles the binary (~40–60 MB) and needs a writable state dir.

## Rust

**Clears six of seven requirements with best-in-class parts; fails the seventh as an off-the-shelf purchase.**

- **The gap:** no mature spec-first 3.1 request validator. The only candidate (`baerwang/openapi-rs`) is 45 stars with no crates.io release — design reference, not a dependency. Building it: `oas3` 0.22 (native 3.1 parser) + `matchit` path routing + precompiled **`jsonschema` 0.48** validators (the most actively maintained 2020-12 engine in any ecosystem; `instance_path`/`schema_path` on every error) ≈ 1–2k LOC, realistically 1–2 weeks if parameter styles are scoped to `form`/`simple` (reject exotic styles at lint time). Error quality would end up excellent *because* the shape is under our control — but the validator is then bespoke code to own forever, and it is SEAM's core feature.
- **Merge:** hand-rolled (~200–400 LOC); all existing mergers are Node CLIs.
- **Tailscale:** `tailscale-rs` (official native-Rust tsnet, announced Apr 2026) is explicitly experimental — env-var-gated, unaudited crypto, DERP-relay-only, **no WhoIs API as of 0.4.0**. The old `tsnet` crate/libtailscale bindings are dead ends (no whois in the C header). Production path is the tailscaled-sidecar LocalAPI call (~100 LOC, prior art in `tailscale-localapi` crate).
- **OpenBao:** `vaultrs` 0.8.0 (2026-03) lists official OpenBao support; slow (~annual) cadence, raw-HTTP fallback is ~200 LOC.
- **Proxy:** axum/hyper/tower is arguably the strongest proxy toolkit here — the tower layer model matches validate→strip→inject→forward exactly; `axum-reverse-proxy` 1.3 has tested WebSocket passthrough. `notify` + `arc-swap` for hot reload is a well-trodden pattern.
- **Footprint:** best of all — ~10–25 MB image, 5–30 MB RSS. Shop fit is also best: primary language, `rust-verify` CI already exists.
- Fragment caveat: `oas3` may misparse 3.0-format documents — `seam lint` must enforce `openapi: 3.1.x` on fragments (worth doing regardless of language).

## TypeScript/Node

**Ajv is the most battle-tested 2020-12 engine anywhere, but "OpenAPI 3.1 support" means three different things across the libraries that wrap it.**

- **openapi-backend 5.18** — best architectural shape (framework-agnostic, in-memory spec, atomic instance swap for hot reload) — but its source instantiates draft-07-mode `Ajv`, not `Ajv2020`, despite the README's 3.1 checkbox; correct 2020-12 semantics require the `customizeAjv` escape hatch plus a conformance test.
- **express-openapi-validator 5.6** — the most genuine out-of-the-box 3.1 (ships a `$dynamicAnchor`→`$ref` rewrite workaround), but Express-bound, drags in `multer`, 246 open issues, and hot reload means rebuilding the middleware stack.
- **fastify-openapi-glue** — healthiest repo, but Fastify freezes routes at boot: spec reload requires `@fastify/restartable`. Wrong shape for requirement 6.
- **Merge:** DIY (~100 LOC); `openapi-merge` is dead for 3.1 (open issue since 2024), Redocly `join` handles 3.1 but is CLI-only.
- **Tailscale:** no client on npm; WhoIs is a ~20-line `undici` unix-socket call.
- **Proxy:** node-http-proxy is dead (2020); the live options are `@fastify/reply-from`/`@fastify/http-proxy` (undici-based, streaming, header-rewrite hooks; WebSocket support self-described "partial" — first subprotocol only).
- **Footprint:** ~55 MB compressed base image, 50–80 MB RSS; no credible single-binary story (Node SEA embeds the ~110 MB runtime).

## Python

**Every requirement clears, and the validator claims were verified empirically — but the footprint is the largest and the core dependency is a bus-factor-one project.**

- **openapi-core 0.23.1** (2026-04): real 3.1/2020-12 request validation — **empirically confirmed on this host** with type arrays, `exclusiveMaximum`, `const`, `prefixItems`; `iter_errors()` yields field-level errors (`$.name | type | 123 is not of type 'string', 'null'`) after unwrapping the exception chain (~30-line adapter). Measured 0.59 ms/request validation and a **16 ms full spec rebuild** — hot reload is trivial. Risks: one primary maintainer, documented history of breaking 0.x churn, dep-pin friction.
- **connexion ruled out** — exactly the right shape, but its OpenAPI 3.1 PR has been open and unmerged since 2021.
- **Merge:** dict-merge ~50 lines, demonstrated working; validate result with openapi-spec-validator.
- **Tailscale:** ten lines of httpx-over-UDS — **live-tested against this host's tailscaled** (returned real node + user identity). Name trap: the PyPI `tailscale` package is the SaaS API, not LocalAPI.
- **OpenBao:** hvac 2.4 works unmodified (OpenBao commits to Vault API compatibility); hvac is sync — `asyncio.to_thread()` or raw httpx.
- **Proxy:** starlette + httpx streaming pattern is ~100 LOC; fastapi-proxy-lib solves the WebSocket edge cases but is a small single-author project (vendor if it goes quiet).
- **Footprint:** ~170–200 MB image, 60–120 MB realistic RSS — operationally fine for one Deployment, visibly the heaviest option.

## Cross-cutting findings

- **Fragment merging is DIY in every language.** No ecosystem ships a maintained, 3.1-aware, runtime-embeddable multi-spec join. This weakens "library availability" as a merge criterion — the merge is 50–400 LOC of controlled-input code anywhere, and `seam lint` gates fragment quality before the gateway ever sees them.
- **Body buffering on validated routes is universal.** Validating a JSON body means reading it fully before proxying, in any language. Streaming passthrough applies to non-validated content types; this is inherent to requirement 2, not a language property.
- **WhoIs via tailscaled sidecar works everywhere** (10–100 LOC). The languages only diverge on **embedded** tsnet — production-grade in Go alone. Note for Phase 7: per-worker embedded identity is a **NEEDLE-side** (Rust) problem regardless of SEAM's language, and `tailscale-rs` is not production-ready — the worker identity mechanism will likely be a tailscaled-per-worker or small Go shim either way. SEAM's language choice does not solve or worsen that.
- **OpenAPI 3.1 support claims require verification.** Two of four ecosystems had a headline library whose "3.1 support" was materially overstated (kin-openapi ~60–70% complete; openapi-backend wiring draft-07 Ajv). Whatever is chosen, a 2020-12 conformance test (`prefixItems`, type arrays, `const`, `unevaluatedProperties`) belongs in SEAM's test suite from day one.

## Recommendation

**Go**, despite Rust being the shop language. The reasoning:

1. **The validator is the product.** SEAM's defining feature is schema-validated, structured, field-level error responses. pb33f libopenapi-validator is the only mature library anywhere that does genuine 3.1 request validation, and its error model (JSON pointer + spec line/col + `HowToFix`) is not just adequate but *better than what we would design ourselves* — those fields flow straight into SEAM's structured 400s. Rust would mean hand-building SEAM's most important component; Python and Node would mean accepting either a bus-factor-one dependency or DIY Ajv wiring plus conformance-testing burden.
2. **Tailscale is first-party.** WhoIs, Grants capability parsing, and (if ever wanted) embedded tsnet for the gateway's own tailnet presence are all native, maintained by Tailscale, with their own production precedents (TailSQL, Setec, Golink — the exact architectural analogs cited in `tailscale-identity-mechanisms.md`).
3. **The proxy layer's security posture comes free.** stdlib `ReverseProxy.Rewrite` strips hop-by-hop and spoofable forwarding headers *before* user code runs — the precise CVE-2025-64484-class pitfall flagged in `secret-injection-gateways.md` is handled by the standard library's design.
4. The costs are real but small: Go is new to this shop (one more toolchain in CI; no `rust-verify` reuse), and merge needs ~100 lines of glue plus the Speakeasy `Join` option. Against owning a bespoke validator forever, that trade favors Go decisively.

**Runner-up: Rust** — if "no new language in the shop" outweighs "don't hand-roll spec validation." The 1–2 week validator build is bounded and testable, `jsonschema` is the best schema engine surveyed, and the footprint/CI story is the best available. It is a defensible choice; it just puts SEAM's core feature on our maintenance budget instead of pb33f's.

Not recommended for this workload: **Python** (heaviest footprint, single-maintainer core dependency, sync Vault client) and **Node** (3.1 wiring inconsistencies demand per-library forensics, WebSocket proxying "partial", no footprint advantage over Go to compensate).

## Sources

Per-language source lists were compiled by the research agents with claims verified against: GitHub repos/issues/PRs (kin-openapi #230/#1125, connexion #1396/#1951, openapi-merge #113, golang/go #26937/#29627/#35892), package registries (pkg.go.dev, crates.io API, npm registry API, PyPI JSON API — all queried 2026-07-16), official docs (pb33f.io, openbao.org/api-docs/libraries, tailscale.com KB, nodejs.org SEA docs, redocly.com CLI docs), the Tailscale LocalAPI write-up (dorianmonnier.fr, 2024-12-26), and Docker Hub image-size APIs. Python findings additionally verified by execution: `test_openapi31.py` in the session scratchpad exercised openapi-core 0.23.1 validation semantics, error structure, timing (0.59 ms/req, 16 ms rebuild), and a live tailscaled WhoIs over `/run/tailscale/tailscaled.sock` on this host.
