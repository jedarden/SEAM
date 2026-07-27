# ArgoCD Proxy Corpus Capture

This directory contains the corpus (request/response pairs) captured from the ArgoCD read-only proxy.

## Capture Mechanism

The corpus is captured using `seam-capture`, a differential capture proxy that sits in front of the incumbent ArgoCD proxy and records all HTTP traffic.

## Quick Start

### Start Capture

```bash
./scripts/capture-argocd.sh start
```

This starts the capture proxy on port 8082, forwarding requests to the incumbent ArgoCD proxy at `https://argocd-ro-ardenone-manager-ts.ardenone.com:8444`.

### Make Test Requests

```bash
# List all applications
curl -sk http://localhost:8082/api/v1/applications

# List all clusters
curl -sk http://localhost:8082/api/v1/clusters

# Get specific application
curl -sk http://localhost:8082/api/v1/applications/myapp

# Get application sync status
curl -sk http://localhost:8082/api/v1/applications/myapp/sync

# Get application manifest
curl -sk http://localhost:8082/api/v1/applications/myapp/manifest
```

### Stop Capture

```bash
./scripts/capture-argocd.sh stop
```

The corpus will be saved to `corpus.json` with all captured request/response pairs.

### Check Status

```bash
./scripts/capture-argocd.sh status
```

## Configuration

The capture behavior can be configured via environment variables:

- `SEAM_ARGOCD_INCUMBENT_URL` - The ArgoCD proxy URL (default: https://argocd-ro-ardenone-manager-ts.ardenone.com:8444)
- `SEAM_CAPTURE_PORT` - The listen port for the capture proxy (default: 8082)

Example:
```bash
SEAM_CAPTURE_PORT=9999 ./scripts/capture-argocd.sh start
```

## Corpus Format

The corpus is stored in JSON format with the following structure:

```json
{
  "schema": "seam-diff-corpus/v1",
  "service": "argocd",
  "incumbent": "https://argocd-ro-ardenone-manager-ts.ardenone.com:8444",
  "capturedAt": "2026-07-27T12:00:00Z",
  "description": "ArgoCD read-only proxy corpus captured from production",
  "entries": [
    {
      "id": "list-apps-get",
      "description": "List all applications",
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
      ]
    }
  ]
}
```

## Important ArgoCD API Routes

Based on the current ArgoCD proxy usage, these are the key routes to capture:

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

## Notes

- The capture proxy automatically handles authentication injection
- Request bodies are base64-encoded in the corpus
- The corpus is git-tracked and serves as the oracle for differential testing
- All credentials are stored as references (e.g., `vault:seam/routes/argocd/ro-token`), never as literal values
