// Package corpus defines the on-disk format of a differential test corpus.
//
// A corpus is a set of captured request/response pairs recorded at an
// *incumbent* proxy — the hand-rolled fleet proxy a SEAM route is about to
// replace. The corpus is the oracle for the Phase 6b service-by-service
// cutover: a service's fragment does not ship, and its CLAUDE.md prose is not
// deleted, until its corpus passes the differential replay
// (docs/plan/plan.md, Testing Strategy → Conformance / differential harness).
//
// Security shape: a corpus entry never stores a credential value. It stores a
// *reference* (Secret.Ref, e.g. "vault:seam/routes/argocd/ro-token") plus the
// metadata of how SEAM injects it. The literal value is resolved to memory at
// replay time from a local, git-ignored secrets source (internal/secref), so a
// corpus — a git-tracked test artifact attached to a cutover PR — can never
// leak the very secret SEAM exists to hide.
package corpus

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"os"
	"sort"
)

// SchemaVersion is the version string stamped on every corpus file. A change
// to the on-disk shape bumps this and the loader refuses a mismatch with a
// pointed message, rather than guessing an older layout.
const SchemaVersion = "seam-diff-corpus/v1"

// Corpus is one service's captured differential corpus.
type Corpus struct {
	Schema      string  `json:"schema"`           // always SchemaVersion
	Service     string  `json:"service"`          // the <svc> owner token
	Incumbent   string  `json:"incumbent"`        // base URL of the incumbent proxy captured against
	CapturedAt  string  `json:"capturedAt"`       // RFC3339, set on first capture
	Description string  `json:"description"`      // free-form, surfaces in the report header
	Entries     []Entry `json:"entries"`
}

// Entry is a single captured request plus the expectations for its replay.
type Entry struct {
	ID          string   `json:"id"`                     // stable, human-readable (e.g. "list-apps")
	Description string   `json:"description,omitempty"`  // what this entry exercises
	Request     Request  `json:"request"`                // the caller's request, replayed verbatim
	Secrets     []Secret `json:"secrets,omitempty"`      // injected-credential refs (never values)
	Expect      *Expect  `json:"expect,omitempty"`       // per-entry comparison overrides
}

// Request is the caller-supplied request, recorded at the incumbent and
// replayed verbatim against both targets. It deliberately carries no
// credential: both the incumbent and SEAM inject server-side.
type Request struct {
	Method          string              `json:"method"`
	Path            string              `json:"path"`                    // path only, no query
	Query           string              `json:"query,omitempty"`         // query without the leading '?'
	Headers         map[string][]string `json:"headers,omitempty"`       // canonicalized keys
	BodyB64         string              `json:"bodyB64,omitempty"`       // base64 of the body; "" == empty
	BodyContentType string              `json:"bodyContentType,omitempty"`
}

// Secret is a *reference* to an injected credential. The literal value is held
// only in memory during a replay (Secret.Bare, never serialized) and is
// resolved by internal/secref from a local secrets source.
type Secret struct {
	Ref      string   `json:"ref"`               // e.g. "vault:seam/routes/argocd/ro-token"
	InjectAs InjectAs `json:"injectAs"`          // how SEAM injects this credential
	Bare     string   `json:"-"`                 // resolved at replay time; never written to disk
}

// InjectAs mirrors the route-fragment x-inject-as shape ({kind, name}).
type InjectAs struct {
	Kind string `json:"kind"`           // header | bearer | query
	Name string `json:"name,omitempty"` // header/query name; omitted (and rejected) for bearer
}

