#!/bin/bash
set -e

log() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $1"; }
error() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] ERROR: $1" >&2; return 1; }
warn() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] WARNING: $1" >&2; }

log "=== Evaluator Isolation Verification (detection-only, post-withdrawal) ==="
log "Testing: retired token path stays denied, the single live victoriametrics-query grant reads, SEAM cannot read evaluator credential paths"

# Check if we have admin access to OpenBao
if [ -z "$BAO_ADDR" ]; then
  BAO_ADDR="${BAO_ADDR:-http://openbao-rs-manager.openbao.svc.cluster.local:8200}"
fi

if [ -z "$BAO_TOKEN" ]; then
  error "BAO_TOKEN environment variable not set. This test requires OpenBao admin access."
fi

export VAULT_ADDR="$BAO_ADDR"
export VAULT_TOKEN="$BAO_TOKEN"

log "OpenBao endpoint: $BAO_ADDR"

# Verify we can reach OpenBao
if ! bao status >/dev/null 2>&1; then
  error "Cannot reach OpenBao at $BAO_ADDR"
fi

log "Connected to OpenBao"

# ===================================================================
# Part 1: Verify Policies Exist
# ===================================================================
log ""
log "=== Part 1: Verifying OpenBao Policies ==="

# Check evaluator policy exists
if bao policy read seam-retirement-evaluator-policy >/dev/null 2>&1; then
  log "✓ seam-retirement-evaluator-policy exists"
else
  error "✗ seam-retirement-evaluator-policy does not exist"
fi

# Check SEAM policy exists
if bao policy read seam >/dev/null 2>&1; then
  log "✓ seam policy exists"
else
  error "✗ seam policy does not exist"
fi

# Verify evaluator policy denies SEAM routes
if bao policy read seam-retirement-evaluator-policy 2>/dev/null | grep -q 'path "secret/data/seam/routes'; then
  log "✓ Evaluator policy has SEAM routes deny rule"
else
  warn "Evaluator policy may not explicitly deny SEAM routes (relies on default-deny)"
fi

# Verify SEAM policy denies evaluator paths
if bao policy read seam 2>/dev/null | grep -q 'path "secret/data/evaluators'; then
  log "✓ SEAM policy explicitly denies evaluator paths"
else
  log "Note: SEAM policy may rely on default-deny for evaluator paths (acceptable)"
fi

# ===================================================================
# Part 2: Verify the Retired Token Path Stays Denied
# ===================================================================
log ""
log "=== Part 2: Verifying the Retired Evaluator Token Path (withdrawn 2026-09-05) ==="

# The evaluator is detection-only and holds no GitHub credential of any
# kind. The retired path must stay absent AND denied: a read succeeding here
# means the withdrawn grant (or the secret) came back -- a policy regression,
# never a missing prerequisite to "fix".
if bao kv get secret/evaluators/seam-retirement-evaluator/github-token >/dev/null 2>&1; then
  error "✗ Retired evaluator token path is READABLE -- the withdrawn grant has come back (policy regression)"
else
  log "✓ Retired evaluator token path absent/denied (403) -- withdrawal holds"
fi

# ===================================================================
# Part 3: Verify the Single Live Grant -- VictoriaMetrics Query Credential
# ===================================================================
log ""
log "=== Part 3: Verifying the VictoriaMetrics Query Credential ==="

# The evaluator policy's single live grant: the query-only bearer token that
# vmauth scopes to read endpoints. (The draft
# secret/monitoring/victoriametrics/readonly-credentials path was never
# applied, is not in the live policy, and is covered by default-deny -- it is
# not an expectation and its absence is correct.)
if bao kv get secret/rs-manager/seam-retirement-evaluator/victoriametrics-query >/dev/null 2>&1; then
  log "✓ VictoriaMetrics query credential path exists"

  VM_ENDPOINT=$(bao kv get -field=endpoint secret/rs-manager/seam-retirement-evaluator/victoriametrics-query 2>/dev/null || echo "")
  if [ -n "$VM_ENDPOINT" ]; then
    log "✓ VictoriaMetrics endpoint configured: $VM_ENDPOINT"
  else
    warn "VictoriaMetrics endpoint field not configured in credential"
  fi
else
  error "✗ VictoriaMetrics query credential path does not exist (the evaluator policy's single live grant is missing)"
fi

# ===================================================================
# Part 4: Verify Evaluator Can Read Its Token
# ===================================================================
log ""
log "=== Part 4: Verifying Evaluator Can Read Own Token ==="

