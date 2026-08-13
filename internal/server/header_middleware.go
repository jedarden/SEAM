package server

import (
	"log"
	"net/http"
	"strings"
)

// The two X-SEAM-* headers that are ALLOWED to pass through from clients.
// All other X-SEAM-* headers are stripped in stage 2 to prevent clients
// from injecting fake control-plane headers.
//
// NOTE: Go's http package canonicalizes header names (e.g., "X-SEAM-*" -> "X-Seam-*"),
// so these keys use the canonical form.
var allowedSEAMHeaders = map[string]bool{
	"X-Seam-Spec-Version": true,
	"X-Seam-Api-Version":  true,
}

// headerStrippingMiddleware implements Stage 2 of the control-plane pipeline.
//
// This middleware strips all X-SEAM-* headers from incoming requests EXCEPT
// the two allowed exceptions (X-SEAM-Spec-Version and X-SEAM-API-Version).
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
			if strings.HasPrefix(headerName, "X-Seam-") && !allowedSEAMHeaders[headerName] {
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
