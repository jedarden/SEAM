# The differential replay comparison contract

This document is the canonical statement of what the SEAM differential
harness (`tools/diffharness`, a standalone Go module) considers
response-equivalent, what it canonicalizes before comparing, how failures
and skips are decided, which exit codes each tool promises, and how the
cutover workflow consumes the result. `internal/compare/compare.go`
implements it; the tests named in the rule index pin it. The plan's
owning section is [Testing Strategy → Conformance / differential
harness](../plan/plan.md); the operator-facing tool manual is the
[diffharness README](../../tools/diffharness/README.md); the runbook that
consumes the gate is [migration-runbook.md §Stage 2](../migration-runbook.md).

Every rule below carries an ID. A change to any rule is a change to the
cutover safety argument and must land as a doc + implementation + test
change together — the rule index at the bottom of this document maps each
ID to the test that pins it, and `TestContractDocPinsRuleIDs` fails if a
pinned rule disappears from this document.

## 0. Model

A corpus entry carries one captured request and the credentials SEAM would
inject for it. `seam-replay` sends the request to **both** the incumbent
proxy and SEAM, then asks `internal/compare` whether the two responses are
equivalent **modulo the enumerated expected diffs**. The verdicts:

- **PASS** — equivalent modulo expected diffs, and no secret leaked.
- **FAIL** — not equivalent, or a secret leaked into the SEAM response.
- **SKIP** — the entry could not be exercised (explicit `Expect.skip`,
  an unresolved secret ref, or the incumbent failed to answer). A skip is
  counted and printed, never silently dropped, and never contributes to a
  failure — but it also proves nothing.

The comparator itself is policy-free: it never sends traffic, applies no
default ignore sets, and returns no SKIP. Defaults (volatile-header
ignores, skip classification) live in the replay layer, so `compare`
stays a pure function of two responses + secrets + options.

## 1. Canonicalization (before anything is compared)

- **C1 — Header keys.** Every header/trailer key is
  `textproto.CanonicalMIMEHeaderKey`-canonicalized **before** comparison,
  at the boundary that produces the value: the corpus loader
  (`corpus.Load` / `AppendEntry`, request headers; empty values dropped)
  for the recorded side, and the replay transport (`replayOne`) for both
  live responses. `compare` does **not** canonicalize — two
  differently-cased spellings of the same header are different headers to
  it, so a caller that skips canonicalization gets a false diff, by
  design. Canonicalization is a load/transport invariant, not a
  comparison fallback.
- **C2 — HTTP method.** Upper-cased at load; an absent method defaults to
  `GET`.
- **C3 — Repeated values.** A header or trailer sent multiple times is
  compared as an **order-insensitive multiset**. HTTP repeated-field
  semantics are order-independent, and a proxy re-merge may reorder.
- **C4 — Bodies are byte-exact.** The only transformation ever applied to
  a body is secret redaction (C5). There is **no** JSON/YAML semantic
  normalization: two JSON documents that differ in key order, whitespace,
  or number formatting are a FAIL even when they parse to the same value.
  This is deliberate — agents depend on the incumbent's *exact observed
  bytes*, so "equivalent JSON" is not the bar, and a silent semantic
  normalizer would widen the cutover safety claim beyond what is proved.
  A genuinely nondeterministic body takes `Expect.ignoreBody` (N2), which
  says so loudly in the report.
- **C5 — Secret redaction.** On **both** sides, in bodies, header values,
  and trailer values, every bare secret value is replaced with
  `[REDACTED-BY-SEAM]`. Secrets are applied **longest-bare-first**, so a
  secret that is a substring of another cannot be partially redacted
  first. An empty bare value is never redacted with (it would match
  everything) and never scanned for.

## 2. Comparison rules per dimension

- **D1 — Status.** Default: the plain differential —
  `incumbent.status == seam.status` and neither is unset(0). A pinned
  `Expect.status` (corpus `expect.status`) instead requires
  `seam.status == pin`; the incumbent's status is reported for context
  only and never gates. The pin exists for routes where SEAM legitimately
  transforms the status (an `_all` fan-out that is 207 on SEAM and 200 on
  the incumbent): the incumbent cannot be the oracle for a status it does
  not itself produce.
- **D2 — Headers.** Compare the two canonicalized header maps as multisets
  after (a) dropping the **ignore set** (N1), (b) dropping the
  **SEAM-added set**: every `X-Seam-*` header plus `Deprecation`,
  `Sunset`, `Link`, `Retry-After` — SEAM is expected to add these and the
  incumbent is not required to have them, in either direction — and (c)
  redacting per C5. What remains must match; a mismatch is reported as
  `dropped` (incumbent only), `added` (SEAM only), or `changed`.
- **D3 — Trailers.** Identical rules to D2.
- **D4 — Body.** Byte-equal after C5 redaction. `Expect.ignoreBody`
  suppresses the structural diff (N2) but **never** the leak scan (S3).

## 3. Nondeterministic fields

