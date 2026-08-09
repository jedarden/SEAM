#!/usr/bin/env bash
# ============================================================================
# Create OpenBao Kubernetes auth role and policy for SEAM
# ============================================================================
#
# This script creates:
# 1. OpenBao policy "seam" granting read on seam/routes/* ONLY
# 2. Kubernetes auth role "seam" bound to SEAM's ServiceAccount
#
# Prerequisites:
# - OpenBao CLI (bao) or vault CLI installed
# - Valid OpenBao token with admin privileges
# - Kubernetes auth method already enabled in OpenBao
#
# Usage:
#   export BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
#   export BAO_TOKEN="hvs.xxxxx"  # Admin token
#   ./setup-seam-openbao.sh
#
# ============================================================================

set -e

BAO_ADDR="${BAO_ADDR:-http://openbao-ardenone.tail1b1987.ts.net:8200}"
BAO_TOKEN="${BAO_TOKEN:?Error: BAO_TOKEN must be set to an admin token}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Detect CLI (bao or vault)
if command -v bao &>/dev/null; then
  BAO_CLI="bao"
elif command -v vault &>/dev/null; then
  BAO_CLI="vault"
else
  echo "Error: Neither bao nor vault CLI found. Install OpenBao CLI."
  exit 1
fi

export BAO_ADDR BAO_TOKEN

echo "OpenBao Address: ${BAO_ADDR}"
echo "OpenBao CLI: ${BAO_CLI}"
echo ""

# ============================================================================
# Step 1: Write SEAM policy
# ============================================================================
echo "=== Step 1: Creating SEAM policy ==="

# Check if policy file exists
POLICY_FILE="${SCRIPT_DIR}/seam-openbao-policy.hcl"
if [ ! -f "$POLICY_FILE" ]; then
  echo "Error: Policy file not found: $POLICY_FILE"
  exit 1
fi

# Write policy to OpenBao
"${BAO_CLI}" policy write seam "$POLICY_FILE"
echo "✓ Policy 'seam' created"

# Display policy for verification
echo ""
echo "Policy contents:"
"${BAO_CLI}" policy read seam
echo ""

# ============================================================================
# Step 2: Create Kubernetes auth role for SEAM
# ============================================================================
echo "=== Step 2: Creating Kubernetes auth role 'seam' ==="

# Check if kubernetes auth method is enabled
if ! "${BAO_CLI}" auth list | grep -q "kubernetes/"; then
  echo "Error: Kubernetes auth method not enabled. Please enable it first:"
  echo "  ${BAO_CLI} auth enable kubernetes"
  exit 1
fi

# Get the kubernetes auth mount point (could be kubernetes or custom)
K8S_AUTH_PATH=$("${BAO_CLI}" auth list | grep "kubernetes/" | awk '{print $1}' | head -1)

echo "Using Kubernetes auth path: ${K8S_AUTH_PATH}"

# Create the role
# Role name: seam
# Bound ServiceAccount: seam (in seam namespace)
# Bound namespace: seam
# Policies: seam
# Token TTL: 24h
"${BAO_CLI}" write "${K8S_AUTH_PATH}/role/seam" \
  bound_service_account_names=seam \
  bound_service_account_namespaces=seam \
  policies=seam \
  ttl=24h \
  max_ttl=72h

echo "✓ Kubernetes auth role 'seam' created"

# Display role for verification
echo ""
echo "Role details:"
"${BAO_CLI}" read "${K8S_AUTH_PATH}/role/seam"
echo ""

# ============================================================================
# Step 3: Create test secret under seam/routes/
# ============================================================================
echo "=== Step 3: Creating test secret ==="

# Create test secret path
TEST_PATH="seam/routes/test-secret"
"${BAO_CLI}" kv put secret/${TEST_PATH} \
  test_key="test_value_$(date +%s)" \
  description="Test secret for SEAM OpenBao role verification"

echo "✓ Test secret created at: secret/${TEST_PATH}"

# ============================================================================
# Step 4: Verify role can read test secret
# ============================================================================
echo "=== Step 4: Verifying role permissions ==="

# Get a token using the Kubernetes auth role (simulating what SEAM pod would do)
# In production, SEAM pod would use its ServiceAccount token
TEST_TOKEN=$("${BAO_CLI}" write -field=token "${K8S_AUTH_PATH}/login/seam" \
  role=seam \
  jwt="test.jwt.placeholder")

# Actually, we can't test Kubernetes auth without a real JWT from the cluster
# Instead, let's create a test token with the seam policy
TEST_TOKEN=$("${BAO_CLI}" token create -policy=seam -field=token)

echo "Test token created: ${TEST_TOKEN:0:20}..."

# Test reading the test secret
echo ""
echo "Test 1: Reading seam/routes/test-secret (should succeed)"
if VAULT_TOKEN="${TEST_TOKEN}" "${BAO_CLI}" kv get secret/${TEST_PATH} &>/dev/null; then
  echo "✓ SUCCESS: Can read seam/routes/*"
else
  echo "✗ FAILED: Cannot read seam/routes/*"
fi

# Test that we CANNOT read evaluator secrets
echo ""
echo "Test 2: Reading seam-retirement-evaluator/* (should be denied)"
if VAULT_TOKEN="${TEST_TOKEN}" "${BAO_CLI}" kv get secret/seam-retirement-evaluator/test &>/dev/null; then
  echo "✗ FAILED: Can read evaluator secrets (POLICY LEAK!)"
else
  echo "✓ SUCCESS: Denied access to evaluator secrets"
fi

# Test that we CANNOT read other secrets
echo ""
echo "Test 3: Reading kalshi/* (should be denied)"
if VAULT_TOKEN="${TEST_TOKEN}" "${BAO_CLI}" kv get secret/kalshi/test &>/dev/null; then
  echo "✗ FAILED: Can read other tenant secrets (POLICY LEAK!)"
else
  echo "✓ SUCCESS: Denied access to other tenant secrets"
fi

# Test that we CANNOT list arbitrary paths
echo ""
echo "Test 4: Listing secret/ (should be denied)"
if VAULT_TOKEN="${TEST_TOKEN}" "${BAO_CLI}" list secret/ &>/dev/null; then
  echo "✗ FAILED: Can list secret/ (POLICY LEAK!)"
else
  echo "✓ SUCCESS: Denied listing access"
fi

# Revoke test token
echo ""
"${BAO_CLI}" token revoke "${TEST_TOKEN}" &>/dev/null
echo "✓ Test token revoked"

# ============================================================================
# Summary
# ============================================================================
echo ""
echo "============================================"
echo "  SETUP COMPLETE"
echo "============================================"
echo ""
echo "Policy: seam (read on seam/routes/* only)"
echo "Role: ${K8S_AUTH_PATH}/role/seam"
echo "Bound SA: seam (namespace: seam)"
echo ""
echo "Next steps:"
echo "1. Ensure seam ServiceAccount exists in seam namespace"
echo "2. Configure SEAM deployment to use Kubernetes auth"
echo "3. Test in-pod authentication with projected service account token"
echo ""
echo "Verification:"
echo "  Token can read:     secret/seam/routes/*"
echo "  Token cannot read: seam-retirement-evaluator/*, kalshi/*, other secrets"
echo "============================================"
