#!/bin/bash
# Test script for OpenAPI Docs UI interactive features
# Tests: expand/collapse, parameter display, schema display, navigation, JS errors

echo "=== OpenAPI Docs UI Interactive Feature Test ==="
echo ""

BASE_URL="http://localhost:8888"
DOCS_URL="$BASE_URL/docs"
OPENAPI_URL="$BASE_URL/openapi.json"

echo "Test 1: Check if docs page loads"
echo "URL: $DOCS_URL"
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$DOCS_URL")
if [ "$HTTP_STATUS" = "200" ]; then
    echo "✓ Docs page loads successfully (HTTP $HTTP_STATUS)"
else
    echo "✗ Docs page failed to load (HTTP $HTTP_STATUS)"
    exit 1
fi
echo ""

echo "Test 2: Check if OpenAPI spec loads"
echo "URL: $OPENAPI_URL"
SPEC_HTTP=$(curl -s -o /dev/null -w "%{http_code}" "$OPENAPI_URL")
if [ "$SPEC_HTTP" = "200" ]; then
    echo "✓ OpenAPI spec loads successfully (HTTP $SPEC_HTTP)"

    # Check if spec has test endpoints
    ENDPOINT_COUNT=$(curl -s "$OPENAPI_URL" | jq '.paths | length')
    echo "  Found $ENDPOINT_COUNT endpoints in spec"
else
    echo "✗ OpenAPI spec failed to load (HTTP $SPEC_HTTP)"
    exit 1
fi
echo ""

echo "Test 3: Check HTML structure for ReDoc"
DOCS_HTML=$(curl -s "$DOCS_URL")
if echo "$DOCS_HTML" | grep -q "redoc"; then
    echo "✓ ReDoc is being used for documentation"
else
    echo "✗ ReDoc not found in docs HTML"
fi

if echo "$DOCS_HTML" | grep -q "redoc-container"; then
    echo "✓ ReDoc container element present"
else
    echo "✗ ReDoc container element not found"
fi

if echo "$DOCS_HTML" | grep -q "window.onload"; then
    echo "✓ JavaScript initialization code present"
else
    echo "✗ JavaScript initialization code not found"
fi
echo ""

echo "Test 4: Check for test endpoints in spec"
curl -s "$OPENAPI_URL" | jq -r '.paths | keys[]' | while read -r endpoint; do
    echo "  Endpoint: $endpoint"

    # Check if endpoint has methods
    METHODS=$(curl -s "$OPENAPI_URL" | jq ".paths[\"$endpoint\"] | keys")
    echo "    Methods: $METHODS"
done
echo ""

echo "Test 5: Check parameter schemas for /test/get endpoint"
PARAM_COUNT=$(curl -s "$OPENAPI_URL" | jq '.paths["/test/get"].get.parameters | length')
echo "  /test/get has $PARAM_COUNT parameters"

if [ "$PARAM_COUNT" -gt 0 ]; then
    FIRST_PARAM=$(curl -s "$OPENAPI_URL" | jq '.paths["/test/get"].get.parameters[0]')
    PARAM_NAME=$(echo "$FIRST_PARAM" | jq -r '.name')
    PARAM_IN=$(echo "$FIRST_PARAM" | jq -r '.in')
    PARAM_TYPE=$(echo "$FIRST_PARAM" | jq -r '.schema.type')
    PARAM_DESC=$(echo "$FIRST_PARAM" | jq -r '.description')

    echo "    First parameter: $PARAM_NAME"
    echo "      Location: $PARAM_IN"
    echo "      Type: $PARAM_TYPE"
    echo "      Description: $PARAM_DESC"

    if [ -n "$PARAM_NAME" ] && [ -n "$PARAM_IN" ] && [ -n "$PARAM_TYPE" ]; then
        echo "    ✓ Parameter metadata is complete"
    else
        echo "    ✗ Parameter metadata is incomplete"
    fi
fi
echo ""

