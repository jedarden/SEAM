# ArgoCD-RO Proxy Corpus Data Structure Design

## Document Metadata

- **Created:** 2026-07-27
- **Bead:** bf-49g8 (explore)
- **Status:** Design Complete
- **Related Beads:** 
  - bf-1d0k (setup capture mechanism)
  - bf-69n1 (argocd-ro proxy architecture)
  - bf-4qv5 (corpus capture validation)

## Overview

This document specifies the data structure for capturing HTTP request/response pairs from the ArgoCD read-only proxy. The corpus serves as the oracle for differential testing during service migration to SEAM.

## Design Principles

1. **Security-First:** Credentials stored as references only, never literal values
2. **Git-Tracked:** Corpus files are committed to version control for reproducibility
3. **Replayable:** Each entry can be replayed verbatim against both incumbent and SEAM
4. **Differential-Ready:** Structured for comparison between two HTTP responses
5. **Self-Documenting:** Human-readable descriptions and stable IDs

## JSON Schema Definition

### Top-Level Schema

```json
{
  "schema": "seam-diff-corpus/v1",
  "service": "argocd",
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
| `service` | string | Yes | Service name token. Must be `"argocd"` |
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
      "ref": "vault:seam/routes/argocd/ro-token",
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
  "ref": "vault:seam/routes/argocd/ro-token",
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

```
corpus/
├── README.md                           # Corpus capture documentation
├── capture-config.yaml                 # Global capture configuration
└── argocd-proxy/
    ├── README.md                       # ArgoCD-specific documentation
    ├── corpus.json                     # Primary corpus file
    ├── corpus-template.json            # Template with example entries
    ├── applications-list.json          # Per-endpoint capture files (optional)
    ├── clusters-list.json              # Per-endpoint capture files (optional)
    └── metadata/
        ├── capture-session.json        # Session metadata
        └── validation-report.json      # Validation results
```

### File Naming Conventions

**Primary corpus file:** `corpus.json`
- Single source of truth for differential testing
- Sorted by entry ID for stable diffs
- Git-tracked

**Per-endpoint captures:** `{endpoint}-{method}.json`
- Optional granular capture files
- Useful for debugging specific endpoints
- Merged into primary corpus before testing

**Template file:** `corpus-template.json`
- Example entries showing all fields
- Reference for manual corpus creation
- Not used in automated testing

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

**File:** `corpus/argocd-proxy/metadata/capture-session.json`

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
      "ref": "vault:seam/routes/argocd/ro-token",
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
- Secret references (e.g., `vault:seam/routes/argocd/ro-token`)
- Git-tracked corpus files
- Shared in pull requests

❌ **Never:**
- Literal credential values in corpus files
- Base64-encoded credentials (except request/response bodies)
- Personal access tokens or API keys

### Corpus Review Checklist

Before committing corpus files:

1. ✅ Verify all `secrets[].ref` fields use reference format
2. ✅ Check no literal bearer tokens in headers
3. ✅ Ensure response bodies don't leak credentials
4. ✅ Validate JSON syntax with `jq .`
5. ✅ Review descriptions for sensitive information

### Access Control

**Corpus files:**
- Mode: `0644` (readable by all, writable by owner)
- Git-tracked: Yes
- Encryption: No (plaintext JSON)

**Secret resolution:**
- Runtime: Memory-only resolution from local secrets source
- Source: `internal/secref` package
- Storage: Git-ignored local files

## Complete Example

```json
{
  "schema": "seam-diff-corpus/v1",
  "service": "argocd",
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
          "ref": "vault:seam/routes/argocd/ro-token",
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
          "ref": "vault:seam/routes/argocd/ro-token",
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
          "ref": "vault:seam/routes/argocd/ro-token",
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
5. **Secrets:** Valid reference format
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

### Replay Phase

```bash
# Differential replay against both incumbents
./seam-replay \
  --incumbent https://argocd-ro-ardenone-manager-ts.ardenone.com:8444 \
  --seam http://localhost:8080 \
  --corpus corpus/argocd-proxy/corpus.json
```

### Manual Corpus Creation

1. Copy `corpus-template.json` as starting point
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
- **Corpus Package:** `tools/diffharness/internal/corpus/corpus.go`
- **Capture Tool:** `tools/diffharness/cmd/seam-capture/main.go`
- **Configuration:** `corpus/capture-config.yaml`

---

**End of Design Document**
