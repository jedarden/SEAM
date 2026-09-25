# ArgoCD-RO Proxy Corpus Data Structure Design

## Document Metadata

- **Created:** 2026-07-27
- **Bead:** seam-ace37016 (explore) — closed; rehydrated from retired bead-forge ID `bf-49g8`
- **Status:** Design Complete
- **Related Beads:** 
  - seam-89d9a0f8 (setup capture mechanism) — closed; from `bf-1d0k`
  - seam-5e9046fc (argocd-ro proxy architecture) — closed; from `bf-69n1`
  - seam-611cf731 (corpus capture validation) — closed; from `bf-4qv5`

> **Bead ID provenance:** this document was written against the retired
> bead-forge (`bf-*`) store. The workspace was rehydrated into bead-rs on
> 2026-08-14 (commit 9e9b514); each reference above now names its current
> `seam-*` bead, with the original `bf-*` ID retained as provenance. All
> linked beads are closed — they record design-era context, not open work.

## Overview

This document specifies the data structure for capturing HTTP request/response pairs from the ArgoCD read-only proxy. The corpus serves as the oracle for differential testing during service migration to SEAM.

> **Secret-reference base:** the `ref` values below are written against SEAM's
> enforced base `rs-manager/rs-manager/seam/routes` (`internal/spec/allowlist.go`
> `DefaultVaultBaseDir`; `SEAM_VAULT_BASE_DIR` overrides), matching the
> `x-vault-path` the fragments now carry. The earlier cluster-agnostic base
> `seam/routes` is **retired** (consolidated 2026-09-04) — a corpus reference
> under the old base would resolve outside the enforced prefix and fail
> validation, so it must not be copied into a new capture.
>
> **Canonical service token:** `argocd-ro`, the deployed fragment's
> `x-seam-owner`. The corpus loader now enforces both properties at fixture
> time (`tools/diffharness/internal/corpus` `Load` / `AppendEntry`): every
> `secrets[].ref` must be `vault:`-schemed, free of traversal, glob, and
> templated segments, and resolve strictly under the enforced base — a corpus
> carrying an off-base or malformed ref is rejected when the fixture is
> loaded, not when a replay fails to resolve it.

## Design Principles

1. **Security-First:** Credentials stored as references only, never literal values
2. **Two-tier storage:** The reviewed fixtures under `tools/diffharness/testdata/*.json` are
   committed so replay is reproducible from a fresh clone. Runtime captures written under a
   repository-root `corpus/` directory are **gitignored** (since the 2026-09-18 history purge,
   seam-70ae655e / commit 9984a5b) and stay out of git; promoting a runtime capture to a
   fixture is a deliberate, reviewed act, not an automatic one
3. **Replayable:** Each entry can be replayed verbatim against both incumbent and SEAM
4. **Differential-Ready:** Structured for comparison between two HTTP responses
5. **Self-Documenting:** Human-readable descriptions and stable IDs

## JSON Schema Definition

### Top-Level Schema

```json
{
  "schema": "seam-diff-corpus/v1",
  "service": "argocd-ro",
  "incumbent": "https://argocd-ro-ardenone-manager-ts.ardenone.com:8444",
  "capturedAt": "2026-07-27T12:00:00Z",
  "description": "ArgoCD read-only proxy corpus captured from production",
  "entries": []
}
```

### Field Definitions

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `schema` | string | Yes | Schema version identifier. Must be `"seam-diff-corpus/v1"` |
| `service` | string | Yes | Service name token. Must be `"argocd-ro"` — the deployed fragment's `x-seam-owner` (`declarative-config/k8s/rs-manager/seam/routes/argocd-ro/`). The earlier `argocd` / `argocd-proxy` tokens are retired and must not be copied into a new corpus |
| `incumbent` | string | Yes | Base URL of incumbent proxy captured against |
| `capturedAt` | string | Yes | RFC3339 timestamp of first capture |
| `description` | string | Yes | Free-form description of the corpus |
| `entries` | array | Yes | Array of corpus entry objects |

