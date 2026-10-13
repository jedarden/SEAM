package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedProviderProfilesCarryTheSeedSet(t *testing.T) {
	profiles, err := SeedProviderProfiles()
	if err != nil {
		t.Fatalf("SeedProviderProfiles: %v", err)
	}
	want := []string{"anthropic-messages", "bearer-json", "openai-compatible"}
	if got := profiles.Names(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("seed profile names = %v, want %v", got, want)
	}
}

func TestApplyProviderProfileFillsAbsentFieldsAndKeepsFragmentOverrides(t *testing.T) {
	profiles, err := SeedProviderProfiles()
	if err != nil {
		t.Fatalf("SeedProviderProfiles: %v", err)
	}
	fragment := map[string]any{
		"x-seam-owner":        "subq",
		"x-provider-profile":  "openai-compatible",
		"x-credential-source": "vault",
		"x-vault-path":        "secret/rs-manager/subq/api-key",
	}
	applied, err := ApplyProviderProfile(fragment, profiles)
	if err != nil {
		t.Fatalf("ApplyProviderProfile: %v", err)
	}
	if applied != "openai-compatible" {
		t.Fatalf("applied = %q, want openai-compatible", applied)
	}
	if source, _ := fragment["x-credential-source"].(string); source != "vault" {
		t.Fatalf("fragment x-credential-source override lost: %v", fragment["x-credential-source"])
	}
	// Non-credential defaults still arrive from the profile.
	if mode, _ := fragment["x-body-mode"].(string); mode != "opaque" {
		t.Fatalf("profile body-mode default missing: %v", fragment["x-body-mode"])
	}
	if streaming, _ := fragment["x-streaming"].(bool); !streaming {
		t.Fatalf("profile streaming default missing: %v", fragment["x-streaming"])
	}
}

func TestApplyProviderProfileSubstitutesOwnerToken(t *testing.T) {
	profiles, err := SeedProviderProfiles()
	if err != nil {
		t.Fatalf("SeedProviderProfiles: %v", err)
	}
	fragment := map[string]any{
		"x-seam-owner":       "llm-gateway",
		"x-provider-profile": "anthropic-messages",
	}
	if _, err := ApplyProviderProfile(fragment, profiles); err != nil {
		t.Fatalf("ApplyProviderProfile: %v", err)
	}
	if name, _ := fragment["x-credential-env"].(string); name != "SEAM_CRED_LLM_GATEWAY_API_KEY" {
		t.Fatalf("credential env = %q, want owner-namespace substitution", name)
	}
	inject, _ := fragment["x-inject-as"].(map[string]any)
	if inject == nil || inject["kind"] != "header" || inject["name"] != "x-api-key" {
		t.Fatalf("anthropic-messages inject-as = %v, want x-api-key header injection", fragment["x-inject-as"])
	}
}

func TestApplyProviderProfileRejectsUnknownName(t *testing.T) {
	profiles, err := SeedProviderProfiles()
	if err != nil {
		t.Fatalf("SeedProviderProfiles: %v", err)
	}
	fragment := map[string]any{"x-seam-owner": "subq", "x-provider-profile": "closedai"}
	if _, err := ApplyProviderProfile(fragment, profiles); err == nil || !strings.Contains(err.Error(), "unknown provider profile") {
		t.Fatalf("unknown profile error = %v, want unknown-profile rejection", err)
	}
}

func TestParseProviderProfilesRejectsOperatorOwnedControls(t *testing.T) {
	for _, field := range []string{"x-unscrubbable", "x-upstream-plaintext", "x-max-request-bytes", "x-timeout", "x-required-scope", "x-upstream"} {
		document := "profiles:\n  p:\n    " + field + ": {}\n"
		if _, err := ParseProviderProfiles([]byte(document), "test"); err == nil || !strings.Contains(err.Error(), field) {
			t.Fatalf("profile carrying %s: error = %v, want field named in rejection", field, err)
		}
	}
}

