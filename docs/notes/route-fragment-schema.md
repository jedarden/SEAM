# Route Fragment Schema — `x-seam-schema: v1`

**Status:** designed 2026-07-22 (bead `bf-2wt`). Concrete, validated JSON Schema for a SEAM route fragment. Unblocks Phase 1b (fragment merge / collision / quarantine) and Phase 3 (the first real fragment — the ArgoCD read-only proxy). Closes the "narrowed, not open" Route-fragment-schema Open Question in `docs/plan/plan.md`.

**Authority split (read this first):** this note and its sibling `route-fragment-schema.json` are one deliverable in two files. The `.json` is the *machine contract* — it is valid JSON Schema draft 2020-12 and is what `seam lint` (Phase 9a) and the gateway's runtime quarantine (Phase 1b) validate against. This note is the *human authority* on two things the schema deliberately cannot express on its own:

1. **Placement** — which `x-*` fields are fragment-root, operation-level, or path-item-level, and *why* each placement is fixed rather than left to whoever writes the JSON Schema.
2. **Validator-side cross-field constraints** — the rules that require reading more than one field, more than one path, the mounted manifest, or the merged route table, and which the Go validator (`internal/spec`) must enforce *on top of* the schema, exactly as it already enforces the OpenBao allowlist and collision key. Every such constraint is listed in [§4](#4-what-the-go-validator-must-enforce-on-top).

The schema and this note are kept in lockstep: a change to a field's shape is a change to the `.json`; a change to a field's *placement* or to a cross-field rule is a change to this note. If the two ever disagree, the **`.json` governs what the validator accepts/rejects** and the **note is stale and must be fixed** — but a note that encodes a constraint the schema does not is a bug in the schema, not the note.

**Where to look for field semantics:** every field's full rationale lives in `docs/plan/plan.md` Data Models (and, for the load-bearing few, its Open Questions entry). This note does **not** re-derive those semantics; it maps them onto a concrete schema and records the placement + cross-field decisions a schema author needs. Cite the plan for *why a field exists*; cite this note for *what shape and placement the schema gives it*.

**Language:** the validator that consumes this schema is Go (ADR-001). OpenAPI-native correctness of the `paths`/`components` subtree is owned by `pb33f/libopenapi` + `libopenapi-validator`, not by this schema; the SEAM `x-*` extension fields are shape-validated by a JSON Schema 2020-12 engine against `route-fragment-schema.json`. The two share one engine in `internal/spec` so `serve` and `lint` cannot drift (Components: Gateway source layout).

---

## 1. What a fragment *is*

A fragment is **an OpenAPI 3.1 `paths`/`components` fragment plus SEAM extension fields**, delivered as one key in the per-service ConfigMap `seam-routes-<svc>` and mounted at `/etc/gateway/routes.d/<svc>/<name>.yaml` (Architecture). It is **not** a standalone OpenAPI document — `openapi:`/`info:` are tolerated but informational; the merge layer synthesizes the served document and populates `servers:` with SEAM's own caller-facing base URL. OpenAPI's native `servers:` is **never** used to name an upstream target (Data Models, `x-upstream`): upstream hosts live only in SEAM-owned `x-*` fields, so `/openapi.json` never discloses them.

A fragment is owned by exactly one `<svc>` (its `x-seam-owner`), declared at fragment root and verified against the mounted parent directory. A service ships **N** fragment files into one `routes/<svc>/` directory — coexistence of versions is the case the `(path, method, x-api-version)` collision key exists for (Version Migration Strategy §2).

## 2. The schema↔validator boundary

The single most important property of this schema is that it is **deliberately incomplete by construction**, and that the incompleteness is *enumerated here* rather than discovered by an implementer. JSON Schema can validate the *shape* of one object and the *intra-object* relations between its sibling keys, but it cannot:

- read two different path templates and compare them (cross-path),
- look up a referenced key in the merged route table (cross-fragment),
- compare a field against a mounted file or the operator manifest (manifest-level), or
- enforce ordering/non-overlap over an array of date ranges (semantic, not structural).

Every constraint that needs one of those is a **validator-side** constraint, listed in [§4](#4-what-the-go-validator-must-enforce-on-top). The schema encodes *as much as it faithfully can* and then stops, and each spot where it stops is marked with a `$comment` naming the validator-side rule. The validator (`internal/spec`) runs the schema **and** the §4 rules on every fragment, twice — once at `seam lint` in CI and again at gateway merge time, never trusting lint to have run (the same belt-and-braces posture as the OpenBao allowlist).

This boundary is also why `additionalProperties: true` at every level: the schema validates the SEAM-owned `x-*` keys it knows about and **passes everything else through unchecked** — OpenAPI-native keys are validated for OpenAPI correctness by libopenapi, and unknown `x-*` keys are accepted so a v1 validator does not reject a fragment carrying a forward field. `x-seam-schema: v1` is the detectable escape hatch if the format itself evolves (a v2 fragment is *rejected*, not shape-guessed — the `const: "v1"`).

---

## 3. Field inventory and placement

### 3.1 Fragment-root fields

| Field | Shape (→ `$def`) | Required? | Notes |
|---|---|---|---|
| `x-seam-schema` | `"v1"` (`const`) | **yes** | Versions the *format SEAM parses*. `const` so a v2 fragment is rejected, not guessed. Root-only. |
| `x-seam-owner` | `ownerToken` | **yes** | Redundant in-file declaration of `<svc>`; checked against the mounted parent directory (validator-side). Root-only; whole-fragment property. |
| `paths` | object, `minProperties: 1` | **yes** | OpenAPI path templates keyed `^/`; each value is a SEAM `pathItem`. |
| `components` | object | no | Reusable OpenAPI components; OpenAPI-validity owned by libopenapi. |
| `x-api-version` | `apiVersion` (`^v[1-9][0-9]*$`) | no | Versions the *contract SEAM serves*. Absent → keyed `_unversioned` (SEAM-assigned; authoring it literally is rejected). Root-only. |
| `x-upstream` | `upstreamUrl` | conditional | Single upstream base URL. Mutually exclusive with `x-upstream-map`. Required for an ordinary single-instance non-adapter fragment (enforced by `constraint-forwarding-fragment-has-upstream`). |
| `x-upstream-map` | `upstreamMap` | conditional | Multi-instance map: instance name → entry object. Mutually exclusive with `x-upstream`; requires `x-instance-param`. |
| `x-instance-param` | `paramName` | conditional | The path parameter that selects a map entry (e.g. `cluster`). Required with `x-upstream-map`, forbidden without it. |
| `x-upstream-strip-prefix` | `upstreamStripPrefix` | no | Literal prefix removed from every matched template (step 3 shorthand). Must be a prefix of every path (validator-side). Upstream-facing → forbidden on adapter. |
| `x-upstream-tls` | `upstreamTls` | no | `{caBundle, serverName, insecureSkipVerify}`. Absent = verify against system trust store + hostname. Upstream-facing → forbidden on adapter (taken from target). |
| `x-upstream-plaintext` | `acknowledged` | conditional | Required when `x-upstream` is `http://`. Mutually exclusive with `x-upstream-map` (plaintext is per-entry there). |
| `x-vault-path` | `vaultPath` | paired | Which OpenBao secret to fetch. **Both-or-neither** with `x-inject-as`. |
| `x-inject-as` | `injectAs` | paired | How to inject: `{kind: header\|bearer\|query, name}`. **Both-or-neither** with `x-vault-path`. |
| `x-credential-probe` | `credentialProbe` | no | `{path, method, interval}` for the sentinel. Forbidden on a pass-through fragment (one declaring neither vault nor inject). |
| `x-breaker` | `breaker` | no | Per-upstream circuit-breaker tuning. On by default (infrastructure guard); `enabled:false` is the opt-out. Upstream-facing → forbidden on adapter. |
| `x-required-scope` | `scopeArray` | no | **Fragment-root default**; an operation-level value *replaces* it, never merges. Conjunctive; bare string = one-element array. |
| `x-fanout-scope` | `scopeArray` | no | Gates the `_all` fan-out, in addition to operation-level scope. Parsed from Phase 10; enforced from Phase 7. |
| `x-seam-deprecated` | `seamDeprecated` | no | `{since, sunset?, brownout?}` marking every route deprecated. Object, not boolean. |
| `x-adapter` | `adapter` | no | Declarative request/response transforms delegating to `targetVersion`. Mutually exclusive with every upstream-facing field. |
| `x-unscrubbable` | `acknowledged` | no | Root **or** operation level. Acknowledgement that a route runs unscrubbed. |
| `x-requires-approval` | boolean | no | **RESERVED** (approval-gated routes, accepted but unphased). Declared so the first fragment can carry it without a schema change; not yet enforced. |

### 3.2 Path-item-level fields

| Field | Shape | Notes |
|---|---|---|
| `x-upstream-path-template` | string `^/` | Authoritative upstream path in the *upstream's* terms (step 3, wins over strip-prefix). Begins `/`; no designated instance param; every `{param}` must exist in the matched template (validator-side, cross-field). Upstream-facing → forbidden on adapter. |

### 3.3 Operation-level fields

| Field | Shape (→ `$def`) | Notes |
|---|---|---|
| `x-required-scope` | `scopeArray` | **The authority** wherever both root and operation are present; *replaces* the root default. Per-method granularity is what makes the visible-but-not-invocable 403 expressible. |
| `x-loop-guard` | `loopGuard` | `{maxRepeats, window}`. Absent = no loop guard on that route. |
| `x-cost-per-call` | `costPerCall` | `{amount, unit}`. Required if `x-quota` is present. |
| `x-quota` | `quota` | `{amount, unit, window}`. `unit` must equal the same route's `x-cost-per-call.unit` (validator-side). Requires `x-cost-per-call` (schema-encoded). |
| `x-unscrubbable` | `acknowledged` | Operation-level opt-in. |
| `x-requires-approval` | boolean | RESERVED forward-compat. |

### 3.4 Placement decisions worth stating

Three fields have placement that is **fixed by a security rule and was not left to the schema author** (Open Questions, decided 2026-07-20):

- **`x-required-scope` is operation-level with a fragment-root default.** A root-only field *cannot* say "GET needs `k8s-ro:get`, DELETE needs more", which is exactly the case the enumeration-oracle 403 rule (Architecture, `/docs/{route}`) needs. The root form is ergonomics; the operation form *replaces* (never merges) so both "this one method needs more" and "needs less" are expressible.
- **`x-api-version`, `x-seam-owner`, `x-seam-schema` are fragment-root only.** Each is a whole-fragment property (version of the served contract; the owner; the parsed format). An operation-level occurrence of any is a lint error. This follows from fragment-scoped versioning, not a new decision.
- **`x-unscrubbable` is allowed at root *and* operation** — a route-level opt-in is needed, but a whole-fragment one is the common case.

The remaining fields' placement follows from their semantics (an upstream target, a vault path, a breaker, a map, a probe, a strip-prefix, an adapter, a fan-out scope, and a deprecation marker are all whole-fragment properties → root; per-route guards → operation; the upstream-path rewrite is stated in the upstream's path terms → path-item).

---

## 4. What the Go validator must enforce *on top*

The schema validates shape + intra-object relations. **Every constraint below needs more than that and is enforced by `internal/spec` (both at `seam lint` and at gateway merge), never by the schema alone.** Each is already named in the schema as a `$comment` at the field it binds, and is the peer of the OpenBao/vault-prefix allowlist and the collision key — belt and braces, enforced twice.

### 4.1 Cross-path / cross-template (within one fragment)

- **`x-instance-param` must name a parameter present in EVERY path the fragment declares**, and its segment is the one dropped from the computed upstream path. (Schema can check it is a bare name; it cannot enumerate the fragment's path templates.)
- **`x-upstream-strip-prefix` must be a prefix of EVERY declared path.** A partly-applicable strip silently forwards one fragment's routes to two upstream path shapes. (Schema checks it is a literal: leading `/`, no trailing `/`, no `{`.)
- **`x-upstream-path-template`**: every `{param}` it names must exist in the matched template, and it must NOT name the designated instance parameter (`x-instance-param`'s value). (Schema checks it begins `/`.)
- **`x-credential-probe.path` must be a route the fragment itself serves** (a path+method declared in `paths`).
- **Reserved control-plane namespace**: no path in `paths` may collide with SEAM's reserved set (`/docs`, `/docs/{route}`, `/openapi.json`, `/whoami`, `/scopes`, `/changes`, `/health/credentials`, `/health/upstreams`, `/config/status`, and the prefixes `/docs/`, `/health/`, `/config/`, `/approvals/`, `/_seam/`). A fragment declaring any is quarantined whole (Fragment Toolchain).

### 4.2 Cross-object value equality (within one operation)

- **`x-quota.unit` must be byte-identical to the same route's `x-cost-per-call.unit`.** A mixed pair draws a credit balance down in dollars. (The schema encodes *that quota requires cost* — `constraint-quota-requires-cost` — but not unit equality across two sibling keys.)

### 4.3 Effective per-instance resolution (on a map fragment)

- **The effective `(vaultPath, injectAs)` pair must resolve both-or-neither for EACH instance** — fragment-level defaults plus any entry override. The schema checks fragment-level both-or-neither (`constraint-vault-inject-paired`) and per-entry both-or-neither (`constraint-entry-vault-inject-paired`, currently an inert placeholder), but the *merged* pair is only well-defined after combining the two — a validator-side merge.
- **`x-upstream-map` entry `url` host ∈ the operator-owned allowlist**, and **`plaintext: acknowledged`** on every entry whose `url` is `http://` (schema checks the per-entry requirement; membership is manifest-level).

### 4.4 Manifest-level (the schema must *assume* these, never encode them)

- **Upstream-host allowlist membership** for `x-upstream` and every `x-upstream-map` entry `url`. The set lives in `seam-upstream-allowlist` (SEAM's own manifest), **never in a fragment** — a fragment-supplied allowlist is self-certifying. The schema rejects IP literals (they defeat enumeration) but cannot decide membership.
- **`x-vault-path` resolves inside the OpenBao prefix `seam/routes/<x-seam-owner>/*`** — the prefix shape is encoded (`^seam/routes/<owner>/`), and the owner segment must equal the fragment's `x-seam-owner` (co-ownership). Path traversal, globs, and templated segments are rejected at schema level.
- **`x-seam-owner` equals the mounted parent directory** (`/etc/gateway/routes.d/<svc>/`). A mismatch quarantines the whole fragment; the in-file field is never trusted on its own.
- **`x-upstream-tls.caBundle` names a key present and parseable** in `seam-upstream-ca` (a missing key quarantines; it never silently falls back to the system trust store).
- **`x-adapter.targetVersion` names a LIVE coexisting `x-api-version`** after merge; **`x-adapter`'s closed transform vocabulary** is validated (Phase 8 owns the vocabulary).

### 4.5 Merge-time (across the whole route table)

- **Collision key `(path, method, x-api-version)`** — two fragments on the same triple collide; the later-merged (deterministic filename order) loses, never the incumbent. A fragment with no `x-api-version` is keyed `_unversioned`; differing `x-api-version` is legal coexistence, not a collision.
- **Same-origin `x-breaker` disagreement** → strictest values govern (lowest threshold, longest open), disagreement named at `/config/status`.
- **Scrubbing invariant** — a route whose `x-adapter.response` alters body structure forces the buffered path (Architecture, pipeline); `seam lint` flags a body-restructuring adapter on an unbufferable route unless it carries `x-unscrubbable: acknowledged`.

### 4.6 Warnings, not failures (lint surfaces them; schema does not reject)

- **`window` > `168h`** on `x-quota`/`x-loop-guard` — a tumbling, restart-scoped window longer than the deploy cadence is a cap that quietly resets; visible, not forbidden.
- **Map width × plausible per-instance body size > effective `maxFanoutEnvelopeBytes`** — routine truncation risk; the truncation behavior is well-defined and safe, so it is flagged, not blocked.

---

## 5. What the schema encodes (its own `allOf`)

For completeness, these are the cross-field constraints the schema **does** express at the fragment root (its ten `allOf` entries) plus the one operation-level one. Each is exercised by the validation corpus (see [§7](#7-validation-and-conformance-corpus)):

| Constraint | What it catches |
|---|---|
| `constraint-adapter-excludes-upstream-fields` | an `x-adapter` fragment declaring any upstream-facing field |
| `constraint-upstream-xor-map` | `x-upstream-map` alongside `x-upstream` |
| `constraint-forwarding-fragment-has-upstream` | a non-adapter fragment with no upstream target at all |
| `constraint-map-requires-instance-param` | `x-upstream-map` without `x-instance-param` |
| `constraint-instance-param-only-with-map` | `x-instance-param` without `x-upstream-map` |
| `constraint-upstream-excludes-instance-param` | `x-instance-param` on a single-`x-upstream` route |
| `constraint-vault-inject-paired` | `x-vault-path` without `x-inject-as` or vice-versa |
| `constraint-passthrough-has-no-probe` | `x-credential-probe` on a pass-through (no vault/inject) fragment |
| `constraint-plaintext-required-on-http` | an `http://` `x-upstream` without `x-upstream-plaintext` |
| `constraint-plaintext-excludes-map` | `x-upstream-plaintext` alongside `x-upstream-map` |
| `constraint-quota-requires-cost` (operation) | `x-quota` without `x-cost-per-call` on the same route |

The `acknowledged` singleton (no `false` form) for `insecureSkipVerify` / `x-upstream-plaintext` / `x-unscrubbable`, the `apiVersion` grammar, the `duration` grammar, the IP-literal rejection on `upstreamUrl`, and the `injectAs` kind↔name conditionals are all encoded inline in their `$def`s.

---

## 6. Worked fragments

These are illustrative, not shipped — they render the schema's intent for the two flagship cases. The ArgoCD pilot (Phase 3) is the first real fragment this schema unblocks.

### 6.1 Single-instance, credential-injecting — the ArgoCD pilot (Phase 3)

The existing hand-rolled read-only proxy, expressed as a fragment. Injects its own read-only bearer token server-side; needs no caller credential; strips the `/argocd` service prefix.

```json
{
  "x-seam-schema": "v1",
  "x-seam-owner": "argocd",
  "x-upstream": "https://argocd-ro-ardenone-manager-ts.ardenone.com:8444",
  "x-upstream-tls": { "caBundle": "argocd-ro.pem" },
  "x-upstream-strip-prefix": "/argocd",
  "x-vault-path": "seam/routes/argocd/ro-token",
  "x-inject-as": { "kind": "bearer" },
  "x-credential-probe": { "path": "/argocd/api/v1/applications", "method": "GET", "interval": "6h" },
  "paths": {
    "/argocd/api/v1/applications": {
      "get": {
        "x-required-scope": ["argocd:read"],
        "summary": "List ArgoCD applications (read-only)"
      }
    }
  }
}
```

A `GET /argocd/api/v1/applications` forwards to `<upstream>/api/v1/applications` with `Authorization: Bearer <token>` (step 3: drop nothing — no instance param — strip `/argocd`, re-substitute nothing). The probe validates the token every 6h. `x-upstream-tls.caBundle` pins the self-signed proxy's CA (this is the fleet's `-k` replacement).

### 6.2 Multi-instance map — the fleet kubectl-proxies (Phase 5)

Nine near-identical cluster APIs collapsed into one fragment. Each entry holds its own credential and (for the read/write clusters) a tighter probe cadence; `_all` fans out.

```json
{
  "x-seam-schema": "v1",
  "x-seam-owner": "k8s",
  "x-instance-param": "cluster",
  "x-fanout-scope": ["k8s-ro:get"],
  "x-upstream-map": {
    "ardenone-cluster": {
      "url": "https://traefik-ardenone-cluster:8001",
      "vaultPath": "seam/routes/k8s/ardenone-cluster-ro",
      "injectAs": { "kind": "bearer" },
      "requiredScope": ["k8s-ro:get"],
      "probeInterval": "24h"
    },
    "apexalgo-iad": {
      "url": "http://kubectl-proxy-apexalgo-iad:8001",
      "plaintext": "acknowledged",
      "vaultPath": "seam/routes/k8s/apexalgo-iad-admin",
      "injectAs": { "kind": "bearer" },
      "requiredScope": ["k8s-rw:admin"],
      "probeInterval": "12h"
    }
  },
  "paths": {
    "/k8s/{cluster}/api/v1/pods": {
      "get": { "x-required-scope": ["k8s-ro:get"] },
      "delete": { "x-required-scope": ["k8s-rw:delete"] }
    }
  }
}
```

`GET /k8s/ardenone-cluster/api/v1/pods` → `<that entry's url>/api/v1/pods` (step 2 drops `{cluster}`, step 3 strips `/k8s`). `GET /k8s/_all/api/v1/pods` fans out across every instance the caller holds `requiredScope` for, dropping the rest as `scope-withheld` envelope entries. Note `plaintext` is per-entry (the `apexalgo-iad` `http://` hop), never fragment-wide.

### 6.3 Pass-through — legal, injects nothing

A route that forwards with TLS, guards, health and versioning but no secret. `x-credential-probe` would be a lint error here.

```json
{
  "x-seam-schema": "v1",
  "x-seam-owner": "echo",
  "x-upstream": "https://echo.example.svc.cluster.local",
  "paths": { "/echo": { "get": {} } }
}
```

### 6.4 Adapter — an older coexisting version

Delegates to a live `v1` route; declares none of the upstream-facing fields (they come from `targetVersion`). Carries only caller-facing fields and the transform sequence.

```json
{
  "x-seam-schema": "v1",
  "x-seam-owner": "argocd",
  "x-api-version": "v1",
  "x-seam-deprecated": { "since": "2026-07-22" },
  "x-adapter": {
    "targetVersion": "v2",
    "request": [],
    "response": []
  },
  "paths": { "/argocd/api/v1/applications": { "get": { "x-required-scope": ["argocd:read"] } } }
}
```

---

## 7. Validation and conformance corpus

The schema is verified by a fixture suite (draft 2020-12, exercised with Ajv 8.20 — the battle-tested 2020-12 engine named in ADR-001's research). **Executed 2026-07-22 against the committed `route-fragment-schema.json` with Ajv 8.20.0:** the schema meta-validates against the bundled 2020-12 meta-schema, compiles with no dangling `$ref`, accepts every canonical shape below, and rejects every violation below — **31/31 checks green** (meta-valid + compile + 8 accept + 21 reject). The runner and fixtures live in `~/scratch/seam-schema-verify/` (`harness.js` + `valid.js`/`invalid.js`) until Phase 9a provides a checked-in home; re-running is `node harness.js`. Three guarantees:

1. **It is a valid draft 2020-12 document** (meta-validated against the 2020-12 meta-schema) and **compiles cleanly** (no dangling `$ref`).
2. **It accepts** every canonical shape — single-instance inject, https/http pass-through, multi-instance map with per-instance vault/inject and plaintext, adapter fragment, operation-level cost+quota, operation-level scope replacing a root default, and the deprecated-with-brownout object.
3. **It rejects** every cross-field violation the schema is responsible for — unpaired vault/inject, map-without-instance-param (and the reverse), upstream+map together, adapter+upstream together, an upstream-less non-adapter fragment, `http://` without plaintext, quota without cost, bare `x-seam-deprecated: true`, a `v2` schema marker, missing `x-seam-owner`/`paths`, an IP-literal host, `injectAs` kind↔name violations, plaintext alongside a map, a probe on a pass-through fragment, an authored `_unversioned`, a calendar-aligned duration (`1mo`), and brownout without sunset.

The suite lives off-repo for now (experimental, `~/scratch`); the authoritative home for a checked-in conformance corpus is Phase 9a's `seam lint`/`seam diff` test tree, which should pin these fixtures as golden valid/invalid cases so the schema and the Go validator stay in lockstep.

---

## 8. Relationship to ADR-001 and the Go validator

ADR-001 ratifies Go and names the validator as the product: SEAM's differentiator is schema-validated, structured, field-level 400s, served by `pb33f/libopenapi-validator`. This schema is the contract that validator holds the SEAM `x-*` fields to:

- **OpenAPI-native correctness** (`paths`, `components`, parameter/response schemas, the standard `deprecated` boolean) → `libopenapi` / `libopenapi-validator`. The schema does not re-validate these; `additionalProperties: true` lets them pass through.
- **SEAM `x-*` shape and intra-object relations** → JSON Schema 2020-12 against `route-fragment-schema.json` (this file), run in `internal/spec`.
- **Cross-field / cross-path / manifest / merge rules ([§4](#4-what-the-go-validator-must-enforce-on-top))** → Go code in `internal/spec`, the same engine `serve` and `lint` share so the two cannot drift.

If Go is ever replaced (ADR-001 Consequences: bounded rewrite), this schema is portable — it is plain JSON Schema 2020-12 with no Go-specific content, and the §4 rules are language-neutral. The `x-seam-schema: v1` marker is the versioned escape hatch: when the format itself must evolve, bump it and a v1 validator rejects the v2 fragment outright rather than guessing from shape.
