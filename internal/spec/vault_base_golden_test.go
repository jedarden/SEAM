package spec

import (
	"os"
	"strings"
	"testing"
)

// enforcedVaultBaseGolden is the one-line golden file pinning the default
// vault base this package and the diffharness mirror both enforce. The
// diffharness module is deliberately standalone (stdlib only, no SEAM gateway
// imports — see tools/diffharness/go.mod), so it cannot import internal/spec
// to stay honest; the golden file is the one artifact both suites can read,
// and each side asserts its constant against it.
const enforcedVaultBaseGolden = "testdata/enforced-vault-base.txt"

func readEnforcedVaultBaseGolden(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(enforcedVaultBaseGolden)
	if err != nil {
		t.Fatalf("read %s: %v", enforcedVaultBaseGolden, err)
	}
	golden := strings.TrimSpace(string(raw))
	if golden == "" {
		t.Fatalf("%s is empty: the shared enforced vault base must be pinned there", enforcedVaultBaseGolden)
	}
	return golden
}

// TestDefaultVaultBaseDirMatchesGolden is the SEAM-side half of the vault-base
// drift tripwire. DefaultVaultBaseDir is mirrored by hand in
// tools/diffharness/internal/corpus, whose suite validates fixtures only
// against its own copy — so a one-sided move of either constant stayed green
// on both sides and surfaced only later, as replay-time secret-resolution
// failures (the base already moved once, on the 2026-09-04 consolidation).
// Both suites now read the same golden file: moving DefaultVaultBaseDir
// without updating it fails here, updating the golden without moving the
// diffharness mirror fails the corpus suite, and moving constant and mirror
// without the golden fails whichever suite reads it. When the base moves,
// move all three.
func TestDefaultVaultBaseDirMatchesGolden(t *testing.T) {
	t.Setenv(VaultBaseDirEnvVar, "") // the golden describes the un-overridden base

	golden := readEnforcedVaultBaseGolden(t)
	if DefaultVaultBaseDir != golden {
		t.Fatalf("DefaultVaultBaseDir = %q but %s pins %q: move all three (this constant, the golden, and tools/diffharness/internal/corpus.DefaultVaultBaseDir)",
			DefaultVaultBaseDir, enforcedVaultBaseGolden, golden)
	}
	if got := ResolveVaultBaseDir(""); got != golden {
		t.Fatalf("ResolveVaultBaseDir(\"\") = %q but %s pins %q", got, enforcedVaultBaseGolden, golden)
	}
}
