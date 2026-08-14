# OpenBao Resource Verification Report

**Task:** bf-143mj - Verify OpenBao resources created in OpenBao secret store  
**Date:** 2026-08-14  
**Cluster:** ardenone-cluster (OpenBao on rs-manager)  
**Method:** CLI connectivity checks, Kubernetes proxy verification, direct API queries

---

## Executive Summary

**Overall Status:** ❌ **VERIFICATION INCOMPLETE - AUTHENTICATION BLOCKED**

OpenBao infrastructure is confirmed operational, but actual resource verification is blocked by missing authentication credentials. While the server is running and accessible, I cannot confirm whether the expected provisioning resources (policies, roles, secrets) were actually created.

---

## 1. OpenBao Infrastructure Status

### 1.1 Server Connectivity
```bash
$ bao status -address=http://openbao-ardenone.tail1b1987.ts.net:8200

Key             Value
---             -----
Seal Type       shamir
Initialized     true
Sealed          false
Total Shares    1
Threshold       1
Version         2.5.1
Build Date      2026-02-23T17:30:29Z
Storage Type     file
Cluster Name    vault-cluster-55c6b026
Cluster ID      48cef8cd-d360-7864-914d-ffc46fb7071a
HA Enabled      false
```

**Verdict:** ✅ OpenBao server is running, initialized, and unsealed

### 1.2 Network Access
- **Endpoint:** `http://openbao-ardenone.tail1b1987.ts.net:8200`
- **Access Method:** Tailscale VPN
- **Status:** ✅ CONFIRMED - Server responding on expected endpoint

---

## 2. Authentication Attempts

### 2.1 Stored Token Check
```bash
$ cat /home/coding/.vault-token
test-token
```

**Status:** ❌ Placeholder token, not a valid OpenBao root token

### 2.2 Authentication Attempt
```bash
$ bao login -address=http://openbao-ardenone.tail1b1987.ts.net:8200 token=$(cat /home/coding/.vault-token)

Error authenticating: error looking up token: Error making API request.
URL: GET http://openbao-ardenone.tail1b1987.ts.net:8200/v1/auth/token/lookup-self
Code: 403. Errors:
* permission denied
```

**Verdict:** ❌ Cannot authenticate - token is invalid/placeholder

---

## 3. Kubernetes Resources Verification

### 3.1 Namespace Check
```bash
$ kubectl --server=http://traefik-ardenone-manager:8001 get ns seam
NAME   STATUS   AGE
seam   Active   27d
```

**Verdict:** ✅ Namespace `seam` exists

### 3.2 ServiceAccount Check
```bash
$ kubectl --server=http://traefik-ardenone-manager:8001 get sa -n seam
No resources found in seam namespace.
```

**Verdict:** ❌ **CONFIRMED MISSING** - No ServiceAccounts in seam namespace

**Impact:** Without ServiceAccounts, Kubernetes-based authentication cannot function. This blocks:
- SEAM server from authenticating to OpenBao
- seam-retirement-evaluator from accessing secrets
- JWT token-based login workflows

**Required ServiceAccounts (from setup script):**
- `seam` - For SEAM gateway authentication
- `seam-retirement-evaluator` - For retirement evaluator authentication

---

## 4. Expected OpenBao Resources (From Configuration)

Based on the setup script and policy definition, the following resources should exist:

### 4.1 OpenBao Policy: `seam`
**Location:** Policy engine  
**Purpose:** Grant read-only access to SEAM route secrets  
**Expected Configuration:**
```hcl
path "secret/data/seam/routes/*" {
  capabilities = ["read"]
}
path "secret/data/evaluators/*" {
  capabilities = ["deny"]
}
path "secret/data/*" {
  capabilities = ["deny"]
}
```

**Verification Status:** ❌ CANNOT VERIFY (requires admin token)

### 4.2 Kubernetes Auth Role: `seam`
**Full Path:** `auth/kubernetes/role/seam`  
**Expected Configuration:**
- bound_service_account_names: `seam`
- bound_service_account_namespaces: `seam`
- policies: `seam`
- ttl: `24h`
- max_ttl: `72h`

**Verification Status:** ❌ CANNOT VERIFY (requires admin token)

### 4.3 Test Secret
**Expected Path:** `secret/seam/routes/test-secret`  
**Expected Data:**
- `test_key`: `test_value_<timestamp>`
- `description`: `Test secret for SEAM OpenBao role verification`

**Verification Status:** ❌ CANNOT VERIFY (requires authentication)

---

## 5. Verification Methodology

### 5.1 What Was Successfully Verified ✅
1. OpenBao server availability (running, initialized, unsealed)
2. OpenBao server accessibility via Tailscale
3. Kubernetes namespace `seam` existence
4. ServiceAccount absence (confirmed empty list)

### 5.2 What Could Not Be Verified ❌
1. OpenBao policies (requires admin token or successful authentication)
2. OpenBao roles (requires admin token or successful authentication)
3. Secret existence (requires authentication to read)
4. Secret values (requires authentication to read)
5. Kubernetes auth method configuration (requires admin token)

### 5.3 Verification Blockers
1. **No valid admin token** - Stored token is a placeholder
2. **Missing ServiceAccounts** - Cannot use Kubernetes auth without SAs
3. **No alternative auth methods** - Only Kubernetes auth configured for this use case

---

## 6. Discrepancies Found

### 6.1 Critical Discrepancy: Missing ServiceAccounts
**Expected:** ServiceAccounts `seam` and `seam-retirement-evaluator` in namespace `seam`  
**Actual:** Zero ServiceAccounts in namespace `seam`  
**Impact:** Complete authentication block for SEAM workloads

