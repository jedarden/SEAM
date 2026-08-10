# OpenBao Setup for seam-retirement-evaluator

## Precondition Status

**Bead:** `bf-38lwm`

This document describes the OpenBao role and policy for the seam-retirement-evaluator service, which requires its own dedicated GitHub token and VictoriaMetrics credentials while maintaining strict isolation from SEAM's route secrets.

## What Was Created

### 1. Evaluator Policy (`openbao-policy.hcl`)

**Location:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-policy.hcl`

**Policy:**
```hcl
# Allow reading evaluator's own GitHub token
path "secret/data/seam-retirement-evaluator/*" {
  capabilities = ["read"]
}

# Allow reading VictoriaMetrics credentials (for metrics query access)
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

**Critical Security Properties:**
- **Read-only:** Evaluator can only read, never write secrets
- **Namespace-scoped:** Only `seam-retirement-evaluator/*` and `monitoring/victoriametrics/*` are accessible
- **Explicit deny:** SEAM route secrets are explicitly denied
- **Isolation:** Mutual denial between SEAM and evaluator roles
- **Default-deny:** All other paths are explicitly denied

### 2. OpenBao Role Configuration (`openbao-role-config.hcl`)

**Location:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-role-config.hcl`

**Role Binding:**
```hcl
# Role Binding
bound_service_account_names = ["seam-retirement-evaluator"]
bound_service_account_namespaces = ["seam"]

# Policies
policies = ["seam-retirement-evaluator-policy"]
token_default_policies = ["seam-retirement-evaluator-policy"]

# Token TTL
token_ttl = "24h"
token_max_ttl = "72h"
```

**Role Specification:**
- **Role name:** `seam-retirement-evaluator`
- **Bound ServiceAccount:** `seam-retirement-evaluator` (in namespace `seam`)
- **Policies:** `seam-retirement-evaluator-policy`
- **Token TTL:** 24h
- **Token Max TTL:** 72h

### 3. Setup Method

The evaluator uses an **Argo WorkflowTemplate** for setup (unlike SEAM's shell script), which provides:
- Automated setup via CI/CD
- Parameterizable GitHub token injection
- Better reproducibility

**WorkflowTemplate:** `seam-retirement-evaluator-openbao-setup`

**Location:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-setup-job.yaml`

**What the workflow does:**
1. Creates policy `seam-retirement-evaluator-policy` in OpenBao
2. Creates Kubernetes auth role `seam-retirement-evaluator`
3. Creates GitHub token path at `secret/seam-retirement-evaluator/github-token`
4. Creates VictoriaMetrics credentials path
5. Verifies SEAM policy isolation (ensures SEAM cannot read evaluator paths)

## How to Apply

### Option 1: Using Argo Workflow (Recommended)

**With GitHub Token:**
```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: seam-retirement-evaluator-setup-
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: seam-retirement-evaluator-openbao-setup
  arguments:
    parameters:
    - name: github-token
      value: "ghp_YOUR_ACTUAL_TOKEN_HERE"
EOF
```

**Without Token (Creates Placeholder):**
```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: seam-retirement-evaluator-setup-
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: seam-retirement-evaluator-openbao-setup
EOF
```

### Option 2: Manual OpenBao API

```bash
# Set environment variables
export BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
export BAO_TOKEN="<your-admin-token>"

# Write the policy
bao policy write seam-retirement-evaluator-policy openbao-policy.hcl

# Create the Kubernetes auth role
bao write auth/kubernetes/role/seam-retirement-evaluator \
  bound_service_account_names=seam-retirement-evaluator \
  bound_service_account_namespaces=seam \
  policies=seam-retirement-evaluator-policy \
  ttl=24h \
  max_ttl=72h

# Create GitHub token secret
bao kv put secret/seam-retirement-evaluator/github-token \
  token="ghp_YOUR_ACTUAL_TOKEN_HERE" \
  updated_by="manual-setup"

# Create VictoriaMetrics credentials placeholder
bao kv put secret/monitoring/victoriametrics/readonly-credentials \
  endpoint="http://victorialogs-single-ardenone-manager-vector-headless.monitoring.svc.cluster.local:8428" \
  username="" \
  password=""
```

