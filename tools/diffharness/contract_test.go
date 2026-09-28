// Package diffharness carries no implementation: it exists so this module-wide
// tripwire can live at the module root, next to the README it guards. The
// differential replay comparison contract is defined in
// docs/design/differential-replay-contract.md; every rule ID it defines is
// supposed to be pinned by a named test in this module, and every test named
// in its rule index is supposed to exist. This file pins both directions of
// that coupling — a rule dropped from the document, or a pinned test renamed
// or deleted, fails the module gate (definition-of-done "diffharness module
// gate") instead of rotting silently.
package diffharness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ruleIDs are the contract's rule markers, in the "**ID — Title.**" form the
// document uses. G3 has no marker bullet (it is the documented exception: the
// workflow-side lane lives in another repository) and is deliberately absent.
var ruleIDs = []string{
	"C1 —", "C2 —", "C3 —", "C4 —", "C5 —",
	"D1 —", "D2 —", "D3 —", "D4 —",
	"N1 —", "N2 —", "N3 —",
	"S1 —", "S2 —", "S3 —", "S4 —",
	"F1 —", "F2 —", "F3 —", "F4 —",
	"X1 —", "X2 —",
	"G1 —", "G2 —", "G3 —",
}

// pinnedTests are the concrete test function names the document's rule index
// cites. prose-annotated entries (S1's golden tests) are excluded here — they
// are pinned by name in their own packages — but everything else must resolve
// to a real function in this module.
var pinnedTests = []string{
	// C1 / C2 — canonicalization
	"TestLoadCanonicalizesHeaders",
	"TestLoadCanonicalizesMethod",
	"TestLoadDefaultsEmptyMethod",
	"TestReplayOneCanonicalizesResponseHeaders",
	"TestCompareDoesNotCanonicalizeKeys",
	// C3 / C4 — value and body rules
	"TestRepeatedHeadersOrderInsensitive",
	"TestJSONBodyIsByteExactNotSemanticallyNormalized",
	// C5 — redaction
	"TestSubstringSecretRedactsLongestFirst",
	"TestEmptySecretIsIgnored",
	"TestBearerEchoRedactsOnlySecret",
	// D1 — status
	"TestStatusDiffIsFail",
	"TestExpectedStatusPinsSeamSide",
	"TestExpectedStatusMismatchFails",
	// D2 — headers
	"TestSeamDropsHeaderIsFail",
	"TestSeamAddsUnexpectedHeaderIsFail",
	"TestIgnoreHeaders",
	"TestSeamAddsXSEAMHeadersIsExpected",
	"TestDeprecationHeadersAreExpectedSeamAdditions",
	// D3 / D4 — trailers and body
	"TestLeakInTrailer",
	"TestBodyDiffNonSecretIsFail",
	"TestIgnoreBodySuppressesStructuralDiff",
	// N2 / N3 — nondeterminism
	"TestIgnoreBodyDoesNotWeakenLeakCheck",
	"TestRunMainAllSkippedCorpusExits1",
	// S1 / S2 — secret references
	"TestLoadValidatesSecretRefsAgainstEnforcedBase",
	"TestCheckedInFixturesResolveUnderEnforcedVaultBase",
	"TestResolveFileLegOverEnvLeg",
	"TestResolveEnvLegFallback",
	"TestEnvNameMapping",
	"TestResolveUnresolvedRefIsNotAnError",
	// S3 / S4 — leak check and echo semantics
	"TestEchoedSecretInSeamResponseIsLeakFailure",
	"TestLeakInHeader",
	"TestRedactedCredentialEchoIsPass",
	// F3 / F4 — failures
	"TestIncumbentFailureSkipsSeamFailureFails",
	"TestCorpusLifecycleWithRealisticWorkload",
	// X1 — exit codes
	"TestRunMainUsageErrorsExit2",
	"TestRunMainAllPassExits0",
	"TestRunMainReplayFailureExits1",
	"TestRunMainMissingCorpusExits1",
	// X2 / G1 — cutover exit codes and consumption
	"TestCheckReplayGatesOnExitCode",
	"TestDeriveReplayReport",
}

// rootPinnedTests are rule-index entries pinned by test functions in the ROOT
// module — outside the walk above, but part of the contract's pin set and
// reachable from here at a fixed relative path.
var rootPinnedTests = map[string]string{
	// G2 — the gate-wiring tripwire lives in internal/corpusboundary
	"TestFixtureValidationStaysWired": filepath.Join("..", "..", "internal", "corpusboundary", "boundary_test.go"),
}

// contractDocPath resolves from this file's package directory
// (tools/diffharness) to the repository-root contract document. In a
// `git archive` extraction the layout is identical, so the tripwire runs
// everywhere the module gate runs.
func contractDocPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "design", "differential-replay-contract.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("contract document missing at %s: %v — the comparison contract must stay defined, not just implemented", path, err)
	}
	return path
}

func TestContractDocPinsRuleIDs(t *testing.T) {
	raw, err := os.ReadFile(contractDocPath(t))
	if err != nil {
		t.Fatalf("read contract document: %v", err)
	}
	doc := string(raw)
	for _, id := range ruleIDs {
		if !strings.Contains(doc, "**"+id) {
			t.Errorf("contract document lost rule %q — a rule the tests pin must stay defined, or its pins are enforcing nothing", strings.TrimSuffix(id, " —"))
		}
	}
}

func TestRuleIndexNamesExistingTests(t *testing.T) {
	defined := make(map[string]bool)
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if s, ok := strings.CutPrefix(line, "func "); ok {
				if name, _, ok := strings.Cut(s, "("); ok {
					defined[name] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module sources: %v", err)
	}
	for _, name := range pinnedTests {
		if !defined[name] {
			t.Errorf("rule index cites %s but no such test function exists in the module — the index is rotting", name)
		}
	}
	for name, path := range rootPinnedTests {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("rule index cites %s via %s, which cannot be read: %v", name, path, err)
			continue
		}
		if !strings.Contains(string(raw), "func "+name+"(") {
			t.Errorf("rule index cites %s but the root-module tripwire no longer defines it — the gate-wiring pin has been dropped", name)
		}
	}
}
