#!/usr/bin/env bash
#
# Install the SEAM CI gate watch as a systemd user timer.
#
# The gate itself (scripts/ci-gate.sh + scripts/ci-gate-bead.sh) only acts
# when someone runs it. This installs the loop that runs it every 5 minutes,
# so a red gate blocks the claim frontier and a green one releases it without
# anybody having to notice either transition. See AGENTS.md, "The seam-ci
# gate".
#
# Usage: tools/install_ci_gate_watch.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_FILE="$SCRIPT_DIR/ci-gate-watch.service"
TIMER_FILE="$SCRIPT_DIR/ci-gate-watch.timer"
USER_SYSTEMD_DIR="$HOME/.config/systemd/user"

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

log_success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }
log_error()   { echo -e "${RED}[ERROR]${NC} $*"; }

for file in "$SERVICE_FILE" "$TIMER_FILE"; do
    if [[ ! -f "$file" ]]; then
        log_error "Required file not found: $file"
        exit 1
    fi
done

mkdir -p "$USER_SYSTEMD_DIR"
install -m 644 "$SERVICE_FILE" "$USER_SYSTEMD_DIR/"
install -m 644 "$TIMER_FILE" "$USER_SYSTEMD_DIR/"

systemctl --user daemon-reload
systemctl --user enable --now ci-gate-watch.timer

# One pass now, so the frontier is correct immediately instead of waiting
# for the first timer tick -- and so a broken install surfaces here rather
# than silently every 5 minutes.
bash "$SCRIPT_DIR/ci-gate-watch.sh" || true

log_success "ci-gate-watch.timer installed and enabled"
systemctl --user list-timers --no-pager | grep ci-gate-watch || true
