# OpenBao Role Configuration: seam-retirement-evaluator

**Status:** ❌ **ROLE NOT FOUND - Configuration is Template/Expected Only**

**Documentation Date:** 2026-08-16  
**Bead:** seam-d70c5345  
**OpenBao Server:** `http://openbao-ardenone.tail1b1987.ts.net:8200`

---

## Executive Summary

The `seam-retirement-evaluator` OpenBao role **does not currently exist** in the OpenBao server. This document describes the **expected configuration** based on the provisioning workflow template `seam-retirement-evaluator-openbao-setup`, which has been defined but not successfully executed.

**Finding:** Role configuration is documented but not provisioned. The setup workflow exists but has not been executed successfully, leaving the role absent from OpenBao.

---

## Expected Role Configuration

### Role Identity
- **Name:** `seam-retirement-evaluator`
- **Authentication Method:** Kubernetes JWT authentication
- **Expected Path:** `auth/kubernetes/role/seam-retirement-evaluator`
- **Status:** ❌ **NOT FOUND** (as of 2026-08-16)

### Kubernetes Authentication Binding
```bash
bao write auth/kubernetes/role/seam-retirement-evaluator \
  bound_service_account_names=seam-retirement-evaluator \
  bound_service_account_namespaces=seam \
  policies=seam-retirement-evaluator-policy \
  token_ttl=24h \
  token_max_ttl=72h \
  token_default_policies=seam-retirement-evaluator-policy
```

#### Service Account Binding
- **ServiceAccount:** `seam-retirement-evaluator`
- **Namespace:** `seam`
- **Binding Type:** Exact match on SA name and namespace

#### Token Lifecycle
- **Token TTL:** 24 hours (default token lifetime)
- **Token Max TTL:** 72 hours (maximum allowed token lifetime)
- **Default Policies:** `seam-retirement-evaluator-policy`

---

## Attached Policy: seam-retirement-evaluator-policy

### Policy Source
This policy is defined in the workflow template and would be created during setup execution:

