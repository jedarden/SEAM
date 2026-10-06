# Aligned capture and replay schema contract

This document defines the lifecycle contract for the differential-harness
artifacts. It is intentionally narrower than an HTTP route schema: it says
which values may cross each persistence boundary and which values are produced
only while replay is running.

The persisted corpus envelope is `seam-diff-corpus/v1`. The contract has four
artifact forms:

1. a private runtime capture;
2. a private sanitized candidate prepared for promotion;
3. a checked-in replay fixture; and
4. a replay output report.

The first three are related, but they are not interchangeable. In particular,
a capture-time response is evidence about the capture session, not the oracle
used by replay.

## Implementation sources of truth

The JSON shape is implemented by these exact Go types:

- Gateway runtime captures: `internal/server.CorpusFile`, `CorpusEntry`,
  `CapturedRequest`, and `CapturedResponse` in
  [`internal/server/capture.go`](../../internal/server/capture.go).
- Standalone capture and checked-in fixtures: `corpus.Corpus`, `Entry`,
  `Request`, `Response`, `Secret`, `InjectAs`, and `Expect` in
  [`tools/diffharness/internal/corpus/corpus.go`](../../tools/diffharness/internal/corpus/corpus.go).
  `corpus.Load` is the fixture loader and `Corpus.AppendEntry` applies the
  same request/header/method and secret-reference checks when an entry is
  appended.
- Responses collected during replay: `compare.Response` in
  [`tools/diffharness/internal/compare/compare.go`](../../tools/diffharness/internal/compare/compare.go).
  `seam-replay.replayOne` constructs this in memory for each target; it does
  not write it into the corpus.
- Replay output: `seam-replay.Report` and `EntryReport` in
  [`tools/diffharness/cmd/seam-replay/main.go`](../../tools/diffharness/cmd/seam-replay/main.go).
  The report is written only when `--report` is supplied.

The standalone `corpus` types are the promotion and fixture contract. The
gateway types intentionally omit replay-only fields (`secrets` and `expect`)
because the gateway capture is raw runtime data. Their shared JSON fields are
listed below rather than relying on the two implementations staying aligned by
accident.

## Artifact ownership and allowed fields

### 1. Private runtime capture

Location: the gitignored repository `corpus/` tree, or another private capture
location. A runtime capture must be treated as sensitive local data until it
has been reviewed.

Top-level fields are `schema`, `service`, `incumbent`, `capturedAt`,
`description`, and `entries`. Each entry may contain `id`, `timestamp`,
`description`, `request`, and `response`. The capture producers do not add
`secrets` or `expect`.

The request fields are exactly:

| Field | JSON type | Meaning |
| --- | --- | --- |
| `method` | string | HTTP method; the harness uppercases it on load and defaults an absent method to `GET`. |
| `path` | string | Path only, without the query string. |
| `query` | string, optional | Query string without the leading `?`. |
| `headers` | object of string arrays, optional | Canonicalized request header names and their repeated values. |
| `bodyB64` | string, optional | Standard base64 of the request body; empty means no body. |
| `bodyContentType` | string, optional | Content type associated with `bodyB64`. |

The capture-time response fields are exactly:

| Field | JSON type | Meaning |
| --- | --- | --- |
| `statusCode` | integer | Status returned by the incumbent during capture. |
| `headers` | object of string arrays, optional | Canonicalized response header names and repeated values. |
| `bodyB64` | string, optional | Standard base64 of the captured response body. |
| `bodyContentType` | string, optional | Content type associated with the captured response body. |

The gateway writes these response fields through `CapturedResponse`; the
standalone proxy writes the same JSON shape through `corpus.Response`. The
standalone type uses a nullable `*Response` on `Entry`, while the gateway's
`CapturedResponse` field is a value. That implementation detail does not make
the response a fixture field: promotion removes it.

### 2. Private sanitized candidate

Location: a private temporary directory, never `corpus/` and never the
checked-in fixture directory. This is the reviewed handoff artifact between a
raw capture and a fixture; it has no separate Go type or schema version.

A candidate is a `corpus.Corpus` JSON document after sanitization. It may carry
the same top-level fields as a corpus and the following entry fields:

- `id`, optional `timestamp`, optional `description`, and `request`;
- optional `secrets`, containing references and injection metadata only; and
- optional `expect`, containing replay comparison policy only.

The candidate must not contain an entry `response`. It must also not contain
the in-memory `Secret.Bare` field: that field is tagged `json:"-"` and is
never serialized. A candidate that still has a capture-time response, a
literal credential, or an unreviewed sensitive request value is discarded,
not promoted.

Sanitization does not change the request field names or types. It removes or
replaces sensitive values in request headers, query fields, and decoded bodies
with the repository's redaction marker or an intentionally absent field. A
base64 body is not safe merely because it is encoded; it must be decoded and
reviewed before promotion.

`secrets[]` is configuration, not a response expectation. Its allowed fields
are:

| Field | JSON type | Meaning |
| --- | --- | --- |
| `ref` | string | A `vault:` reference under the enforced base; never a resolved value. |
| `injectAs.kind` | string | Injection kind: `header`, `bearer`, or `query`. |
| `injectAs.name` | string, conditional | Header/query name; omitted for `bearer`. |

`expect` is also configuration, not a captured response. Its only allowed
fields are:

| Field | JSON type | Meaning |
| --- | --- | --- |
| `status` | integer, optional | Expected status for the SEAM response when the route intentionally transforms status. |
| `ignoreHeaders` | string array, optional | Per-entry response headers ignored by comparison. |
| `ignoreBody` | boolean, optional | Suppresses structural body comparison while retaining leak scanning. |
| `skip` | string, optional | Reason the entry is not replayable yet. |

The candidate is checked with the same loader used for fixtures. The relevant
promotion and fixture-integrity checks are described in
[`docs/capture_testing.md`](../capture_testing.md), including the explicit
check that checked-in entries have no `response`.

### 3. Checked-in replay fixture

Location: `tools/diffharness/testdata/*.json`. This is the durable, reviewed
form that a fresh checkout can load. Its type is `corpus.Corpus` and its entry
type is `corpus.Entry`.

A fixture permits the same request fields as a runtime capture:
`method`, `path`, `query`, `headers`, `bodyB64`, and `bodyContentType`. It may
also retain the informational entry `timestamp`, `description`, `secrets`,
and `expect` fields described above.

The fixture has no entry `response`, by contract. A response in a checked-in
fixture would be capture-time data that replay never reads. The fixture's
`secrets` values are references only, and its request bodies are permitted only
after their decoded contents have passed the sanitization review.

### 4. Replay output

Location: the private path supplied to `seam-replay --report`. Its exact
serialized type is `Report`:

| Field | JSON type | Meaning |
| --- | --- | --- |
| `corpus` | string | Fixture service token. |
| `corpusPath` | string | Input fixture path. |
| `incumbent` | string | Incumbent base used by the run. |
| `seam` | string | SEAM base used by the run. |
| `runAt` | string | Report creation time. |
| `durationSeconds` | number | Total replay duration. |
| `passCount` | integer | Number of `PASS` entries. |
| `failCount` | integer | Number of `FAIL` entries. |
| `skipCount` | integer | Number of `SKIP` entries. |
| `entries` | array of `EntryReport` | One result per fixture entry. |

Each `EntryReport` may contain only the fields represented by its Go struct:
`id`, `description`, `verdict`, `skipReason`, `secretLeaked`, `leakedSecret`,
`leakedWhere`, `reasons`, `statusDiff`, `headerDiffs`, `trailerDiffs`,
`bodyDiff`, and `bodyIgnored`. A report is not a corpus and must not be fed
back to `corpus.Load`.

The response data used to produce a report is transient. For each target,
`replayOne` collects a `compare.Response` with exactly `Status`, `Headers`,
`Body`, and `Trailers`. These values are not serialized as a response object in
the report. Only comparison results may appear:

- `statusDiff` contains `incumbent`, `seam`, and optional `expected` status;
- `headerDiffs` and `trailerDiffs` contain `name`, `kind`, and optional
  incumbent/SEAM value arrays;
- `bodyDiff` contains `incumbentLen`, `seamLen`, and an optional short
  `preview`; and
- `bodyIgnored` records that structural body comparison was suppressed.

