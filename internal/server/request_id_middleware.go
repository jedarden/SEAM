package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"
)

// Context key for request ID
const requestIDKey contextKey = iota

// requestIDMiddleware generates a unique request ID for each incoming request
// and stores it in the request context for use in error responses and logging
//
//nolint:unused // Intentional scaffolding: not yet wired into setupRoutes.
func (s *Server) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Generate or extract request ID
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = uuid.New().String()
		}

		// Store in context
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		r = r.WithContext(ctx)

		// Add response header
		w.Header().Set("X-Request-ID", requestID)

		// Call next handler
		next.ServeHTTP(w, r)
	})
}

// GetRequestID extracts the request ID from the request context
func GetRequestID(r *http.Request) string {
	if r == nil || r.Context() == nil {
		return ""
	}
	requestID, ok := r.Context().Value(requestIDKey).(string)
	if !ok {
		return ""
	}
	return requestID
}

// MustGetRequestID extracts the request ID from the request context
// It returns a formatted error string if the request ID is not found
func MustGetRequestID(r *http.Request) string {
	requestID := GetRequestID(r)
	if requestID == "" {
		return "req_unknown"
	}
	return fmt.Sprintf("req_%s", requestID[:8])
}
