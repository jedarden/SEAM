# ArgoCD-RO Proxy Corpus Capture Integration Design

## Document Metadata

- **Created:** 2026-07-27
- **Bead:** seam-5e9046fc (explore) — closed; rehydrated from retired bead-forge ID `bf-69n1`
- **Status:** Design — reconciled against the implementation 2026-09-25 (seam-e368ef50)
- **Related Beads:** seam-89d9a0f8 (setup capture mechanism), seam-89d9a0f8 (verify capture mechanism) — closed; rehydrated from retired bead-forge ID `bf-1d0k`

> **Bead ID provenance:** this document was written against the retired
> bead-forge (`bf-*`) store. The workspace was rehydrated into bead-rs on
> 2026-08-14 (commit 9e9b514); each reference above now names its current
> `seam-*` bead, with the original `bf-*` ID retained as provenance. All
> linked beads are closed — they record design-era context, not open work.

> **Implementation reconciliation (2026-09-25, seam-e368ef50):** this design
> predates several subsystems it now depends on and has been refreshed to
> match the code. The route-fragment system is **implemented** (fragment merge
> mode, per-service fragment directories, hot reload — see the
> [Fragment directory and hot-reload
> scope](../../README.md#fragment-directory-and-hot-reload-scope) section of
> the README); the internal capture middleware is **implemented**
> (`--capture-enabled`, wrapped into the dispatch path); and `seam-replay`
> differential testing is **implemented**, with the cutover workflow that
> gates on it documented in [`../migration-runbook.md`](../migration-runbook.md).
> Paths, endpoint lists, statuses, and workflow references below were verified
> against the tree at the commit carrying this edit.

## Executive Summary

This document explores the ArgoCD read-only proxy implementation within SEAM and identifies integration points for the corpus capture mechanism. The corpus capture system enables differential testing by recording HTTP request/response pairs from the incumbent ArgoCD proxy before migrating services to SEAM.

## Current Architecture Overview

### 1. SEAM Server Structure

The SEAM gateway (`internal/server/server.go`) currently implements:

- **Dual-listener architecture:**
  - Caller-facing port (default 8080) - for external clients
  - Operator-only port (default 8081) - for administrative operations

- **Control-plane endpoints** (reserved paths that short-circuit route-table lookup):
  - *Caller listener:* `/openapi.json` - OpenAPI spec serving; `/docs`, `/docs/route`, `/docs/paths` - API documentation; `/_seam/health` (alias) and `/_seam/healthz` - Kubernetes liveness probe; `/_seam/readyz` - Kubernetes readiness probe; `/whoami`, `/scopes` - identity and scope introspection; `/api/v1/tailscale/ephemeral-key` - ephemeral key issuance; `/changes` - version migration endpoints; plus the catch-all dispatch handler for upstream proxying
  - *Operator listener:* `/_seam/metrics` - Prometheus metrics; `/config/status` - configuration fragment status; `/_seam/capture/save`, `/_seam/capture/status` - corpus flush and capture status; `/_seam/cache/status`, `/_seam/cache/cleanup` - response-cache operations; `/health/credentials`, `/health/upstreams` - health sentinels (operator-tier endpoints behind `seam:ops:read`)

- **Route-fragment system (implemented):** In fragment mode (`--fragment-mode` /
  `SEAM_FRAGMENT_MODE`) every route SEAM serves is merged from a single
  fragments directory resolved once at startup (`--fragments-dir` /
  `SEAM_FRAGMENTS_DIR`, default `./fragments` — one subdirectory per service,
  e.g. `fragments/argocd-ro/`). `--enable-hot-reload` /
  `SEAM_HOT_RELOAD_ENABLED` adds a file-watch reload that re-walks the tree,
  re-merges, and atomically swaps the route table (in-flight requests finish
  on the old table); schema validation runs at startup only and `seam lint`
  remains the structural gate. See the
  [Fragment directory and hot-reload
  scope](../../README.md#fragment-directory-and-hot-reload-scope) section of
  the README for the authoritative description.

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

**Location:** `corpus/argocd-proxy/corpus.json` — a **runtime capture path,
gitignored** (`/corpus/` in `.gitignore` since the 2026-09-18 history purge,
seam-70ae655e / commit 9984a5b). A capture run writes here and the file stays
out of git. The corpora that *are* committed are the reviewed fixtures under
`tools/diffharness/testdata/` (`corpus-argocd.json`, `example-corpus.json`);
promoting a runtime capture into that directory is a deliberate, reviewed act
(see the review checklist in
[`argocd-ro-corpus-data-structure.md`](argocd-ro-corpus-data-structure.md)).

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
          "ref": "vault:rs-manager/rs-manager/seam/routes/argocd/ro-token",
          "injectAs": {"kind": "bearer"}
        }
      ]
    }
  ]
}
```

**Security Model:**
- Credentials stored as **references only** (e.g., `vault:rs-manager/rs-manager/seam/routes/argocd/ro-token`)
- Never literal values in corpus files
- Checked-in fixtures (`tools/diffharness/testdata/*.json`) are refs-only and safe to commit after review; runtime captures under `corpus/` are gitignored and never committed
- Literal values resolved at replay-time from local secrets source

#### 3. Control Scripts

**Location:** `scripts/capture-argocd.sh`

**Operations:**
- `start` - Launch capture proxy on port 8082
- `stop` - Gracefully stop and save corpus
- `status` - Show capture status and entry count
- `restart` - Stop/start cycle

**Supported capture/restart lifecycle:**

- **Persistence is automatic once started:** an autosave lands every 10
  entries, and a graceful `stop` flushes the corpus. An ungraceful kill loses
  at most the entries captured since the last autosave.
- **Restart appends, never truncates.** On start the tool loads an existing
  corpus file and appends to it; `capturedAt` stays pinned to the first
  capture. A corpus whose `service` token differs from `--service` is
  refused rather than mixed; an incumbent-URL change logs a warning.
- **Entry IDs are unique**, so re-capturing a path already in the loaded
  corpus is refused (logged, not appended). A deliberate re-capture of the
  same routes means starting from a fresh corpus file.
- **Capture-disabled mode is a transparent forwarding path** — requests
  reach the incumbent, nothing is recorded.
- **Everything a capture run writes lands under the gitignored `corpus/`
  tree** and stays out of git; only a reviewed promotion into
  `tools/diffharness/testdata/` becomes a committed fixture.

The gateway's internal capture middleware follows the same triggers
(autosave threshold, graceful-shutdown flush, lossless reload after
restart), pinned by the durability tests catalogued in
[`docs/capture_testing.md`](../capture_testing.md).

**Configuration:**
- `SEAM_ARGOCD_INCUMBENT_URL` - Override incumbent URL
- `SEAM_CAPTURE_PORT` - Override listen port

### Current Corpus Status

The corpora captured in July 2026 were removed from git by the 2026-09-18
history purge (seam-70ae655e / commit 9984a5b) and the `corpus/` path was
gitignored. What exists today:

- **Committed fixtures** (replayable from a fresh clone):
  `tools/diffharness/testdata/corpus-argocd.json` — the deployed `argocd-ro`
  capture — plus `example-corpus.json`.
- **Runtime captures:** none checked in; a fresh capture run writes to
  `corpus/argocd-proxy/corpus.json`, which stays untracked.

**Capture Configuration:**
- Incumbent: `https://argocd-ro-ardenone-manager-ts.ardenone.com:8444`
- Listen port: 8082
- Corpus path: `corpus/argocd-proxy/corpus.json` (runtime, gitignored)

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

### Option 2: SEAM Internal Middleware (Implemented)

**Implementation:** Middleware within SEAM's HTTP handlers — built in the
server constructor when capture is enabled and wrapped around the caller-facing
dispatch handler (`internal/server/server.go`, dispatch path:
`callerHandler = s.captureMiddleware.Wrap(callerHandler)`).

**Configuration:**
- `--capture-enabled` / `SEAM_CAPTURE_ENABLED` - enable capture (default off)
- `--corpus-dir` / `SEAM_CORPUS_DIR` - corpus directory (default `corpus`,
  gitignored); the middleware writes `<corpus-dir>/corpus.json`

**Behavior:**
1. An existing corpus is loaded at startup (`Load`), so a restart appends
   rather than truncates
2. Requests through the dispatch path are recorded and forwarded; responses
   are captured and appended to the corpus
3. An autosave lands every 10 entries; `Server.Shutdown` flushes the corpus
4. `/_seam/capture/save` and `/_seam/capture/status` (operator listener)
   expose a manual flush and the live entry count

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

**Example:** `vault:rs-manager/rs-manager/seam/routes/argocd/ro-token`

The path is written against SEAM's enforced base
`rs-manager/rs-manager/seam/routes` (`internal/spec/allowlist.go`
`DefaultVaultBaseDir`; `SEAM_VAULT_BASE_DIR` overrides). The earlier
cluster-agnostic base `seam/routes` is **retired** (consolidated 2026-09-04) —
a capture referencing it resolves outside the enforced prefix and fails
validation at replay.

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
4. Promote the reviewed capture into the committed fixtures under
   `tools/diffharness/testdata/` (runtime captures under `corpus/` are
   gitignored and are not themselves committed)

**Status:** ✅ Complete - Basic corpus captured and re-homed as the checked-in
fixture `tools/diffharness/testdata/corpus-argocd.json` after the 2026-09-18
history purge removed the original from git

### Phase 2: Corpus Expansion

**Goal:** Expand corpus to cover all ArgoCD operations

**Actions:**
1. Identify missing API routes (sync, manifest, repositories)
2. Capture additional operations
3. Add expected response metadata
4. Populate secret references

**Status:** ⏳ Pending — the committed fixture `corpus-argocd.json` holds two
entries (`list-apps-get`, `get-app-myapp-get`); sync, manifest, and
repository routes are not yet captured

### Phase 3: SEAM Implementation

**Goal:** Implement SEAM route fragment for argocd-ro

**Actions:**
1. Create OpenAPI fragment for argocd routes
2. Implement route handler in SEAM
3. Configure secret injection
4. Test with corpus replay

**Status:** 🟡 Partially complete — `fragments/argocd-ro/1-argocd-read-only-proxy.yaml`
is authored (applications list/get, clusters list). Route serving comes from
the implemented fragment system; credential curation (`x-vault-path` /
`x-inject-as`) and the remaining routes are still pending

### Phase 4: Differential Testing

**Goal:** Validate SEAM responses match incumbent

**Actions:**
1. Run `seam-replay` against both incumbents
2. Compare responses
3. Fix any discrepancies
4. Ensure all corpus entries pass

**Status:** 🟡 Tooling shipped — `seam-replay`
(`tools/diffharness/cmd/seam-replay`) replays a corpus against the incumbent
and SEAM with differential comparison and leak detection, and the cutover
workflow in [`../migration-runbook.md`](../migration-runbook.md) gates on its
pass report (hard gate). A live differential run against the deployed
`argocd-ro` proxy is still pending

## Security Considerations

### Corpus File Security

- ✅ Corpus files contain **only secret references**, not values
- ✅ Committed fixtures (`tools/diffharness/testdata/*.json`) are safe to commit after review; runtime captures under `corpus/` are gitignored and stay out of git entirely
- ✅ Can be shared without credential exposure
- ✅ Review process (plus the loader's enforced vault-base validation) ensures no accidental credential leakage

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
   - Validate JSON: `cat corpus/argocd-proxy/corpus.json | jq` (runtime path, untracked)
   - Check write permissions

## Next Steps

1. ✅ **Complete exploration of current architecture** (this document)
2. ⏳ **Expand corpus coverage** - Capture additional ArgoCD API routes
3. 🟡 **Complete the SEAM argocd route fragment** - `fragments/argocd-ro/`
   authored; credential curation and remaining routes pending
4. ✅ **Implement seam-replay differential testing** - shipped, with the
   cutover workflow documented in [`../migration-runbook.md`](../migration-runbook.md)
5. ⏳ **Validate corpus passes against SEAM implementation** - the live
   differential run that gates cutover

## References

- **SEAM Server:** `internal/server/server.go`
- **Spec Loader:** `internal/spec/loader.go` (fragment load/merge/lint in `internal/spec/`)
- **Hot Reload:** `internal/server/hot_reload.go` (route-table swap on fragment changes)
- **Capture Tool:** `tools/diffharness/cmd/seam-capture/main.go`
- **Replay Tool:** `tools/diffharness/cmd/seam-replay/main.go`
- **Corpus Package:** `tools/diffharness/internal/corpus/corpus.go`
- **Control Scripts:** `scripts/capture-argocd.sh`
- **Committed fixtures:** `tools/diffharness/testdata/` (runtime captures under `corpus/` are gitignored)
- **Integrity checks and lifecycle:** `docs/capture_testing.md`
- **Cutover workflow:** `docs/migration-runbook.md` (`seam-replay` pass report as the hard gate; `seam-cutover` go/no-go runner)
- **Fragment mode and hot-reload scope:** README, [Fragment directory and hot-reload scope](../../README.md#fragment-directory-and-hot-reload-scope)

---

**End of Design Document**
