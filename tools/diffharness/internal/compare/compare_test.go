package compare

import (
	"net/http"
	"net/textproto"
	"testing"

	"github.com/ardenone/seam/tools/diffharness/internal/corpus"
)

// canon builds a canonical-keyed header map the way the corpus loader and the
// replay layer do, so these tests exercise compare on already-canonical input.
func canon(m map[string][]string) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[textproto.CanonicalMIMEHeaderKey(k)] = v
	}
	return out
}

func secret(ref, bare string) SecretValue {
	return SecretValue{Ref: ref, Bare: bare, InjectAs: corpus.InjectAs{Kind: "bearer"}}
}

func resp(status int, headers map[string][]string, body string) *Response {
	return &Response{Status: status, Headers: canon(headers), Body: []byte(body)}
}

func TestIdenticalResponsesPass(t *testing.T) {
	r := resp(http.StatusOK, map[string][]string{"Content-Type": {"application/json"}}, `{"ok":true}`)
	got := Compare(r, cloneResp(r), nil, Options{})
	if got.Verdict != VerdictPass {
		t.Fatalf("expected PASS, got %v: %v", got.Verdict, got.Reasons)
	}
	if got.SecretLeaked {
		t.Fatalf("false leak reported")
	}
}

func TestSeamAddsXSEAMHeadersIsExpected(t *testing.T) {
	inc := resp(http.StatusOK, map[string][]string{"Content-Type": {"application/json"}}, `{"ok":true}`)
	seam := resp(http.StatusOK, map[string][]string{
		"Content-Type":         {"application/json"},
		"X-Seam-Spec-Version":  {"9f2a…"},
		"X-Seam-API-Version":   {"_unversioned"},
		"X-Seam-Scope-Version": {"v3"}, // Phase 7
	}, `{"ok":true}`)
	got := Compare(inc, seam, nil, Options{})
	if got.Verdict != VerdictPass {
		t.Fatalf("X-SEAM-* additions must be expected, got %v: %v", got.Verdict, got.Reasons)
	}
}

func TestRedactedCredentialEchoIsPass(t *testing.T) {
	// Incumbent proxy does NOT scrub: its error body echoes the live token.
	const token = "sk-live-GLM-9f2c4a8e1b"
	inc := resp(http.StatusUnauthorized, nil, `{"error":"invalid api_key: `+token+`"}`)
	// SEAM scrubs the echoed credential to the redaction token.
	seam := resp(http.StatusUnauthorized, nil, `{"error":"invalid api_key: `+RedactionToken+`"}`)
	got := Compare(inc, seam, []SecretValue{secret("glm", token)}, Options{})
	if got.Verdict != VerdictPass {
		t.Fatalf("redacted credential-echo must be a PASS, got %v: %v", got.Verdict, got.Reasons)
	}
}

func TestEchoedSecretInSeamResponseIsLeakFailure(t *testing.T) {
	// The defining failure mode: SEAM returns byte-identically INCLUDING an
	// echoed secret. This is a leak and a hard FAIL.
	const token = "sk-live-GLM-9f2c4a8e1b"
	inc := resp(http.StatusOK, nil, `{"echo":"`+token+`"}`)
	seam := resp(http.StatusOK, nil, `{"echo":"`+token+`"}`) // NOT redacted
	got := Compare(inc, seam, []SecretValue{secret("glm", token)}, Options{})
	if got.Verdict != VerdictFail {
		t.Fatalf("echoed secret must FAIL, got %v", got.Verdict)
	}
	if !got.SecretLeaked {
		t.Fatalf("SecretLeaked must be true")
	}
	if got.LeakedWhere != "body" {
		t.Fatalf("LeakedWhere = %q, want body", got.LeakedWhere)
	}
}

func TestLeakInHeader(t *testing.T) {
	const token = "twitterapi-bearer-XYZ"
	seam := resp(http.StatusOK, map[string][]string{"X-Upstream-Debug": {token}}, `{"ok":true}`)
	inc := resp(http.StatusOK, nil, `{"ok":true}`)
	got := Compare(inc, seam, []SecretValue{secret("tw", token)}, Options{})
	if !got.SecretLeaked {
		t.Fatalf("expected leak in header, got verdict %v leak=%v", got.Verdict, got.SecretLeaked)
	}
	if got.LeakedWhere != "header:X-Upstream-Debug" {
		t.Fatalf("LeakedWhere = %q", got.LeakedWhere)
	}
}

func TestLeakInTrailer(t *testing.T) {
	const token = "trailer-secret-123"
	seam := &Response{Status: 200, Headers: canon(nil), Body: []byte(`ok`), Trailers: canon(map[string][]string{"X-Debug": {token}})}
	inc := resp(http.StatusOK, nil, `ok`)
	got := Compare(inc, seam, []SecretValue{secret("t", token)}, Options{})
	if !got.SecretLeaked || got.LeakedWhere != "trailer:X-Debug" {
		t.Fatalf("expected trailer leak, got leak=%v where=%q", got.SecretLeaked, got.LeakedWhere)
	}
}

