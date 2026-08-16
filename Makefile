.PHONY: run build test clean benchmark benchmark-ci

# Default target
run:
	go run cmd/seam/main.go serve

# Build the seam binary
build:
	go build -o seam cmd/seam/main.go

# Run tests
test:
	go test ./...

# Run benchmarks
benchmark:
	go test -bench=. -benchmem -benchtime=10s ./benches/...

# Run benchmarks with CPU profiling
benchmark-cpu:
	go test -bench=. -benchmem -benchtime=10s -cpuprofile=cpu.prof ./benches/...

# Run benchmarks with memory profiling
benchmark-mem:
	go test -bench=. -benchmem -benchtime=10s -memprofile=mem.prof ./benches/...

# CI benchmark run (output format for automated collection)
benchmark-ci:
	go test -bench=. -benchmem -benchtime=5s -json ./benches/... | tee benchmark-results/latest.json

# Clean build artifacts
clean:
	rm -f seam
	go clean

# Install dependencies
deps:
	go mod download
	go mod tidy