### Option 3: Using curl directly

```bash
# Set variables
BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
BAO_TOKEN="<your-admin-token>"
H="-H X-Vault-Token:${BAO_TOKEN}"

# Write policy
POLICY='
path "secret/data/seam-retirement-evaluator/*" {
  capabilities = ["read"]
}
path "secret/data/monitoring/victoriametrics/*" {
  capabilities = ["read"]
}
path "secret/data/seam/routes/*" {
  capabilities = ["deny"]
}
path "secret/data/*" {
  capabilities = ["deny"]
}
'
curl -s ${H} "${BAO_ADDR}/v1/sys/policies/acl/seam-retirement-evaluator-policy" \
  -X PUT -d "{\"policy\": $(echo "$POLICY" | jq -Rs .)}"

# Create role
curl -s ${H} "${BAO_ADDR}/v1/auth/kubernetes/role/seam-retirement-evaluator" \
  -X POST -d '{
    "bound_service_account_names": ["seam-retirement-evaluator"],
    "bound_service_account_namespaces": ["seam"],
    "policies": ["seam-retirement-evaluator-policy"],
    "ttl": "24h",
    "max_ttl": "72h"
  }'
```

## Verification

After applying the setup, run the verification workflow:

```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig create -f - <<EOF
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

**Verification checks:**
1. ✓ Evaluator ServiceAccount can authenticate to OpenBao via Kubernetes auth
2. ✓ Evaluator can read own GitHub token at `seam-retirement-evaluator/github-token`
3. ✓ Evaluator cannot read SEAM route secrets (`seam/routes/*`)
4. ✓ Evaluator can read VictoriaMetrics credentials (`monitoring/victoriametrics/*`)
5. ✓ Evaluator policy correctly bounded (default-deny enforced)
6. ✓ SEAM cannot access evaluator's GitHub token (isolation verified)

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                    OpenBao (rs-manager)                          │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────────────────┐  ┌──────────────────────────┐   │
│  │  SEAM OpenBao Role       │  │  Evaluator OpenBao Role  │   │
│  │  (seam-openbao-policy)   │  │  (seam-retirement-evaluator) │
│  ├──────────────────────────┤  ├──────────────────────────┤   │
│  │ Can read:                │  │ Can read:                │   │
│  │ - seam/routes/*          │  │ - seam-retirement-evaluator/*│
│  │                           │  │ - monitoring/victoriametrics/*│
│  │ DENIED:                   │  │                           │   │
│  │ - seam-retirement-evaluator/* │  │ DENIED:                   │
│  │ - other paths             │  │ - seam/routes/*          │   │
│  └──────────────────────────┘  │ - other paths             │   │
│                                 └──────────────────────────┘   │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐ │
│  │  secret/data/seam-retirement-evaluator/github-token       │ │
│  │  ┌──────────────────────────────────────────────────────┐│ │
│  │  │ GitHub PAT (repo scope, declarative-config PRs)     ││ │
│  │  │                                                      ││ │
│  │  │ Created by: seam-retirement-evaluator-setup         ││ │
│  │  │ Token: ghp_xxxxxxxxxxxxx (or REPLACE placeholder)  ││ │
│  │  └──────────────────────────────────────────────────────┘│ │
│  └──────────────────────────────────────────────────────────┘ │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐ │
│  │  secret/data/monitoring/victoriametrics/readonly-credentials│
│  │  ┌──────────────────────────────────────────────────────┐│ │
│  │  │ endpoint: http://victorialogs-...monitoring.svc...   ││ │
│  │  │ username: (empty - internal auth)                   ││ │
│  │  │ password: (empty - internal auth)                   ││ │
│  │  └──────────────────────────────────────────────────────┘│ │
│  └──────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

## Security Model

The evaluator's OpenBao access is deliberately isolated from SEAM:

### Security Boundaries

**Allowed for Evaluator:**
- ✅ Read `secret/data/seam-retirement-evaluator/*` (GitHub token for PRs)
- ✅ Read `secret/data/monitoring/victoriametrics/*` (metrics query credentials)

**Denied for Evaluator:**
- ❌ Read `secret/data/seam/routes/*` (SEAM's route secrets)
- ❌ Read any other secrets (default-deny)
- ❌ Write any secrets (read-only)

**Allowed for SEAM:**
- ✅ Read `secret/data/seam/routes/*` (SEAM route secrets)

**Denied for SEAM:**
- ❌ Read `secret/data/seam-retirement-evaluator/*` (evaluator's GitHub token)
- ❌ Read any other secrets (default-deny)

### Mutual Isolation

This isolation is enforced at **two levels**:

1. **OpenBao policy** – token capabilities are bounded at the source
2. **Verification steps** – runtime testing of permissions

The mutual denial ensures:
- SEAM cannot read the evaluator's GitHub token (security boundary)
- Evaluator cannot read SEAM route secrets (security boundary)
- Both have read-only access to their respective resources
- No cross-contamination of credentials

### Threat Model

The hostile-fragment threat model requires that:
1. The evaluator's OpenBao token has **literally no access** to SEAM route secrets
2. A malicious fragment author cannot exfiltrate other secrets via `x-vault-path`
3. Even if lint is bypassed, the gateway's token cannot reach other paths

## Comparison: SEAM vs seam-retirement-evaluator

| Aspect | SEAM | seam-retirement-evaluator |
|--------|------|---------------------------|
| **ServiceAccount** | `seam` | `seam-retirement-evaluator` |
| **Namespace** | `seam` | `seam` |
| **OpenBao Role** | `seam` | `seam-retirement-evaluator` |
| **Policy** | `seam` | `seam-retirement-evaluator-policy` |
| **Token TTL** | 24h | 24h |
| **Token Max TTL** | 72h | 72h |
| **Setup Method** | Shell script | Argo WorkflowTemplate |
| **Primary Secret Access** | `seam/routes/*` | `seam-retirement-evaluator/*`, `monitoring/victoriametrics/*` |
| **Explicit Deny Rules** | `seam-retirement-evaluator/*`, `*` (default) | `seam/routes/*`, `*` (default) |

## GitHub Token Requirements

The evaluator needs a GitHub Personal Access Token that can:
- **Target:** `jedarden/declarative-config` on GitHub (not Forgejo)
- **Capability:** Open pull requests only
- **Scopes:** `repo` (Full control of private repositories)
- **Expiration:** 90 days recommended

### Security Consideration

While the token's capability is bounded (can only open PRs), it can technically open PRs against ANY path in declarative-config. **Human reviewers must reject any evaluator PR that touches paths outside `routes/<service>/`.**

## Completion Criteria

The setup is complete when:

1. ✓ OpenBao policy `seam-retirement-evaluator-policy` exists
2. ✓ Kubernetes auth role `seam-retirement-evaluator` exists
3. ✓ GitHub token exists at `secret/data/seam-retirement-evaluator/github-token` (not placeholder)
4. ✓ VictoriaMetrics credentials path exists at `secret/data/monitoring/victoriametrics/readonly-credentials`
5. ✓ Verification workflow passes all tests
6. ✓ SEAM provably cannot access evaluator's token path

## Notes

- OpenBao root tokens are stored in a password manager (see `openbao-dr-runbook.md`)
- Kubernetes auth method must already be enabled in OpenBao (`auth/kubernetes/`)
- The ServiceAccount `seam-retirement-evaluator` is created in the `seam` namespace
- This setup creates **server-side** resources only – no cluster resources beyond the ServiceAccount
- The evaluation service uses this GitHub token to open PRs against `jedarden/declarative-config` when retiring routes

## Related Documentation

- **SEAM OpenBao Setup:** `/home/coding/SEAM/docs/notes/openbao-seam-setup.md`
- **Research:** `/home/coding/SEAM/docs/research/openbao-kubernetes-auth-seam-research.md`
- **Setup Guide:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/SETUP_GUIDE.md`
- **Completion Guide:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/COMPLETION_GUIDE.md`

## References

- **Bead:** bf-38lwm (documentation task)
- **Bead:** bf-37z98 (research task)
- **Evaluator Policy:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-policy.hcl`
- **Evaluator Role Config:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-role-config.hcl`
- **Setup Workflow:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-setup-job.yaml`
- **Verify Workflow:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-verify-workflow.yaml`
