#!/usr/bin/env bash
# Simplified verification script for worker identity isolation tests

echo "=== SEAM Worker Identity Isolation Test Verification ==="
echo ""

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

PASS=0
FAIL=0

check_pass() {
    echo -e "${GREEN}✓${NC} $1"
    ((PASS++)) || true
}

check_fail() {
    echo -e "${RED}✗${NC} $1"
    ((FAIL++)) || true
}

echo "1. Checking test file existence..."

if [ -f "internal/server/worker_identity_integration_test.go" ]; then
    check_pass "Integration test file exists"
else
    check_fail "Integration test file missing"
fi

if [ -f "internal/tailscale/client_test.go" ]; then
    check_pass "Tailscale client test file exists"
else
    check_fail "Tailscale client test file missing"
fi

if [ -f "internal/tailscale/cache_test.go" ]; then
    check_pass "Cache test file exists"
else
    check_fail "Cache test file missing"
fi

echo ""
echo "2. Checking test function coverage..."

TEST_FUNCTIONS=(
    "TestConcurrentWorkerIdentityCreation"
    "TestIdentityTagging"
    "TestIdentityScopeExtraction"
    "TestIdentityResolutionMiddleware"
    "TestIdentityResolutionForNonTailscaleIP"
    "TestConcurrentIdentityResolution"
    "TestTailscaleClientWithMultipleWorkers"
    "TestIdentityStringRepresentation"
    "TestIdentityExpiryCleanup"
)

for test_func in "${TEST_FUNCTIONS[@]}"; do
    if grep -q "func $test_func" internal/server/worker_identity_integration_test.go 2>/dev/null; then
        check_pass "Test function: $test_func"
    else
        check_fail "Test function missing: $test_func"
    fi
done

echo ""
echo "3. Checking documentation..."

if [ -f "docs/testing/worker-identity-testing-guide.md" ]; then
    check_pass "Testing guide documentation exists"
else
    check_fail "Testing guide documentation missing"
fi

echo ""
echo "4. Checking implementation files..."

if [ -f "internal/tailscale/client.go" ]; then
    check_pass "Tailscale client implementation exists"
fi

if [ -f "internal/server/identity.go" ]; then
    check_pass "Identity resolver implementation exists"
fi

if [ -f "internal/tailscale/cache.go" ]; then
    check_pass "Cache implementation exists"
fi

echo ""
echo "=== Summary ==="
echo -e "${GREEN}Passed:${NC} $PASS"
echo -e "${RED}Failed:${NC} $FAIL"

if [ $FAIL -eq 0 ]; then
    echo -e "${GREEN}All checks passed!${NC}"
    exit 0
else
    echo -e "${RED}Some checks failed${NC}"
    exit 1
fi
