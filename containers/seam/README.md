# SEAM container

Build the image from the repository root so the Dockerfile can access the Go
module, source, and OpenAPI assets:

```bash
docker build -f containers/seam/Dockerfile -t seam:local .
```

The image starts `seam serve` by default. It runs as UID/GID `65534`, contains
only the static binary, CA certificates, and `/spec`, and exposes the two
listeners:

```bash
docker run --rm \
  --publish 8080:8080 \
  --publish 8081:8081 \
  seam:local
```

Verify the caller-facing liveness endpoint with:

```bash
curl --fail http://127.0.0.1:8080/_seam/healthz
```

The image health check runs `/seam healthcheck`, which probes that same
caller-facing endpoint. Override the listener ports with `SEAM_CALLER_PORT`
and `SEAM_OPERATOR_PORT` when the container is run with matching published
ports.
