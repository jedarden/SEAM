.PHONY: run build test clean

# Default target
run:
	go run cmd/seam/main.go serve

# Build the seam binary
build:
	go build -o seam cmd/seam/main.go

# Run tests
test:
	go test ./...

# Clean build artifacts
clean:
	rm -f seam
	go clean

# Install dependencies
deps:
	go mod download
	go mod tidy
