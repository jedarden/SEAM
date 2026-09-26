# x-unscrubbable — Acknowledgement Contract

**Created:** 2026-09-26
**Bead:** seam-c4625dec
**Purpose:** The complete contract for the `x-unscrubbable` route-fragment extension: who may acknowledge, how admission (schema + lint) and the runtime enforce the acknowledgement, and what observability each enforcement point emits. Resolves the open decision in `route-fragment-schema-limitations.md` §2.4 ("Does `x-unscrubbable` bypass ALL scrubbing, or just body validation?").

---

## The contract

`x-unscrubbable: acknowledged` asserts that a route's upstream responses may be
**unscannable** — content the proxy cannot reliably scan for echoed injected
credentials: opaque media types (binary, image/audio/video, unlisted
`application/*`), responses with an unsupported `Content-Encoding`, and
protocol upgrades.

The acknowledgement permits exactly one behavior change at runtime: an
unscannable response is **passed through whole — body, headers, and trailers
unsanitized — instead of refused**.

It is **not** a blanket scrubbing opt-out. Every response the proxy can scan is
scrubbed on an acknowledged route exactly as on an ordinary one: body,
headers, and trailers, buffered or incrementally, regardless of the
acknowledgement. Absence of the field is refusal, not permission; there is no
`false` form.

Request-side handling is outside the acknowledgement's scope. Inbound
sanitation strips caller-supplied credential locations before injection and
runs on every route; there is no request-side scrubbing for the
acknowledgement to bypass. (The §2.4 question "path parameter scrubbing? query
parameter scrubbing?" therefore has a short answer: unaffected — nothing
request-side changes.)

## Who may acknowledge

- **The fragment owner.** The extension is authored in the route fragment by
  the team named by `x-seam-owner` — the same identity that owns the
  `x-vault-path` credential the route injects. Accountability for the exposure
  travels with that owner field.
- **Only a route that injects a credential.** Scrubbing exists to catch echoed
  injected credentials, so on a pass-through or adapter fragment (no
  `x-vault-path` + `x-inject-as` pair) the acknowledgement guards nothing.
  `seam lint` flags this as `scrubbing.unscrubbable-vacuous`.
- **A human, every time.** The literal `acknowledged` is authored by the
  fragment author, but every acknowledgement draws a
  `scrubbing.unscrubbable` lint warning that requires human review before the
  fragment ships. The value cannot be set accidentally: any other value is a
  schema error at admission and a route-table build failure at runtime.

## Enforcement

### Admission time (schema + lint)

| Layer | Rule | Finding |
|---|---|---|
| `spec/route-fragment-schema.json` | `x-unscrubbable` (fragment-root or operation level) must be exactly `"acknowledged"`; any other value fails schema validation | `fragment.schema` (error) |
| `seam lint` (`internal/spec/lint.go` `checkUnscrubbable`) | Every acknowledgement, root or operation level, warns for human review | `scrubbing.unscrubbable` (warning) |
| `seam lint` (same check) | An acknowledgement on a fragment with no `x-vault-path`/`x-inject-as` pair warns as vacuous | `scrubbing.unscrubbable-vacuous` (warning) |
| Route-table build (`internal/server/route_table.go` `extractAcknowledgedExtension`) | Re-validates the literal; any value other than `acknowledged` fails the build of the route table | build error |

Placement semantics: a fragment-root acknowledgement covers every operation in
the fragment; an operation-level acknowledgement narrows the opt-in to that
operation. Because absence is refusal and there is no `false` form, a narrower
scope can add an acknowledgement but can never rescind a broader one.

### Run time (proxy)

1. **Protocol upgrades.** A credential-injecting request that asks for an
   upgrade (`Connection: Upgrade`) is refused — 502 `upstream_failed`
   envelope, the upstream never sees the injected credential — unless the
   route acknowledges (`internal/server/proxy.go`, `isProtocolUpgrade` gate).
2. **Unscannable responses.** An opaque response or one with an unsupported
   `Content-Encoding`:
   - **No acknowledgement:** refused. 502 `upstream_failed` envelope; the
     upstream body never reaches the caller.
   - **Acknowledged:** passed through unsanitized — body, headers, and
     trailers byte for byte.
3. **Scannable responses.** Always scrubbed — body, headers, and trailers,
   buffered or incrementally — acknowledged or not.

## Observability

| Moment | Emission |
|---|---|
| Config status (`scrubbing` section) | `unscrubbable_routes` (path, method, api_version, redacted upstream, reason `"x-unscrubbable: acknowledged"`), `unscrubbable_count`, `max_buffered_response_bytes`. No vault path, no credential policy. |
| Acknowledged unscannable pass-through (request time) | `[proxy] x-unscrubbable acknowledgement: serving unscannable response unsanitized for <METHOD> <route> (status N, content-type "…")` — one line per pass-through; the only durable record that an unsanitized response was delivered. |
| Refused unscannable response (request time) | `[proxy] refusing unscannable response for <METHOD> <route> (status N, content-type "…"): no x-unscrubbable acknowledgement` plus the `upstream_failed` request error. |
| Admission | `seam lint` warnings `scrubbing.unscrubbable` and `scrubbing.unscrubbable-vacuous`, per fragment file. |

Log lines deliberately carry the route path only, never the query string: a
`query`-kind `x-inject-as` places the credential in the upstream URL, so query
bytes must not reach logs.

## Test inventory

`internal/server/response_scrubber_test.go`:

- `TestAcknowledgedRouteStillScrubsScannableResponse` — acknowledged route,
  scannable response: body, headers, and trailers are still scrubbed and carry
  the redaction marker (the not-a-blanket-bypass pin).
- `TestAcknowledgedUnscannablePassThroughIsWholeResponse` — acknowledged
  route, opaque response: body, headers, and trailer delivered untouched, no
  marker anywhere (the whole-response pin).
- `TestAcknowledgedUnscannablePassThroughEmitsOperatorLog` — pass-through
  emits the operator log naming the unsanitized route and never the
  credential.
- `TestInjectedOpaqueResponseRefusesWithoutAcknowledgement`,
  `TestInjectedUnsupportedEncodingRefusesWithoutAcknowledgement`,
  `TestInjectedProtocolUpgradeRefusesWithoutAcknowledgement` — ordinary
  routes refuse every unscannable interaction.
- `TestInjectedResponseScrubsBodyHeadersAndTrailers`,
  `TestInjectedGzipResponseIsDecodedScrubbedAndReencoded`,
  `TestInjectedOversizedResponseUsesIncrementalScrubbing` — ordinary routes
  scrub every scannable shape.
- `TestRouteTableCarriesUnscrubbableAcknowledgement` — the route table
  carries the acknowledgement into `RouteEntry.Unscrubbable`.

`internal/spec/lint_test.go`:

- `TestLintDirectoryUnscrubbableAcknowledgementContract` — the human-review
  warning fires at both placement levels; the vacuous warning fires exactly
  when the fragment declares no credential injection.

## History

- 2026-09-26 (seam-c4625dec): contract specified; vacuous-acknowledgement lint
  added (`scrubbing.unscrubbable-vacuous`); request-time refusal and
  pass-through logs added; scannable-still-scrubbed and whole-response
  pass-through pinned by tests. Resolves the open decision in
  `route-fragment-schema-limitations.md` §2.4.
