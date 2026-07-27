// Package compare is the differential comparison engine: given an incumbent
// response and a SEAM response for the same captured request, decide whether
// they are response-equivalent modulo the enumerated expected diffs.
//
// The contract is fixed by the plan (Testing Strategy → Conformance /
// differential harness):
//
//   - expected diffs, never flagged: the X-SEAM-* response headers SEAM adds
//     (X-SEAM-Spec-Version, X-SEAM-API-Version, X-SEAM-Scope-Version from
//     Phase 7, X-SEAM-Fanout-Partial, X-SEAM-Budget-Remaining, ...), the
//     credential SEAM injects into the upstream request, and any bytes the
//     scrubber redacts to [REDACTED-BY-SEAM];
//   - a redacted credential-echo is a PASS;
//   - a corpus entry returning byte-identically INCLUDING an echoed secret is
//     a FAILURE — a leak.
//
// The leak check is the security-critical invariant ("nothing may leak a
// secret") and is evaluated independently and first, so it can never be masked
// by an expected-diff allowance.
package compare

import (
	"bytes"
	"fmt"
	"net/textproto"
	"sort"
	"strings"

	"github.com/ardenone/seam/tools/diffharness/internal/corpus"
)

// RedactionToken is the exact bytes SEAM's scrubber substitutes for a secret.
// The harness normalizes incumbent bodies/headers with the same token so a
// redacted credential-echo compares equal across the two targets.
const RedactionToken = "[REDACTED-BY-SEAM]"

// Response is a normalized HTTP response for comparison. Header and Trailer
// keys are canonicalized (textproto.CanonicalMIMEHeaderKey) by the caller.
type Response struct {
	Status   int
	Headers  map[string][]string
	Body     []byte
	Trailers map[string][]string
}

// SecretValue is a corpus Secret with its literal value resolved into memory.
// Only Bare is used for matching; the composed forms an upstream might echo
// ("Bearer "+Bare, a header value) all contain Bare, so scanning for Bare is
// both necessary and sufficient for the leak check.
type SecretValue struct {
	Ref      string
	Bare     string
	InjectAs corpus.InjectAs
}

// Options tunes the expected-diff set. The replay layer builds one of these per
// corpus entry by merging the global flags with that entry's corpus.Expect, so a
// route can override status, ignore volatile headers, or skip body comparison
// without a code change.
type Options struct {
	// IgnoreHeaders (canonical names) are dropped from both sides before
	// comparing, for volatile headers that differ call-to-call on the same
	// backend (Date, Server, X-Request-Id, ...). Merged with per-entry Expect.
	IgnoreHeaders []string

	// SeamAddedHeaders (canonical names) are headers SEAM is expected to add and
	// which are therefore removed from BOTH sides before comparison (never
	// required to match). Defaults to the X-SEAM-* family plus the deprecation
	// / sunset / retry-after headers SEAM derives (see DefaultSeamAddedHeaders).
	SeamAddedHeaders []string

	// ExpectedStatus pins the status the SEAM (cutover) target must return. When
	// nil, the plain differential rule applies: incumbent.status == seam.status.
	// When set, it requires seam.status == *ExpectedStatus — the form for a route
	// where SEAM legitimately transforms the status (an _all fan-out that is 207
	// on SEAM but 200 on the incumbent): the incumbent cannot be the oracle for a
	// status it does not itself produce, so the pinned expectation is enforced
	// against the SEAM side and the incumbent value is reported for context only.
	// The incumbent's status is never required to match the pin.
	ExpectedStatus *int

	// IgnoreBody skips body comparison entirely, for responses whose bodies are
	// non-deterministic (timestamps, request IDs embedded in the payload). Use
	// sparingly: a body the differential cannot pin is a body the cutover cannot
	// prove equivalent, and the report calls this out explicitly. The leak check
	// still scans the body regardless — IgnoreBody never weakens "nothing leaks".
	IgnoreBody bool
}

