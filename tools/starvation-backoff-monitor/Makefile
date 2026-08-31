# Makefile for starvation-backoff-monitor

BINARY_NAME=starvation-backoff-monitor
GOFLAGS=-ldflags="-s -w"

.PHONY: all build clean install test

all: build

build:
	go build $(GOFLAGS) -o $(BINARY_NAME) .

clean:
	rm -f $(BINARY_NAME)
	go clean

install: build
	install -m 755 $(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)

test:
	go test -v ./...

test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	rm coverage.out

# Run locally with default settings
run: build
	./$(BINARY_NAME) --verbose

# Run with custom workspace
run-custom: build
	./$(BINARY_NAME) --workspace-root $(PWD) --verbose

# Run with leader election enabled (requires K8s cluster access)
run-k8s: build
	./$(BINARY_NAME) --enable-lease --lease-namespace seam-monitoring --verbose

# Build for multiple platforms
build-all:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o $(BINARY_NAME)-linux-amd64 .
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -o $(BINARY_NAME)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -o $(BINARY_NAME)-darwin-arm64 .
