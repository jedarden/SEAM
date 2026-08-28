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
