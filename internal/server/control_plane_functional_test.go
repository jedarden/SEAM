package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHeaderStrippingMiddlewareFunctional verifies that forged X-SEAM-*
// headers do not survive Stage 2 for non-reserved paths
func TestHeaderStrippingMiddlewareFunctional(t *testing.T) {
	// Create a minimal test server that doesn't require spec files
	// We'll create a mock handler that just processes headers
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check what X-SEAM-* headers are present
		// NOTE: Go canonicalizes header names (e.g., "X-SEAM-*" -> "X-Seam-*")
		var seamHeaders []string
		for headerName := range r.Header {
			if strings.HasPrefix(headerName, "X-Seam-") {
				seamHeaders = append(seamHeaders, headerName)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if len(seamHeaders) > 0 {
			w.Write([]byte(`{"seam_headers": ["` + strings.Join(seamHeaders, `", "`) + `"]}`))
		} else {
			w.Write([]byte(`{"seam_headers": []}`))
		}
	})

	// Create a minimal server instance (without spec loader)
	s := &Server{}

	// Wrap with header stripping middleware
	wrappedHandler := s.headerStrippingMiddleware(testHandler)

	tests := []struct {
		name              string
		path              string
		forgedHeaders     map[string]string
		expectedRemaining []string // Headers that should remain after stripping (in canonical form)
	}{
		{
			name: "non-reserved path - forged headers stripped",
			path: "/api/test",
			forgedHeaders: map[string]string{
				"X-SEAM-Fake":         "fake-value",
				"X-SEAM-Injected":     "injected-value",
				"X-SEAM-Spec-Version": "v1.0.0", // This should be allowed
			},
			expectedRemaining: []string{"X-Seam-Spec-Version"},
		},
		{
			name: "non-reserved path - only allowed headers remain",
			path: "/api/other",
			forgedHeaders: map[string]string{
				"X-SEAM-API-Version": "2023-01-01", // This should be allowed
				"X-SEAM-Internal":    "internal-value",
			},
			expectedRemaining: []string{"X-Seam-Api-Version"},
		},
		{
			name:              "non-reserved path - no X-SEAM headers at all",
			path:              "/api/clean",
			forgedHeaders:     map[string]string{},
			expectedRemaining: []string{},
		},
		{
			name: "both allowed headers pass through",
			path: "/api/both",
			forgedHeaders: map[string]string{
				"X-SEAM-Spec-Version": "v1.0.0",
				"X-SEAM-API-Version":  "2023-01-01",
				"X-SEAM-Evil":         "evil-value",
			},
			expectedRemaining: []string{"X-Seam-Spec-Version", "X-Seam-Api-Version"},
		},
		{
			name: "all forged headers stripped",
			path: "/api/stripped",
			forgedHeaders: map[string]string{
				"X-SEAM-Fake1": "value1",
				"X-SEAM-Fake2": "value2",
				"X-SEAM-Fake3": "value3",
			},
			expectedRemaining: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create request with forged headers
			req := httptest.NewRequest("GET", tt.path, nil)
			for headerName, headerValue := range tt.forgedHeaders {
				req.Header.Set(headerName, headerValue)
			}

			// Record response
			rec := httptest.NewRecorder()

			// Serve request
			wrappedHandler.ServeHTTP(rec, req)

			// Check response
			resp := rec.Result()
			body, _ := io.ReadAll(resp.Body)

			// Parse which headers were present
			bodyStr := string(body)

			// Verify expected headers are present
			for _, expectedHeader := range tt.expectedRemaining {
				if !strings.Contains(bodyStr, `"`+expectedHeader+`"`) {
					t.Errorf("Expected header %s to remain after stripping, but it was not found in response: %s", expectedHeader, bodyStr)
				}
			}

			// Verify forged headers were stripped
			for headerName := range tt.forgedHeaders {
				isAllowed := false
				for _, allowed := range tt.expectedRemaining {
					if headerName == allowed {
						isAllowed = true
						break
					}
				}
				if !isAllowed && strings.Contains(bodyStr, `"`+headerName+`"`) {
					t.Errorf("Forged header %s should have been stripped, but was found in response: %s", headerName, bodyStr)
				}
			}
		})
	}
}

// TestAllowedSEAMHeadersList verifies the exact list of allowed headers
func TestAllowedSEAMHeadersList(t *testing.T) {
	// This test verifies that only the two specified headers are allowed
	// NOTE: Go canonicalizes header names (e.g., "X-SEAM-API-Version" -> "X-Seam-Api-Version")
	allowedCount := 0
	for header := range allowedSEAMHeaders {
		if header == "X-Seam-Spec-Version" || header == "X-Seam-Api-Version" {
			allowedCount++
		}
	}

	if allowedCount != 2 {
		t.Errorf("Expected exactly 2 allowed X-SEAM-* headers, found %d", allowedCount)
	}

	if !allowedSEAMHeaders["X-Seam-Spec-Version"] {
		t.Error("X-Seam-Spec-Version must be in the allowed list")
	}

	if !allowedSEAMHeaders["X-Seam-Api-Version"] {
		t.Error("X-Seam-Api-Version must be in the allowed list")
	}

	// Verify no other headers are allowed
	for header := range allowedSEAMHeaders {
		if header != "X-Seam-Spec-Version" && header != "X-Seam-Api-Version" {
			t.Errorf("Header %s should NOT be in the allowed list", header)
		}
	}
}
