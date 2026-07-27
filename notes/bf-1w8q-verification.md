# Capture Middleware Implementation Verification

## Requirements Check for bf-1w8q

### ✅ 1. Middleware implementation that captures full HTTP requests
**Location:** `internal/server/capture.go:147-169`
- Captures: Method, Path, Query, Headers, Body (base64-encoded)
- Function: `captureRequest(r *http.Request) CapturedRequest`

### ✅ 2. Middleware implementation that captures full HTTP responses  
**Location:** `internal/server/capture.go:172-185`
- Captures: StatusCode, Headers, Body (base64-encoded)
- Function: `captureResponse(rec *responseRecorder) CapturedResponse`

### ✅ 3. Captured data saved to corpus/ directory in JSON format
**Location:** `internal/server/capture.go:204-242`
- Saves to: `corpus.json` in configured corpus directory
- Function: `Save() error`
- Format: Properly structured JSON with schema version

### ✅ 4. Includes timestamps and metadata
**Location:** `internal/server/capture.go:60-68` (CorpusFile struct)
- Timestamps: RFC3339 format in `CapturedAt` field
- Per-entry timestamps: RFC3339Nano in `Timestamp` field
- Metadata: Service, Incumbent, Description, Schema version

### ✅ 5. Code follows existing patterns from seam-capture tool
**Evidence:**
- Uses same corpus schema: `seam-diff-corpus/v1`
- Same entry structure: ID, Description, Request, Response
- Same header canonicalization approach
- Same response recorder pattern
- Same base64 encoding for bodies

## Implementation Features

### Key Components
1. **CaptureMiddleware struct** - Main middleware controller
2. **CorpusEntry struct** - Single request/response pair
3. **CapturedRequest/Response structs** - HTTP data structures
4. **responseRecorder** - Custom ResponseWriter for capturing
5. **Integration with SEAM server** - Wrapped in server.go:594-598

### Advanced Features
- Auto-save every 10 entries
- Load existing corpus on startup
- Thread-safe with mutex locking
- Skips reserved paths automatically
- Graceful shutdown with corpus save
- Manual save endpoint: `POST /_seam/capture/save`

## Usage Example
```bash
# Start SEAM with capture enabled
./seam serve --capture-enabled --corpus-dir=corpus/argocd-proxy

# Make requests to capture
curl http://localhost:8080/api/v1/applications

# Check capture status
curl http://localhost:8081/_seam/capture/status

# Manual save
curl -X POST http://localhost:8081/_seam/capture/save
```

## Result
✅ All acceptance criteria met
✅ Implementation complete and functional
✅ Existing corpus file shows working captures
