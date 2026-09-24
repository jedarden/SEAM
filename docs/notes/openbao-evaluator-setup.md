# OpenBao Setup for seam-retirement-evaluator

> **GITHUB-TOKEN MODEL WITHDRAWN — 2026-09-05** (`declarat-b818338b`). The
> evaluator is **detection-only**: it has no GitHub client, opens no PRs, and
> holds no third-party credential. Its whole output is one structured log
> record and one Prometheus counter per deprecation candidate; the proposed
> `x-seam-deprecated` edit is landed by a human as an ordinary commit to
> `main`. The GitHub-token provisioning workflow, the token requirements, and
> the completion criteria that required a token below are **historical** —
> they can no longer be satisfied and must not be "finished". The live policy
> (`~/declarative-config/k8s/rs-manager/seam-retirement-evaluator/openbao-policy.hcl`,
> written every cycle by the rs-manager hardening-reconciler) grants read on
> exactly one path: the query-only VictoriaMetrics credential
> `rs-manager/seam-retirement-evaluator/victoriametrics-query`. The retired
> GitHub credential path must stay **denied** — the verify script and
> `seam-evaluator-credential-boundary-canary` assert a 403 on it
> (`seam-retirement-evaluator/github/token` for the `seam` identity, and the
> evaluator identity on its own retired path). Do not restore the grant to
> make an old verify step pass; it had no caller.
>
> **VictoriaMetrics credential, settled:** the evaluator **binary** performs
> unauthenticated VM queries (no Authorization header in
> `tools/seam-retirement-evaluator/victoriametrics.go`; config is two env
> vars). The credentialled path exists for the estate's boundary proof: a
> vmauth-scoped, read-only bearer token at the path above, exercised by
> `seam-retirement-evaluator-access-canary`. The old
> `monitoring/victoriametrics/readonly-credentials` path is not in the live
> policy and no longer exists as a grant.
>
> **ROUTE BOUNDARY REPOINTED — 2026-09-04.** SEAM's enforced vault base dir is
> now `rs-manager/rs-manager/seam/routes`
> (`internal/spec/allowlist.go` `DefaultVaultBaseDir`, overridable via
> `SEAM_VAULT_BASE_DIR`). A path under the old bare `seam/routes` base falls
> outside the enforced prefix and fails validation. The route credentials
> consolidated from `secret/seam/*` to `secret/rs-manager/rs-manager/seam/*`,
> and the deployed evaluator policy denies **both** prefixes — consolidated
> `secret/data/rs-manager/rs-manager/seam/routes/*`, and legacy
> `secret/data/seam/routes/*` kept until the old paths retire.
>
> **Source checked, 2026-09-04:** `~/declarative-config/k8s/rs-manager/seam-retirement-evaluator/openbao-policy.hcl`
> at commit `eec9f2f3` ("deny the evaluator on the consolidated seam route
> path"), cross-checked against live ConfigMap
> `seam/seam-retirement-evaluator-access-canaries` on the rs-manager cluster
> (argocd instance `seam-retirement-evaluator-ns-rs-manager`), whose
> `evaluator.sh` probes both prefixes expecting 403. The `declarative-config/`
> paths named below are SEAM's stale in-repo snapshot — read the live repo.
>
> Note the deny set, not just the prefix: a policy carrying only the legacy
> `seam/routes/*` deny reads as correct while silently granting the evaluator
> read on the consolidated route credentials, because a glob is prefix-exact.

## Precondition Status

**Bead:** `bf-38lwm`

This document describes the OpenBao role and policy for the seam-retirement-evaluator service, which maintains strict isolation from SEAM's route secrets. Everything below about a dedicated GitHub token is historical (see the withdrawal banner above) — the evaluator is detection-only and its policy's single grant is the query-only VictoriaMetrics credential.

## What Was Created

### 1. Evaluator Policy (`openbao-policy.hcl`)

**Location:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-policy.hcl` *(stale in-repo snapshot — live copy at `~/declarative-config/k8s/rs-manager/seam-retirement-evaluator/openbao-policy.hcl`)*

**Policy (as originally created 2026-08 — historical; the live policy's
single grant is `rs-manager/seam-retirement-evaluator/victoriametrics-query`):**
```hcl
# Allow reading evaluator's own GitHub token
path "secret/data/seam-retirement-evaluator/*" {
  capabilities = ["read"]
}

# Allow reading VictoriaMetrics credentials (for metrics query access)
path "secret/data/monitoring/victoriametrics/*" {
  capabilities = ["read"]
}

# Explicitly deny access to SEAM's route secrets -- the consolidated prefix,
# which is the base in force.
path "secret/data/rs-manager/rs-manager/seam/routes/*" {
  capabilities = ["deny"]
}

# LEGACY / RETIRED base. Kept until the old paths retire. A glob is
# prefix-exact, so dropping this one before the legacy data is gone would
# silently re-open the old location.
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
- Parameterizable GitHub token injection *(historical — no consumer since 2026-09-05)*
- Better reproducibility

**WorkflowTemplate:** `seam-retirement-evaluator-openbao-setup`

**Location:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-setup-job.yaml` *(stale in-repo snapshot — the live home is `~/declarative-config/k8s/rs-manager/seam-retirement-evaluator/`)*

**What the workflow does** *(historical description; the rs-manager
hardening-reconciler now writes `seam-retirement-evaluator-policy` every
cycle and the setup workflow no longer writes the policy)*:
1. Creates policy `seam-retirement-evaluator-policy` in OpenBao
2. Creates Kubernetes auth role `seam-retirement-evaluator`
3. Creates GitHub token path at `secret/seam-retirement-evaluator/github-token` *(historical — path retired, now denied)*
4. Creates VictoriaMetrics credentials path *(today: `rs-manager/seam-retirement-evaluator/victoriametrics-query`)*
5. Verifies SEAM policy isolation (ensures SEAM cannot read evaluator paths)

## How to Apply

### Option 1: Using Argo Workflow (Recommended)

**With GitHub Token (HISTORICAL — the token parameter no longer has a
consumer; the evaluator is detection-only):**
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

*(The GitHub-token and `readonly-credentials` puts below are historical —
the token path is retired and must stay denied; the live credential is
`rs-manager/seam-retirement-evaluator/victoriametrics-query`.)*

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
path "secret/data/rs-manager/rs-manager/seam/routes/*" {
  capabilities = ["deny"]
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

**Verification checks** (inverted where noted, 2026-09-05):
1. ✓ Evaluator ServiceAccount can authenticate to OpenBao via Kubernetes auth
2. ✓ Evaluator is **denied (403)** on the retired GitHub credential path `seam-retirement-evaluator/github/token` — inverted from the old read check when the grant was removed
3. ✓ Evaluator cannot read SEAM route secrets (`rs-manager/rs-manager/seam/routes/*`, the base in force)
4. ✓ Evaluator cannot read SEAM route secrets on the legacy base (`seam/routes/*`, retired — kept only until the old paths are gone)
5. ✓ Evaluator can read the query-only VictoriaMetrics credential (`rs-manager/seam-retirement-evaluator/victoriametrics-query`) — and its token cannot reach the metrics write or delete APIs
6. ✓ Evaluator policy correctly bounded (default-deny enforced)
7. ✓ SEAM cannot access the evaluator credential path (isolation verified — `seam-evaluator-credential-boundary-canary` asserts the 403 every 5 minutes)

## Architecture Overview

The two lower credential boxes below are **historical** (withdrawn
2026-09-05): the live estate's only evaluator credential is the query-only
VictoriaMetrics token at
`secret/data/rs-manager/seam-retirement-evaluator/victoriametrics-query`.

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
│  │ - rs-manager/rs-manager/ │  │ - seam-retirement-evaluator/*│
│  │   seam/routes/*          │  │ - monitoring/victoriametrics/*│
│  │   (consolidated)         │  │                          │   │
│  │ DENIED:                  │  │ DENIED:                  │   │
│  │ - seam-retirement-eval/* │  │ - rs-manager/rs-manager/ │   │
│  │ - other paths            │  │   seam/routes/* (in force)│  │
│  │                          │  │ - seam/routes/* (legacy, │   │
│  │                          │  │   retired)               │   │
│  │                          │  │ - other paths            │   │
│  └──────────────────────────┘  └──────────────────────────┘   │
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
- ✅ Read `secret/data/rs-manager/seam-retirement-evaluator/victoriametrics-query` (the query-only VictoriaMetrics credential — the single grant)
- ~~Read `secret/data/seam-retirement-evaluator/*` (GitHub token for PRs)~~ — withdrawn 2026-09-05
- ~~Read `secret/data/monitoring/victoriametrics/*`~~ — never in the live policy

**Denied for Evaluator:**
- ❌ Read `secret/data/rs-manager/rs-manager/seam/routes/*` (SEAM's route secrets)
- ❌ Read the retired GitHub credential path `seam-retirement-evaluator/github/token` (must 403)
- ❌ Read any other secrets (default-deny)
- ❌ Write any secrets (read-only)

**Allowed for SEAM:**
- ✅ Read `secret/data/rs-manager/rs-manager/seam/routes/*` (SEAM route secrets)

**Denied for SEAM:**
- ❌ Read `secret/data/seam-retirement-evaluator/*` (evaluator credential path — explicit deny)
- ❌ Read any other secrets (default-deny)

### Mutual Isolation

This isolation is enforced at **two levels**:

1. **OpenBao policy** – token capabilities are bounded at the source
2. **Verification steps** – runtime testing of permissions

The mutual denial ensures:
- SEAM cannot read the evaluator credential path (security boundary — canaried every 5 minutes)
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
| **Primary Secret Access** | `rs-manager/rs-manager/seam/routes/*` | `rs-manager/seam-retirement-evaluator/victoriametrics-query` (query-only VM credential; no GitHub token) |
| **Explicit Deny Rules** | `seam-retirement-evaluator/*`, `*` (default) | `rs-manager/rs-manager/seam/routes/*` + retired `seam/routes/*` |

## GitHub Token Requirements

**Withdrawn 2026-09-05** (`declarat-b818338b`). The evaluator no longer
holds, reads, or needs a GitHub token: it is detection-only, and the
`x-seam-deprecated` edit it proposes is landed by a human as an ordinary
commit to `main` in declarative-config. The requirements below are kept only
to explain what the historical setup created — do not provision a token
against them.

- **Target (historical):** `jedarden/declarative-config` on GitHub (not Forgejo)
- **Capability (historical):** Open pull requests only
- **Scopes (historical):** `repo` (Full control of private repositories)
- **Expiration (historical):** 90 days recommended

### Security Consideration (current)

The evaluator's write-path blast radius is now **zero**: there is no git-host
credential and no PR path. The remaining boundary to watch is VictoriaMetrics
— the `victoriametrics-query` token is vmauth-scoped to read endpoints only,
and the access canary asserts the write and delete APIs reject it.

## Completion Criteria

The setup is complete when:

1. ✓ OpenBao policy `seam-retirement-evaluator-policy` exists (as written every cycle by the rs-manager hardening-reconciler: single read grant on `rs-manager/seam-retirement-evaluator/victoriametrics-query`, explicit denies on both SEAM route prefixes)
2. ✓ Kubernetes auth role `seam-retirement-evaluator` exists
3. ✓ The query-only VictoriaMetrics credential exists at `secret/data/rs-manager/seam-retirement-evaluator/victoriametrics-query` (with `endpoint` and `token` fields)
4. ✓ No GitHub token exists for the evaluator — the retired path stays absent **and denied** (403 for both the evaluator and `seam` identities)
5. ✓ Verification passes: both canary Deployments READY 1/1 in namespace `seam` on rs-manager
6. ✓ SEAM provably cannot access the evaluator credential path

## Notes

- OpenBao root tokens are stored in a password manager (see `openbao-dr-runbook.md`)
- Kubernetes auth method must already be enabled in OpenBao (`auth/kubernetes/`)
- The ServiceAccount `seam-retirement-evaluator` is created in the `seam` namespace
- This setup creates **server-side** resources only – no cluster resources beyond the ServiceAccount
- The evaluator is detection-only: it emits deprecation candidates and lands nothing itself — a human commits the proposed `x-seam-deprecated` edit to `main` in declarative-config

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
