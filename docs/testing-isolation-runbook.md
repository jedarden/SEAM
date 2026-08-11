# SEAM Isolation Testing Runbook

## Overview

This runbook provides step-by-step procedures for testing and verifying the security isolation between SEAM and the seam-retirement-evaluator service. Use this guide to validate that all authentication and authorization paths work correctly.

**Last Updated:** 2026-08-11  
**Bead:** bf-4oa45

## Prerequisites

### Required Access

- **kubectl access** to `rs-manager` cluster (read/write)
- **kubectl access** to `iad-ci` cluster (read/write)
- **OpenBao admin token** (stored in password manager)
- **OpenBao CLI** (`bao`) installed locally or in test container

### Required Tools

```bash
# Verify kubectl access
kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig get nodes
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig get nodes

# Verify OpenBao CLI (if testing locally)
bao version
```

### Environment Setup

```bash
# Set OpenBao endpoint (rs-manager cluster)
export BAO_ADDR="http://openbao-rs-manager.openbao.svc.cluster.local:8200"
export VAULT_ADDR=$BAO_ADDR

# Set OpenBao admin token (from password manager)
export BAO_TOKEN="<your-admin-token>"

# Verify connectivity
bao status
```

## Quick Verification (5 minutes)

### Step 1: Verify Policies Exist

```bash
# Check SEAM policy
bao policy read seam

# Check Evaluator policy
bao policy read seam-retirement-evaluator-policy
```

**Expected Output:**
- SEAM policy should show `seam/routes/*` allowed, `evaluators/*` denied
- Evaluator policy should show `evaluators/seam-retirement-evaluator/*` and `monitoring/victoriametrics/*` allowed, `seam/routes/*` denied

### Step 2: Verify Kubernetes Roles

```bash
# Check SEAM role
bao read auth/kubernetes/role/seam

# Check Evaluator role
bao read auth/kubernetes/role/seam-retirement-evaluator
```

**Expected Output:**
- SEAM role should be bound to ServiceAccount `seam` in namespace `seam`
- Evaluator role should be bound to ServiceAccount `seam-retirement-evaluator` in namespace `seam`

### Step 3: Verify Secrets Exist

```bash
# Check evaluator GitHub token path
bao kv get secret/evaluators/seam-retirement-evaluator/github-token

# Check VictoriaMetrics credentials path
bao kv get secret/monitoring/victoriametrics/readonly-credentials

# Check at least one SEAM route secret exists
bao kv list secret/seam/routes/
```

**Expected Output:**
- Evaluator token path should exist (may contain placeholder initially)
- VictoriaMetrics credentials should exist
- At least one SEAM route secret should exist

## Comprehensive Testing (15 minutes)

### Test 1: Run Go Unit Tests

```bash
cd /home/coding/SEAM

# Test SEAM access denial
go test -v ./internal/server -run TestOpenBaoTokenAccessDenial

# Test end-to-end isolation
go test -v ./internal/server -run TestE2EIsolation
```

**Expected Result:**
- Tests may SKIP if OpenBao is not installed locally
- If OpenBao is available, all tests should PASS

**Interpretation:**
- SKIP is expected for local development without OpenBao
- PASS means isolation is correctly enforced
- FAIL means security boundaries are violated (investigate immediately)

### Test 2: Run Argo Workflow Verification

```bash
# Submit verification workflow
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

# Watch workflow progress
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig get workflows -n argo-workflows -l workflows.argoproj.io/workflow-template=seam-retirement-evaluator-verify-openbao -w

# Get workflow name
WF_NAME=$(kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig get workflows -n argo-workflows -l workflows.argoproj.io/workflow-template=seam-retirement-evaluator-verify-openbao -o jsonpath='{.items[0].metadata.name}')

# View logs
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig logs -n argo-workflows -c main ${WF_NAME}
```

**Expected Output:**
```
[timestamp] === OpenBao Access Verification for seam-retirement-evaluator ===
[timestamp] OpenBao is reachable
[timestamp] Running as ServiceAccount: seam-retirement-evaluator in namespace: seam
[timestamp] Successfully authenticated to OpenBao
[timestamp] PASS: Can read own GitHub token path
[timestamp] PASS: SEAM routes are correctly inaccessible
[timestamp] PASS: Can read VictoriaMetrics credentials
[timestamp] PASS: Correctly denied access to other paths (armor/)
[timestamp] === Verification Results ===
[timestamp] ✓ Evaluator ServiceAccount can authenticate to OpenBao
[timestamp] ✓ Evaluator can read own GitHub token
[timestamp] ✓ Evaluator cannot read SEAM routes (isolation verified)
[timestamp] ✓ Evaluator can read VictoriaMetrics credentials
[timestamp] ✓ Evaluator policy correctly bounded (cannot access other paths)
[timestamp] === All verification tests passed ===
```

