#!/bin/bash
set -e

echo "=== Verifying OpenBao Secret Values ==="
echo "Checking for secret presence and non-empty values"
echo ""

OPENBAO_ADDR="http://openbao-rs-manager.openbao.svc.cluster.local:8200"
export VAULT_ADDR=$OPENBAO_ADDR

# Function to check if secret exists and has value
check_secret() {
  local path=$1
  local field=$2
  local secret_name=$3

  echo "Checking: $secret_name"
  echo "  Path: $path"
  echo "  Field: $field"

  # Check if bao command exists
  if ! command -v bao &> /dev/null; then
    echo "  ❌ ERROR: bao CLI not found"
    return 1
  fi

  # Check if OpenBao is reachable
  if ! bao status &> /dev/null; then
    echo "  ❌ ERROR: Cannot reach OpenBao at $OPENBAO_ADDR"
    echo "  This is expected if not running in the cluster"
    return 1
  fi

  # Try to read the secret
  result=$(bao kv get -field="$field" "$path" 2>&1) || true

  # Check for permission denied
  if echo "$result" | grep -qi "permission denied"; then
    echo "  ❌ FAIL: Permission denied"
    return 1
  fi

  # Check for invalid path
  if echo "$result" | grep -qi "Invalid"; then
    echo "  ❌ FAIL: Path does not exist"
    return 1
  fi

  # Check for placeholder value
  if echo "$result" | grep -qi "REPLACE_WITH_ACTUAL_GITHUB_PAT"; then
    echo "  ⚠️  WARNING: Secret exists but contains placeholder value"
    return 1
  fi

  # Check for empty value
  if [ -z "$result" ] || [ "$result" = "null" ]; then
    echo "  ❌ FAIL: Secret exists but has empty/null value"
    return 1
  fi

  # Success - secret has a value
  echo "  ✓ PASS: Secret exists with non-empty value"
  echo "  Value preview: ${result:0:20}..."
  return 0
}

echo "=== GitHub Token ==="
check_secret "secret/evaluators/seam-retirement-evaluator/github-token" "token" "GitHub Token for seam-retirement-evaluator"
echo ""

echo "=== VictoriaMetrics Credentials ==="
check_secret "secret/monitoring/victoriametrics/readonly-credentials" "username" "VictoriaMetrics username"
check_secret "secret/monitoring/victoriametrics/readonly-credentials" "password" "VictoriaMetrics password"
echo ""

echo "=== Verification Complete ==="