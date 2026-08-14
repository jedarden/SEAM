# OpenBao Resource Verification Report

**Generated:** 2026-08-14 07:06 UTC  
**Task:** bf-1chb9 - Verify OpenBao resources created successfully  
**Cluster:** ardenone-cluster (OpenBao on rs-manager)  
**Method:** Direct API queries and Kubernetes proxy access

---

## Executive Summary

**Overall Status:** ❌ **VERIFICATION FAILED - RESOURCES MISSING**

The actual OpenBao state verification reveals that critical resources are **missing**, not merely unverified. The OpenBao server is running and accessible, but the expected provisioning targets do not exist.

**Key Finding:** Without an admin token or proper Kubernetes authentication, I cannot create or verify OpenBao policies and roles. However, I can confirm that prerequisite Kubernetes resources (ServiceAccounts) are missing, which blocks authentication-based verification.

---

## 1. OpenBao Infrastructure Status

### 1.1 Server Availability
- **Endpoint:** `http://openbao-ardenone.tail1b1987.ts.net:8200`
- **Status:** ✅ **RUNNING**
- **Version:** v2.5.1
- **Initialized:** ✅ Yes
- **Sealed:** ✅ No (unsealed)
- **Cluster Name:** `vault-cluster-55c6b026`
- **Cluster ID:** `48cef8cd-d360-7864-914d-ffc46fb7071a`

```bash
$ curl -s http://openbao-ardenone.tail1b1987.ts.net:8200/v1/sys/health | jq
{
  "initialized": true,
  "sealed": false,
  "standby": false,
  "performance_standby": false,
  "replication_performance_mode": "disabled",
  "replication_dr_mode": "disabled",
  "server_time_utc": 1786705407,
  "version": "2.5.1",
  "cluster_name": "vault-cluster-55c6b026",
  "cluster_id": "48cef8cd-d360-7864-914d-ffc46fb7071a"
}
```

**Verdict:** OpenBao infrastructure is operational.

---

## 2. Kubernetes Resources Verification

### 2.1 Namespace: `seam`
- **Expected Location:** ardenone-cluster
- **Status:** ✅ **EXISTS**
- **Verification Method:** Kubernetes API proxy
- **Resource Version:** 615076544

```bash
$ curl -s "http://traefik-ardenone-manager:8001/api/v1/namespaces/seam" | jq
{
  "kind": "Namespace",
  "metadata": {
    "name": "seam",
    "resourceVersion": "615076544"
  }
}
```

### 2.2 ServiceAccount: `seam`
- **Expected Location:** Namespace `seam`
- **Status:** ❌ **MISSING**
- **Impact:** SEAM server cannot authenticate to OpenBao via Kubernetes auth
- **Verification Method:** Kubernetes API proxy

```bash
$ curl -s "http://traefik-ardenone-manager:8001/api/v1/namespaces/seam/serviceaccounts" | jq
{
  "kind": "ServiceAccountList",
  "items": []  # <-- Empty! No ServiceAccounts exist
}
```

### 2.3 ServiceAccount: `seam-retirement-evaluator`
- **Expected Location:** Namespace `seam`
- **Status:** ❌ **MISSING**
- **Impact:** Retirement evaluator cannot authenticate to GitHub or VictoriaMetrics
- **Verification Method:** Kubernetes API proxy
- **Evidence:** Same empty ServiceAccount list as above

### 2.4 Other Required ServiceAccounts
- **ServiceAccount:** `argo-workflow` (in `argo-workflows` namespace)
- **Status:** ⚠️ **NOT CHECKED** (out of scope for this verification)
- **Note:** Required for workflow-based provisioning

---

## 3. OpenBao Resources Status

### 3.1 Kubernetes Authentication Method
- **Path:** `auth/kubernetes`
- **Status:** ⚠️ **CONFIGURATION UNKNOWN** (cannot check without admin token)
- **Expected Behavior:** Should validate JWT tokens from ServiceAccounts
- **Verification Blocker:** Requires admin token or successful Kubernetes login

### 3.2 OpenBao Role: `seam`
- **Full Path:** `auth/kubernetes/role/seam`
- **Bound ServiceAccount:** `seam` (namespace: `seam`)
- **Status:** ❌ **CANNOT VERIFY** (cannot check without admin token)
- **Expected Configuration:**
  ```yaml
  bound_service_account_names: ["seam"]
  bound_service_account_namespaces: ["seam"]
  policies: ["seam"]
  token_ttl: "24h"
  token_max_ttl: "72h"
  ```