### Entry Schema

```json
{
  "id": "list-applications-get",
  "description": "List all ArgoCD applications",
  "request": {
    "method": "GET",
    "path": "/api/v1/applications",
    "query": "",
    "headers": {
      "Accept": ["application/json"]
    },
    "bodyB64": "",
    "bodyContentType": ""
  },
  "secrets": [
    {
      "ref": "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token",
      "injectAs": {
        "kind": "bearer"
      }
    }
  ],
  "expect": {
    "ignoreHeaders": ["Date", "Server"]
  }
}
```

### Entry Field Definitions

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | Yes | Stable, human-readable entry ID (kebab-case) |
| `description` | string | No | What this entry exercises |
| `request` | object | Yes | HTTP request object (see below) |
| `secrets` | array | No | Secret reference objects |
| `expect` | object | No | Per-entry comparison overrides |

### Request Object Schema

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `method` | string | Yes | HTTP method (canonicalized: GET, POST, etc.) |
| `path` | string | Yes | Request path only, no query string |
| `query` | string | No | Query string without leading `?` |
| `headers` | object | No | Header map with canonicalized keys |
| `bodyB64` | string | No | Base64-encoded request body |
| `bodyContentType` | string | No | Content-Type of request body |

### Secret Reference Schema

```json
{
  "ref": "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token",
  "injectAs": {
    "kind": "bearer"
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ref` | string | Yes | Secret reference path (e.g., `vault:...`) |
| `injectAs` | object | Yes | Injection specification (see below) |

### InjectAs Schema

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `kind` | string | Yes | Injection method: `header`, `bearer`, or `query` |
| `name` | string | Conditional | Header/query name (required for `header`/`query`, rejected for `bearer`) |

### Expect Schema

