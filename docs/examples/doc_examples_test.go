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
	"path/filepath"
	"strings"
	"testing"

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
	for _, tree := range []struct {
		name string
		dir  string
	}{
		{name: "docs/examples", dir: "."},
		{name: "examples", dir: filepath.Join("..", "..", "examples")},
	} {
		t.Run(tree.name, func(t *testing.T) {
			checkExampleTree(t, tree.name, tree.dir)
		})
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