- **N1 — Volatile headers.** The replay layer drops a default ignore set —
  `Date`, `Server`, `X-Request-Id`, `Set-Cookie`, `ETag` — plus any
  per-entry `Expect.ignoreHeaders`, from both sides. Use this for headers
  that differ call-to-call against the *same* backend.
- **N2 — Nondeterministic bodies.** `Expect.ignoreBody` skips the
  structural body comparison for payloads embedding timestamps or request
  IDs. Use sparingly: a body the differential cannot pin is a body the
  cutover cannot prove equivalent. The report sets `bodyIgnored` on such
  entries so the gap is visible in the evidence attached to a cutover PR.
  The leak scan still runs (S3).
- **N3 — Skips.** `Expect.skip` (route not yet onboarded), an unresolved
  secret ref, and an incumbent-side transport failure all SKIP with a
  reason. A SEAM-side transport failure is **never** a skip (F3). A corpus
  whose entries are all skipped proves nothing and is refused (X1).

## 4. Secret references

- **S1 — Syntax and base.** A corpus carries secret *references*, never
  values. The full reference syntax — scheme handling, traversal/glob
  rejection, and containment under the enforced vault base
  `rs-manager/rs-manager/seam/routes` — is defined in
  [credential-reference-syntax.md](../notes/credential-reference-syntax.md)
  and enforced at load/capture time; the harness and the gateway pin the
  shared base to a common golden file, so the two cannot drift.
- **S2 — Resolution.** At replay time a ref resolves from the
  `--secrets` JSON file first, then from the environment variable derived
  from the whole ref (`vault:seam/routes/argocd/ro-token` →
  `SEAM_DIFF_SECRET_VAULT_SEAM_ROUTES_ARGOCD_RO_TOKEN`: upper-cased,
  every run of non-`[A-Z0-9_]` collapsed to one `_`). A ref resolving
  nowhere SKIPs its entry — an unresolved credential is a coverage gap,
  not a conformance failure, and must never be fabricated.
- **S3 — The leak check.** Independently of every rule above, each bare
  secret value is scanned for in the **SEAM response only** — body first,
  then headers, then trailers. The check runs **first** and **locks** the
  verdict: no expected-diff allowance, ignore set, status pin, or body
  ignore can mask a leak. `IgnoreBody` therefore never weakens it.
- **S4 — Echo semantics.** A redacted credential-echo (SEAM scrubs an
  upstream error that quoted the credential to
  `[REDACTED-BY-SEAM]`) is a **PASS** — the incumbent side is redacted
  with the same token before comparison, so the two match. An entry
  returning byte-identically *including* an echoed secret is a **hard
  FAIL** (`secretLeaked`, with the ref and where) — that is the one
  outcome the harness exists to catch.

## 5. Failures

- **F1 — Verdict model.** The three verdicts of §0 are exhaustive.
  `compare` returns only PASS/FAIL; the replay layer adds SKIP.
- **F2 — Precedence.** The leak check (S3) evaluates before every
  structural dimension and its verdict cannot be un-masked; structural
  diffs are still collected and reported on a leaking entry so the
  operator sees everything at once.
- **F3 — Transport asymmetry.** If the **incumbent** fails to answer, the
  entry SKIPs — the differential needs both sides, and an incumbent
  outage must not read as a SEAM regression. If **SEAM** fails to answer,
  the entry FAILs — the cutover target must answer for every replayable
  route.
- **F4 — Reporting.** Every FAIL carries machine-readable reasons (per-
  dimension diff structs) and a human-readable summary; the JSON report
  is the attachable evidence (README §Report Format). A leak is
  identified by ref and location (`body`, `header:Name`, `trailer:Name`).

## 6. Exit codes

- **X1 — `seam-replay`.** `0` = no FAIL (passes and skips both fine);
  `1` = at least one FAIL **or** a harness failure (unreadable/malformed
  corpus, unreadable secrets file, a corpus with no replayable entries —
  the nothing-to-prove guard, an unwritable report); `2` = usage error
  (missing required flags). Skips alone never fail a run, but the
  all-skipped corpus refuses to exit 0 because a gate that proved nothing
  must not read as green.
- **X2 — `seam-cutover check`.** `0` = no mechanical FAIL (MANUAL items
  still require operator attestation in the cutover PR); `1` = at least
  one mechanical FAIL (NO-GO — blocks that one service only); `2` = usage
  error. Unarmed checks (`--operator`, `--replay-bin`, `--agent-doc` not
  given) report **SKIP, never a silent pass**.

## 7. How the cutover workflow consumes the replay result

- **G1 — Consumption contract.** `seam-cutover check` runs `seam-replay`
  as a **subprocess** with `--corpus`, `--incumbent`, `--seam`,
  `--report`, and (when given) `--secrets`, and gates **on its exit code
  alone** — it does not re-parse the report. The replay report is written
  beside the check report as `<report>-replay.json` (a `--report` of
  `…/cutover-check.json` yields `…/cutover-check-replay.json`), so a
  cutover PR attaches both. A subprocess failure carries the last 4 KB of
  replay output in the check detail. The subprocess is bounded
  (15 minutes). This is runbook Stage 2 gate item 1, the hard item: the
  corpus must be green **on the exact build that will serve traffic**.