```json
{
  "status": 200,
  "ignoreHeaders": ["Date", "Server", "X-Request-Id"],
  "ignoreBody": false,
  "skip": ""
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `status` | int | No | Expected status code (nil means differential comparison) |
| `ignoreHeaders` | array | No | Header names to ignore during comparison |
| `ignoreBody` | bool | No | Skip body comparison entirely |
| `skip` | string | No | Skip reason (empty = not skipped) |

## File Organization Structure

### Directory Layout

Corpus data lives in two tiers:

**Committed fixtures** — `tools/diffharness/testdata/*.json`, tracked in git:

```
tools/diffharness/testdata/
├── corpus-argocd.json            # The deployed argocd-ro capture (fixture)
├── example-corpus.json           # Schema/example fixture used by the module tests
└── secrets-argocd.local.json     # Fixture secret references (refs only, no values)
```

These are validated by the diffharness module's own tests
(`cd tools/diffharness && go test ./...`) and are the corpora a fresh clone
can replay immediately.

**Runtime captures** — under a repository-root `corpus/` directory,
**gitignored** (`/corpus/` in `.gitignore` since the 2026-09-18 purge):

```
corpus/                                # gitignored — never committed
└── <service>/
    ├── corpus.json                    # Primary corpus file written by a capture run
    ├── secrets.local.json             # Local secret values (git-ignored by design)
    └── metadata/                      # Session metadata (optional)
```

The `/corpus/` ignore rule is deliberately anchored to the repository root:
a bare `corpus/` pattern would also ignore the
`tools/diffharness/internal/corpus/` Go package and silently drop its tests
from every commit.

### File Naming Conventions

**Primary corpus file:** `corpus.json`
- Single source of truth for differential testing
- Sorted by entry ID for stable diffs
- Committed **only** as a reviewed fixture under `tools/diffharness/testdata/`;
  the runtime copy stays untracked

**Per-endpoint captures:** `{endpoint}-{method}.json`
- Optional granular capture files
- Useful for debugging specific endpoints
- Merged into primary corpus before testing

**Example fixture:** `tools/diffharness/testdata/example-corpus.json`
- Committed example entries showing all fields
- Reference for manual corpus creation
- Exercised by the diffharness module tests

## Metadata Format

### Timestamps

**Format:** RFC3339 with timezone offset

**Locations:**
1. **Corpus-level:** `capturedAt` field
   - Set on first capture
   - Never updated on subsequent appends
   - Example: `"2026-07-27T12:00:00-04:00"`

2. **Entry-level:** (optional) `capturedAt` in entry metadata
   - Capture time of individual entry
   - Useful for chronological analysis
   - Not required for replay

### Request IDs

**Entry ID generation:**

```
entryID(path, method) = kebab-case(path) + "-" + lower(method)
```

**Examples:**
- `/api/v1/applications` + `GET` → `api-v1-applications-get`
- `/api/v1/clusters/{name}` + `GET` → `api-v1-clusters-name-get`
- `/api/v1/applications/{name}/sync` + `GET` → `api-v1-applications-name-sync-get`

**Manual IDs:** For special cases, IDs can be manually specified:
- `health-check-get`
- `metrics-get`
- `streaming-logs-post`

### Session Metadata

**File:** `corpus/argocd-ro/metadata/capture-session.json`

```json
{
  "sessionId": "capture-2026-07-27-120000",
  "startedAt": "2026-07-27T12:00:00-04:00",
  "endedAt": "2026-07-27T12:15:30-04:00",
  "captureTool": "seam-capture",
  "captureToolVersion": "v0.1.0",
  "incumbentUrl": "https://argocd-ro-ardenone-manager-ts.ardenone.com:8444",
  "entriesCaptured": 15,
  "captureDurationSeconds": 930,
  "captureHost": "localhost",
  "capturePort": 8082
}
```

## ArgoCD-Specific Considerations

### Key API Endpoints

**Applications:**
1. `GET /api/v1/applications` - List all applications
2. `GET /api/v1/applications/{name}` - Get specific application
3. `GET /api/v1/applications/{name}/sync` - Get sync status
4. `GET /api/v1/applications/{name}/manifest` - Get application manifest

**Clusters:**
1. `GET /api/v1/clusters` - List all clusters
2. `GET /api/v1/clusters/{name}` - Get specific cluster details

**Repositories:**
1. `GET /api/v1/repositories` - List repositories
2. `GET /api/v1/repositories/{url}` - Get specific repository

### Headers to Always Ignore

ArgoCD API returns volatile headers that should always be ignored:

```json
{
  "expect": {
    "ignoreHeaders": [
      "Date",
      "Server",
      "X-Request-Id",
      "Set-Cookie",
      "ETag"
    ]
  }
}
```

### Secret Injection Pattern

All ArgoCD API requests require bearer authentication:

```json
{
  "secrets": [
    {
      "ref": "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token",
      "injectAs": {
        "kind": "bearer"
      }
    }
  ]
}
```

## Security Considerations

### Credential Safety

✅ **Safe:**
- Secret references (e.g., `vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token`)
- Checked-in fixture corpora under `tools/diffharness/testdata/`, after the review checklist below

❌ **Never:**
- Literal credential values in corpus files
- Base64-encoded credentials (except request/response bodies)
- Personal access tokens or API keys
- Committing a runtime capture from `corpus/` without promoting it to a reviewed fixture first

### Corpus Review Checklist

Before promoting a runtime capture into a committed fixture under
`tools/diffharness/testdata/`:

1. ✅ Verify all `secrets[].ref` fields use reference format
2. ✅ Check no literal bearer tokens in headers
3. ✅ Ensure response bodies don't leak credentials
4. ✅ Validate JSON syntax with `jq .`
5. ✅ Review descriptions for sensitive information
6. ✅ Run the fixture checks (`cd tools/diffharness && go test ./...`) — the
   loader rejects an off-base or malformed `secrets[].ref` at fixture time

Runtime captures under `corpus/` are never committed as-is; they are
working data for a replay run, and only a reviewed copy becomes a fixture.

### Access Control

**Corpus files:**
- Mode: `0644` (readable by all, writable by owner)
- Git-tracked: fixtures only (`tools/diffharness/testdata/`); runtime `corpus/` is git-ignored
- Encryption: No (plaintext JSON)

**Secret resolution:**
- Runtime: Memory-only resolution from local secrets source
- Source: `internal/secref` package
- Storage: Git-ignored local files

## Complete Example

```json
{
  "schema": "seam-diff-corpus/v1",
  "service": "argocd-ro",
  "incumbent": "https://argocd-ro-ardenone-manager-ts.ardenone.com:8444",
  "capturedAt": "2026-07-27T12:00:00-04:00",
  "description": "ArgoCD read-only proxy corpus captured from production",
  "entries": [
    {
      "id": "api-v1-applications-get",
      "description": "List all ArgoCD applications",
      "request": {
        "method": "GET",
        "path": "/api/v1/applications",
        "query": "",
        "headers": {
          "Accept": ["application/json"],
          "User-Agent": ["curl/8.14.1"]
        },
        "bodyB64": "",
        "bodyContentType": ""
      },
      "secrets": [
        {
          "ref": "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token",
          "injectAs": {
            "kind": "bearer"
          }
        }
      ],
      "expect": {
        "ignoreHeaders": ["Date", "Server", "X-Request-Id"]
      }
    },
    {
      "id": "api-v1-applications-myapp-manifest-get",
      "description": "Get application manifest for 'myapp'",
      "request": {
        "method": "GET",
        "path": "/api/v1/applications/myapp/manifest",
        "query": "",
        "headers": {
          "Accept": ["application/json"]
        },
        "bodyB64": "",
        "bodyContentType": ""
      },
      "secrets": [
        {
          "ref": "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token",
          "injectAs": {
            "kind": "bearer"
          }
        }
      ]
    },
    {
      "id": "api-v1-clusters-get",
      "description": "List all ArgoCD clusters",
      "request": {
        "method": "GET",
        "path": "/api/v1/clusters",
        "query": "",
        "headers": {
          "Accept": ["application/json"]
        },
        "bodyB64": "",
        "bodyContentType": ""
      },
      "secrets": [
        {
          "ref": "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token",
          "injectAs": {
            "kind": "bearer"
          }
        }
      ]
    }
  ]
}
```

## Implementation Reference

### Go Struct Definitions

Located in `tools/diffharness/internal/corpus/corpus.go`:

```go
type Corpus struct {
    Schema      string
    Service     string
    Incumbent   string
    CapturedAt  string
    Description string
    Entries     []Entry
}

type Entry struct {
    ID          string
    Description string
    Request     Request
    Secrets     []Secret
    Expect      *Expect
}

type Request struct {
    Method          string
    Path            string
    Query           string
    Headers         map[string][]string
    BodyB64         string
    BodyContentType string
}

type Secret struct {
    Ref      string
    InjectAs InjectAs
    Bare     string // never serialized
}

type InjectAs struct {
    Kind string
    Name string
}

type Expect struct {
    Status        *int
    IgnoreHeaders []string
    IgnoreBody    bool
    Skip          string
}
```

## Validation Rules

### Corpus-Level Validation

1. **Schema version:** Must match `seam-diff-corpus/v1`
2. **Service name:** Must be non-empty
3. **Entry IDs:** Must be unique within corpus
4. **Entry sorting:** Sorted by ID for stable diffs

### Entry-Level Validation

1. **ID:** Required, unique, stable
2. **Request:** Required, with at least method and path
3. **Headers:** Canonicalized keys (http.CanonicalHeaderKey)
4. **Method:** Canonicalized (textproto.CanonicalMIMEHeaderKey)
5. **Secrets:** Valid reference format — enforced at load time against the enforced vault base (see the note above)
6. **Expect:** Valid injection kind (header/bearer/query)

### Differential Testing Validation

1. **Status comparison:** By default, require incumbent.status == seam.status
2. **Header comparison:** Case-insensitive, ignoring specified headers
3. **Body comparison:** Exact match, unless `ignoreBody: true`
4. **Skipped entries:** Appear in report but don't fail it

## Usage Patterns

### Capture Phase

```bash
# Start capture proxy
./scripts/capture-argocd.sh start

# Make test requests
curl -sk http://localhost:8082/api/v1/applications
curl -sk http://localhost:8082/api/v1/clusters

# Stop capture and save corpus
./scripts/capture-argocd.sh stop
```

**Capture/restart lifecycle.** The supported lifecycle is
start → capture → graceful stop → restart:

- `start` launches the capture proxy; persistence is automatic from there —
  an autosave lands every 10 entries, and a graceful stop (`stop`, SIGTERM)
  flushes the corpus. Killing the process ungracefully loses at most the
  entries captured since the last autosave.
- A restart does **not** truncate: on start the tool loads an existing corpus
  file and appends to it (`capturedAt` stays pinned to the first capture). A
  corpus whose `service` token differs from the `--service` flag is refused
  rather than mixed.
- Entry IDs must stay unique, so re-capturing a path that is already in the
  loaded corpus is refused (logged, not appended) — the corpus accumulates
  distinct requests, and a deliberate re-capture of the same route means
  starting from a fresh file.
- The output path is inside the gitignored `corpus/` tree; nothing a capture
  run writes is a commit candidate until it is promoted to a reviewed fixture
  under `tools/diffharness/testdata/` (checklist above).

The gateway's internal capture middleware follows the same triggers —
autosave threshold, graceful-shutdown flush, and lossless reload after a
restart — as pinned by the durability tests in
`docs/capture_testing.md`.

### Replay Phase

```bash
# Differential replay against both incumbents
./seam-replay \
  --incumbent https://argocd-ro-ardenone-manager-ts.ardenone.com:8444 \
  --seam http://localhost:8080 \
  --corpus tools/diffharness/testdata/corpus-argocd.json
```

(A replay against a runtime capture points `--corpus` at that untracked
`corpus/<service>/corpus.json` instead; the checked-in fixture is what a
fresh clone can replay without capturing first.)

### Manual Corpus Creation

1. Copy `tools/diffharness/testdata/example-corpus.json` as starting point
2. Add entries following the schema
3. Validate JSON: `jq . corpus.json`
4. Test with `seam-replay`

## Future Enhancements

### Schema Versioning

**Current:** `seam-diff-corpus/v1`

**Planned additions:**
- v2: Response capture (currently request-only)
- v3: Multi-scenario entries (parameterized tests)
- v4: Streaming response capture

### Automation

**Planned tools:**
- `seam-capture-expand`: Auto-generate entries from OpenAPI specs
- `seam-corpus-lint`: Validate corpus files against schema
- `seam-corpus-merge`: Merge multiple corpus files

### Metadata Extensions

**Future fields:**
- Entry tags (functional, smoke, regression)
- Priority/weight (for subset testing)
- Correlation IDs (link related entries)

## References

- **SEAM Plan:** `docs/plan/plan.md`
- **Corpus Capture Integration:** `docs/design/argocd-ro-proxy-corpus-capture-integration.md`
- **Integrity checks and lifecycle:** `docs/capture_testing.md`
- **Corpus Package:** `tools/diffharness/internal/corpus/corpus.go`
- **Capture Tool:** `tools/diffharness/cmd/seam-capture/main.go`
- **Committed fixtures:** `tools/diffharness/testdata/` (runtime `corpus/` is git-ignored)

---

**End of Design Document**
