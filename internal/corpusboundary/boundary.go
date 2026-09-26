// Package corpusboundary enforces the repository's split between runtime
// capture data and checked-in fixtures.
//
// The repository keeps corpus data in two places with opposite git
// lifecycles (docs/capture_testing.md, "Capture and Corpus Testing"):
//
//   - Runtime captures land under the repository-root corpus/ directory.
//     The 2026-09-18 history purge (seam-70ae655e, commit 9984a5b) removed
//     every checked-in corpus and gitignored the path, so live captures —
//     which carry route metadata and credential references — stay out of git
//     entirely.
//   - The checked-in fixtures under tools/diffharness/testdata/ (plus the
//     corpus package that validates them,
//     tools/diffharness/internal/corpus/) are committed and must stay
//     committed: schema version, entry-ID uniqueness, header/method
//     canonicalization, and secrets[].ref vault-base enforcement all run
//     against them.
//
// The boundary breaks accidentally in two directions. Widening the ignore
// back to a bare `corpus/` pattern also swallows the nested corpus Go
// package — the exact regression the .gitignore comment warns about, which
// silently dropped corpus_test.go from every commit. And a runtime capture
// swept up by a blanket `git add` is promoted into version history, where
// the next purge has to remove it again. The checks in this package fail on
// both: the gitignore must keep the anchored `/corpus/` entry and must not
// ignore any guarded fixture or package path, and — in a real checkout —
// nothing under corpus/ may be tracked. Deliberate promotion of a capture
// is a move into tools/diffharness/testdata (where the diffharness fixture
// validation applies), never a commit under corpus/.
package corpusboundary

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// RuntimeCorpusIgnorePattern is the gitignore entry that keeps runtime
// captures out of git. The anchored form is load-bearing: a bare `corpus/`
// matches the nested tools/diffharness/internal/corpus/ package too and
// silently drops it from commits, which is why this package rejects any
// other spelling (see the comment above the /corpus/ line in .gitignore).
const RuntimeCorpusIgnorePattern = "/corpus/"

// GuardedPaths are the checked-in paths that must never match any gitignore
// pattern: the corpus package itself (the validator for the fixture format)
// and the checked-in fixtures it validates. The fixture list must be kept in
// step with tools/diffharness/testdata — the boundary test asserts each of
// these files exists, so deleting or renaming a fixture fails the check
// instead of silently losing its validation.
var GuardedPaths = []string{
	"tools/diffharness/internal/corpus/corpus.go",
	"tools/diffharness/internal/corpus/corpus_test.go",
	"tools/diffharness/testdata/corpus-argocd.json",
	"tools/diffharness/testdata/example-corpus.json",
	"tools/diffharness/testdata/secrets-argocd.local.json",
}

// FixtureValidationLane is the definition-of-done check name that runs the
// diffharness corpus package — the validator every checked-in fixture gets.
// The boundary test asserts the lane stays wired into
// scripts/definition-of-done.sh, so the fixture validation cannot be
// unwired silently again (docs/capture_testing.md, "Where each check is
// enforced", previously recorded the fixture checks as wired into neither
// gate).
const FixtureValidationLane = "diffharness fixture validation"

// Pattern is one parsed .gitignore line.
type Pattern struct {
	// regex matches a whole root-relative path (files or directories,
	// without a trailing slash) this pattern applies to.
	regex *regexp.Regexp
	// negated is a leading !, which un-ignores matching paths.
	negated bool
	// dirOnly is a trailing slash: the pattern matches directories only.
	dirOnly bool
	// source is the original line, for diagnostics.
	source string
}

