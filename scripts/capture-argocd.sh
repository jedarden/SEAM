#!/bin/bash
# capture-argocd.sh - Start corpus capture for argocd-ro proxy
#
# This script starts the seam-capture tool to record HTTP request/response
# pairs from the argocd-ro proxy for later replay testing.
#
# Usage:
#   ./scripts/capture-argocd.sh [start|stop|status]
#
# The captured corpus is saved to corpus/argocd-proxy/corpus.json
# and can be replayed using seam-replay for differential testing.

set -e

# Configuration
INCUMBENT_URL="${SEAM_ARGOCD_INCUMBENT_URL:-https://argocd-ro-ardenone-manager-ts.ardenone.com:8444}"
SERVICE="argocd"
CORPUS_PATH="corpus/argocd-proxy/corpus.json"
CAPTURE_PORT="${SEAM_CAPTURE_PORT:-8082}"
CAPTURE_ENABLED="${SEAM_CAPTURE_ENABLED:-true}"
DESCRIPTION="ArgoCD read-only proxy corpus captured from production"
BINARY="./seam-capture"
PIDFILE=".seam-capture.pid"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if seam-capture binary exists
check_binary() {
    if [ ! -f "$BINARY" ]; then
        log_error "seam-capture binary not found at $BINARY"
        log_info "Build it with: go build -o seam-capture ./tools/diffharness/cmd/seam-capture/main.go"
        exit 1
    fi
}

# Start the capture process
start_capture() {
    check_binary

    if [ -f "$PIDFILE" ]; then
        PID=$(cat "$PIDFILE")
        if ps -p "$PID" > /dev/null 2>&1; then
            log_warn "Capture already running (PID: $PID)"
            return 0
        else
            log_warn "Removing stale PID file"
            rm -f "$PIDFILE"
        fi
    fi

    # Ensure corpus directory exists
    mkdir -p "$(dirname "$CORPUS_PATH")"

    log_info "Starting corpus capture for argocd-ro proxy"
    log_info "  Incumbent URL: $INCUMBENT_URL"
    log_info "  Listen port: $CAPTURE_PORT"
    log_info "  Capture enabled: $CAPTURE_ENABLED"
    log_info "  Corpus path: $CORPUS_PATH"
    log_info ""
    log_info "To make test requests:"
    log_info "  curl -sk http://localhost:${CAPTURE_PORT}/api/v1/applications"
    log_info "  curl -sk http://localhost:${CAPTURE_PORT}/api/v1/clusters"
    log_info ""

    nohup "$BINARY" \
        --incumbent "$INCUMBENT_URL" \
        --service "$SERVICE" \
        --corpus "$CORPUS_PATH" \
        --listen ":$CAPTURE_PORT" \
        --capture-enabled="$CAPTURE_ENABLED" \
        --description "$DESCRIPTION" \
        > /tmp/seam-capture.log 2>&1 &

    PID=$!
    echo $PID > "$PIDFILE"

    sleep 1

    if ps -p $PID > /dev/null 2>&1; then
        log_info "Capture started successfully (PID: $PID)"
        log_info "Log file: /tmp/seam-capture.log"
    else
        log_error "Failed to start capture (check log: /tmp/seam-capture.log)"
        rm -f "$PIDFILE"
        exit 1
    fi
}

# Stop the capture process
stop_capture() {
    if [ ! -f "$PIDFILE" ]; then
        log_warn "Capture not running (no PID file)"
        return 0
    fi

    PID=$(cat "$PIDFILE")

    if ! ps -p "$PID" > /dev/null 2>&1; then
        log_warn "Capture not running (stale PID file)"
        rm -f "$PIDFILE"
        return 0
    fi

    log_info "Stopping capture (PID: $PID)"
    kill $PID
    rm -f "$PIDFILE"

    # Wait for process to terminate
    for i in {1..10}; do
        if ! ps -p $PID > /dev/null 2>&1; then
            log_info "Capture stopped"
            log_info "Corpus saved to: $CORPUS_PATH"

            # Show corpus stats
            if [ -f "$CORPUS_PATH" ]; then
                ENTRIES=$(jq '.entries | length' "$CORPUS_PATH" 2>/dev/null || echo "0")
                log_info "Total entries captured: $ENTRIES"
            fi
            return 0
        fi
        sleep 1
    done

    log_warn "Process did not stop gracefully, forcing..."
    kill -9 $PID 2>/dev/null || true
    rm -f "$PIDFILE"
}

# Show capture status
status_capture() {
    if [ ! -f "$PIDFILE" ]; then
        echo "Status: NOT RUNNING"
        return 0
    fi

    PID=$(cat "$PIDFILE")

    if ps -p "$PID" > /dev/null 2>&1; then
        echo "Status: RUNNING"
        echo "PID: $PID"
        echo "Listen port: $CAPTURE_PORT"
        echo "Incumbent URL: $INCUMBENT_URL"
        echo "Corpus path: $CORPUS_PATH"

        # Show corpus stats if available
        if [ -f "$CORPUS_PATH" ]; then
            ENTRIES=$(jq '.entries | length' "$CORPUS_PATH" 2>/dev/null || echo "0")
            echo "Entries captured: $ENTRIES"

            # Show last few entry descriptions
            if [ "$ENTRIES" -gt 0 ]; then
                echo "Recent captures:"
                jq -r '.entries[-3:] | .[] | "  - \(.description // .id)"' "$CORPUS_PATH" 2>/dev/null || true
            fi
        fi
    else
        echo "Status: NOT RUNNING (stale PID file)"
        rm -f "$PIDFILE"
    fi
}

# Main command dispatcher
case "${1:-start}" in
    start)
        start_capture
        ;;
    stop)
        stop_capture
        ;;
    status)
        status_capture
        ;;
    restart)
        stop_capture
        sleep 1
        start_capture
        ;;
    *)
        echo "Usage: $0 {start|stop|status|restart}"
        exit 1
        ;;
esac
