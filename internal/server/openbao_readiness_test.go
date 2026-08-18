package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadyzBlocksUntilOpenBaoLogin(t *testing.T) {
	server := New(&Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	})
	// Isolate the OpenBao assertion from the separate allowlist readiness gate.
	server.allowlistEnforcer = nil

	server.setOpenBaoReady(false)
	request := httptest.NewRequest(http.MethodGet, "/_seam/readyz", nil)
	response := httptest.NewRecorder()
	server.callerMux.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status while OpenBao login is pending = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}

	server.setOpenBaoReady(true)
	response = httptest.NewRecorder()
	server.callerMux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("readyz status after OpenBao login = %d, want %d", response.Code, http.StatusOK)
	}
}
