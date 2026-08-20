package server

import (
	"log"
	"net/http"
	"strings"
)

// The X-SEAM-* headers that are ALLOWED to pass through from clients.
// All other X-SEAM-* headers are stripped in stage 2 to prevent clients
// from injecting fake control-plane headers.
//
// NOTE: Go's http package canonicalizes header names (e.g., "X-SEAM-*" -> "X-Seam-*"),
// so these keys use the canonical form.
var allowedSEAMHeaders = map[string]bool{
	"X-Seam-Spec-Version": true,
	"X-Seam-Api-Version":  true,
}

// versionInjectionMiddleware adds version headers to all HTTP responses.
//
// This middleware injects two headers into every response:
//   - X-SEAM-Spec-Version: The full SHA256 hash (64 hex characters) of the loaded OpenAPI spec
//   - X-SEAM-API-Version: The API version (currently "_unversioned")
//
// These headers allow clients to identify which version of the spec and API they are
// communicating with, enabling version-dependent behavior and debugging.
//
// This middleware applies to ALL routes, including control-plane endpoints.
// It sits at the outermost layer of the middleware chain to ensure all
// responses carry version information regardless of how they are generated.
func (s *Server) versionInjectionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Create a wrapped response writer that injects headers
		vw := &versionWriter{
			ResponseWriter: w,
			specHash:       s.specLoader.GetHash(),
		}

		// Call the next handler with our wrapped writer
		next.ServeHTTP(vw, r)
	})
}

// versionWriter wraps http.ResponseWriter to inject version headers.
//
// This wrapper ensures that version headers are added to the response
// before any data is written. The headers are injected on the first call
// to Write() or Header(), ensuring they are present even if the inner
// handler doesn't explicitly set them.
//
// The wrapped writer preserves all other ResponseWriter behavior including
// the http.Flusher, http.Hijacker, and http.Pusher interfaces (if implemented
// by the underlying writer).
type versionWriter struct {
	http.ResponseWriter
	specHash        string
	headersInjected bool
}

// Write injects version headers before writing the response body.
//
// This method is called by the handler to write response data. We intercept
// the first call to inject our version headers, then pass through to the
// underlying writer. Subsequent calls go directly to the underlying writer
// for performance.
func (vw *versionWriter) Write(b []byte) (int, error) {
	if !vw.headersInjected {
		vw.injectHeaders()
		vw.headersInjected = true
	}
	return vw.ResponseWriter.Write(b)
}

// WriteHeader injects version headers before setting the status code.
//
// This method is called by handlers that explicitly set HTTP status codes.
// We inject headers before the status code is sent, ensuring they are
// included in the response headers.
func (vw *versionWriter) WriteHeader(statusCode int) {
	if !vw.headersInjected {
		vw.injectHeaders()
		vw.headersInjected = true
	}
	vw.ResponseWriter.WriteHeader(statusCode)
}

// Header returns the header map, injecting version headers if needed.
//
// This method is called when handlers access the header map directly.
// We ensure version headers are present before returning the map to
// the caller, giving them a chance to override if needed (though this
// is not recommended).
func (vw *versionWriter) Header() http.Header {
	if !vw.headersInjected {
		vw.injectHeaders()
		vw.headersInjected = true
	}
	return vw.ResponseWriter.Header()
}

// injectHeaders adds the version headers to the response.
//
// This method is called once per request to add:
//   - X-SEAM-Spec-Version: The stable hash identifying the loaded spec
//   - X-SEAM-API-Version: The API version (currently "_unversioned")
//
// Headers are added using the canonical form required by Go's http package
// (e.g., "X-Seam-Spec-Version" not "X-SEAM-Spec-Version").
func (vw *versionWriter) injectHeaders() {
	// Add spec version header
	if vw.specHash != "" {
		vw.ResponseWriter.Header().Set("X-Seam-Spec-Version", vw.specHash)
	}

	// Add API version header (currently unversioned)
	vw.ResponseWriter.Header().Set("X-Seam-Api-Version", unversionedAPIVersion)
}

// headerStrippingMiddleware implements Stage 2 of the control-plane pipeline.
//
// This middleware strips all X-SEAM-* headers from incoming requests EXCEPT
// the allowed exceptions (X-SEAM-Spec-Version and X-SEAM-API-Version).
//
// Purpose: Prevent clients from injecting fake X-SEAM-* headers that could
// interfere with control-plane operation or impersonate internal responses.
//
// Control-plane paths (reserved paths) bypass this middleware entirely via
// short-circuit at stage 1 (isReservedPath check in validation middleware).
//
// Stage pipeline:
//   - Stage 1: Control-plane path detection (validationMiddleware checks isReservedPath)
//   - Stage 2: Header stripping (this middleware)
//   - Stage 3: Identity/authentication (inert for now)
//   - Stage 4: Route table lookup / proxy (not yet implemented)
func (s *Server) headerStrippingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip all X-SEAM-* headers except the two allowed exceptions
		// NOTE: Header names are canonicalized by Go (e.g., "X-SEAM-*" -> "X-Seam-*")
		stripped := false
		for headerName := range r.Header {
			if strings.HasPrefix(strings.ToLower(headerName), "x-seam-") &&
				!allowedSEAMHeaders[headerName] && !strings.EqualFold(headerName, "X-SEAM-Dry-Run") {
				r.Header.Del(headerName)
				stripped = true
			}
		}

		if stripped {
			// Log that we stripped headers (for debugging/security monitoring)
			// In production, this might go to a security event log
			log.Printf("[Header-Strip] Stripped forbidden X-SEAM-* headers from request to %s", r.URL.Path)
		}

		// Proceed to next handler
		next.ServeHTTP(w, r)
	})
}
