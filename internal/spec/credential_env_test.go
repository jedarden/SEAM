package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envCredentialFragment(owner, variable string) string {
	fragment := validLintFragment(owner, "v1", "https://api.example.com")
	injection := "x-credential-source: env\nx-credential-env: " + variable + "\nx-inject-as:\n  kind: bearer\n"
	return strings.Replace(fragment, "x-upstream: https://api.example.com\n", "x-upstream: https://api.example.com\n"+injection, 1)
}

func fileCredentialFragment(owner, filename string) string {
	fragment := validLintFragment(owner, "v1", "https://api.example.com")
	injection := "x-credential-source: file\nx-credential-file: " + filename + "\nx-inject-as:\n  kind: bearer\n"
	return strings.Replace(fragment, "x-upstream: https://api.example.com\n", "x-upstream: https://api.example.com\n"+injection, 1)
}

func TestCredentialEnvOwnershipLintAndLoader(t *testing.T) {
	tests := []struct {
		name     string
		owner    string
		variable string
		siblings []string
	}{
		{name: "sibling owner prefix", owner: "ab", variable: "SEAM_CRED_AB_CD_API_KEY", siblings: []string{"ab-cd"}},
		{name: "OpenBao configuration", owner: "service-a", variable: "SEAM_OPENBAO_ADDR"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeLintTestFragment(t, root, test.owner, "route.yaml", envCredentialFragment(test.owner, test.variable))
			for _, sibling := range test.siblings {
				writeLintTestFragment(t, root, sibling, "route.yaml", envCredentialFragment(sibling, "SEAM_CRED_"+strings.ToUpper(strings.ReplaceAll(sibling, "-", "_"))+"_API_KEY"))
			}

			lint, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
			if err != nil {
				t.Fatalf("LintDirectory() error = %v", err)
			}
			if !lint.HasErrors() {
				t.Fatal("lint accepted an env variable outside the fragment owner's namespace")
			}
			if test.name == "sibling owner prefix" {
				found := false
				for _, finding := range lint.Errors {
					if finding.Code == "credential.env-owner-mismatch" && strings.Contains(finding.Message, `"ab-cd"`) {
						found = true
					}
				}
				if !found {
					t.Fatalf("lint did not identify the more specific sibling owner: %+v", lint.Errors)
				}
			}

			loader, err := NewFragmentLoader()
			if err != nil {
				t.Fatalf("NewFragmentLoader() error = %v", err)
			}
			if err := loader.LoadDirectory(root); err != nil {
				t.Fatalf("LoadDirectory() error = %v", err)
			}
			if err := loader.ValidateFragments(lintTestSchemaPath(t)); err != nil {
				t.Fatalf("ValidateFragments() error = %v", err)
			}
			quarantined := loader.GetQuarantined()
			if len(quarantined) == 0 {
				t.Fatal("gateway loader did not quarantine the invalid env owner")
			}
			if test.name == "sibling owner prefix" {
				for _, fragment := range quarantined {
					if fragment.Owner == "ab" && strings.Contains(strings.Join(fragment.QuarantineReasons, "\n"), `more specific owner "ab-cd"`) {
						return
					}
				}
				t.Fatalf("loader did not name the more specific sibling owner: %+v", quarantined[0].QuarantineReasons)
			}
		})
	}
}

func TestCredentialEnvOwnershipRejectsConfigurationNames(t *testing.T) {
	for _, name := range []string{"SEAM_OPENBAO_ADDR", "SEAM_VAULT_BASE_DIR", "SEAM_CRED_OTHER_API_KEY"} {
		if err := credentialEnvOwnerError(name, "service-a", []string{"service-a"}); err == nil {
			t.Errorf("credentialEnvOwnerError(%q) unexpectedly accepted", name)
		}
	}
}

func TestCredentialFileOwnershipLintAndLoader(t *testing.T) {
	root := t.TempDir()
	writeLintTestFragment(t, root, "service-a", "route.yaml", fileCredentialFragment("service-a", "SEAM_CRED_OTHER_API_KEY"))

	lint, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatalf("LintDirectory() error = %v", err)
	}
	found := false
	for _, finding := range lint.Errors {
		if finding.Code == "credential.file-owner-mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("lint did not reject file outside owner namespace: %+v", lint.Errors)
	}

	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatalf("NewFragmentLoader() error = %v", err)
	}
	if err := loader.LoadDirectory(root); err != nil {
		t.Fatalf("LoadDirectory() error = %v", err)
	}
	if err := loader.ValidateFragments(lintTestSchemaPath(t)); err != nil {
		t.Fatalf("ValidateFragments() error = %v", err)
	}
	quarantined := loader.GetQuarantined()
	if len(quarantined) != 1 || !strings.Contains(strings.Join(quarantined[0].QuarantineReasons, "\n"), "credential_file_ownership_failed") {
		t.Fatalf("loader did not quarantine the invalid file owner: %+v", quarantined)
	}
}