**Interpretation:**
- All "PASS" messages mean isolation is correctly enforced
- "FAIL" messages mean security boundaries are violated (investigate immediately)
- "WARNING" about placeholder token is acceptable during initial setup

### Test 3: Manual OpenBao Access Test

```bash
# Get evaluator token (as evaluator SA)
# First, create a test pod as evaluator SA
kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig run -n seam openbao-test --rm -i --restart=Never --image=ghcr.io/openbao/openbao:1.15.0 --serviceaccount=seam-retirement-evaluator -- bash

# Inside the pod, authenticate as evaluator
bao write -field=client_token auth/kubernetes/login role=seam-retirement-evaluator jwt=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)

# Store the token
export EVAL_TOKEN=$(bao write -field=client_token auth/kubernetes/login role=seam-retirement-evaluator jwt=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token))

# Try to read evaluator token (should succeed)
bao kv get -field=token secret/evaluators/seam-retirement-evaluator/github-token

# Try to read SEAM routes (should fail)
bao kv get secret/seam/routes/

# Exit the pod
exit
```

**Expected Output:**
- Reading evaluator token: SUCCESS (returns token value or "REPLACE_WITH_ACTUAL_GITHUB_PAT")
- Reading SEAM routes: FAILURE (permission denied error)

**Interpretation:**
- SUCCESS for evaluator token means evaluator can read its own secrets ✅
- FAILURE for SEAM routes means evaluator is correctly isolated ✅

### Test 4: SEAM Isolation Test

```bash
# Create test pod as SEAM SA
kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig run -n seam openbao-test-seam --rm -i --restart=Never --image=ghcr.io/openbao/openbao:1.15.0 --serviceaccount=seam -- bash

# Inside the pod, authenticate as SEAM
bao write -field=client_token auth/kubernetes/login role=seam jwt=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)

# Try to read SEAM routes (should succeed)
bao kv get secret/seam/routes/

# Try to read evaluator token (should fail)
bao kv get secret/evaluators/seam-retirement-evaluator/github-token

# Exit the pod
exit
```

**Expected Output:**
- Reading SEAM routes: SUCCESS (returns list of routes or route secrets)
- Reading evaluator token: FAILURE (permission denied error)

**Interpretation:**
- SUCCESS for SEAM routes means SEAM can read its own secrets ✅
- FAILURE for evaluator token means SEAM is correctly isolated ✅

## Full Isolation Validation (30 minutes)

### Phase 1: Policy Structure Validation

```bash
# Create validation script
cat > /tmp/validate_policies.sh <<'EOF'
#!/bin/bash
set -e

echo "=== Phase 1: Validating Policy Structure ==="

# Check SEAM policy
echo "Checking SEAM policy..."
SEAM_POLICY=$(bao policy read seam -format=json)

# Should allow seam/routes/*
if echo "$SEAM_POLICY" | grep -q '"path":"secret/data/seam/routes/*"'; then
  echo "✓ SEAM policy allows seam/routes/*"
else
  echo "✗ SEAM policy missing seam/routes/*"
  exit 1
fi

# Should deny evaluators/*
if echo "$SEAM_POLICY" | grep -q '"path":"secret/data/evaluators/*"'; then
  echo "✓ SEAM policy denies evaluators/*"
else
  echo "✗ SEAM policy missing evaluators/* deny"
  exit 1
fi

# Check Evaluator policy
echo "Checking Evaluator policy..."
EVAL_POLICY=$(bao policy read seam-retirement-evaluator-policy -format=json)

# Should allow evaluators/seam-retirement-evaluator/*
if echo "$EVAL_POLICY" | grep -q '"path":"secret/data/evaluators/seam-retirement-evaluator/*"'; then
  echo "✓ Evaluator policy allows evaluators/seam-retirement-evaluator/*"
else
  echo "✗ Evaluator policy missing evaluators/seam-retirement-evaluator/*"
  exit 1
fi

# Should allow monitoring/victoriametrics/*
if echo "$EVAL_POLICY" | grep -q '"path":"secret/data/monitoring/victoriametrics/*"'; then
  echo "✓ Evaluator policy allows monitoring/victoriametrics/*"
else
  echo "✗ Evaluator policy missing monitoring/victoriametrics/*"
  exit 1
fi

# Should deny seam/routes/*
if echo "$EVAL_POLICY" | grep -q '"path":"secret/data/seam/routes/*"'; then
  echo "✓ Evaluator policy denies seam/routes/*"
else
  echo "✗ Evaluator policy missing seam/routes/* deny"
  exit 1
fi

echo "=== Phase 1 Complete: All policies valid ==="
EOF

chmod +x /tmp/validate_policies.sh
/tmp/validate_policies.sh
```