func TestBearerEchoRedactsOnlySecret(t *testing.T) {
	// For kind: bearer the upstream sees "Authorization: Bearer <secret>"; an
	// echo may contain that full string. SEAM redacts the bare secret only,
	// leaving "Bearer [REDACTED-BY-SEAM]". The incumbent (unscrubbed) shows the
	// raw form. After normalization both must match.
	const token = "GLM-9f2c4a8e1b"
	inc := resp(http.StatusUnauthorized, nil, `received Authorization: Bearer `+token)
	seam := resp(http.StatusUnauthorized, nil, `received Authorization: Bearer `+RedactionToken)
	got := Compare(inc, seam, []SecretValue{{Ref: "glm", Bare: token, InjectAs: corpus.InjectAs{Kind: "bearer"}}}, Options{})
	if got.Verdict != VerdictPass {
		t.Fatalf("bearer echo must normalize and PASS, got %v: %v", got.Verdict, got.Reasons)
	}
}

func TestSeamDropsHeaderIsFail(t *testing.T) {
	inc := resp(http.StatusOK, map[string][]string{"Content-Type": {"application/json"}, "X-Trace-Id": {"abc"}}, `ok`)
	seam := resp(http.StatusOK, map[string][]string{"Content-Type": {"application/json"}}, `ok`) // dropped X-Trace-Id
	got := Compare(inc, seam, nil, Options{})
	if got.Verdict != VerdictFail {
		t.Fatalf("dropped header must FAIL, got %v", got.Verdict)
	}
	if len(got.HeaderDiffs) != 1 || got.HeaderDiffs[0].Name != "X-Trace-Id" || got.HeaderDiffs[0].Kind != HeaderDropped {
		t.Fatalf("unexpected header diffs: %+v", got.HeaderDiffs)
	}
}

func TestSeamAddsUnexpectedHeaderIsFail(t *testing.T) {
	inc := resp(http.StatusOK, map[string][]string{"Content-Type": {"application/json"}}, `ok`)
	seam := resp(http.StatusOK, map[string][]string{"Content-Type": {"application/json"}, "X-Surprise": {"1"}}, `ok`)
	got := Compare(inc, seam, nil, Options{})
	if got.Verdict != VerdictFail {
		t.Fatalf("unexpected SEAM-added header must FAIL, got %v", got.Verdict)
	}
	if got.HeaderDiffs[0].Kind != HeaderAdded {
		t.Fatalf("expected HeaderAdded, got %+v", got.HeaderDiffs)
	}
}

func TestStatusDiffIsFail(t *testing.T) {
	inc := resp(http.StatusOK, nil, `ok`)
	seam := resp(http.StatusBadGateway, nil, `ok`)
	got := Compare(inc, seam, nil, Options{})
	if got.Verdict != VerdictFail || got.StatusDiff == nil {
		t.Fatalf("status diff must FAIL with StatusDiff set, got %v %+v", got.Verdict, got.StatusDiff)
	}
}

func TestBodyDiffNonSecretIsFail(t *testing.T) {
	inc := resp(http.StatusOK, nil, `{"items":[1,2,3]}`)
	seam := resp(http.StatusOK, nil, `{"items":[1,2]}`)
	got := Compare(inc, seam, nil, Options{})
	if got.Verdict != VerdictFail || got.BodyDiff == nil {
		t.Fatalf("non-secret body diff must FAIL, got %v %+v", got.Verdict, got.BodyDiff)
	}
}

func TestIgnoreHeaders(t *testing.T) {
	inc := resp(http.StatusOK, map[string][]string{"Date": {"Mon"}, "Content-Type": {"json"}}, `ok`)
	seam := resp(http.StatusOK, map[string][]string{"Date": {"Tue"}, "Content-Type": {"json"}}, `ok`)
	got := Compare(inc, seam, nil, Options{IgnoreHeaders: []string{"Date"}})
	if got.Verdict != VerdictPass {
		t.Fatalf("ignored header must not fail, got %v: %v", got.Verdict, got.Reasons)
	}
}

func TestSubstringSecretRedactsLongestFirst(t *testing.T) {
	// secret A contains secret B as a substring. The incumbent echoes A.
	// Redacting longest-first yields one token for A; a naive shortest-first
	// pass would first redact B inside A and leave a partial. Both must still
	// end up leak-free and equivalent to a fully-redacted SEAM body.
	const long = "abcdef-secret"
	const short = "abc"
	secrets := []SecretValue{
		{Ref: "long", Bare: long},
		{Ref: "short", Bare: short},
	}
	inc := resp(http.StatusOK, nil, `v=`+long+` tail=`+short)
	seam := resp(http.StatusOK, nil, `v=`+RedactionToken+` tail=`+RedactionToken)
	got := Compare(inc, seam, secrets, Options{})
	if got.Verdict != VerdictPass {
		t.Fatalf("overlapping secrets must normalize and PASS, got %v: %v", got.Verdict, got.Reasons)
	}
}

