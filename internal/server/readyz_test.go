package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/spec"
)

// newReadyzTestServer builds a server whose readiness inputs are fully
// controlled by the test: the route table starts empty and is swapped
// explicitly, the allowlist check is disabled (satisfied), OpenBao is logged
// in, and no probe registry is attached. Each test then installs exactly the
// dependency state it exercises.
func newReadyzTestServer(t *testing.T) *Server {
	t.Helper()
	server := New(&Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	})
	server.allowlistEnforcer = nil
	server.setOpenBaoReady(true)
	if err := server.routeTableHolder.Swap(NewRouteTable(nil)); err != nil {
		t.Fatalf("swap empty route table: %v", err)
	}
	return server
}

// readyzProbe issues one GET /_seam/readyz against the caller mux and decodes
// the body as a flat boolean map. The decode doubles as a shape assertion:
// any nested or non-boolean value fails the test, pinning the historical
// map[string]bool response contract.
func readyzProbe(t *testing.T, server *Server) (int, map[string]bool) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/_seam/readyz", nil)
	response := httptest.NewRecorder()
	server.callerMux.ServeHTTP(response, request)
	var body map[string]bool
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode readyz body %q: %v", response.Body.String(), err)
	}
	return response.Code, body
}

// populatedRouteTable returns a table carrying one servable route.
func populatedRouteTable() *RouteTable {
	table := NewRouteTable(nil)
	table.AddRoute(RouteEntry{
		PathTemplate:   "/api/v1/ping",
		Method:         http.MethodGet,
		APIVersion:     "v1",
		UpstreamTarget: "http://upstream.test",
	})
	return table
}

// installPopulatedRouteTable swaps a one-route table into the server.
func installPopulatedRouteTable(t *testing.T, server *Server) {
	t.Helper()
	if err := server.routeTableHolder.Swap(populatedRouteTable()); err != nil {
		t.Fatalf("swap populated route table: %v", err)
	}
}

// assertOnlyDependencyFails pins the "fails independently" contract: the
// named dependency is false, every other reported dependency is true, and the
// aggregate ready flag mirrors the failure.
func assertOnlyDependencyFails(t *testing.T, body map[string]bool, failing string) {
	t.Helper()
	if body[failing] {
		t.Errorf("%s = true, want false", failing)
	}
	for _, name := range []string{
		readyzCheckRouteTable,
		readyzCheckAllowlist,
		readyzCheckOpenBao,
		readyzCheckCredentialProbe,
	} {
		if name == failing {
			continue
		}
		if !body[name] {
			t.Errorf("%s = false while only %s fails; dependencies must fail independently, got %v", name, failing, body)
		}
	}
	if body["ready"] {
		t.Errorf("ready = true while %s fails, got %v", failing, body)
	}
}

func TestReadyzRouteTableDependencyFailsIndependently(t *testing.T) {
	server := newReadyzTestServer(t)

	// An empty table - no valid fragment loaded - must withhold readiness.
	status, body := readyzProbe(t, server)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("readyz with empty route table = %d, want %d", status, http.StatusServiceUnavailable)
	}
	assertOnlyDependencyFails(t, body, readyzCheckRouteTable)

	installPopulatedRouteTable(t, server)

	status, body = readyzProbe(t, server)
	if status != http.StatusOK {
		t.Fatalf("readyz with populated route table = %d, want %d", status, http.StatusOK)
	}
	if !body["ready"] || !body[readyzCheckRouteTable] {
		t.Errorf("expected a ready route table, got %v", body)
	}
}

func TestReadyzOpenBaoDependencyFailsIndependently(t *testing.T) {
	server := newReadyzTestServer(t)
	installPopulatedRouteTable(t, server)

	server.setOpenBaoReady(false)

	status, body := readyzProbe(t, server)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("readyz while OpenBao login pending = %d, want %d", status, http.StatusServiceUnavailable)
	}
	assertOnlyDependencyFails(t, body, readyzCheckOpenBao)

	server.setOpenBaoReady(true)

	status, body = readyzProbe(t, server)
	if status != http.StatusOK {
		t.Fatalf("readyz after OpenBao login = %d, want %d", status, http.StatusOK)
	}
	if !body["ready"] || !body[readyzCheckOpenBao] {
		t.Errorf("expected OpenBao dependency satisfied, got %v", body)
	}
}