func TestLintAppliesProfileDefaultsBeforeOperatorControls(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "subq"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The acceptance fragment: profile + upstream + owner, nothing else.
	const profileOnly = `x-seam-schema: v1
x-seam-owner: subq
x-provider-profile: openai-compatible
x-upstream: https://api.subq.ai/v1
x-cost-per-call: {amount: 0.001, unit: USD}
x-max-request-bytes: 33554432
paths:
  /chat/completions:
    post:
      responses:
        "200": {description: ok}
`
	if err := os.WriteFile(filepath.Join(dir, "subq", "route.yaml"), []byte(profileOnly), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := LintDirectory(LintOptions{FragmentsDir: dir, SchemaPath: "../../spec/route-fragment-schema.json"})
	if err != nil {
		t.Fatalf("LintDirectory: %v", err)
	}
	if report.HasErrors() {
		t.Fatalf("profile-only fragment has lint errors: %+v", report.Errors)
	}

	// A fragment overriding the profile's env name with another owner's
	// variable is rejected: the ownership rule binds on the merged fragment.
	foreign := strings.Replace(profileOnly, "x-upstream:", "x-credential-env: SEAM_CRED_ZAI_API_KEY\nx-upstream:", 1)
	foreignDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(foreignDir, "subq"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreignDir, "subq", "route.yaml"), []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err = LintDirectory(LintOptions{FragmentsDir: foreignDir, SchemaPath: "../../spec/route-fragment-schema.json"})
	if err != nil {
		t.Fatalf("LintDirectory(foreign): %v", err)
	}
	if !report.HasErrors() {
		t.Fatal("foreign SEAM_CRED_ZAI_API_KEY override produced no lint error")
	}
	found := false
	for _, finding := range report.Errors {
		if finding.Code == "credential.env-owner-mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("foreign env name reported as %+v, want credential.env-owner-mismatch", report.Errors)
	}
}

func TestLintRejectsUnknownProviderProfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "subq"), 0o755); err != nil {
		t.Fatal(err)
	}
	fragment := "x-seam-schema: v1\nx-seam-owner: subq\nx-provider-profile: nope\nx-upstream: https://api.subq.ai/v1\npaths:\n  /x:\n    get:\n      responses:\n        \"200\": {description: ok}\n"
	if err := os.WriteFile(filepath.Join(dir, "subq", "route.yaml"), []byte(fragment), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := LintDirectory(LintOptions{FragmentsDir: dir, SchemaPath: "../../spec/route-fragment-schema.json"})
	if err != nil {
		t.Fatalf("LintDirectory: %v", err)
	}
	if !report.HasErrors() || report.Errors[0].Code != "profile.unknown" {
		t.Fatalf("unknown profile findings = %+v, want profile.unknown error", report.Errors)
	}
}

func TestGatewayLoaderQuarantinesUnknownProviderProfile(t *testing.T) {
	dir := t.TempDir()
	serviceDir := filepath.Join(dir, "subq")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fragment := `{"x-seam-schema":"v1","x-seam-owner":"subq","x-provider-profile":"nope","x-upstream":"https://api.subq.ai/v1","paths":{"/x":{"get":{"responses":{"200":{"description":"ok"}}}}}}`
	if err := os.WriteFile(filepath.Join(serviceDir, "route.json"), []byte(fragment), 0o644); err != nil {
		t.Fatal(err)
	}
	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.LoadDirectory(dir); err != nil {
		// A directory whose only fragment was quarantined reports the
		// nothing-loadable refusal; the quarantine itself already happened.
		if !strings.Contains(err.Error(), "refusing to build an empty API document") {
			t.Fatalf("LoadDirectory: %v", err)
		}
	}
	if loader.GetValidFragmentCount() != 0 || loader.GetQuarantinedCount() != 1 {
		t.Fatalf("valid=%d quarantined=%d, want the unknown-profile fragment quarantined", loader.GetValidFragmentCount(), loader.GetQuarantinedCount())
	}
	if reasons := loader.GetQuarantined()[0].QuarantineReasons; len(reasons) == 0 || !strings.Contains(reasons[0], "unknown provider profile") {
		t.Fatalf("quarantine reasons = %v, want unknown-profile reason", reasons)
	}
}

func TestResolveProviderProfilesFailsClosedOnBadConfiguredFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte("profiles:\n  p:\n    x-unscrubbable: acknowledged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ProviderProfilesEnvVar, path)
	if _, err := ResolveProviderProfiles(); err == nil {
		t.Fatal("configured profile file with an operator control resolved without error")
	}
}
