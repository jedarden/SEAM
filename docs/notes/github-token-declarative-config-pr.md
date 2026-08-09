# GitHub Token for declarative-config Pull Requests

## Purpose
Dedicated GitHub Personal Access Token (PAT) for opening pull requests against `jedarden/declarative-config` repository on GitHub.

## Token Requirements

### Repository
- **Target:** `jedarden/declarative-config` on GitHub (github.com/jedarden/declarative-config)
- **Note:** This is the GitHub mirror, NOT the Forgejo origin (git.ardenone.com)

### Permissions Required
- `repo` (Full repository access)
  - `repo:status` - Access commit status
  - `repo_deployment` - Access deployment status
  - `public_repo` - Access public repos (if needed)
  - `repo:invite` - Accept repo invitations
  - `security_events` - View security events

### Token Type
**Fine-grained Personal Access Token** (recommended)
- Repository-specific access to `jedarden/declarative-config`
- Read/write permissions for:
  - Contents (read/write)
  - Pull requests (read/write)
  - Issues (read - for PR linking)

## Creation Procedure

### Via GitHub Web UI

1. Navigate to: https://github.com/settings/tokens
2. Click "Generate new token" → "Generate new token (classic)"
3. Configure:
   - **Note:** "SEAM declarative-config PR token (bf-2hwgv)"
   - **Expiration:** 90 days (or as per policy)
   - **Scopes:** Check `repo` (full repository access)
4. Click "Generate token"
5. **Copy the token immediately** - it won't be shown again

### Via GitHub CLI (fallback)

```bash
# This uses existing authentication to display the current token
gh auth token

# For a dedicated token, use the web UI procedure above
```

## Current Authentication Status

As of 2026-08-09, the GitHub CLI is authenticated with:
- **Account:** jedarden (Jed Arden)
- **Scopes:** gist, read:org, repo, user, workflow
- **Token:** _not recorded here — retrieve at use time with `gh auth token`_ (existing token)
- **Permissions on declarative-config:** ADMIN (can create PRs, push, admin)

**Verified:** The existing token already has the required `repo` scope and ADMIN permissions on declarative-config. It can open PRs immediately.

## Token Provisioning Options

### Option A: Use Existing Token (Immediate)
**Current Token:** retrieve with `gh auth token`; never paste the value into this repo
- **Pros:** Immediately available, already verified, has required permissions
- **Cons:** Shared with personal GitHub CLI use, not dedicated to evaluator
- **Best for:** Initial testing and development

### Option B: Create Dedicated Token (Recommended for Production)
Create a separate token specifically for the seam-retirement-evaluator:
- **Pros:** Isolated identity, can be rotated independently, audit separation
- **Cons:** Requires manual creation via GitHub web UI
- **Best for:** Production deployment and long-term operation

**Recommendation:** Start with Option A for testing, then transition to Option B for production.

## Storage Strategy

### OpenBao Secret Path
```
secret/SEAM/github/declarative-config-pr
```

### Secret Structure
```json
{
  "token": "gho_XXXXXXXXXXXXXXXX",
  "created": "2026-08-09T00:00:00Z",
  "expires": "2026-11-07T00:00:00Z",
  "purpose": "Pull requests to declarative-config",
  "repo": "jedarden/declarative-config",
  "created_by": "bf-2hwgv"
}
```

### Access Control
- OpenBao role: `seam-retirement-evaluator` (created in parallel task bf-2hwgu)
- Kubernetes ServiceAccount: `seam-retirement-evaluator` in `seam` namespace
- TTL: 1 hour (default lease)

## Testing Procedure

After provisioning, verify the token can open a PR:

```bash
# Test with a dry-run or validation PR
export GH_TOKEN="gho_XXXXXXXXXXXXXXXX"
gh pr list --repo jedarden/declarative-config

# Create a test PR (when ready)
gh pr create \
  --repo jedarden/declarative-config \
  --title "Test: Token validation" \
  --body "Automated test to verify token permissions" \
  --head feature-branch \
  --base main
```

## Tracking

- **Bead ID:** bf-2hwgv
- **Created:** 2026-08-09
- **Status:** Pending provisioning and OpenBao storage
- **Dependencies:** None (parallel with OpenBao role creation)

## Security Considerations

1. **Token Scope:** Use fine-grained tokens when possible (limit to specific repository)
2. **Rotation:** Rotate tokens before expiration (recommended: 90-day cycle)
3. **Access:** Only the `seam-retirement-evaluator` role should access this token
4. **Audit:** Monitor OpenBao access logs for token retrieval
5. **Revocation:** Immediate revocation if token is compromised or no longer needed

## Related Documentation

- [OpenBao Role Creation](../openbao/seam-retirement-evaluator-role.md) (bf-2hwgu)
- [SEAM Retirement Evaluator Plan](../plan/seam-retirement-evaluator.md)