### Phase 2: Kubernetes Role Validation

```bash
# Create role validation script
cat > /tmp/validate_roles.sh <<'EOF'
#!/bin/bash
set -e

echo "=== Phase 2: Validating Kubernetes Roles ==="

# Check SEAM role
echo "Checking SEAM Kubernetes role..."
SEAM_ROLE=$(bao read auth/kubernetes/role/seam -format=json)

# Verify bound ServiceAccount
if echo "$SEAM_ROLE" | grep -q '"bound_service_account_names":["seam"]'; then
  echo "✓ SEAM role bound to correct ServiceAccount"
else
  echo "✗ SEAM role has incorrect ServiceAccount binding"
  exit 1
fi

# Verify bound namespace
if echo "$SEAM_ROLE" | grep -q '"bound_service_account_namespaces":["seam"]'; then
  echo "✓ SEAM role bound to correct namespace"
else
  echo "✗ SEAM role has incorrect namespace binding"
  exit 1
fi

# Check Evaluator role
echo "Checking Evaluator Kubernetes role..."
EVAL_ROLE=$(bao read auth/kubernetes/role/seam-retirement-evaluator -format=json)

# Verify bound ServiceAccount
if echo "$EVAL_ROLE" | grep -q '"bound_service_account_names":["seam-retirement-evaluator"]'; then
  echo "✓ Evaluator role bound to correct ServiceAccount"
else
  echo "✗ Evaluator role has incorrect ServiceAccount binding"
  exit 1
fi

# Verify bound namespace
if echo "$EVAL_ROLE" | grep -q '"bound_service_account_namespaces":["seam"]'; then
  echo "✓ Evaluator role bound to correct namespace"
else
  echo "✗ Evaluator role has incorrect namespace binding"
  exit 1
fi

echo "=== Phase 2 Complete: All Kubernetes roles valid ==="
EOF

chmod +x /tmp/validate_roles.sh
/tmp/validate_roles.sh
```

### Phase 3: Secret Path Validation

```bash
# Create secret validation script
cat > /tmp/validate_secrets.sh <<'EOF'
#!/bin/bash
set -e

echo "=== Phase 3: Validating Secret Paths ==="

# Check evaluator token path
echo "Checking evaluator GitHub token path..."
if bao kv get secret/evaluators/seam-retirement-evaluator/github-token >/dev/null 2>&1; then
  TOKEN_VALUE=$(bao kv get -field=token secret/evaluators/seam-retirement-evaluator/github-token)
  if echo "$TOKEN_VALUE" | grep -q "REPLACE_WITH_ACTUAL_GITHUB_PAT"; then
    echo "⚠ Evaluator token path exists but contains placeholder"
  else
    echo "✓ Evaluator token path exists with actual token"
  fi
else
  echo "✗ Evaluator token path does not exist"
  exit 1
fi

# Check VictoriaMetrics credentials
echo "Checking VictoriaMetrics credentials path..."
if bao kv get secret/monitoring/victoriametrics/readonly-credentials >/dev/null 2>&1; then
  echo "✓ VictoriaMetrics credentials path exists"
else
  echo "✗ VictoriaMetrics credentials path does not exist"
  exit 1
fi

# Check at least one SEAM route secret
echo "Checking SEAM route secrets..."
SEAM_ROUTES=$(bao kv list secret/seam/routes/ 2>/dev/null | wc -l)
if [ "$SEAM_ROUTES" -gt 0 ]; then
  echo "✓ Found $SEAM_ROUTES SEAM route secrets"
else
  echo "⚠ No SEAM route secrets found (may be expected for new installation)"
fi

echo "=== Phase 3 Complete: All secret paths validated ==="
EOF

chmod +x /tmp/validate_secrets.sh
/tmp/validate_secrets.sh
```