// Expect holds per-entry overrides to the default differential comparison.
type Expect struct {
	// Status, if non-nil, is the status both targets must return. nil (default)
	// means "require incumbent.status == seam.status" — the plain differential
	// rule. Set it for routes where SEAM legitimately transforms the status
	// (e.g. an _all fan-out that is 207 on SEAM).
	Status *int `json:"status,omitempty"`

	// IgnoreHeaders are canonical header names to drop from both sides before
	// comparing — volatile headers that differ call-to-call on the same backend
	// (Date, Server, X-Request-Id, Set-Cookie, ETag, ...).
	IgnoreHeaders []string `json:"ignoreHeaders,omitempty"`

	// IgnoreBody skips body comparison entirely, for responses whose bodies are
	// non-deterministic (timestamps, request IDs embedded in the payload). Use
	// sparingly: a body the differential cannot pin is a body the cutover
	// cannot prove equivalent, and the report calls this out explicitly.
	IgnoreBody bool `json:"ignoreBody,omitempty"`

	// Skip marks an entry as not-yet-replayable (e.g. depends on an upstream not
	// yet onboarded). Skipped entries appear in the report but do not fail it.
	Skip string `json:"skip,omitempty"`
}

// Load reads and validates a corpus file.
func Load(path string) (*Corpus, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read corpus %q: %w", path, err)
	}
	var c Corpus
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse corpus %q: %w", path, err)
	}
	if c.Schema != SchemaVersion {
		return nil, fmt.Errorf("corpus %q: schema %q is not %q — this harness speaks %s only",
			path, c.Schema, SchemaVersion, SchemaVersion)
	}
	if c.Service == "" {
		return nil, fmt.Errorf("corpus %q: service is required", path)
	}
	// Canonicalize every header key up front so comparison never has to think
	// about case again, and detect duplicate entry IDs.
	seen := make(map[string]int, len(c.Entries))
	for i := range c.Entries {
		e := &c.Entries[i]
		if e.ID == "" {
			return nil, fmt.Errorf("corpus %q: entry %d has no id", path, i)
		}
		if prev, ok := seen[e.ID]; ok {
			return nil, fmt.Errorf("corpus %q: duplicate entry id %q (entries %d and %d)", path, e.ID, prev, i)
		}
		seen[e.ID] = i
		e.Request.Headers = canonicalHeaders(e.Request.Headers)
		if e.Request.Method == "" {
			e.Request.Method = http.MethodGet
		}
		e.Request.Method = textproto.CanonicalMIMEHeaderKey(e.Request.Method)
	}
	return &c, nil
}

// Save writes the corpus, sorted by entry ID for stable diffs.
func (c *Corpus) Save(path string) error {
	if c.Schema == "" {
		c.Schema = SchemaVersion
	}
	sort.Slice(c.Entries, func(i, j int) bool { return c.Entries[i].ID < c.Entries[j].ID })
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal corpus: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write corpus %q: %w", path, err)
	}
	return nil
}

// AppendEntry adds an entry, assigning an ID if empty and guarding against
// duplicates. Used by the capture proxy to grow a corpus in place.
func (c *Corpus) AppendEntry(e Entry) error {
	if e.ID == "" {
		e.ID = fmt.Sprintf("entry-%d", len(c.Entries)+1)
	}
	for _, ex := range c.Entries {
		if ex.ID == e.ID {
			return fmt.Errorf("entry id %q already exists in corpus", e.ID)
		}
	}
	e.Request.Headers = canonicalHeaders(e.Request.Headers)
	if e.Request.Method == "" {
		e.Request.Method = http.MethodGet
	}
	e.Request.Method = textproto.CanonicalMIMEHeaderKey(e.Request.Method)
	c.Entries = append(c.Entries, e)
	return nil
}

// ErrEmpty is returned when an operation expects at least one replayable entry.
var ErrEmpty = errors.New("corpus has no entries")

// HasReplayable reports whether the corpus has at least one non-skipped entry.
func (c *Corpus) HasReplayable() bool {
	for _, e := range c.Entries {
		if e.Expect == nil || e.Expect.Skip == "" {
			return true
		}
	}
	return false
}

// canonicalHeaders returns a copy with textproto-canonicalized keys and empty
// value slices dropped, so two corpora recorded with different header casing
// compare equal and no comparison ever re-canonicalizes.
func canonicalHeaders(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, vs := range in {
		ck := textproto.CanonicalMIMEHeaderKey(k)
		var keep []string
		for _, v := range vs {
			if v != "" {
				keep = append(keep, v)
			}
		}
		if len(keep) > 0 {
			out[ck] = keep
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
