package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/spec"
)

// TestReadyzDependencyStateMatrix is the executable form of the /_seam/readyz
// contract (docs/notes/readyz-contract.md): one row per dependency state,
// each pinning the exact HTTP status, the application/json content type, and
// the exact flat boolean body. The rows cover every dependency failing alone
// (the independence property), the satisfiable states of the credential-probe
// dependency, and combined failures (the aggregate ready flag is a plain AND
// over the dependency set, so any unmet key yields the same 503).
func TestReadyzDependencyStateMatrix(t *testing.T) {
	now := time.Now()
	// One probe result past its freshness window (2x cadence + grace) and one
	// inside it, shared by the rows that need them.
	stale := now.Add(-2*defaultCredentialProbeInterval - probeFreshnessGrace - time.Minute)

	tests := []struct {
		name string
		// setup installs exactly the dependency state under test on a server
		// that starts with every dependency satisfied.
		setup     func(t *testing.T, server *Server)
		wantCode  int
		wantFalse []string
	}{
		{
			name:     "all_dependencies_satisfied",
			setup:    func(t *testing.T, server *Server) {},
			wantCode: http.StatusOK,
		},
		{
			name: "route_table_empty",
			setup: func(t *testing.T, server *Server) {
				if err := server.routeTableHolder.Swap(NewRouteTable(nil)); err != nil {
					t.Fatalf("swap empty route table: %v", err)
				}
			},
			wantCode:  http.StatusServiceUnavailable,
			wantFalse: []string{readyzCheckRouteTable},
		},
		{
			name: "allowlist_fail_closed",
			setup: func(t *testing.T, server *Server) {
				// No allowlist loaded: enforcement fails closed (EC-14).
				failsClosed, err := spec.NewAllowlistEnforcer(spec.DefaultVaultBaseDir, "")
				if err != nil {
					t.Fatalf("create fail-closed enforcer: %v", err)
				}
				server.allowlistEnforcer = failsClosed
			},
			wantCode:  http.StatusServiceUnavailable,
			wantFalse: []string{readyzCheckAllowlist},
		},
		{
			name: "openbao_login_pending",
			setup: func(t *testing.T, server *Server) {
				server.setOpenBaoReady(false)
			},
			wantCode:  http.StatusServiceUnavailable,
			wantFalse: []string{readyzCheckOpenBao},
		},
		{
			name: "credential_probe_never_verified",
			setup: func(t *testing.T, server *Server) {
				registry := NewCredentialProbeRegistry()
				registry.Set(CredentialProbeResult{
					FragmentID: "kubernetes-api",
					InstanceID: "prod",
					Status:     CredentialUnknown,
					Interval:   defaultCredentialProbeInterval,
				})
				server.setCredentialProbes(registry)
			},
			wantCode:  http.StatusServiceUnavailable,
			wantFalse: []string{readyzCheckCredentialProbe},
		},
		{
			name: "credential_probe_stale_past_window",
			setup: func(t *testing.T, server *Server) {
				registry := NewCredentialProbeRegistry()
				registry.Set(CredentialProbeResult{
					FragmentID:   "kubernetes-api",
					InstanceID:   "prod",
					Status:       CredentialHealthy,
					Interval:     defaultCredentialProbeInterval,
					LastVerified: &stale,
				})
				server.setCredentialProbes(registry)
			},
			wantCode:  http.StatusServiceUnavailable,
			wantFalse: []string{readyzCheckCredentialProbe},
		},
		{
			name: "credential_probe_cold_start_registry",
			setup: func(t *testing.T, server *Server) {
				// An attached registry with no results yet is cold start, not
				// failure: the probe loop has simply not reported.
				server.setCredentialProbes(NewCredentialProbeRegistry())
			},
			wantCode: http.StatusOK,
		},
		{
			name: "route_table_and_openbao_fail_together",
			setup: func(t *testing.T, server *Server) {
				if err := server.routeTableHolder.Swap(NewRouteTable(nil)); err != nil {
					t.Fatalf("swap empty route table: %v", err)
				}
				server.setOpenBaoReady(false)
			},
			wantCode:  http.StatusServiceUnavailable,
			wantFalse: []string{readyzCheckRouteTable, readyzCheckOpenBao},
		},
		{
			name: "route_table_allowlist_and_credential_probe_fail_together",
			setup: func(t *testing.T, server *Server) {
				if err := server.routeTableHolder.Swap(NewRouteTable(nil)); err != nil {
					t.Fatalf("swap empty route table: %v", err)
				}
				failsClosed, err := spec.NewAllowlistEnforcer(spec.DefaultVaultBaseDir, "")
				if err != nil {
					t.Fatalf("create fail-closed enforcer: %v", err)
				}
				server.allowlistEnforcer = failsClosed
				registry := NewCredentialProbeRegistry()
				registry.Set(CredentialProbeResult{
					FragmentID:   "kubernetes-api",
					InstanceID:   "prod",
					Status:       CredentialHealthy,
					Interval:     defaultCredentialProbeInterval,
					LastVerified: &stale,
				})
				server.setCredentialProbes(registry)
			},
			wantCode:  http.StatusServiceUnavailable,
			wantFalse: []string{readyzCheckRouteTable, readyzCheckAllowlist, readyzCheckCredentialProbe},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newReadyzTestServer(t)
			installPopulatedRouteTable(t, server)
			tt.setup(t, server)

			request := httptest.NewRequest(http.MethodGet, "/_seam/readyz", nil)
			response := httptest.NewRecorder()
			server.callerMux.ServeHTTP(response, request)

			if response.Code != tt.wantCode {
				t.Fatalf("readyz status = %d, want %d (body: %s)",
					response.Code, tt.wantCode, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}

			// The decode doubles as the flatness assertion: any nested or
			// non-boolean value fails here, pinning the map[string]bool
			// response shape.
			var body map[string]bool
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode readyz body %q: %v", response.Body.String(), err)
			}

			// The key enumeration is closed: exactly the four dependency
			// names plus the aggregate, nothing more.
			allKeys := []string{
				readyzCheckRouteTable,
				readyzCheckAllowlist,
				readyzCheckOpenBao,
				readyzCheckCredentialProbe,
				"ready",
			}
			if len(body) != len(allKeys) {
				t.Errorf("readyz body carries %d keys (%v), want exactly the closed set %v",
					len(body), body, allKeys)
			}
			for _, key := range allKeys {
				if _, ok := body[key]; !ok {
					t.Errorf("readyz body is missing key %q, got %v", key, body)
				}
			}

			// The row's failing set reads false; every other dependency reads
			// true — dependencies fail independently and never mask each
			// other.
			failing := make(map[string]bool, len(tt.wantFalse))
			for _, name := range tt.wantFalse {
				failing[name] = true
				if body[name] {
					t.Errorf("%s = true, want false in state %q", name, tt.name)
				}
			}
			for _, name := range allKeys[:len(allKeys)-1] {
				if failing[name] {
					continue
				}
				if !body[name] {
					t.Errorf("%s = false while the state only fails %v; dependencies must fail independently",
						name, tt.wantFalse)
				}
			}

			// ready is the plain AND over the dependency set.
			wantReady := len(tt.wantFalse) == 0
			if body["ready"] != wantReady {
				t.Errorf("ready = %v, want %v (failing: %v)", body["ready"], wantReady, tt.wantFalse)
			}
		})
	}
}
