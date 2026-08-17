package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunLintCommandReportsWarningsWithoutFailing(t *testing.T) {
	root := t.TempDir()
	ownerDir := filepath.Join(root, "owner")
	if err := os.MkdirAll(ownerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fragment := `x-seam-schema: v1
x-seam-owner: owner
x-api-version: v1
x-upstream: https://api.example.com
x-upstream-plaintext: acknowledged
paths:
  /api:
    get:
      responses:
        "200":
          description: ok
`
	if err := os.WriteFile(filepath.Join(ownerDir, "route.yaml"), []byte(fragment), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runLintCommand([]string{
		"--fragments-dir", root,
		"--schema", lintCommandTestSchemaPath(t),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("warning-only lint returned %d: stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "WARNING [transport.plaintext]") {
		t.Fatalf("warning was not rendered: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "seam lint: passed") {
		t.Fatalf("summary was not rendered: %s", stdout.String())
	}
}

func TestRunLintCommandReturnsFailureForReservedPath(t *testing.T) {
	root := t.TempDir()
	ownerDir := filepath.Join(root, "owner")
	if err := os.MkdirAll(ownerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fragment := `x-seam-schema: v1
x-seam-owner: owner
x-api-version: v1
x-upstream: https://api.example.com
paths:
  /config/status:
    get:
      responses:
        "200":
          description: no
`
	if err := os.WriteFile(filepath.Join(ownerDir, "route.yaml"), []byte(fragment), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runLintCommand([]string{
		filepath.Join(ownerDir, "route.yaml"),
		"--schema-path", lintCommandTestSchemaPath(t),
		"--json",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("reserved-path lint returned %d, want 1: stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"path.reserved"`) {
		t.Fatalf("JSON report omitted reserved-path finding: %s", stdout.String())
	}
}

func lintCommandTestSchemaPath(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(sourceFile), "..", "..", "spec", "route-fragment-schema.json")
}
