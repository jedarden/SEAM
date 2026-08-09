# OpenBao Setup for SEAM

## Precondition Status

**Bead:** `bf-5rx9`

This document describes the OpenBao role and policy that must be created in OpenBao before SEAM can authenticate and read route secrets.

## What Was Created

### 1. SEAM Policy (`seam-openbao-policy.hcl`)

**Location:** `/home/coding/SEAM/declarative-config/infra/seam/seam-openbao-policy.hcl`

**Policy:**
```hcl
# Allow reading SEAM route secrets ONLY
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

**Critical Security Properties:**
- **Read-only:** SEAM can only read, never write secrets
- **Namespace-scoped:** Only `seam/routes/*` is accessible
- **Explicit deny:** All other paths are explicitly denied, including:
  - `seam-retirement-evaluator/*` (evaluator's GitHub token)
  - `kalshi/*` (Kalshi credentials)
  - `armor/*` (Armor credentials)
  - Any other tenant's material
  - Cluster kubeconfigs

### 2. Setup Script (`setup-seam-openbao.sh`)

**Location:** `/home/coding/SEAM/declarative-config/infra/seam/setup-seam-openbao.sh`

**What it does:**
1. Writes the SEAM policy to OpenBao
2. Creates Kubernetes auth role `seam` bound to ServiceAccount `seam` in namespace `seam`
3. Creates test secret at `seam/routes/test-secret`
4. Verifies the role can read the test secret and is denied elsewhere

**Usage:**
```bash
export BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
export BAO_TOKEN="hvs.xxxxx"  # Admin token
./declarative-config/infra/seam/setup-seam-openbao.sh
```

### 3. Kubernetes Auth Role Specification

**Role name:** `seam`
**Bound ServiceAccount:** `seam` (in namespace `seam`)
**Policies:** `seam`
**Token TTL:** 24h
**Token Max TTL:** 72h

## How to Apply

### Option 1: Using the Setup Script (Recommended)

```bash
cd /home/coding/SEAM/declarative-config/infra/seam
export BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
export BAO_TOKEN="<your-admin-token>"
./setup-seam-openbao.sh
```

### Option 2: Manual OpenBao API

```bash
# Set environment variables
export BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
export BAO_TOKEN="<your-admin-token>"

# Write the policy
bao policy write seam seam-openbao-policy.hcl

# Create the Kubernetes auth role
bao write auth/kubernetes/role/seam \
  bound_service_account_names=seam \
  bound_service_account_namespaces=seam \
  policies=seam \
  ttl=24h \
  max_ttl=72h
```

### Option 3: Using curl directly

```bash
# Set variables
BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
BAO_TOKEN="<your-admin-token>"
H="-H X-Vault-Token:${BAO_TOKEN}"

# Write policy
POLICY='
path "secret/data/seam/routes/*" {
  capabilities = ["read"]
}
path "secret/data/seam-retirement-evaluator/*" {
  capabilities = ["deny"]
}
path "secret/data/*" {
  capabilities = ["deny"]
}
'
curl -s ${H} "${BAO_ADDR}/v1/sys/policies/acl/seam" \
  -X PUT -d "{\"policy\": $(echo "$POLICY" | jq -Rs .)}"

# Create role
curl -s ${H} "${BAO_ADDR}/v1/auth/kubernetes/role/seam" \
  -X POST -d '{
    "bound_service_account_names": ["seam"],
    "bound_service_account_namespaces": ["seam"],
    "policies": ["seam"],
    "ttl": "24h",
    "max_ttl": "72h"
  }'
```

## Verification

After applying the setup, the script automatically verifies:

1. **Can read** `seam/routes/test-secret` → SUCCESS
2. **Cannot read** `seam-retirement-evaluator/*` → DENIED
3. **Cannot read** `kalshi/*` → DENIED
4. **Cannot list** `secret/` → DENIED

## What Still Needs to Happen (6a Deliverable)

This precondition creates the **server-side** OpenBao role and policy. The **cluster-side** work is part of Phase 6a:

1. Create ServiceAccount `seam` in namespace `seam`
2. Configure SEAM deployment with projected service account token volume
3. Implement in-pod OpenBao login using Kubernetes auth
4. Test first successful login proof

## Threat Model

The hostile-fragment threat model requires that:
1. SEAM's OpenBao token has **literally no access** outside `seam/routes/*`
2. A malicious fragment author cannot exfiltrate other secrets via `x-vault-path`
3. Even if lint is bypassed, the gateway's token cannot reach other paths

This is enforced at **two levels**:
1. **OpenBao policy** (this precondition) – token capabilities are bounded at the source
2. **Gateway validation** – runtime re-check of `x-vault-path` against allowlist

## Notes

- OpenBao root tokens are stored in a password manager (see `openbao-dr-runbook.md`)
- Kubernetes auth method must already be enabled in OpenBao (`auth/kubernetes/`)
- The namespace `seam` and ServiceAccount `seam` will be created in Phase 6a
- This setup is **server-side only** – no cluster resources are created
