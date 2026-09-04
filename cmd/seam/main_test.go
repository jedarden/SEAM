package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/server"
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

func TestAllowlistPathIsFixedInCluster(t *testing.T) {
	if got := resolveAllowlistFile("/tmp/developer-allowlist.yaml", true); got != server.DefaultUpstreamAllowlistFile {
		t.Fatalf("in-cluster allowlist path = %q, want fixed mount %q", got, server.DefaultUpstreamAllowlistFile)
	}
	if got := resolveAllowlistFile("/tmp/developer-allowlist.yaml", false); got != "/tmp/developer-allowlist.yaml" {
		t.Fatalf("local allowlist path = %q, want developer path", got)
	}
}

// resolveVaultBaseDir is the whole env-var half of the vault base directory
// contract: SEAM_VAULT_BASE_DIR overrides the flag, and an unset variable
// hands back whatever the flag said (normally "") so server.New keeps its
// in-code default. The prefix ValidateVaultPath enforces is asserted on the
// server side in internal/server.
func TestResolveVaultBaseDir(t *testing.T) {
	const envVar = "SEAM_VAULT_BASE_DIR"
	oldVal, hadOld := os.LookupEnv(envVar)
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv(envVar, oldVal)
		} else {
			_ = os.Unsetenv(envVar)
		}
	})

	tests := []struct {
		name     string
		envValue string
		setEnv   bool
		flag     string
		want     string
	}{
		{
			name:   "unset variable leaves the flag value",
			setEnv: false,
			flag:   "tenants/alpha",
			want:   "tenants/alpha",
		},
		{
			name:   "unset variable and unset flag leaves the server default",
			setEnv: false,
			flag:   "",
			want:   "",
		},
		{
			name:     "set variable overrides the flag",
			setEnv:   true,
			envValue: "tenants/alpha",
			flag:     "from-flag",
			want:     "tenants/alpha",
		},
		{
			name:     "set variable supplies what the flag omits",
			setEnv:   true,
			envValue: "tenants/alpha",
			flag:     "",
			want:     "tenants/alpha",
		},
		{
			name:     "blank variable is treated as unset",
			setEnv:   true,
			envValue: "   ",
			flag:     "from-flag",
			want:     "from-flag",
		},
		{
			name:     "surrounding whitespace is trimmed",
			setEnv:   true,
			envValue: "  tenants/alpha  ",
			flag:     "",
			want:     "tenants/alpha",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				_ = os.Setenv(envVar, tc.envValue)
			} else {
				_ = os.Unsetenv(envVar)
			}

			if got := resolveVaultBaseDir(tc.flag); got != tc.want {
				t.Errorf("resolveVaultBaseDir(%q) with %s=%q = %q, want %q",
					tc.flag, envVar, tc.envValue, got, tc.want)
			}
		})
	}
}
