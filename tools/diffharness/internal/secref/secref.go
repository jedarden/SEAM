// Package secref resolves a corpus Secret.Ref into the literal credential value
// the comparison needs — at replay time, in memory, from a local source the
// operator controls. It is the only place in the harness that ever holds a
// secret value, and it never writes one to disk.
//
// Two sources, layered file-over-env:
//
//   - A JSON file mapping ref -> value (--secrets). Git-ignored by convention
//     (the README names it *.local.json); the operator populates it from the
//     vault for the duration of a replay.
//   - Environment variables derived from the ref (--secrets-env). A ref like
//     "vault:seam/routes/argocd/ro-token" becomes SEAM_DIFF_SECRET_vault_seam_routes_argocd_ro_token.
//
// If neither resolves a ref that a corpus entry needs, the entry is reported as
// SKIP ("unresolved secret ref") rather than failed: an unresolved ref is a
// configuration gap, not a cutover regression, and must not turn a corpus red.
package secref

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Resolver maps a secret ref to its literal value.
type Resolver struct {
	values map[string]string
}

// NewResolver builds a resolver from an optional secrets file. A nil/empty path
// yields a resolver backed only by the environment.
func NewResolver(path string) (*Resolver, error) {
	r := &Resolver{values: map[string]string{}}
	if path == "" {
		return r, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read secrets file %q: %w", path, err)
	}
	if err := json.Unmarshal(raw, &r.values); err != nil {
		return nil, fmt.Errorf("parse secrets file %q: %w", path, err)
	}
	return r, nil
}

// Resolve returns the literal value for ref, checking the file first then the
// SEAM_DIFF_SECRET_<sanitized> environment variable. ok is false if neither has it.
func (r *Resolver) Resolve(ref string) (value string, ok bool) {
	if v, hit := r.values[ref]; hit {
		return v, true
	}
	if v, hit := os.LookupEnv(envName(ref)); hit {
		return v, true
	}
	return "", false
}

// envName is the stable mapping ref -> env var: uppercase, every run of
// non-[A-Z0-9_] collapsed to a single underscore, prefixed SEAM_DIFF_SECRET_.
// It is deterministic and injective for the ref shapes the corpus uses.
func envName(ref string) string {
	var b strings.Builder
	b.WriteString("SEAM_DIFF_SECRET_")
	prevUnder := false
	for _, ru := range strings.ToUpper(ref) {
		switch {
		case ru >= 'A' && ru <= 'Z', ru >= '0' && ru <= '9':
			b.WriteRune(ru)
			prevUnder = false
		default:
			if !prevUnder {
				b.WriteByte('_')
				prevUnder = true
			}
		}
	}
	return b.String()
}
