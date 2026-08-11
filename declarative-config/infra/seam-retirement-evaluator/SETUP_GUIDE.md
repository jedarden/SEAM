# seam-retirement-evaluator OpenBao Setup Guide

This document describes how to provision OpenBao resources for the seam-retirement-evaluator to ensure it has its own dedicated GitHub token and can query VictoriaMetrics metrics while being isolated from SEAM's OpenBao role.

## Prerequisites

- Access to OpenBao admin/root credentials
- Access to create GitHub Personal Access Tokens
- Access to submit Argo Workflows in `iad-ci` cluster
- `kubectl` access to `rs-manager` cluster

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
│  │ - seam/routes/*          │  │ - evaluators/seam-retirement-evaluator/*│
│  │                           │  │ - monitoring/victoriametrics/*│
│  │ DENIED:                   │  │                           │   │
│  │ - evaluators/seam-retirement-evaluator/* │  │ DENIED:                   │
│  │ - other paths             │  │ - seam/routes/*          │   │
│  └──────────────────────────┘  │ - other paths             │   │
│                                 └──────────────────────────┘   │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐ │
│  │  secret/data/evaluators/seam-retirement-evaluator/github-token       │ │
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

## Step 1: Create GitHub Personal Access Token

The evaluator needs a GitHub token that can open PRs against `jedarden/declarative-config` (GitHub origin, not Forgejo).

1. Visit: https://github.com/settings/tokens
2. Click "Generate new token" → "Generate new token (classic)"
3. Configure:
   - **Note**: "seam-retirement-evaluator PR token"
   - **Expiration**: 90 days (or as per org policy)
   - **Scopes**: `repo` (Full control of private repositories)
4. Click "Generate token"
5. **Copy the token immediately** - it won't be shown again

### Important Notes

- **Repository target**: `jedarden/declarative-config` on GitHub (not Forgejo)
- **Token capability**: Opening PRs only (read + write to open PRs)
- **Security consideration**: While the token's capability is bounded (can only open PRs), it can technically open PRs against ANY path in declarative-config
- **Human review gate**: Reviewers must reject any evaluator PR that touches paths outside `routes/<service>/`

## Step 2: Run OpenBao Setup Workflow

The setup workflow creates:
- OpenBao policy for the evaluator
- Kubernetes authentication role binding
- GitHub token storage path
- VictoriaMetrics credentials path
- Verification of SEAM isolation

### Option A: With GitHub Token (Recommended)

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

### Option B: Without Token (Creates Placeholder)

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

If you use Option B, you'll need to manually add the token later:

```bash
# From within a pod that has OpenBao access, or using port-forward
bao kv put secret/evaluators/seam-retirement-evaluator/github-token \
  token="ghp_YOUR_ACTUAL_TOKEN_HERE" \
  updated_by="manual-update"
```

## Step 3: Verify the Setup

After the setup workflow completes successfully, run the verification workflow:

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

This will verify:
- ✓ Evaluator ServiceAccount can authenticate to OpenBao
- ✓ Evaluator can read own GitHub token
- ✓ Evaluator cannot read SEAM routes (isolation verified)
- ✓ Evaluator can read VictoriaMetrics credentials
- ✓ Evaluator policy correctly bounded

## Step 3.5: Test VictoriaMetrics Query (Optional)

After the OpenBao verification passes, you can optionally test that the evaluator can successfully query VictoriaMetrics for SEAM metrics:

```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: seam-retirement-evaluator-vm-query-
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: evaluator-victoriametrics-query-test
EOF
```

This will verify:
- ✓ Evaluator can read VictoriaMetrics credentials from OpenBao
- ✓ VictoriaMetrics endpoint is reachable
- ✓ SEAM metrics can be queried (`seam_cache_hits_total`, `seam_cache_misses_total`, etc.)
- ✓ Metrics are returned in correct format

**Note:** This test may show warnings if SEAM has not yet been scraped by VictoriaMetrics or if no metrics have been emitted. The test verifies that the evaluator CAN query VictoriaMetrics, even if no SEAM metrics are present yet.

## Step 4: Check Workflow Status

Monitor the workflows:

```bash
# List recent workflows
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  get workflows -n argo-workflows | grep seam-retirement-evaluator

# Get workflow logs
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  logs -n argo-workflows <pod-name> -c main
```

## Troubleshooting

### Workflow fails with "cannot reach OpenBao"

Check that OpenBao is running:
```bash
kubectl --server=http://traefik-rs-manager:8001 get pods -n openbao
```

### Verification shows "cannot read own GitHub token"

Check that the token path exists:
```bash
# From an admin pod
bao kv get secret/evaluators/seam-retirement-evaluator/github-token
```

If it shows "REPLACE_WITH_ACTUAL_GITHUB_PAT", update it with the real token.

### Verification shows "can access SEAM routes"

This is a **security issue**. Check the policy:
```bash
bao policy read seam-retirement-evaluator-policy
```

The policy should have:
```
path "secret/data/seam/routes/*" {
  capabilities = ["deny"]
}
```

### VictoriaMetrics credentials don't exist

The setup creates a placeholder. If actual credentials are needed:
```bash
bao kv put secret/monitoring/victoriametrics/readonly-credentials \
  username="your_username" \
  password="your_password" \
  endpoint="http://victorialogs-single-ardenone-manager-vector-headless.monitoring.svc.cluster.local:8428"
```

## Files Created

This setup process creates/uses the following files:

1. `declarative-config/infra/seam-retirement-evaluator/openbao-policy.hcl` - Policy definition
2. `declarative-config/infra/seam-retirement-evaluator/openbao-setup-job.yaml` - Setup workflow template
3. `declarative-config/infra/seam-retirement-evaluator/openbao-verify-workflow.yaml` - Verification workflow template
4. `declarative-config/infra/seam/seam-openbao-policy.hcl` - SEAM's policy (existing, ensures isolation)

## Security Model

The evaluator's OpenBao access is deliberately isolated:

1. **Separate path**: `secret/data/evaluators/seam-retirement-evaluator/*` (dedicated evaluators namespace, not under `seam/routes/*`)
2. **Separate policy**: `seam-retirement-evaluator-policy` (not shared with SEAM)
3. **Explicit denial**: SEAM policy explicitly denies access to evaluator paths
4. **Bounded capability**: Evaluator policy only allows:
   - Reading its own GitHub token
   - Reading VictoriaMetrics credentials
   - Nothing else (including SEAM routes)

This ensures:
- SEAM cannot read the evaluator's GitHub token (security boundary)
- Evaluator cannot read SEAM route secrets (security boundary)
- Both have read-only access to their respective resources
- No cross-contamination of credentials

## Completion Criteria

The setup is complete when:

1. ✓ OpenBao policy `seam-retirement-evaluator-policy` exists
2. ✓ Kubernetes auth role `seam-retirement-evaluator` exists
3. ✓ GitHub token exists at `secret/data/evaluators/seam-retirement-evaluator/github-token` (not placeholder)
4. ✓ VictoriaMetrics credentials path exists at `secret/data/monitoring/victoriametrics/readonly-credentials`
5. ✓ Verification workflow passes all tests
6. ✓ SEAM provably cannot access evaluator's token path

## Next Steps

After completing this setup, the evaluator can be deployed with:

1. `ServiceAccount: seam-retirement-evaluator` in `seam` namespace
2. OpenBao Kubernetes authentication using the configured role
3. Ability to read GitHub token for opening declarative-config PRs
4. Ability to query VictoriaMetrics for retirement evaluation metrics
5. Guaranteed isolation from SEAM's OpenBao role and route secrets
