package pointerguard

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the package directory to the repository root,
// identified by the go.mod and .gitignore that only the root carries. Tests
// run with the package directory as their working directory, and the checks
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

// inGitCheckout reports whether root is a real checkout, as opposed to a
// `git archive` extraction. The two layouts carry identical trees, but only a
// checkout can enumerate tracked files, so the repo-backed checks branch on
// it: a checkout is checked against git's own view, an extraction against the
// extracted tree.
func inGitCheckout(root string) bool {
	info, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}

func gitRun(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

// dotDirFree reports whether a repository-relative path stays outside
// dot-directories (.git, .beads, …). The doc scan applies it in both
// enumeration modes so a checkout's tracked-file list and an extraction's
// filesystem walk see the same document set.
func dotDirFree(relPath string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(relPath), "/") {
		if strings.HasPrefix(seg, ".") && seg != "." && seg != ".." {
			return false
		}
	}
	return true
}

// repoDocs returns the repository's documentation files, repository-relative.
// In a checkout that is exactly the tracked *.md files outside dot
// directories; in an extraction (no git), the same filter applied to the
// extracted tree.
func repoDocs(t *testing.T, root string) []string {
	t.Helper()
	var docs []string
	if inGitCheckout(root) {
		for _, p := range strings.Split(gitRun(t, root, "ls-files", "*.md"), "\n") {
			if p != "" && dotDirFree(p) {
				docs = append(docs, p)
			}
		}
		return docs
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			if rel != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, ".md") && dotDirFree(rel) {
			docs = append(docs, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk extraction: %v", err)
	}
	return docs
}

// trackedInfraTree returns every file under InfraPointerDir, repository-
// relative, using git's view in a checkout and the extracted tree otherwise.
func trackedInfraTree(t *testing.T, root string) []string {
	t.Helper()
	if inGitCheckout(root) {
		var files []string
		for _, p := range strings.Split(gitRun(t, root, "ls-files", InfraPointerDir+"/"), "\n") {
			if p != "" {
				files = append(files, p)
			}
		}
		return files
	}
	var files []string
	err := filepath.WalkDir(filepath.Join(root, InfraPointerDir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return relErr
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", InfraPointerDir, err)
	}
	return files
}

// TestMatcherClassifiesInstructionsNotMentions pins the doc scan's line: the
// historical offenders (quoting real shapes from the bf-era OpenBao records)
// must be flagged, while prose that explains the pointer rule — AGENTS.md's
// own sentences — and deployments from the authoritative k8s/ tree must stay
// clean. A matcher that flags mentions instead of instructions would fail
// the repository's own documentation.
func TestMatcherClassifiesInstructionsNotMentions(t *testing.T) {
	cases := []struct {
		line string
		kind string // "" means the line must not be flagged
	}{
		// The historical offenders, verbatim shapes.
		{line: "./declarative-config/infra/seam/setup-seam-openbao.sh", kind: "executes a path inside a pointer tree"},
		{line: "1. Run setup script: `./declarative-config/infra/seam/setup-seam-openbao.sh`", kind: "executes a path inside a pointer tree"},
		{line: "bash /home/coding/SEAM/declarative-config/infra/seam/setup-seam-openbao.sh", kind: "executes a path inside a pointer tree"},
		{line: "source declarative-config/infra/seam/seam-openbao-policy.hcl", kind: "executes a path inside a pointer tree"},
		{line: "cd /home/coding/SEAM/declarative-config/infra/seam", kind: "enters a pointer-tree directory"},
		{line: "cd declarative-config/infra", kind: "enters a pointer-tree directory"},
		{line: "kubectl apply -f declarative-config/k8s/iad-ci/argo-workflows/seam-ci.yaml", kind: "deployment command against a pointer tree"},
		{line: "argo submit declarative-config/infra/seam/openbao-setup-job.yaml", kind: "deployment command against a pointer tree"},

		// Prose that explains or references must never be flagged.
		{line: "`declarative-config/infra/` in this repo is a retirement pointer only. Do not restore manifests there."},
		{line: "The in-repo file `declarative-config/k8s/iad-ci/argo-workflows/seam-ci.yaml` is a pointer only (bead seam-5515cac0)."},
		{line: "Use `declarative-config/k8s/rs-manager/{seam,seam-retirement-evaluator}/` for new infrastructure configuration."},
		{line: "The authoritative manifest is declarative-config/k8s/iad-ci/argo-workflows/seam-ci-workflowtemplate.yml."},
		{line: "declarative-config/infrastructure is not a real directory."},
		{line: "kubectl apply -f declarative-config/k8s/rs-manager/seam/deployment.yaml"},
		{line: ""},
	}
	for _, tc := range cases {
		if got := classifyLine(tc.line); got != tc.kind {
			t.Errorf("classifyLine(%q) = %q, want %q", tc.line, got, tc.kind)
		}
	}
}

// TestHistoricalMarkerNeedsHeaderVisibility pins the marker's window: it
// marks a frozen record only near the top of the document, so the historical
// claim stays header-visible instead of being buried below the fold.
func TestHistoricalMarkerNeedsHeaderVisibility(t *testing.T) {
	offending := strings.Repeat("filler\n", HistoricalMarkerWindow) +
		"./declarative-config/infra/seam/setup-seam-openbao.sh\n"
	marker := "<!-- " + HistoricalMarker + ": point-in-time record -->\n"

	if findings := ScanDoc("report.md", []byte(marker+offending)); len(findings) != 0 {
		t.Errorf("a header-marked historical record was scanned: %+v", findings)
	}
	// Marker on the last line of the window still counts.
	lastLine := strings.Repeat("filler\n", HistoricalMarkerWindow-1) + marker + offending
	if findings := ScanDoc("report.md", []byte(lastLine)); len(findings) != 0 {
		t.Errorf("a marker inside the window was ignored: %+v", findings)
	}
	// One line further down it is buried, and the scan runs.
	buried := strings.Repeat("filler\n", HistoricalMarkerWindow) + marker + offending
	if findings := ScanDoc("report.md", []byte(buried)); len(findings) != 1 {
		t.Errorf("a buried marker silenced the scan: %+v", findings)
	}
}

// TestCheckInfraTreeFlagsRestoredManifests: the retirement pointer holds the
// README and nothing else.
func TestCheckInfraTreeFlagsRestoredManifests(t *testing.T) {
	if violations := CheckInfraTree([]string{InfraPointerDoc, "cmd/seam/main.go", "declarative-config/k8s/rs-manager/seam/deployment.yaml"}); len(violations) != 0 {
		t.Errorf("pointer-only tree flagged: %v", violations)
	}

	restored := []string{
		InfraPointerDoc,
		"declarative-config/infra/seam/seam-openbao-policy.hcl",
		"declarative-config/infra/setup.sh",
	}
	violations := CheckInfraTree(restored)
	if len(violations) != 2 {
		t.Fatalf("CheckInfraTree(%v) = %d violations, want 2: %v", restored, len(violations), violations)
	}
	for _, v := range violations {
		if !strings.Contains(v, AuthoritativeRepo) {
			t.Errorf("violation does not name the authoritative repo: %q", v)
		}
	}
}

// TestCheckInfraPointerDocRequiresPointerLanguage: the README must keep
// naming the authoritative repo and the forbid-restore instruction.
func TestCheckInfraPointerDocRequiresPointerLanguage(t *testing.T) {
	good := "# Infrastructure configuration moved\n\nThe " + AuthoritativeRepo +
		" repository is the source of truth.\n\nDo not restore staging copies in this repository.\n"
	if violations := CheckInfraPointerDoc([]byte(good)); len(violations) != 0 {
		t.Errorf("pointer README flagged: %v", violations)
	}

	noForbid := "# Infrastructure configuration moved\n\nThe " + AuthoritativeRepo + " repository is the source of truth.\n"
	if violations := CheckInfraPointerDoc([]byte(noForbid)); len(violations) != 1 || !strings.Contains(violations[0], InfraForbidMarker) {
		t.Errorf("dropping the forbid instruction was not flagged: %v", violations)
	}

	noRepo := "# Infrastructure configuration moved\n\nDo not restore staging copies in this repository.\n"
	if violations := CheckInfraPointerDoc([]byte(noRepo)); len(violations) != 1 || !strings.Contains(violations[0], AuthoritativeRepo) {
		t.Errorf("dropping the authoritative repo was not flagged: %v", violations)
	}
}

// seamCIPtrContent is the pointer language SeamCIPointerFile carries.
func seamCIPtrContent() string {
	return "# This file is a POINTER, not a manifest.\n#\n# The live seam-ci WorkflowTemplate is GitOps-managed from:\n#\n#   " +
		AuthoritativeRepo + "\n#   " + AuthoritativeSeamCITemplate + "\n"
}

// TestCheckSeamCIPointerDocRejectsManifestContent: a restored snapshot is
// caught line-by-line, and a pointer that lost its reference is caught too.
func TestCheckSeamCIPointerDocRejectsManifestContent(t *testing.T) {
	if violations := CheckSeamCIPointerDoc([]byte(seamCIPtrContent())); len(violations) != 0 {
		t.Errorf("comment-only pointer flagged: %v", violations)
	}

	manifest := seamCIPtrContent() + "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\n"
	violations := CheckSeamCIPointerDoc([]byte(manifest))
	if len(violations) != 2 {
		t.Fatalf("embedded manifest lines produced %d violations, want 2: %v", len(violations), violations)
	}
	for _, v := range violations {
		if !strings.Contains(v, AuthoritativeRepo) {
			t.Errorf("violation does not route the editor to the authoritative template: %q", v)
		}
	}

	noRef := "# A pointer that forgot where it points.\n"
	if violations := CheckSeamCIPointerDoc([]byte(noRef)); len(violations) != 2 {
		t.Errorf("pointer without authoritative references produced %d violations, want 2: %v", len(violations), violations)
	}
}

// TestPointerFilesHoldPointers is the static half: the two shipped pointer
// files satisfy their checks. Runs everywhere, including a `git archive`
// extraction.
func TestPointerFilesHoldPointers(t *testing.T) {
	root := repoRoot(t)

	infra := readFile(t, filepath.Join(root, InfraPointerDoc))
	if violations := CheckInfraPointerDoc(infra); len(violations) != 0 {
		t.Errorf("%s drifted out of pointer shape: %v", InfraPointerDoc, violations)
	}

	seamci := readFile(t, filepath.Join(root, SeamCIPointerFile))
	if violations := CheckSeamCIPointerDoc(seamci); len(violations) != 0 {
		t.Errorf("%s drifted out of pointer shape: %v", SeamCIPointerFile, violations)
	}
}

// TestInfraTreeHoldsOnlyThePointer: no manifest, script, or document may
// reappear under the retirement pointer. In a checkout this is checked
// against git's tracked-file list — an untracked stray is a different
// hygiene problem, not a deployment source — and in an extraction against
// the extracted tree, where every file is by definition committed.
func TestInfraTreeHoldsOnlyThePointer(t *testing.T) {
	root := repoRoot(t)
	files := trackedInfraTree(t, root)

	var violations []string
	if violations = CheckInfraTree(files); len(violations) != 0 {
		t.Errorf("declarative-config/infra carries embedded manifests: %v", violations)
	}
	found := false
	for _, f := range files {
		if f == InfraPointerDoc {
			found = true
		}
	}
	if !found {
		t.Errorf("%s is missing; the retirement pointer is gone", InfraPointerDoc)
	}

	if inGitCheckout(root) {
		if tracked := gitRun(t, root, "ls-files", "--", SeamCIPointerFile); strings.TrimSpace(tracked) == "" {
			t.Errorf("%s is not tracked; the in-repo CI pointer is gone", SeamCIPointerFile)
		}
	}
}

// TestDocsDoNotTreatPointersAsSources scans the repository's documentation
// for instructions that run or apply files from the pointer trees. In a
// checkout the document set is git's tracked *.md files; in an extraction it
// is the extracted tree, where every file is by definition committed.
func TestDocsDoNotTreatPointersAsSources(t *testing.T) {
	root := repoRoot(t)
	var findings []DocFinding
	for _, doc := range repoDocs(t, root) {
		content := readFile(t, filepath.Join(root, filepath.FromSlash(doc)))
		findings = append(findings, ScanDoc(doc, content)...)
	}
	for _, f := range findings {
		t.Errorf("%s:%d treats a pointer tree as a deployment source (%s): %s\n"+
			"Re-point the instruction at the authoritative declarative-config repository, or mark the document as a frozen record with a %s header line.",
			f.File, f.Line, f.Kind, f.Text, HistoricalMarker)
	}
}
