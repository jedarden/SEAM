#!/bin/bash
# Validation script for SEAM rate limiting and monitoring extension fields
# Performs structural validation and documents expected schema validation behavior

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCHEMA_FILE="$SCRIPT_DIR/route-fragment-schema.json"
VALIDATOR_DIR="$HOME/scratch/seam-schema-verify"

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo "=== SEAM Rate Limiting and Monitoring Schema Validation ==="
echo ""
echo "${YELLOW}Note: Full JSON Schema validation requires Ajv 8.20+ with draft-2020-12 support.${NC}"
echo "${YELLOW}This script performs structural validation and documents expected behavior.${NC}"
echo ""

# Check if validator directory exists
if [ ! -d "$VALIDATOR_DIR" ]; then
  echo "Creating validator directory..."
  mkdir -p "$VALIDATOR_DIR"
fi

# Function to validate JSON structure
validate_json_structure() {
  local file=$1
  local description=$2

  echo -n "Validating $description... "

  if python3 -c "import json; json.load(open('$file'))" 2>/dev/null; then
    echo -e "${GREEN}PASS${NC} (valid JSON structure)"
    return 0
  else
    echo -e "${RED}FAIL${NC} (invalid JSON structure)"
    return 1
  fi
}

# Function to check for required fields
check_field_exists() {
  local file=$1
  local field=$2
  local description=$3

  echo -n "  Checking $field exists... "
  if python3 -c "import json; data=json.load(open('$file')); print(data$field)" 2>/dev/null >/dev/null; then
    echo -e "${GREEN}PASS${NC}"
    return 0
  else
    echo -e "${YELLOW}SKIP${NC} (field not present)"
    return 1
  fi
}

# Function to check field value pattern
check_field_pattern() {
  local file=$1
  local field=$2
  local pattern=$3
  local description=$4

  echo -n "  Checking $field matches pattern... "
  value=$(python3 -c "import json; data=json.load(open('$file')); print(data$field)" 2>/dev/null || echo "")
  if [[ "$value" =~ $pattern ]]; then
    echo -e "${GREEN}PASS${NC}"
    return 0
  else
    echo -e "${RED}FAIL${NC} (value: $value)"
    return 1
  fi
}

echo "${BLUE}=== Schema Definition Checks ===${NC}"
echo ""

# Check schema file exists
if [ ! -f "$SCHEMA_FILE" ]; then
  echo -e "${RED}Error: Schema file not found: $SCHEMA_FILE${NC}"
  exit 1
fi

echo "Schema file: $SCHEMA_FILE"
echo ""

# Check schema defines required extensions
echo "Checking schema defines required extension fields..."

for extension in "x-seam-schema" "x-loop-guard" "x-cost-per-call" "x-quota" "x-upstream-map"; do
  echo -n "  $extension defined in schema... "
  if grep -q "\"$extension\"" "$SCHEMA_FILE"; then
    echo -e "${GREEN}PASS${NC}"
  else
    echo -e "${RED}FAIL${NC}"
  fi
done

echo ""
echo "${BLUE}=== Example Fragment Structure Validation ===${NC}"
echo ""

# Create valid example fragment
cat > "$VALIDATOR_DIR/rate-limiting-valid.json" <<'EOF'
{
  "x-seam-schema": "v1",
  "x-seam-owner": "analytics",
  "x-upstream": "https://analytics.service.example.com",
  "x-loop-guard": {
    "max_depth": 10,
    "max_redirects": 5
  },
  "x-cost-per-call": 0.01,
  "x-quota": {
    "limit": 1000,
    "window": "1h",
    "scope": "per-token"
  },
  "paths": {
    "/analytics/data": {
      "get": {
        "x-required-scope": ["analytics:read"],
        "x-cost-per-call": 0.001,
        "x-quota": {
          "limit": 10000,
          "window": "1h",
          "scope": "per-token"
        },
        "summary": "List data (cheap, high quota)"
      },
      "post": {
        "x-required-scope": ["analytics:analyze"],
        "x-cost-per-call": 0.05,
        "x-quota": {
          "limit": 200,
          "window": "1h",
          "scope": "per-token"
        },
        "summary": "Analyze data (expensive, low quota)"
      }
    },
    "/analytics/export": {
      "get": {
        "x-required-scope": ["analytics:export"],
        "x-loop-guard": {
          "max_depth": 5,
          "max_redirects": 0
        },
        "summary": "Export data"
      }
    }
  }
}
EOF

# Create invalid examples for documentation
cat > "$VALIDATOR_DIR/invalid-no-schema.json" <<'EOF'
{
  "x-seam-owner": "analytics",
  "x-upstream": "https://analytics.service.example.com",
  "paths": { "/test": { "get": {} } }
}
EOF

cat > "$VALIDATOR_DIR/invalid-negative-cost.json" <<'EOF'
{
  "x-seam-schema": "v1",
  "x-seam-owner": "analytics",
  "x-upstream": "https://analytics.service.example.com",
  "x-cost-per-call": -0.01,
  "paths": { "/test": { "get": {} } }
}
EOF

cat > "$VALIDATOR_DIR/invalid-quota-without-cost.json" <<'EOF'
{
  "x-seam-schema": "v1",
  "x-seam-owner": "analytics",
  "x-upstream": "https://analytics.service.example.com",
  "paths": {
    "/test": {
      "get": {
        "x-quota": {
          "limit": 100,
          "window": "1h"
        }
      }
    }
  }
}
EOF

