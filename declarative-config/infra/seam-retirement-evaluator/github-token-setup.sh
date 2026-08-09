#!/bin/bash
set -e

log() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $1"; }
error() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] ERROR: $1" >&2; exit 1; }

log "=== GitHub Token Setup for seam-retirement-evaluator ==="

# GitHub token creation using GitHub CLI
if command -v gh >/dev/null 2>&1; then
  log "GitHub CLI found - creating token via gh CLI..."

  # Check if authenticated
  if ! gh auth status >/dev/null 2>&1; then
    error "GitHub CLI not authenticated. Run: gh auth login"
  fi

  # Create token with appropriate scopes
  log "Creating GitHub token with repo scope..."
  # Note: GitHub CLI token creation requires manual approval, this is just documentation
  cat <<'EOF'

To create a GitHub Personal Access Token manually:

1. Visit: https://github.com/settings/tokens
2. Click "Generate new token" → "Generate new token (classic)"
3. Set:
   - Note: "seam-retirement-evaluator PR token"
   - Expiration: 90 days
   - Scopes: repo (Full control of private repositories)
4. Click "Generate token"
5. Copy the token immediately - it won't be shown again

EOF

  # Alternative: use existing GitHub token from environment
  if [ -n "$GITHUB_TOKEN" ]; then
    log "Found GITHUB_TOKEN in environment - using this for evaluator"
    EVALUATOR_TOKEN="$GITHUB_TOKEN"
  else
    log "No GITHUB_TOKEN found - please create token manually"
  fi
else
  log "GitHub CLI not found - manual token creation required"
  cat <<'EOF'

GitHub Token Creation Required:
===============================
1. Visit: https://github.com/settings/tokens
2. Generate new token (classic)
3. Set scopes: repo (Full control of private repositories)
4. Copy token and set as GITHUB_TOKEN environment variable

EOF
fi

# OpenBao storage instructions
log "=== OpenBao Token Storage Instructions ==="
cat <<'EOF'

Once you have the GitHub token, store it in OpenBao at:

Path: secret/data/seam-retirement-evaluator/github-token

Using OpenBao CLI (vault):
  vault kv put secret/seam-retirement-evaluator/github-token \
    token=ghp_your_token_here

Using OpenBao API:
  curl -X POST http://openbao-ardenone-cluster.openbao.svc.cluster.local:8200/v1/secret/data/seam-retirement-evaluator/github-token \
    -H "X-Vault-Token: your_openbao_token" \
    -H "Content-Type: application/json" \
    -d '{"data":{"token":"ghp_your_token_here"}}'

IMPORTANT Security Requirements:
- Store OUTSIDE seam/routes/* hierarchy ✓
- Only evaluator ServiceAccount can read ✓
- SEAM's OpenBao role MUST NOT have access ✓
- Token has minimal scope (repo only) ✓

EOF

log "=== Setup complete ==="
log "Next steps: Store the GitHub token in OpenBao and configure Kubernetes auth role"
