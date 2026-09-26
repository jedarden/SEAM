// Package pointerguard enforces the repository's declarative-config pointer
// rule (AGENTS.md, "Where things live").
//
// Two in-repository paths are pointers, never manifests:
//
//   - InfraPointerDir — a retirement pointer whose README names the
//     authoritative jedarden/declarative-config paths. The tree once held
//     staging copies of OpenBao policies, setup scripts, and workflow
//     manifests; those aged in place while the authoritative files moved on,
//     and a reader following them provisioned against stale state.
//   - SeamCIPointerFile — a comment-only pointer to the seam-ci
//     WorkflowTemplate ArgoCD actually applies (AuthoritativeRepo +
//     AuthoritativeSeamCITemplate). Its earlier life as an embedded snapshot
//     is the failure class behind the 2026-09 red-gate incidents: editing the
//     copy changed no CI behavior while looking like it did (bead
//     seam-5515cac0, removed 2026-09-25).
//
// The rule breaks in two ways, and the checks here fail on both. An
// authoritative manifest restored into either path — any file under the infra
// tree but the pointer README, any non-comment line in the seam-ci pointer —
// re-creates a deployment source that nothing applies and nothing prunes. And
// documentation that instructs a reader to run or apply files from the
// pointer trees treats the copies as live again.
//
// The doc scan deliberately flags instructions, not mentions: prose that
// explains the pointer status (including AGENTS.md itself) must keep working.
// Frozen point-in-time records that legitimately quote now-dead instructions —
// the bf-era OpenBao rollout reports of 2026-08-14 — carry the
// HistoricalMarker in their header, which the scan honours visibly; the
// marker is an honest record of "this document describes removed copies", not
// a bypass, and it only works in the document header so it cannot be smuggled
// in below the fold.
package pointerguard

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// InfraPointerDir is the in-repository retirement-pointer directory. It must
// never hold anything but InfraPointerDoc.
const InfraPointerDir = "declarative-config/infra"

// InfraPointerDoc is the only file InfraPointerDir may contain: the pointer
// that names the authoritative declarative-config locations.
const InfraPointerDoc = InfraPointerDir + "/README.md"

// SeamCIPointerFile is the in-repository seam-ci pointer. Every non-blank
// line must be a comment.
const SeamCIPointerFile = "declarative-config/k8s/iad-ci/argo-workflows/seam-ci.yaml"

// AuthoritativeRepo is the repository that owns SEAM's applied
// infrastructure: manifests and WorkflowTemplates live there, synced by
// ArgoCD, never in this repository.
const AuthoritativeRepo = "jedarden/declarative-config"

// AuthoritativeSeamCITemplate is the path of the live seam-ci
// WorkflowTemplate inside AuthoritativeRepo. SeamCIPointerFile must keep
// naming it, or the pointer points nowhere.
const AuthoritativeSeamCITemplate = "k8s/iad-ci/argo-workflows/seam-ci-workflowtemplate.yml"

// InfraForbidMarker is the phrase InfraPointerDoc must keep carrying — the
// instruction that keeps the retirement a retirement.
const InfraForbidMarker = "do not restore"

// HistoricalMarker marks a document as a frozen point-in-time record whose
// instructions describe in-repository staging copies that no longer exist.
// ScanDoc skips marked documents, but only when the marker sits within the
// first HistoricalMarkerWindow lines, so the claim is header-visible rather
// than buried.
const HistoricalMarker = "seam-pointerguard: historical"

// HistoricalMarkerWindow is how far into a document the HistoricalMarker may
// sit and still mark it historical.
const HistoricalMarkerWindow = 12

// CheckInfraTree returns a violation for every tracked path under
// InfraPointerDir that is not InfraPointerDoc — an authoritative manifest
// restored into the retirement pointer. Paths outside InfraPointerDir are
// ignored, so callers may pass a whole repository's tracked-file list.
func CheckInfraTree(tracked []string) []string {
	var violations []string
	for _, p := range tracked {
		cleaned := strings.TrimSuffix(p, "/")
		if cleaned == InfraPointerDoc {
			continue
		}
		if cleaned == InfraPointerDir || strings.HasPrefix(cleaned, InfraPointerDir+"/") {
			violations = append(violations, fmt.Sprintf(
				"%s is an embedded manifest under the %s retirement pointer; infrastructure configuration belongs in %s, at the paths %s names",
				p, InfraPointerDir, AuthoritativeRepo, InfraPointerDoc))
		}
	}
	sort.Strings(violations)
	return violations
}

// CheckInfraPointerDoc verifies InfraPointerDoc still points somewhere: it
// must name AuthoritativeRepo and keep the InfraForbidMarker instruction. A
// pointer that lost either is a pointer that no longer routes a reader to the
// authoritative configuration.
func CheckInfraPointerDoc(content []byte) []string {
	var violations []string
	lowered := strings.ToLower(string(content))
	if !strings.Contains(lowered, strings.ToLower(AuthoritativeRepo)) {
		violations = append(violations, fmt.Sprintf("%s no longer names %s; the retirement pointer points nowhere", InfraPointerDoc, AuthoritativeRepo))
	}
	if !strings.Contains(lowered, InfraForbidMarker) {
		violations = append(violations, fmt.Sprintf("%s no longer carries the %q instruction; the retirement is no longer stated", InfraPointerDoc, InfraForbidMarker))
	}
	return violations
}

