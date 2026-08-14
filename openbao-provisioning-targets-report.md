# OpenBao Provisioning Target Resources Report

**Generated:** 2026-08-14  
**Cluster:** ardenone-cluster (OpenBao on rs-manager)  
**Purpose:** List expected provisioning targets and their existence status

## Expected Provisioning Targets

### 1. SEAM OpenBao Policy
- **Resource Type:** OpenB ACL Policy
- **Name:** `seam`
- **Location:** OpenBao policy engine
- **Purpose:** Grant read-only access to SEAM route secrets
- **Expected Path Access:** `secret/data/seam/routes/*`
- **Expected Capabilities:** `["read"]`
- **Explicit Denies:**
  - `secret/data/seam-retirement-evaluator/*` → `["deny"]`
  - `secret/data/*` (default-deny) → `["deny"]`

### 2. SEAM Kubernetes Auth Role
- **Resource Type:** Kubernetes Authentication Role
- **Full Path:** `auth/kubernetes/role/seam`
- **Bound ServiceAccount:** `seam` (in namespace `seam`)
- **Bound Namespace:** `seam`
- **Policies:** `seam`
- **Token TTL:** 24h
- **Token Max TTL:** 72h

### 3. Test Secret (for verification)
- **Resource Type:** KV v2 Secret
- **Path:** `secret/data/seam/routes/test-secret`
- **Purpose:** Verify SEAM role can read route secrets
- **Expected Data:**
  - `test_key`: test value
  - `description`: "Test secret for SEAM OpenBao role verification"

### 4. Production Route Secrets
- **Resource Type:** KV v2 Secrets
- **Path Pattern:** `secret/data/seam/routes/*`
- **Purpose:** Store API credentials, authentication tokens for routes defined in fragments
- **Expected Sources:** Route fragments with `x-vault-path` extensions

## Resource Status Summary

| Resource | Expected Location | Status | Notes |
|----------|------------------|--------|-------|
| Policy `seam` | OpenBao policies | UNKNOWN | Cannot verify without admin token |
| Role `seam` | `auth/kubernetes/role/seam` | UNKNOWN | Cannot verify without admin token |
| Test secret | `secret/data/seam/routes/test-secret` | UNKNOWN | Cannot verify without authentication |
| Route secrets | `secret/data/seam/routes/*` | UNKNOWN | No route fragments with vault paths found |

## Investigation Methodology

### What I Checked
1. ✅ OpenBao connectivity: **CONFIRMED** - Server responding at `http://openbao-ardenone.tail1b1987.ts.net:8200`
2. ✅ Documentation review: **COMPLETE** - All setup docs, templates, and scripts analyzed
3. ✅ Workflow execution logs: **REVIEWED** - Multiple OpenBao test workflows executed
4. ❌ Direct OpenBao query: **BLOCKED** - No admin token available

### Key Findings

#### Connectivity Status
- OpenBao server: **Running** (v2.5.1, initialized, unsealed)
- Network access: **CONFIRMED** via Tailscale
- API endpoint: **RESPONDING**

#### Authentication Status
- Admin token: **NOT AVAILABLE** (stored in password manager)
- Kubernetes auth: **CONFIGURED** (based on documentation)
- ServiceAccount `seam`: **UNKNOWN** (cluster access required)

#### Workflow Execution History
From recent workflow logs:
- `openbao-connectivity-debug-jc6pd`: ✅ **SUCCEEDED** (23m ago)
- `openbao-auth-debug-rlcw9`: ❌ **FAILED** (authentication error)
- Multiple `openbao-read-test-*` workflows: ❌ **FAILED** (exit code 1, retries exhausted)

### Configuration Files Analyzed
1. `/home/coding/SEAM/declarative-config/infra/seam/seam-openbao-policy.hcl` - Policy definition
2. `/home/coding/SEAM/declarative-config/infra/seam/setup-seam-openbao.sh` - Setup script
3. `/home/coding/SEAM/docs/notes/openbao-seam-setup.md` - Documentation
4. `/home/coding/SEAM/docs/openbao-workflow-templates.md` - Workflow templates

## Verification Steps Required

To complete this verification, the following steps are needed:

### Immediate Actions
1. **Obtain OpenBao admin token** from password manager
2. **Run setup script** if resources don't exist:
   ```bash
   export BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
   export BAO_TOKEN="<admin-token>"
   ./declarative-config/infra/seam/setup-seam-openbao.sh
   ```

### Direct OpenBao Queries (with admin token)
```bash
# Check if policy exists
bao policy read seam

# Check if role exists
bao read auth/kubernetes/role/seam

# List route secrets
bao kv get secret/seam/routes/test-secret
bao list secret/seam/routes/
```

### Kubernetes Resource Verification
```bash
# Check if ServiceAccount exists
kubectl get sa seam -n seam

# Check if namespace exists
kubectl get ns seam
```

## Security Model Requirements

The provisioning implements the hostile-fragment threat model:

### Policy Requirements
- ✅ **Read-only** on `seam/routes/*`
- ✅ **Explicit deny** on all other paths
- ✅ **No write capabilities**
- ✅ **No listing capabilities** outside allowed paths

### Isolation Requirements
- SEAM role cannot read `seam-retirement-evaluator/*`
- SEAM role cannot read `kalshi/*` or other tenant secrets
- SEAM role cannot read cluster kubeconfigs or other sensitive paths

## Missing Items

### Route Fragment Secrets
- **Current Status:** No route fragments in `/home/coding/SEAM/fragments/` contain `x-vault-path` extensions
- **Expected:** Route definitions should reference OpenBao secrets for authentication
- **Example Expected Format:**
  ```json
  {
    "x-seam-owner": "my-service",
    "x-upstream": "https://api.example.com",
    "x-vault-path": "secret/data/seam/routes/my-service-api-key",
    "paths": { "/api": { ... } }
  }
  ```

### Production Secrets
- **Current Status:** No production route secrets documented
- **Expected Phase:** Phase 6a implementation (in-pod authentication)

## Conclusion

**Overall Status: INCOMPLETE VERIFICATION**

The provisioning targets are well-documented and the setup scripts are complete, but direct verification is blocked by lack of OpenBao admin access. The OpenBao server is confirmed running and accessible, and workflow templates exist for provisioning operations.

### Next Steps
1. Obtain admin token from password manager
2. Run verification queries to confirm resource existence
3. Execute setup script if resources are missing
4. Create route secrets when fragments require external authentication
5. Implement Phase 6a in-pod authentication

### Confidence Assessment
- OpenBao availability: **HIGH** (confirmed via health check)
- Policy/Role existence: **MEDIUM** (scripts exist, execution unconfirmed)
- Secret existence: **LOW** (no direct verification possible)
- Route fragment integration: **LOW** (no vault paths in current fragments)

---

**Report Generated by:** SEAM Provisioning Target Verification  
**Task:** bf-4o9s9 - Read and list provisioning target resources  
**Method:** Documentation analysis, workflow log review, connectivity testing
