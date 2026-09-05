# SEAM Gateway Container
#
# Build from the repository root:
#   docker build -f containers/seam/Dockerfile -t seam:local .

# Stage 1: compile a static Linux binary.
#
# Tracked to go.mod's `go` directive, not frozen at a patch release: the
# golang images set GOTOOLCHAIN=local, so an image older than the directive
# hard-fails `go mod download` with "go.mod requires go >= ..." instead of
# self-upgrading. This drifted behind at 0c3d484 and broke seam-ci's
# image-build step on every run after go.mod reached go 1.26.0.
FROM golang:1.26-bookworm AS builder

ARG REVISION

RUN apt-get update && \
    apt-get install -y --no-install-recommends git ca-certificates && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

# The server loads its OpenAPI assets from ./spec at runtime. Copy only the
# source and assets needed by this binary so unrelated workspace files never
# enter the image build context or layers.
COPY cmd ./cmd
COPY internal ./internal
COPY spec ./spec
COPY containers/seam/VERSION ./containers/seam/VERSION

RUN SEAM_VERSION="$(tr -d '\r\n' < containers/seam/VERSION)" && \
	CGO_ENABLED=0 GOOS=linux go build \
	-trimpath \
	-ldflags="-s -w -X github.com/ardenone/seam/internal/buildinfo.Version=${SEAM_VERSION} -X github.com/ardenone/seam/internal/buildinfo.Revision=${REVISION}" \
	-o /out/seam ./cmd/seam

# Stage 2: no shell, package manager, or compiler in the runtime image.
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /out/seam /seam
COPY spec /spec

# Numeric nobody identity is available even in a scratch image.
USER 65534:65534

EXPOSE 8080/tcp 8081/tcp

WORKDIR /

# scratch has no shell, so use the binary's healthcheck subcommand directly.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/seam", "healthcheck"]

ENTRYPOINT ["/seam"]
CMD ["serve"]