- **G2 — CI gate (repository-side).** The diffharness is a standalone
  nested Go module, so the root-module sweeps — `go vet ./...`,
  golangci-lint, `go test -race ./...` — never descend into it. The
  module's gate is the named `diffharness module gate` check in
  `scripts/definition-of-done.sh --slow`, which builds and tests the
  whole module (contract implementation included) from the module
  directory. `internal/corpusboundary`'s tripwire pins the wiring: if the
  lane is renamed, narrowed, or dropped, the root sweep fails. The
  corpus-vs-fixture boundary (`corpus/` runtime captures never tracked;
  `tools/diffharness/testdata/` fixtures always validated) is pinned by
  the same package.
- **G3 — CI gate (workflow-side).** The `seam-ci` WorkflowTemplate lanes
  run root-module commands only; the workflow-side equivalent of G2 is a
  lane that runs the same module gate in iad-ci. The template is
  GitOps-managed in `jedarden/declarative-config`
  (`k8s/iad-ci/argo-workflows/seam-ci-workflowtemplate.yml`) — a separate
  repository, so that lane addition is a deliberate cross-repo follow-up,
  not something this document can wire by existing. Until it lands, the
  module gate is enforced wherever the Definition of Done runs (pre-commit,
  NEEDLE close gate, and any CI step invoking the script).

## Rule index — where each rule is pinned

| Rule | Pinned by |
|---|---|
| C1 (loader half) | `TestLoadCanonicalizesHeaders`, corpus package |
| C1 (transport half) | `TestReplayOneCanonicalizesResponseHeaders` |
| C1 (compare assumes canonical) | `TestCompareDoesNotCanonicalizeKeys` |
| C2 | `TestLoadCanonicalizesMethod`, `TestLoadDefaultsEmptyMethod` |
| C3 | `TestRepeatedHeadersOrderInsensitive` |
| C4 | `TestJSONBodyIsByteExactNotSemanticallyNormalized` |
| C5 | `TestSubstringSecretRedactsLongestFirst`, `TestEmptySecretIsIgnored`, `TestBearerEchoRedactsOnlySecret` |
| D1 | `TestStatusDiffIsFail`, `TestExpectedStatusPinsSeamSide`, `TestExpectedStatusMismatchFails` |
| D2 | `TestSeamDropsHeaderIsFail`, `TestSeamAddsUnexpectedHeaderIsFail`, `TestIgnoreHeaders`, `TestSeamAddsXSEAMHeadersIsExpected`, `TestDeprecationHeadersAreExpectedSeamAdditions` |
| D3 | `TestLeakInTrailer` (+ header diff kinds apply unchanged) |
| D4 | `TestBodyDiffNonSecretIsFail`, `TestIgnoreBodySuppressesStructuralDiff` |
| N1 | `TestIgnoreHeaders` (default set asserted live by the lifecycle test) |
| N2 | `TestIgnoreBodySuppressesStructuralDiff`, `TestIgnoreBodyDoesNotWeakenLeakCheck` |
| N3 | `TestRunMainAllSkippedCorpusExits1`, lifecycle replay PASS run |
| S1 | `TestLoadValidatesSecretRefsAgainstEnforcedBase`, `TestCheckedInFixturesResolveUnderEnforcedVaultBase`, the vault-base golden tests (both suites) |
| S2 | `TestResolveFileLegOverEnvLeg`, `TestResolveEnvLegFallback`, `TestEnvNameMapping`, `TestResolveUnresolvedRefIsNotAnError` |
| S3 | `TestEchoedSecretInSeamResponseIsLeakFailure`, `TestLeakInHeader`, `TestLeakInTrailer`, `TestIgnoreBodyDoesNotWeakenLeakCheck` |
| S4 | `TestRedactedCredentialEchoIsPass`, `TestEchoedSecretInSeamResponseIsLeakFailure` |
| F1 | the whole `compare` package suite |
| F2 | `TestEchoedSecretInSeamResponseIsLeakFailure` (verdict locked, diffs still collected) |
| F3 | `TestIncumbentFailureSkipsSeamFailureFails` |
| F4 | `TestCorpusLifecycleWithRealisticWorkload` (report persistence + summary assertions) |
| X1 | `TestRunMainUsageErrorsExit2`, `TestRunMainAllPassExits0`, `TestRunMainReplayFailureExits1`, `TestRunMainMissingCorpusExits1`, `TestRunMainAllSkippedCorpusExits1` |
| X2 | `cli_test.go` (`TestRunMainUsageErrorsExit2`), `TestCheckReplayGatesOnExitCode` |
| G1 | `TestCheckReplayGatesOnExitCode`, `TestDeriveReplayReport` |
| G2 | `TestFixtureValidationStaysWired` |
| G3 | this document (no automated pin — the template lives in another repo) |