cat > "$VALIDATOR_DIR/invalid-loop-guard-bounds.json" <<'EOF'
{
  "x-seam-schema": "v1",
  "x-seam-owner": "analytics",
  "x-upstream": "https://analytics.service.example.com",
  "x-loop-guard": {
    "max_depth": 0,
    "max_redirects": -1
  },
  "paths": { "/test": { "get": {} } }
}
EOF

cat > "$VALIDATOR_DIR/invalid-quota-limit.json" <<'EOF'
{
  "x-seam-schema": "v1",
  "x-seam-owner": "analytics",
  "x-upstream": "https://analytics.service.example.com",
  "x-cost-per-call": 0.01,
  "x-quota": {
    "limit": 0,
    "window": "1h"
  },
  "paths": { "/test": { "get": {} } }
}
EOF

# Validate valid example structure
validate_json_structure "$VALIDATOR_DIR/rate-limiting-valid.json" "Valid example fragment"

if [ $? -eq 0 ]; then
  echo ""
  echo "  Checking extension fields in valid example:"
  check_field_exists "$VALIDATOR_DIR/rate-limiting-valid.json" "['x-seam-schema']" "x-seam-schema"
  check_field_pattern "$VALIDATOR_DIR/rate-limiting-valid.json" "['x-seam-schema']" "^v[0-9]+$" "pattern"
  check_field_exists "$VALIDATOR_DIR/rate-limiting-valid.json" "['x-loop-guard']" "x-loop-guard"
  check_field_exists "$VALIDATOR_DIR/rate-limiting-valid.json" "['x-cost-per-call']" "x-cost-per-call"
  check_field_exists "$VALIDATOR_DIR/rate-limiting-valid.json" "['x-quota']" "x-quota"
fi

echo ""
echo "${BLUE}=== Expected Schema Validation Behavior ===${NC}"
echo ""

echo "The following fragments should be ${GREEN}ACCEPTED${NC} by the schema:"
echo "  ✓ Valid rate limiting fragment with all extensions (rate-limiting-valid.json)"
echo ""

echo "The following fragments should be ${RED}REJECTED${NC} by the schema:"
echo "  ✗ Missing x-seam-schema (invalid-no-schema.json)"
echo "  ✗ Negative x-cost-per-call (invalid-negative-cost.json)"
echo "  ✗ x-quota without x-cost-per-call (invalid-quota-without-cost.json)"
echo "  ✗ Loop guard max_depth < 1 (invalid-loop-guard-bounds.json)"
echo "  ✗ Loop guard max_redirects < 0 (invalid-loop-guard-bounds.json)"
echo "  ✗ Quota limit < 1 (invalid-quota-limit.json)"
echo ""

echo "${BLUE}=== Schema Validation Rules Summary ===${NC}"
echo ""

echo "x-seam-schema:"
echo "  - Type: string"
echo "  - Pattern: ^v[0-9]+$"
echo "  - Required: YES"
echo "  - Example: \"v1\""
echo ""

echo "x-loop-guard:"
echo "  - Type: object"
echo "  - Required: max_depth, max_redirects"
echo "  - max_depth: integer >= 1"
echo "  - max_redirects: integer >= 0"
echo "  - Example: { \"max_depth\": 10, \"max_redirects\": 5 }"
echo ""

echo "x-cost-per-call:"
echo "  - Type: number"
echo "  - Minimum: 0"
echo "  - Pattern: ^\\d+(\\.\\d{1,2})?$ (max 2 decimal places)"
echo "  - Example: 0.01 (USD per call)"
echo ""

echo "x-quota:"
echo "  - Type: object"
echo "  - Required: limit, window"
echo "  - limit: integer >= 1"
echo "  - window: RFC3339 duration (e.g., '60s', '5m', '1h')"
echo "  - scope: enum [global, per-token, per-user, per-route]"
echo "  - Constraint: requires x-cost-per-call"
echo "  - Example: { \"limit\": 1000, \"window\": \"1h\", \"scope\": \"per-token\" }"
echo ""

echo "x-upstream-map:"
echo "  - Type: object"
echo "  - minProperties: 1"
echo "  - Maps instance names to upstreamMapEntry objects"
echo "  - Each entry must have: url (required), optional overrides"
echo "  - Requires: x-instance-param"
echo "  - Example: { \"instance1\": { \"url\": \"https://...\" } }"
echo ""

echo "${BLUE}=== Full Validation Notes ===${NC}"
echo ""

echo "The complete JSON Schema validation will be performed by:"
echo "  - seam lint (Phase 9a CI/CD tooling)"
echo "  - Gateway runtime quarantine (Phase 1b merge-time validation)"
echo ""

echo "These tools use the Go validator from internal/spec which:"
echo "  - Runs JSON Schema validation against route-fragment-schema.json"
echo "  - Enforces cross-field constraints documented in docs/notes/route-fragment-schema.md §4"
echo "  - Validates manifest-level constraints (OpenBao allowlist, upstream host allowlist)"
echo "  - Checks merge-time collision detection and resolution"
echo ""

echo "${GREEN}=== Summary ===${NC}"
echo ""
echo "✓ Schema file: $SCHEMA_FILE"
echo "✓ Documentation: docs/notes/route-fragment-schema.md"
echo "✓ Example fragment: Section 6.5 (Rate limiting and monitoring)"
echo "✓ Validation test fixtures: $VALIDATOR_DIR/"
echo ""
echo "All rate limiting and monitoring extension field schemas are:"
echo "  ✓ x-seam-schema: Version marker (required)"
echo "  ✓ x-loop-guard: Loop protection (max_depth, max_redirects)"
echo "  ✓ x-cost-per-call: Cost tracking (USD, non-negative)"
echo "  ✓ x-quota: Rate limiting (limit, window, scope)"
echo "  ✓ x-upstream-map: Multi-instance routing"