// CheckSeamCIPointerDoc verifies SeamCIPointerFile is still a pointer: every
// non-blank line must be a comment (a manifest line here is an authoritative
// snapshot ArgoCD will never apply), and the pointer must still name
// AuthoritativeRepo and AuthoritativeSeamCITemplate.
func CheckSeamCIPointerDoc(content []byte) []string {
	var violations []string
	for i, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		violations = append(violations, fmt.Sprintf(
			"%s:%d carries non-comment content (%q); the in-repo seam-ci file is a pointer only, and manifest content there is a snapshot nothing applies — edit %s %s instead",
			SeamCIPointerFile, i+1, truncate(line, 60), AuthoritativeRepo, AuthoritativeSeamCITemplate))
	}
	if !strings.Contains(string(content), AuthoritativeRepo) {
		violations = append(violations, fmt.Sprintf("%s no longer names %s; the pointer no longer says where the live WorkflowTemplate lives", SeamCIPointerFile, AuthoritativeRepo))
	}
	if !strings.Contains(string(content), AuthoritativeSeamCITemplate) {
		violations = append(violations, fmt.Sprintf("%s no longer names %s; the pointer no longer says which file is authoritative", SeamCIPointerFile, AuthoritativeSeamCITemplate))
	}
	return violations
}

// DocFinding is one line of documentation that treats a pointer tree as a
// deployment source.
type DocFinding struct {
	// File is the repository-relative path of the document.
	File string
	// Line is the 1-based line number.
	Line int
	// Kind says how the line treats the copy: a deployment command applied
	// to it, an execution of a path inside it, or a directory entry into it.
	Kind string
	// Text is the trimmed offending line.
	Text string
}

// pointerPath matches the two pointer targets, with or without this
// checkout's absolute prefix. The infra branch ends at a segment boundary so
// sibling names (declarative-config/infrastructure) never match, and the
// k8s/ subtree does not match at all — only the seam-ci pointer file does.
// Deploying from authoritative paths (declarative-config/k8s/...) is correct
// and must stay unflagged.
const pointerPath = `(?:/home/coding/SEAM/)?declarative-config/infra(?:/|\b)|` +
	`(?:/home/coding/SEAM/)?declarative-config/k8s/iad-ci/argo-workflows/seam-ci\.yaml\b`

var (
	// pointerName matches the two pointer targets anywhere in a line.
	pointerName = regexp.MustCompile(`(?:` + pointerPath + `)`)

	// deployVerb matches a command that applies or renders a manifest; it
	// only matters when the same line names a pointer path.
	deployVerb = regexp.MustCompile(`kubectl\s+(?:apply|create|delete|patch|edit|replace)|` +
		`argo\s+(?:submit|lint|template)|helm\s+(?:install|upgrade|template)`)

	// execPath matches a line that runs something located inside a pointer
	// tree: ./path, sh|bash|zsh|dash|source|sudo path. (The character class
	// is assembled by concatenation because a raw string cannot carry the
	// backtick it must treat as a boundary character.)
	execPath = regexp.MustCompile(`(?:^|[\s` + "`" + `"(])(?:(?:\./|(?:ba|z|da)?sh\s+|source\s+|sudo\s+)(?:/home/coding/SEAM/)?` +
		`(?:(?:` + pointerPath + `)))`)

	// enterPath matches a cd into a pointer tree, with optional flags.
	enterPath = regexp.MustCompile(`\bcd\s+(?:-\S+\s+)*(?:/home/coding/SEAM/)?(?:` + pointerPath + `)`)
)

// ScanDoc scans one document's content for lines that treat the pointer
// trees as deployment sources and returns every finding. Documents carrying
// HistoricalMarker within their header window are frozen point-in-time
// records and are returned clean.
func ScanDoc(file string, content []byte) []DocFinding {
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		if i >= HistoricalMarkerWindow {
			break
		}
		if strings.Contains(line, HistoricalMarker) {
			return nil
		}
	}

	var findings []DocFinding
	for i, line := range lines {
		if kind := classifyLine(line); kind != "" {
			findings = append(findings, DocFinding{File: file, Line: i + 1, Kind: kind, Text: strings.TrimSpace(line)})
		}
	}
	return findings
}

// classifyLine reports how a single line treats a pointer tree as a
// deployment source, or "" when the line is a mention rather than an
// instruction.
func classifyLine(line string) string {
	if !pointerName.MatchString(line) {
		return ""
	}
	if deployVerb.MatchString(line) {
		return "deployment command against a pointer tree"
	}
	if execPath.MatchString(line) {
		return "executes a path inside a pointer tree"
	}
	if enterPath.MatchString(line) {
		return "enters a pointer-tree directory"
	}
	return ""
}

// truncate shortens s for inclusion in a diagnostic message.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
