package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScopeVersionMiddleware(t *testing.T) {
	server := &Server{
		scopeVersionCache: NewScopeVersionCache(),
	}

	tests := []struct {
		name              string
		identity          *Identity
		expectedHeader    string
		checkHeaderValue  bool
	}{
		{
			name: "resolved identity with scopes",
			identity: &Identity{
				NodeKey:     "test-node",
				NodeName:    "test.example.com",
				Resolved:    true,
				Capabilities: []string{"seam:read", "seam:write"},
			},
			expectedHeader:   "X-SEAM-Scope-Version",
			checkHeaderValue: true,
		},
		{
			name: "resolved identity with no scopes",
			identity: &Identity{
				NodeKey:     "empty-node",
				NodeName:    "empty.example.com",
				Resolved:    true,
				Capabilities: []string{},
			},
			expectedHeader:   "X-SEAM-Scope-Version",
			checkHeaderValue: true,
		},
		{
			name:              "nil identity",
			identity:          nil,
			expectedHeader:    "X-SEAM-Scope-Version",
			checkHeaderValue:  true,
		},
		{
			name: "unresolved identity",
			identity: &Identity{
				NodeKey:     "anonymous",
				NodeName:    "unknown",
				Resolved:    false,
				Capabilities: []string{},
			},
			expectedHeader:   "X-SEAM-Scope-Version",
			checkHeaderValue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a simple next handler that writes OK
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("OK"))
			})

			// Wrap with middleware
			handler := server.scopeVersionMiddleware(next)

			// Create request with identity in context
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.identity != nil {
				req = req.WithContext(contextWithIdentity(req.Context(), tt.identity))
			}

			// Create response recorder
			w := httptest.NewRecorder()

			// Call handler
			handler.ServeHTTP(w, req)

			// Check header is present
			headerValue := w.Header().Get(tt.expectedHeader)
			if headerValue == "" {
				t.Errorf("Expected %s header to be set", tt.expectedHeader)
			}

			// If checking value, verify it's a valid hash (SHA-256 = 64 hex chars)
			if tt.checkHeaderValue && headerValue != "" {
				if len(headerValue) != 64 {
					t.Errorf("Expected SHA-256 hash (64 chars), got %d chars", len(headerValue))
				}
				// Verify it's all hex
				for _, c := range headerValue {
					if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
						t.Errorf("Header value is not valid hex: %s", headerValue)
						break
					}
				}
			}
		})
	}
}

func TestScopeVersionResponseWriter(t *testing.T) {
	tests := []struct {
		name           string
		handlerFunc    func(w *scopeVersionResponseWriter)
		expectedStatus int
		checkHeader    bool
	}{
		{
			name: "explicit WriteHeader",
			handlerFunc: func(w *scopeVersionResponseWriter) {
				w.WriteHeader(http.StatusOK)
			},
			expectedStatus: http.StatusOK,
			checkHeader:    true,
		},
		{
			name: "implicit WriteHeader via Write",
			handlerFunc: func(w *scopeVersionResponseWriter) {
				w.Write([]byte("test"))
			},
			expectedStatus: http.StatusOK,
			checkHeader:    true,
		},
		{
			name: "explicit status code",
			handlerFunc: func(w *scopeVersionResponseWriter) {
				w.WriteHeader(http.StatusNotFound)
			},
			expectedStatus: http.StatusNotFound,
			checkHeader:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scopeVersion := "test-scope-version-123"

			// Create response recorder
			recorder := httptest.NewRecorder()

			// Create scope version response writer
			wrapped := &scopeVersionResponseWriter{
				ResponseWriter: recorder,
				scopeVersion:   scopeVersion,
				headerWritten:  false,
			}

			// Call handler function
			tt.handlerFunc(wrapped)

			// Check status code
			if recorder.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, recorder.Code)
			}

			// Check header was set
			if tt.checkHeader {
				headerValue := recorder.Header().Get("X-SEAM-Scope-Version")
				if headerValue != scopeVersion {
					t.Errorf("Expected scope version %s, got %s", scopeVersion, headerValue)
				}
			}
		})
	}
}

func TestScopeVersionMiddlewareIdempotence(t *testing.T) {
	server := &Server{
		scopeVersionCache: NewScopeVersionCache(),
	}

	identity := &Identity{
		NodeKey:     "test-node",
		NodeName:    "test.example.com",
		Resolved:    true,
		Capabilities: []string{"seam:read"},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	handler := server.scopeVersionMiddleware(next)

	// Make multiple requests with the same identity
	var hashes []string
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		req = req.WithContext(contextWithIdentity(req.Context(), identity))

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		hash := w.Header().Get("X-SEAM-Scope-Version")
		hashes = append(hashes, hash)
	}

	// All hashes should be the same (same scope set)
	if hashes[0] != hashes[1] || hashes[1] != hashes[2] {
		t.Errorf("Scope version hashes should be idempotent:\n got %v", hashes)
	}
}

func TestScopeVersionMiddlewareDifferentScopes(t *testing.T) {
	server := &Server{
		scopeVersionCache: NewScopeVersionCache(),
	}

	identity1 := &Identity{
		NodeKey:     "node-1",
		NodeName:    "node1.example.com",
		Resolved:    true,
		Capabilities: []string{"seam:read"},
	}

	identity2 := &Identity{
		NodeKey:     "node-2",
		NodeName:    "node2.example.com",
		Resolved:    true,
		Capabilities: []string{"seam:write"},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	handler := server.scopeVersionMiddleware(next)

	// Make request with first identity
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1 = req1.WithContext(contextWithIdentity(req1.Context(), identity1))
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	hash1 := w1.Header().Get("X-SEAM-Scope-Version")

	// Make request with second identity
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2 = req2.WithContext(contextWithIdentity(req2.Context(), identity2))
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	hash2 := w2.Header().Get("X-SEAM-Scope-Version")

	// Hashes should be different (different scope sets)
	if hash1 == hash2 {
		t.Errorf("Different scope sets should produce different hashes:\n identity1: %s\n identity2: %s", hash1, hash2)
	}
}

func TestScopeVersionResponseWriterUnwrap(t *testing.T) {
	recorder := httptest.NewRecorder()
	wrapped := &scopeVersionResponseWriter{
		ResponseWriter: recorder,
		scopeVersion:   "test-version",
		headerWritten:  false,
	}

	// Test Unwrap returns the underlying ResponseWriter
	unwrapped := wrapped.Unwrap()
	if unwrapped != recorder {
		t.Error("Unwrap should return the underlying ResponseWriter")
	}
}

func TestScopeVersionResponseWriterFlush(t *testing.T) {
	recorder := httptest.NewRecorder()
	wrapped := &scopeVersionResponseWriter{
		ResponseWriter: recorder,
		scopeVersion:   "test-version",
		headerWritten:  false,
	}

	// Create a flushable recorder (not all ResponseWriters support Flush)
	flushableRecorder := &flushableRecorder{
		ResponseRecorder: recorder,
	}
	wrapped.ResponseWriter = flushableRecorder

	// Call Flush
	wrapped.Flush()

	// Verify header was written
	headerValue := recorder.Header().Get("X-SEAM-Scope-Version")
	if headerValue != "test-version" {
		t.Errorf("Expected header to be set after Flush, got %s", headerValue)
	}

	// Verify flush was called
	if !flushableRecorder.flushed {
		t.Error("Expected Flush to be called on underlying ResponseWriter")
	}
}

// flushableRecorder is a test helper that implements http.Flusher
type flushableRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushableRecorder) Flush() {
	f.flushed = true
}
