# OpenBao SEAM Role and Policy Setup

## Task Status

**Bead:** `bf-5rx9` (Precondition of Phase 2)
**Status:** Files prepared, awaiting admin token execution

## What Has Been Created

All files are ready in `/home/coding/SEAM/declarative-config/infra/seam/`:

1. **`seam-openbao-policy.hcl`** - OpenBao policy granting read ONLY on `seam/routes/*`
2. **`setup-seam-openbao.sh`** - Automated setup script with verification
3. **Documentation:** See `/home/coding/SEAM/docs/notes/openbao-seam-setup.md`

## The Policy

```hcl
# Allow reading SEAM route secrets ONLY
path "secret/data/seam/routes/*" {
  capabilities = ["read"]
}

# Deny access to evaluator's secrets (explicit separation)
path "secret/data/seam-retirement-evaluator/*" {
  capabilities = ["deny"]
}

# Deny access to all other secrets (default-deny)
path "secret/data/*" {
  capabilities = ["deny"]
}
```

**Critical security properties:**
- Read-only access to `seam/routes/*` ONLY
- Explicit deny for all other paths
- Hostile-fragment threat model: even a malicious fragment cannot exfiltrate other secrets

## How to Execute (Requires Admin Token)

### Prerequisites
```bash
# OpenBao is running at:
BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"

# You need an admin token (root token or token with admin policy):
BAO_TOKEN="hvs.xxxxx"  # From password manager
```

### Quick Execution
```bash
cd /home/coding/SEAM/declarative-config/infra/seam
export BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
export BAO_TOKEN="<your-admin-token>"
./setup-seam-openbao.sh
```

### Manual Step-by-Step
```bash
export BAO_ADDR="http://openbao-ardenone.tail1b1987.ts.net:8200"
export BAO_TOKEN="<your-admin-token>"

# 1. Write the policy
bao policy write seam seam-openbao-policy.hcl

# 2. Create the Kubernetes auth role
bao write auth/kubernetes/role/seam \
  bound_service_account_names=seam \
  bound_service_account_namespaces=seam \
  policies=seam \
  ttl=24h \
  max_ttl=72h

# 3. Verify
bao read auth/kubernetes/role/seam
bao policy read seam
```

## What the Setup Script Does

The script (`setup-seam-openbao.sh`) automatically:

1. **Creates policy "seam"** - Read on `seam/routes/*` only
2. **Creates role "seam"** - Kubernetes auth bound to SA `seam` in namespace `seam`
3. **Creates test secret** - At `seam/routes/test-secret`
4. **Verifies permissions**:
   - ✓ Can read `seam/routes/test-secret`
   - ✓ Denied `seam-retirement-evaluator/*`
   - ✓ Denied `kalshi/*`
   - ✓ Denied listing `secret/`
5. **Cleans up** - Revokes test token

## Verification Criteria

**Task completion requirement:** A token obtained via this role can:
- Read `seam/routes/test-secret` → SUCCESS
- Be denied `seam-retirement-evaluator/*` → DENIED
- Be denied `kalshi/*` → DENIED
- Be denied listing `secret/` → DENIED

The setup script performs all these checks automatically.

## What Remains (6a Deliverable)

This precondition creates the **server-side** OpenBao configuration. Phase 6a will create the **cluster-side** resources:

1. ServiceAccount `seam` in namespace `seam`
2. SEAM deployment with projected service account token
3. In-pod OpenBao login using Kubernetes auth
4. First successful login proof

## Troubleshooting

### "bao: command not found"
```bash
# The script detects and uses either bao or vault CLI
# Install OpenBao CLI: https://openbao.org/docs/install
```

### "Kubernetes auth method not enabled"
```bash
bao auth enable kubernetes
```

### "Invalid token"
- Token may have expired
- Check token has admin policies: `bao lookup token <token>`

### "Cannot connect to OpenBao"
- Check BAO_ADDR is correct
- Verify OpenBao is running: `curl ${BAO_ADDR}/v1/sys/health`
- Check Tailscale connection

## Security Notes

- **Root tokens** are stored in a password manager (see `openbao-dr-runbook.md`)
- This policy implements the **hostile-fragment threat model**
- SEAM's token has literally no access outside `seam/routes/*`
- Even if lint is bypassed, the gateway's token cannot reach other paths

## Files Summary

| File | Purpose |
|------|---------|
| `seam-openbao-policy.hcl` | Policy definition (read seam/routes/* only) |
| `setup-seam-openbao.sh` | Automated setup with verification |
| `docs/notes/openbao-seam-setup.md` | Detailed documentation |

All files are in `/home/coding/SEAM/declarative-config/infra/seam/`
