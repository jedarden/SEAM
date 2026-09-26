package corpusboundary

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the package directory to the repository root,
// identified by the go.mod and .gitignore that only the root carries. Tests
// run with the package directory as their working directory, and the check
// must also work from a `git archive` extraction, where the layout is
// identical but no .git directory exists.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if fileExists(filepath.Join(dir, "go.mod")) && fileExists(filepath.Join(dir, ".gitignore")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no repository root (go.mod + .gitignore) above %s", dir)
		}
		dir = parent
	}
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// TestGitignoreAnchorsRuntimeCorpusOnly is the static half of the boundary:
// the root .gitignore must keep the anchored /corpus/ entry, must actually
// ignore paths under corpus/, and must not ignore any guarded fixture or
// package path. It runs everywhere — including a `git archive` extraction
// with no .git directory — so a gitignore edit that would swallow the nested
// corpus package fails before anything is committed.
func TestGitignoreAnchorsRuntimeCorpusOnly(t *testing.T) {
	root := repoRoot(t)
	gitignore := readFile(t, filepath.Join(root, ".gitignore"))
	patterns := ParseGitignore(gitignore)

	found := false
	for _, p := range patterns {
		if !p.negated && strings.TrimSpace(p.source) == RuntimeCorpusIgnorePattern {
			found = true
			break
		}
	}
	if !found {
		t.Errorf(".gitignore carries no anchored %q entry; runtime captures would become commit candidates", RuntimeCorpusIgnorePattern)
	}

	// The entry must do what it says: a capture under corpus/ is ignored.
	if !Ignored(patterns, "corpus/some-service/capture.json") {
		t.Errorf("corpus/some-service/capture.json is not ignored; the %q entry is not doing its job", RuntimeCorpusIgnorePattern)
	}

	// ...and must not do anything else: every guarded checked-in path is
	// outside the ignore.
	for _, guarded := range GuardedPaths {
		if Ignored(patterns, guarded) {
			t.Errorf("%s is ignored by .gitignore (%s); the nested corpus package or a checked-in fixture would silently drop out of commits",
				guarded, strings.Join(HazardousPatterns(patterns, guarded), "; "))
		}
		if !fileExists(filepath.Join(root, filepath.FromSlash(guarded))) {
			t.Errorf("guarded path %s does not exist in the repository", guarded)
		}
	}
}

// TestGitignoreMatcherFlagsBareCorpusPatterns proves the matcher can fail:
// each spelling that would swallow the nested corpus package is flagged, the
// anchored spelling is not, and a negation still rescues a path. Without
// these negative controls the static check above could pass vacuously
// against a matcher that never matches anything.
func TestGitignoreMatcherFlagsBareCorpusPatterns(t *testing.T) {
	const pkg = "tools/diffharness/internal/corpus/corpus.go"

	t.Run("bare corpus patterns are hazards", func(t *testing.T) {
		for _, ignore := range []string{"corpus/\n", "corpus\n", "**/corpus/\n", "build/\ncorpus/\n"} {
			patterns := ParseGitignore([]byte(ignore))
			if !Ignored(patterns, pkg) {
				t.Errorf("gitignore %q does not ignore %s; the matcher missed the bare-corpus hazard", ignore, pkg)
			}
			if hazardous := HazardousPatterns(patterns, pkg); len(hazardous) == 0 {
				t.Errorf("HazardousPatterns named nothing for gitignore %q, though Ignored flagged it", ignore)
			}
		}
	})

	t.Run("anchored entry is not a hazard", func(t *testing.T) {
		patterns := ParseGitignore([]byte(RuntimeCorpusIgnorePattern + "\n"))
		if Ignored(patterns, pkg) {
			t.Errorf("anchored %q ignores %s; it must only cover the repository-root corpus/", RuntimeCorpusIgnorePattern, pkg)
		}
	})

	t.Run("negation rescues a path", func(t *testing.T) {
		patterns := ParseGitignore([]byte("*.log\n!keep.log\n"))
		if !Ignored(patterns, "logs/drop.log") {
			t.Error("logs/drop.log should be ignored by *.log")
		}
		if Ignored(patterns, "logs/keep.log") {
			t.Error("logs/keep.log should be rescued by !keep.log")
		}
	})

	t.Run("existing patterns keep their git semantics", func(t *testing.T) {
		patterns := ParseGitignore([]byte("/seam\nbin/\nlogs/*.log\n**/.beads/traces/\n.env.*.local\n"))
		ignored := []string{"seam", "bin/x", "tools/bin/y", "logs/a.log", "a/.beads/traces/b", ".env.prod.local"}
		for _, p := range ignored {
			if !Ignored(patterns, p) {
				t.Errorf("%s should be ignored", p)
			}
		}
		notIgnored := []string{"cmd/seam/main.go", "logs/a.txt", "internal/server/server.go", ".env.local"}
		for _, p := range notIgnored {
			if Ignored(patterns, p) {
				t.Errorf("%s should not be ignored", p)
			}
		}
	})
}

