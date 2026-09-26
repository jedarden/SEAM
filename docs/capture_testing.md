# Capture and Corpus Testing

This document records the integrity checks for captured corpus data. The
repository keeps corpus data in two places:

- `tools/diffharness/testdata/*.json` holds the checked-in
  differential-harness fixtures: `corpus-argocd.json` (the deployed
  `argocd-ro` capture) and `example-corpus.json`. These persist request data
  and replay expectations; response values are collected from the incumbent
  and SEAM at replay time. The fixture format, including the
  `secrets[].ref` grammar, is specified in
  [`docs/design/argocd-ro-corpus-data-structure.md`](design/argocd-ro-corpus-data-structure.md).
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
never committing it under `corpus/`.

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
The mirror lives in `tools/diffharness/internal/corpus/corpus.go`; when the
base moves, move both. The checked-in fixtures themselves are walked by
`TestCheckedInFixturesResolveUnderEnforcedVaultBase`, which also pins
`argocd-ro` as the canonical service token.

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
`--slow` lane as a third named check, `diffharness fixture validation`,
which runs `go test ./internal/corpus/` from the module directory: the
module is standalone, so the root `go test ./...` sweep never descends into
it, and without the lane the fixture validation only happened when someone
remembered to run it by hand. The `corpus/` vs `testdata/` boundary itself
is pinned by `internal/corpusboundary` in the root sweep (and by the
NEEDLE close gate, which runs it in a clean extraction). A malformed capture
or a failing round-trip fails the build; an off-base or malformed secret ref
in a checked-in fixture fails the module run — by hand above, or via the
`diffharness fixture validation` lane — and must be fixed before the
corpus is committed.

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

## Results

Last verified: 2026-09-26.

| Check | Result | Coverage |
| --- | --- | --- |
| `cd tools/diffharness && go test ./...` | PASS | Schema, service, and entry-ID checks plus header/method canonicalization and `secrets[].ref` enforcement against the enforced vault base, including the retired `seam/routes` rejection |
| `diffharness fixture validation` (DoD `--slow`) | PASS | The same fixture validation, wired into the Definition of Done as `go test ./internal/corpus/` so the root sweep's module boundary cannot silently drop it |
| `internal/corpusboundary` | PASS | Anchored `/corpus/` ignore entry present and effective; guarded fixture and package paths outside every ignore rule; nothing under `corpus/` tracked in a real checkout |
| Focused server capture suite, `-count=5` | PASS | Request/response integrity plus successful and error response-pair preservation |
| Capture durability suite, `-count=1` | PASS | Autosave threshold boundary, shutdown flush below threshold, toggle-respect and shutdown-failure containment, corpus readability after restart |

The full `internal/server` package contains broader infrastructure and
performance tests that are outside this integrity check. Run that package
separately when changing capture implementation details; failures in unrelated
readiness, performance, or environment-dependent tests should be reported with
their test name rather than attributed to corpus integrity.

## Review requirements for new captures

Before committing a newly captured corpus:

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