### 3.3 OpenBao Role: `seam-retirement-evaluator`
- **Full Path:** `auth/kubernetes/role/seam-retirement-evaluator`
- **Bound ServiceAccount:** `seam-retirement-evaluator` (namespace: `seam`)
- **Status:** ❌ **CANNOT VERIFY** (cannot check without admin token)
- **Expected Configuration:**
  ```yaml
  bound_service_account_names: ["seam-retirement-evaluator"]
  bound_service_account_namespaces: ["seam"]
  policies: ["seam-retirement-evaluator-policy"]
  token_ttl: "24h"
  token_max_ttl: "72h"
  ```

### 3.4 OpenBao Policy: `seam`
- **Expected Path:** Policy engine (sys/policy)
- **Status:** ❌ **CANNOT VERIFY** (cannot check without admin token)
- **Expected Capabilities:**
  ```hcl
  path "secret/data/seam/routes/*" {
    capabilities = ["read"]
  }
  path "secret/data/seam-retirement-evaluator/*" {
    capabilities = ["deny"]
  }
  path "secret/data/*" {
    capabilities = ["deny"]
  }
  ```

### 3.5 OpenBao Policy: `seam-retirement-evaluator-policy`
- **Status:** ❌ **CANNOT VERIFY** (cannot check without admin token)
- **Expected Capabilities:**
  ```hcl
  path "secret/data/evaluators/seam-retirement-evaluator/*" {
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
  ```

---

## 4. Secret Resources Verification

### 4.1 Test Secret: `secret/data/seam/routes/test-secret`
- **Expected Path:** `secret/data/seam/routes/test-secret`
- **Purpose:** Verify SEAM role read permissions
- **Expected Data:**
  - `test_key`: "test value"
  - `description`: "Test secret for SEAM OpenBao role verification"
- **Status:** ❌ **CANNOT VERIFY** (requires authentication)
- **Verification Blocker:** Cannot login without ServiceAccount and role

### 4.2 GitHub Token: `secret/data/evaluators/seam-retirement-evaluator/github-token`
- **Expected Path:** `secret/data/evaluators/seam-retirement-evaluator/github-token`
- **Purpose:** Store GitHub PAT for retirement evaluator
- **Expected Fields:**
  - `token`: GitHub Personal Access Token
- **Status:** ❌ **CANNOT VERIFY** (requires authentication)
- **Verification Blocker:** Cannot login without ServiceAccount and role

### 4.3 VictoriaMetrics Credentials: `secret/data/monitoring/victoriametrics/readonly-credentials`
- **Expected Path:** `secret/data/monitoring/victoriametrics/readonly-credentials`
- **Purpose:** Store VictoriaMetrics read-only credentials
- **Expected Fields:**
  - `username`: Read-only username
  - `password`: Read-only password
  - `endpoint`: VictoriaMetrics URL
- **Status:** ❌ **CANNOT VERIFY** (requires authentication)
- **Verification Blocker:** Cannot login without ServiceAccount and role

### 4.4 Production Route Secrets: `secret/data/seam/routes/*`
- **Expected Pattern:** `secret/data/seam/routes/*`
- **Purpose:** Store API credentials for external route authentication
- **Status:** ❌ **NOT YET IMPLEMENTED** (expected for Phase 6a)
- **Current State:** No route fragments with `x-vault-path` extensions exist

---

## 5. Authentication Testing Attempts

### 5.1 Kubernetes Auth Login Attempt
- **Method:** POST to `/v1/auth/kubernetes/login` with role `seam`
- **Result:** ❌ **FAILED** - Missing JWT token
- **Error:** `{"errors":["missing jwt"]}`

```bash
$ curl -s -X POST "http://openbao-ardenone.tail1b1987.ts.net:8200/v1/auth/kubernetes/login" \
  -H "Content-Type: application/json" \
  -d '{"role": "seam"}'
{"errors":["missing jwt"]}
```

### 5.2 JWT Token Source
- **Expected Source:** ServiceAccount JWT token from pod
- **Current Context:** Not running in a Kubernetes pod
- **Workaround:** Could extract token from pod via `kubectl`
- **Blocker:** No running SEAM pods to extract token from

---

## 6. Verification Methodology Summary

