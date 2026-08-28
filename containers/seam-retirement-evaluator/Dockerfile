# SEAM Retirement Evaluator Container
# Phase 8.5: Retirement evaluation with GitHub PR opening

FROM golang:1.23.0-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY tools/seam-retirement-evaluator/ ./tools/seam-retirement-evaluator/

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w" \
    -o seam-retirement-evaluator \
    ./tools/seam-retirement-evaluator/

# Final stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy the binary from builder
COPY --from=builder /build/seam-retirement-evaluator /app/

# Use non-root user
RUN addgroup -g 1000 evaluator && \
    adduser -D -u 1000 -G evaluator evaluator

USER evaluator

# Health check
HEALTHCHECK --interval=60s --timeout=5s --start-period=30s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/healthz || exit 1

EXPOSE 8080

ENTRYPOINT ["/app/seam-retirement-evaluator"]
