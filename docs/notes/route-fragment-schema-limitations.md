# Route Fragment Schema — Limitations and Open Questions

**Status:** Design documentation for `route-fragment-schema.json` (v1)  
**Created:** 2026-08-07  
**Purpose:** Document limitations, unresolved questions, and extensibility considerations for the v1 route fragment schema.

## Overview

The SEAM route fragment schema (`x-seam-schema: v1`) is deliberately incomplete by construction. This document enumerates:

1. **What the schema cannot express** — inherent limitations of JSON Schema 2020-12
2. **Open questions** — design decisions requiring resolution
3. **Known edge cases** — ambiguous or boundary behaviors
4. **Extensibility considerations** — how the schema can evolve

**Key principle:** The schema validates *shape* and *intra-object* relations. Cross-field, cross-path, manifest, and merge-time constraints are enforced by the Go validator (`internal/spec`). See `route-fragment-schema.md` §2, §4.

---

## 1. Schema Limitations

### 1.1 Inherent JSON Schema Limitations

**Cross-path validation (within one fragment)**
- **What the schema cannot do:** Validate that `x-instance-param` appears in every path template
- **Why:** JSON Schema cannot read multiple sibling keys and compare their values
- **Current solution:** Validator-side rule (route-fragment-schema.md §4.1)
- **Example:** Fragment with `x-instance-param: "cluster"` and paths `/k8s/{cluster}/pods` and `/api/v1/config` — schema accepts both paths; validator rejects the second

**Cross-template validation**
- **What the schema cannot do:** Verify that every `{param}` in `x-upstream-path-template` exists in the matched path template
- **Why:** Requires comparing two different path strings
- **Current solution:** Validator-side rule
- **Example:** Path `/k8s/{cluster}/pods` with upstream template `/api/v1/{namespace}/pods` — schema accepts both; validator rejects the mismatch

**Manifest-level validation**
- **What the schema cannot do:** Check `x-upstream` URL against an allowlist stored outside the fragment
- **Why:** Allowlist is in ConfigMap `seam-upstream-allowlist`, not in the fragment
- **Current solution:** Validator compares against manifest at runtime
- **Example:** `x-upstream: "https://malicious.com"` — schema accepts valid URL shape; validator checks allowlist membership

**Merge-time collision detection**
- **What the schema cannot do:** Detect duplicate `(path, method, x-api-version)` across multiple fragments
- **Why:** Collisions are only visible after loading all fragments
- **Current solution:** Merge-time detection in loader
- **Example:** Two fragments both defining `GET /api/v1/users` with no `x-api-version` — both validate individually; collision detected at merge

### 1.2 Validator-Side Constraint Incompleteness

**Per-entry vault/inject pairing (currently incomplete)**
- **Current state:** Schema has `constraint-entry-vault-inject-paired` as an "inert placeholder"
- **Why:** `x-upstream-map` entry validation requires evaluating merged defaults (fragment-level vault + per-entry override)
- **Current solution:** Validator-side merge then check
- **Open question:** Should the schema encode the merged check, or leave it wholly to the validator?

**Unit equality across fields**
- **What the schema cannot do:** Enforce that `x-quota.unit` equals `x-cost-per-call.unit`
- **Why:** Cross-object value equality
- **Current solution:** Validator-side rule
- **Example:** `x-cost-per-call: {"value": 0.001, "unit": "usd"}` with `x-quota: {"limit": 1000, "unit": "credits"}` — schema accepts; validator rejects

### 1.3 OpenAPI 3.1 Specific Limitations

**WebSocket support**
- **Current state:** Schema does not explicitly validate `callbacks` or WebSocket-specific fields
- **Why:** OpenAPI 3.1 supports WebSockets via `callbacks`; SEAM's WebSocket story is not yet defined
- **Impact:** Fragments can declare callback structures; schema passes them through
- **Future:** Phase for WebSocket proxying will need callback-specific validation

**Parameter serialization styles**
- **Current state:** Schema does not validate parameter `style`, `explode`, or `allowReserved`
- **Why:** Delegated to libopenapi's OpenAPI 3.1 validator
- **Impact:** Invalid serialization styles pass schema validation but fail OpenAPI correctness checks
- **Risk:** Lint → runtime drift if libopenapi's rules evolve