// DefaultSeamAddedHeaders is the family SEAM adds that the plan names as
// expected diffs. Any header matching the X-Seam- prefix is *also* treated as a
// SEAM addition at compare time, so a future X-SEAM-* header needs no edit here.
var DefaultSeamAddedHeaders = []string{
	"Deprecation",
	"Sunset",
	"Link",       // rel="deprecation" pointers SEAM emits on deprecated routes
	"Retry-After", // SEAM-derived on breaker 503 / budget 402 / loop 429
}

// Verdict is the outcome for one entry.
type Verdict string

const (
	VerdictPass Verdict = "PASS"
	VerdictFail Verdict = "FAIL"
	// VerdictSkip is assigned by the replay layer for entries that could not be
	// exercised (unresolved secret ref, network error, Expect.Skip). Compare
	// itself never returns Skip.
	VerdictSkip Verdict = "SKIP"
)

// Result is the outcome of one comparison.
type Result struct {
	Verdict Verdict  `json:"verdict"`
	Reasons []string `json:"reasons,omitempty"`

	// Security invariant — populated even on PASS so the report can state it.
	SecretLeaked bool   `json:"secretLeaked,omitempty"`
	LeakedSecret string `json:"leakedSecret,omitempty"` // the ref whose value leaked
	LeakedWhere  string `json:"leakedWhere,omitempty"`  // body | header:Name | trailer:Name

	// Structural diffs. Nil/zero valued when that dimension is equivalent.
	StatusDiff   *StatusDiff   `json:"statusDiff,omitempty"`
	HeaderDiffs  []HeaderDiff  `json:"headerDiffs,omitempty"`
	TrailerDiffs []HeaderDiff  `json:"trailerDiffs,omitempty"`
	BodyDiff     *BodyDiff     `json:"bodyDiff,omitempty"`

	// BodyIgnored is set when body comparison was suppressed by Options.IgnoreBody
	// for a non-deterministic payload. Informational — never a failure on its own.
	BodyIgnored bool `json:"bodyIgnored,omitempty"`
}

// StatusDiff records a status-code disagreement. When Expected is non-nil the
// comparison was status-pinned (the cutover target was required to return it);
// Incumbent is then context, not a gate.
type StatusDiff struct {
	Incumbent int  `json:"incumbent"`
	Seam      int  `json:"seam"`
	Expected  *int `json:"expected,omitempty"`
}

// HeaderKind names how a header differed.
type HeaderKind string

const (
	HeaderDropped HeaderKind = "dropped" // incumbent sent it, SEAM did not
	HeaderAdded   HeaderKind = "added"   // SEAM sent it, incumbent did not (and it is not an expected X-SEAM-* addition)
	HeaderChanged HeaderKind = "changed" // both sent it, values differ after redaction
)

// HeaderDiff records one header disagreement.
type HeaderDiff struct {
	Name      string     `json:"name"`
	Kind      HeaderKind `json:"kind"`
	Incumbent []string   `json:"incumbent,omitempty"`
	Seam      []string   `json:"seam,omitempty"`
}

// BodyDiff records a body disagreement after secret redaction.
type BodyDiff struct {
	IncumbentLen int    `json:"incumbentLen"`
	SeamLen      int    `json:"seamLen"`
	Preview      string `json:"preview,omitempty"` // first differing region, truncated
}