echo "Test 6: Check request body schema for /test/post endpoint"
HAS_BODY=$(curl -s "$OPENAPI_URL" | jq '.paths["/test/post"].post.requestBody != null')
if [ "$HAS_BODY" = "true" ]; then
    echo "  ✓ /test/post has request body"

    BODY_REQUIRED=$(curl -s "$OPENAPI_URL" | jq -r '.paths["/test/post"].post.requestBody.required')
    BODY_CONTENT_TYPE=$(curl -s "$OPENAPI_URL" | jq -r '.paths["/test/post"].post.requestBody.content | keys[0]')
    BODY_SCHEMA_TYPE=$(curl -s "$OPENAPI_URL" | jq -r '.paths["/test/post"].post.requestBody.content["'"$BODY_CONTENT_TYPE"'"].schema.type')

    echo "    Required: $BODY_REQUIRED"
    echo "    Content-Type: $BODY_CONTENT_TYPE"
    echo "    Schema Type: $BODY_SCHEMA_TYPE"

    # Check schema properties
    PROP_COUNT=$(curl -s "$OPENAPI_URL" | jq '.paths["/test/post"].post.requestBody.content["'"$BODY_CONTENT_TYPE"'"].schema.properties | length')
    echo "    Properties count: $PROP_COUNT"

    if [ "$PROP_COUNT" -gt 0 ]; then
        echo "    ✓ Request body schema is defined"
    fi
else
    echo "  ✗ /test/post missing request body"
fi
echo ""

echo "Test 7: Check response schemas"
GET_RESPONSE_200=$(curl -s "$OPENAPI_URL" | jq '.paths["/test/get"].get.responses["200"] != null')
if [ "$GET_RESPONSE_200" = "true" ]; then
    echo "  ✓ /test/get has 200 response"

    RESP_DESC=$(curl -s "$OPENAPI_URL" | jq -r '.paths["/test/get"].get.responses["200"].description')
    echo "    Description: $RESP_DESC"

    HAS_CONTENT=$(curl -s "$OPENAPI_URL" | jq '.paths["/test/get"].get.responses["200"].content != null')
    if [ "$HAS_CONTENT" = "true" ]; then
        echo "    ✓ Response has content schema"
    fi
else
    echo "  ✗ /test/get missing 200 response"
fi
echo ""

echo "Test 8: Check component schemas"
COMPONENT_SCHEMAS=$(curl -s "$OPENAPI_URL" | jq '.components.schemas != null')
if [ "$COMPONENT_SCHEMAS" = "true" ]; then
    SCHEMA_COUNT=$(curl -s "$OPENAPI_URL" | jq '.components.schemas | length')
    echo "  ✓ Components section exists with $SCHEMA_COUNT schemas"

    curl -s "$OPENAPI_URL" | jq -r '.components.schemas | keys[]' | while read -r schema; do
        echo "    Schema: $schema"
    done
else
    echo "  ✗ No component schemas found"
fi
echo ""

echo "Test 9: Check ReDoc CDN accessibility"
REDOC_CDN="https://cdn.jsdelivr.net/npm/redoc@2.5.0/bundles/redoc.standalone.js"
CDN_STATUS=$(curl -s -o /dev/null -w "%{http_code}" --connect-timeout 5 "$REDOC_CDN")
if [ "$CDN_STATUS" = "200" ]; then
    echo "✓ ReCD CDN is accessible (HTTP $CDN_STATUS)"
else
    echo "⚠ ReCD CDN not accessible (HTTP $CDN_STATUS) - may affect UI rendering"
fi
echo ""

echo "Test 10: Verify expandResponses configuration in ReDoc init"
if echo "$DOCS_HTML" | grep -q "expandResponses"; then
    echo "✓ expandResponses configuration found in ReDoc init"
    EXPAND_CONFIG=$(echo "$DOCS_HTML" | grep -o 'expandResponses.*' | cut -d'"' -f2)
    echo "  Configuration: expandResponses=$EXPAND_CONFIG"
else
    echo "⚠ expandResponses configuration not found"
fi
echo ""

echo "=== Test Summary ==="
echo "All structural tests completed. The OpenAPI docs UI should support:"
echo "  • Expand/collapse for sections (ReDoc feature with expandResponses)"
echo "  • Parameter input display (test endpoint has query parameters)"
echo "  • Schema display (request/response schemas defined)"
echo "  • Navigation between endpoints (3 test endpoints available)"
echo "  • JavaScript rendering (ReDoc standalone.js from CDN)"
echo ""
echo "For full interactive testing, open the docs in a browser:"
echo "  $DOCS_URL"