---

## 2. Open Questions and Design Decisions

### 2.1 RESERVED Fields

**`x-requires-approval` (boolean, root or operation)**
- **Current state:** Schema accepts but does not enforce; marked as RESERVED
- **Question:** What approval mechanism? What UI? What audit trail?
- **Status:** "Accepted but unphased" — declared now so first fragments can carry it without schema change
- **Decision needed:** Phase for approval-gated routes and seam-approval integration

**Per-entry `requiredScope` on `x-upstream-map`**
- **Current state:** Schema validates shape but not semantics
- **Question:** How does fragment-root `x-required-scope` interact with per-entry `requiredScope`?
- **Options:**
  1. Union: Fragment scope OR entry scope (loose)
  2. Intersection: Fragment scope AND entry scope (strict)
  3. Replacement: Entry scope replaces fragment scope for that instance
- **Current behavior:** Not yet implemented
- **Decision needed:** Choose union/intersection/replacement before Phase 5 (kubectl-proxy fanout)

### 2.2 Schema Evolution

**What breaks when `x-seam-schema: v2` is introduced?**
- **Question:** How should v1 validator behave when it sees `x-seam-schema: "v2"`?
- **Current approach:** `const: "v1"` rejects v2 outright
- **Alternatives considered:**
  1. Strict rejection (current) — safe but requires coordinated rollout
  2. Permissive ignore — v1 validator ignores unknown `x-*` fields (risky)
  3. Best-effort validation — v1 validates what it recognizes
- **Decision needed:** Rollout strategy for v2 (canary? feature flag?)

**How to add new `x-*` fields without breaking v1 validators?**
- **Current approach:** `additionalProperties: true` at all levels
- **Risk:** Typo in field name silently passes validation
- **Alternatives:**
  1. Strict mode: Opt-in to unknown field rejection
  2. Known fields list: Schema enumerates all known `x-*` names
- **Decision needed:** Should SEAM adopt a strict validation mode for production?

### 2.3 Duration and Time Handling

**Duration grammar limitations**
- **Current state:** Schema validates `^\d+[smhd]$` (seconds, minutes, hours, days)
- **Missing:** Months (`1mo`), weeks (`1w`), calendar-aligned durations (`P1M` ISO 8601)
- **Why:** OpenAPI's `pattern` cannot reject `1mo` while accepting `1m` (ambiguous)
- **Impact:** Operators cannot specify "per-calendar-month" quotas
- **Decision needed:** Extend duration grammar? Support ISO 8601 durations?

**Brownout windows and timezone handling**
- **Current state:** `x-seam-deprecated.brownout` has no timezone specified
- **Question:** Are brownout times UTC? Fragment-local time? SEAM-local time?
- **Risk:** Ambiguous brownout windows cause confusion
- **Decision needed:** Specify timezone convention or require explicit UTC

### 2.4 Security and Authorization

**`x-unscrubbable` semantics**
- **Current state:** Boolean acknowledged opt-in for body-restructuring adapters
- **Question:** Does `x-unscrubbable` bypass ALL scrubbing, or just body validation?
- **Ambiguity:** Path parameter scrubbing? Query parameter scrubbing?
- **Decision needed:** Clarify scrubbing scope for unscrubbable routes

**Scope inheritance and merging**
- **Current state:** Operation-level `x-required-scope` *replaces* fragment-root, never merges
- **Question:** Should there be a merge mode for additive scopes?
- **Example:** Fragment requires `[base:read]`, operation wants to add `[admin:write]`
- **Current behavior:** Operation must repeat `[base:read, admin:write]` explicitly
- **Decision needed:** Add `scope-merge-mode` field or keep replacement-only?

---

## 3. Known Edge Cases and Ambiguities

### 3.1 Empty and Null Handling

**Empty `paths` object**
- **Current state:** Schema requires `minProperties: 1` on `paths`
- **Question:** Should an empty paths object be treated as a no-op fragment or rejected?
- **Current behavior:** Rejected at schema level
- **Edge case:** What if a service wants to ship a placeholder fragment?