```hcl
# OpenBao HCL policy for seam-retirement-evaluator
# Allows read access to evaluator's own GitHub token path and VictoriaMetrics credentials
# Explicitly denies access to seam/routes/* to ensure SEAM cannot read evaluator's token

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

### Policy Breakdown

| Path Pattern | Capabilities | Purpose |
|--------------|-------------|---------|
| `secret/data/evaluators/seam-retirement-evaluator/*` | `read` | Allow evaluator to read its own credentials (GitHub token) |
| `secret/data/monitoring/victoriametrics/*` | `read` | Allow metrics query access to VictoriaMetrics |
| `secret/data/seam/routes/*` | `deny` | Explicitly deny SEAM route secrets (security isolation) |
| `secret/data/*` | `deny` | Deny all other secrets by default |

### Security Model
- **Isolation:** Evaluator cannot read SEAM's route secrets
- **Self-Service:** Evaluator can only read its own credentials from the dedicated evaluators path
- **Least Privilege:** Read-only access, no write or list capabilities
- **Default Deny:** All other paths explicitly denied

---

## Expected Secrets Structure

### 1. GitHub Token
- **Path:** `secret/evaluators/seam-retirement-evaluator/github-token`
- **Purpose:** GitHub Personal Access Token for retirement evaluator workflows
- **Fields:**
  - `token`: GitHub PAT (expected format: `ghp_`, `gho_`, or `ghu_` prefix)
  - `created_by`: "seam-retirement-evaluator-setup-workflow"
  - `created_on`: ISO 8601 timestamp

**Status:** ❌ **NOT FOUND** (placeholder or missing)

### 2. VictoriaMetrics Credentials
- **Path:** `secret/monitoring/victoriametrics/readonly-credentials`
- **Purpose:** Read-only credentials for VictoriaMetrics queries
- **Fields:**
  - `username`: (empty for internal auth)
  - `password`: (empty for internal auth)
  - `endpoint`: `http://victorialogs-single-ardenone-manager-vector-headless.monitoring.svc.cluster.local:8428`
  - `note`: Explains that VictoriaMetrics uses internal auth

**Status:** ❌ **NOT FOUND** (placeholder or missing)

---

## Provisioning Workflow

### Workflow Template
- **Name:** `seam-retirement-evaluator-openbao-setup`
- **Namespace:** `argo-workflows`
- **Purpose:** Automated provisioning of role, policy, and secret placeholders

### Setup Steps (Expected)
1. Load the `seam-retirement-evaluator-policy` HCL policy
2. Configure Kubernetes auth role bindings
3. Create GitHub token path (or placeholder if no token provided)
4. Verify SEAM policy isolation (ensure SEAM cannot read evaluator secrets)
5. Create VictoriaMetrics credentials placeholder

### Workflow Status
- **Template Exists:** ✅ Yes (defined in Argo Workflows)
- **Execution Status:** ❌ **Never Successfully Executed**
- **Blocker:** Missing prerequisite ServiceAccount `seam-retirement-evaluator` in namespace `seam`

---

## Current Gaps

### Missing Prerequisites
1. **ServiceAccount:** `seam-retirement-evaluator` does not exist in namespace `seam`
2. **OpenBao Role:** Role not created (setup workflow not executed)
3. **Policy:** Policy not loaded into OpenBao policy engine
4. **Secrets:** Neither GitHub token nor VictoriaMetrics credentials exist

### Why Setup Failed
According to the comprehensive OpenBao report (OPENBAO_RESOURCES_FINAL_REPORT.md):
- All authentication workflows failed with exit code 1
- ServiceAccount lookup failures prevented setup execution
- JWT token validation issues
- No successful workflow execution found in logs

---

## Remediation Path

### Immediate Actions Required
1. **Create ServiceAccount:**
   ```bash
   kubectl create serviceaccount seam-retirement-evaluator -n seam
   ```

2. **Execute Setup Workflow:**
   ```bash
   kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig \
     create -f - <<EOF
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
         value: "REPLACE_WITH_ACTUAL_GITHUB_PAT"
   EOF
   ```

3. **Verify Role Creation:**
   ```bash
   bao read auth/kubernetes/role/seam-retirement-evaluator
   ```

4. **Test Access:**
   Execute verification workflow `seam-retirement-evaluator-verify-openbao`

---

## Security Compliance

### Expected Isolation ✅ (if configured)
- **SEAM Isolation:** SEAM role cannot read evaluator secrets (explicit deny)
- **Evaluator Isolation:** Evaluator role cannot read SEAM route secrets (explicit deny)
- **Cross-Tenant Isolation:** Both roles denied access to all other secrets
- **No Write Access:** Both roles are read-only
- **No List Capabilities:** Prevents secret enumeration

### Current Status ⚠️
Policies are correctly designed for security isolation, but since they are not loaded into OpenBao, the security model is not enforced.

---

## Verification Checklist

### Post-Provisioning Verification
Once the setup workflow is successfully executed, verify:

- [ ] Role `auth/kubernetes/role/seam-retirement-evaluator` exists
- [ ] Policy `seam-retirement-evaluator-policy` is loaded
- [ ] ServiceAccount `seam-retirement-evaluator` exists in namespace `seam`
- [ ] GitHub token secret exists at `secret/evaluators/seam-retirement-evaluator/github-token`
- [ ] VictoriaMetrics credentials exist at `secret/monitoring/victoriametrics/readonly-credentials`
- [ ] SEAM policy cannot read evaluator secrets
- [ ] Evaluator policy cannot read SEAM route secrets

---

## References

- **Setup Template:** `/home/coding/SEAM/seam-retirement-evaluator-openbao-setup-template.json`
- **Comprehensive Report:** `/home/coding/SEAM/OPENBAO_RESOURCES_FINAL_REPORT.md`
- **Workflow Execution Logs:** Multiple failed attempts documented in report
- **OpenBao Server:** `http://openbao-ardenone.tail1b1987.ts.net:8200`

---

## Conclusion

The `seam-retirement-evaluator` role configuration is well-documented and properly designed with strong security isolation. However, **the role does not currently exist** in OpenBao because the provisioning workflow has never been successfully executed. The primary blocker is the missing ServiceAccount `seam-retirement-evaluator` in the `seam` namespace.

**Next Steps:**
1. Create the missing ServiceAccount
2. Execute the setup workflow
3. Verify role creation and test access
4. Replace placeholder GitHub token with actual PAT

This documentation serves as the authoritative reference for the expected configuration once provisioning is completed.
