#!/bin/bash
# GitHub Token Provisioning Script for seam-retirement-evaluator
# Task: bf-2hwgv
#
# This script creates or configures a GitHub token for the evaluator
# and prepares it for storage in OpenBao

set -e

log() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $1"; }
error() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] ERROR: $1" >&2; exit 1; }

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log "=== GitHub Token Provisioning for seam-retirement-evaluator ==="

# Check GitHub CLI availability
if ! command -v gh >/dev/null 2>&1; then
  error "GitHub CLI not found. Install with: brew install gh"
fi

# Verify authentication
log "Checking GitHub authentication..."
if ! gh auth status >/dev/null 2>&1; then
  error "GitHub CLI not authenticated. Run: gh auth login"
fi

# Get current user and verify repo access
GITHUB_USER=$(gh api /user --jq '.login')
REPO_ACCESS=$(gh repo view jedarden/declarative-config --json viewerPermission --jq '.viewerPermission')

log "✓ Authenticated as: $GITHUB_USER"
log "✓ Repository permissions: $REPO_ACCESS"

if [[ "$REPO_ACCESS" != "ADMIN" && "$REPO_ACCESS" != "MAINTAIN" && "$REPO_ACCESS" != "WRITE" ]]; then
  error "Insufficient permissions on declarative-config repository"
fi

# Display current token info
log "=== Current GitHub Token ==="
CURRENT_TOKEN=$(gh auth token)
echo "Token: ${CURRENT_TOKEN:0:12}... (masked)"
echo "This token has the required permissions for opening PRs"

# Ask user for choice
echo ""
echo "Choose token provisioning option:"
echo "1) Use existing GitHub token (immediate, for testing)"
echo "2) Create dedicated token (recommended for production)"
echo "3) Exit and create token manually later"
read -p "Enter choice [1-3]: " choice

case $choice in
  1)
    log "=== Using Existing Token ==="
    TOKEN="$CURRENT_TOKEN"
    TOKEN_TYPE="existing"
    log "✓ Using existing token: ${TOKEN:0:12}..."
    ;;
  2)
    log "=== Creating Dedicated Token ==="
    cat <<'INSTRUCTIONS'

To create a dedicated token manually:

1. Visit: https://github.com/settings/tokens
2. Click "Generate new token" → "Generate new token (classic)"
3. Configure:
   - Note: "seam-retirement-evaluator PR token (bf-2hwgv)"
   - Expiration: 90 days (or as per policy)
   - Scopes: repo (Full control of private repositories)
4. Click "Generate token"
5. Copy the token immediately - it won't be shown again

INSTRUCTIONS

    read -p "Paste the new token here: " TOKEN
    if [[ -z "$TOKEN" || ! "$TOKEN" =~ ^gho_ ]]; then
      error "Invalid token format. Tokens start with 'gho_'"
    fi
    TOKEN_TYPE="dedicated"
    log "✓ New token received: ${TOKEN:0:12}..."
    ;;
  3)
    log "Exiting. Create token manually and store in OpenBao later."
    exit 0
    ;;
  *)
    error "Invalid choice. Exiting."
    ;;
esac

# Verify token works
log "=== Verifying Token ==="
export GH_TOKEN="$TOKEN"
if gh repo view jedarden/declarative-config --json name >/dev/null 2>&1; then
  log "✓ Token verified - can access declarative-config"
else
  error "Token verification failed"
fi

# Display storage instructions
log "=== OpenBao Storage Instructions ==="
cat <<STORAGE

Store the token in OpenBao at this path:

Path: secret/data/seam-retirement-evaluator/github-token

Using OpenBao CLI:
  bao kv put secret/seam-retirement-evaluator/github-token \
    token="$TOKEN" \
    created="$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
    type="$TOKEN_TYPE" \
    created_by="bf-2hwgv" \
    repo="jedarden/declarative-config" \
    purpose="Pull requests for seam-retirement-evaluator"

IMPORTANT SECURITY NOTES:
- Store OUTSIDE seam/routes/* hierarchy ✓
- Only evaluator ServiceAccount can read ✓
- SEAM's OpenBao role MUST NOT have access ✓
- Token has minimal scope (repo only) ✓

STORAGE

# Save token info for reference
TOKEN_FILE="$HOME/.seam-github-token-${TOKEN_TYPE}"
cat > "$TOKEN_FILE" <<TOKENFILE
# GitHub Token for seam-retirement-evaluator
# Task: bf-2hwgv
# Created: $(date -u '+%Y-%m-%dT%H:%M:%SZ')
# Type: $TOKEN_TYPE

TOKEN_TYPE=$TOKEN_TYPE
TOKEN=${TOKEN:0:12}... (full token stored in OpenBao)
REPO=jedarden/declarative-config
PURPOSE=Pull requests for seam-retirement-evaluator
OPENBAO_PATH=secret/data/seam-retirement-evaluator/github-token
CREATED_BY=bf-2hwgv
TOKENFILE

log "✓ Token reference saved to: $TOKEN_FILE"
log "=== Provisioning Complete ==="
log ""
log "Next steps:"
log "1. Store token in OpenBao (see instructions above)"
log "2. Run OpenBao setup workflow (task bf-2hwgu)"
log "3. Verify evaluator can read the token"
log ""
log -e "${GREEN}Token ready for OpenBao storage!${NC}"