**Empty `x-upstream-map`**
- **Current state:** Schema allows `x-upstream-map: {}` (empty map)
- **Question:** Is an empty map a valid fragment? What does it forward to?
- **Current behavior:** Valid per schema, but validator has no instances to route to
- **Decision needed:** Should schema require `minProperties: 1` on `x-upstream-map`?

**Null vs missing optional fields**
- **Current state:** Schema distinguishes between `null` and missing for some fields
- **Ambiguity:** Does `x-breaker: null` mean "use defaults" or "breaker disabled"?
- **Current behavior:** Missing = use defaults; `null` behavior undefined
- **Decision needed:** Specify null handling policy

### 3.2 Collision and Merge Behavior

**Deterministic collision loser selection**
- **Current state:** "Deterministic filename order" decides collision loser
- **Ambiguity:** What order? Lexicographic? ASCII sort? Locale-aware?
- **Risk:** Different filesystems sort differently (ext4 vs NFS)
- **Decision needed:** Specify sort algorithm explicitly or use timestamp

**Silent collision vs explicit conflict declaration**
- **Current state:** Later fragment "loses" and is not merged; logged but not surfaced to caller
- **Question:** Should SEAM surface collision errors at `/config/status`? (currently yes, but is it sufficient?)
- **Risk:** Operator doesn't notice a route disappeared due to collision
- **Current behavior:** Logged, surfaced at `/config/status`
- **Edge case:** What if ALL routes from a service are lost to collisions?

### 3.3 Encoding and Character Set

**Path parameter character encoding**
- **Current state:** Schema validates `^[a-zA-Z0-9_-]+$` for parameter names
- **Question:** How are non-ASCII characters in path values handled?
- **Example:** `/api/{region}` where `region=us-west-1` vs `region=eu-central-1`
- **Current behavior:** libopenapi handles URL encoding; schema doesn't restrict values
- **Edge case:** What if upstream expects unencoded UTF-8?

**YAML anchor and alias support**
- **Current state:** Fragments loaded as YAML; anchors/aliases may be present
- **Question:** Does SEAM resolve YAML anchors before validation?
- **Ambiguity:** Anchor expansion happens before schema validation (schema sees resolved JSON)
- **Risk:** Circular anchors cause infinite expansion
- **Decision needed:** Document YAML anchor behavior or ban anchors

### 3.4 Versioning Edge Cases

**`x-api-version` absence and `_unversioned`**
- **Current state:** Missing `x-api-version` → keyed as `_unversioned` in collision detection
- **Ambiguity:** Can a fragment author literally write `x-api-version: "_unversioned"`?
- **Current behavior:** Schema rejects `_unversioned` as literal value (must match `^v[1-9][0-9]*$`)
- **Edge case:** What if operator wants `_unversioned` to be a real version name?

**Version ordering and comparison**
- **Current state:** Collision key uses exact string match for `x-api-version`
- **Question:** How do versions compare? Is `v2` "newer" than `v10`? (lexicographic: yes; semantic: no)
- **Impact:** Adapter `targetVersion` references must match exactly
- **Decision needed:** Should versions be semver? Or leave as opaque strings?

---

## 4. Future Extensibility Considerations

### 4.1 Adding New SEAM Extensions

**Pattern for new `x-*` fields**
- **Current approach:** Any `x-*` field passes schema validation (due to `additionalProperties: true`)
- **Risk:** Typos silently pass (`x-upstream-url` vs `x-upstream`)
- **Future pattern:**
  1. All known fields enumerated in schema
  2. `additionalProperties: false` in strict mode
  3. Unknown fields require explicit `x-seam-allow-unknown: true`
- **Decision needed:** When to tighten validation?

**Breaking changes checklist**
A schema change is **breaking** if it causes valid v1 fragments to fail:
- Adding required fields to existing definitions
- Tightening patterns (e.g., `^v\d+$` → `^v[1-9]$`)
- Removing enum values
- Changing `additionalProperties: true` → `false`
- Adding new `allOf` constraints

A schema change is **non-breaking** if it only adds:
- New optional fields with defaults
- New definitions not referenced by required paths
- Loosening patterns (rare, but possible)

