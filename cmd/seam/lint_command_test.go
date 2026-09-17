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

// lint applies SEAM_FRAGMENTS_DIR, SEAM_SCHEMA_PATH and SEAM_UPSTREAM_ALLOWLIST
// with the opposite precedence to serve: the environment only fills a flag
// that is still at its default, so an explicit flag wins. (serve lets the
// environment beat any flag — the Deployment is its operator's configuration
// surface; lint and diff are developer tools where a typed flag is the more
// specific intent.) The corollary is a sentinel limitation: passing the
// default value explicitly, e.g. --fragments-dir ./fragments, is
// indistinguishable from omitting the flag, and the environment fills it.
//
// The tests below drive runLintCommand through the real os environment with
// t.Setenv, and read precedence off the exit code: a rejected fragments dir
// fails lint with 1, a failed schema load exits 2, and a clean lint exits 0.

func clearLintEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SEAM_FRAGMENTS_DIR", "")
	t.Setenv("SEAM_SCHEMA_PATH", "")
	t.Setenv("SEAM_UPSTREAM_ALLOWLIST", "")
}

func writeLintFixtureDir(t *testing.T, fragment string) string {
	t.Helper()
	dir := t.TempDir()
	ownerDir := filepath.Join(dir, "owner")
	if err := os.MkdirAll(ownerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownerDir, "route.yaml"), []byte(fragment), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLintEnvSuppliesDefaultFragmentsDir(t *testing.T) {
	clearLintEnv(t)
	envDir := writeLintFixtureDir(t, `x-seam-schema: v1
x-seam-owner: owner
x-api-version: v1
x-upstream: https://api.example.com
paths:
  /config/status:
    get:
      responses:
        "200":
          description: no
`)
	t.Setenv("SEAM_FRAGMENTS_DIR", envDir)

	var stdout, stderr bytes.Buffer
	code := runLintCommand([]string{"--schema", lintCommandTestSchemaPath(t)}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("lint with env fragments dir returned %d, want 1 (the reserved-path finding): stdout=%s stderr=%s",
			code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "path.reserved") {
		t.Fatalf("lint did not report a reserved-path finding: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), envDir+string(filepath.Separator)) {
		t.Fatalf("lint did not report against the env dir %s: %s", envDir, stdout.String())
	}
}

func TestLintExplicitFlagBeatsEnvFragmentsDir(t *testing.T) {
	clearLintEnv(t)
	t.Setenv("SEAM_FRAGMENTS_DIR", writeLintFixtureDir(t, `x-seam-schema: v1
x-seam-owner: owner
x-api-version: v1
x-upstream: https://api.example.com
paths:
  /config/status:
    get:
      responses:
        "200":
          description: no
`))
	validDir := writeLintFixtureDir(t, `x-seam-schema: v1
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
`)

	var stdout, stderr bytes.Buffer
	code := runLintCommand([]string{
		"--fragments-dir", validDir,
		"--schema", lintCommandTestSchemaPath(t),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("flagged fragments dir lost to the environment: code=%d stdout=%s stderr=%s",
			code, stdout.String(), stderr.String())
	}
}

func TestLintEnvSuppliesDefaultSchemaPath(t *testing.T) {
	clearLintEnv(t)
	validDir := writeLintFixtureDir(t, `x-seam-schema: v1
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
`)
	t.Setenv("SEAM_SCHEMA_PATH", filepath.Join(t.TempDir(), "missing-schema.json"))

	var stdout, stderr bytes.Buffer
	code := runLintCommand([]string{"--fragments-dir", validDir}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("lint with missing env schema returned %d, want 2 (schema load failure): stdout=%s stderr=%s",
			code, stdout.String(), stderr.String())
	}
}

func TestLintExplicitFlagBeatsEnvSchemaPath(t *testing.T) {
	clearLintEnv(t)
	validDir := writeLintFixtureDir(t, `x-seam-schema: v1
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
`)
	t.Setenv("SEAM_SCHEMA_PATH", filepath.Join(t.TempDir(), "missing-schema.json"))

	var stdout, stderr bytes.Buffer
	code := runLintCommand([]string{
		"--fragments-dir", validDir,
		"--schema-path", lintCommandTestSchemaPath(t),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("flagged schema path lost to the environment: code=%d stdout=%s stderr=%s",
			code, stdout.String(), stderr.String())
	}
}
