package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oauthRefreshFragment returns a schema-valid fragment whose default vault
// credential is injected by exchanging the stored refresh token at tokenURL.
// Every other enforcer-checked field is valid, so a quarantine of this
// fragment is attributable to the tokenUrl host alone.
func oauthRefreshFragment(owner, tokenURL string) string {
	return "x-seam-schema: v1\n" +
		"x-seam-owner: " + owner + "\n" +
		"x-api-version: v1\n" +
		"x-upstream: https://upstream.example.com\n" +
		"x-vault-path: seam/routes/" + owner + "/oauth\n" +
		"x-inject-as:\n" +
		"  kind: oauth-refresh\n" +
		"  tokenUrl: " + tokenURL + "\n" +
		"  clientId: seam-gateway\n" +
		"paths:\n" +
		"  /api:\n" +
		"    get:\n" +
		"      responses:\n" +
		"        \"200\":\n" +
		"          description: ok\n"
}

// compositeOAuthRefreshFragment carries the oauth-refresh credential in an
// x-inject-as list entry instead of the legacy object form. A list is paired
// with per-entry source refs rather than a fragment-level x-vault-path.
func compositeOAuthRefreshFragment(owner, tokenURL string) string {
	return "x-seam-schema: v1\n" +
		"x-seam-owner: " + owner + "\n" +
		"x-api-version: v1\n" +
		"x-upstream: https://upstream.example.com\n" +
		"x-inject-as:\n" +
		"  - kind: oauth-refresh\n" +
		"    tokenUrl: " + tokenURL + "\n" +
		"    clientId: seam-gateway\n" +
		"    source: vault\n" +
		"    vaultPath: seam/routes/" + owner + "/oauth\n" +
		"paths:\n" +
		"  /api:\n" +
		"    get:\n" +
		"      responses:\n" +
		"        \"200\":\n" +
		"          description: ok\n"
}

// upstreamMapOAuthRefreshFragment carries the oauth-refresh credential on one
// upstream-map instance, the shape a route uses to inject a different
// credential per upstream. x-upstream-map is mutually exclusive with
// x-upstream and requires x-instance-param naming a parameter present in
// every declared path.
func upstreamMapOAuthRefreshFragment(owner, tokenURL string) string {
	return "x-seam-schema: v1\n" +
		"x-seam-owner: " + owner + "\n" +
		"x-api-version: v1\n" +
		"x-instance-param: cluster\n" +
		"x-upstream-map:\n" +
		"  alt:\n" +
		"    url: https://upstream.example.com\n" +
		"    injectAs:\n" +
		"      kind: oauth-refresh\n" +
		"      tokenUrl: " + tokenURL + "\n" +
		"      clientId: seam-gateway\n" +
		"paths:\n" +
		"  /api/{cluster}:\n" +
		"    get:\n" +
		"      responses:\n" +
		"        \"200\":\n" +
		"          description: ok\n"
}

// validateWithTokenAllowlist loads fragmentYAML from a fresh fragments dir
// under an enforcer whose upstream allowlist admits only the two example hosts,
// then runs schema + allowlist validation. It returns whether the fragment was
// quarantined and its quarantine reasons joined for assertion.
func validateWithTokenAllowlist(t *testing.T, fragmentYAML string) (quarantined bool, reasons string) {
	t.Helper()

	fragmentsDir := t.TempDir()
	writeLintTestFragment(t, fragmentsDir, "spot", "route.yaml", fragmentYAML)

	allowlist := filepath.Join(t.TempDir(), "allowlist.yaml")
	if err := os.WriteFile(allowlist, []byte("hosts:\n  - upstream.example.com\n  - token.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	enforcer, err := NewAllowlistEnforcer("seam/routes", allowlist)
	if err != nil {
		t.Fatalf("NewAllowlistEnforcer() error = %v", err)
	}

	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatalf("NewFragmentLoader() error = %v", err)
	}
	if err := loader.LoadDirectory(fragmentsDir); err != nil {
		t.Fatalf("LoadDirectory() error = %v", err)
	}
	loader.SetAllowlistEnforcer(enforcer)
	if err := loader.ValidateFragments(lintTestSchemaPath(t)); err != nil {
		t.Fatalf("ValidateFragments() error = %v", err)
	}

	q := loader.GetQuarantined()
	if len(q) == 0 {
		return false, ""
	}
	parts := make([]string, 0, len(q))
	for _, fragment := range q {
		parts = append(parts, strings.Join(fragment.QuarantineReasons, "\n"))
	}
	return true, strings.Join(parts, "\n")
}

// The tokenUrl of an oauth-refresh credential receives the stored refresh
// token, so the loader holds its host to the same upstream allowlist as the
// route's traffic upstream — in the legacy object form, in composite list
// entries, and on upstream-map instances.
func TestFragmentLoaderValidatesTokenURLAgainstAllowlist(t *testing.T) {
	t.Run("allowlisted token url is accepted", func(t *testing.T) {
		quarantined, reasons := validateWithTokenAllowlist(t, oauthRefreshFragment("spot", "https://token.example.com/oauth/token"))
		if quarantined {
			t.Fatalf("loader quarantined a fragment whose tokenUrl host is allowlisted: %s", reasons)
		}
	})

	t.Run("off-allowlist token url quarantines the fragment", func(t *testing.T) {
		quarantined, reasons := validateWithTokenAllowlist(t, oauthRefreshFragment("spot", "https://evil.example.com/oauth/token"))
		if !quarantined {
			t.Fatal("loader accepted a fragment whose tokenUrl host is not allowlisted")
		}
		if !strings.Contains(reasons, "token_url_host_validation_failed") {
			t.Fatalf("quarantine reason does not name the tokenUrl host check: %s", reasons)
		}
	})

	t.Run("off-allowlist token url in a composite list entry quarantines", func(t *testing.T) {
		quarantined, reasons := validateWithTokenAllowlist(t, compositeOAuthRefreshFragment("spot", "https://evil.example.com/oauth/token"))
		if !quarantined {
			t.Fatal("loader accepted a composite list whose oauth-refresh tokenUrl host is not allowlisted")
		}
		if !strings.Contains(reasons, "token_url_host_validation_failed") {
			t.Fatalf("quarantine reason does not name the tokenUrl host check: %s", reasons)
		}
	})

	t.Run("off-allowlist token url on an upstream-map instance names the instance", func(t *testing.T) {
		quarantined, reasons := validateWithTokenAllowlist(t, upstreamMapOAuthRefreshFragment("spot", "https://evil.example.com/oauth/token"))
		if !quarantined {
			t.Fatal("loader accepted an upstream map whose oauth-refresh tokenUrl host is not allowlisted")
		}
		if !strings.Contains(reasons, "token_url_host_validation_failed") || !strings.Contains(reasons, `x-upstream-map["alt"]`) {
			t.Fatalf("quarantine reason does not name the tokenUrl check and the offending instance: %s", reasons)
		}
	})
}
