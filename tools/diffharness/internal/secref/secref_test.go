package secref

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests in this package pin the replay-time half of the
// credential-reference boundary (docs/notes/credential-reference-syntax.md):
// a corpus Secret.Ref — always vault:-schemed — resolves to a value in
// memory, file leg over env leg, and the ref→env-var serialization the env
// leg is built on. The ref-shape and base-containment rules themselves are
// the corpus loader's job, not the resolver's.

func TestResolveFileLegOverEnvLeg(t *testing.T) {
	t.Setenv("SEAM_DIFF_SECRET_VAULT_SEAM_ROUTES_ARGOCD_RO_TOKEN", "from-env")
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.local.json")
	// The file holds the same ref as the env var above — file must win —
	// plus a ref no env var carries.
	content := `{
		"vault:seam/routes/argocd-ro/ro-token": "from-file",
		"vault:seam/routes/argocd-ro/file-only-token": "file-only"
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write secrets file: %v", err)
	}
	r, err := NewResolver(path)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	if v, ok := r.Resolve("vault:seam/routes/argocd-ro/ro-token"); !ok || v != "from-file" {
		t.Errorf("file leg must win over env leg: got %q, ok=%v", v, ok)
	}
	if v, ok := r.Resolve("vault:seam/routes/argocd-ro/file-only-token"); !ok || v != "file-only" {
		t.Errorf("file-only ref: got %q, ok=%v", v, ok)
	}
}

func TestResolveEnvLegFallback(t *testing.T) {
	t.Setenv("SEAM_DIFF_SECRET_VAULT_SEAM_ROUTES_KALSHI_API_KEY", "env-value")
	r, err := NewResolver("") // no file: env-only resolver
	if err != nil {
		t.Fatalf("NewResolver(\"\"): %v", err)
	}
	v, ok := r.Resolve("vault:seam/routes/kalshi/api-key")
	if !ok || v != "env-value" {
		t.Errorf("env leg fallback: got %q, ok=%v, want env-value", v, ok)
	}
}

func TestResolveUnresolvedRefIsNotAnError(t *testing.T) {
	r, err := NewResolver("")
	if err != nil {
		t.Fatalf("NewResolver(\"\"): %v", err)
	}
	if v, ok := r.Resolve("vault:seam/routes/nobody/configured/this"); ok {
		t.Errorf("unresolved ref returned ok=true with %q; an unresolved ref is a SKIP, not a failure", v)
	}
}

func TestNewResolverErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		if _, err := NewResolver(filepath.Join(t.TempDir(), "absent.json")); err == nil {
			t.Error("expected error for a missing secrets file, got nil")
		}
	})
	t.Run("malformed JSON", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "broken.json")
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatalf("write broken secrets file: %v", err)
		}
		if _, err := NewResolver(path); err == nil {
			t.Error("expected error for a malformed secrets file, got nil")
		}
	})
}

// TestEnvNameMapping pins the ref→env-var serialization the README documents:
// uppercase, every run of non-[A-Z0-9_] collapsed to a single underscore,
// prefixed SEAM_DIFF_SECRET_. The mapping is derived from the ref string
// alone, so it is mechanical for any base the ref carries.
func TestEnvNameMapping(t *testing.T) {
	tests := []struct {
		ref  string
		want string
	}{
		{
			ref:  "vault:seam/routes/argocd/ro-token",
			want: "SEAM_DIFF_SECRET_VAULT_SEAM_ROUTES_ARGOCD_RO_TOKEN",
		},
		{
			// Consolidated-estate base: same mechanical mapping.
			ref:  "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token",
			want: "SEAM_DIFF_SECRET_VAULT_RS_MANAGER_RS_MANAGER_SEAM_ROUTES_ARGOCD_RO_RO_TOKEN",
		},
		{
			// Slashes, dots and colons all collapse; runs never double up.
			ref:  "vault:a//b...c",
			want: "SEAM_DIFF_SECRET_VAULT_A_B_C",
		},
		{
			ref:  "",
			want: "SEAM_DIFF_SECRET_",
		},
	}
	for _, tt := range tests {
		if got := envName(tt.ref); got != tt.want {
			t.Errorf("envName(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

// TestEnvNameCollisionIsTheDocumentedAmbiguity pins the env leg's known
// non-injectivity: sanitization folds /, _ and - to the same underscore, so
// distinct refs can derive one variable. The boundary answer is that the env
// leg is a convenience and the file leg — keyed by the exact ref string — is
// the disambiguator (docs/notes/credential-reference-syntax.md,
// "Known non-injectivity of the env leg"). If this test ever fails, the
// mapping changed and that doc section must be revisited with it.
func TestEnvNameCollisionIsTheDocumentedAmbiguity(t *testing.T) {
	colliding := []string{
		"vault:tenant/routes/a-b",
		"vault:tenant/routes/a_b",
		"vault:tenant/routes/a/b",
	}
	names := make(map[string][]string, len(colliding))
	for _, ref := range colliding {
		names[envName(ref)] = append(names[envName(ref)], ref)
	}
	if len(names) != 1 {
		t.Fatalf("expected the documented refs to collide on one env var, got %d distinct names", len(names))
	}
	for name, refs := range names {
		if !strings.HasPrefix(name, "SEAM_DIFF_SECRET_") {
			t.Errorf("collided name %q lost the SEAM_DIFF_SECRET_ prefix", name)
		}
		if len(refs) != len(colliding) {
			t.Errorf("env name %q covers %d refs, want %d", name, len(refs), len(colliding))
		}
	}
}
