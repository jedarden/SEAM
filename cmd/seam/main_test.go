package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The container image's HEALTHCHECK depends on these semantics: exit zero only
// when the caller-facing liveness endpoint answers 200.
func TestRunHealthcheck(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr string
	}{
		{name: "serving returns no error", status: http.StatusOK},
		{name: "not ready is an error", status: http.StatusServiceUnavailable, wantErr: "got HTTP 503"},
		{name: "server error is an error", status: http.StatusInternalServerError, wantErr: "got HTTP 500"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/_seam/healthz" {
					t.Errorf("probed %q, want /_seam/healthz", r.URL.Path)
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			err := runHealthcheck(srv.URL+"/_seam/healthz", 2*time.Second)

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// A dead gateway must fail the probe rather than hang or panic.
func TestRunHealthcheckUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL + "/_seam/healthz"
	srv.Close() // nothing is listening now

	if err := runHealthcheck(url, 500*time.Millisecond); err == nil {
		t.Fatal("expected an error probing a closed listener, got nil")
	}
}
