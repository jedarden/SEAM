# SEAM Gateway Container
# Multi-stage build for minimal final image

# Stage 1: Build stage
FROM golang:1.26 AS builder

# Install dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends git ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the seam binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-w -extldflags "-static"' -o seam ./cmd/seam

# Stage 2: Runtime stage
FROM scratch

# Copy CA certificates from builder (for HTTPS connections)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the binary
COPY --from=builder /build/seam /seam

# Copy spec directory (for OpenAPI spec serving)
COPY spec /spec

# Use non-root user
USER 65534:65534

# Expose ports
EXPOSE 8080 8081

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/seam", "healthcheck"] || exit 1

# Set entrypoint
ENTRYPOINT ["/seam"]

# Default command is serve
CMD ["serve"]