// Compare evaluates response-equivalence of seam against incumbent.
func Compare(incumbent, seam *Response, secrets []SecretValue, opts Options) Result {
	res := Result{}

	// 0. Normalize options: ignore + seam-added sets, canonicalized.
	ignore := canonSet(opts.IgnoreHeaders)
	seamAdded := canonSet(opts.SeamAddedHeaders)
	for _, h := range DefaultSeamAddedHeaders {
		seamAdded[textproto.CanonicalMIMEHeaderKey(h)] = struct{}{}
	}

	// 1. Leak check — independent, first, never maskable.
	res.SecretLeaked, res.LeakedSecret, res.LeakedWhere = findLeak(seam, secrets)
	if res.SecretLeaked {
		res.Verdict = VerdictFail
		res.Reasons = append(res.Reasons,
			fmt.Sprintf("SECURITY: secret %q leaked into the SEAM response %s — the scrubber did not remove an echoed credential", res.LeakedSecret, res.LeakedWhere))
		// A leak is definitive; structural diffs are still collected below for
		// the report, but the verdict is already locked.
	}

	// Sort secrets longest-bare-first so a secret that is a substring of another
	// does not get partially redacted during normalization.
	sorted := make([]SecretValue, 0, len(secrets))
	for _, s := range secrets {
		if s.Bare == "" {
			continue // empty value: nothing to redact or scan (resolver misuse; caller reports it)
		}
		sorted = append(sorted, s)
	}
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i].Bare) > len(sorted[j].Bare) })

	// 2. Status. Pinned (ExpectedStatus) compares the SEAM side to the pin; the
	// default is the plain differential, incumbent == seam. A 0/0 pair means
	// neither was recorded — treated as unknown and flagged.
	if opts.ExpectedStatus != nil {
		want := *opts.ExpectedStatus
		if seam.Status != want {
			res.StatusDiff = &StatusDiff{Incumbent: incumbent.Status, Seam: seam.Status, Expected: &want}
			res.Reasons = append(res.Reasons,
				fmt.Sprintf("status differs: seam=%d expected=%d (incumbent=%d)", seam.Status, want, incumbent.Status))
		}
	} else {
		statusOK := incumbent.Status == seam.Status && incumbent.Status != 0
		if !statusOK {
			res.StatusDiff = &StatusDiff{Incumbent: incumbent.Status, Seam: seam.Status}
			res.Reasons = append(res.Reasons, fmt.Sprintf("status differs: incumbent=%d seam=%d", incumbent.Status, seam.Status))
		}
	}

	// 3. Headers (redact secrets in values, drop ignore + seam-added).
	res.HeaderDiffs = diffHeaders(incumbent.Headers, seam.Headers, sorted, ignore, seamAdded)
	for _, d := range res.HeaderDiffs {
		res.Reasons = append(res.Reasons, fmt.Sprintf("header %q %s", d.Name, d.Kind))
	}

	// 4. Trailers — same rules.
	res.TrailerDiffs = diffHeaders(incumbent.Trailers, seam.Trailers, sorted, ignore, seamAdded)
	for _, d := range res.TrailerDiffs {
		res.Reasons = append(res.Reasons, fmt.Sprintf("trailer %q %s", d.Name, d.Kind))
	}

	// 5. Body — redact secrets in both, compare bytes. IgnoreBody suppresses the
	// structural body diff for non-deterministic payloads, but the leak scan in
	// step 1 already ran unconditionally, so IgnoreBody never weakens the
	// security invariant.
	if opts.IgnoreBody {
		res.BodyIgnored = true
	} else {
		incBody := redact(incumbent.Body, sorted)
		seamBody := redact(seam.Body, sorted)
		if !bytes.Equal(incBody, seamBody) {
			res.BodyDiff = &BodyDiff{
				IncumbentLen: len(incBody),
				SeamLen:      len(seamBody),
				Preview:      bodyPreview(incBody, seamBody),
			}
			res.Reasons = append(res.Reasons, fmt.Sprintf("body differs after redaction: incumbent=%dB seam=%dB", len(incBody), len(seamBody)))
		}
	}

	if res.Verdict == VerdictFail {
		return res // leak already locked it
	}
	if len(res.Reasons) == 0 {
		res.Verdict = VerdictPass
		return res
	}
	res.Verdict = VerdictFail
	return res
}

// findLeak reports whether any bare secret appears anywhere in the SEAM
// response — body, any header value, any trailer value. The where string
// locates the first hit for the report.
func findLeak(seam *Response, secrets []SecretValue) (leaked bool, ref, where string) {
	if seam == nil {
		return false, "", ""
	}
	// Body first (the common echo path), then headers, then trailers.
	for _, s := range secrets {
		if s.Bare == "" {
			continue
		}
		needle := []byte(s.Bare)
		if bytes.Contains(seam.Body, needle) {
			return true, s.Ref, "body"
		}
		for name, vals := range seam.Headers {
			for _, v := range vals {
				if bytes.Contains([]byte(v), needle) {
					return true, s.Ref, "header:" + name
				}
			}
		}
		for name, vals := range seam.Trailers {
			for _, v := range vals {
				if bytes.Contains([]byte(v), needle) {
					return true, s.Ref, "trailer:" + name
				}
			}
		}
	}
	return false, "", ""
}

