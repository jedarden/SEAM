package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardenone/seam/internal/server"
	"github.com/ardenone/seam/internal/spec"
)

// TestReadmeEnvTableMatchesServeContract closes the loop the other serve
// configuration tests leave open. serveFlagEnvPairs is pinned to the flag set
// (TestServeFlagEnvMapping) and to the lookups applyEnvOverrides performs
// (TestServeEnvOverridesConsultExactlyTheDocumentedVariables), but the
// README's environment-variable table itself is prose: nothing failed when
// the table and the mapping drifted apart, because the sync lived only in
// comments telling you to move both together. This test parses the table out
// of README.md and requires every row to pair the variable the mapping
// documents with the flag it names and the flag's real default, so a renamed
// variable, a re-paired flag, or an edited default fails here until the
// table and the mapping move together.
//
// Three rows render the *effective* default rather than the flag's literal
// DefValue, because the empty flag value resolves further downstream:
//
//   - --upstream-ca-dir defaults to empty at the flag but resolves to
//     server.DefaultUpstreamCADir even outside a cluster
//     (resolveUpstreamCADir), and the README appends the in-cluster caveat;
//   - --allowlist-file defaults to empty, which is "none" outside a cluster
//     (resolveAllowlistFile), with the same caveat;
//   - --vault-base-dir defaults to empty at the flag and falls back to the
//     shared spec.DefaultVaultBaseDir in server.New.
func TestReadmeEnvTableMatchesServeContract(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	registerServeFlags(fs)

	type tableRow struct {
		variable, flag, def string
	}
	var rows []tableRow
	for i, line := range strings.Split(string(readme), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "| `SEAM_") {
			continue
		}
		cells := strings.Split(trimmed, "|")
		if len(cells) < 5 {
			t.Errorf("README.md line %d looks like an environment-variable table row but does not have Variable/Flag/Default cells: %q", i+1, trimmed)
			continue
		}
		rows = append(rows, tableRow{
			variable: normalizeReadmeCell(cells[1]),
			flag:     normalizeReadmeCell(cells[2]),
			def:      normalizeReadmeCell(cells[3]),
		})
	}
	if len(rows) == 0 {
		t.Fatal("README.md's environment-variable table has no SEAM_* rows; the table every serve configuration test points at is gone")
	}
	if len(rows) != len(serveFlagEnvPairs) {
		t.Errorf("README.md's environment-variable table has %d rows, want %d — the README promises one row per serve flag; update the table and serveFlagEnvPairs together", len(rows), len(serveFlagEnvPairs))
	}

	byVariable := make(map[string]tableRow, len(rows))
	for _, r := range rows {
		if prev, dup := byVariable[r.variable]; dup {
			t.Errorf("README.md's environment-variable table documents %s twice (paired with --%s and --%s)",
				r.variable, strings.TrimPrefix(prev.flag, "--"), strings.TrimPrefix(r.flag, "--"))
			continue
		}
		byVariable[r.variable] = r
	}

	pairsByVariable := make(map[string]string, len(serveFlagEnvPairs))
	for _, pair := range serveFlagEnvPairs {
		pairsByVariable[pair.envVar] = pair.flag
	}

	for _, r := range rows {
		pair, documented := pairsByVariable[r.variable]
		if !documented {
			t.Errorf("README.md pairs %s with --%s, but serveFlagEnvPairs does not document that variable — update the table and the mapping together", r.variable, strings.TrimPrefix(r.flag, "--"))
			continue
		}
		if want := "--" + pair; r.flag != want {
			t.Errorf("README.md pairs %s with --%s, want --%s — the table and the mapping disagree about the pairing", r.variable, strings.TrimPrefix(r.flag, "--"), pair)
		}
	}

	for _, pair := range serveFlagEnvPairs {
		r, inTable := byVariable[pair.envVar]
		if !inTable {
			t.Errorf("serveFlagEnvPairs documents %s for --%s, but README.md's table has no such row — update the table and the mapping together", pair.envVar, pair.flag)
			continue
		}
		defined := fs.Lookup(pair.flag)
		if defined == nil {
			t.Errorf("serveFlagEnvPairs pairs --%s with %s, but registerServeFlags defines no --%s flag", pair.flag, pair.envVar, pair.flag)
			continue
		}
		want := readmeDefaultCell(pair.flag, defined.DefValue)
		if r.def != want {
			t.Errorf("README.md documents %s's default as %q, want %q — keep the table's Default column synchronized with registerServeFlags and the documented effective defaults", pair.envVar, r.def, want)
		}
	}
}

// normalizeReadmeCell strips the cell decoration (surrounding whitespace and
// the markdown backticks) so cells compare as plain text.
func normalizeReadmeCell(cell string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(cell), "`", ""))
}

// readmeDefaultCell renders the Default cell the README must show for a
// serve flag. Most cells are the flag's literal DefValue; the three flags
// whose empty value resolves further downstream render the effective default
// instead, with the in-cluster caveat the README documents for the two
// upstream-trust paths.
func readmeDefaultCell(flagName, defValue string) string {
	switch flagName {
	case "upstream-ca-dir":
		return server.DefaultUpstreamCADir + " (override refused in-cluster)"
	case "allowlist-file":
		return "none (override refused in-cluster)"
	case "vault-base-dir":
		return spec.DefaultVaultBaseDir
	default:
		return defValue
	}
}
