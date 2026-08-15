# SEAM Security Quick Reference

## Critical Security Checks (5-Minute Audit)

### 1. Log Leak Detection
```bash
# Check all SEAM logs for secret patterns
sudo grep -rE "(ghp_|gho_[A-Za-z0-9]{36}|Bearer [A-Za-z0-9]{30,})" /var/log/seam/
# Expected: No matches
```

### 2. Response Leak Detection  
```bash
# Test that error responses don't contain secrets
curl -X POST http://localhost:8080/github-alerts/issues -d '{"invalid":"data"}' | grep -iE "(ghp_|gho_|token)" && echo "LEAK" || echo "SAFE"
# Expected: SAFE
```

### 3. OpenBao Policy Check
```bash
# Verify SEAM cannot read evaluator paths
bao kv get evaluators/seam-retirement-evaluator/github-token 2>&1 | grep -i "permission denied"
# Expected: "permission denied" error
```

### 4. Memory Leak Check
```bash
# Check process memory for secrets
sudo gcore $(pgrep seam) /tmp/seam-core.$$
strings /tmp/seam-core.$$ | grep -iE "(ghp_|gho_|token)" && echo "MEMORY LEAK" || echo "MEMORY SAFE"
sudo rm /tmp/seam-core.$$
# Expected: MEMORY SAFE
```

## Incident Response: Suspected Secret Leak

### Immediate Shutdown (Level 1)
```bash
# 1. Stop SEAM immediately
kubectl scale deployment seam -n seam --replicas=0

# 2. Revoke all OpenBao tokens  
bao lease revoke -prefix "auth/kubernetes/role/seam"

# 3. Capture logs for investigation
kubectl logs -n seam deployment/seam --all-containers=true > /tmp/seam-incident-$(date +%s).log
```

### Emergency Rotation (Level 2 - Confirmed Leak)
```bash
# 1. Rotate all compromised secrets
# 2. Fix security vulnerability in code
# 3. Redeploy with security patch
# 4. Verify fix with full security test suite
```

## Daily Security Monitoring

### Automated Daily Check
```bash
#!/bin/bash
# Run this daily via cron

echo "=== Daily SEAM Security Check ==="

# Log leak check
if sudo grep -rE "(ghp_|gho_[A-Za-z0-9]{36})" /var/log/seam/; then
  echo "CRITICAL: Secret found in logs"
  exit 1
fi

# Policy check  
if ! bao kv get evaluators/seam-retirement-evaluator/github-token 2>&1 | grep -i "permission denied"; then
  echo "CRITICAL: OpenBao policy violation"
  exit 1
fi

echo "✓ Daily security check passed"
```

## Security Test Commands

### Run Full Security Suite
```bash
# Unit tests for secret handling
go test -v ./internal/server -run TestOpenBaoTokenAccessDenial

# E2E isolation tests
go test -v ./internal/server -run TestE2EIsolation

# Memory safety tests
go test -v ./internal/server -run TestPanicWithSecretInScope
```

### Run Penetration Tests
```bash
# Hostile fragment attack simulation
./scripts/pen-test-hostile-fragment.sh

# Memory dump attack
./scripts/pen-test-memory-dump.sh

# OpenBao bypass attempts
./scripts/pen-test-openbao-bypass.sh
```

## Critical Security Patterns

### Detect: Secret Patterns to Watch For
```
ghp_[A-Za-z0-9]{36}           # GitHub Personal Access Token
gho_[A-Za-z0-9]{36}           # GitHub OAuth Token  
ghu_[A-Za-z0-9]{20}           # GitHub User Token
sk_test_[A-Za-z0-9]{24}       # Stripe Test Key
Bearer [A-Za-z0-9]{30,}       # Bearer Tokens
api[_-]?key['\"]?\s*[:=]\s*['\"]?[A-Za-z0-9]{20,}  # API Keys
```

### Safe: Reference-Only Patterns
```
secret/data/seam/routes/*          # OpenBao path references
evaluators/seam-retirement-evaluator/*  # Secret path metadata
x-vault-path: seam/routes/*        # Fragment configuration
```

## The 2026-08-09 Incident: Lessons Learned

### What Happened
A worker pasted a live `gho_` GitHub OAuth token into documentation twice. Only Forgejo's pre-receive hook prevented a public leak.

### Root Cause
Treating "document the token" and "document how to obtain the token" as the same task.

### Prevention
1. ✅ `PreToolUse` hook blocks writes with credential values
2. ✅ SEAM architecture: secrets travel by reference, never value
3. ✅ This audit guide validates no leaks in any context

### Your Responsibility
**NEVER write a credential value.** Write the retrieval path instead:
- ✅ `secret/evaluators/seam-retirement-evaluator/github-token`
- ❌ `gho_1234567890abcdef...`

## When to Sound the Alarm

### CRITICAL (Immediate Response Required)
- ❌ Secret found in logs
- ❌ Secret found in error responses
- ❌ Secret found in memory dumps
- ❌ OpenBao policy violation detected

### HIGH (Investigation Required)
- ⚠️  Security test failures
- ⚠️  Unexpected policy changes
- ⚠️  Network policy violations
- ⚠️  ServiceAccount boundary issues

### NORMAL (Continuous Monitoring)
- ✓ Daily security scans pass
- ✓ Log monitoring shows no issues
- ✓ Memory analysis shows no leaks
- ✓ Policy enforcement tests pass

## Quick Links

- **Full Security Guide:** `docs/security-audit-penetration-test-guide.md`
- **Security Isolation Model:** `docs/security-isolation-model.md`
- **Testing Runbook:** `docs/testing-isolation-runbook.md`
- **Project Rules:** `AGENTS.md`

## Emergency Contacts

For security incidents requiring immediate escalation:
1. Review this quick reference
2. Follow Level 1/Level 2 incident response procedures
3. Document all investigation steps
4. Update this reference with lessons learned

---

**Remember:** Security is a process, not a product. When in doubt, shut it down and investigate.