// gitArgs runs git in the repository root and returns its exit status.
func gitRun(t *testing.T, root string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), string(out)
	}
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return 0, string(out)
}

// TestRuntimeCorpusStaysOutOfGit is the git-backed half of the boundary: in
// a real checkout nothing under corpus/ may be tracked (no accidental
// promotion via a blanket add), while the guarded package and fixture paths
// stay tracked and outside every ignore rule — including for names that do
// not exist yet, so a future file under either tree inherits the same
// protection. It skips outside a git repository (a `git archive` extraction
// has no .git and nothing tracked), where TestGitignoreAnchorsRuntimeCorpusOnly
// carries the guarantee alone.
func TestRuntimeCorpusStaysOutOfGit(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Skip("no .git directory: `git archive` extraction, static checks only")
	}
	if code, _ := gitRun(t, root, "rev-parse", "--verify", "-q", "HEAD"); code != 0 {
		t.Skip("repository has no commits yet: nothing can be tracked or ignored")
	}

	if code, out := gitRun(t, root, "ls-files", "--", "corpus"); code != 0 || strings.TrimSpace(out) != "" {
		t.Errorf("git ls-files corpus = %q (exit %d); runtime captures must never be tracked — promote a capture by moving it to tools/diffharness/testdata instead", strings.TrimSpace(out), code)
	}
	if code, _ := gitRun(t, root, "check-ignore", "-q", "--", "corpus"); code != 0 {
		t.Errorf("git check-ignore corpus exit = %d; the repository-root corpus/ directory must stay ignored", code)
	}
	if code, _ := gitRun(t, root, "check-ignore", "-q", "--", "corpus/probe.json"); code != 0 {
		t.Errorf("git check-ignore corpus/probe.json exit = %d; paths under corpus/ must stay ignored", code)
	}

	// A name that does not exist yet proves the *rules* protect the trees,
	// not just today's files: if the ignore ever widens to a bare corpus/
	// pattern, this probe is reported ignored and the check fails.
	probe := "tools/diffharness/internal/corpus/boundary-probe.go"
	if code, out := gitRun(t, root, "check-ignore", "-q", "--", probe); code == 0 {
		t.Errorf("git check-ignore %s reports it ignored:\n%s", probe, out)
	}
	probeFixture := "tools/diffharness/testdata/boundary-probe.json"
	if code, out := gitRun(t, root, "check-ignore", "-q", "--", probeFixture); code == 0 {
		t.Errorf("git check-ignore %s reports it ignored:\n%s", probeFixture, out)
	}

	code, out := gitRun(t, root, append([]string{"ls-files", "--"}, GuardedPaths...)...)
	if code != 0 {
		t.Fatalf("git ls-files guarded paths exit = %d: %s", code, out)
	}
	tracked := strings.TrimSpace(out)
	for _, guarded := range GuardedPaths {
		if !strings.Contains(tracked, guarded) {
			t.Errorf("guarded path %s is not tracked in git; the fixture boundary has been broken", guarded)
		}
	}
}

// TestFixtureValidationStaysWired pins the other half of the boundary: the
// checked-in fixtures keep receiving their validation. The diffharness
// module is standalone, so the root `go test ./...` sweep never descends
// into it — the validation reaches the fixtures only through the
// definition-of-done lane, and the fixtures only exist if nobody deletes
// them. docs/capture_testing.md previously recorded the fixture checks as
// wired into neither gate; this test keeps that from silently regressing.
func TestFixtureValidationStaysWired(t *testing.T) {
	root := repoRoot(t)

	dod := string(readFile(t, filepath.Join(root, "scripts", "definition-of-done.sh")))
	if !strings.Contains(dod, FixtureValidationLane) {
		t.Errorf("scripts/definition-of-done.sh no longer carries the %q lane; tools/diffharness/testdata fixtures would lose schema, uniqueness, canonicalization, and vault-reference validation", FixtureValidationLane)
	}
	if !strings.Contains(dod, "go test ./internal/corpus/") {
		t.Error("scripts/definition-of-done.sh no longer runs the diffharness corpus package; that package is the validator for the checked-in fixtures")
	}

	nested := string(readFile(t, filepath.Join(root, "tools", "diffharness", "internal", "corpus", "corpus_test.go")))
	if !strings.Contains(nested, "TestCheckedInFixturesResolveUnderEnforcedVaultBase") {
		t.Error("tools/diffharness/internal/corpus/corpus_test.go no longer loads the checked-in fixtures (TestCheckedInFixturesResolveUnderEnforcedVaultBase is gone); the fixture validation coverage has been dropped")
	}
}