// diffHeaders compares two header/trailer maps under the redaction, ignore and
// seam-added rules. A SEAM-added header (X-Seam-* prefix or in seamAdded) is
// dropped from both sides and never compared.
func diffHeaders(inc, seam map[string][]string, secrets []SecretValue, ignore, seamAdded map[string]struct{}) []HeaderDiff {
	names := make(map[string]struct{}, len(inc)+len(seam))
	for k := range inc {
		names[k] = struct{}{}
	}
	for k := range seam {
		names[k] = struct{}{}
	}
	var ordered []string
	for k := range names {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)

	var diffs []HeaderDiff
	for _, name := range ordered {
		if _, drop := ignore[name]; drop {
			continue
		}
		if isSeamAdded(name, seamAdded) {
			continue // expected SEAM addition: ignore on both sides
		}
		iv := redactValues(inc[name], secrets)
		sv := redactValues(seam[name], secrets)
		switch {
		case len(iv) == 0 && len(sv) > 0:
			diffs = append(diffs, HeaderDiff{Name: name, Kind: HeaderAdded, Seam: sv})
		case len(iv) > 0 && len(sv) == 0:
			diffs = append(diffs, HeaderDiff{Name: name, Kind: HeaderDropped, Incumbent: iv})
		case !equalValues(iv, sv):
			diffs = append(diffs, HeaderDiff{Name: name, Kind: HeaderChanged, Incumbent: iv, Seam: sv})
		}
	}
	return diffs
}

// isSeamAdded reports whether name is an expected SEAM response-header
// addition: any X-Seam-* header, or one enumerated in seamAdded.
func isSeamAdded(name string, seamAdded map[string]struct{}) bool {
	if strings.HasPrefix(name, "X-Seam-") {
		return true
	}
	_, ok := seamAdded[name]
	return ok
}

// redact replaces every bare secret occurrence in body with RedactionToken,
// secrets applied longest-first (caller sorts).
func redact(body []byte, secrets []SecretValue) []byte {
	out := body
	for _, s := range secrets {
		out = bytes.ReplaceAll(out, []byte(s.Bare), []byte(RedactionToken))
	}
	return out
}

func redactValues(vals []string, secrets []SecretValue) []string {
	if len(vals) == 0 {
		return nil
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		for _, s := range secrets {
			v = strings.ReplaceAll(v, s.Bare, RedactionToken)
		}
		out = append(out, v)
	}
	return out
}

func equalValues(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	// Order-insensitive multiset compare: HTTP header semantics for repeated
	// fields are order-independent, and a re-merge can reorder.
	tmp := append([]string(nil), b...)
	for _, av := range a {
		j := -1
		for k, bv := range tmp {
			if bv == av {
				j = k
				break
			}
		}
		if j < 0 {
			return false
		}
		tmp = append(tmp[:j], tmp[j+1:]...)
	}
	return true
}

func canonSet(in []string) map[string]struct{} {
	m := make(map[string]struct{}, len(in))
	for _, h := range in {
		m[textproto.CanonicalMIMEHeaderKey(h)] = struct{}{}
	}
	return m
}

// bodyPreview returns a short, safe preview of the first region where the two
// redacted bodies diverge, for the human reading the report. It never includes
// a raw secret (the inputs are already redacted).
func bodyPreview(a, b []byte) string {
	const window = 160
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	start := i - window / 2
	if start < 0 {
		start = 0
	}
	excerpt := func(buf []byte) string {
		end := start + window
		if end > len(buf) {
			end = len(buf)
		}
		if start > len(buf) {
			return ""
		}
		s := string(buf[start:end])
		s = strings.ReplaceAll(s, "\n", "\\n")
		if len(s) > window {
			s = s[:window] + "…"
		}
		return s
	}
	return fmt.Sprintf("@offset %d\n  incumbent: %q\n  seam:      %q", i, excerpt(a), excerpt(b))
}
