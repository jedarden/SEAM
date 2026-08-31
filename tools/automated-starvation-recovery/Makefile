.PHONY: all install test clean help

# Default target
all: help

# Install the tool
install:
	@echo "Installing automated starvation recovery tool..."
	chmod +x automated_recovery.py
	@echo "✓ Tool installed and made executable"

# Create symlink for easy access
link: install
	@echo "Creating symlink in /usr/local/bin..."
	ln -sf $(PWD)/automated_recovery.py /usr/local/bin/automated-starvation-recovery
	@echo "✓ Symlink created: /usr/local/bin/automated-starvation-recovery"

# Install systemd service
install-service: install
	@echo "Installing systemd service..."
	systemctl link $(PWD)/automated-starvation-recovery.service
	systemctl daemon-reload
	@echo "✓ Service installed. Enable with: sudo systemctl enable --now automated-starvation-recovery"

# Run tests
test:
	@echo "Running automated recovery tool in dry-run mode..."
	./automated_recovery.py --once --dry-run --verbose

# Run once (production mode)
run:
	./automated_recovery.py --once --verbose

# Clean temporary files
clean:
	@echo "Cleaning temporary files..."
	rm -f /tmp/automated-recovery-*.log
	rm -f /tmp/bead_visibility_diagnostic.json
	@echo "✓ Clean complete"

# Help
help:
	@echo "Automated Starvation Recovery Tool - Makefile"
	@echo ""
	@echo "Targets:"
	@echo "  make install         - Install tool (make executable)"
	@echo "  make link           - Create symlink in /usr/local/bin"
	@echo "  make install-service - Install systemd service"
	@echo "  make test           - Run in dry-run mode"
	@echo "  make run            - Run once (production mode)"
	@echo "  make clean          - Clean temporary files"
	@echo "  make help           - Show this help message"