### 4.2 OpenAPI Version Evolution

**OpenAPI 3.2+ support**
- **Current state:** Schema targets OpenAPI 3.1
- **Question:** How will SEAM handle OpenAPI 3.2 when released?
- **Options:**
  1. Bump `x-seam-schema` to `v2` with 3.2 support
  2. Keep `v1` but allow 3.2 features
  3. Parallel schemas (`v1-3.1`, `v1-3.2`)
- **Impact:** Any OpenAPI version bump requires re-validation of libopenapi compatibility

**JSON Schema draft evolution**
- **Current state:** Uses JSON Schema 2020-12
- **Future:** 2024-12 or later drafts may add new applicators
- **Question:** Can SEAM adopt newer drafts without breaking v1 compatibility?
- **Risk:** New drafts may deprecate features v1 relies on

### 4.3 Extensibility Hooks

**Custom validation extensions**
- **Question:** Should fragments support user-defined validation hooks?
- **Example:** `x-seam-validate: "customScript.sh"` for site-specific rules
- **Risk:** Security and reproducibility concerns
- **Current state:** Not supported; all validation is schema + Go validator
- **Future:** Phase for pluggable validation (unlikely given security posture)

**Plugin extensibility**
- **Question:** Should SEAM support third-party middleware declarations?
- **Example:** `x-plugin-rate-limit: {strategy: "token-bucket"}`
- **Current state:** All rate limiting is built-in (`x-quota`, `x-loop-guard`)
- **Future:** Plugin system would require schema changes and security review

### 4.4 Observability and Debugging

**Validation error formatting**
- **Current state:** Validator returns structured errors with line/column
- **Question:** Should schema-level errors include field paths?
- **Example:** Error `#/x-upstream-map/us-east/url` vs generic "url format invalid"
- **Future:** Enhance error messages with JSON Pointer paths

**Validation debugging mode**
- **Question:** Should SEAM support a "verbose validation" mode?
- **Features:**
  - Show which schema constraints passed/failed
  - Explain why validator rejected (e.g., "x-instance-param not in path /api/v1/config")
  - Suggest fixes (e.g., "did you mean x-instance-param: 'region'?")
- **Current state:** No debug mode; errors are final
- **Future:** `seam lint --verbose` or `seam validate --explain`

---

## 5. Implementation Status

### 5.1 Fully Implemented

| Feature | Schema | Validator | Status |
|---------|--------|-----------|--------|
| Shape validation (all `x-*` fields) | ✅ | ✅ | Complete |
| Intra-object constraints (e.g., vault+inject paired) | ✅ | ✅ | Complete |
| Fragment-root defaults (e.g., `x-required-scope`) | ✅ | ✅ | Complete |
| OpenAPI 3.1 `paths`/`components` validation | ⏭️ | ✅ | libopenapi |

### 5.2 Partially Implemented

| Feature | Schema | Validator | Status |
|---------|--------|-----------|--------|
| Cross-path validation (e.g., `x-instance-param` in all paths) | ⚠️ (comment only) | ✅ | Validator-side only |
| Manifest allowlist (e.g., upstream host whitelist) | ⚠️ (IP literal reject) | ✅ | Validator-side only |
| Merge-time collision detection | ❌ (N/A) | ✅ | Validator-side only |
| Per-entry vault/inject pairing in maps | ⚠️ (inert placeholder) | ⏳ | Not yet enforced |

### 5.3 Not Yet Implemented

| Feature | Schema | Validator | Status |
|---------|--------|-----------|--------|
| `x-requires-approval` enforcement | ✅ (RESERVED) | ❌ | Accepted but unphased |
| WebSocket callback validation | ⏭️ | ❌ | OpenAPI 3.1 callbacks not validated |
| Calendar-aligned durations (months, weeks) | ❌ | ❌ | Not designed |
| Brownout timezone specification | ⏭️ | ❌ | Undefined |
| Scope merging modes (union/intersection/replacement) | ❌ | ❌ | Not designed |

---

## 6. Migration Paths

### 6.1 If Schema Becomes a Bottleneck

**Problem:** Schema needs to express a constraint that JSON Schema 2020-12 cannot encode.

