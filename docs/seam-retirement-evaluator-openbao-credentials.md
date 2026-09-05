# SEAM Retirement Evaluator - OpenBao Credentials

> **RETIRED 2026-09-05.** The seam-retirement-evaluator no longer holds a GitHub
> credential of any kind. It is detection-only: it emits deprecation candidates as
> a structured record plus a metric and opens no pull requests, so the credential
> had no consumer. The read grant on
> `secret/data/seam-retirement-evaluator/github/token` was removed from the
> authoritative policy in declarative-config (commit 74ce49b0), and the verify
> template's assertion was inverted so the grant reappearing is now an error.
> **Do not provision this token.** Everything below about obtaining or storing it
> is historical.


> **SUPERSEDED — read the live manifest before provisioning anything.**
> This page is a 2026-08-13 design snapshot. The policy actually deployed for
> this role is `declarative-config/k8s/rs-manager/seam-retirement-evaluator/openbao-policy.hcl`
> (commit `eec9f2f3`, 2026-09-04, "deny the evaluator on the consolidated seam
> route path"), which grants read on two exact paths and denies **both** the
> consolidated and the legacy SEAM route prefixes. The path shapes below
> (`evaluators/seam-retirement-evaluator/*`, `monitoring/victoriametrics/*`)
> were never applied. Verified live 2026-09-04 against that file plus
> ConfigMap `seam/seam-retirement-evaluator-access-canaries` (argocd instance
> `seam-retirement-evaluator-ns-rs-manager`).

## Overview

This document specifies the OpenBao credentials configuration for the seam-retirement-evaluator service. Child beads should reference this document when configuring authentication or storing tokens.

## OpenBao Authentication Configuration

### Kubernetes Authentication Method
- **Auth Method Path**: `auth/kubernetes`
- **Auth Method Type**: Kubernetes

### OpenBao Role
- **Role Name**: `seam-retirement-evaluator`
- **Full Role Path**: `auth/kubernetes/role/seam-retirement-evaluator`

### ServiceAccount
- **ServiceAccount Name**: `seam-retirement-evaluator`
- **Namespace**: `seam`
- **Full ServiceAccount Reference**: `seam/seam-retirement-evaluator`

### Token Configuration
- **Token TTL**: 24h
- **Token Max TTL**: 72h
- **Default Policy**: `seam-retirement-evaluator-policy`

### OpenBao Policy
- **Policy Name**: `seam-retirement-evaluator-policy`

#### Policy Capabilities

The evaluator policy grants the following access:

1. **GitHub Token Storage** (Read Only)
   - Path: `secret/data/evaluators/seam-retirement-evaluator/*`
   - Capabilities: `["read"]`
   - Purpose: Store and retrieve the evaluator's GitHub personal access token
   - *Superseded in the deployed policy*: read on the exact path
     `secret/data/seam-retirement-evaluator/github/token` (+ `secret/metadata/`)

2. **VictoriaMetrics Credentials** (Read Only)
   - Path: `secret/data/monitoring/victoriametrics/*`
   - Capabilities: `["read"]`
   - Purpose: Access monitoring/metrics credentials
   - *Superseded in the deployed policy*: read on the exact path
     `secret/data/rs-manager/seam-retirement-evaluator/victoriametrics-query`
     (+ `secret/metadata/`), query-only — the vmagent write token lives elsewhere

3. **SEAM Route Secrets — CONSOLIDATED PREFIX** (Explicit Deny, in force)
   - Path: `secret/data/rs-manager/rs-manager/seam/routes/*`
   - Capabilities: `["deny"]`
   - Purpose: Prevent evaluator from accessing SEAM's route configuration
   - *Not in this snapshot*: this deny was added later, in `eec9f2f3`. A glob is
     prefix-exact, so the legacy deny below alone stopped matching once the data
     consolidated.

4. **SEAM Route Secrets — LEGACY / RETIRED PREFIX** (Explicit Deny)
   - Path: `secret/data/seam/routes/*`
   - Capabilities: `["deny"]`
   - Purpose: Prevent evaluator from accessing SEAM's route configuration
   - **Retired base.** SEAM's enforced vault base dir is
     `rs-manager/rs-manager/seam/routes`; a bare `seam/routes` path fails
     SEAM-side validation. Carried in the deployed policy only until the legacy
     paths retire.

5. **All Other Secrets** (Default Deny)
   - Path: `secret/data/*`
   - Capabilities: `["deny"]`
   - Purpose: Default-deny security posture

## Token Storage Path

When storing the evaluator's GitHub token, use the following path:

```
secret/data/evaluators/seam-retirement-evaluator/github-token
```

## Authentication Flow

1. The evaluator pod uses ServiceAccount `seam/seam-retirement-evaluator`
2. The pod authenticates to OpenBao via the Kubernetes auth method
3. OpenBao validates the ServiceAccount against the `seam-retirement-evaluator` role
4. Upon successful authentication, the evaluator receives a token with `seam-retirement-evaluator-policy` permissions
5. The token can read from:
   - `secret/data/evaluators/seam-retirement-evaluator/*` (own secrets)
   - `secret/data/monitoring/victoriametrics/*` (monitoring credentials)

## Infrastructure Verification

Before using these credentials for child beads, verify:

1. **OpenBao Role Exists**
   ```bash
   bao read auth/kubernetes/role/seam-retirement-evaluator
   ```

2. **ServiceAccount Exists**
   ```bash
   kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig get serviceaccount -n seam seam-retirement-evaluator
   ```

3. **Policy Exists**
   ```bash
   bao policy read seam-retirement-evaluator-policy
   ```

## Distinction from SEAM's OpenBao Role

The retirement evaluator uses a **different** OpenBao role than the main SEAM service:

| Property | SEAM Service | Retirement Evaluator |
|----------|--------------|---------------------|
| Role Name | `seam` (or `seam-openbao-role`) | `seam-retirement-evaluator` |
| Policy | `seam-openbao-policy` | `seam-retirement-evaluator-policy` |
| ServiceAccount | `seam` | `seam-retirement-evaluator` |
| Route Secret Access | ✅ Can read `secret/data/rs-manager/rs-manager/seam/routes/*` (consolidated; granted by the `seam` policy written by the OpenBao hardening reconciler, ref `declarat-0c5206e7`). The legacy `secret/data/seam/routes/*` base is retired. | ❌ Denied by policy on both the consolidated and the legacy prefix |
| GitHub Token Storage | ❌ Cannot access evaluator token | ✅ Can read `secret/data/evaluators/seam-retirement-evaluator/*` |

## Related Documentation

> The `declarative-config/` paths below are SEAM's **stale in-repo snapshot**,
> not the live repo. Read the same paths under `~/declarative-config` instead.

- OpenBao policy **in force**: `~/declarative-config/k8s/rs-manager/seam-retirement-evaluator/openbao-policy.hcl`
- OpenBao Setup Workflow: `declarative-config/k8s/rs-manager/seam-retirement-evaluator/openbao-setup-job.yaml`
- OpenBao Role Documentation: `~/declarative-config/k8s/rs-manager/seam-retirement-evaluator/setup-openbao-resources.yml`
- Access canaries (live ConfigMap source): `~/declarative-config/k8s/rs-manager/seam-retirement-evaluator/access-canaries.yml`
- ServiceAccount Manifest: `declarative-config/k8s/rs-manager/seam/serviceaccount.yaml`
- Security Isolation Model: `/home/coding/SEAM/docs/security-isolation-model.md`

## Child Bead Usage

When implementing child beads that need to store or access the retirement evaluator's GitHub token:

1. Reference this document for the correct OpenBao role and ServiceAccount names
2. Store the token at: `secret/data/evaluators/seam-retirement-evaluator/github-token`
3. Ensure the evaluator pod uses ServiceAccount: `seam/seam-retirement-evaluator`
4. Verify the OpenBao role `seam-retirement-evaluator` exists before storing credentials

---

**Last Updated**: 2026-09-04 (deny boundary repointed to `secret/data/rs-manager/rs-manager/seam/routes/*`; original snapshot 2026-08-13)  
**Purpose**: Document OpenBao credentials for seam-retirement-evaluator child bead implementations
