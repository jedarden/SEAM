// Package docexamples_test gates the checked-in documentation example trees
// on the same validation the gateway applies to production fragments. The
// examples under docs/examples and examples teach the fragment grammar by
// hand, and until this gate existed nothing re-checked them: the grammar
// moved to unit-bearing x-cost-per-call objects, amount/unit/window x-quota,
// and maxRepeats/window x-loop-guard while the docs still taught bare
// numbers, limit/window_seconds, and max_iterations/backoff_ms. This test is
// the CI hook that fails on the next drift instead of on the next reader.
package docexamples

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	seamspec "github.com/ardenone/seam/internal/spec"
)

// ownerDirectoryPlacement is the one lint code exempted here, and only for
// documentation copies.
const ownerDirectoryPlacement = "owner.directory-mismatch"

// TestCheckedInDocumentationExamplesStayLintClean parses every YAML/JSON file
// in each documentation example tree, validates it against the repo's
// route-fragment schema, and runs the full seam lint rule set over it — the
// same LintDirectory entry point the seam-lint CI lane runs against
// fragments/argocd-ro. A stale extension shape, a broken envelope, or an
// x-vault-path that stops nesting its x-seam-owner fails the build here.
func TestCheckedInDocumentationExamplesStayLintClean(t *testing.T) {
	for _, tree := range documentationTrees() {
		t.Run(tree.name, func(t *testing.T) {
			checkExampleTree(t, tree.name, tree.dir)
		})
	}
}

// documentationTrees returns the checked-in documentation estates this gate
// guards. Both gates below — lint cleanliness and credential-reference
// hygiene — must walk exactly these trees, so the list lives in one place.
func documentationTrees() []struct {
	name string
	dir  string
} {
	return []struct {
		name string
		dir  string
	}{
		{name: "docs/examples", dir: "."},
		{name: "examples", dir: filepath.Join("..", "..", "examples")},
	}
}

func checkExampleTree(t *testing.T, name, dir string) {
	t.Helper()

	want := fragmentFileCount(t, dir)
	if want == 0 {
		t.Fatalf("no .json/.yaml/.yml files under %s - the documentation tree is missing", name)
	}

	report, err := seamspec.LintDirectory(seamspec.LintOptions{
		FragmentsDir: dir,
		SchemaPath:   filepath.Join("..", "..", "spec", "route-fragment-schema.json"),
	})
	if err != nil {
		t.Fatalf("LintDirectory(%s): %v", name, err)
	}
	if report.Files != want {
		t.Fatalf("linted %d files but found %d under %s - the lint walk and the tree disagree", report.Files, want, name)
	}

	for _, finding := range report.Errors {
		if finding.Code == ownerDirectoryPlacement {
			// The placement rule encodes the production fragments-tree
			// convention: a fragment lives at fragments/<owner>/<file>,
			// so its parent directory must equal x-seam-owner.
			// Documentation copies live under a documentation topic
			// instead, so that half of the owner chain cannot apply.
			// The staleness half still does — owner.vault-path-mismatch
			// fires when an example's x-vault-path stops nesting its
			// x-seam-owner, and that code is not exempted.
			continue
		}
		t.Errorf("%s: [%s] %s", finding.File, finding.Code, finding.Message)
	}
	for _, finding := range report.Warnings {
		t.Logf("warning %s: [%s] %s", finding.File, finding.Code, finding.Message)
	}
}

// fragmentFileCount independently enumerates the tree so a silent walk change
// (a renamed directory, a new extension) cannot turn this gate into a no-op
// that linted zero files and reported success.
func fragmentFileCount(t *testing.T, dir string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".json", ".yaml", ".yml":
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return count
}

// credentialFieldKeys are the fragment-root and upstream-map-entry fields
// that name a credential. docs/notes/credential-reference-syntax.md defines
// the fragment surface's one legal shape: a scheme-less bare path.
var credentialFieldKeys = map[string]bool{
	"x-vault-path": true, // fragment root
	"vaultPath":    true, // x-upstream-map entry
}

// injectFieldKeys are the fields that carry injection metadata. The schema
// closes them to kind/name, so anything else — above all a value — is a
// literal credential smuggled past the pair.
var injectFieldKeys = map[string]bool{
	"x-inject-as": true, // fragment root
	"injectAs":    true, // x-upstream-map entry
}

// literalCredentialPatterns detect a credential *value* rather than a
// reference: provider-token prefixes with provider-specific payload shapes,
// a JWT's three base64url segments, and a raw alphanumeric run long enough
// that no path segment or prose word collides with it. All three are
// verified absent from the current trees; a new example that pastes a real
// token fails here instead of shipping in documentation.
var literalCredentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-[A-Za-z0-9]{16,}`),                                           // OpenAI-style
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),                                    // GitHub
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),                                  // GitHub fine-grained
	regexp.MustCompile(`\bxox[abpsr]-[A-Za-z0-9-]{10,}`),                                  // Slack
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}`),                                      // GitLab
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                            // AWS access key
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),                                       // Google API key
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), // JWT
	regexp.MustCompile(`\b[A-Za-z0-9]{32,}\b`),                                            // raw high-entropy run
}

