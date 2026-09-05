# OpenBao Kubernetes-auth Research: SEAM and seam-retirement-evaluator

> **ROUTE BOUNDARY SUPERSEDED — 2026-09-04.** This research predates the
> `secret/seam/*` → `secret/rs-manager/rs-manager/seam/*` consolidation. Every
> `seam/routes` path below is the **legacy / retired** base: SEAM's enforced
> vault base dir is now `rs-manager/rs-manager/seam/routes`
> (`internal/spec/allowlist.go` `DefaultVaultBaseDir`), a bare `seam/routes`
> path fails SEAM-side validation, and the deployed evaluator policy denies
> **both** prefixes — consolidated
> `secret/data/rs-manager/rs-manager/seam/routes/*` and legacy
> `secret/data/seam/routes/*`. Source:
> `~/declarative-config/k8s/rs-manager/seam-retirement-evaluator/openbao-policy.hcl`
> at commit `eec9f2f3` (2026-09-04), cross-checked against live ConfigMap
> `seam/seam-retirement-evaluator-access-canaries`. The OpenBao endpoint named
> at the bottom (`openbao-ardenone.tail1b1987.ts.net:8200`) is the legacy
> ardenone-cluster instance, **decommissioned 2026-08-29**; the evaluator is
> provisioned on the rs-manager instance. All `declarative-config/...` paths
> are SEAM's stale in-repo snapshot, not the live repo.

**Research Date:** 2026-08-09  
**Boundary re-checked:** 2026-09-04  
**Bead:** bf-37z98  
**Purpose:** Document existing OpenBao Kubernetes-auth configuration patterns for SEAM to understand requirements for dedicated roles

## Executive Summary

Both SEAM and seam-retirement-evaluator have **comprehensive OpenBao Kubernetes-auth configurations already implemented**. The configurations follow security best practices with:
- **Role separation:** Each service has its own dedicated OpenBao role and policy
- **Namespace isolation:** Both services use ServiceAccounts in the `seam` namespace
- **Least privilege:** Each role has minimal required permissions with explicit deny rules
- **Token lifecycle:** 24h TTL with 72h max TTL for both roles

## Existing Configuration: SEAM OpenBao Role

### ServiceAccount Details

**File:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/namespace.yaml`

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: seam
  labels:
    argocd.argoproj.io/project: rs-manager
```

**ServiceAccount:** `seam` (in namespace `seam`)

### OpenBao Role Configuration

**Role Name:** `seam`  
**Bound ServiceAccount:** `seam` (namespace: `seam`)  
**Policy:** `seam` (from `seam-openbao-policy.hcl`)  
**Token TTL:** 24h  
**Token Max TTL:** 72h

### Policy Definition (`seam-openbao-policy.hcl`)

**Location:** `/home/coding/SEAM/declarative-config/infra/seam/seam-openbao-policy.hcl`

```hcl
# Allow reading SEAM route secrets ONLY
# LEGACY / RETIRED base as written. The read grant in force is on the
# consolidated prefix secret/data/rs-manager/rs-manager/seam/routes/*,
# written by the OpenBao hardening reconciler (ref declarat-0c5206e7).
path "secret/data/seam/routes/*" {
  capabilities = ["read"]
}

# Deny access to evaluator's secrets (explicit separation)
path "secret/data/seam-retirement-evaluator/*" {
  capabilities = ["deny"]
}

# Deny access to all other secrets (default-deny)
path "secret/data/*" {
  capabilities = ["deny"]
}
```

### Setup Method

**Setup Script:** `/home/coding/SEAM/declarative-config/infra/seam/setup-seam-openbao.sh`

**Execution:**
```bash
export BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
export BAO_TOKEN="<admin-token>"
./declarative-config/infra/seam/setup-seam-openbao.sh
```

**What the script does:**
1. Creates policy `seam` in OpenBao
2. Creates Kubernetes auth role `seam` 
3. Creates test secret at `seam/routes/test-secret` *(legacy base — the live equivalent writes under `rs-manager/rs-manager/seam/routes/`)*
4. Verifies role permissions (can read `seam/routes/*` — legacy base, now `rs-manager/rs-manager/seam/routes/*` — denied elsewhere)
5. Cleans up test token

### Security Boundaries

