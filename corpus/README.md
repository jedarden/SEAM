# SEAM Corpus Capture Infrastructure

This directory contains the corpus capture infrastructure for SEAM. The corpus captures HTTP request/response pairs from incumbent proxies to enable differential testing during service migration to SEAM.

## Overview

The corpus capture mechanism allows recording real traffic from existing proxies (incumbents) before migrating those services to SEAM. This captured corpus serves as the oracle for differential testing - when a SEAM route is implemented, we replay the corpus against both the incumbent and SEAM to ensure they produce equivalent responses.

## Architecture

```
┌─────────────────┐     ┌──────────────┐     ┌──────────────────┐
│  Test/Agent     │────▶│ seam-capture │────▶│ Incumbent Proxy │
│  (curl, etc.)   │     │  (localhost) │     │  (argocd-ro,     │
└─────────────────┘     └──────────────┘     │   kubectl-proxy) │
         │                      │              └──────────────────┘
         │                      │                       │
         ▼                      ▼                       │
    Response             ┌─────────────┐               │
                         │ Corpus File │               │
                         │ (JSON)      │               │
                         └─────────────┘               │
                                                      ▼
                                                 Response
```

## Components

### 1. seam-capture Binary
The capture proxy that records HTTP traffic.

**Location:** `./seam-capture` (built from `tools/diffharness/cmd/seam-capture`)

**Build:**
```bash
cd tools/diffharness
go build -o ../../seam-capture ./cmd/seam-capture/main.go
```

### 2. Capture Scripts
Helper scripts for managing capture sessions.

**Location:** `scripts/capture-argocd.sh`

**Usage:**
```bash
./scripts/capture-argocd.sh start   # Start capture
./scripts/capture-argocd.sh stop    # Stop and save corpus
./scripts/capture-argocd.sh status  # Check capture status
```

### 3. Configuration
Per-service configuration for capture settings.

**Location:** `corpus/capture-config.yaml`

### 4. Corpus Files
Captured request/response pairs in structured JSON format.

**Location:** `corpus/<service>/corpus.json`

## Quick Start

### 1. Build the Capture Tool

```bash
cd tools/diffharness
go build -o ../../seam-capture ./cmd/seam-capture/main.go
cd ../..
```

### 2. Start Capture for ArgoCD

```bash
./scripts/capture-argocd.sh start
```

### 3. Make Test Requests

```bash
curl -sk http://localhost:8082/api/v1/applications | jq
curl -sk http://localhost:8082/api/v1/clusters | jq
```

### 4. Stop Capture

```bash
./scripts/capture-argocd.sh stop
```

### 5. Review Captured Corpus

```bash
cat corpus/argocd-proxy/corpus.json | jq
```

## Enabling/Disabling Capture

The capture mechanism can be enabled or disabled in several ways:

### Method 1: Configuration File

Edit `corpus/capture-config.yaml`:

```yaml
services:
  argocd:
    enabled: true  # Set to false to disable
```

### Method 2: Environment Variable

```bash
# Override settings without editing config
export SEAM_CAPTURE_ENABLED=true
./scripts/capture-argocd.sh start
```

### Method 3: Manual Control

Simply don't start the capture script. When the capture proxy is not running, normal operation is unaffected.

## Corpus Format

Each corpus file contains:

```json
{
  "schema": "seam-diff-corpus/v1",
  "service": "argocd",
  "incumbent": "https://argocd-ro-proxy.example.com",
  "capturedAt": "2026-07-27T12:00:00Z",
  "description": "Human-readable description",
  "entries": [
    {
      "id": "list-apps-get",
      "description": "What request exercises",
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
      ],
      "expect": {
        "ignoreHeaders": ["Date", "Server"]
      }
    }
  ]
}
```

## Key Features

### 1. Non-Intrusive Capture
- Transparent proxy - no modification to requests/responses
- Can be enabled/disabled without affecting normal operations
- Runs on separate port from production services

### 2. Credential Safety
- Credentials stored as references (e.g., `vault:seam/routes/argocd/ro-token`)
- Never literal values in corpus files
- Git-tracked corpus files are safe to commit

### 3. Flexible Configuration
- Per-service settings
- Configurable ports and paths
- Auto-save intervals to prevent data loss

### 4. Comprehensive Capture
- Full HTTP request (method, path, headers, body)
- Full HTTP response (status, headers, body)
- Timestamp and metadata

## Usage in Development Workflow

### Phase: Pre-Migration
1. Enable capture for target service
2. Run production traffic through capture proxy
3. Build representative corpus of typical requests
4. Commit corpus to repository

### Phase: SEAM Implementation
1. Implement SEAM route fragment
2. Run differential replay: `./seam-replay --incumbent <url> --seam <url> --corpus <file>`
3. Fix any differences between incumbent and SEAM responses
4. Ensure corpus passes

### Phase: Validation
1. Corpus must pass (green) before service cutover
2. Corpus serves as acceptance criteria for migration
3. Failed corpus = migration not complete

## Important Notes

### Security
- Corpus files are git-tracked and should be reviewed before committing
- Ensure no sensitive data leaks (secrets should be references only)
- Check response bodies for accidental credential exposure

### Performance
- Capture adds minimal latency (single-hop proxy)
- Auto-save every N requests prevents large in-memory buffers
- Body size limits prevent memory exhaustion

### Reliability
- Corpus is saved on graceful shutdown
- Capture proxy can be stopped/restarted without data loss
- Existing corpus is loaded and extended on restart

## Troubleshooting

### Capture Proxy Won't Start
- Check if port is already in use: `lsof -i :8082`
- Verify binary exists: `ls -lh seam-capture`
- Check logs: `cat /tmp/seam-capture.log`

### No Traffic Captured
- Verify requests go to capture proxy port (8082, not 8080)
- Check incumbent URL is accessible
- Ensure capture proxy is running: `./scripts/capture-argocd.sh status`

### Corpus File Empty/Invalid
- Stop capture gracefully: `./scripts/capture-argocd.sh stop`
- Check for syntax errors: `cat corpus/argocd-proxy/corpus.json | jq`
- Verify write permissions: `ls -la corpus/argocd-proxy/`

## Future Enhancements

- Automatic corpus generation from OpenAPI specs
- Corpus validation and linting
- Differential reporting with detailed diff output
- Corpus versioning and migration support
