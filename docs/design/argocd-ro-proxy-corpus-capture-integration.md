# ArgoCD-RO Proxy Corpus Capture Integration Design

## Document Metadata

- **Created:** 2026-07-27
- **Bead:** bf-69n1 (explore)
- **Status:** Exploration/Design
- **Related Beads:** bf-1d0k (setup capture mechanism), bf-1d0k (verify capture mechanism)

## Executive Summary

This document explores the ArgoCD read-only proxy implementation within SEAM and identifies integration points for the corpus capture mechanism. The corpus capture system enables differential testing by recording HTTP request/response pairs from the incumbent ArgoCD proxy before migrating services to SEAM.

## Current Architecture Overview

### 1. SEAM Server Structure

The SEAM gateway (`internal/server/server.go`) currently implements:

- **Dual-listener architecture:**
  - Caller-facing port (default 8080) - for external clients
  - Operator-only port (default 8081) - for administrative operations

- **Control-plane endpoints** (reserved paths that short-circuit route-table lookup):
  - `/openapi.json` - OpenAPI spec serving
  - `/docs` - API documentation
  - `/_seam/healthz` - Kubernetes liveness probe
  - `/_seam/readyz` - Kubernetes readiness probe
  - `/_seam/metrics` - Prometheus metrics
  - `/config/status` - Configuration fragment status

- **Route-fragment system (planned):** Dynamic route loading from OpenAPI fragments

### 2. Incumbent ArgoCD Proxy

The current ArgoCD read-only proxy is accessed via:
- **Endpoint:** `https://argocd-ro-ardenone-manager-ts.ardenone.com:8444`
- **Authentication:** Injected bearer token (read-only)
- **Access:** Tailscale VPN entrypoint only

**Key API Routes Used:**

1. **Applications**
   - `GET /api/v1/applications` - List all applications
   - `GET /api/v1/applications/{name}` - Get specific application
   - `GET /api/v1/applications/{name}/sync` - Get sync status
   - `GET /api/v1/applications/{name}/manifest` - Get application manifest

2. **Clusters**
   - `GET /api/v1/clusters` - List all clusters
   - `GET /api/v1/clusters/{name}` - Get specific cluster details

3. **Repositories**
   - `GET /api/v1/repositories` - List repositories
   - `GET /api/v1/repositories/{url}` - Get specific repository

## Corpus Capture Mechanism

### Architecture

```
┌─────────────────┐     ┌──────────────┐     ┌──────────────────┐
│  Test/Agent     │────▶│ seam-capture │────▶│ Incumbent Proxy │
│  (curl, etc.)   │     │  (localhost) │     │  (argocd-ro)     │
└─────────────────┘     └──────────────┘     └──────────────────┘
         │                      │                       │
         │                      ▼                       │
         │                ┌─────────────┐              │
         │                │ Corpus File │              │
         │                │ (JSON)      │              │
         │                └─────────────┘              │
         ▼                                              ▼
    Response                                     Response
```

### Components

#### 1. seam-capture Binary

**Location:** `./seam-capture` (built from `tools/diffharness/cmd/seam-capture/main.go`)

**Key Features:**
- Transparent reverse proxy using `httputil.NewSingleHostReverseProxy`
- Captures all HTTP request/response pairs
- Records to corpus JSON file with unique entry IDs
- Periodic auto-save (every 10 entries) to prevent data loss
- Graceful shutdown with corpus save

**Capture Flow:**

```go
1. Incoming request → captureHandler
2. Parse request (method, path, headers, body)
3. Forward to incumbent via httputil.ReverseProxy
4. Capture response (status, headers, body)
5. Append entry to corpus
6. Return response to client
```

#### 2. Corpus Format

**Schema Version:** `seam-diff-corpus/v1`

**Location:** `corpus/argocd-proxy/corpus.json`

**Structure:**
```json
{
  "schema": "seam-diff-corpus/v1",
  "service": "argocd",
  "incumbent": "https://argocd-ro-ardenone-manager-ts.ardenone.com:8444",
  "capturedAt": "2026-07-27T12:00:00Z",
  "description": "ArgoCD read-only proxy corpus",
  "entries": [
    {
      "id": "api-v1-applications-get",
      "description": "GET /api/v1/applications",
      "request": {
        "method": "GET",
        "path": "/api/v1/applications",
        "query": "",
        "headers": {"Accept": ["application/json"]},
        "bodyB64": ""
      },
      "secrets": [
        {
          "ref": "vault:seam/routes/argocd/ro-token",
          "injectAs": {"kind": "bearer"}
        }
      ]
    }
  ]
}
```

