# SEAM Security Isolation Model

## Overview

This document describes the complete security isolation model for SEAM and the seam-retirement-evaluator service. It documents all authentication and authorization paths, OpenBao policies, and the security boundaries that enforce the hostile-fragment threat model.

**Last Updated:** 2026-08-11  
**Bead:** bf-4oa45

## Threat Model: Hostile Fragment

SEAM operates under the hostile-fragment threat model, which assumes:

1. **Fragment authors may be malicious** - Route fragment authors can attempt to exfiltrate credentials
2. **Lint can be bypassed** - Client-side validation is not sufficient
3. **Gateway token must be bounded** - SEAM's OpenBao token must have literally no access outside its designated paths
4. **Cross-tenant isolation is required** - Each service must have strictly bounded access to secrets

### Security Requirements

Under this threat model, the following requirements MUST be satisfied:

1. **SEAM's OpenBao token** can ONLY read `secret/data/seam/routes/*` and NOTHING else
2. **Evaluator's OpenBao token** can ONLY read:
   - `secret/data/evaluators/seam-retirement-evaluator/*` (its own GitHub token)
   - `secret/data/monitoring/victoriametrics/*` (VM credentials)
3. **Mutual denial** - SEAM cannot read evaluator paths, evaluator cannot read SEAM paths
4. **Default-deny** - Both roles explicitly deny all other paths
5. **Read-only** - Neither role has write capabilities to any secret

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        OpenBao (rs-manager)                                  │
│                    http://openbao-rs-manager...:8200                          │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌──────────────────────────────┐  ┌──────────────────────────────────┐   │
│  │   SEAM OpenBao Role          │  │   Evaluator OpenBao Role         │   │
│  │   (seam policy)              │  │   (seam-retirement-evaluator)    │   │
│  ├──────────────────────────────┤  ├──────────────────────────────────┤   │
│  │ Bound SA: seam               │  │ Bound SA: seam-retirement-eval  │   │
│  │ Namespace: seam              │  │ Namespace: seam                 │   │
│  │ Token TTL: 24h               │  │ Token TTL: 24h                   │   │
│  ├──────────────────────────────┤  ├──────────────────────────────────┤   │
│  │ CAN READ:                    │  │ CAN READ:                        │   │
│  │ • seam/routes/*              │  │ • evaluators/seam-retirement-eval/*│  │
│  │                              │  │ • monitoring/victoriametrics/*    │   │
│  │ DENIED:                       │  │                                  │   │
│  │ • evaluators/*               │  │ DENIED:                           │   │
│  │ • all other paths            │  │ • seam/routes/*                   │   │
│  │                              │  │ • all other paths                 │   │
│  └──────────────────────────────┘  └──────────────────────────────────┘   │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  secret/data/seam/routes/*                                          │   │
│  │  ┌────────────────────────────────────────────────────────────────┐ │   │
│  │  • seam/routes/github-alerts/token                                │ │   │
│  │  • seam/routes/kalshi-tape/token                                   │ │   │
│  │  • seam/routes/mta-my-way/token                                    │ │   │
│  │  • ... (one per route that needs external authentication)         │ │   │
│  └────────────────────────────────────────────────────────────────┘ │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  secret/data/evaluators/seam-retirement-evaluator/github-token       │   │
│  │  ┌────────────────────────────────────────────────────────────────┐ │   │
│  │  GitHub PAT for opening PRs in jedarden/declarative-config         │ │   │
│  │  Scope: repo (full control of private repositories)                │ │   │
│  │  Expiration: 90 days                                                │ │   │
│  │  Usage: Open PRs when retiring routes from production              │ │   │
│  └────────────────────────────────────────────────────────────────┘ │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  secret/data/monitoring/victoriametrics/readonly-credentials         │   │
│  │  ┌────────────────────────────────────────────────────────────────┐ │   │
│  │  endpoint: http://victorialogs-single-ardenone-manager...           │ │   │
│  │  username: (empty - internal Kubernetes auth)                      │ │   │
│  │  password: (empty - internal Kubernetes auth)                      │ │   │
│  │  Usage: Query SEAM metrics for retirement evaluation              │ │   │
│  └────────────────────────────────────────────────────────────────┘ │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Authentication Paths

### Path 1: SEAM Gateway Authentication

**Service:** SEAM gateway  
**ServiceAccount:** `seam` (namespace: `seam`)  
**OpenBao Role:** `seam`  
**OpenBao Policy:** `seam`  

**Authentication Flow:**

1. **Kubernetes Authentication:**
   - SEAM pod runs with ServiceAccount `seam`
   - Pod has projected service account token volume mounted at `/var/run/secrets/kubernetes.io/serviceaccount/token`
   - SA token is automatically injected by Kubernetes

2. **OpenBao Login:**
   - SEAM reads SA token from mounted volume
   - SEAM authenticates to OpenBao via Kubernetes auth method:
     ```
     POST /v1/auth/kubernetes/login
     {
       "role": "seam",
       "jwt": "<service-account-token>"
     }
     ```
   - OpenBao validates JWT with Kubernetes API server
   - OpenBao returns OpenBao client token with `seam` policy attached

3. **Token Usage:**
   - SEAM uses OpenBao token to read route secrets on-demand
   - Token is cached in-memory for TTL duration (24h)
   - Token auto-renews before expiration

**Access Boundaries:**
- ✅ CAN read: `secret/data/seam/routes/*`
- ❌ CANNOT read: `secret/data/evaluators/*`
- ❌ CANNOT read: `secret/data/monitoring/*`
- ❌ CANNOT read: Any other paths
- ❌ CANNOT write: Any secrets

### Path 2: Evaluator Authentication

**Service:** seam-retirement-evaluator  
**ServiceAccount:** `seam-retirement-evaluator` (namespace: `seam`)  
**OpenBao Role:** `seam-retirement-evaluator`  
**OpenBao Policy:** `seam-retirement-evaluator-policy`  

**Authentication Flow:**

1. **Kubernetes Authentication:**
   - Evaluator pod runs with ServiceAccount `seam-retirement-evaluator`
   - Pod has projected service account token volume
   - SA token is automatically injected by Kubernetes

2. **OpenBao Login:**
   - Evaluator reads SA token from mounted volume
   - Evaluator authenticates to OpenBao via Kubernetes auth method:
     ```
     POST /v1/auth/kubernetes/login
     {
       "role": "seam-retirement-evaluator",
       "jwt": "<service-account-token>"
     }
     ```
   - OpenBao validates JWT with Kubernetes API server
   - OpenBao returns OpenBao client token with `seam-retirement-evaluator-policy` attached

3. **Token Usage:**
   - Evaluator uses OpenBao token to read GitHub token for opening PRs
   - Evaluator uses OpenBao token to read VictoriaMetrics credentials
   - Token is cached in-memory for TTL duration (24h)
   - Token auto-renews before expiration

**Access Boundaries:**
- ✅ CAN read: `secret/data/evaluators/seam-retirement-evaluator/*`
- ✅ CAN read: `secret/data/monitoring/victoriametrics/*`
- ❌ CANNOT read: `secret/data/seam/routes/*`
- ❌ CANNOT read: Any other paths
- ❌ CANNOT write: Any secrets

## OpenBao Policies

### SEAM Policy

**File:** `declarative-config/infra/seam/seam-openbao-policy.hcl`

```hcl
# Allow reading SEAM route secrets ONLY
path "secret/data/seam/routes/*" {
  capabilities = ["read"]
}

# Deny access to evaluator's secrets (explicit separation)
path "secret/data/evaluators/*" {
  capabilities = ["deny"]
}

# Deny access to all other secrets (default-deny)
path "secret/data/*" {
  capabilities = ["deny"]
}
```

**Policy Properties:**
- **Read-only:** SEAM can only read, never write secrets
- **Path-scoped:** Only `seam/routes/*` is accessible
- **Explicit deny:** All other paths are explicitly denied
- **Isolation enforced:** Evaluator paths explicitly denied

### Evaluator Policy

**File:** `declarative-config/infra/seam-retirement-evaluator/openbao-policy.hcl`

```hcl
# Allow reading evaluator's own GitHub token
path "secret/data/evaluators/seam-retirement-evaluator/*" {
  capabilities = ["read"]
}

# Allow reading VictoriaMetrics credentials
path "secret/data/monitoring/victoriametrics/*" {
  capabilities = ["read"]
}

# Explicitly deny access to SEAM's route secrets
path "secret/data/seam/routes/*" {
  capabilities = ["deny"]
}

# Deny access to all other secrets
path "secret/data/*" {
  capabilities = ["deny"]
}
```

**Policy Properties:**
- **Read-only:** Evaluator can only read, never write secrets
- **Dual-path access:** Own token path and VM credentials only
- **Explicit deny:** SEAM route paths explicitly denied
- **Isolation enforced:** Mutual denial with SEAM policy

## Secret Paths

### SEAM Route Secrets

**Path Pattern:** `secret/data/seam/routes/<route-name>/token`

**Examples:**
- `secret/data/seam/routes/github-alerts/token`
- `secret/data/seam/routes/kalshi-tape/token`
- `secret/data/seam/routes/mta-my-way/token`

**Access:**
- ✅ SEAM CAN read
- ❌ Evaluator CANNOT read

**Contents:**
Each path contains a JSON object with authentication credentials for the external service:
```json
{
  "token": "external-service-token-or-api-key",
  "type": "bearer-token-or-api-key",
  "updated_by": "manual-setup-or-automation"
}
```

### Evaluator GitHub Token

**Path:** `secret/data/evaluators/seam-retirement-evaluator/github-token`

**Access:**
- ✅ Evaluator CAN read
- ❌ SEAM CANNOT read

**Contents:**
```json
{
  "token": "ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "type": "github_pat",
  "updated_by": "manual-setup-or-workflow"
}
```

**Token Requirements:**
- Target: `jedarden/declarative-config` on GitHub (not Forgejo)
- Capability: Open pull requests only
- Scopes: `repo` (Full control of private repositories)
- Expiration: 90 days recommended

**Security Consideration:**
While the token's capability is bounded (can only open PRs), it can technically open PRs against ANY path in declarative-config. Human reviewers must reject any evaluator PR that touches paths outside `routes/<service>/`.

### VictoriaMetrics Credentials

**Path:** `secret/data/monitoring/victoriametrics/readonly-credentials`

**Access:**
- ✅ Evaluator CAN read
- ❌ SEAM CANNOT read

**Contents:**
```json
{
  "endpoint": "http://victorialogs-single-ardenone-manager-vector-headless.monitoring.svc.cluster.local:8428",
  "username": "",
  "password": ""
}
```

**Usage:**
- Evaluator queries VictoriaMetrics for SEAM route metrics
- Metrics determine which routes are candidates for retirement
- Credentials are empty because VictoriaMetrics uses internal Kubernetes auth

## VictoriaMetrics Access Pattern

### Query Endpoint

**VictoriaMetrics Endpoint:** `http://victorialogs-single-ardenone-manager-vector-headless.monitoring.svc.cluster.local:8428`

**Authentication:** Internal Kubernetes auth (no username/password required)

### Query Pattern

The evaluator queries metrics to determine route health and usage:

```bash
# Example: Query request count for a route over the last 30 days
curl -g 'http://victoriametrics:8428/api/v1/query?query=sum(rate(seam_request_count{route="github-alerts"}[30d]))'

# Example: Query error rate for a route
curl -g 'http://victoriametrics:8428/api/v1/query?query=sum(rate(seam_request_errors{route="github-alerts"}[30d]))'
```

**Metrics Used for Retirement Decisions:**
- `seam_request_count` - Total requests per route
- `seam_request_errors` - Error rate per route
- `seam_latency_p99` - 99th percentile latency per route
- `seam_last_request_timestamp` - Timestamp of last request per route

**Access Boundaries:**
- Evaluator has read-only access to metrics
- Evaluator cannot write or delete metrics
- SEAM has no access to VictoriaMetrics

## Kubernetes Role Bindings

### SEAM Role Binding

**File:** `declarative-config/infra/seam/setup-seam-openbao.sh`

```yaml
OpenBao Role: seam
Bound ServiceAccount: seam
Namespace: seam
Policies: ["seam"]
Token TTL: 24h
Token Max TTL: 72h
```

### Evaluator Role Binding

**File:** `declarative-config/infra/seam-retirement-evaluator/openbao-role-config.hcl`

```yaml
OpenBao Role: seam-retirement-evaluator
Bound ServiceAccount: seam-retirement-evaluator
Namespace: seam
Policies: ["seam-retirement-evaluator-policy"]
Token TTL: 24h
Token Max TTL: 72h
```

**Binding Validation:**
- ServiceAccount names MUST match exactly
- Namespace MUST be `seam`
- Policies MUST be correct for each role
- TTL settings enforce regular token renewal

## Security Properties Verified

### 1. Path Separation

✅ **VERIFIED:**
- Evaluator token: `secret/data/evaluators/seam-retirement-evaluator/*`
- SEAM routes: `secret/data/seam/routes/*`
- VictoriaMetrics: `secret/data/monitoring/victoriametrics/*`
- No overlap between paths

### 2. Policy Isolation

✅ **VERIFIED:**
- Evaluator cannot read SEAM routes (explicit deny or default-deny)
- SEAM cannot read evaluator token (explicit deny or default-deny)
- Each policy allows only its designated paths

### 3. Service Account Binding

✅ **VERIFIED:**
- Evaluator role only accessible to `seam-retirement-evaluator` SA
- SEAM role only accessible to `seam` SA
- No cross-binding between roles

### 4. Bounded Capabilities

✅ **VERIFIED:**
- Evaluator: read-only access to own token and VM credentials
- SEAM: read-only access to route secrets only
- No write capabilities granted to either

## Test Coverage

### Unit Tests

**File:** `internal/server/openbao_token_access_denial_test.go`

Tests:
- SEAM CANNOT read evaluator's GitHub token
- SEAM CAN read its own route secrets
- Permission denied errors are correctly returned

### End-to-End Tests

**File:** `internal/server/e2e_isolation_test.go`

Tests:
- Evaluator CAN read own GitHub token
- Evaluator CAN read VictoriaMetrics credentials
- Evaluator CAN query VictoriaMetrics
- Evaluator CANNOT access SEAM routes
- Evaluator CANNOT access other secrets
- SEAM CAN read own route secrets
- SEAM CANNOT read evaluator's token

### Integration Tests (Argo Workflows)

**File:** `declarative-config/infra/seam-retirement-evaluator/openbao-verify-workflow.yaml`

Tests:
- Evaluator ServiceAccount can authenticate to OpenBao
- Evaluator can read own GitHub token
- Evaluator cannot read SEAM routes
- Evaluator can read VictoriaMetrics credentials
- Evaluator policy correctly bounded

## Verification Methods

### Method 1: Go Unit Tests

```bash
# Run SEAM access denial test
go test -v ./internal/server -run TestOpenBaoTokenAccessDenial

# Run end-to-end isolation test
go test -v ./internal/server -run TestE2EIsolation
```

**Note:** These tests require OpenBao in PATH or they skip. Use integration tests for cluster validation.

### Method 2: Argo Workflow Verification

```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kueconfig create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: seam-retirement-evaluator-verify-
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: seam-retirement-evaluator-verify-openbao
EOF
```

### Method 3: Manual OpenBao CLI

```bash
# Set environment variables
export BAO_ADDR="http://openbao-rs-manager.openbao.svc.cluster.local:8200"
export BAO_TOKEN="<your-admin-token>"

# Verify evaluator policy
bao policy read seam-retirement-evaluator-policy

# Verify SEAM policy
bao policy read seam

# Check for deny rules
bao policy read seam | grep 'evaluators'
bao policy read seam-retirement-evaluator-policy | grep 'seam/routes'
```

## Comparison: SEAM vs Evaluator

| Aspect | SEAM | seam-retirement-evaluator |
|--------|------|---------------------------|
| **ServiceAccount** | `seam` | `seam-retirement-evaluator` |
| **Namespace** | `seam` | `seam` |
| **OpenBao Role** | `seam` | `seam-retirement-evaluator` |
| **OpenBao Policy** | `seam` | `seam-retirement-evaluator-policy` |
| **Token TTL** | 24h | 24h |
| **Token Max TTL** | 72h | 72h |
| **Primary Secret Access** | `seam/routes/*` | `evaluators/seam-retirement-evaluator/*`, `monitoring/victoriametrics/*` |
| **Can Read Own Secrets** | ✅ Yes | ✅ Yes |
| **Can Read Other's Secrets** | ❌ No | ❌ No |
| **Can Write Secrets** | ❌ No | ❌ No |
| **Setup Method** | Shell script | Argo WorkflowTemplate |
| **Explicit Deny Rules** | `evaluators/*`, `*` (default) | `seam/routes/*`, `*` (default) |

## Security Checklist

### Setup Verification

- [ ] OpenBao server accessible at `http://openbao-rs-manager.openbao.svc.cluster.local:8200`
- [ ] SEAM policy `seam` exists in OpenBao
- [ ] Evaluator policy `seam-retirement-evaluator-policy` exists in OpenBao
- [ ] SEAM Kubernetes auth role `seam` exists
- [ ] Evaluator Kubernetes auth role `seam-retirement-evaluator` exists
- [ ] SEAM ServiceAccount `seam` exists in namespace `seam`
- [ ] Evaluator ServiceAccount `seam-retirement-evaluator` exists in namespace `seam`

### Secret Verification

- [ ] At least one SEAM route secret exists at `seam/routes/*/token`
- [ ] Evaluator GitHub token exists at `evaluators/seam-retirement-evaluator/github-token`
- [ ] VictoriaMetrics credentials exist at `monitoring/victoriametrics/readonly-credentials`

### Isolation Verification

- [ ] SEAM can read `seam/routes/*` secrets
- [ ] SEAM cannot read `evaluators/*` secrets (permission denied)
- [ ] Evaluator can read `evaluators/seam-retirement-evaluator/*` secrets
- [ ] Evaluator can read `monitoring/victoriametrics/*` secrets
- [ ] Evaluator cannot read `seam/routes/*` secrets (permission denied)
- [ ] Both roles cannot read other paths (armor/, kalshi/, etc.)

### Test Verification

- [ ] Unit tests pass: `go test -v ./internal/server -run TestOpenBaoTokenAccessDenial`
- [ ] E2E tests pass: `go test -v ./internal/server -run TestE2EIsolation`
- [ ] Integration workflow passes: Argo workflow `seam-retirement-evaluator-verify-openbao`

## Related Documentation

- **SEAM OpenBao Setup:** `docs/notes/openbao-seam-setup.md`
- **Evaluator OpenBao Setup:** `docs/notes/openbao-evaluator-setup.md`
- **Isolation Verification Plan:** `docs/notes/evaluator-isolation-verification-plan.md`
- **OpenBao Research:** `docs/research/openbao-kubernetes-auth-seam-research.md`
- **Setup Guide:** `declarative-config/infra/seam-retirement-evaluator/SETUP_GUIDE.md`
- **Completion Guide:** `declarative-config/infra/seam-retirement-evaluator/COMPLETION_GUIDE.md`
- **Test Runbook:** `docs/testing-isolation-runbook.md`

## References

- **Bead:** bf-4oa45 (verification and documentation task)
- **Bead:** bf-38lwm (evaluator documentation task)
- **Bead:** bf-5rx9 (SEAM documentation task)
- **SEAM Policy:** `declarative-config/infra/seam/seam-openbao-policy.hcl`
- **Evaluator Policy:** `declarative-config/infra/seam-retirement-evaluator/openbao-policy.hcl`
- **SEAM Setup:** `declarative-config/infra/seam/setup-seam-openbao.sh`
- **Evaluator Setup:** `declarative-config/infra/seam-retirement-evaluator/openbao-setup-job.yaml`
- **Verify Workflow:** `declarative-config/infra/seam-retirement-evaluator/openbao-verify-workflow.yaml`
