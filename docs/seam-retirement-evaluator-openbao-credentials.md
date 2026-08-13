# SEAM Retirement Evaluator - OpenBao Credentials

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

2. **VictoriaMetrics Credentials** (Read Only)
   - Path: `secret/data/monitoring/victoriametrics/*`
   - Capabilities: `["read"]`
   - Purpose: Access monitoring/metrics credentials

3. **SEAM Route Secrets** (Explicit Deny)
   - Path: `secret/data/seam/routes/*`
   - Capabilities: `["deny"]`
   - Purpose: Prevent evaluator from accessing SEAM's route configuration

4. **All Other Secrets** (Default Deny)
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
| Route Secret Access | ✅ Can read `secret/data/seam/routes/*` | ❌ Denied by policy |
| GitHub Token Storage | ❌ Cannot access evaluator token | ✅ Can read `secret/data/evaluators/seam-retirement-evaluator/*` |

## Related Documentation

- OpenBao Setup Workflow: `/home/coding/SEAM/declarative-config/k8s/rs-manager/seam-retirement-evaluator/openbao-setup-job.yaml`
- OpenBao Role Documentation: `/home/coding/SEAM/declarative-config/k8s/rs-manager/seam-retirement-evaluator/setup-openbao-resources.yml`
- ServiceAccount Manifest: `/home/coding/SEAM/declarative-config/k8s/rs-manager/seam/serviceaccount.yaml`
- Security Isolation Model: `/home/coding/SEAM/docs/security-isolation-model.md`

## Child Bead Usage

When implementing child beads that need to store or access the retirement evaluator's GitHub token:

1. Reference this document for the correct OpenBao role and ServiceAccount names
2. Store the token at: `secret/data/evaluators/seam-retirement-evaluator/github-token`
3. Ensure the evaluator pod uses ServiceAccount: `seam/seam-retirement-evaluator`
4. Verify the OpenBao role `seam-retirement-evaluator` exists before storing credentials

---

**Last Updated**: 2026-08-13  
**Purpose**: Document OpenBao credentials for seam-retirement-evaluator child bead implementations