### 6.2 Unknown Status: OpenBao Resources
**Expected:** Policies, roles, and test secret created by setup workflow  
**Actual:** Cannot verify - authentication blocked  
**Possible Explanations:**
- Setup workflow never executed
- Setup workflow executed but failed silently
- Resources exist but cannot be verified without authentication

### 6.3 Authentication Token
**Expected:** Valid OpenBao admin token in `~/.vault-token`  
**Actual:** Placeholder string "test-token"  
**Impact:** Cannot perform administrative queries or resource verification

---

## 7. Verification Output

### 7.1 Server Status
```
✅ OpenBao server: RUNNING (v2.5.1, initialized, unsealed)
✅ Network access: CONFIRMED (Tailscale endpoint reachable)
✅ Cluster: vault-cluster-55c6b026
```

### 7.2 Kubernetes Resources
```
✅ Namespace: seam (exists)
❌ ServiceAccount: seam (MISSING)
❌ ServiceAccount: seam-retirement-evaluator (MISSING)
❌ Total ServiceAccounts in seam namespace: 0
```

### 7.3 OpenBao Resources
```
❓ Policy: seam (UNKNOWN - cannot verify)
❓ Role: auth/kubernetes/role/seam (UNKNOWN - cannot verify)
❓ Secret: secret/seam/routes/test-secret (UNKNOWN - cannot verify)
```

---

## 8. Comparison with Expected State

| Resource | Expected Location | Expected State | Actual State | Verification Method |
|----------|------------------|----------------|--------------|---------------------|
| OpenBao server | `http://openbao-ardenone.tail1b1987.ts.net:8200` | Running | ✅ Running | Status API |
| Namespace `seam` | Kubernetes cluster | Exists | ✅ Exists | kubectl proxy |
| SA `seam` | `seam` namespace | Exists | ❌ Missing | kubectl proxy |
| SA `seam-retirement-evaluator` | `seam` namespace | Exists | ❌ Missing | kubectl proxy |
| Policy `seam` | OpenBao policy engine | Exists | ❓ Unknown | BLOCKED |
| Role `seam` | `auth/kubernetes/role/seam` | Exists | ❓ Unknown | BLOCKED |
| Test secret | `secret/seam/routes/test-secret` | Exists | ❓ Unknown | BLOCKED |

---

## 9. Security Model Verification

### 9.1 Expected Isolation (From Policy Definition)
- ✅ Read-only access on `seam/routes/*`
- ✅ Explicit deny on `seam-retirement-evaluator/*`
- ✅ Explicit deny on all other paths
- ✅ No write capabilities
- ✅ No listing capabilities outside allowed paths

### 9.2 Actual Compliance Status
**Status:** ❌ **CANNOT VERIFY**

The policy definition is properly configured for the hostile-fragment threat model, but actual policy enforcement cannot be tested without:
1. Confirming the policy exists in OpenBao
2. Testing read access with a SEAM role token
3. Testing denial on non-allowed paths

---

## 10. Recommendations

### 10.1 Immediate Actions Required

1. **Create ServiceAccounts** (via declarative-config):
   ```yaml
   # declarative-config/k8s/rs-manager/seam/serviceaccounts.yaml
   apiVersion: v1
   kind: ServiceAccount
   metadata:
     name: seam
     namespace: seam
   ---
   apiVersion: v1
   kind: ServiceAccount
   metadata:
     name: seam-retirement-evaluator
     namespace: seam
   ```

2. **Obtain Admin Token**: Retrieve OpenBao root token from password manager

3. **Execute Setup Script**:
   ```bash
   export BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
   export BAO_TOKEN="<actual-root-token>"
   cd /home/coding/SEAM/declarative-config/infra/seam
   ./setup-seam-openbao.sh
   ```

### 10.2 Verification Actions (After Setup)

1. **Verify Policy Exists**:
   ```bash
   bao policy read seam
   ```

2. **Verify Role Exists**:
   ```bash
   bao read auth/kubernetes/role/seam
   ```

3. **Verify Test Secret**:
   ```bash
   bao kv get secret/seam/routes/test-secret
   ```

4. **Test Authentication**:
   ```bash
   # From a pod using seam ServiceAccount
   JWT=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)
   bao write auth/kubernetes/login role=seam jwt="$JWT"
   ```

---

## 11. Conclusion

### Summary
OpenBao infrastructure is confirmed operational and accessible, but resource verification is blocked by missing prerequisites. The verification workflow cannot proceed without ServiceAccounts and valid authentication credentials.

### Key Findings
1. ✅ OpenBao server is confirmed running and accessible
2. ✅ Kubernetes namespace `seam` exists
3. ❌ ServiceAccounts are **confirmed missing** (not just unknown)
4. ❓ OpenBao resources (policies, roles, secrets) remain unknown

### Final Verdict
**INFRASTRUCTURE READY, RESOURCES NOT VERIFIED, AUTHENTICATION BLOCKED**

The write-side of the OpenBao integration cannot be confirmed. Either:
- The setup workflows were never executed, OR
- The setup workflows failed silently, OR
- Resources exist but cannot be verified without authentication

### Path Forward
1. Create missing ServiceAccounts via declarative-config
2. Obtain admin token from password manager
3. Execute setup script to create OpenBao resources
4. Re-run verification to confirm resource creation

---

**Report Generated By:** Task bf-143mj  
**Verification Date:** 2026-08-14  
**Status:** ❌ VERIFICATION INCOMPLETE  
**Blocker:** Authentication credentials and prerequisite ServiceAccounts
