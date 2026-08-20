package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newVersionTestServer(t *testing.T) *Server {
	t.Helper()

	return New(&Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	})
}

func versionedTestHandler(s *Server, next http.Handler) http.Handler {
	return s.versionInjectionMiddleware(s.versionMiddleware(next))
}

// TestInvalidVersionReturns400WithHeaders verifies the listener-wide behavior
// for every registered caller and operator endpoint, including catch-all and
// not-found requests.
func TestInvalidVersionReturns400WithHeaders(t *testing.T) {
	s := newVersionTestServer(t)
	callerHandler := versionedTestHandler(s, s.callerMux)
	operatorHandler := versionedTestHandler(s, s.operatorMux)

	tests := []struct {
		name    string
		handler http.Handler
		method  string
		url     string
	}{
		{name: "caller health", handler: callerHandler, method: http.MethodGet, url: "/_seam/health?version=v1"},
		{name: "caller healthz", handler: callerHandler, method: http.MethodGet, url: "/_seam/healthz?version=v1"},
		{name: "caller readyz", handler: callerHandler, method: http.MethodGet, url: "/_seam/readyz?version=v1"},
		{name: "OpenAPI document", handler: callerHandler, method: http.MethodGet, url: "/openapi.json?version=v1"},
		{name: "documentation", handler: callerHandler, method: http.MethodGet, url: "/docs?version=v1"},
		{name: "route documentation", handler: callerHandler, method: http.MethodGet, url: "/docs/route?path=/openapi.json&method=GET&version=v1"},
		{name: "caller catch-all", handler: callerHandler, method: http.MethodGet, url: "/not-configured?version=v1"},
		{name: "operator metrics", handler: operatorHandler, method: http.MethodGet, url: "/_seam/metrics?version=v1"},
		{name: "operator config status", handler: operatorHandler, method: http.MethodGet, url: "/config/status?version=v1"},
		{name: "operator capture save", handler: operatorHandler, method: http.MethodPost, url: "/_seam/capture/save?version=v1"},
		{name: "operator capture status", handler: operatorHandler, method: http.MethodGet, url: "/_seam/capture/status?version=v1"},
		{name: "operator cache status", handler: operatorHandler, method: http.MethodGet, url: "/_seam/cache/status?version=v1"},
		{name: "operator cache cleanup", handler: operatorHandler, method: http.MethodPost, url: "/_seam/cache/cleanup?version=v1"},
		{name: "operator credential health", handler: operatorHandler, method: http.MethodGet, url: "/health/credentials?version=v1"},
		{name: "operator not found", handler: operatorHandler, method: http.MethodGet, url: "/not-registered?version=v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.url, nil)
			w := httptest.NewRecorder()

			tt.handler.ServeHTTP(w, req)

			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
			}
			if specVersion := resp.Header.Get("X-SEAM-Spec-Version"); specVersion == "" {
				t.Error("X-SEAM-Spec-Version header is missing")
			}
			if apiVersion := resp.Header.Get("X-SEAM-API-Version"); apiVersion != unversionedAPIVersion {
				t.Errorf("X-SEAM-API-Version = %q, want %q", apiVersion, unversionedAPIVersion)
			}

			var body invalidVersionParameterResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error != ErrCodeInvalidVersion {
				t.Errorf("error = %q, want %q", body.Error, ErrCodeInvalidVersion)
			}
			if body.Message != "Invalid version parameter. Expected: _unversioned" {
				t.Errorf("message = %q, want expected alphabet named explicitly", body.Message)
			}
			if body.ExpectedVersion != unversionedAPIVersion {
				t.Errorf("expected_version = %q, want %q", body.ExpectedVersion, unversionedAPIVersion)
			}
			if body.ActualVersion != "v1" {
				t.Errorf("actual_version = %q, want v1", body.ActualVersion)
			}
		})
	}
}

func TestVersionValidationIsWiredToBothListeners(t *testing.T) {
	s := New(&Config{
		CallerPort:   0,
		OperatorPort: 0,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("shut down server: %v", err)
		}
	})

	tests := []struct {
		name    string
		handler http.Handler
		url     string
	}{
		{name: "caller", handler: s.callerServer.Handler, url: "/_seam/health?version=v1"},
		{name: "operator", handler: s.operatorServer.Handler, url: "/config/status?version=v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tt.handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.url, nil))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
			}

			var body invalidVersionParameterResponse
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error != ErrCodeInvalidVersion {
				t.Errorf("error = %q, want %q", body.Error, ErrCodeInvalidVersion)
			}
		})
	}
}