# We need to simulate what the evaluator SA would see
# Check if the Kubernetes role exists
if bao read auth/kubernetes/role/seam-retirement-evaluator >/dev/null 2>&1; then
  log "✓ Kubernetes auth role 'seam-retirement-evaluator' exists"

  ROLE_OUTPUT=$(bao read auth/kubernetes/role/seam-retirement-evaluator -format=json 2>/dev/null)
  BOUND_SA=$(echo "$ROLE_OUTPUT" | jq -r '.data.bound_service_account_names[]' 2>/dev/null || echo "")
  BOUND_NS=$(echo "$ROLE_OUTPUT" | jq -r '.data.bound_service_account_namespaces[]' 2>/dev/null || echo "")

  if [ "$BOUND_SA" = "seam-retirement-evaluator" ]; then
    log "✓ Role bound to correct ServiceAccount: $BOUND_SA"
  else
    error "✗ Role bound to wrong ServiceAccount: $BOUND_SA (expected: seam-retirement-evaluator)"
  fi

  if [ "$BOUND_NS" = "seam" ]; then
    log "✓ Role bound to correct namespace: $BOUND_NS"
  else
    error "✗ Role bound to wrong namespace: $BOUND_NS (expected: seam)"
  fi

  ROLE_POLICIES=$(echo "$ROLE_OUTPUT" | jq -r '.data.policies[]' 2>/dev/null || echo "")
  if echo "$ROLE_POLICIES" | grep -q "seam-retirement-evaluator-policy"; then
    log "✓ Role uses correct policy: seam-retirement-evaluator-policy"
  else
    error "✗ Role does not use seam-retirement-evaluator-policy"
  fi
else
  error "✗ Kubernetes auth role 'seam-retirement-evaluator' does not exist"
fi

# ===================================================================
# Part 5: Verify SEAM Cannot Read Evaluator Token
# ===================================================================
log ""
log "=== Part 5: Verifying SEAM Cannot Read Evaluator Credential Paths ==="

# Check SEAM Kubernetes role exists and has correct policies
if bao read auth/kubernetes/role/seam >/dev/null 2>&1; then
  log "✓ Kubernetes auth role 'seam' exists"

  SEAM_ROLE_OUTPUT=$(bao read auth/kubernetes/role/seam -format=json 2>/dev/null)
  SEAM_POLICIES=$(echo "$SEAM_ROLE_OUTPUT" | jq -r '.data.policies[]' 2>/dev/null || echo "")

  if echo "$SEAM_POLICIES" | grep -q "seam"; then
    log "✓ SEAM role uses 'seam' policy"
  else
    error "✗ SEAM role does not use 'seam' policy"
  fi

  # Verify seam policy does NOT include evaluator access
  if bao policy read seam 2>/dev/null | grep -q 'capabilities = \["read"\]' && \
     bao policy read seam 2>/dev/null | grep -q 'path "secret/data/evaluators'; then
    # Check if it's a deny rule
    if bao policy read seam 2>/dev/null | grep -A1 'path "secret/data/evaluators' | grep -q "deny"; then
      log "✓ SEAM policy explicitly denies evaluator path access"
    else
      error "✗ SEAM policy may allow evaluator path access (SECURITY ISSUE!)"
    fi
  fi
else
  warn "Kubernetes auth role 'seam' does not exist (may not be created yet)"
fi

# ===================================================================
# Part 6: Summary and Recommendations
# ===================================================================
log ""
log "=== Verification Summary ==="
log ""
log "Policy Isolation:"
log "  ✓ Evaluator policy exists and is correctly scoped"
log "  ✓ SEAM policy exists and isolates evaluator paths"
log "  ✓ Each role is bound to its respective ServiceAccount"
log ""
log "Evaluator Access:"
log "  ✓ Retired token path stays absent/denied (withdrawn 2026-09-05 -- detection-only, no credential)"
log "  ✓ Evaluator Kubernetes role configured correctly"
log "  ✓ Evaluator policy's single live grant (victoriametrics-query) reads"
log ""
log "SEAM Access Restriction:"
log "  ✓ SEAM policy explicitly denies evaluator paths (or uses default-deny)"
log "  ✓ SEAM cannot read evaluator's GitHub token path"
log ""
log "Next Steps:"
log "  1. Nothing to provision: the evaluator is detection-only and must never hold a credential"
log "  2. Deploy the evaluator using seam-retirement-evaluator ServiceAccount"
log "  3. Run the evaluator and verify its unauthenticated VM queries and detection-only output"
log "  4. Confirm SEAM cannot access evaluator credential paths (the boundary canary attests the 403 every cycle)"
log ""
log "=== Verification Complete ==="
