package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// reportedVaultBaseDir builds a server and reads back the vault base directory
// it actually enforces, via the same /config/status section an operator sees.
// Reading it through the running server rather than off the Config struct is
// the point: the whole reason SEAM_VAULT_BASE_DIR exists is that the prefix is
// deployment configuration, so the assertion has to land on the resolved
// value, not on the field that carries it in.
func reportedVaultBaseDir(t *testing.T, vaultBaseDir string) (string, *Server) {
	t.Helper()

	s := New(&Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
		VaultBaseDir: vaultBaseDir,
	})

	req := httptest.NewRequest(http.MethodGet, "/config/status", nil)
	recorder := httptest.NewRecorder()
	s.configStatusHandler(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var status map[string]interface{}
	if err := json.NewDecoder(recorder.Body).Decode(&status); err != nil {
		t.Fatalf("decode /config/status response: %v", err)
	}
	allowlist, ok := status["allowlist"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing allowlist section in /config/status: %v", status)
	}
	got, _ := allowlist["vault_base_dir"].(string)
	return got, s
}

// TestVaultBaseDirDefaultIsEstatePrefix pins the behaviour an unset
// SEAM_VAULT_BASE_DIR must preserve: the consolidated estate prefix
// secret/<installation>/<cluster>/seam/routes, with both segments naming
// rs-manager, is what ValidateVaultPath enforces. The schema itself stays
// base-agnostic (docs/notes/route-fragment-schema.md) — this is the default
// base, not a pattern constraint.
func TestVaultBaseDirDefaultIsEstatePrefix(t *testing.T) {
	got, s := reportedVaultBaseDir(t, "")
	if got != "rs-manager/rs-manager/seam/routes" {
		t.Fatalf("default vault base dir = %q, want \"rs-manager/rs-manager/seam/routes\"", got)
	}

	if err := s.allowlistEnforcer.ValidateVaultPath("rs-manager/rs-manager/seam/routes/unifi/api-key", "unifi"); err != nil {
		t.Errorf("path under the default prefix rejected: %v", err)
	}
	if err := s.allowlistEnforcer.ValidateVaultPath("rs-manager/rs-manager/seam/routes/twitterapi/api-key", "twitterapi"); err != nil {
		t.Errorf("path under the default prefix rejected: %v", err)
	}
	// A path nesting under a different base is outside the owner directory.
	if err := s.allowlistEnforcer.ValidateVaultPath("tenants/alpha/unifi/api-key", "unifi"); err == nil {
		t.Error("path outside the default prefix was accepted")
	}
	// The pre-consolidation prefix is no longer the default: a fragment still
	// carrying the bare seam/routes path must now be rejected.
	if err := s.allowlistEnforcer.ValidateVaultPath("seam/routes/unifi/api-key", "unifi"); err == nil {
		t.Error("path under the superseded bare seam/routes prefix was accepted")
	}
}

// TestVaultBaseDirMovesEnforcedPrefix is the wiring assertion for
// SEAM_VAULT_BASE_DIR: whatever resolves the variable into cfg.VaultBaseDir
// (cmd/seam's resolveVaultBaseDir) has to be honoured here, moving the prefix
// ValidateVaultPath enforces — including rejecting paths that were valid under
// the default.
func TestVaultBaseDirMovesEnforcedPrefix(t *testing.T) {
	got, s := reportedVaultBaseDir(t, "tenants/alpha")
	if got != "tenants/alpha" {
		t.Fatalf("configured vault base dir = %q, want \"tenants/alpha\"", got)
	}

	if err := s.allowlistEnforcer.ValidateVaultPath("tenants/alpha/unifi/api-key", "unifi"); err != nil {
		t.Errorf("path under the configured prefix rejected: %v", err)
	}
	if err := s.allowlistEnforcer.ValidateVaultPath("tenants/alpha/twitterapi/api-key", "twitterapi"); err != nil {
		t.Errorf("path under the configured prefix rejected: %v", err)
	}
	// The prefix moved: the previously-valid default location is no longer
	// inside the owner directory.
	if err := s.allowlistEnforcer.ValidateVaultPath("seam/routes/unifi/api-key", "unifi"); err == nil {
		t.Error("path under the old default prefix was accepted after the override")
	}
}

// TestVaultBaseDirOwnerScopingSurvivesOverride checks that the override moves
// the base, not the co-ownership rule: an owner still cannot reach another
// owner's directory beneath the configured base.
func TestVaultBaseDirOwnerScopingSurvivesOverride(t *testing.T) {
	_, s := reportedVaultBaseDir(t, "tenants/alpha")

	if err := s.allowlistEnforcer.ValidateVaultPath("tenants/alpha/twitterapi/api-key", "unifi"); err == nil {
		t.Error("cross-owner path was accepted under the configured base")
	}
}