func TestVersionMiddlewareAcceptsOnlyUnversioned(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantStatus int
		wantActual string
	}{
		{name: "omitted", url: "/resource", wantStatus: http.StatusNoContent},
		{name: "unversioned", url: "/resource?version=_unversioned", wantStatus: http.StatusNoContent},
		{name: "encoded unversioned", url: "/resource?version=%5Funversioned", wantStatus: http.StatusNoContent},
		{name: "repeated unversioned", url: "/resource?version=_unversioned&version=_unversioned", wantStatus: http.StatusNoContent},
		{name: "other version", url: "/resource?version=v2", wantStatus: http.StatusBadRequest, wantActual: "v2"},
		{name: "empty", url: "/resource?version=", wantStatus: http.StatusBadRequest},
		{name: "whitespace", url: "/resource?version=%20", wantStatus: http.StatusBadRequest, wantActual: " "},
		{name: "invalid duplicate after valid", url: "/resource?version=_unversioned&version=beta", wantStatus: http.StatusBadRequest, wantActual: "beta"},
		{name: "invalid duplicate before valid", url: "/resource?version=beta&version=_unversioned", wantStatus: http.StatusBadRequest, wantActual: "beta"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusNoContent)
			})
			handler := (&Server{}).versionMiddleware(next)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.url, nil))

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if nextCalled != (tt.wantStatus == http.StatusNoContent) {
				t.Errorf("nextCalled = %t for status %d", nextCalled, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusBadRequest {
				var body invalidVersionParameterResponse
				if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if body.ActualVersion != tt.wantActual {
					t.Errorf("actual_version = %q, want %q", body.ActualVersion, tt.wantActual)
				}
			}
		})
	}
}

func TestValidVersionAcceptedOnAllEndpoints(t *testing.T) {
	s := newVersionTestServer(t)
	callerHandler := versionedTestHandler(s, s.callerMux)
	operatorHandler := versionedTestHandler(s, s.operatorMux)

	tests := []struct {
		name       string
		handler    http.Handler
		method     string
		url        string
		wantStatus int
	}{
		{name: "caller health", handler: callerHandler, method: http.MethodGet, url: "/_seam/health?version=_unversioned", wantStatus: http.StatusOK},
		{name: "caller healthz", handler: callerHandler, method: http.MethodGet, url: "/_seam/healthz?version=_unversioned", wantStatus: http.StatusOK},
		{name: "caller readyz", handler: callerHandler, method: http.MethodGet, url: "/_seam/readyz?version=_unversioned", wantStatus: http.StatusServiceUnavailable},
		{name: "OpenAPI document", handler: callerHandler, method: http.MethodGet, url: "/openapi.json?version=_unversioned", wantStatus: http.StatusOK},
		{name: "documentation", handler: callerHandler, method: http.MethodGet, url: "/docs?version=_unversioned", wantStatus: http.StatusOK},
		{name: "route documentation", handler: callerHandler, method: http.MethodGet, url: "/docs/route?path=/openapi.json&method=GET&version=_unversioned", wantStatus: http.StatusOK},
		{name: "caller catch-all", handler: callerHandler, method: http.MethodGet, url: "/not-configured?version=_unversioned", wantStatus: http.StatusNotFound},
		{name: "operator metrics", handler: operatorHandler, method: http.MethodGet, url: "/_seam/metrics?version=_unversioned", wantStatus: http.StatusOK},
		{name: "operator config status", handler: operatorHandler, method: http.MethodGet, url: "/config/status?version=_unversioned", wantStatus: http.StatusOK},
		{name: "operator capture save", handler: operatorHandler, method: http.MethodPost, url: "/_seam/capture/save?version=_unversioned", wantStatus: http.StatusServiceUnavailable},
		{name: "operator capture status", handler: operatorHandler, method: http.MethodGet, url: "/_seam/capture/status?version=_unversioned", wantStatus: http.StatusOK},
		{name: "operator cache status", handler: operatorHandler, method: http.MethodGet, url: "/_seam/cache/status?version=_unversioned", wantStatus: http.StatusOK},
		{name: "operator cache cleanup", handler: operatorHandler, method: http.MethodPost, url: "/_seam/cache/cleanup?version=_unversioned", wantStatus: http.StatusOK},
		{name: "operator credential health", handler: operatorHandler, method: http.MethodGet, url: "/health/credentials?version=_unversioned", wantStatus: http.StatusOK},
		{name: "operator not found", handler: operatorHandler, method: http.MethodGet, url: "/not-registered?version=_unversioned", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tt.handler.ServeHTTP(w, httptest.NewRequest(tt.method, tt.url, nil))

			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if apiVersion := resp.Header.Get("X-SEAM-API-Version"); apiVersion != unversionedAPIVersion {
				t.Errorf("X-SEAM-API-Version = %q, want %q", apiVersion, unversionedAPIVersion)
			}
		})
	}
}
