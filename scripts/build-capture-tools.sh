#!/bin/bash
# build-capture-tools.sh - Build the SEAM corpus capture and replay tools

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "Building SEAM corpus capture and replay tools..."
echo "Repo root: $REPO_ROOT"
echo ""

# Build seam-capture
echo "Building seam-capture..."
cd "$REPO_ROOT/tools/diffharness"
go build -o ../../seam-capture ./cmd/seam-capture/main.go
echo "✓ seam-capture built -> $REPO_ROOT/seam-capture"

# Build seam-replay
echo "Building seam-replay..."
go build -o ../../seam-replay ./cmd/seam-replay/main.go
echo "✓ seam-replay built -> $REPO_ROOT/seam-replay"

cd "$REPO_ROOT"

echo ""
echo "All tools built successfully!"
echo ""
echo "Available binaries:"
echo "  seam-capture - Capture HTTP traffic from incumbent proxies"
echo "  seam-replay  - Replay corpus against two targets for differential testing"
echo ""
echo "Quick start:"
echo "  ./scripts/capture-argocd.sh start   # Start capturing"
echo "  ./scripts/capture-argocd.sh stop    # Stop and save corpus"
echo "  ./seam-replay --help                # See replay options"