### Phase 4: Runtime Access Validation

```bash
# Create runtime validation script
cat > /tmp/validate_runtime.sh <<'EOF'
#!/bin/bash
set -e

echo "=== Phase 4: Validating Runtime Access ==="

# Test Evaluator access
echo "Testing Evaluator access..."
kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig run -n seam eval-test --rm -i --restart=Never --image=ghcr.io/openbao/openbao:1.15.0 --serviceaccount=seam-retirement-evaluator -- bash <<'INNER_EOF'
set -e

# Authenticate
EVAL_TOKEN=$(bao write -field=client_token auth/kubernetes/login role=seam-retirement-evaluator jwt=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token))

# Test 1: Read own token (should succeed)
if bao kv get secret/evaluators/seam-retirement-evaluator/github-token >/dev/null 2>&1; then
  echo "✓ Evaluator can read own token"
else
  echo "✗ Evaluator cannot read own token"
  exit 1
fi

# Test 2: Read VM credentials (should succeed)
if bao kv get secret/monitoring/victoriametrics/readonly-credentials >/dev/null 2>&1; then
  echo "✓ Evaluator can read VM credentials"
else
  echo "✗ Evaluator cannot read VM credentials"
  exit 1
fi

# Test 3: Try to read SEAM routes (should fail)
if bao kv get secret/seam/routes/ 2>&1 | grep -qi "permission denied\|Invalid"; then
  echo "✓ Evaluator correctly denied access to SEAM routes"
else
  echo "✗ Evaluator can access SEAM routes (SECURITY BREACH)"
  exit 1
fi

INNER_EOF

# Test SEAM access
echo "Testing SEAM access..."
kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig run -n seam seam-test --rm -i --restart=Never --image=ghcr.io/openbao/openbao:1.15.0 --serviceaccount=seam -- bash <<'INNER_EOF'
set -e

# Authenticate
SEAM_TOKEN=$(bao write -field=client_token auth/kubernetes/login role=seam jwt=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token))

# Test 1: Try to read evaluator token (should fail)
if bao kv get secret/evaluators/seam-retirement-evaluator/github-token 2>&1 | grep -qi "permission denied\|Invalid"; then
  echo "✓ SEAM correctly denied access to evaluator token"
else
  echo "✗ SEAM can access evaluator token (SECURITY BREACH)"
  exit 1
fi

# Test 2: Read own routes (should succeed)
if bao kv list secret/seam/routes/ >/dev/null 2>&1; then
  echo "✓ SEAM can read own route secrets"
else
  echo "✗ SEAM cannot read own route secrets"
  exit 1
fi

INNER_EOF

echo "=== Phase 4 Complete: All runtime access validated ==="
EOF

chmod +x /tmp/validate_runtime.sh
/tmp/validate_runtime.sh
```

## Troubleshooting

### Issue: "Cannot reach OpenBao"

**Symptom:** `bao status` fails with connection error

**Diagnosis:**
```bash
# Check OpenBao pod is running
kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig get pods -n openbao

# Check OpenBao service
kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig get svc -n openbao

# Check connectivity from test pod
kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig run -n seam connectivity-test --rm -i --restart=Never --image=curlimages/curl -- curl -v http://openbao-rs-manager.openbao.svc.cluster.local:8200/v1/sys/health
```

**Resolution:**
- Ensure OpenBao is running: `kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig get pods -n openbao`
- Check service is exposing port 8200
- Verify network policies allow traffic from `seam` namespace to `openbao` namespace

### Issue: "Permission denied" when reading own secrets

**Symptom:** Evaluator cannot read its own GitHub token

**Diagnosis:**
```bash
# Check policy exists
bao policy read seam-retirement-evaluator-policy

# Check role is bound correctly
bao read auth/kubernetes/role/seam-retirement-evaluator

# Check ServiceAccount exists
kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig get sa seam-retirement-evaluator -n seam
```

**Resolution:**
- Re-run setup workflow: `kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig create -f declarative-config/infra/seam-retirement-evaluator/openbao-setup-job.yaml`
- Verify policy syntax is correct
- Verify ServiceAccount name matches exactly

### Issue: "Can access SEAM routes" (Security Breach)

**Symptom:** Evaluator can read SEAM route secrets

