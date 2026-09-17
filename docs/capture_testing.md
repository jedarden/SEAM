# Capture and Corpus Testing

This document records the integrity checks for captured corpus data. The
repository uses two related formats:

- Most `corpus/**/corpus.json` and `corpus-template.json` files are standalone
  differential-harness inputs. They persist request data and replay
  expectations; response values are collected from the incumbent and SEAM at
  replay time. The live ArgoCD capture is the exception: its eight entries
  also retain the incumbent response for capture auditing.
- `corpus/argocd-proxy/*.json` includes the complete ArgoCD corpus, its schema
  template, and response-body snapshots. The request plus response pair is
  always the entry in `corpus/argocd-proxy/corpus.json`; snapshots are
  developer-friendly response fixtures and are checked against those entries.
- `internal/server` capture files persist complete request/response pairs for
  middleware-level capture tests. Request and response bodies are encoded as
  standard base64 strings.

## Automated checks

Run the repository fixture checks from the repository root:

```sh
go test ./corpus
```

This walks every checked-in `.json` file below `corpus/`, rejects empty files,
and verifies JSON syntax. It also checks the metadata, unique entry IDs,
request method/path, header shape, and base64 request bodies in each primary
differential corpus. The ArgoCD-specific tests additionally require complete
request/response pairs, validate response bodies, check route coverage, and
match each response snapshot to its captured pair; see
[`corpus/argocd-proxy/COMPLETENESS.md`](../corpus/argocd-proxy/COMPLETENESS.md).

Run the request/response round-trip check and the existing response-pair
regressions with:

```sh
go test ./internal/server -run '^(TestCaptureCorpusDataIntegrity|TestProxyCaptureEnabledPreservesSuccessfulResponsePair|TestProxyCaptureEnabledPreservesErrorResponsePair)$' -count=5
```

`TestCaptureCorpusDataIntegrity` sends a request with query parameters,
headers, and a body through the capture middleware; verifies the live response;
saves the corpus; parses the saved JSON; and compares the decoded request and
response bodies, status, content types, headers, paths, and timestamps with
the values that were sent and returned. Repeating the focused suite five times
guards against intermittent capture or save corruption.

Both checks are enforced automatically, not left as manual steps: the
`seam-ci` verify step runs them on every push to `main` (ahead of the full
`go test -race ./...` sweep), and `scripts/definition-of-done.sh --slow`
-- included in `--all` -- gates them as the named checks `corpus integrity`
and `capture corpus round-trip`. A malformed, incomplete, or mismatched
corpus or response snapshot fails the build.

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

## Results

Last verified: 2026-09-16.

| Check | Result | Coverage |
| --- | --- | --- |
| `go test ./corpus` | PASS | All checked-in corpus JSON documents, differential request records, and the complete ArgoCD capture |
| Focused server capture suite, `-count=5` | PASS | Request/response integrity plus successful and error response-pair preservation |
| Capture durability suite, `-count=1` | PASS | Autosave threshold boundary, shutdown flush below threshold, toggle-respect and shutdown-failure containment, corpus readability after restart |

The full `internal/server` package contains broader infrastructure and
performance tests that are outside this integrity check. Run that package
separately when changing capture implementation details; failures in unrelated
readiness, performance, or environment-dependent tests should be reported with
their test name rather than attributed to corpus integrity.

## Review requirements for new captures

Before committing a newly captured corpus:

1. Run `go test ./corpus` and inspect the listed file count in the test output.
2. Confirm every primary corpus has non-empty metadata and unique entry IDs.
3. Confirm request paths do not contain an embedded query string; put the
   query in the `query` field.
4. Confirm non-empty request bodies decode from base64 and contain no
   credential values.
5. For middleware capture output, verify both request and response bodies,
   status, headers, and content types are present and decode correctly.
6. Keep credentials as route references such as
   `vault:rs-manager/rs-manager/seam/routes/<service>/<key>` (SEAM's enforced
   base; the older cluster-agnostic `seam/routes` base is **retired** as of
   the 2026-09-04 consolidation and fails validation); never put the resolved
   value in a corpus, test fixture, log, or report.
