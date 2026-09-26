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

// TestLintDirectoryUnscrubbableAcknowledgementContract pins the lint half of
// the x-unscrubbable contract: every acknowledgement draws the human-review
// warning at whichever level declares it, and an acknowledgement on a fragment
// that injects no credential is additionally flagged as vacuous.
func TestLintDirectoryUnscrubbableAcknowledgementContract(t *testing.T) {
	rootAcknowledgement := strings.ReplaceAll(
		validLintFragment("owner", "v1", "https://api.example.com"),
		"x-upstream: https://api.example.com\n",
		"x-upstream: https://api.example.com\nx-unscrubbable: acknowledged\n")
	operationAcknowledgement := strings.ReplaceAll(
		validLintFragment("owner", "v1", "https://api.example.com"),
		"    get:\n",
		"    get:\n      x-unscrubbable: acknowledged\n")
	injection := "x-vault-path: seam/routes/owner/token\nx-inject-as:\n  kind: header\n  name: X-Api-Key\n"

	tests := []struct {
		name            string
		fragment        string
		wantVacuousCode bool
	}{
		{
			name:            "root acknowledgement with credential injection is not vacuous",
			fragment:        strings.ReplaceAll(rootAcknowledgement, "x-upstream: https://api.example.com\n", "x-upstream: https://api.example.com\n"+injection),
			wantVacuousCode: false,
		},
		{
			name:            "operation acknowledgement with credential injection is not vacuous",
			fragment:        strings.ReplaceAll(operationAcknowledgement, "x-upstream: https://api.example.com\n", "x-upstream: https://api.example.com\n"+injection),
			wantVacuousCode: false,
		},
		{
			name:            "root acknowledgement without credential injection is vacuous",
			fragment:        rootAcknowledgement,
			wantVacuousCode: true,
		},
		{
			name:            "operation acknowledgement without credential injection is vacuous",
			fragment:        operationAcknowledgement,
			wantVacuousCode: true,
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
			if report.HasErrors() {
				t.Fatalf("fragment was rejected: %+v", report.Errors)
			}
			hasReview, hasVacuous := false, false
			for _, warning := range report.Warnings {
				switch warning.Code {
				case "scrubbing.unscrubbable":
					hasReview = true
				case "scrubbing.unscrubbable-vacuous":
					hasVacuous = true
				}
			}
			if !hasReview {
				t.Fatalf("acknowledgement drew no human-review warning: %+v", report.Warnings)
			}
			if hasVacuous != test.wantVacuousCode {
				t.Fatalf("vacuous warning = %v, want %v (warnings: %+v)", hasVacuous, test.wantVacuousCode, report.Warnings)
			}
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

// deployedUpstreamAllowlist is the shape the seam-upstream-allowlist ConfigMap
// actually ships: a snake_case upstream_hosts key mixing exact hostnames,
// port-pinned hosts and wildcard suffixes, next to a vault_paths key the host
// loader must leave alone.
const deployedUpstreamAllowlist = `# SEAM Upstream Allowlist Configuration
upstream_hosts:
  - "openbao.openbao.svc.cluster.local"
  - "*.ardenone.com"
  - "api.z.ai"
  - "traefik-iad-ci:8001"
  - "kubernetes.default.svc"
vault_paths:
  - "seam/routes/"
`

func TestLoadUpstreamAllowlistReadsDeployedConfigMapShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allowlist.yaml")
	if err := os.WriteFile(path, []byte(deployedUpstreamAllowlist), 0o600); err != nil {
		t.Fatal(err)
	}

	allowlist, err := loadUpstreamAllowlist(path)
	if err != nil {
		t.Fatalf("loadUpstreamAllowlist returned error: %v", err)
	}
	if allowlist == nil {
		t.Fatal("loadUpstreamAllowlist returned nil for a manifest that exists")
	}

	seen := make(map[string]bool, len(allowlist.entries))
	for _, entry := range allowlist.entries {
		seen[entry] = true
	}
	for _, want := range []string{"openbao.openbao.svc.cluster.local", "*.ardenone.com", "api.z.ai", "traefik-iad-ci", "kubernetes.default.svc"} {
		if !seen[want] {
			t.Errorf("allowlist entry %q missing from %v", want, allowlist.entries)
		}
	}
	if seen["traefik-iad-ci:8001"] {
		t.Errorf("port-pinned entry was not reduced to its host half: %v", allowlist.entries)
	}
	if seen["seam/routes/"] {
		t.Errorf("vault_paths was swallowed as a host entry: %v", allowlist.entries)
	}
}

func TestDeployedUpstreamAllowlistMatchesDeclaredUpstreams(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allowlist.yaml")
	if err := os.WriteFile(path, []byte(deployedUpstreamAllowlist), 0o600); err != nil {
		t.Fatal(err)
	}
	allowlist, err := loadUpstreamAllowlist(path)
	if err != nil {
		t.Fatalf("loadUpstreamAllowlist returned error: %v", err)
	}

	for host, want := range map[string]bool{
		"unifi.ardenone.com":  true,  // wildcard suffix
		"traefik-iad-ci":      true,  // port-pinned entry matches on the bare host
		"api.z.ai":            true,  // exact
		"ardenone.com":        false, // a wildcard does not cover its own apex domain
		"traefik-evil-ci":     false, // must not match on a shared prefix
		"not-allowed.example": false,
	} {
		if got := allowlist.allowed(host); got != want {
			t.Errorf("allowed(%q) = %v, want %v", host, got, want)
		}
	}
}

// TestNormalizeHostEntryReducesPinnedPorts pins the entry-level reduction
// rules the deployed allowlist depends on: a numeric port is advisory and is
// dropped, while anything that is not host:port-with-numeric-port survives
// verbatim — in particular a bare IPv6 literal, whose colons must not be
// mistaken for a port separator.
func TestNormalizeHostEntryReducesPinnedPorts(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "bare_host", input: "kubernetes.default.svc", want: "kubernetes.default.svc", ok: true},
		{name: "pinned_port", input: "traefik-iad-ci:8001", want: "traefik-iad-ci", ok: true},
		{name: "url_with_port_uses_hostname", input: "https://traefik-iad-ci:8001", want: "traefik-iad-ci", ok: true},
		{name: "wildcard_survives", input: "*.ardenone.com", want: "*.ardenone.com", ok: true},
		{name: "wildcard_with_port", input: "*.ardenone.com:443", want: "*.ardenone.com", ok: true},
		{name: "trailing_dot", input: "api.z.ai.", want: "api.z.ai", ok: true},
		{name: "uppercase_lowered", input: "API.Z.AI:443", want: "api.z.ai", ok: true},
		{name: "ipv6_literal_untouched", input: "2001:db8::1", want: "2001:db8::1", ok: true},
		{name: "non_numeric_colon_untouched", input: "host:spell", want: "host:spell", ok: true},
		{name: "empty_rejected", input: "   ", want: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeHostEntry(tt.input)
			if ok != tt.ok || got != tt.want {
				t.Errorf("normalizeHostEntry(%q) = (%q, %v), want (%q, %v)", tt.input, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestLintDirectoryAcceptsUpstreamsDeclaredByDeployedAllowlist(t *testing.T) {
	root := t.TempDir()
	// The manifest lives outside the fragments directory: LintDirectory walks
	// the whole tree and would treat an in-tree YAML file as a fragment.
	allowlistDir := t.TempDir()
	allowlist := filepath.Join(allowlistDir, "allowlist.yaml")
	if err := os.WriteFile(allowlist, []byte(deployedUpstreamAllowlist), 0o600); err != nil {
		t.Fatal(err)
	}

	// Each host is written exactly as a fragment declares it: a port-pinned
	// https upstream and one a wildcard entry has to cover.
	writeLintTestFragment(t, root, "k8s", "route.yaml",
		strings.ReplaceAll(validLintFragment("k8s", "v1", "https://traefik-iad-ci:8001"), "  /api:\n", "  /k8s:\n"))
	writeLintTestFragment(t, root, "unifi", "route.yaml",
		strings.ReplaceAll(validLintFragment("unifi", "v1", "https://unifi.ardenone.com"), "  /api:\n", "  /unifi:\n"))
	writeLintTestFragment(t, root, "stranger", "route.yaml",
		strings.ReplaceAll(validLintFragment("stranger", "v1", "https://not-allowed.example.com"), "  /api:\n", "  /stranger:\n"))

	report, err := LintDirectory(LintOptions{
		FragmentsDir:          root,
		SchemaPath:            lintTestSchemaPath(t),
		UpstreamAllowlistPath: allowlist,
	})
	if err != nil {
		t.Fatalf("LintDirectory returned setup error: %v", err)
	}
	if len(report.Errors) != 1 {
		t.Fatalf("expected only the unknown host to fail, got %+v", report.Errors)
	}
	assertFindingCodes(t, report.Errors, []string{"upstream.host-not-allowed"})
}

func TestVaultPathShapeIsBaseAgnostic(t *testing.T) {
	tests := []struct {
		name       string
		vaultPath  string
		wantErrors []string
	}{
		{
			name:      "consolidated cluster-scoped base",
			vaultPath: "rs-manager/rs-manager/seam/routes/guard-test/bearer-token",
		},
		{
			name:      "legacy flat base",
			vaultPath: "seam/routes/guard-test/bearer-token",
		},
		{
			name:      "deep name below the owner",
			vaultPath: "rs-manager/rs-manager/seam/routes/guard-test/prod/api-key",
		},
		{
			name:       "owner absent from the path",
			vaultPath:  "seam/routes/other-owner/token",
			wantErrors: []string{"owner.vault-path-mismatch"},
		},
		{
			name:       "owner as the final segment is not co-ownership",
			vaultPath:  "seam/routes/guard-test",
			wantErrors: []string{"owner.vault-path-mismatch"},
		},
		{
			name:       "no base segment above the owner",
			vaultPath:  "guard-test/token",
			wantErrors: []string{"fragment.schema", "owner.vault-path-mismatch"},
		},
		{
			name:       "traversal stays a schema failure",
			vaultPath:  "rs-manager/rs-manager/seam/routes/../guard-test/token",
			wantErrors: []string{"fragment.schema"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeLintTestFragment(t, root, "guard-test", "route.yaml", vaultPathLintFragment(test.vaultPath))
			report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
			if err != nil {
				t.Fatalf("LintDirectory returned setup error: %v", err)
			}
			if len(test.wantErrors) == 0 {
				if report.HasErrors() {
					t.Fatalf("vault path %q was rejected: %+v", test.vaultPath, report.Errors)
				}
				return
			}
			assertFindingCodes(t, report.Errors, test.wantErrors)
		})
	}
}

// vaultPathLintFragment returns a valid fragment with a credential pair added,
// so a test only names the path under test.
func vaultPathLintFragment(vaultPath string) string {
	return strings.ReplaceAll(
		validLintFragment("guard-test", "v1", "https://api.example.com"),
		"x-seam-owner: guard-test\n",
		"x-seam-owner: guard-test\nx-vault-path: "+vaultPath+"\nx-inject-as:\n  kind: bearer\n")
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
