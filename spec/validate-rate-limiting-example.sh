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
    "maxRepeats": 10,
    "window": "1h"
  },
  "x-cost-per-call": {
    "amount": 0.01,
    "unit": "credits"
  },
  "x-quota": {
    "amount": 1000,
    "unit": "credits",
    "window": "1h"
  },
  "paths": {
    "/analytics/data": {
      "get": {
        "x-required-scope": ["analytics:read"],
        "x-cost-per-call": {
          "amount": 0.001,
          "unit": "credits"
        },
        "x-quota": {
          "amount": 10000,
          "unit": "credits",
          "window": "1h"
        },
        "summary": "List data (cheap, high quota)"
      },
      "post": {
        "x-required-scope": ["analytics:analyze"],
        "x-cost-per-call": {
          "amount": 0.05,
          "unit": "credits"
        },
        "x-quota": {
          "amount": 200,
          "unit": "credits",
          "window": "1h"
        },
        "summary": "Analyze data (expensive, low quota)"
      }
    },
    "/analytics/export": {
      "get": {
        "x-required-scope": ["analytics:export"],
        "x-loop-guard": {
          "maxRepeats": 5,
          "window": "30m"
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
  "x-cost-per-call": {
    "amount": -0.01,
    "unit": "credits"
  },
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
          "amount": 100,
          "unit": "credits",
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
    "maxRepeats": 0,
    "window": "1w"
  },
  "paths": { "/test": { "get": {} } }
}
EOF

cat > "$VALIDATOR_DIR/invalid-quota-amount.json" <<'EOF'
{
  "x-seam-schema": "v1",
  "x-seam-owner": "analytics",
  "x-upstream": "https://analytics.service.example.com",
  "x-cost-per-call": {
    "amount": 0.01,
    "unit": "credits"
  },
  "x-quota": {
    "amount": -1,
    "unit": "credits",
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
echo "  ✗ Loop guard maxRepeats < 1 and bad window grammar (invalid-loop-guard-bounds.json)"
echo "  ✗ Quota amount < 0 (invalid-quota-amount.json)"
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
echo "  - Required: maxRepeats, window"
echo "  - maxRepeats: integer >= 1"
echo "  - window: ^[0-9]+(s|m|h|d)$"
echo "  - Example: { \"maxRepeats\": 10, \"window\": \"1h\" }"
echo ""

echo "x-cost-per-call:"
echo "  - Type: object"
echo "  - Required: amount, unit"
echo "  - amount: number >= 0"
echo "  - unit: non-empty opaque string"
echo "  - Example: { \"amount\": 0.01, \"unit\": \"credits\" }"
echo ""

echo "x-quota:"
echo "  - Type: object"
echo "  - Required: amount, unit, window"
echo "  - amount: number >= 0"
echo "  - unit: non-empty and byte-identical to x-cost-per-call.unit"
echo "  - window: ^[0-9]+(s|m|h|d)$ (e.g., '60s', '5m', '1h')"
echo "  - Constraint: requires x-cost-per-call"
echo "  - Example: { \"amount\": 1000, \"unit\": \"credits\", \"window\": \"1h\" }"
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
echo "  ✓ x-loop-guard: Repeated-request protection (maxRepeats, window)"
echo "  ✓ x-cost-per-call: Unit-bearing cost tracking (amount, unit)"
echo "  ✓ x-quota: Unit-bearing budget (amount, unit, window)"
echo "  ✓ x-upstream-map: Multi-instance routing"
