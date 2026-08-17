package spec

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLintDirectoryAcceptsYAMLAndTreatsAbsentManifestAsInert(t *testing.T) {
	root := t.TempDir()
	writeLintTestFragment(t, root, "owner", "route.yaml", validLintFragment("owner", "v1", "https://api.example.com"))

	report, err := LintDirectory(LintOptions{
		FragmentsDir: root,
		SchemaPath:   lintTestSchemaPath(t),
		// Deliberately absent: this is the pre-6a inert allowlist case.
		UpstreamAllowlistPath: filepath.Join(root, "not-yet-created.yaml"),
	})
	if err != nil {
		t.Fatalf("LintDirectory returned setup error: %v", err)
	}
	if report.HasErrors() {
		t.Fatalf("valid YAML fragment was rejected: %+v", report.Errors)
	}
	if report.Files != 1 || len(report.Warnings) != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestLintDirectory9aHardErrorsAndFlags(t *testing.T) {
	tests := []struct {
		name       string
		fragment   string
		wantErrors []string
		wantWarn   []string
	}{
		{
			name: "owner and vault chain",
			fragment: strings.ReplaceAll(
				validLintFragment("owner", "v1", "https://api.example.com"),
				"x-seam-owner: owner\n", "x-seam-owner: other\nx-vault-path: seam/routes/owner/token\n"),
			wantErrors: []string{"owner.directory-mismatch", "owner.vault-path-mismatch"},
		},
		{
			name: "reserved and authored unversioned",
			fragment: strings.ReplaceAll(
				strings.ReplaceAll(validLintFragment("owner", "v1", "https://api.example.com"), "  /api:\n", "  /docs:\n"),
				"x-api-version: v1\n", "x-api-version: _unversioned\n"),
			wantErrors: []string{"api-version.invalid", "path.reserved"},
		},
		{
			name: "plaintext and URL checks",
			fragment: strings.ReplaceAll(
				validLintFragment("owner", "v1", "http://127.0.0.1:8080"),
				"x-upstream: http://127.0.0.1:8080\n", "x-upstream: http://127.0.0.1:8080\nx-upstream-tls:\n  insecureSkipVerify: acknowledged\n"),
			wantErrors: []string{"fragment.schema", "upstream.ip-literal", "transport.plaintext-missing"},
			wantWarn:   []string{"transport.insecure-skip-verify"},
		},
		{
			name: "malformed absolute URL",
			fragment: strings.ReplaceAll(
				validLintFragment("owner", "v1", "https://api.example.com"),
				"x-upstream: https://api.example.com\n", "x-upstream: api.example.com\n"),
			wantErrors: []string{"fragment.schema", "upstream.url-invalid"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeLintTestFragment(t, root, "owner", "route.yaml", test.fragment)
			report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
			if err != nil {
				t.Fatalf("LintDirectory returned setup error: %v", err)
			}
			assertFindingCodes(t, report.Errors, test.wantErrors)
			assertFindingCodes(t, report.Warnings, test.wantWarn)
		})
	}
}

func TestLintDirectoryDetectsTripleCollisionsButAllowsMethodAndVersionCoexistence(t *testing.T) {
	root := t.TempDir()
	writeLintTestFragment(t, root, "owner", "a.yaml", validLintFragment("owner", "v1", "https://api.example.com"))
	writeLintTestFragment(t, root, "other", "b.yaml", validLintFragment("other", "v1", "https://api.example.com"))
	writeLintTestFragment(t, root, "third", "c.yaml", strings.ReplaceAll(validLintFragment("third", "v1", "https://api.example.com"), "    get:\n", "    post:\n"))
	writeLintTestFragment(t, root, "fourth", "d.yaml", validLintFragment("fourth", "v2", "https://api.example.com"))

	report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatalf("LintDirectory returned setup error: %v", err)
	}
	assertFindingCodes(t, report.Errors, []string{"path.collision"})
	if len(report.Errors) != 1 {
		t.Fatalf("expected one collision and no method/version collision, got %+v", report.Errors)
	}
}

func TestLintDirectoryChecksAllowlistWhenManifestExists(t *testing.T) {
	root := t.TempDir()
	writeLintTestFragment(t, root, "owner", "route.yaml", validLintFragment("owner", "v1", "https://not-allowed.example.com"))
	allowlist := filepath.Join(root, "allowlist.yaml")
	if err := os.WriteFile(allowlist, []byte("hosts:\n  - api.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := LintDirectory(LintOptions{
		FragmentsDir:          root,
		SchemaPath:            lintTestSchemaPath(t),
		UpstreamAllowlistPath: allowlist,
	})
	if err != nil {
		t.Fatalf("LintDirectory returned setup error: %v", err)
	}
	assertFindingCodes(t, report.Errors, []string{"upstream.host-not-allowed"})
}

func validLintFragment(owner, version, upstream string) string {
	return "x-seam-schema: v1\n" +
		"x-seam-owner: " + owner + "\n" +
		"x-api-version: " + version + "\n" +
		"x-upstream: " + upstream + "\n" +
		"paths:\n" +
		"  /api:\n" +
		"    get:\n" +
		"      responses:\n" +
		"        \"200\":\n" +
		"          description: ok\n"
}

func writeLintTestFragment(t *testing.T, root, owner, name, contents string) {
	t.Helper()
	directory := filepath.Join(root, owner)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func lintTestSchemaPath(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(sourceFile), "..", "..", "spec", "route-fragment-schema.json")
}

func assertFindingCodes(t *testing.T, findings []LintFinding, want []string) {
	t.Helper()
	for _, code := range want {
		found := false
		for _, finding := range findings {
			if finding.Code == code {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("finding code %q not present in %+v", code, findings)
		}
	}
}
