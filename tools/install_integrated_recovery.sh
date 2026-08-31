#!/usr/bin/env bash
#
# Install script for Integrated Starvation Recovery System
#
# This script installs and enables the integrated recovery system as a systemd user service.
#

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_FILE="$SCRIPT_DIR/integrated-starvation-recovery.service"
USER_SYSTEMD_DIR="$HOME/.config/systemd/user"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*"
}

echo "Installing Integrated Starvation Recovery System..."

# Create user systemd directory if it doesn't exist
mkdir -p "$USER_SYSTEMD_DIR"

# Copy service file
if [[ ! -f "$SERVICE_FILE" ]]; then
    log_error "Service file not found: $SERVICE_FILE"
    exit 1
fi

cp "$SERVICE_FILE" "$USER_SYSTEMD_DIR/"
log_success "Service file installed to: $USER_SYSTEMD_DIR"

# Reload systemd
if systemctl --user daemon-reload 2>/dev/null; then
    log_success "Systemd user daemon reloaded"
else
    log_warning "Failed to reload systemd daemon - you may need to run: systemctl --user daemon-reload"
fi

# Enable and start service
if systemctl --user enable integrated-starvation-recovery.service 2>/dev/null; then
    log_success "Service enabled"
else
    log_warning "Failed to enable service - you may need to run: systemctl --user enable integrated-starvation-recovery.service"
fi

if systemctl --user start integrated-starvation-recovery.service 2>/dev/null; then
    log_success "Service started"
else
    log_warning "Failed to start service - you may need to run: systemctl --user start integrated-starvation-recovery.service"
fi

# Show status
echo ""
echo "Service status:"
if systemctl --user status integrated-starvation-recovery.service 2>/dev/null; then
    log_success "Service is running"
else
    log_warning "Check service status with: systemctl --user status integrated-starvation-recovery.service"
fi

echo ""
echo "Installation complete!"
echo ""
echo "To manage the service:"
echo "  Start:   systemctl --user start integrated-starvation-recovery.service"
echo "  Stop:    systemctl --user stop integrated-starvation-recovery.service"
echo "  Status:  systemctl --user status integrated-starvation-recovery.service"
echo "  Logs:    journalctl --user -u integrated-starvation-recovery.service -f"
echo "  Disable: systemctl --user disable integrated-starvation-recovery.service"
echo ""
echo "To run manually (one-shot):"
echo "  integrated-starvation-recovery --once --verbose"
echo ""
echo "To run with custom interval (e.g., 10 minutes):"
echo "  integrated-starvation-recovery --interval 10 --verbose"
