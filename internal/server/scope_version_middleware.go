package server

import (
	"net/http"
)

// scopeVersionMiddleware adds the X-SEAM-Scope-Version header to all responses.
//
// Phase 7 Stage 5-6: This header is computed from the caller's effective scope set
// and provides correlation for scope changes over time.
//
// The header value is a SHA-256 hash of the sorted, normalized scope list:
//   - Empty scope set: canonical hash of empty string
//   - Non-empty: hash of each scope separated by newline
//
// This middleware runs AFTER identity resolution (Stage 3) so it has access
// to the resolved identity in the request context.
func (s *Server) scopeVersionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get resolved identity from context
		identity := identityFromContext(r.Context())

		// Get effective scopes from identity
		effectiveScopes := effectiveScopesFromIdentity(identity)

		// Compute scope version hash
		var scopeVersion string
		if s.scopeVersionCache != nil {
			scopeVersion = s.scopeVersionCache.RecordScopeVersion(identity, effectiveScopes)
		} else {
			scopeVersion = ComputeScopeVersionHash(effectiveScopes)
		}

		// Wrap response writer to add the header
		wrapped := &scopeVersionResponseWriter{
			ResponseWriter:   w,
			scopeVersion:    scopeVersion,
			headerWritten:   false,
		}

		next.ServeHTTP(wrapped, r)
	})
}

// scopeVersionResponseWriter wraps http.ResponseWriter to inject X-SEAM-Scope-Version header
type scopeVersionResponseWriter struct {
	http.ResponseWriter
	scopeVersion  string
	headerWritten bool
}

// WriteHeader intercepts the header write to inject the scope version
func (w *scopeVersionResponseWriter) WriteHeader(statusCode int) {
	if !w.headerWritten {
		w.ResponseWriter.Header().Set("X-SEAM-Scope-Version", w.scopeVersion)
		w.headerWritten = true
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

// Write intercepts the body write to ensure header is set
func (w *scopeVersionResponseWriter) Write(b []byte) (int, error) {
	if !w.headerWritten {
		w.WriteHeader(http.StatusOK) // Will trigger WriteHeader
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets net/http.ResponseController preserve streaming, flushing, and
// connection-hijacking capabilities of the underlying writer.
func (w *scopeVersionResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Flush implements http.Flusher for wrapped writers that support it
func (w *scopeVersionResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		if !w.headerWritten {
			w.WriteHeader(http.StatusOK)
		}
		flusher.Flush()
	}
}

// Hijack implements http.Hijacker for wrapped writers that support it
func (w *scopeVersionResponseWriter) Hijack() (c interface{}, rw interface{}, err error) {
	if hijacker, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}
