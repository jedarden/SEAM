# seam-retirement-evaluator OpenBao Role Configuration

**Documentation Date:** 2026-08-14  
**Bead:** bf-a38su  
**Purpose:** Document the role configuration for seam-retirement-evaluator following cluster patterns

## Configuration Summary

The seam-retirement-evaluator uses OpenBao Kubernetes authentication with a dedicated role and policy. However, there is currently a **namespace mismatch** that must be corrected.

## Current Configuration (AS-IS)

### ServiceAccount Details

**File:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/serviceaccount.yaml`

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: seam-retirement-evaluator
  namespace: seam-retirement-evaluator
```

**Current ServiceAccount:** `seam-retirement-evaluator` in namespace `seam-retirement-evaluator`

### Namespace

**File:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/namespace.yaml`

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: seam-retirement-evaluator
  labels:
    argocd.argoproj.io/project: rs-manager
```

## OpenBao Role Configuration (INTENDED)

**File:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-role-config.hcl`

**Role Name:** `seam-retirement-evaluator`  
**Bound ServiceAccount:** `seam-retirement-evaluator`  
**Intended Namespace:** `seam` (per cluster pattern)  
**Actual Namespace:** `seam-retirement-evaluator` (current deployment)  
**Policy:** `seam-retirement-evaluator-policy`  
**Token TTL:** 24h  
**Token Max TTL:** 72h

### Role Binding Configuration

```hcl
bound_service_account_names = ["seam-retirement-evaluator"]
bound_service_account_namespaces = ["seam"]
```

## Required Correction

The OpenBao role is configured to bind to the ServiceAccount in the `seam` namespace, but the ServiceAccount is actually deployed in the `seam-retirement-evaluator` namespace. This causes authentication failures.

### Correction Options

#### Option 1: Update ServiceAccount namespace to `seam` (RECOMMENDED)

This follows the cluster pattern documented in research bead bf-37z98 where both SEAM and seam-retirement-evaluator would coexist in the `seam` namespace for mutual isolation.

**Changes required:**
1. Update `serviceaccount.yaml` namespace to `seam`
2. Remove dedicated `namespace.yaml` (use existing seam namespace)
3. Update deployment manifests to use `seam` namespace

#### Option 2: Update OpenBao role binding to `seam-retirement-evaluator` namespace

If the evaluator must have its own namespace, update the OpenBao role configuration accordingly.

**Changes required:**
1. Update `openbao-role-config.hcl` to use `bound_service_account_namespaces = ["seam-retirement-evaluator"]`
2. Update `openbao-setup-job.yaml` to use the correct namespace
3. Re-run the OpenBao setup workflow

## Policy Definition

**File:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-policy.hcl`

```hcl
# Allow reading evaluator's own GitHub token from dedicated evaluators path
path "secret/data/evaluators/seam-retirement-evaluator/*" {
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

## Security Boundaries

### Allowed Access
- ✅ Read `secret/data/evaluators/seam-retirement-evaluator/*` (GitHub token)
- ✅ Read `secret/data/monitoring/victoriametrics/*` (VictoriaMetrics credentials)

### Denied Access
- ❌ Read `secret/data/seam/routes/*` (SEAM's route secrets)
- ❌ Read any other secrets (default-deny)

## Cluster Pattern Reference

Based on research bead bf-37z98, the established pattern for OpenBao Kubernetes-auth roles is:

| Service | ServiceAccount | Namespace | OpenBao Role | Policy |
|---------|----------------|------------|--------------|---------|
| SEAM | `seam` | `seam` | `seam` | `seam` |
| seam-retirement-evaluator | `seam-retirement-evaluator` | `seam` (intended) | `seam-retirement-evaluator` | `seam-retirement-evaluator-policy` |

### Security Design Principles

1. **Mutual Isolation:** Each role explicitly denies access to the other's secrets
2. **Namespace Co-location:** Both ServiceAccounts exist in the `seam` namespace (intended pattern)
3. **Least Privilege:** Each role has minimal required access only
4. **Token Time-boxing:** Both use 24h TTL with 72h max limit
5. **Role-specific Setup:** Each has its own dedicated setup mechanism

## Setup Method

**WorkflowTemplate:** `seam-retirement-evaluator-openbao-setup`  
**Location:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-setup-job.yaml`

### Manual Setup Commands

```bash
# Set OpenBao environment
export BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
export BAO_TOKEN="<admin-token>"

# Create the policy
bao policy write seam-retirement-evaluator-policy \
  declarative-config/infra/seam-retirement-evaluator/openbao-policy.hcl

# Create the Kubernetes auth role
bao write auth/kubernetes/role/seam-retirement-evaluator \
  bound_service_account_names=seam-retirement-evaluator \
  bound_service_account_namespaces=seam \
  policies=seam-retirement-evaluator-policy \
  token_ttl=24h \
  token_max_ttl=72h \
  token_default_policies=seam-retirement-evaluator-policy
```

## Verification

**WorkflowTemplate:** `seam-retirement-evaluator-openbao-verify`  
**Location:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-verify-workflow.yaml`

### Verification Tests

1. Kubernetes auth login works with seam-retirement-evaluator SA
2. Can read evaluator's GitHub token
3. Can read VictoriaMetrics credentials  
4. Cannot read SEAM's route secrets

## OpenBao Endpoint Details

**URL:** `http://openbao-ardenone.tail1b1987.ts.net:8200`  
**Alternative (cluster-internal):** `http://openbao-rs-manager.openbao.svc.cluster.local:8200`  
**Kubernetes Auth Mount:** `auth/kubernetes/`

## Issues and Recommendations

### Current Issue
The OpenBao role binding is configured for namespace `seam`, but the ServiceAccount is deployed in namespace `seam-retirement-evaluator`. This mismatch causes authentication failures.

### Recommended Action
Update the deployment configuration to place the seam-retirement-evaluator ServiceAccount in the `seam` namespace, following the established cluster pattern. This requires:

1. Update `serviceaccount.yaml` to use namespace `seam`
2. Update deployment manifests to target `seam` namespace
3. Remove dedicated `namespace.yaml` if not needed for other resources
4. Verify OpenBao authentication works after correction

## References

- **Research Bead:** bf-37z98 (OpenBao Kubernetes-auth research)
- **Research Document:** `/home/coding/SEAM/docs/research/openbao-kubernetes-auth-seam-research.md`
- **Setup Workflow:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-setup-job.yaml`
- **Policy File:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-policy.hcl`
- **Role Config:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-role-config.hcl`