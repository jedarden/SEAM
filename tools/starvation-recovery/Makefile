# Makefile for starvation-recovery tool

BINARY=starvation-recovery
BUILD_DIR=build
GO=go

.PHONY: all build clean install test

all: build

build:
	$(GO) build -o $(BUILD_DIR)/$(BINARY) main.go

clean:
	rm -rf $(BUILD_DIR)

install: build
	sudo cp $(BUILD_DIR)/$(BINARY) /usr/local/bin/
	@echo "Installed $(BINARY) to /usr/local/bin"

test:
	$(GO) test -v ./...

run:
	$(GO) run main.go --once --verbose

# Build for deployment
build-release:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -ldflags="-s -w" -o $(BUILD_DIR)/$(BINARY) main.go

# Development
fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...