**Security Model:**
- Credentials stored as **references only** (e.g., `vault:seam/routes/argocd/ro-token`)
- Never literal values in corpus files
- Git-tracked corpus files are safe to commit
- Literal values resolved at replay-time from local secrets source

#### 3. Control Scripts

**Location:** `scripts/capture-argocd.sh`

**Operations:**
- `start` - Launch capture proxy on port 8082
- `stop` - Gracefully stop and save corpus
- `status` - Show capture status and entry count
- `restart` - Stop/start cycle

**Configuration:**
- `SEAM_ARGOCD_INCUMBENT_URL` - Override incumbent URL
- `SEAM_CAPTURE_PORT` - Override listen port

### Current Corpus Status

**Captured Entries (as of 2026-07-27):**
1. `api-v1-applications-get` - List all applications
2. `api-v1-clusters-get` - List all clusters

**Capture Configuration:**
- Incumbent: `https://argocd-ro-ardenone-manager-ts.ardenone.com:8444`
- Listen port: 8082
- Corpus path: `corpus/argocd-proxy/corpus.json`

## Request/Response Flow Analysis

### Current Incumbent Flow (Before SEAM)

```
Client → ArgoCD-RO Proxy → ArgoCD API Server → Response
                │
                └─ Injects bearer token
```

### Capture Phase Flow

```
Client → seam-capture → ArgoCD-RO Proxy → ArgoCD API Server → Response
         │                    │
         └─ Record to corpus └─ Injects bearer token
```

### Future SEAM Flow (After Migration)

```
Client → SEAM Gateway → Route Fragment → ArgoCD API Server → Response
                  │
                  └─ Inject secrets from Vault
```

### Differential Replay Flow

```
seam-replay → Incumbent Proxy → Response (recorded as baseline)
      │
      └─→ SEAM Gateway → Response (compared to baseline)
```

## Integration Points for Capture Middleware

### Option 1: Standalone Capture Proxy (Current Implementation)

**Implementation:** External process on separate port

**Pros:**
- Non-intrusive to SEAM server code
- Easy enable/disable
- Can run independently of SEAM
- Safe for production use

**Cons:**
- Separate process to manage
- Port conflict potential
- Not integrated into SEAM's lifecycle

**Architecture:**
```
:8080 (SEAM)     :8082 (seam-capture)     :8444 (argocd-ro)
   │                    │                        │
   └─ Control plane ───┴────────────────────────▶
```

### Option 2: SEAM Internal Middleware (Future Enhancement)

**Implementation:** Middleware within SEAM's HTTP handlers

**Integration Point:** In `setupRoutes()` or as middleware wrapper

**Code Location:** `internal/server/server.go:99-109`

**Example Integration:**
```go
// In setupRoutes()
captureMiddleware := NewCaptureMiddleware(corpusPath)
s.callerMux.Handle("/", captureMiddleware.Wrap(s.routeHandler))

// Capture middleware would:
// 1. Intercept request before routing
// 2. Record request details
// 3. Pass through to route handler
// 4. Capture response
// 5. Append to corpus
```

**Pros:**
- Integrated into SEAM lifecycle
- Single process
- Can capture SEAM's own responses
- No port management

**Cons:**
- More complex implementation
- Requires changes to core server
- Potential performance impact on production

## Data Flow Through Proxy

### Stage 1: Request Capture

```
Incoming Request
    ↓
Extract components:
  - Method (GET, POST, etc.)
  - Path (/api/v1/applications)
  - Query string
  - Headers (canonicalized)
  - Body (base64-encoded)
    ↓
Create Request struct
    ↓
```

### Stage 2: Forwarding

```
Request struct
    ↓
httputil.NewSingleHostReverseProxy(target)
    ↓
Forward to incumbent
    ↓
Response received
```

### Stage 3: Response Recording

```
Response
    ↓
Extract components:
  - Status code
  - Headers
  - Body
    ↓
Create Entry struct
    ↓
Append to corpus.Entries
    ↓
Periodic save (every 10 entries)
```

## Key Design Decisions

### 1. Corpus Entry ID Generation