// TestDocumentationCredentialFieldsStayReferences pins the
// credential-reference boundary (docs/notes/credential-reference-syntax.md)
// on the documentation estate. The lint gate above validates the fragment
// grammar; this walk validates what no schema reaches: that every credential
// field across both trees still resolves to a scheme-less path reference,
// that injection metadata never grows a literal value field, and that no
// prose, description, or example payload string anywhere carries a
// literal-shaped credential. Docs teach by copy-paste; a token pasted into
// an example is a leaked credential, and a path rewritten into the corpus's
// vault:-schemed form is a shape the runtime refuses at load.
func TestDocumentationCredentialFieldsStayReferences(t *testing.T) {
	for _, tree := range documentationTrees() {
		t.Run(tree.name, func(t *testing.T) {
			checkCredentialReferences(t, tree.name, tree.dir)
		})
	}
}

func checkCredentialReferences(t *testing.T, name, dir string) {
	t.Helper()

	files := 0
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".json", ".yaml", ".yml":
		default:
			return nil
		}
		files++

		contents, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: read: %v", path, err)
			return nil
		}
		var decoded any
		if err := yaml.Unmarshal(contents, &decoded); err != nil {
			t.Errorf("%s: parse: %v", path, err)
			return nil
		}
		normalized, err := normalizeDocValue(decoded)
		if err != nil {
			t.Errorf("%s: normalize: %v", path, err)
			return nil
		}
		scanCredentialStrings(t, path, "", "", normalized)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", name, err)
	}
	if files == 0 {
		t.Fatalf("no .json/.yaml/.yml files under %s - the documentation tree is missing", name)
	}
}

// scanCredentialStrings walks one parsed document, carrying the enclosing
// key so credential-naming fields can be held to the reference shape while
// every other string is swept for literal credential shapes.
func scanCredentialStrings(t *testing.T, file, path, key string, value any) {
	t.Helper()

	switch {
	case credentialFieldKeys[key]:
		text, ok := value.(string)
		if !ok {
			t.Errorf("%s: %s: credential field must be a path string, got %T", file, fieldPath(path, key), value)
			return
		}
		if strings.HasPrefix(text, "vault:") {
			t.Errorf("%s: %s: carries the vault: scheme - that form belongs to the differential corpus's secret refs, the fragment surface is the bare path (docs/notes/credential-reference-syntax.md): %q", file, fieldPath(path, key), text)
		}
		segments := strings.Split(text, "/")
		if len(segments) < 3 || containsEmpty(segments) {
			t.Errorf("%s: %s: is not a <base>/<owner>/<name> path reference - a pasted literal cannot name a vault KV path: %q", file, fieldPath(path, key), text)
		}
		return

	case injectFieldKeys[key]:
		fields, ok := value.(map[string]any)
		if !ok {
			t.Errorf("%s: %s: injection metadata must be an object, got %T", file, fieldPath(path, key), value)
			return
		}
		for field := range fields {
			switch field {
			case "kind", "name":
				// The only members the schema allows: where and how to
				// inject, never what.
			default:
				t.Errorf("%s: %s: injection metadata carries %q - a literal credential value must never ride beside kind/name", file, fieldPath(path, key), field)
			}
		}
		return
	}

	switch value := value.(type) {
	case map[string]any:
		for childKey, child := range value {
			scanCredentialStrings(t, file, fieldPath(path, key), childKey, child)
		}
	case []any:
		for _, child := range value {
			scanCredentialStrings(t, file, path, key, child)
		}
	case string:
		if key == "" {
			return
		}
		for _, pattern := range literalCredentialPatterns {
			if match := pattern.FindString(value); match != "" {
				t.Errorf("%s: %s: string for %q contains a literal-shaped credential %q - replace it with a reference", file, fieldPath(path, key), key, match)
				return
			}
		}
	}
}

// fieldPath renders a JSON-pointer-style location for error messages,
// skipping the empty document root.
func fieldPath(path, key string) string {
	if path == "" {
		return "/" + key
	}
	if key == "" {
		return path
	}
	return path + "/" + key
}

func containsEmpty(segments []string) bool {
	for _, segment := range segments {
		if segment == "" {
			return true
		}
	}
	return false
}

// normalizeDocValue makes yaml.v3's map[any]any representation safe for the
// string walk, mirroring internal/spec's lint normalizer.
func normalizeDocValue(value any) (any, error) {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			normalized, err := normalizeDocValue(child)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case map[any]any:
		result := make(map[string]any, len(value))
		for rawKey, child := range value {
			key, ok := rawKey.(string)
			if !ok {
				return nil, fs.ErrInvalid
			}
			normalized, err := normalizeDocValue(child)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(value))
		for i, child := range value {
			normalized, err := normalizeDocValue(child)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}