**Options:**
1. **Move to validator-side:** Add Go rule in `internal/spec` (current pattern)
2. **Custom meta-schema:** Extend JSON Schema with SEAM-specific keywords (risky, breaks portability)
3. **Hybrid approach:** Schema validates shape, custom annotation encodes constraint (e.g., `$seam: "validator-rule-instance-param-all-paths"`)

**Recommendation:** Follow current pattern — validator-side for cross-field rules.

### 6.2 If OpenAPI 3.2 Breaks Compatibility

**Problem:** OpenAPI 3.2 removes or changes a feature SEAM relies on.

**Options:**
1. **Version lock:** Stay on OpenAPI 3.1 indefinitely
2. **Bump `x-seam-schema` to v2:** Require 3.2 for all new fragments
3. **Dual schema support:** v1 for 3.1, v2 for 3.2

**Recommendation:** Version lock is safest. OpenAPI evolution is slow; 3.1 is stable for years.

### 6.3 If Go Validator Is Replaced

**Problem:** ADR-001's Go choice is revisited; validator reimplemented in another language.

**Migration path:**
1. **Schema is portable:** JSON Schema 2020-12 is language-agnostic
2. **Reimplement §4 rules:** Port Go validator logic to new language
3. **Keep conformance corpus:** Test suite (harness.js + fixtures) validates compatibility

**Risk:** Low. Schema is designed for portability; Go is an implementation detail.

---

## 7. Decision Log

### 7.1 Decided (as of 2026-08-07)

| Decision | Date | Rationale |
|----------|------|-----------|
| `x-seam-schema: v1` is `const`, not pattern | 2026-07-22 | Rejects v2 fragments outright, no guessing |
| `additionalProperties: true` at all levels | 2026-07-22 | Pass through unknown `x-*` for forward-compat |
| `x-required-scope` is operation-level with root default | 2026-07-20 | Per-method granularity for visible-but-not-invocable 403 |
| `x-instance-param` required with `x-upstream-map` | 2026-07-22 | Map without instance param is undefined |
| `x-upstream` XOR `x-upstream-map` | 2026-07-22 | Mutually exclusive by schema constraint |
| Validator-side for cross-path rules | 2026-07-22 | JSON Schema cannot compare multiple path templates |
| 2020-12 meta-schema (not 2019-09 or next) | 2026-07-22 | Latest stable; Ajv 8.20.0 supports it |

### 7.2 Undecided (requires resolution)

| Question | Impact | Proposed Resolution |
|----------|--------|---------------------|
| How does fragment-root `x-required-scope` interact with per-entry `requiredScope` in maps? | High (Phase 5) | Intersection: Both scopes required |
| Should SEAM support strict validation mode (`additionalProperties: false`)? | Medium | Yes, as opt-in flag `x-seam-strict: true` |
| What approval mechanism for `x-requires-approval`? | Medium (approval-gated routes) | Phase for seam-approval integration |
| Should schema require `minProperties: 1` on `x-upstream-map`? | Low | Yes, empty map is useless |
| Timezone convention for `x-seam-deprecated.brownout`? | Medium | UTC only, no timezone field |
| How are scopes merged (union vs intersection vs replacement)? | Low | Keep replacement-only; no merge mode |

---

## 8. References

**Primary documentation:**
- `route-fragment-schema.json` — JSON Schema 2020-12 contract
- `route-fragment-schema.md` — Human authority on field placement and validator-side rules
- `route-fragment-schema-integration.md` — Runtime and CI integration guide

**Related schema documentation:**
- `route-fragment-schema-auth-validation.md` — Authentication and authorization validation patterns

**Architecture context:**
- `docs/plan/plan.md` — Full SEAM architecture and data model rationale
- `language-runtime-choice.md` (ADR-001) — Why Go, why pb33f/libopenapi-validator

**Code reference:**
- `internal/spec/loader.go` — Fragment loading and runtime validation
- `internal/spec/fragment.go` — Fragment validation (planned)

---

**Document version:** 1.0  
**Last updated:** 2026-08-07  
**Next review:** When `x-seam-schema: v2` is designed or Phase 5 (kubectl-proxies) begins
