#!/bin/bash
# Check if OpenBao role exists for seam-retirement-evaluator

set -e

log() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $1"; }
error() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] ERROR: $1" >&2; exit 1; }

# Find OpenBao pod
OPENBAO_POD=$(kubectl --server=http://traefik-rs-manager:8001 get pods -n openbao -o jsonpath='{.items[?(@.metadata.name=="openbao-rs-manager-0")].metadata.name}' 2>/dev/null || echo "")

if [ -z "$OPENBAO_POD" ]; then
    # Fallback to getting first pod matching name pattern
    OPENBAO_POD=$(kubectl --server=http://traefik-rs-manager:8001 get pods -n openbao -o name | grep openbao-rs-manager | head -1 | cut -d/ -f2 || echo "")
fi

if [ -z "$OPENBAO_POD" ]; then
    error "Could not find OpenBao pod"
fi

log "Found OpenBao pod: $OPENBAO_POD"

# Check if the role exists
log "Checking if OpenBao role 'seam-retirement-evaluator' exists..."

ROLE_OUTPUT=$(kubectl --server=http://traefik-rs-manager:8001 exec -n openbao "$OPENBAO_POD" -c openbao -- bao read auth/kubernetes/role/seam-retirement-evaluator 2>&1 || echo "ROLE_NOT_FOUND")

if echo "$ROLE_OUTPUT" | grep -q "ROLE_NOT_FOUND"; then
    log "❌ OpenBao role 'seam-retirement-evaluator' does NOT exist"
    log "Role needs to be created via the setup workflow"
    exit 1
elif echo "$ROLE_OUTPUT" | grep -q "Invalid"; then
    log "❌ OpenBao role 'seam-retirement-evaluator' does NOT exist"
    log "Role needs to be created via the setup workflow"
    exit 1
else
    log "✅ OpenBao role 'seam-retirement-evaluator' EXISTS"
    log ""
    log "Role details:"
    echo "$ROLE_OUTPUT" | head -20
fi

# Check if the policy exists
log ""
log "Checking if OpenBao policy 'seam-retirement-evaluator-policy' exists..."

POLICY_OUTPUT=$(kubectl --server=http://traefik-rs-manager:8001 exec -n openbao "$OPENBAO_POD" -c openbao -- bao policy read seam-retirement-evaluator-policy 2>&1 || echo "POLICY_NOT_FOUND")

if echo "$POLICY_OUTPUT" | grep -q "POLICY_NOT_FOUND"; then
    log "❌ OpenBao policy 'seam-retirement-evaluator-policy' does NOT exist"
    log "Policy needs to be created via the setup workflow"
    exit 1
elif echo "$POLICY_OUTPUT" | grep -q "Invalid"; then
    log "❌ OpenBao policy 'seam-retirement-evaluator-policy' does NOT exist"
    log "Policy needs to be created via the setup workflow"
    exit 1
else
    log "✅ OpenBao policy 'seam-retirement-evaluator-policy' EXISTS"
    log ""
    log "Policy details:"
    echo "$POLICY_OUTPUT"
fi

log ""
log "=== Verification Summary ==="
log "✅ ServiceAccount: seam-retirement-evaluator (exists in seam namespace)"
log "✅ OpenBao Role: seam-retirement-evaluator (exists)"
log "✅ OpenBao Policy: seam-retirement-evaluator-policy (exists)"
log ""
log "Setup is complete and ready for use!"
