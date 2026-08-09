#!/bin/bash
set -e

log() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $1"; }

# OpenBao runs on rs-manager itself (not ardenone-cluster)
OPENBAO_ADDR="http://openbao-rs-manager.openbao.svc.cluster.local:8200"
EVALUATOR_SA="seam-retirement-evaluator"
EVALUATOR_NS="seam"
GITHUB_TOKEN_PATH="secret/data/seam-retirement-evaluator/github-token"

log "== Starting OpenBao provisioning for seam-retirement-evaluator =="

# Check if running in cluster (for OpenBao access)
if [ -f "/var/run/secrets/kubernetes.io/serviceaccount/token" ]; then
  log "Running in Kubernetes cluster, will attempt OpenBao operations"
  IN_CLUSTER=true
else
  log "Not running in cluster, will generate OpenBao setup instructions"
  IN_CLUSTER=false
fi

# Step 1: Create OpenBao policy for evaluator
log "Creating OpenBao policy for seam-retirement-evaluator..."
POLICY_HCL=$(cat <<'EOF'
# OpenBao HCL policy for seam-retirement-evaluator
# Allows read access to evaluator's own GitHub token path and VictoriaMetrics credentials
# Explicitly denies access to seam/routes/* to ensure SEAM cannot read evaluator's token

# Allow reading evaluator's own GitHub token
path "secret/data/seam-retirement-evaluator/*" {
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
EOF
)

if [ "$IN_CLUSTER" = true ]; then
  # This would require OpenBao root token - skip for now
  log "Note: OpenBao policy creation requires root token - will be configured manually"
else
  log "Generated policy HCL for manual OpenBao configuration"
fi

# Step 2: Create GitHub token instructions
log "Creating GitHub token setup instructions..."
cat <<'EOF'

================================================================================
GitHub Token Setup Instructions for seam-retirement-evaluator
================================================================================

The evaluator needs a GitHub Personal Access Token (PAT) with the following
scope and permissions:

GitHub Repository: jedarden/declarative-config
Token Capabilities:
  - repo (full repository access)
  - pull_requests (read/write)
  - contents:write (for creating PRs)

Token Storage Location in OpenBao:
  Path: secret/data/seam-retirement-evaluator/github-token
  Format: JSON with "token" field containing the PAT value

Example OpenBao write command (requires OpenBao root/admin token):

  vault kv put openbao/secret/seam-retirement-evaluator/github-token \
    token=ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx

Security Requirements:
  - Token MUST be stored OUTSIDE seam/routes/* hierarchy
  - Only evaluator's ServiceAccount can read this path
  - SEAM's OpenBao role MUST NOT have access to this path
  - Token should have minimal scope (only repo + PR operations)

GitHub Token Creation Steps:
  1. Go to GitHub Settings → Developer settings → Personal access tokens → Tokens (classic)
  2. Generate new token (classic)
  3. Set note: "seam-retirement-evaluator PR token"
  4. Set expiration (recommended: 90 days)
  5. Select scopes:
     - repo (Full control of private repositories)
  6. Generate token and copy immediately
  7. Store in OpenBao using the command above

================================================================================

EOF

# Step 3: Create Kubernetes auth role configuration
log "Generating Kubernetes auth role configuration..."
cat <<EOF

================================================================================
OpenBao Kubernetes Auth Role Configuration
================================================================================

Role Name: seam-retirement-evaluator
Bound ServiceAccount: seam-retirement-evaluator@seam
Bound Namespace: seam
Token Policies: seam-retirement-evaluator-policy
Token TTL: 24h
Token Max TTL: 72h

OpenBao Configuration Commands (requires OpenBao admin/root access):

vault write auth/kubernetes/role/seam-retirement-evaluator \\
    bound_service_account_names=seam-retirement-evaluator \\
    bound_service_account_namespaces=seam \\
    policies=seam-retirement-evaluator-policy \\
    token_ttl=24h \\
    token_max_ttl=72h \\
    token_default_policies=seam-retirement-evaluator-policy

# Create the policy from the HCL file above
vault policy write seam-retirement-evaluator-policy - <<EOF
$POLICY_HCL
EOF

================================================================================

EOF

log "== Provisioning instructions generated =="
log "Next steps:"
log "1. Create GitHub PAT with repo scope"
log "2. Store token in OpenBao at secret/data/seam-retirement-evaluator/github-token"
log "3. Configure OpenBao Kubernetes auth role for evaluator ServiceAccount"
log "4. Configure VictoriaMetrics read access"
log "5. Verify SEAM cannot access evaluator's token path"
