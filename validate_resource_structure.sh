#!/bin/bash
# Validate SEAM route fragment structure against schema

echo "=== SEAM Resource Structure Validation ==="
echo ""

FRAGMENTS_DIR="/home/coding/SEAM/fragments"
SCHEMA_FILE="/home/coding/SEAM/spec/route-fragment-schema.json"

# Check if jq is available
if ! command -v jq &> /dev/null; then
    echo "Error: jq is required but not installed"
    exit 1
fi

# Check if ajv-cli is available for JSON Schema validation
if ! command -v ajv &> /dev/null; then
    echo "Warning: ajv-cli not found. Installing..."
    npm install -g ajv-cli
fi

echo "Searching for fragment files..."
FRAGMENT_FILES=$(find "$FRAGMENTS_DIR" -type f \( -name "*.json" -o -name "*.yaml" -o -name "*.yml" \))

TOTAL_COUNT=0
VALID_COUNT=0
MALFORMED_FILES=()

for file in $FRAGMENT_FILES; do
    TOTAL_COUNT=$((TOTAL_COUNT + 1))
    echo ""
    echo "Validating: $file"

    # Convert YAML to JSON if needed
    if [[ "$file" == *.yaml || "$file" == *.yml ]]; then
        if ! command -v yq &> /dev/null; then
            echo "  ⚠️  SKIP: yq not installed for YAML processing"
            continue
        fi
        CONTENT=$(yq eval -o=json "$file" 2>/dev/null)
        if [ $? -ne 0 ]; then
            echo "  ❌ ERROR: Failed to parse YAML"
            MALFORMED_FILES+=("$file: YAML parse error")
            continue
        fi
    else
        CONTENT=$(cat "$file")
    fi

    # Extract field values
    x_seam_schema=$(echo "$CONTENT" | jq -r '.["x-seam-schema"] // empty' 2>/dev/null)
    x_seam_owner=$(echo "$CONTENT" | jq -r '.["x-seam-owner"] // empty' 2>/dev/null)
    paths=$(echo "$CONTENT" | jq -r '.paths // empty' 2>/dev/null)
    x_upstream=$(echo "$CONTENT" | jq -r '.["x-upstream"] // empty' 2>/dev/null)
    x_upstream_map=$(echo "$CONTENT" | jq -r '.["x-upstream-map"] // empty' 2>/dev/null)
    x_adapter=$(echo "$CONTENT" | jq -r '.["x-adapter"] // empty' 2>/dev/null)

    ERRORS=()

    # Check required fields
    if [ -z "$x_seam_schema" ]; then
        ERRORS+=("Missing required field: x-seam-schema")
    else
        # Validate pattern: ^v[0-9]+$
        if ! [[ "$x_seam_schema" =~ ^v[0-9]+$ ]]; then
            ERRORS+=("Invalid x-seam-schema pattern: '$x_seam_schema' (must match ^v[0-9]+\$)")
        fi
    fi

    if [ -z "$x_seam_owner" ]; then
        ERRORS+=("Missing required field: x-seam-owner")
    else
        # Validate pattern: ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$
        if ! [[ "$x_seam_owner" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]]; then
            ERRORS+=("Invalid x-seam-owner pattern: '$x_seam_owner' (must be lowercase alphanumeric with hyphens)")
        fi
    fi

    if [ -z "$paths" ] || [ "$paths" == "null" ]; then
        ERRORS+=("Missing required field: paths")
    else
        # Check if paths has at least one property
        path_count=$(echo "$CONTENT" | jq '.paths | length' 2>/dev/null)
        if [ "$path_count" -lt 1 ]; then
            ERRORS+=("paths object must have at least one property (found $path_count)")
        fi
    fi

    # Check upstream constraints
    has_upstream=$([ ! -z "$x_upstream" ] && [ "$x_upstream" != "null" ] && echo "1" || echo "0")
    has_upstream_map=$([ ! -z "$x_upstream_map" ] && [ "$x_upstream_map" != "null" ] && echo "1" || echo "0")
    has_adapter=$([ ! -z "$x_adapter" ] && [ "$x_adapter" != "null" ] && echo "1" || echo "0")

    upstream_count=$((has_upstream + has_upstream_map + has_adapter))

    if [ $upstream_count -eq 0 ]; then
        ERRORS+=("Fragment must declare exactly one of: x-upstream, x-upstream-map, or x-adapter (found none)")
    elif [ $upstream_count -gt 1 ]; then
        ERRORS+=("Fragment must declare exactly one of: x-upstream, x-upstream-map, or x-adapter (found $upstream_count)")
    fi

    # Display results
    if [ ${#ERRORS[@]} -eq 0 ]; then
        echo "  ✅ VALID: Structure matches schema"
        VALID_COUNT=$((VALID_COUNT + 1))
    else
        echo "  ❌ MALFORMED: Found ${#ERRORS[@]} structural issue(s)"
        for error in "${ERRORS[@]}"; do
            echo "     - $error"
        done
        MALFORMED_FILES+=("$file: ${ERRORS[*]}")
    fi
done

echo ""
echo "=== Validation Summary ==="
echo "Total files checked: $TOTAL_COUNT"
echo "Valid files: $VALID_COUNT"
echo "Malformed files: $((TOTAL_COUNT - VALID_COUNT))"

if [ ${#MALFORMED_FILES[@]} -gt 0 ]; then
    echo ""
    echo "=== Malformed Files Detail ==="
    for malformed in "${MALFORMED_FILES[@]}"; do
        echo "❌ $malformed"
    done
    exit 1
else
    echo ""
    echo "✅ All resources are structurally valid!"
    exit 0
fi
