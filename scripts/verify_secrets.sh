#!/bin/bash
set -e

echo "=== Verifying OpenBao Secret Values (detection-only, post-withdrawal) ==="
echo "Checking the live grant's presence and that retired paths stay denied"
echo ""

OPENBAO_ADDR="http://openbao-rs-manager.openbao.svc.cluster.local:8200"
export VAULT_ADDR=$OPENBAO_ADDR

# Function to check if secret exists and has value (presence only -- no value
# is ever printed)
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

  # Check for empty value
  if [ -z "$result" ] || [ "$result" = "null" ]; then
    echo "  ❌ FAIL: Secret exists but has empty/null value"
    return 1
  fi

  # Success - secret has a value (never echoed: values stay in the store)
  echo "  ✓ PASS: Secret exists with non-empty value"
  return 0
}

# Function to check that a retired path stays absent and denied. The
# evaluator is detection-only (withdrawn 2026-09-05): it holds no GitHub
# credential, so a read succeeding here means the withdrawn grant or secret
# came back -- a policy regression, never a missing prerequisite.
check_secret_absent() {
  local path=$1
  local secret_name=$2

  echo "Checking: $secret_name (retired -- must stay absent/denied)"
  echo "  Path: $path"

  if ! command -v bao &> /dev/null; then
    echo "  ❌ ERROR: bao CLI not found"
    return 1
  fi

  if ! bao status &> /dev/null; then
    echo "  ❌ ERROR: Cannot reach OpenBao at $OPENBAO_ADDR"
    echo "  This is expected if not running in the cluster"
    return 1
  fi

  if bao kv get "$path" &> /dev/null; then
    echo "  ❌ FAIL: Retired path is READABLE -- withdrawn grant has come back (policy regression)"
    return 1
  fi

  echo "  ✓ PASS: Retired path absent/denied (403) -- withdrawal holds"
  return 0
}

echo "=== Retired GitHub Token Path (must stay denied) ==="
check_secret_absent "secret/evaluators/seam-retirement-evaluator/github-token" "Retired GitHub Token for seam-retirement-evaluator"
echo ""

echo "=== VictoriaMetrics Query Credential (the evaluator's single live grant) ==="
check_secret "secret/rs-manager/seam-retirement-evaluator/victoriametrics-query" "endpoint" "VictoriaMetrics query endpoint"
check_secret "secret/rs-manager/seam-retirement-evaluator/victoriametrics-query" "token" "VictoriaMetrics query token"
echo ""

echo "=== Verification Complete ==="