func TestReadyzAllowlistDependencyFailsIndependently(t *testing.T) {
	server := newReadyzTestServer(t)
	installPopulatedRouteTable(t, server)

	// No allowlist loaded: fail-closed, readiness withheld.
	failsClosed, err := spec.NewAllowlistEnforcer(spec.DefaultVaultBaseDir, "")
	if err != nil {
		t.Fatalf("create fail-closed enforcer: %v", err)
	}
	server.allowlistEnforcer = failsClosed

	status, body := readyzProbe(t, server)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("readyz with fail-closed allowlist = %d, want %d", status, http.StatusServiceUnavailable)
	}
	assertOnlyDependencyFails(t, body, readyzCheckAllowlist)

	// A loaded allowlist satisfies the dependency.
	allowlistFile := filepath.Join(t.TempDir(), "allowlist.yaml")
	if err := os.WriteFile(allowlistFile, []byte("allowed_hosts:\n  - localhost\n"), 0o600); err != nil {
		t.Fatalf("write allowlist file: %v", err)
	}
	loaded, err := spec.NewAllowlistEnforcer(spec.DefaultVaultBaseDir, allowlistFile)
	if err != nil {
		t.Fatalf("create loaded enforcer: %v", err)
	}
	server.allowlistEnforcer = loaded

	status, body = readyzProbe(t, server)
	if status != http.StatusOK {
		t.Fatalf("readyz with loaded allowlist = %d, want %d", status, http.StatusOK)
	}
	if !body["ready"] || !body[readyzCheckAllowlist] {
		t.Errorf("expected allowlist dependency satisfied, got %v", body)
	}
}

func TestReadyzCredentialProbeDependencyFailsIndependently(t *testing.T) {
	server := newReadyzTestServer(t)
	installPopulatedRouteTable(t, server)

	// No registry attached: nothing configured, dependency satisfied.
	status, body := readyzProbe(t, server)
	if status != http.StatusOK {
		t.Fatalf("readyz without probe registry = %d, want %d", status, http.StatusOK)
	}
	if !body[readyzCheckCredentialProbe] {
		t.Errorf("credential_probe = false with no probes configured, want true: %v", body)
	}

	// Attached registry with no results: probe loop still in cold start.
	registry := NewCredentialProbeRegistry()
	server.setCredentialProbes(registry)
	if status, _ = readyzProbe(t, server); status != http.StatusOK {
		t.Fatalf("readyz during probe cold start = %d, want %d", status, http.StatusOK)
	}

	now := time.Now()

	// A configured probe that has never verified is not fresh.
	registry.Set(CredentialProbeResult{
		FragmentID: "kubernetes-api",
		InstanceID: "prod",
		Status:     CredentialUnknown,
		Interval:   defaultCredentialProbeInterval,
	})
	status, body = readyzProbe(t, server)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("readyz with never-verified probe = %d, want %d", status, http.StatusServiceUnavailable)
	}
	assertOnlyDependencyFails(t, body, readyzCheckCredentialProbe)

	// A fresh successful verification satisfies the dependency.
	registry = NewCredentialProbeRegistry()
	server.setCredentialProbes(registry)
	registry.Set(CredentialProbeResult{
		FragmentID:   "kubernetes-api",
		InstanceID:   "prod",
		Status:       CredentialHealthy,
		Interval:     defaultCredentialProbeInterval,
		LastVerified: &now,
	})
	if status, _ = readyzProbe(t, server); status != http.StatusOK {
		t.Fatalf("readyz with fresh probe verification = %d, want %d", status, http.StatusOK)
	}

	// Beyond twice the cadence plus grace, the signal is stale.
	registry = NewCredentialProbeRegistry()
	server.setCredentialProbes(registry)
	stale := now.Add(-2*defaultCredentialProbeInterval - probeFreshnessGrace - time.Minute)
	registry.Set(CredentialProbeResult{
		FragmentID:   "kubernetes-api",
		InstanceID:   "prod",
		Status:       CredentialHealthy,
		Interval:     defaultCredentialProbeInterval,
		LastVerified: &stale,
	})
	status, body = readyzProbe(t, server)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("readyz with stale probe verification = %d, want %d", status, http.StatusServiceUnavailable)
	}
	assertOnlyDependencyFails(t, body, readyzCheckCredentialProbe)

	// The window scales with the probe's own cadence: a 24h probe verified
	// 3h ago is well inside its window and must not withhold readiness.
	registry = NewCredentialProbeRegistry()
	server.setCredentialProbes(registry)
	old := now.Add(-3 * time.Hour)
	registry.Set(CredentialProbeResult{
		FragmentID:   "kubernetes-api",
		InstanceID:   "prod",
		Status:       CredentialHealthy,
		Interval:     24 * time.Hour,
		LastVerified: &old,
	})
	if status, _ = readyzProbe(t, server); status != http.StatusOK {
		t.Fatalf("readyz with in-window long-cadence probe = %d, want %d", status, http.StatusOK)
	}
}