// ParseGitignore parses .gitignore content into patterns. Blank lines and
// comments are skipped. The supported syntax subset is the one .gitignore
// uses today: `*` and `?` wildcards (never crossing a /), a `**/` segment
// matching zero or more directories, leading- or middle-slash anchoring,
// trailing-slash directory-only patterns, and `!` negation. A `[` starts a
// literal bracket rather than a character class — none of the patterns this
// repository ships uses one, and a future pattern that did would be scanned
// literally, never silently skipped.
func ParseGitignore(data []byte) []Pattern {
	var patterns []Pattern
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		p := Pattern{source: line}
		if rest, ok := strings.CutPrefix(trimmed, "!"); ok {
			p.negated = true
			trimmed = rest
		}
		if rest, ok := strings.CutSuffix(trimmed, "/"); ok {
			p.dirOnly = true
			trimmed = rest
		}
		anchored := strings.HasPrefix(trimmed, "/")
		trimmed = strings.TrimPrefix(trimmed, "/")
		// A separator anywhere (after anchor and dir-marker stripping)
		// anchors the pattern to the root; without one it matches at any
		// depth.
		anchored = anchored || strings.Contains(trimmed, "/")
		p.regex = compileGitignoreGlob(trimmed, anchored)
		patterns = append(patterns, p)
	}
	return patterns
}

// compileGitignoreGlob translates one gitignore glob into a regexp over a
// whole root-relative path. Unanchored patterns are wrapped so they match at
// any depth; anchored ones must match from the root. Every compiled pattern
// accepts a trailing slash boundary so a pattern written for a directory
// also excludes everything beneath it.
func compileGitignoreGlob(glob string, anchored bool) *regexp.Regexp {
	var b strings.Builder
	if anchored {
		b.WriteString("^")
	} else {
		b.WriteString("(?:^|/)")
	}
	segs := strings.Split(glob, "/")
	prevMulti := false
	for i, seg := range segs {
		if seg == "**" {
			// Zero or more whole directories; the group carries its own
			// trailing slashes, so the next segment must not add one.
			// "**/foo" matches "foo" at the root too — the zero case —
			// which is exactly the bare-corpus hazard shape.
			b.WriteString("(?:[^/]+/)*")
			prevMulti = true
			continue
		}
		if i > 0 && !prevMulti {
			b.WriteString("/")
		}
		prevMulti = false
		b.WriteString(translateSegment(seg))
	}
	b.WriteString("(?:$|/)")
	return regexp.MustCompile(b.String())
}

// translateSegment translates one non-** path segment: * and ? stay within
// the segment, everything else is literal.
func translateSegment(seg string) string {
	var b strings.Builder
	for _, r := range seg {
		switch r {
		case '*':
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}

// matches reports whether the pattern applies to node, a root-relative path.
// isDir says whether node names a directory; directory-only patterns never
// match a file.
func (p Pattern) matches(node string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	return p.regex.MatchString(node)
}

// Ignored reports whether the parsed patterns would exclude relPath, using
// git's directory rule: once a parent directory is excluded, git never
// descends into it, so the whole subtree is ignored and a negation on a
// deeper path cannot resurrect it. Per node the last matching pattern wins,
// so a ! pattern rescues the node it names.
func Ignored(patterns []Pattern, relPath string) bool {
	cleaned := path.Clean(strings.TrimSuffix(path.Clean(relPath), "/"))
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "..") {
		return false
	}
	segs := strings.Split(cleaned, "/")
	for i := range segs {
		node := strings.Join(segs[:i+1], "/")
		ignored := false
		for _, p := range patterns {
			if p.matches(node, i < len(segs)-1) {
				ignored = !p.negated
			}
		}
		if ignored {
			return true
		}
	}
	return false
}

// HazardousPatterns returns the parsed patterns with a positive (non-
// negation) match on relPath or one of its ancestor directories — the
// diagnostic half of Ignored, so a failing boundary check can name the
// offending .gitignore line instead of just the path.
func HazardousPatterns(patterns []Pattern, relPath string) []string {
	cleaned := path.Clean(strings.TrimSuffix(path.Clean(relPath), "/"))
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "..") {
		return nil
	}
	segs := strings.Split(cleaned, "/")
	var hazardous []string
	seen := map[string]bool{}
	for i := range segs {
		node := strings.Join(segs[:i+1], "/")
		isDir := i < len(segs)-1
		for _, p := range patterns {
			if !p.negated && p.matches(node, isDir) && !seen[p.source] {
				seen[p.source] = true
				hazardous = append(hazardous, fmt.Sprintf("%q (matches %q)", p.source, node))
			}
		}
	}
	return hazardous
}
