#!/bin/bash
# Simple OpenBao connection verification script
# Usage: ./scripts/test-openbao-connection.sh

set -e

OPENBAO_ADDR="${OPENBAO_ADDR:-http://openbao.external-secrets.svc.cluster.local:8200}"
OPENBAO_TOKEN="${OPENBAO_TOKEN:-}"

echo "🔍 Testing OpenBao connection to: $OPENBAO_ADDR"
echo "🔑 Token: ${OPENBAO_TOKEN:+(set)}"

# Test 1: Health endpoint (no auth required)
echo ""
echo "📋 Test 1: Health endpoint check"
HEALTH_RESPONSE=$(curl -s -w "\n%{http_code}" --connect-timeout 5 "$OPENBAO_ADDR/v1/sys/health" 2>/dev/null || echo "000")
HTTP_CODE=$(echo "$HEALTH_RESPONSE" | tail -1)

if [ "$HTTP_CODE" = "200" ] || [ "$HTTP_CODE" = "501" ]; then
    echo "✅ Health endpoint reachable (HTTP $HTTP_CODE)"
else
    echo "❌ Health check failed (HTTP $HTTP_CODE)"
    exit 1
fi

# Test 2: Read secret (requires auth)
if [ -n "$OPENBAO_TOKEN" ]; then
    echo ""
    echo "📋 Test 2: Secret read access"

    # Try to read from a simple test path
    SECRET_RESPONSE=$(curl -s -w "\n%{http_code}" \
        --connect-timeout 5 \
        -H "X-Vault-Token: $OPENBAO_TOKEN" \
        "$OPENBAO_ADDR/v1/secret/seam/test-connection" 2>/dev/null || echo "000")

    HTTP_CODE=$(echo "$SECRET_RESPONSE" | tail -1)
    BODY=$(echo "$SECRET_RESPONSE" | head -n -1)

    case "$HTTP_CODE" in
        200)
            echo "✅ Secret read successful"
            echo "📄 Response: $BODY"
            ;;
        404)
            echo "✅ Secret read access verified (404 = path doesn't exist, but auth works)"
            ;;
        403)
            echo "❌ Authentication failed - token may be invalid (HTTP 403)"
            echo "📄 Response: $BODY"
            exit 1
            ;;
        000)
            echo "❌ Connection failed - server may be unreachable"
            exit 1
            ;;
        *)
            echo "⚠️  Unexpected response (HTTP $HTTP_CODE)"
            echo "📄 Response: $BODY"
            ;;
    esac
else
    echo ""
    echo "⏭️  Skipping secret read test (no OPENBAO_TOKEN set)"
    echo "💡 Set OPENBAO_TOKEN to test authenticated access"
fi

echo ""
echo "✅ OpenBao connection verification complete!"
echo ""
echo "📋 Connection Details:"
echo "   Address: $OPENBAO_ADDR"
echo "   Auth: ${OPENBAO_TOKEN:+Token-based} ${OPENBAO_TOKEN:-"(none)"}"
echo "   Health: ✅ Verified"
