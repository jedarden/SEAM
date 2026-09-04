package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRunDiffCommandDetectsChanges(t *testing.T) {
	// Create temporary directories for base and current
	baseDir := t.TempDir()
	currentDir := t.TempDir()

	// Create base fragment
	baseOwnerDir := filepath.Join(baseDir, "owner")
	if err := os.MkdirAll(baseOwnerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	baseFragment := `x-seam-schema: v1
x-seam-owner: owner
paths:
  /api/test:
    get:
      summary: Test endpoint
`
	if err := os.WriteFile(filepath.Join(baseOwnerDir, "route.yaml"), []byte(baseFragment), 0o600); err != nil {
		t.Fatal(err)
	}

	// Create current fragment with change
	currentOwnerDir := filepath.Join(currentDir, "owner")
	if err := os.MkdirAll(currentOwnerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	currentFragment := `x-seam-schema: v1
x-seam-owner: owner
paths:
  /api/test:
    get:
      summary: Modified test endpoint
  /api/new:
    get:
      summary: New endpoint
`
	if err := os.WriteFile(filepath.Join(currentOwnerDir, "route.yaml"), []byte(currentFragment), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runDiffCommand([]string{
		"--fragments-dir", currentDir,
		"--base", baseDir,
		"--json",
	}, &stdout, &stderr)

	// Should return 1 (changes detected)
	if code != 1 {
		t.Errorf("Expected return code 1 for changes, got %d: stderr=%s", code, stderr.String())
	}

	// Should contain JSON output
	if !bytes.Contains(stdout.Bytes(), []byte(`"paths_added"`)) {
		t.Errorf("Expected JSON output with paths_added, got: %s", stdout.String())
	}
}

func TestRunDiffCommandNoChanges(t *testing.T) {
	dir := t.TempDir()

	ownerDir := filepath.Join(dir, "owner")
	if err := os.MkdirAll(ownerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fragment := `x-seam-schema: v1
x-seam-owner: owner
paths:
  /api/test:
    get:
      summary: Test endpoint
`
	if err := os.WriteFile(filepath.Join(ownerDir, "route.yaml"), []byte(fragment), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runDiffCommand([]string{
		"--fragments-dir", dir,
		"--base", dir,
	}, &stdout, &stderr)

	// Should return 0 (no changes)
	if code != 0 {
		t.Errorf("Expected return code 0 for no changes, got %d: stderr=%s", code, stderr.String())
	}
}

func TestRunDiffCommandMissingBase(t *testing.T) {
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := runDiffCommand([]string{
		"--fragments-dir", dir,
	}, &stdout, &stderr)

	// Should return error when no base directory and no git repo
	if code != 2 {
		t.Errorf("Expected return code 2 for missing base, got %d: stderr=%s", code, stderr.String())
	}
}

// A fragments directory that yields no fragments must not read as a diff that
// found no changes: both sides would merge to the same minimal empty document
// and the command would exit 0 for what is usually a mistyped path.
func TestRunDiffCommandRejectsEmptyCurrentFragments(t *testing.T) {
	for name, currentDir := range map[string]string{
		"missing directory":  filepath.Join(t.TempDir(), "does-not-exist"),
		"no fragment files":  newDirWithNonFragmentFiles(t),
		"empty nested owner": newDirWithEmptyOwnerDir(t),
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runDiffCommand([]string{
				"--fragments-dir", currentDir,
				"--base", currentDir,
			}, &stdout, &stderr)

			if code != 2 {
				t.Errorf("Expected return code 2 for a fragments dir with no fragments, got %d: stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}

			// The failure must be on stderr; stdout stays empty so a caller
			// cannot mistake an empty report for a successful no-change diff.
			if stdout.Len() != 0 {
				t.Errorf("Expected no stdout for a failed diff, got: %s", stdout.String())
			}
			if !bytes.Contains(stderr.Bytes(), []byte("no fragments loaded")) {
				t.Errorf("Expected a 'no fragments loaded' diagnostic, got: %s", stderr.String())
			}
		})
	}
}

// An empty base is not an error, but it must be called out: it reports every
// current path as added.
func TestRunDiffCommandWarnsOnEmptyBase(t *testing.T) {
	currentDir := t.TempDir()
	ownerDir := filepath.Join(currentDir, "owner")
	if err := os.MkdirAll(ownerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fragment := `x-seam-schema: v1
x-seam-owner: owner
paths:
  /api/test:
    get:
      summary: Test endpoint
`
	if err := os.WriteFile(filepath.Join(ownerDir, "route.yaml"), []byte(fragment), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runDiffCommand([]string{
		"--fragments-dir", currentDir,
		"--base", filepath.Join(t.TempDir(), "does-not-exist"),
	}, &stdout, &stderr)

	if code != 1 {
		t.Errorf("Expected return code 1 for an added path, got %d: stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("no fragments loaded from base")) {
		t.Errorf("Expected a warning about the empty base, got: %s", stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("/api/test")) {
		t.Errorf("Expected the added path in the diff, got: %s", stdout.String())
	}
}

func newDirWithNonFragmentFiles(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ownerDir := filepath.Join(dir, "owner")
	if err := os.MkdirAll(ownerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownerDir, "README.md"), []byte("not a fragment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newDirWithEmptyOwnerDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "owner"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
