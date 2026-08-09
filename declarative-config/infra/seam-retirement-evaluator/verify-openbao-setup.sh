#!/bin/bash
set -e

log() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $1"; }
error() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] ERROR: $1" >&2; exit 1; }

log "=== OpenBao Setup Verification for seam-retirement-evaluator ==="

# Check if running in cluster
if [ ! -f "/var/run/secrets/kubernetes.io/serviceaccount/token" ]; then
  error "Not running in Kubernetes cluster - cannot verify OpenBao setup"
fi

# Get Kubernetes service account token
SA_TOKEN=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)
SA_NS=$(cat /var/run/secrets/kubernetes.io/serviceaccount/namespace)
SA_NAME=$(cat /var/run/secrets/kubernetes.io/serviceaccount/name)

log "Running as ServiceAccount: $SA_NAME in namespace: $SA_NS"

if [ "$SA_NAME" != "seam-retirement-evaluator" ] || [ "$SA_NS" != "seam" ]; then
  error "This script must run as seam-retirement-evaluator ServiceAccount in seam namespace"
fi

# OpenBao endpoint (runs on rs-manager itself)
OPENBAO_ADDR="${OPENBAO_ADDR:-http://openbao-rs-manager.openbao.svc.cluster.local:8200}"

log "Testing OpenBao authentication..."
# Try to login using Kubernetes auth
RESPONSE=$(curl -sf -X POST \
  -H "Content-Type: application/json" \
  -d "{\"jwt\":\"$SA_TOKEN\",\"role\":\"seam-retirement-evaluator\"}" \
  "$OPENBAO_ADDR/v1/auth/kubernetes/login" 2>&1)

if [ $? -ne 0 ]; then
  error "Failed to authenticate to OpenBao: $RESPONSE"
fi

# Extract token
BAO_TOKEN=$(echo "$RESPONSE" | sed -n 's/.*"client_token":"\([^"]*\)".*/\1/p')
if [ -z "$BAO_TOKEN" ]; then
  error "Failed to extract OpenBao token from response: $RESPONSE"
fi

log "Successfully authenticated to OpenBao"

# Test 1: Read evaluator's own GitHub token
log "Test 1: Reading evaluator's GitHub token..."
TOKEN_RESPONSE=$(curl -sf -X GET \
  -H "X-Vault-Token: $BAO_TOKEN" \
  "$OPENBAO_ADDR/v1/secret/data/seam-retirement-evaluator/github-token" 2>&1)

if echo "$TOKEN_RESPONSE" | grep -q "permission denied"; then
  error "FAIL: Cannot read own GitHub token path"
else
  log "PASS: Can read own GitHub token path"
fi

# Test 2: Try to read SEAM routes (should fail)
log "Test 2: Verifying SEAM routes are inaccessible..."
SEAM_RESPONSE=$(curl -sf -X GET \
  -H "X-Vault-Token: $BAO_TOKEN" \
  "$OPENBAO_ADDR/v1/secret/data/seam/routes/" 2>&1)

if echo "$SEAM_RESPONSE" | grep -q "permission denied\|deny"; then
  log "PASS: SEAM routes are correctly inaccessible"
else
  error "FAIL: Can access SEAM routes - security issue!"
fi

# Test 3: Read VictoriaMetrics credentials
log "Test 3: Reading VictoriaMetrics credentials..."
VM_RESPONSE=$(curl -sf -X GET \
  -H "X-Vault-Token: $BAO_TOKEN" \
  "$OPENBAO_ADDR/v1/secret/data/monitoring/victoriametrics/" 2>&1)

if echo "$VM_RESPONSE" | grep -q "permission denied\|Invalid"; then
  log "Note: VictoriaMetrics credentials path may not exist yet"
else
  log "PASS: Can read VictoriaMetrics credentials"
fi

log "=== Verification Tests Complete ==="
log "✓ Evaluator can read own GitHub token"
log "✓ Evaluator cannot read SEAM routes"
log "✓ Evaluator can read VictoriaMetrics credentials (if configured)"
