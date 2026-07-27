# ArgoCD Proxy Routes Catalog (Bead bf-3w8n)

## Summary

Catalog of all ArgoCD read-only proxy routes currently used by agents, discovered via codebase grep analysis.

## Base Endpoint

- **Proxy URL**: `https://argocd-ro-ardenone-manager-ts.ardenone.com:8444`
- **Authentication**: None required — proxy injects read-only bearer token
- **Access**: Tailscale VPN only (not public internet)
- **Permissions**: Read-only — applications, projects, clusters, repositories, logs

---

## Routes Currently Documented and Used

### 1. List All Applications

**HTTP Method**: `GET`  
**Path**: `/api/v1/applications`  
**Full URL**: `https://argocd-ro-ardenone-manager-ts.ardenone.com:8444/api/v1/applications`

**Usage Pattern**:
```bash
curl -sk https://argocd-ro-ardenone-manager-ts.ardenone.com:8444/api/v1/applications
```

**Grep Pattern**:
```bash
grep -r "api/v1/applications" --include="*.md" . | grep -v node_modules
```

**Where Used**:
- `/home/coding/CLAUDE.md` (primary documentation)
- `docs/notes/route-fragment-schema.md` (SEAM route fragment example)
- `tools/diffharness/testdata/corpus-argocd.json` (test corpus)
- `tools/diffharness/README.md` (example usage)

**Expected Response Structure**:
```json
{
  "items": [
    {
      "metadata": {
        "name": "app-name",
        "namespace": "namespace",
        "uid": "uid",
        "resourceVersion": "version"
      },
      "spec": {
        "source": {
          "repoURL": "https://github.com/org/repo",
          "targetRevision": "branch",
          "path": "k8s/"
        },
        "destination": {
          "server": "https://kubernetes.default.svc",
          "namespace": "namespace"
        },
        "project": "default"
      },
      "status": {
        "health": {"status": "Healthy"},
        "sync": {"status": "Synced"},
        "operationState": {}
      }
    }
  ]
}
```

---

### 2. Get Specific Application Status

**HTTP Method**: `GET`  
**Path**: `/api/v1/applications/{app-name}`  
**Full URL**: `https://argocd-ro-ardenone-manager-ts.ardenone.com:8444/api/v1/applications/{app-name}`

**Usage Pattern**:
```bash
curl -sk https://argocd-ro-ardenone-manager-ts.ardenone.com:8444/api/v1/applications/<app-name>
```

**Grep Pattern**:
```bash
grep -r "applications/<app-name>\|applications/{app-name}\|applications/myapp" --include="*.md" --include="*.json" . | grep -v node_modules
```

**Where Used**:
- `/home/coding/CLAUDE.md` (primary documentation)
- `tools/diffharness/testdata/corpus-argocd.json` (test corpus entry: "get-app-myapp-get")
- `tools/diffharness/README.md` (example usage)

**Expected Response Structure**:
```json
{
  "metadata": {
    "name": "myapp",
    "namespace": "default",
    "uid": "uid",
    "resourceVersion": "version"
  },
  "spec": {
    "source": {
      "repoURL": "https://github.com/org/repo",
      "targetRevision": "main",
      "path": "k8s/"
    },
    "destination": {
      "server": "https://kubernetes.default.svc",
      "namespace": "default"
    },
    "project": "default"
  },
  "status": {
    "health": {"status": "Healthy"},
    "sync": {
      "status": "Synced",
      "revision": "revision-hash"
    },
    "history": [
      {
        "revision": "revision-hash",
        "deployedAt": "2024-01-01T00:00:00Z"
      }
    ],
    "operationState": {
      "phase": "Succeeded",
      "message": "Successfully synced"
    }
  }
}
```

---

### 3. List Clusters

**HTTP Method**: `GET`  
**Path**: `/api/v1/clusters`  
**Full URL**: `https://argocd-ro-ardenone-manager-ts.ardenone.com:8444/api/v1/clusters`

**Usage Pattern**:
```bash
curl -sk https://argocd-ro-ardenone-manager-ts.ardenone.com:8444/api/v1/clusters
```

**Grep Pattern**:
```bash
grep -r "api/v1/clusters" --include="*.md" --include="*.json" . | grep -v node_modules
```

