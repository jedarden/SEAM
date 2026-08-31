.PHONY: all install test clean help

# Default target
all: help

# Install the tool
install:
	@echo "Installing bead visibility diagnostic tool..."
	chmod +x bead_visibility_diagnostic.py
	@echo "✓ Tool installed and made executable"

# Create symlink for easy access
link: install
	@echo "Creating symlink in /usr/local/bin..."
	ln -sf $(PWD)/bead_visibility_diagnostic.py /usr/local/bin/bead-visibility-diagnostic
	@echo "✓ Symlink created: /usr/local/bin/bead-visibility-diagnostic"

# Install systemd user service
install-service: install
	@echo "Installing systemd user service..."
	mkdir -p ~/.config/systemd/user
	cp bead-visibility-diagnostic.service ~/.config/systemd/user/
	systemctl --user daemon-reload
	@echo "✓ Service installed. Enable with: make enable-service"

# Enable and start the service
enable-service:
	@echo "Enabling and starting bead-visibility-diagnostic service..."
	systemctl --user enable bead-visibility-diagnostic.service
	systemctl --user start bead-visibility-diagnostic.service
	@echo "✓ Service enabled and started"
	@echo "Check status with: systemctl --user status bead-visibility-diagnostic.service"

# Disable and stop the service
disable-service:
	@echo "Stopping and disabling bead-visibility-diagnostic service..."
	systemctl --user stop bead-visibility-diagnostic.service || true
	systemctl --user disable bead-visibility-diagnostic.service || true
	@echo "✓ Service disabled and stopped"

# Check service status
status:
	@echo "Checking bead-visibility-diagnostic service status..."
	systemctl --user status bead-visibility-diagnostic.service

# View service logs
logs:
	@echo "Showing bead-visibility-diagnostic service logs..."
	journalctl --user -u bead-visibility-diagnostic.service -f

# Run tests
test:
	@echo "Running bead visibility diagnostic tool in test mode..."
	./bead_visibility_diagnostic.py --once --log-dir /home/coding/SEAM/.beads/diagnostics

# Run once (production mode)
run:
	./bead_visibility_diagnostic.py --once --verbose --log-dir /home/coding/SEAM/.beads/diagnostics

# Run in foreground for debugging
debug:
	@echo "Running bead visibility diagnostic tool in foreground (1-minute interval)..."
	./bead_visibility_diagnostic.py --interval 1 --log-dir /home/coding/SEAM/.beads/diagnostics

# Clean temporary files
clean:
	@echo "Cleaning temporary files..."
	rm -f /tmp/bead-visibility-diagnostic-*.log
	rm -f /tmp/bead_visibility_diagnostic.json
	@echo "✓ Clean complete"

# Uninstall the service
uninstall: disable-service
	@echo "Uninstalling bead-visibility-diagnostic service..."
	rm -f ~/.config/systemd/user/bead-visibility-diagnostic.service
	systemctl --user daemon-reload
	@echo "✓ Service uninstalled"

# Help
help:
	@echo "Bead Visibility Diagnostic Tool - Makefile"
	@echo ""
	@echo "Targets:"
	@echo "  make install          - Install tool (make executable)"
	@echo "  make link            - Create symlink in /usr/local/bin"
	@echo "  make install-service - Install systemd user service"
	@echo "  make enable-service  - Enable and start the service"
	@echo "  make disable-service - Disable and stop the service"
	@echo "  make status          - Show service status"
	@echo "  make logs            - Show service logs (follow mode)"
	@echo "  make test            - Run once in test mode"
	@echo "  make run             - Run once (production mode)"
	@echo "  make debug           - Run in foreground (1-minute interval)"
	@echo "  make clean           - Clean temporary files"
	@echo "  make uninstall       - Uninstall the service"
	@echo "  make help            - Show this help message"