### 6.1 What I Verified ✅
1. **OpenBao server health** - Confirmed running, initialized, unsealed
2. **Namespace existence** - `seam` namespace exists
3. **ServiceAccount absence** - Confirmed NO ServiceAccounts in `seam` namespace
4. **Network connectivity** - OpenBao endpoint accessible via Tailscale

### 6.2 What I Could Not Verify ❌
1. **OpenBao roles** - Cannot check without admin token or successful login
2. **OpenBao policies** - Cannot check without admin token
3. **Secret presence** - Cannot read without authentication
4. **Secret values** - Cannot verify without authentication
5. **Kubernetes auth configuration** - Cannot check without admin token

### 6.3 Verification Blockers
1. **No admin token** - Cannot directly query OpenBao resources
2. **Missing ServiceAccounts** - Cannot authenticate via Kubernetes auth
3. **No running pods** - Cannot extract JWT tokens for authentication
4. **Expired kubeconfig** - Admin kubeconfig rejected by API server

---

## 7. Missing Resources Summary

| Resource | Expected Location | Status | Impact |
|----------|------------------|--------|---------|
| ServiceAccount `seam` | `seam` namespace | ❌ MISSING | Blocks SEAM server OpenBao auth |
| ServiceAccount `seam-retirement-evaluator` | `seam` namespace | ❌ MISSING | Blocks evaluator OpenBao auth |
| OpenBao role `seam` | `auth/kubernetes/role/seam` | ⚠️ UNKNOWN | Cannot verify without SA |
| OpenBao role `seam-retirement-evaluator` | `auth/kubernetes/role/seam-retirement-evaluator` | ⚠️ UNKNOWN | Cannot verify without SA |
| OpenBao policy `seam` | Policy engine | ⚠️ UNKNOWN | Cannot verify without admin token |
| OpenBao policy `seam-retirement-evaluator-policy` | Policy engine | ⚠️ UNKNOWN | Cannot verify without admin token |
| Test secret `secret/data/seam/routes/test-secret` | KV v2 secrets | ⚠️ UNKNOWN | Cannot verify without authentication |
| GitHub token secret | `secret/data/evaluators/...` | ⚠️ UNKNOWN | Cannot verify without authentication |
| VictoriaMetrics credentials | `secret/data/monitoring/...` | ⚠️ UNKNOWN | Cannot verify without authentication |

---

## 8. Resource Structure Analysis

### 8.1 Expected vs. Actual

**What Should Exist (from documentation):**
1. Kubernetes ServiceAccounts for identity
2. OpenBao Kubernetes auth roles for policy binding
3. OpenBao policies for authorization rules
4. KV v2 secrets for credential storage

**What Actually Exists:**
1. ✅ OpenBao server (running)
2. ✅ `seam` namespace (exists)
3. ❌ ServiceAccounts (NONE exist in seam namespace)
4. ❓ OpenBao roles (cannot verify)
5. ❓ OpenBao policies (cannot verify)
6. ❓ Secrets (cannot verify)

### 8.2 Schema Compliance

Based on the documentation analysis:
- ✅ Policy HCL syntax is correct
- ✅ Role configuration follows OpenBao Kubernetes auth pattern
- ✅ Secret paths follow KV v2 convention (`secret/data/...`)
- ⚠️ Actual resources cannot be verified for compliance

---

## 9. Security Model Compliance

### 9.1 Expected Isolation (From Documentation)
- SEAM role: read-only `seam/routes/*`, deny all else
- Evaluator role: read-only `evaluators/*` and `monitoring/*`, deny routes
- Both roles: explicit deny on all other paths

### 9.2 Actual Compliance Status
**Status:** ❌ **CANNOT VERIFY**

Without the ability to query roles and policies, I cannot verify:
- Whether policies exist
- Whether policies are correctly configured
- Whether isolation is enforced
- Whether least-privilege is granted

---

## 10. Workflow Execution History Context

### 10.1 Previous Verification Attempts

From the comprehensive reports:

**Successful Workflows:**
- `openbao-connectivity-debug-jc6pd` ✅ - Confirmed OpenBao connectivity

**Failed Workflows:**
- `evaluator-openbao-read-test-8thj8` ❌ - ServiceAccount not found
- `openbao-auth-debug-rlcw9` ❌ - Authentication error
- Multiple `openbao-read-test-*` ❌ - Exit code 1, retries exhausted

### 10.2 Pattern Analysis

All authentication-based workflows fail consistently. This strongly suggests:
1. ServiceAccounts are missing (CONFIRMED in this verification)
2. OpenBao Kubernetes auth roles may be missing or misconfigured
3. JWT token validation is failing