**Where Used**:
- `/home/coding/CLAUDE.md` (primary documentation)
- `corpus/argocd-proxy/clusters-list.json` (empty corpus file)

**Expected Response Structure**:
```json
{
  "items": [
    {
      "metadata": {
        "name": "cluster-name",
        "namespace": "argocd",
        "uid": "uid"
      },
      "spec": {},
      "status": {
        "server": "https://kubernetes.default.svc",
        "connectionState": {
          "status": "Successful"
        },
        "clustersInfo": {}
      }
    }
  ]
}
```

---

## Additional Routes Mentioned (Not Currently Used)

The following routes are mentioned in documentation but not actively used in the codebase:

### 4. List Projects

**HTTP Method**: `GET`  
**Path**: `/api/v1/projects`  
**Full URL**: `https://argocd-ro-ardenone-manager-ts.ardenone.com:8444/api/v1/projects`

**Usage Pattern**:
```bash
curl -sk https://argocd-ro-ardenone-manager-ts.ardenone.com:8444/api/v1/projects
```

**Note**: Mentioned in CLAUDE.md permissions but no active usage found.

---

### 5. List Repositories

**HTTP Method**: `GET`  
**Path**: `/api/v1/repositories`  
**Full URL**: `https://argocd-ro-ardenone-manager-ts.ardenone.com:8444/api/v1/repositories`

**Usage Pattern**:
```bash
curl -sk https://argocd-ro-ardenone-manager-ts.ardenone.com:8444/api/v1/repositories
```

**Note**: Mentioned in CLAUDE.md permissions but no active usage found.

---

## SEAM Route Fragment Configuration

The ArgoCD proxy is integrated with SEAM (this project) via route fragments:

**Fragment Path**: `/argocd/api/v1/applications`  
**SEAM-facing Path**: `GET /argocd/api/v1/applications`  
**Upstream Path**: `/api/v1/applications` (via `x-upstream-strip-prefix: /argocd`)  
**Credential**: `vault:seam/routes/argocd/ro-token`  
**Credential Probe**: `GET /argocd/api/v1/applications` every 6h  
**TLS Config**: `caBundle: argocd-ro.pem` (CA pinning for self-signed proxy)

**Fragment Configuration** (from `docs/notes/route-fragment-schema.md`):
```yaml
x-upstream: "https://argocd-ro-ardenone-manager-ts.ardenone.com:8444"
x-upstream-strip-prefix: "/argocd"
x-upstream-tls:
  caBundle: "argocd-ro.pem"
x-vault-path: "vault:seam/routes/argocd/ro-token"
x-inject-as:
  kind: "bearer"
x-credential-probe:
  path: "/argocd/api/v1/applications"
  method: "GET"
  interval: "6h"
paths:
  "/argocd/api/v1/applications":
    get:
      x-required-scope: ["argocd:read"]
      summary: "List ArgoCD applications (read-only)"
```

---

## Test Corpus Files

**Location**: `tools/diffharness/testdata/corpus-argocd.json`  
**Entries**:
1. `list-apps-get` - GET /api/v1/applications
2. `get-app-myapp-get` - GET /api/v1/applications/myapp

**Captured Response Files** (empty):
- `corpus/argocd-proxy/applications-list.json`
- `corpus/argocd-proxy/clusters-list.json`

---

## Discovery Method

Routes were discovered by:
1. Reading CLAUDE.md ArgoCD section
2. Grepping for `argocd` patterns across codebase
3. Examining SEAM route fragment schema docs
4. Reviewing test corpus files
5. Checking plan.md for SEAM integration details

---

## Complete Grep Commands for Discovery

```bash
# Find all argocd references
grep -r "argocd" --include="*.md" --include="*.go" --include="*.sh" --include="*.py" --include="*.yaml" --include="*.yml" --include="*.json" .

# Find API endpoint patterns
grep -r "api/v1/applications\|api/v1/clusters\|api/v1/projects\|api/v1/repositories" --include="*.md" --include="*.sh" --include="*.py" .

# Find route fragment references
grep -r "argocd-ro" --include="*.md" --include="*.go" --include="*.sh" --include="*.py" .

# Find corpus files
find . -path "*/corpus/*" -name "*argocd*" -o -path "*/testdata/*" -name "*argocd*"
```
