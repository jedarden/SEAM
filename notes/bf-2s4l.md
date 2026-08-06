# Bead bf-2s4l: Initialize Go module and basic server scaffold

## Status: ✅ COMPLETE

This bead has been verified and all acceptance criteria are met. The implementation was completed in commit `051c080`.

## Verification Results

### 1. ✅ go.mod exists with module name and Go version
- Module: `github.com/ardenone/seam`
- Go version: `1.25.7`
- Location: `/home/coding/SEAM/go.mod`

### 2. ✅ Project structure follows Go conventions
```
cmd/seam/main.go - Main entry point with serve/lint/diff/import commands
internal/server/ - Server implementation package
internal/spec/ - OpenAPI spec loader package
```

### 3. ✅ Server starts and listens on configurable ports
- Default caller port: `8080` (configurable via `--caller-port` flag or `SEAM_CALLER_PORT` env var)
- Default operator port: `8081` (configurable via `--operator-port` flag or `SEAM_OPERATOR_PORT` env var)
- Server starts successfully with: `./seam serve` or `make run`

### 4. ✅ Health endpoint returns 200 OK
- `GET /_seam/healthz` returns `200 OK`
- `GET /_seam/readyz` returns `{"ready":true}` with `200` status
- Health check verified on `2026-08-06`

### 5. ✅ make run works
```bash
make run
# Executes: go run cmd/seam/main.go serve
```

## Implementation Details

The server implements a dual-listener architecture:
- **Caller-facing listener**: Public API endpoints (port 8080)
- **Operator-only listener**: Admin/metrics endpoints (port 8081)

### Control Plane Endpoints (Reserved Paths)
- `/_seam/healthz` - Liveness check (always 200 OK)
- `/_seam/readyz` - Readiness check
- `/_seam/metrics` - Prometheus metrics (operator port)
- `/docs` - API documentation
- `/openapi.json` - OpenAPI specification
- `/config/status` - Configuration status (operator port)

### Key Features Implemented
- Graceful shutdown with signal handling
- Configurable ports via flags and environment variables
- OpenAPI spec loading (single file or fragment mode)
- Request validation middleware
- HTTP capture/recording middleware (optional)
- Dual-port security model (caller vs operator)

## Testing Summary

All health endpoints tested and working:
- ✅ `/_seam/healthz` → 200 OK
- ✅ `/_seam/readyz` → 200 `{"ready":true}`
- ✅ `/_seam/metrics` (operator port) → 200 with Prometheus metrics

## Conclusion

The foundational Go server infrastructure is complete and operational. The bead requirements are fully satisfied.