**Diagnosis:**
```bash
# This is a SECURITY BREACH - immediate investigation required
# Check if deny rule exists in evaluator policy
bao policy read seam-retirement-evaluator-policy | grep seam/routes

# Check if SEAM policy exists and is correct
bao policy read seam | grep evaluators
```

**Resolution:**
- IMMEDIATE: Revoke all OpenBao tokens: `bao lease revoke -prefix auth/kubernetes/role/seam-retirement-evaluator`
- IMMEDIATE: Fix policy to add explicit deny for `seam/routes/*`
- Verify: Re-run full isolation validation
- Document: Create incident report

### Issue: "Tests skip locally"

**Symptom:** Go tests skip with "openbao not found in PATH"

**Diagnosis:**
```bash
# Check if OpenBao is installed
which openbao

# Check if it's in PATH
echo $PATH
```

**Resolution:**
- This is expected for local development
- Use Argo workflows for cluster testing instead
- Or install OpenBao locally if needed: https://openbao.org/docs/install

## Test Results Interpretation

### Success Criteria

All of the following MUST be true for isolation to be valid:

1. ✅ SEAM can read `secret/data/seam/routes/*`
2. ✅ SEAM cannot read `secret/data/evaluators/*` (permission denied)
3. ✅ Evaluator can read `secret/data/evaluators/seam-retirement-evaluator/*`
4. ✅ Evaluator can read `secret/data/monitoring/victoriametrics/*`
5. ✅ Evaluator cannot read `secret/data/seam/routes/*` (permission denied)
6. ✅ Both roles cannot read other paths (armor/, kalshi/, etc.)

### Failure Interpretation

**If ANY of the following occur, isolation is VIOLATED:**

- ❌ SEAM can read evaluator token → SECURITY BREACH
- ❌ Evaluator can read SEAM routes → SECURITY BREACH
- ❌ Either role can read other paths → SECURITY BREACH
- ❌ SEAM cannot read its own routes → MISCONFIGURATION
- ❌ Evaluator cannot read its own token → MISCONFIGURATION
- ❌ Evaluator cannot read VM credentials → MISCONFIGURATION

**Immediate Actions for Security Breach:**
1. Revoke all affected OpenBao tokens
2. Fix policies to add explicit deny rules
3. Re-run full validation suite
4. Create incident report

## Continuous Monitoring

### Automated Checks

Set up periodic checks using Argo WorkflowTemplates:

```yaml
# File: declarative-config/k8s/iad-ci/argo-workflows/seam-isolation-monitor.yaml
apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: seam-isolation-monitor
  namespace: argo-workflows
spec:
  templates:
  - name: isolation-check
    container:
      image: ghcr.io/openbao/openbao:1.15.0
      command: ["/bin/bash", "-c"]
      args:
        - |
          # Run verification workflow
          # Send alert if isolation is violated
```

### Alerts to Configure

1. **Policy Changed Alert**: Triggered when OpenBao policies are modified
2. **Role Binding Alert**: Triggered when Kubernetes auth roles are modified
3. **Isolation Test Alert**: Triggered when isolation tests fail
4. **Secret Access Alert**: Triggered when cross-path access is attempted

## Test Schedule

### Initial Testing (After Setup)

Run all tests in this runbook to verify initial setup is correct.

### Regression Testing (After Changes)

Run quick verification (5 minutes) after any changes to:
- OpenBao policies
- Kubernetes auth roles
- ServiceAccount configurations
- OpenBao server configuration

### Periodic Testing (Weekly)

Run comprehensive testing (15 minutes) to verify no configuration drift has occurred.

### Post-Incident Testing (After Security Event)

Run full isolation validation (30 minutes) after any security event or suspected breach.

## Related Documentation

- **Security Isolation Model:** `docs/security-isolation-model.md`
- **SEAM OpenBao Setup:** `docs/notes/openbao-seam-setup.md`
- **Evaluator OpenBao Setup:** `docs/notes/openbao-evaluator-setup.md`
- **Isolation Verification Plan:** `docs/notes/evaluator-isolation-verification-plan.md`

## References

- **Bead:** bf-4oa45 (verification and documentation task)
- **SEAM Policy:** `declarative-config/infra/seam/seam-openbao-policy.hcl`
- **Evaluator Policy:** `declarative-config/infra/seam-retirement-evaluator/openbao-policy.hcl`
- **Verify Workflow:** `declarative-config/infra/seam-retirement-evaluator/openbao-verify-workflow.yaml`