**Current:** `entryID(r)` generates ID from method + path
```go
path := strings.Trim(r.URL.Path, "/")
return strings.ToLower(strings.ReplaceAll(path, "/", "-")) + "-" + strings.ToLower(r.Method)
```

**Result:** `/api/v1/applications` + `GET` → `api-v1-applications-get`

### 2. Secret Reference Format

**Pattern:** `vault:<path-to-secret>`

**Example:** `vault:seam/routes/argocd/ro-token`

**Injection Kind:** `bearer` for ArgoCD

### 3. Header Canonicalization

All header keys are canonicalized using `http.CanonicalHeaderKey` to ensure case-insensitive comparison.

### 4. Body Encoding

Request/response bodies are base64-encoded to handle binary data and ensure JSON compatibility.

## Integration Approach Recommendations

### Phase 1: Pre-Migration (Current)

**Goal:** Build comprehensive corpus before implementing SEAM routes

**Actions:**
1. Run capture proxy in production-like environment
2. Execute typical client operations against capture proxy
3. Build representative corpus
4. Commit corpus to repository

**Status:** ✅ Complete - Basic corpus captured (2 entries)

### Phase 2: Corpus Expansion

**Goal:** Expand corpus to cover all ArgoCD operations

**Actions:**
1. Identify missing API routes (sync, manifest, repositories)
2. Capture additional operations
3. Add expected response metadata
4. Populate secret references

**Status:** ⏳ Pending

### Phase 3: SEAM Implementation

**Goal:** Implement SEAM route fragment for argocd-ro

**Actions:**
1. Create OpenAPI fragment for argocd routes
2. Implement route handler in SEAM
3. Configure secret injection
4. Test with corpus replay

**Status:** ⏳ Pending

### Phase 4: Differential Testing

**Goal:** Validate SEAM responses match incumbent

**Actions:**
1. Run `seam-replay` against both incumbents
2. Compare responses
3. Fix any discrepancies
4. Ensure all corpus entries pass

**Status:** ⏳ Pending

## Security Considerations

### Corpus File Security

- ✅ Corpus files contain **only secret references**, not values
- ✅ Safe to commit to git
- ✅ Can be shared without credential exposure
- ✅ Review process ensures no accidental credential leakage

### Capture Proxy Security

- ⚠️ Currently runs on localhost only
- ⚠️ No authentication required (assumes trusted network)
- ✅ No credential storage in capture proxy
- ✅ Credentials injected at replay-time only

## Performance Considerations

### Capture Overhead

- **Latency:** Single-hop proxy adds ~1-2ms per request
- **Memory:** In-memory corpus until periodic save
- **Disk:** JSON corpus grows linearly with captured entries
- **Network:** No additional network calls beyond forwarding

### Replay Performance

- **Concurrent execution:** seam-replay hits both incumbents in parallel
- **Comparison time:** Linear with response size
- **Corpus size:** Thousands of entries supported

## Troubleshooting Guide

### Common Issues

1. **Capture proxy won't start**
   - Check port availability: `lsof -i :8082`
   - Verify binary: `ls -lh seam-capture`
   - Check logs: `cat /tmp/seam-capture.log`

2. **No traffic captured**
   - Verify requests go to port 8082 (not 8080)
   - Check incumbent URL accessibility
   - Verify capture process running

3. **Corpus file empty/invalid**
   - Stop capture gracefully: `./scripts/capture-argocd.sh stop`
   - Validate JSON: `cat corpus/argocd-proxy/corpus.json | jq`
   - Check write permissions

## Next Steps

1. ✅ **Complete exploration of current architecture** (this document)
2. ⏳ **Expand corpus coverage** - Capture additional ArgoCD API routes
3. ⏳ **Implement SEAM argocd route fragment**
4. ⏳ **Implement seam-replay differential testing**
5. ⏳ **Validate corpus passes against SEAM implementation**

## References

- **SEAM Server:** `internal/server/server.go`
- **Spec Loader:** `internal/spec/loader.go`
- **Capture Tool:** `tools/diffharness/cmd/seam-capture/main.go`
- **Corpus Package:** `tools/diffharness/internal/corpus/corpus.go`
- **Control Scripts:** `scripts/capture-argocd.sh`
- **Corpus Files:** `corpus/argocd-proxy/`

---

**End of Design Document**
