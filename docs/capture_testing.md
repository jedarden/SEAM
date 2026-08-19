# Capture and Corpus Testing

This document records the integrity checks for captured corpus data. The
repository uses two related formats:

- `corpus/**/corpus.json` and `corpus-template.json` are standalone
  differential-harness inputs. They persist complete request data and replay
  expectations; response values are collected from the incumbent and SEAM at
  replay time.
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
differential corpus. The two optional ArgoCD endpoint files are valid empty
JSON arrays because no endpoint capture has been recorded for them yet.

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

## Results

Last verified: 2026-08-19.

| Check | Result | Coverage |
| --- | --- | --- |
| `go test ./corpus` | PASS | All checked-in corpus JSON documents and differential request records |
| Focused server capture suite, `-count=5` | PASS | Request/response integrity plus successful and error response-pair preservation |

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
   `vault:seam/routes/<service>/<key>`; never put the resolved value in a
   corpus, test fixture, log, or report.
