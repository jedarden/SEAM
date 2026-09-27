package corpus

import (
	"os"
	"strings"
	"testing"
)

// enforcedVaultBaseGolden is the SEAM-side golden file this module's enforced
// base must agree with. The module is deliberately standalone (stdlib only,
// no SEAM gateway imports), so it cannot import internal/spec; the golden
// file is the one artifact both suites read, and the path leaving the module
// boundary is the point — the agreement itself is the contract.
const enforcedVaultBaseGolden = "../../../../internal/spec/testdata/enforced-vault-base.txt"

// TestEnforcedVaultBaseMatchesGolden is the diffharness half of the
// vault-base drift tripwire (docs/capture_testing.md). This package's suite
// validates fixtures only against its own DefaultVaultBaseDir mirror, and
// internal/spec's suite only against SEAM's constant, so a one-sided move of
// either stayed green on both sides and surfaced only as replay-time
// secret-resolution failures. Reading the golden file internal/spec's own
// tripwire (TestDefaultVaultBaseDirMatchesGolden) reads makes the drift fail
// at fixture time instead. When the base moves, move all three:
// internal/spec.DefaultVaultBaseDir, this mirror, and the golden.
func TestEnforcedVaultBaseMatchesGolden(t *testing.T) {
	t.Setenv(VaultBaseDirEnvVar, "") // the golden describes the un-overridden base

	raw, err := os.ReadFile(enforcedVaultBaseGolden)
	if err != nil {
		t.Fatalf("read %s: %v (the golden lives in the parent repository; run this suite from a repository checkout)",
			enforcedVaultBaseGolden, err)
	}
	golden := strings.TrimSpace(string(raw))
	if golden == "" {
		t.Fatalf("%s is empty: the shared enforced vault base must be pinned there", enforcedVaultBaseGolden)
	}
	if DefaultVaultBaseDir != golden {
		t.Fatalf("corpus.DefaultVaultBaseDir = %q but %s pins %q: move all three (this mirror, the golden, and internal/spec.DefaultVaultBaseDir)",
			DefaultVaultBaseDir, enforcedVaultBaseGolden, golden)
	}
	if got := resolveVaultBaseDir(); got != golden {
		t.Fatalf("resolveVaultBaseDir() = %q but %s pins %q", got, enforcedVaultBaseGolden, golden)
	}
}