func TestCredentialFileOwnershipRejectsSpecificSiblingOwner(t *testing.T) {
	root := t.TempDir()
	writeLintTestFragment(t, root, "ab", "route.yaml", fileCredentialFragment("ab", "SEAM_CRED_AB_CD_API_KEY"))
	writeLintTestFragment(t, root, "ab-cd", "route.yaml", fileCredentialFragment("ab-cd", "SEAM_CRED_AB_CD_API_KEY"))

	lint, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatalf("LintDirectory() error = %v", err)
	}
	for _, finding := range lint.Errors {
		if finding.Code == "credential.file-owner-mismatch" && strings.Contains(finding.Message, `"ab-cd"`) {
			return
		}
	}
	t.Fatalf("lint did not identify the more specific file owner: %+v", lint.Errors)
}

func TestLintSchemaCredentialSourcePairing(t *testing.T) {
	base := validLintFragment("owner", "v1", "https://api.example.com")
	tests := []struct {
		name     string
		metadata string
		valid    bool
	}{
		{name: "env source", metadata: "x-credential-source: env\nx-credential-env: SEAM_CRED_OWNER_API_KEY\nx-inject-as:\n  kind: bearer\n", valid: true},
		{name: "file source", metadata: "x-credential-source: file\nx-credential-file: SEAM_CRED_OWNER_API_KEY\nx-inject-as:\n  kind: bearer\n", valid: true},
		{name: "legacy vault source", metadata: "x-vault-path: rs-manager/seam/routes/owner/api-key\nx-inject-as:\n  kind: bearer\n", valid: true},
		{name: "env without injection", metadata: "x-credential-source: env\nx-credential-env: SEAM_CRED_OWNER_API_KEY\n"},
		{name: "file without injection", metadata: "x-credential-source: file\nx-credential-file: SEAM_CRED_OWNER_API_KEY\n"},
		{name: "injection without source", metadata: "x-inject-as:\n  kind: bearer\n"},
		{name: "two sources", metadata: "x-vault-path: rs-manager/seam/routes/owner/api-key\nx-credential-source: env\nx-credential-env: SEAM_CRED_OWNER_API_KEY\nx-inject-as:\n  kind: bearer\n"},
		{name: "env and file sources", metadata: "x-credential-source: file\nx-credential-env: SEAM_CRED_OWNER_API_KEY\nx-credential-file: SEAM_CRED_OWNER_FILE_KEY\nx-inject-as:\n  kind: bearer\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			fragment := strings.Replace(base, "x-upstream: https://api.example.com\n", "x-upstream: https://api.example.com\n"+test.metadata, 1)
			writeLintTestFragment(t, root, "owner", "route.yaml", fragment)
			report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
			if err != nil {
				t.Fatalf("LintDirectory() error = %v", err)
			}
			if report.HasErrors() == test.valid {
				t.Fatalf("lint errors=%+v, valid=%v", report.Errors, test.valid)
			}
		})
	}
}

func TestCredentialEnvRuntimeQuarantineReasonAppearsInFragmentStatus(t *testing.T) {
	root := t.TempDir()
	writeLintTestFragment(t, root, "owner", "route.yaml", envCredentialFragment("owner", "SEAM_CRED_OTHER_API_KEY"))
	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.LoadDirectory(root); err != nil {
		t.Fatal(err)
	}
	if err := loader.ValidateFragments(lintTestSchemaPath(t)); err != nil {
		t.Fatal(err)
	}
	status := loader.GetFragmentStatus()
	fragments := status["fragments"].([]FragmentStatus)
	if len(fragments) != 1 || fragments[0].Status != "quarantined" || !strings.Contains(strings.Join(fragments[0].QuarantineReasons, "\n"), "SEAM_CRED_OWNER_") {
		t.Fatalf("fragment quarantine status = %+v", fragments)
	}
}

func TestCredentialEnvLintInputsAreNotMutated(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner", "route.yaml")
	writeLintTestFragment(t, root, "owner", "route.yaml", envCredentialFragment("owner", "SEAM_CRED_OWNER_API_KEY"))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("lint modified a fragment")
	}
}
