# Capture and Corpus Testing

This document records the integrity checks for captured corpus data. The
repository keeps corpus data in two places:

- `tools/diffharness/testdata/*.json` holds the checked-in
  differential-harness fixtures: `corpus-argocd.json` (the deployed
  `argocd-ro` capture) and `example-corpus.json`. These persist request data
  and replay expectations; response values are collected from the incumbent
  and SEAM at replay time — a runtime capture's per-entry `response` is
  dropped at promotion, never committed. The fixture format, including the
  runtime-only `response`, optional informational `timestamp`, the
  `secrets[].ref` grammar, and the three redaction points, is specified in
  [`docs/design/argocd-ro-corpus-data-structure.md`](design/argocd-ro-corpus-data-structure.md).
  The aligned four-form field contract is
  [`docs/design/aligned-capture-replay-schema-contract.md`](design/aligned-capture-replay-schema-contract.md).
- `internal/server` capture files persist complete request/response pairs for
  middleware-level capture tests. Request and response bodies are encoded as
  standard base64 strings.

Captured corpora under a repository-root `corpus/` directory are no longer
tracked: the 2026-09-18 history purge (seam-70ae655e, commit 9984a5b) removed
every checked-in corpus and `.gitignore`d the path, so live captures stay out
of git. The split is enforced, not conventional:
`internal/corpusboundary` checks that the anchored `/corpus/` ignore entry
stays in place (a bare `corpus/` pattern would also swallow the nested
`tools/diffharness/internal/corpus/` package, silently dropping it from
commits), that nothing under `corpus/` is ever tracked in a real checkout,
and that the checked-in fixture and package paths stay tracked and outside
every ignore rule. Deliberately promoting a capture means moving it into
`tools/diffharness/testdata/` — where the fixture validation below applies —
never committing it under `corpus/`; the step-by-step procedure is the
promotion runbook below.

## Request-side redaction procedure

This is the ordered, fail-closed handoff from a runtime capture to a reviewed
fixture. The runtime file is private capture data, not a review artifact. The
four artifact forms and their allowed fields are defined by the [aligned
capture and replay schema contract](design/aligned-capture-replay-schema-contract.md);
the procedure below applies that contract to request-side redaction.

### 1. Capture into a private runtime artifact

Write the producer output only below the gitignored `corpus/` tree (or another
equally private temporary location). Do not print the capture, put it in a
ticket, or copy it into `tools/diffharness/testdata/`. Capture-time responses
are also private: either producer may record response material that has not
been reviewed for secrets.

The request-side boundary is exact:

| Capture path | Request headers redacted before retention | Query values redacted before retention | Request body boundary |
| --- | --- | --- | --- |
| **Gateway middleware** (`internal/server`) | Values for `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, and `X-Api-Key`, case-insensitively; plus every header name declared injectable by the matched route fragment, also case-insensitively. | Values for `api_key`, `api-key`, `apikey`, `access_token`, `access-token`, `auth_token`, and `auth-token`, case-insensitively; plus every query name declared injectable by the matched route, using the configured name. | No body inspection or redaction. The captured `bodyB64` is the request body as read by the middleware (for a positive `Content-Length`), encoded but not sanitized. |
| **Standalone `seam-capture`** (`tools/diffharness/cmd/seam-capture`) | Values for the fixed names `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `X-Api-Key`, `Api-Key`, and `X-Auth-Token`, case-insensitively. It has no route-fragment metadata. | None. Every query value is recorded as received. | No body inspection or redaction. The captured `bodyB64` is read and encoded verbatim, including chunked request bodies. |

Each value redacted by a producer becomes `[REDACTED-BY-SEAM]`. A route-
specific header or query value that the gateway does not recognize is not made
safe by being captured; similarly, an unrecognized standalone header, query
value, or body can remain literal. That difference is why the next review
step is mandatory for both paths. Neither producer's request boundary makes
the capture-time `response` safe, and the response is not part of a fixture.

### 2. Build a private sanitized candidate

Copy the runtime capture into a private temporary directory and create a
candidate there. Never edit the runtime capture in place and never use the
fixture directory as scratch space. For each entry, in order:

1. Remove the capture-time `response` entirely.
2. Review every request header and query value. Preserve the capture marker
   where the producer already redacted a credential location; replace any
   literal credential, token, cookie, authorization material, or other
   secret-like value with the marker or remove the field. A value is not safe
   merely because it is in an unfamiliar header or URL-encoded.
