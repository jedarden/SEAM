SHELL := /bin/bash

BENCH_PACKAGE ?= ./benches/...
BENCH_TIME ?= 10s
BENCH_FILTER ?= .
PROFILE_DIR ?= benchmark-results
THRESHOLD ?= 10
VERSION ?= $(shell tr -d '\r\n' < containers/seam/VERSION)
REVISION ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
BUILD_LDFLAGS := -X github.com/ardenone/seam/internal/buildinfo.Version=$(VERSION) -X github.com/ardenone/seam/internal/buildinfo.Revision=$(REVISION)

.PHONY: run build test clean benchmark benchmark-cpu benchmark-mem benchmark-ci \
	benchmark-save-baseline benchmark-check-regression benchmark-ci-regression \
	benchmark-update-baseline deps

# Default target
run:
	go run cmd/seam/main.go serve

# Build the seam binary
build:
	go build -ldflags "$(BUILD_LDFLAGS)" -o seam ./cmd/seam

# Run tests
test:
	go test ./...

# Run benchmarks
benchmark:
	go test -run='^$$' -bench='$(BENCH_FILTER)' -benchmem -benchtime=$(BENCH_TIME) $(BENCH_PACKAGE)

# Run benchmarks with CPU profiling
benchmark-cpu:
	mkdir -p $(PROFILE_DIR)
	go test -run='^$$' -bench='$(BENCH_FILTER)' -benchmem -benchtime=$(BENCH_TIME) \
		-cpuprofile=$(PROFILE_DIR)/cpu.prof $(BENCH_PACKAGE)

# Run benchmarks with memory profiling
benchmark-mem:
	mkdir -p $(PROFILE_DIR)
	go test -run='^$$' -bench='$(BENCH_FILTER)' -benchmem -benchtime=$(BENCH_TIME) \
		-memprofile=$(PROFILE_DIR)/mem.prof $(BENCH_PACKAGE)

# CI benchmark run (output format for automated collection)
benchmark-ci:
	mkdir -p $(PROFILE_DIR)
	go test -run='^$$' -bench='$(BENCH_FILTER)' -benchmem -benchtime=$(BENCH_TIME) \
		-json $(BENCH_PACKAGE) | tee $(PROFILE_DIR)/latest.json

# Save benchmark baseline
# Usage: make benchmark-save-baseline TYPE=openbao|memory|throughput
benchmark-save-baseline:
	@if [ -z "$(TYPE)" ]; then \
		echo "Error: TYPE is required. Usage: make benchmark-save-baseline TYPE=openbao|memory|throughput"; \
		exit 1; \
	fi
	@set -o pipefail; \
	case "$(TYPE)" in \
		openbao) filter='OpenBao|DirectConnection' ;; \
		memory) filter='Memory|Concurrent|GC' ;; \
		throughput) filter='Proxy|Scaling|Throughput|DirectConnection' ;; \
		*) echo "Error: Invalid TYPE. Must be: openbao, memory, or throughput" >&2; exit 1 ;; \
	esac; \
	echo "Running $(TYPE) benchmarks and saving baseline..."; \
	go test -run='^$$' -bench="$$filter" -benchmem -benchtime=$(BENCH_TIME) $(BENCH_PACKAGE) | \
		go run ./cmd/baseline -type=$(TYPE) -save-baseline

# Check for performance regressions against baseline
# Usage: make benchmark-check-regression TYPE=openbao|memory|throughput THRESHOLD=10
benchmark-check-regression:
	@if [ -z "$(TYPE)" ]; then \
		echo "Error: TYPE is required. Usage: make benchmark-check-regression TYPE=openbao|memory|throughput"; \
		exit 1; \
	fi
	@set -o pipefail; \
	case "$(TYPE)" in \
		openbao) filter='OpenBao|DirectConnection' ;; \
		memory) filter='Memory|Concurrent|GC' ;; \
		throughput) filter='Proxy|Scaling|Throughput|DirectConnection' ;; \
		*) echo "Error: Invalid TYPE. Must be: openbao, memory, or throughput" >&2; exit 1 ;; \
	esac; \
	echo "Running $(TYPE) benchmarks and checking for regressions..."; \
	go test -run='^$$' -bench="$$filter" -benchmem -benchtime=$(BENCH_TIME) $(BENCH_PACKAGE) | \
		go run ./cmd/baseline -type=$(TYPE) -check-regression -threshold=$(THRESHOLD)

# CI regression check (fails on regression)
benchmark-ci-regression:
	@echo "Checking all benchmark types for regressions..."
	@set -e; \
	for type in openbao memory throughput; do \
		echo "Checking $$type..."; \
		$(MAKE) --no-print-directory benchmark-check-regression TYPE=$$type THRESHOLD=$(THRESHOLD) || exit 1; \
	done
	@echo "✅ All benchmarks passed regression checks"

# Update baseline with current results (after intentional performance changes)
# Usage: make benchmark-update-baseline TYPE=openbao|memory|throughput
benchmark-update-baseline:
	@if [ -z "$(TYPE)" ]; then \
		echo "Error: TYPE is required. Usage: make benchmark-update-baseline TYPE=openbao|memory|throughput"; \
		exit 1; \
	fi
	@echo "⚠️  Updating baseline should only be done for intentional performance changes"
	@read -p "Are you sure you want to update the $(TYPE) baseline? (yes/no): " confirm; \
	if [ "$$confirm" != "yes" ]; then \
		echo "Aborted"; \
		exit 1; \
	fi
	@$(MAKE) benchmark-save-baseline TYPE=$(TYPE)
	@echo "✅ Baseline updated for $(TYPE)"

# Clean build artifacts
clean:
	rm -f seam
	go clean

# Install dependencies
deps:
	go mod download
	go mod tidy