The response bodies and headers collected during execution therefore cannot
become fixture expectations by implication. They are fresh observations from
the incumbent and SEAM targets. The report's diff preview is already secret-
redacted by the comparator, but reports may still contain other payload
context; keep them private and do not paste them into documentation.

## Capture-to-replay field mapping

The following mapping is deliberately placeholder-only. Angle-bracket tokens
are instructions for the value's kind, not example data.

| Field | Runtime capture | Sanitized candidate | Checked-in fixture | Replay execution/output |
| --- | --- | --- | --- | --- |
| `request.method` | `<HTTP_METHOD>` | retained | retained | sent to both targets; not copied into `Report` |
| `request.path` | `<REQUEST_PATH>` | retained | retained | sent to both targets; not copied into `Report` |
| `request.query` | `<QUERY_STRING>` | retained after review | retained after review | appended to both target URLs; not copied into `Report` |
| `request.headers` | `<REQUEST_HEADERS>` | redacted/reviewed `<SANITIZED_HEADERS>` | retained sanitized map | sent to both targets; not copied into `Report` |
| `request.bodyB64` | `<CAPTURED_REQUEST_BODY_B64>` | retained only if decoded review passes | retained only if decoded review passes | decoded and sent to both targets; not copied into `Report` |
| `request.bodyContentType` | `<REQUEST_CONTENT_TYPE>` | retained | retained | applied to the replay request; not copied into `Report` |
| `response.statusCode` | `<CAPTURE_STATUS_CODE>` | removed | removed | collected as `<INCUMBENT_STATUS>` and `<SEAM_STATUS>`; summarized only in `<STATUS_DIFF>` when needed |
| `response.headers` | `<CAPTURED_RESPONSE_HEADERS>` | removed | removed | collected per target; summarized as `<HEADER_DIFFS>` |
| `response.bodyB64` | `<CAPTURED_RESPONSE_BODY_B64>` | removed | removed | collected per target; summarized as `<BODY_DIFF>` or omitted when equal/ignored |
| `response.bodyContentType` | `<CAPTURED_RESPONSE_CONTENT_TYPE>` | removed | removed | used only in the transient HTTP response; not emitted by `Report` |
| `secrets[].ref` | absent from producer output | `<SECRET_REF>` | retained reference | resolved to an in-memory `<RESOLVED_SECRET>`; the value is never serialized |
| `secrets[].injectAs` | absent from producer output | `<INJECTION_SPEC>` | retained | used for the target request; not emitted by `Report` |
| `expect.status` | absent from producer output | `<EXPECTED_STATUS>` | retained if needed | becomes comparator option `<EXPECTED_STATUS>` and may appear in `<STATUS_DIFF>` |
| `expect.ignoreHeaders` | absent from producer output | `<IGNORED_HEADER_NAMES>` | retained if needed | merged with defaults before comparison |
| `expect.ignoreBody` | absent from producer output | `<BOOLEAN>` | retained if needed | controls structural comparison; leak scan remains active |
| `expect.skip` | absent from producer output | `<SKIP_REASON>` | retained if needed | yields `<SKIP>` without sending requests |
| `entries[].verdict` | absent | absent | absent | `<PASS_OR_FAIL_OR_SKIP>` in `EntryReport` |

No field mapping permits a response captured at time A to stand in for a
response collected at replay time B. The former is private capture evidence;
the latter is the observation compared by the harness.

## Security invariants

- Store runtime captures, sanitized candidates, secret-resolution inputs, and
  replay reports privately. Only reviewed fixtures belong in git.
- Persist references, never credential values. `Secret.Bare` exists only in
  replay memory and is excluded by `json:"-"`.
- Treat `bodyB64` as data, not as a concealment mechanism. Decode it during
  review and remove or redact sensitive content before a candidate can become a
  fixture.
- Do not use the capture-time `response` as an expectation and do not copy it
  into a candidate or fixture.
- Documentation examples and field mappings in this contract use placeholders
  only; they intentionally contain no credential, endpoint, payload, or other
  sensitive value.

The response-equivalence rules themselves remain in
[`differential-replay-contract.md`](differential-replay-contract.md). This
document defines the artifact boundaries that make those rules unambiguous.