func TestRepeatedHeadersOrderInsensitive(t *testing.T) {
	inc := resp(http.StatusOK, map[string][]string{"Set-Cookie": {"a=1", "b=2"}}, `ok`)
	seam := resp(http.StatusOK, map[string][]string{"Set-Cookie": {"b=2", "a=1"}}, `ok`) // reordered
	got := Compare(inc, seam, nil, Options{})
	// Set-Cookie is order-insensitive in semantics; compare treats it as a multiset.
	if got.Verdict != VerdictPass {
		t.Fatalf("reordered repeated header should PASS, got %v: %v", got.Verdict, got.Reasons)
	}
}

func TestDeprecationHeadersAreExpectedSeamAdditions(t *testing.T) {
	inc := resp(http.StatusOK, map[string][]string{"Content-Type": {"json"}}, `ok`)
	seam := resp(http.StatusOK, map[string][]string{
		"Content-Type": {"json"},
		"Deprecation":  {"@1735689600"},
		"Sunset":       {"Sat, 1 Nov 2025 00:00:00 GMT"},
		"Link":         {`</docs/route?path=/p&version=_unversioned>; rel="deprecation"`},
	}, `ok`)
	got := Compare(inc, seam, nil, Options{})
	if got.Verdict != VerdictPass {
		t.Fatalf("deprecation headers are expected SEAM additions, got %v: %v", got.Verdict, got.Reasons)
	}
}

func TestEmptySecretIsIgnored(t *testing.T) {
	// An empty bare value must never match (it would match everything) — guard.
	seam := resp(http.StatusOK, nil, `ok`)
	inc := resp(http.StatusOK, nil, `ok`)
	got := Compare(inc, seam, []SecretValue{{Ref: "x", Bare: ""}}, Options{})
	if got.SecretLeaked {
		t.Fatalf("empty secret must not report a leak")
	}
	if got.Verdict != VerdictPass {
		t.Fatalf("got %v: %v", got.Verdict, got.Reasons)
	}
}

func TestExpectedStatusPinsSeamSide(t *testing.T) {
	// SEAM transforms a fan-out to 207; the incumbent returns 200. A pinned
	// ExpectedStatus requires seam == pin; the differing incumbent must NOT fail.
	inc := resp(http.StatusOK, nil, `ok`)
	seam := resp(http.StatusMultiStatus, nil, `ok`) // 207
	pin := 207
	got := Compare(inc, seam, nil, Options{ExpectedStatus: &pin})
	if got.Verdict != VerdictPass {
		t.Fatalf("pinned status with seam==pin must PASS, got %v: %v", got.Verdict, got.Reasons)
	}
}

func TestExpectedStatusMismatchFails(t *testing.T) {
	inc := resp(http.StatusOK, nil, `ok`)
	seam := resp(http.StatusBadGateway, nil, `ok`) // seam != pin
	pin := 200
	got := Compare(inc, seam, nil, Options{ExpectedStatus: &pin})
	if got.Verdict != VerdictFail || got.StatusDiff == nil || got.StatusDiff.Expected == nil {
		t.Fatalf("pinned status with seam!=pin must FAIL with Expected set, got %v %+v", got.Verdict, got.StatusDiff)
	}
	if *got.StatusDiff.Expected != 200 {
		t.Fatalf("Expected = %d, want 200", *got.StatusDiff.Expected)
	}
}

func TestIgnoreBodySuppressesStructuralDiff(t *testing.T) {
	// Non-deterministic bodies differ; IgnoreBody must suppress the diff and PASS.
	inc := resp(http.StatusOK, nil, `{"now":"2026-07-22T10:00:00Z","id":"a"}`)
	seam := resp(http.StatusOK, nil, `{"now":"2026-07-22T10:00:05Z","id":"a"}`)
	got := Compare(inc, seam, nil, Options{IgnoreBody: true})
	if got.Verdict != VerdictPass {
		t.Fatalf("IgnoreBody must suppress body diff and PASS, got %v: %v", got.Verdict, got.Reasons)
	}
	if !got.BodyIgnored || got.BodyDiff != nil {
		t.Fatalf("BodyIgnored must be set and BodyDiff nil, got ignored=%v diff=%+v", got.BodyIgnored, got.BodyDiff)
	}
}

func TestIgnoreBodyDoesNotWeakenLeakCheck(t *testing.T) {
	// The defining guarantee: IgnoreBody suppresses the structural body diff but
	// the leak scan still runs and an echoed secret still FAILS.
	const token = "sk-leak-with-ignore-body"
	inc := resp(http.StatusOK, nil, `{"echo":"`+token+`"}`)
	seam := resp(http.StatusOK, nil, `{"echo":"`+token+`"}`) // NOT redacted
	got := Compare(inc, seam, []SecretValue{secret("x", token)}, Options{IgnoreBody: true})
	if got.Verdict != VerdictFail || !got.SecretLeaked {
		t.Fatalf("IgnoreBody must not weaken the leak check, got %v leak=%v", got.Verdict, got.SecretLeaked)
	}
}

func cloneResp(r *Response) *Response {
	out := &Response{Status: r.Status, Body: append([]byte(nil), r.Body...)}
	out.Headers = make(map[string][]string, len(r.Headers))
	for k, v := range r.Headers {
		out.Headers[k] = append([]string(nil), v...)
	}
	return out
}