**Allowed:**
- ✅ Read `secret/data/rs-manager/rs-manager/seam/routes/*` (SEAM route secrets — consolidated prefix, in force; granted by the OpenBao hardening reconciler, ref `declarat-0c5206e7`)
- ✅ *(historical, retired)* Read `secret/data/seam/routes/*` (legacy base — retained only until the old paths retire)

**Denied:**
- ❌ Read `secret/data/seam-retirement-evaluator/*` (evaluator's GitHub token)
- ❌ Read `secret/data/kalshi/*` (Kalshi credentials)
- ❌ Read `secret/data/armor/*` (Armor credentials)
- ❌ Read any other tenant's material
- ❌ Read cluster kubeconfigs
- ❌ List arbitrary paths
- ❌ Write any secrets

### Threat Model

The **hostile-fragment threat model** requires that:
1. SEAM's OpenBao token has **literally no access** outside SEAM's own route prefix — `rs-manager/rs-manager/seam/routes/*` today; `seam/routes/*` was that prefix when this research was written and is now retired
2. A malicious fragment author cannot exfiltrate other secrets via `x-vault-path`
3. Even if lint is bypassed, the gateway's token cannot reach other paths

Enforced at **two levels:**
1. **OpenBao policy** – token capabilities are bounded at the source
2. **Gateway validation** – runtime re-check of `x-vault-path` against allowlist

---

## Existing Configuration: seam-retirement-evaluator OpenBao Role

### ServiceAccount Details

**File:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/serviceaccount.yaml`

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: seam-retirement-evaluator
  namespace: seam
  annotations:
    # This SA will be used for OpenBao Kubernetes authentication
    # and for the evaluator deployment to authenticate to OpenBao
```

**ServiceAccount:** `seam-retirement-evaluator` (in namespace `seam`)

### OpenBao Role Configuration

**Role Name:** `seam-retirement-evaluator`  
**Bound ServiceAccount:** `seam-retirement-evaluator` (namespace: `seam`)  
**Policy:** `seam-retirement-evaluator-policy`  
**Token TTL:** 24h  
**Token Max TTL:** 72h

### Policy Definition

**From WorkflowTemplate:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-setup-job.yaml`

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
# LEGACY / RETIRED base as written. The deployed policy carries this deny AND
# the consolidated secret/data/rs-manager/rs-manager/seam/routes/* deny.
path "secret/data/seam/routes/*" {
  capabilities = ["deny"]
}

# Deny access to all other secrets
path "secret/data/*" {
  capabilities = ["deny"]
}
```

### Setup Method

**WorkflowTemplate:** `seam-retirement-evaluator-openbao-setup`  
**Location:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-setup-job.yaml`

**Execution via Argo Workflow:**
```bash
kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: seam-retirement-evaluator-openbao-setup-
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: seam-retirement-evaluator-openbao-setup
  arguments:
    parameters:
    - name: github-token
      value: "ghp_..." # Optional GitHub PAT
EOF
```

**What the workflow does:**
1. Creates policy `seam-retirement-evaluator-policy` in OpenBao
2. Creates Kubernetes auth role `seam-retirement-evaluator`
3. Creates GitHub token path at `secret/seam-retirement-evaluator/github-token`
4. Creates VictoriaMetrics credentials path
5. Verifies SEAM policy isolation (ensures SEAM cannot read evaluator paths)

### Security Boundaries

**Allowed:**
- ✅ Read `secret/data/seam-retirement-evaluator/*` (evaluator's own GitHub token)
- ✅ Read `secret/data/monitoring/victoriametrics/*` (VictoriaMetrics credentials)

**Denied:**
- ❌ Read `secret/data/rs-manager/rs-manager/seam/routes/*` (SEAM's route secrets — consolidated prefix, in force)
- ❌ Read `secret/data/seam/routes/*` (SEAM's route secrets — **legacy/retired** base, kept until the old paths retire)
- ❌ Read any other secrets (default-deny)

### Verification Workflow

**WorkflowTemplate:** `seam-retirement-evaluator-openbao-verify`  
**Location:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-verify-workflow.yaml`

Tests:
1. Kubernetes auth login works with seam-retirement-evaluator SA
2. Can read evaluator's GitHub token
3. Can read VictoriaMetrics credentials
4. Cannot read SEAM's route secrets

---

## Comparative Analysis: SEAM vs seam-retirement-evaluator

| Aspect | SEAM | seam-retirement-evaluator |
|--------|------|---------------------------|
| **ServiceAccount** | `seam` | `seam-retirement-evaluator` |
| **Namespace** | `seam` | `seam` |
| **OpenBao Role** | `seam` | `seam-retirement-evaluator` |
| **Policy** | `seam` | `seam-retirement-evaluator-policy` |
| **Token TTL** | 24h | 24h |
| **Token Max TTL** | 72h | 72h |
| **Setup Method** | Shell script (`setup-seam-openbao.sh`) | Argo WorkflowTemplate |
| **Primary Secret Access** | `rs-manager/rs-manager/seam/routes/*` (was `seam/routes/*`, now retired) | `seam-retirement-evaluator/*`, `monitoring/victoriametrics/*` |
| **Explicit Deny Rules** | `seam-retirement-evaluator/*`, `*` (default) | `rs-manager/rs-manager/seam/routes/*` (consolidated, in force) **and** `seam/routes/*` (legacy, retired), `*` (default) |

### Key Security Design Principles

1. **Mutual Isolation:** Each role explicitly denies access to the other's secrets
2. **Namespace Co-location:** Both ServiceAccounts exist in the `seam` namespace
3. **Least Privilege:** Each role has minimal required access only
4. **Token Time-boxing:** Both use 24h TTL with 72h max limit
5. **Role-specific Setup:** Each has its own dedicated setup mechanism

---

## Implementation Approach Pattern

### For New Services Requiring OpenBao Access

Based on the existing patterns, a new service should follow this approach:

#### 1. ServiceAccount Creation

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: <service-name>
  namespace: seam
```

#### 2. OpenBao Policy Definition

Create HCL policy with:
- Allow read on specific required paths
- Explicit deny on all other sensitive paths
- Default-deny rule for `secret/data/*`

```hcl
# Allow reading service's own secrets
path "secret/data/<service>/*" {
  capabilities = ["read"]
}

# Explicitly deny access to other services' secrets.
# SEAM's route credentials live at the CONSOLIDATED prefix -- that is the base
# in force and the one a new policy must deny. The legacy secret/data/seam/routes/*
# deny is carried alongside it only while the old paths still exist.
path "secret/data/rs-manager/rs-manager/seam/routes/*" {
  capabilities = ["deny"]
}
path "secret/data/seam/routes/*" {
  capabilities = ["deny"]
}
path "secret/data/<other-service>/*" {
  capabilities = ["deny"]
}

# Deny access to all other secrets
path "secret/data/*" {
  capabilities = ["deny"]
}
```

#### 3. OpenBao Role Configuration

```bash
bao write auth/kubernetes/role/<service-name> \
  bound_service_account_names=<service-name> \
  bound_service_account_namespaces=seam \
  policies=<service-name>-policy \
  token_ttl=24h \
  token_max_ttl=72h \
  token_default_policies=<service-name>-policy
```

#### 4. Setup Automation

**Option A:** Shell script (like SEAM)
- Simple, manual execution
- Good for one-time setup
- Includes verification steps

**Option B:** Argo WorkflowTemplate (like seam-retirement-evaluator)
- Automated via CI/CD
- Parameterizable (e.g., GitHub token)
- Better for complex setups with multiple steps

#### 5. Documentation

Create documentation file (`docs/research/<service>-openbao-research.md`) covering:
- ServiceAccount details
- OpenBao role configuration
- Policy definition
- Security boundaries
- Setup method
- Verification approach

---

## Security Requirements Summary

### Required Role Permissions

**SEAM Role:**
- ✅ Read `secret/data/rs-manager/rs-manager/seam/routes/*` (SEAM route secrets — consolidated prefix, in force)
- ❌ Everything else explicitly denied

**seam-retirement-evaluator Role:**
- ✅ Read `secret/data/seam-retirement-evaluator/*` (GitHub token)
- ✅ Read `secret/data/monitoring/victoriametrics/*` (metrics credentials)
- ❌ `secret/data/rs-manager/rs-manager/seam/routes/*` (SEAM route secrets — consolidated, in force)
- ❌ `secret/data/seam/routes/*` (SEAM route secrets — legacy/retired)
- ❌ Everything else explicitly denied

### Security Boundaries

Both roles enforce:
1. **Read-only access:** No write capabilities
2. **Path isolation:** Explicit deny rules for other services
3. **Default-deny:** `secret/data/*` denies all unspecified paths
4. **Token time-boxing:** 24h TTL, 72h max TTL
5. **ServiceAccount binding:** Tight Kubernetes SA binding

### Mutual Isolation Verification

**SEAM cannot read:**
- ❌ `seam-retirement-evaluator/*` (evaluator's GitHub token)

**seam-retirement-evaluator cannot read:**
- ❌ `rs-manager/rs-manager/seam/routes/*` (SEAM's route secrets — consolidated prefix, in force)
- ❌ `seam/routes/*` (SEAM's route secrets — **legacy/retired** base)

This isolation is enforced by:
1. **Explicit deny rules** in respective policies
2. **Verification steps** in setup scripts/workflows
3. **Runtime testing** of token permissions

---

## OpenBao Cluster Details

### OpenBao Endpoint

**URL:** `http://openbao-ardenone.tail1b1987.ts.net:8200`  
**Alternative (cluster-internal):** `http://openbao-rs-manager.openbao.svc.cluster.local:8200`

### Kubernetes Auth Method

**Mount Point:** `auth/kubernetes/`  
**Status:** Must be enabled before role creation  
**Verification:** `bao auth list | grep kubernetes`

### Token Management

**Root Tokens:** Stored in password manager (see `openbao-dr-runbook.md`)  
**Admin Tokens:** Required for setup scripts and workflow execution  
**Service Tokens:** Obtained via Kubernetes auth during runtime

---

## Verification and Testing

### SEAM Role Verification

The setup script (`setup-seam-openbao.sh`) automatically tests:

```bash
# Test 1: Reading seam/routes/test-secret (should succeed)
#                  ^^^^^^^^^^ legacy base -- the live equivalent probes
#                  rs-manager/rs-manager/seam/routes/test-secret
✓ SUCCESS: Can read seam/routes/*

# Test 2: Reading seam-retirement-evaluator/* (should be denied)
✓ SUCCESS: Denied access to evaluator secrets

# Test 3: Reading kalshi/* (should be denied)
✓ SUCCESS: Denied access to other tenant secrets

# Test 4: Listing secret/ (should be denied)
✓ SUCCESS: Denied listing access
```

### seam-retirement-evaluator Role Verification

The workflow template includes verification steps:

```bash
# Verify SEAM policy isolation
if bao policy read seam-openbao-policy | grep -q "seam-retirement-evaluator"; then
  error "SEAM policy may have access to evaluator paths"
fi
```

---

## Files and Locations

### SEAM Configuration

| File | Location | Purpose |
|------|----------|---------|
| Policy HCL | `/home/coding/SEAM/declarative-config/infra/seam/seam-openbao-policy.hcl` | SEAM policy definition |
| Setup Script | `/home/coding/SEAM/declarative-config/infra/seam/setup-seam-openbao.sh` | Automated setup with verification |
| Documentation | `/home/coding/SEAM/docs/notes/openbao-seam-setup.md` | Detailed SEAM setup guide |
| Namespace | `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/namespace.yaml` | seam namespace definition |

### seam-retirement-evaluator Configuration

| File | Location | Purpose |
|------|----------|---------|
| ServiceAccount | `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/serviceaccount.yaml` | Evaluator ServiceAccount |
| Auth Config | `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-auth.yml` | Auth role documentation |
| Setup Workflow | `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-setup-job.yaml` | OpenBao setup WorkflowTemplate |
| Verify Workflow | `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-verify-workflow.yaml` | Access verification WorkflowTemplate |

---

## Conclusion

The research shows that **both OpenBao Kubernetes-auth configurations are already fully implemented and documented**. The existing setup demonstrates:

1. **Complete security isolation** between SEAM and seam-retirement-evaluator
2. **Consistent pattern** for role creation (ServiceAccount → Policy → Role)
3. **Multiple setup methods** (shell script vs Argo WorkflowTemplate)
4. **Comprehensive verification** of security boundaries
5. **Clear documentation** of all configurations

**No additional implementation is required** for the seam-retirement-evaluator OpenBao role – it exists and follows security best practices. The pattern established here can be reused for any future services requiring OpenBao access.

## References

- **Bead:** bf-37z98 (this research task)
- **SEAM Setup:** `/home/coding/SEAM/docs/notes/openbao-seam-setup.md`
- **SEAM Policy:** `/home/coding/SEAM/declarative-config/infra/seam/seam-openbao-policy.hcl`
- **Evaluator Setup:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-setup-job.yaml`
- **Evaluator Auth:** `/home/coding/SEAM/declarative-config/infra/seam-retirement-evaluator/openbao-auth.yml`
