.PHONY: all build install clean test

BINARY_NAME=pluck-fallback
GO=go
GOFLAGS=-v

all: build

build:
	@echo "Building $(BINARY_NAME)..."
	$(GO) build $(GOFLAGS) -o $(BINARY_NAME) main.go

install: build
	@echo "Installing $(BINARY_NAME) to /usr/local/bin..."
	@sudo cp $(BINARY_NAME) /usr/local/bin/ || echo "Note: sudo failed. Install manually with: sudo cp $(BINARY_NAME) /usr/local/bin/"

clean:
	@echo "Cleaning..."
	@rm -f $(BINARY_NAME)
	@go clean

test:
	@echo "Running tests..."
	$(GO) test -v ./...

fmt:
	@echo "Formatting code..."
	$(GO) fmt ./...

lint:
	@echo "Linting code..."
	@golangci-lint run || echo "golangci-lint not installed, skipping"

run-example:
	@echo "Running example..."
	./$(BINARY_NAME) --workspace ../.. --count 1 --verbose

help:
	@echo "Available targets:"
	@echo "  all         - Build the binary (default)"
	@echo "  build       - Build the binary"
	@echo "  install     - Install to /usr/local/bin"
	@echo "  clean       - Remove build artifacts"
	@echo "  test        - Run tests"
	@echo "  fmt         - Format code"
	@echo "  lint        - Run linter"
	@echo "  run-example - Run with example flags"
	@echo "  help        - Show this help message"