3. Decode every non-empty `request.bodyB64` and inspect the decoded bytes.
   Redact or remove secret-like content, then re-encode only the reviewed
   body. If it cannot be made safe without changing the request's intended
   semantics, discard the candidate instead of carrying the body forward.
4. Rewrite producer metadata to fixture conventions, add only reviewed
   `secrets[].ref` references and `expect` policy, and ensure no resolved
   credential value or in-memory `Secret.Bare` value is serialized.

The candidate is a private sanitized candidate, not a new persistence class:
it uses the `corpus.Corpus` shape and must contain request data only after the
checks above. A `vault:` reference identifies where replay resolves a secret;
it never carries the resolved value. The schema contract's [private
sanitized-candidate definition](design/aligned-capture-replay-schema-contract.md#2-private-sanitized-candidate)
is the field-level authority.

### 3. Apply the fail-closed gate

The gate is testable and has no permissive fallback:

> If any value that will be persisted in, or shown for review as, the
> candidate contains a credential, token, cookie, authorization material, or
> other secret-like value that is not the redaction marker or a `vault:`
> reference in `secrets[].ref`, stop. Do not persist, circulate, stage, or
> review that candidate; discard it and restart from the private runtime
> capture. An undecodable body, unknown sensitive value, or candidate with a
> capture-time `response` fails the same gate.

This rule deliberately permits the private raw runtime capture to exist as
capture input, but permits no unsanitized value to cross from that private
area into the candidate, reviewer channel, or checked-in fixture. The
promotion lifecycle test (`TestPromotionLifecycleConvertsCaptureToFixture`)
pins both producer shapes: standalone verbatim request material must be
scrubbed during promotion, capture markers must survive, responses must be
dropped, and the resulting candidate must load under the fixture contract.

### 4. Human reviewer checkpoint — before fixture creation

After the candidate passes the mechanical checks, a human reviewer inspects
the candidate in its private temporary location. The reviewer must confirm
that request headers, query values, and decoded bodies contain no literal
credential, token, cookie, authorization material, or other secret-like
value; that only references appear in `secrets[]`; that no `response` remains;
and that descriptions and metadata contain no sensitive content. The reviewer
either rejects and discards the candidate or explicitly approves it for
fixture creation. There is no path from runtime capture directly to a fixture.

### 5. Create and validate the fixture

Only after reviewer approval, move the candidate to
`tools/diffharness/testdata/`, run `cd tools/diffharness && go test ./...`, and
include it in the checked-in fixture walk. `corpus.Load` and
`TestCheckedInFixturesResolveUnderEnforcedVaultBase` enforce the schema,
canonicalization, unique IDs, safe in-base `vault:` references, and absence
of capture-time responses. The content-level secret review remains required:
the loader cannot infer that an arbitrary body or query value is secret-like.

## Schema forms and lifecycle

The two persisted forms use the same `seam-diff-corpus/v1` envelope but own
different fields:

| Form | Owns | Does not own |
| --- | --- | --- |
| **Runtime capture** under the untracked `corpus/` directory | The request sent to the incumbent and the incumbent response observed for that request; an entry `timestamp` may record when the exchange was captured | Reviewed `secrets` and `expect` configuration; both producers leave them empty or absent, and the captured response is not a replay oracle |
| **Checked-in fixture** under tracked `tools/diffharness/testdata/` | Retained request data plus reviewed `secrets` references and `expect` replay expectations; the informational `timestamp` may be retained or omitted | The capture-time `response`; replay collects fresh responses from the incumbent and SEAM |

The lifecycle is therefore:

1. **Capture:** a producer records each request together with the incumbent's
   response as one runtime request/response pair and persists it under
   `corpus/`.
2. **Promote:** review the request, rewrite producer metadata, add secret
   references and replay expectations, drop the capture-time `response`, and
   optionally omit the informational `timestamp` before moving the result to
   `tools/diffharness/testdata/`.
3. **Replay:** load the checked-in fixture, inject referenced secrets, send the
   retained request to both targets, collect fresh responses, and compare them
   using the fixture's `expect` values.

The design document's [two persisted schema forms](design/argocd-ro-corpus-data-structure.md#the-two-persisted-schema-forms)
defines the field-level contract; the promotion runbook below defines the
operational rewrite.

## Automated checks

### Corpus fixture integrity

The checked-in corpus fixtures are validated by the diffharness module's own
tests. The module is deliberately standalone — standard library only, it
cannot import the gateway's `internal/spec` package — so its checks run from
the module directory:

```sh
cd tools/diffharness && go test ./...
```

Loading a corpus (`corpus.Load`) and appending an entry
(`Corpus.AppendEntry`) both validate:

- the file parses as JSON and declares the exact schema version the harness
  speaks;
- a non-empty `service` token;
- every entry has a non-empty `id`, and entry IDs are unique;
- header keys and HTTP methods are canonicalized (a missing method defaults
  to `GET`); and
- **every `secrets[].ref` resolves under the enforced vault base.** A ref
  must carry the `vault:` scheme and name a path free of traversal (`..`,
  backslashes), glob characters (`*`, `?`, `[`), and templated segments
  (`{}`); whatever remains must land strictly inside the enforced base
  `rs-manager/rs-manager/seam/routes`. Containment is boundary-correct: a
  sibling that merely shares a string prefix with the base, and a bare base
  naming the parent itself, are both refused. `Load` enforces this at fixture
  time and `AppendEntry` at capture time, so an off-base ref is rejected
  while the corpus is still a fixture instead of failing secret resolution at
  replay time. The retired cluster-agnostic base `seam/routes`
  (consolidated 2026-09-04) fails both checks; the negative case is pinned by
  `TestLoadValidatesSecretRefsAgainstEnforcedBase` ("retired
  pre-consolidation base rejected") and `TestAppendEntryValidatesSecretRefs`.

The enforced base mirrors `internal/spec.ResolveVaultBaseDir`:
`DefaultVaultBaseDir` as above, overridden by `SEAM_VAULT_BASE_DIR` when that
variable is non-blank after trimming (`TestVaultBaseDirOverrideHonored`).
The mirror lives in `tools/diffharness/internal/corpus/corpus.go`. The
harness module is deliberately standalone (stdlib only, no SEAM gateway
imports), so it cannot import `internal/spec` to stay honest; the shared
base is instead pinned to a one-line golden file,
`internal/spec/testdata/enforced-vault-base.txt`, that both suites read —
`TestDefaultVaultBaseDirMatchesGolden` asserts SEAM's `DefaultVaultBaseDir`
and `ResolveVaultBaseDir` against it, and `TestVaultBaseDirMatchesGolden`
(the corpus package) asserts this module's mirror against the same file. The
agreement is enforced, not manual: a one-sided move fails the suite that did
not move, so the drift surfaces at fixture time instead of as replay-time
secret-resolution failures (the base already moved once, on the 2026-09-04
consolidation). When the base moves, move all three — the SEAM constant, the
mirror, and the golden — in one change. The checked-in fixtures themselves
are walked by `TestCheckedInFixturesResolveUnderEnforcedVaultBase`, which
also pins `argocd-ro` as the canonical service token and the fixture
convention that no entry carries a capture-time `response` (promotion drops
it; replay collects fresh responses from both targets). The documented
capture → fixture conversion itself is pinned by
`TestPromotionLifecycleConvertsCaptureToFixture` in the same package.

### Capture round-trip and response-pair checks

Run the request/response round-trip check, the existing response-pair
regressions, and the restart-readability test with:

```sh
go test ./internal/server -run '^(TestCaptureCorpusDataIntegrity|TestProxyCaptureEnabledPreservesSuccessfulResponsePair|TestProxyCaptureEnabledPreservesErrorResponsePair|TestCaptureCorpusReadableAfterRestart)$' -count=5
```

`TestCaptureCorpusDataIntegrity` sends a request with query parameters,
headers, and a body through the capture middleware; verifies the live response;
saves the corpus; parses the saved JSON; and compares the decoded request and
response bodies, status, content types, headers, paths, and timestamps with
the values that were sent and returned. `TestCaptureCorpusReadableAfterRestart`
is the durability test above: a restarted middleware loads the corpus the first
process wrote and must reload every prior entry losslessly before appending.
Repeating the focused suite five times
guards against intermittent capture or save corruption.

### Where each check is enforced

These checks are enforced automatically, not left as manual steps: the
`seam-ci` verify step runs the corpus-integrity and response-pair tests on
every push to `main` (ahead of the full `go test -race ./...` sweep), and
`scripts/definition-of-done.sh --slow`
-- included in `--all` -- gates the same set plus
`TestCaptureCorpusReadableAfterRestart` as the named checks `corpus integrity`
and `capture corpus round-trip`. Both gates guard the retired root-level
`go test ./corpus` walk behind a `[ -d corpus ]` check, because the purge
removed the directory and the unguarded command fails with "directory not
found". The diffharness module's fixture checks are wired into the
`--slow` lane as a third named check, `diffharness module gate`, which
builds and tests the whole module (`go build ./... && go test ./...`) from
the module directory: the module is standalone, so the root `go test ./...`
sweep never descends into it, and without the lane neither the fixture
validation nor the differential comparison contract's implementation
(internal/compare, seam-replay, seam-cutover — the contract document is
[docs/design/differential-replay-contract.md](design/differential-replay-contract.md),
rule G2) would run anywhere but by hand. The `corpus/` vs `testdata/`
boundary itself is pinned by `internal/corpusboundary` in the root sweep
(and by the NEEDLE close gate, which runs it in a clean extraction). A
malformed capture or a failing round-trip fails the build; an off-base or
malformed secret ref in a checked-in fixture fails the module run — by
hand above, or via the `diffharness module gate` lane — and must be fixed
before the corpus is committed.

## Durability triggers

The capture design promises two persistence triggers — an autosave every ten
entries and a corpus save on graceful shutdown — plus lossless readability of
the saved corpus after a restart. The durability tests in
`internal/server/capture_durability_test.go` pin each trigger deterministically
(saves run synchronously inside the middleware and `Shutdown`, so no test
polls the filesystem or sleeps):

- `TestCaptureAutoSaveFiresOnlyOnThreshold` — with autosave enabled, a write
  lands exactly on the 10th and 20th entries and never between thresholds;
  nine entries leave no file on disk.
- `TestCaptureAutoSaveDisabledNeverWrites` — with autosave disabled, entries
  are captured in memory across multiple threshold windows but nothing is
  persisted until `Save` is called explicitly.
- `TestShutdownFlushesCorpusBelowAutoSaveThreshold` — `Server.Shutdown`
  persists a corpus that never crossed the autosave threshold, with schema,
  service, incumbent, and capture order intact.
- `TestShutdownFlushRespectsCaptureToggle` — shutdown writes nothing while
  capture is disabled (including not clobbering an existing corpus file) and
  tolerates a server built without a capture middleware.
- `TestShutdownSaveFailureIsContained` — a corpus write failure during
  shutdown neither fails the shutdown nor loses the retained entries; the
  error is logged and the process can still exit.
- `TestCaptureCorpusReadableAfterRestart` — a first process crosses the
  autosave threshold and is flushed on shutdown, a fresh middleware loads the
  corpus through the production `Load` path, every persisted entry round-trips
  with request/response bodies, query, status, and order intact, and the
  restarted instance continues the autosave cadence with the loaded history
  included.

Run the durability suite with:

```sh
go test ./internal/server -run '^(TestCaptureAutoSaveFiresOnlyOnThreshold|TestCaptureAutoSaveDisabledNeverWrites|TestShutdownFlushesCorpusBelowAutoSaveThreshold|TestShutdownFlushRespectsCaptureToggle|TestShutdownSaveFailureIsContained|TestCaptureCorpusReadableAfterRestart)$' -count=1
```

Save *failures* around these triggers are covered separately by the focused
failure tests (`TestCaptureDiskWriteFailuresAreNonBlocking`,
`TestCaptureRecoversAfterTransientAutoSaveFailure`,
`TestCaptureJsonMarshalFailure`), which verify an autosave that cannot write
still answers every request, retains all entries, and recovers on the next
threshold.

### Standalone capture tool lifecycle

The standalone `seam-capture` proxy (`tools/diffharness/cmd/seam-capture`,
driven by `scripts/capture-argocd.sh`) follows the same persistence model:

- on start it loads an existing corpus file and appends to it (a `service`
  mismatch with the `--service` flag is refused; an incumbent-URL change only
  warns), or starts a fresh corpus when no file exists;
- an autosave lands every 10 appended entries and a graceful stop flushes the
  corpus, so an ungraceful kill loses at most the entries since the last
  autosave;
- entry IDs must remain unique, so re-capturing a path already present in the
  loaded corpus is refused (logged, not appended) — a deliberate re-capture
  starts from a fresh file; and
- its output under the repository-root `corpus/` directory is gitignored
  runtime data, never a commit candidate (see the storage split above).

## Promotion runbook: capture → fixture

Promotion is the deliberate act of turning a runtime capture under
`corpus/` into a reviewed fixture under `tools/diffharness/testdata/` — the
only path capture data may take into git
(`TestRuntimeCorpusStaysOutOfGit` fails while anything under `corpus/` is
tracked). Neither capture producer writes fixture-ready data: the metadata
below is stamped with producer defaults and `secrets[]` is never
populated, so every step here is mandatory.

### 1. Flush the pending entries to disk

The gateway's capture middleware holds entries in memory between the
persistence triggers above. Flush without stopping the process through the
operator endpoint:

```sh
# Where the corpus lives and how many entries are held (operator listener)
curl -sS -H "Authorization: Bearer $OPERATOR_TOKEN" \
  "$OPERATOR_URL/_seam/capture/status" | jq .
# → {"enabled":true,"entry_count":N,"corpus_dir":"corpus"}

# Snapshot-flush to <corpus_dir>/corpus.json
curl -sS -X POST -H "Authorization: Bearer $OPERATOR_TOKEN" \
  "$OPERATOR_URL/_seam/capture/save" | jq .
# → {"status":"saved","entry_count":N}
```

Both endpoints are bound on the operator listener only — the caller mux
answers 404 for them (`TestCaptureEndpointsAreOperatorOnly`) — and require
the `seam:ops:read` scope. `save` is POST-only (405 otherwise, pinned by
the `save rejects GET` case of `TestCaptureEndpointsMethodAndStateErrors`),
ignores any request body, and is a **snapshot rewrite, not a drain**:
entries stay in memory and a repeat save neither duplicates nor drops
anything (`TestCaptureSaveFlushIsIdempotent`). With capture disabled it
answers 503 `Capture middleware not enabled`; a failed write answers
`capture_failed` and retains the entries. Compare `entry_count` from
`status` before and after the flush — the file must now carry all of them.

Graceful shutdown is the equivalent flush for either producer —
`Server.Shutdown` persists a corpus that never crossed the autosave
threshold (`TestShutdownFlushesCorpusBelowAutoSaveThreshold`), and
`scripts/capture-argocd.sh stop` stops the standalone capture proxy — but
the endpoint does it without ending the capture session.

### 2. Rewrite the capture metadata to the fixture conventions

A raw capture carries its producer's defaults, not the fixture contract of
[`docs/design/argocd-ro-corpus-data-structure.md`](design/argocd-ro-corpus-data-structure.md):

| Field | What a raw capture stamps | Fixture convention |
| --- | --- | --- |
| `schema` | `seam-diff-corpus/v1` | unchanged — the only version `corpus.Load` accepts |
| `service` | `seam` (middleware) / the `--service` flag (`seam-capture`; `capture-argocd.sh` passes the retired `argocd`) | the canonical deployed token `argocd-ro` — `argocd` / `argocd-proxy` are retired |
| `incumbent` | the placeholder `seam-incumbent` (middleware) | the base URL actually captured against |
| `capturedAt` | re-stamped at **every** middleware `Save` | RFC3339 timestamp of the **first** capture; appends never update it |
| per-entry `response` | the incumbent response observed at capture — both producers populate it | **dropped.** A fixture retains request data and replay expectations; replay collects fresh responses from both targets, and the checked-in-fixture walk (`TestCheckedInFixturesResolveUnderEnforcedVaultBase`) rejects an entry that still carries one |
| per-entry `timestamp` | stamped per entry by both producers | optional — informational; promotion may retain or omit it; replay never reads it |
| `secrets[]` | never populated by either producer | one `vault:` ref per credential, written by hand |

- The middleware's `capturedAt` is the *save* time, so a promoted fixture
  must carry the first-capture timestamp instead — take it from the
  session metadata (`corpus/<service>/metadata/capture-session.json`
  `startedAt`) when the session recorded one.
- `seam-capture` stamps `--service` verbatim and refuses a corpus whose
  stored service differs from the flag (an incumbent change only warns), so
  pass `--service argocd-ro` at capture time; because
  `capture-argocd.sh` still hardcodes the retired `argocd`, its output
  always needs the token rewritten during this step, and its
  `corpus/argocd-proxy/` output path names the retired token too.
- Every promoted entry needs its `secrets[].ref` written by hand — both
  producers capture requests only (`seam-capture` carries an explicit
  TODO). Copy the path from the route fragment's `x-vault-path`, keep the
  `vault:` scheme, and prefer `injectAs.kind: bearer` (which takes no
  `name`) for bearer credentials. The shape to copy is
  `vault:rs-manager/rs-manager/seam/routes/argocd-ro/<key>`.

#### What redaction has — and has not — already happened

Redaction is not one step; it happens at three points on the way from capture
to a committed fixture (the full story, including the replay-time leg, is the
design doc's [Redaction
section](design/argocd-ro-corpus-data-structure.md#redaction)):

- **The middleware already scrubbed credential locations at capture time.**
  Request headers `Authorization`, `Proxy-Authorization`, `Cookie`,
  `Set-Cookie`, `X-Api-Key` (case-insensitive), query parameters named
  `api_key` / `api-key` / `apikey`, `access_token` / `access-token`,
  `auth_token` / `auth-token`, and every header/query name the matched route
  fragment declares injectable were replaced with the marker
  `[REDACTED-BY-SEAM]` before the entry was retained. In the promoted fixture
  the marker is the *expected* state — it means "value scrubbed at capture;
  the real value resolves at replay through `secrets[].ref`".
- **The standalone `seam-capture` has only fixed-name header scrubbing.** It
  records everything else that crossed the wire verbatim, so its output can
  carry a literal bearer token in an
  unrecognised header. That is a promotion blocker: the review scrubs it to
  the marker (or removes the header) before the fixture is committed. The
  standalone producer does apply the fixed-name scrub to `Authorization`,
  `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `X-Api-Key`, `Api-Key`, and
  `X-Auth-Token`; it has no route-fragment metadata, so it cannot scrub
  route-specific injectable names or any query parameter.
- **Neither producer scrubs request bodies or response bodies.** A credential
  in either body survives capture; the review checklist's decoded-body
  inspection below is the only gate. The standalone producer also applies its
  fixed-name header scrub when recording response headers, while the gateway
  middleware records response headers verbatim. In either case, promotion
  drops the captured `response` before the candidate reaches the fixture path.

#### The persistence boundary

The runtime file under gitignored `corpus/` is capture data, not a safe
fixture. It may contain an unsanitized standalone query or body and must be
treated as sensitive local working data. The boundary before a checked-in
fixture is absolute:

1. Keep the flushed runtime file under `corpus/`; do not print it, stage it, or
   copy it directly into `tools/diffharness/testdata/`.
2. Create a candidate in a private temporary directory. Remove every
   capture-time `response`, replace every literal credential or other sensitive
   value in request headers, query, and decoded body with
   `[REDACTED-BY-SEAM]` or remove that field, then add only `vault:` secret
   references and reviewed `expect` values. Do not guess when a value is
   sensitive: stop and discard the candidate until its source and purpose are
   known.
3. Inspect only that sanitized candidate, run the fixture checks, and move it
   into `tools/diffharness/testdata/` only after the checks pass. A candidate
   containing a literal credential, an unreviewed sensitive value, or a
   capture-time `response` is not eligible for review, staging, or commit.

The whole conversion — both producer shapes in, fixture-convention corpus
out, markers preserved, verbatim credentials scrubbed — is pinned end to end
by `TestPromotionLifecycleConvertsCaptureToFixture`
(`tools/diffharness/internal/corpus`), which runs in the
`diffharness module gate` lane.

### 3. Gate the promotion through the fixture validations

Do not copy the flushed file directly into the tracked fixture directory.
After the persistence-boundary review above, move the sanitized candidate into
`tools/diffharness/testdata/<name>.json` and let the loader reject what
eyeballing misses. `corpus.Load` — and
`Corpus.AppendEntry` at capture time — validate:

- the file parses as JSON and declares `seam-diff-corpus/v1` exactly;
- a non-empty `service` token;
- unique, non-empty entry IDs;
- canonicalized header keys and HTTP methods (a missing method defaults to
  `GET`); and
- every `secrets[].ref`: `vault:`-schemed, free of traversal (`..`,
  backslashes), glob (`*`, `?`, `[`) and templated (`{}`) segments, and
  resolving strictly inside the enforced base
  `rs-manager/rs-manager/seam/routes` — the retired `seam/routes` base
  fails, and containment is boundary-correct (details in the
  fixture-integrity check above).

`TestCheckedInFixturesResolveUnderEnforcedVaultBase` then walks the
checked-in fixtures with the default enforced base pinned (env cleared)
and, for `corpus-argocd.json`, additionally pins `argocd-ro` as the
`service` token with every ref under `<base>/argocd-ro/`. The walk also
enforces the fixture convention from step 2: an entry that still carries a
capture-time `response` fails it. **That test
walks an explicit fixture list, not a `testdata/` glob — add the promoted
file to the `fixtures` slice in
`tools/diffharness/internal/corpus/corpus_test.go` as part of the
promotion**, so it receives the same walk on every future run. Also work
through the design doc's corpus review checklist over the candidate:
`jq .` for syntax, no literal bearer tokens or unreviewed sensitive values in
headers or query fields (a
`[REDACTED-BY-SEAM]` marker is the expected scrubbed state; the standalone
producer only scrubs its fixed header names), no credential values in decoded
request bodies (bodies are never scrubbed by either producer), no capture-time
`response` left on any entry, descriptions reviewed for sensitive content.

### 4. Re-run the fixture suite, then commit by pathspec

```sh
cd tools/diffharness && go test ./...
```

The module is standalone, so the root sweep never runs it — this re-run is
the promotion gate, not a formality, and it is the same set enforced as the
`diffharness module gate` lane in DoD `--slow`. Then commit the
named paths explicitly:

```sh
git add tools/diffharness/testdata/<name>.json \
        tools/diffharness/internal/corpus/corpus_test.go
git commit -m "test(corpus): promote <name> capture to fixture (seam-…)"
```

A pathspec commit, never a directory add: `corpus/` is gitignored and must
stay untracked, and a blanket `git add` is exactly how a runtime capture —
or the adjacent debris of a capture session — slips into a commit. The
`internal/corpusboundary` suite pins the whole split: the anchored
`/corpus/` ignore entry, the tracked guarded fixture and package paths,
and the fixture-validation lane staying wired.

## Results

Last verified: 2026-09-26.

| Check | Result | Coverage |
| --- | --- | --- |
| `cd tools/diffharness && go test ./...` | PASS | Schema, service, and entry-ID checks plus header/method canonicalization and `secrets[].ref` enforcement against the enforced vault base, including the retired `seam/routes` rejection; the checked-in-fixture walk (no capture-time `response` survives promotion) and the promotion-lifecycle pin (`TestPromotionLifecycleConvertsCaptureToFixture`) that proves the documented capture → fixture conversion |
| `diffharness module gate` (DoD `--slow`) | PASS | The whole nested module — fixtures, comparator, and the replay/cutover tools — built and tested in the Definition of Done, so the root sweep's module boundary cannot silently drop any of it |
| `internal/corpusboundary` | PASS | Anchored `/corpus/` ignore entry present and effective; guarded fixture and package paths outside every ignore rule; nothing under `corpus/` tracked in a real checkout |
| Focused server capture suite, `-count=5` | PASS | Request/response integrity plus successful and error response-pair preservation |
| Capture durability suite, `-count=1` | PASS | Autosave threshold boundary, shutdown flush below threshold, toggle-respect and shutdown-failure containment, corpus readability after restart |

The full `internal/server` package contains broader infrastructure and
performance tests that are outside this integrity check. Run that package
separately when changing capture implementation details; failures in unrelated
readiness, performance, or environment-dependent tests should be reported with
their test name rather than attributed to corpus integrity.

## Review requirements for new captures

These are the content-level review items for a newly captured corpus; the
mechanical steps around them (flush, metadata rewrite, validation gates,
pathspec commit) are the promotion runbook above. Before committing a
newly captured corpus:

1. Run the fixture checks (`cd tools/diffharness && go test ./...`) and the
   focused capture suite above.
2. Confirm the primary corpus has non-empty metadata and unique entry IDs.
3. Confirm request paths do not contain an embedded query string; put the
   query in the `query` field.
4. Confirm non-empty request bodies decode from base64 and contain no
   credential values.
5. For middleware capture output, verify both request and response bodies,
   status, headers, and content types are present and decode correctly.
6. Keep credentials as route references such as
   `vault:rs-manager/rs-manager/seam/routes/<service>/<key>` (SEAM's enforced
   base; the older cluster-agnostic `seam/routes` base is **retired** as of
   the 2026-09-04 consolidation and fails validation). This is enforced, not
   just reviewed: `corpus.Load` rejects any ref that is not `vault:`-schemed,
   free of traversal/glob/template characters, and strictly under the
   enforced base — see the fixture-integrity check above. Never put the
   resolved value in a corpus, test fixture, log, or report.
