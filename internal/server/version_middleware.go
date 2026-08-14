package server

import (
	"net/http"
)

// versionMiddleware creates middleware for version-related functionality
//
// This middleware is a skeleton for future version header logic.
// It currently passes all requests through without modification.
// Implementation will be added in subsequent beads.
func (s *Server) versionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// TODO: Add version header logic in future bead
		// For now, just pass through to next handler
		next.ServeHTTP(w, r)
	})
}
