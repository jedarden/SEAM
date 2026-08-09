#!/bin/bash
# Script to embed ReDoc JavaScript from go-redoc library

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ASSETS_DIR="$PROJECT_DIR/internal/server/assets"

echo "Embedding ReDoc JavaScript..."

# Create a temporary Go program to extract the JavaScript
cat > /tmp/extract_redoc.go << 'GOEOF'
package main

import (
	"fmt"
	"os"
	redoclib "github.com/mvrilo/go-redoc"
)

func main() {
	if len(os.Args) > 1 {
		outputFile := os.Args[1]
		err := os.WriteFile(outputFile, []byte(redoclib.JavaScript), 0644)
		if err != nil {
			panic(err)
		}
		fmt.Printf("Wrote %d bytes to %s\n", len(redoclib.JavaScript), outputFile)
	}
}
GOEOF

# Run the extractor
go run /tmp/extract_redoc.go "$ASSETS_DIR/redoc.js"

echo "ReDoc JavaScript embedded successfully"