---

## 11. Root Cause Analysis

### 11.1 Primary Blocker
**Missing ServiceAccounts** in the `seam` namespace

Without ServiceAccounts, the authentication chain is broken:
1. No ServiceAccount → No JWT token → Cannot login via Kubernetes auth
2. Cannot login → Cannot obtain client token → Cannot verify resources
3. Without verification → Cannot confirm provisioning success

### 11.2 Secondary Blockers
1. **No admin token** - Cannot bypass auth to check resources directly
2. **Expired admin kubeconfig** - Cannot manage cluster resources
3. **No running SEAM pods** - Cannot extract tokens from live workloads

---

## 12. Recommended Remediation

### 12.1 Immediate Actions (Priority 1)

**Create Missing ServiceAccounts:**
```bash
# Via ArgoCD (correct method - declarative-config)
# Add to: declarative-config/k8s/rs-manager/seam/serviceaccount.yaml
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

Then push to Forgejo and wait for ArgoCD sync.

### 12.2 Verification Actions (Priority 2)

**After ServiceAccounts exist, verify OpenBao resources:**

Option A: **With admin token** (preferred)
```bash
export BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
export BAO_TOKEN="<admin-token>"

# Check roles
bao read auth/kubernetes/role/seam
bao read auth/kubernetes/role/seam-retirement-evaluator

# Check policies
bao policy read seam
bao policy read seam-retirement-evaluator-policy

# Check secrets
bao kv get secret/seam/routes/test-secret
```

Option B: **With Kubernetes auth** (after SA creation)
```bash
# From a pod in seam namespace using SA 'seam'
JWT=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)
curl -X POST "$BAO_ADDR/v1/auth/kubernetes/login" \
  -H "Content-Type: application/json" \
  -d "{\"role\": \"seam\", \"jwt\": \"$JWT\"}"

# Use returned token to query resources
```

### 12.3 Missing Resource Creation (Priority 3)

If verification shows resources are missing:
1. Run setup script: `./declarative-config/infra/seam/setup-seam-openbao.sh`
2. Execute setup workflow: `seam-retirement-evaluator-openbao-setup`
3. Verify with: `seam-retirement-evaluator-verify-openbao`

---

## 13. Confidence Assessment

| Resource Category | Confidence Level | Evidence |
|-------------------|------------------|----------|
| OpenBao availability | **HIGH** ✅ | Direct health check succeeded |
| Namespace existence | **HIGH** ✅ | Kubernetes API confirmed |
| ServiceAccount absence | **HIGH** ✅ | API returned empty list |
| OpenBao roles | **LOW** ❌ | Cannot query without auth |
| OpenBao policies | **LOW** ❌ | Cannot query without auth |
| Secret existence | **LOW** ❌ | Cannot query without auth |
| Secret values | **UNKNOWN** ❌ | Cannot query without auth |
| Policy compliance | **UNKNOWN** ❌ | Cannot verify without access |

---

## 14. Conclusion

### Summary
The OpenBao server is operational and the `seam` namespace exists, but **critical Kubernetes resources are missing**. The absence of ServiceAccounts (`seam` and `seam-retirement-evaluator`) blocks all authentication-based verification. Without authentication, I cannot verify the presence of OpenBao roles, policies, or secrets.

### Key Findings
1. ✅ OpenBao infrastructure is confirmed working
2. ✅ Kubernetes namespace `seam` exists
3. ❌ ServiceAccounts are **confirmed missing** (not just unknown)
4. ❓ OpenBao resources (roles, policies, secrets) remain unknown

### Final Verdict
**INFRASTRUCTURE READY, RESOURCES NOT CREATED, VERIFICATION BLOCKED**

The provisioning side effects **did not happen** or cannot be confirmed. The missing ServiceAccounts suggest either:
- The setup workflows were never executed, OR
- The setup workflows failed silently, OR
- The ServiceAccounts were manually deleted

### Path Forward
1. Create ServiceAccounts via declarative-config (preferred method)
2. Obtain admin token for direct OpenBao verification
3. Execute setup workflows to create missing OpenBao resources
4. Re-run verification to confirm all resources exist

---

**Report Generated By:** Task bf-1chb9  
**Method:** Direct API queries + Kubernetes proxy access  
**Date:** 2026-08-14 07:06 UTC  
**Status:** ❌ VERIFICATION FAILED - RESOURCES MISSING
