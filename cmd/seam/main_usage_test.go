package main

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestUsageListsEveryDispatchedCommand pins the help smoke contract: the
// top-level usage output names every command the dispatch table can run,
// with its summary. seamCommands is the single source of truth for both the
// usage text and dispatch, so the assertion is that the rendered text never
// falls behind the table — a command added to the table without a usable
// summary, or a hand-written usage line that omits one, fails here.
func TestUsageListsEveryDispatchedCommand(t *testing.T) {
	if len(seamCommands) == 0 {
		t.Fatal("seamCommands is empty; the CLI has no commands")
	}
	var out bytes.Buffer
	writeUsage(&out)
	rendered := out.String()

	if !strings.HasPrefix(rendered, "Usage: seam <command> [<args>]\n") {
		t.Fatalf("usage does not open with the documented usage line: %q", rendered)
	}
	for _, command := range seamCommands {
		if !strings.Contains(rendered, "  "+command.name) {
			t.Errorf("usage omits command %q:\n%s", command.name, rendered)
			continue
		}
		if !strings.Contains(rendered, command.summary) {
			t.Errorf("usage omits %q summary %q:\n%s", command.name, command.summary, rendered)
		}
	}
}

// TestUsageKeepsDocumentedAlignment pins the exact historical rendering of
// the listing — two spaces of indent and a 16-character name column. The
// container HEALTHCHECK path reasons over these lines staying stable, and
// README documents `seam <command>` shapes that match them.
func TestUsageKeepsDocumentedAlignment(t *testing.T) {
	var out bytes.Buffer
	writeUsage(&out)
	for _, want := range []string{
		"  serve            Start the SEAM gateway server\n",
		"  healthcheck      Probe the caller-facing liveness endpoint\n",
		"  lint             Validate SEAM route fragments\n",
		"  diff             Show differences between fragment versions\n",
		"  import           Import fragments into SEAM\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage line drifted from the documented rendering, want %q:\n%s", want, out.String())
		}
	}
}

// TestUnknownCommandNamesTheValidCommands pins the unknown-command
// rejection: it echoes the offending token and then names every command the
// CLI actually dispatches, so a mistyped subcommand self-corrects.
func TestUnknownCommandNamesTheValidCommands(t *testing.T) {
	var out bytes.Buffer
	writeUnknownCommand(&out, "srve")
	rendered := out.String()
	if !strings.HasPrefix(rendered, "Unknown command: srve\n") {
		t.Fatalf("unknown-command output does not echo the offending token: %q", rendered)
	}
	for _, command := range seamCommands {
		if !strings.Contains(rendered, command.name) {
			t.Errorf("unknown-command output omits valid command %q:\n%s", command.name, rendered)
		}
	}
}

// seamFixturePath resolves a path under this package's checked-in testdata.
// The fixture fragments are real files rather than the inline strings the
// other tests build, so they can be linted by hand exactly as the README
// documents (`seam lint cmd/seam/testdata/fixture-fragments`).
func seamFixturePath(t *testing.T, rel string) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(sourceFile), "testdata", rel)
}

// TestLintCheckedInFixtureSmoke is the fixture smoke test for the documented
// lint workflow: the minimal checked-in fragment lints clean (exit 0, zero
// errors, zero warnings) against the repository's real route-fragment
// schema, from a directory layout that follows the x-seam-owner-matches-parent
// rule the import command tells curators to keep.
func TestLintCheckedInFixtureSmoke(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runLintCommand([]string{
		seamFixturePath(t, "fixture-fragments"),
		"--schema", lintCommandTestSchemaPath(t),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("fixture lint returned %d, want 0:\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "seam lint: passed (1 file(s), 0 error(s), 0 warning(s))") {
		t.Fatalf("expected a fully clean fixture lint, got:\n%s", stdout.String())
	}
}

// TestLintBrokenFixtureExitsOne pins the failure half of the documented exit
// contract on a checked-in fixture: a fragment whose x-seam-owner does not
// match its parent directory is a structural error, so lint exits 1 and
// reports an ERROR finding.
func TestLintBrokenFixtureExitsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runLintCommand([]string{
		seamFixturePath(t, filepath.Join("broken-fixture-fragments", "fixture-owner", "fragment.yaml")),
		"--schema", lintCommandTestSchemaPath(t),
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("broken fixture lint returned %d, want 1:\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "ERROR [") {
		t.Fatalf("broken fixture produced no ERROR finding:\n%s", stdout.String())
	}
}
