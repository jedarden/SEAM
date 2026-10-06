package corpus

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCapturePromotionWorkflowContract keeps the executable validation tied to
// the two documents that define the workflow. A renamed, reordered, or
// unlinked step should fail this focused command before a capture is promoted
// by muscle memory into the fixture directory.
func TestCapturePromotionWorkflowContract(t *testing.T) {
	captureDoc := readPromotionDoc(t, "docs", "capture_testing.md")
	alignedDoc := readPromotionDoc(t, "docs", "design", "aligned-capture-replay-schema-contract.md")

	assertInOrder(t, captureDoc, []string{
		"### 1. Capture into a private runtime artifact",
		"### 2. Build a private sanitized candidate",
		"### 3. Apply the fail-closed gate",
		"### 4. Human reviewer checkpoint — before fixture creation",
		"### 5. Create and validate the fixture",
	})
	assertInOrder(t, captureDoc, []string{
		"### 1. Collect the response into private capture storage",
		"### 2. Sanitize before any review or non-private persistence",
		"### 3. Promote only request data and explicit expectations",
		"### 4. Reject unsanitized response material fail-closed",
		"### 5. Replay against fresh target responses",
	})

	for _, ref := range []string{
		"design/aligned-capture-replay-schema-contract.md",
		"design/aligned-capture-replay-schema-contract.md#2-private-sanitized-candidate",
		"TestPromotionLifecycleConvertsCaptureToFixture",
		"TestPromotionBoundaryRejectsUnsanitizedRequestAndResponse",
	} {
		if !strings.Contains(captureDoc, ref) {
			t.Errorf("capture runbook lost required validation/schema reference %q", ref)
		}
	}

	for _, heading := range []string{
		"## Implementation sources of truth",
		"### 1. Private runtime capture",
		"### 2. Private sanitized candidate",
		"### 3. Checked-in replay fixture",
		"### 4. Replay output",
		"## Capture-to-replay field mapping",
		"## Security invariants",
	} {
		if !strings.Contains(alignedDoc, heading) {
			t.Errorf("aligned schema contract lost required section %q", heading)
		}
	}
	for _, field := range []string{
		"`response.statusCode`",
		"`response.headers`",
		"`response.bodyB64`",
		"`secrets[].ref`",
		"`request.bodyB64`",
	} {
		if !strings.Contains(alignedDoc, field) {
			t.Errorf("aligned schema contract lost field mapping %q", field)
		}
	}
}

// TestPromotionBoundaryRejectsUnsanitizedRequestAndResponse covers both sides of the promotion
// boundary. Marker-only request data and a response that is dropped during
// promotion are accepted. Literal request or response material is rejected,
// including values hidden in a base64 body.
func TestPromotionBoundaryRejectsUnsanitizedRequestAndResponse(t *testing.T) {
	const (
		requestSentinel  = "fixture-boundary-request-sentinel"
		responseSentinel = "fixture-boundary-response-sentinel"
	)

	redactedCapture := &Corpus{
		Schema:    SchemaVersion,
		Service:   "argocd-ro",
		Incumbent: "https://incumbent.example.invalid",
		Entries: []Entry{{
			ID: "redacted",
			Request: Request{
				Method: "GET",
				Path:   "/api/v1/items",
				Query:  "api_key=" + url.QueryEscape(captureRedactionMarker),
				Headers: map[string][]string{
					"Authorization": {captureRedactionMarker},
				},
				BodyB64: base64.StdEncoding.EncodeToString([]byte(`{"request":"` + captureRedactionMarker + `"}`)),
			},
			Response: &Response{
				Headers: map[string][]string{
					"Set-Cookie": {captureRedactionMarker},
				},
				BodyB64: base64.StdEncoding.EncodeToString([]byte(`{"response":"` + captureRedactionMarker + `"}`)),
			},
			Secrets: []Secret{{Ref: "vault:" + DefaultVaultBaseDir + "/argocd-ro/ro-token", InjectAs: InjectAs{Kind: "bearer"}}},
		}},
	}

	// A marker is safe at a credential location, but the capture-time response
	// is not a fixture field. Promotion drops it before the candidate check.
	redactedFixture := cloneCorpus(redactedCapture)
	redactedFixture.Entries[0].Response = nil
	if err := validatePromotedFixtureBoundary(redactedFixture); err != nil {
		t.Fatalf("marker-redacted request and dropped response should pass: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*Corpus)
		wantErr string
	}{
		{
			name: "request header literal",
			mutate: func(c *Corpus) {
				c.Entries[0].Response = nil
				c.Entries[0].Request.Headers["Authorization"] = []string{"Bearer " + requestSentinel}
			},
			wantErr: "request header",
		},
		{
			name: "request query literal",
			mutate: func(c *Corpus) {
				c.Entries[0].Response = nil
				c.Entries[0].Request.Query = "api_key=" + url.QueryEscape(requestSentinel)
			},
			wantErr: "request query",
		},
		{
			name: "request body literal",
			mutate: func(c *Corpus) {
				c.Entries[0].Response = nil
				c.Entries[0].Request.BodyB64 = base64.StdEncoding.EncodeToString([]byte(`{"credential":"` + requestSentinel + `"}`))
			},
			wantErr: "request body",
		},
		{
			name: "response header retained",
			mutate: func(c *Corpus) {
				c.Entries[0].Response.Headers["Set-Cookie"] = []string{responseSentinel}
			},
			wantErr: "capture-time response",
		},
		{
			name: "response body retained",
			mutate: func(c *Corpus) {
				c.Entries[0].Response.BodyB64 = base64.StdEncoding.EncodeToString([]byte(`{"credential":"` + responseSentinel + `"}`))
			},
			wantErr: "capture-time response",
		},
		{
			name: "resolved secret retained",
			mutate: func(c *Corpus) {
				c.Entries[0].Response = nil
				c.Entries[0].Secrets[0].Bare = requestSentinel
			},
			wantErr: "resolved secret",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := cloneCorpus(redactedCapture)
			tc.mutate(candidate)
			err := validatePromotedFixtureBoundary(candidate, requestSentinel, responseSentinel)
			if err == nil {
				t.Fatalf("unsafe candidate crossed fixture boundary")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("boundary error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func validatePromotedFixtureBoundary(c *Corpus, forbidden ...string) error {
	for _, entry := range c.Entries {
		if entry.Response != nil {
			return fmt.Errorf("entry %q carries a capture-time response", entry.ID)
		}
		for j, secret := range entry.Secrets {
			if secret.Bare != "" {
				return fmt.Errorf("entry %q secrets[%d] carries a resolved secret", entry.ID, j)
			}
		}
		for name, values := range entry.Request.Headers {
			if !isCredentialHeader(name) {
				continue
			}
			for _, value := range values {
				if value != "" && value != captureRedactionMarker {
					return fmt.Errorf("entry %q request header %q is not redacted", entry.ID, name)
				}
			}
		}
		query, err := url.ParseQuery(entry.Request.Query)
		if err != nil {
			return fmt.Errorf("entry %q request query is not decodable: %w", entry.ID, err)
		}
		for name, values := range query {
			if !isCredentialQueryName(name) {
				continue
			}
			for _, value := range values {
				if value != "" && value != captureRedactionMarker {
					return fmt.Errorf("entry %q request query %q is not redacted", entry.ID, name)
				}
			}
		}
		if entry.Request.BodyB64 != "" {
			body, err := base64.StdEncoding.DecodeString(entry.Request.BodyB64)
			if err != nil {
				return fmt.Errorf("entry %q request body is not decodable: %w", entry.ID, err)
			}
			if containsForbidden(string(body), forbidden...) {
				return fmt.Errorf("entry %q request body contains unredacted sensitive data", entry.ID)
			}
		}
	}

	serialized, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal candidate: %w", err)
	}
	if containsForbidden(string(serialized), forbidden...) {
		return fmt.Errorf("candidate contains unredacted sensitive data")
	}
	return nil
}

func isCredentialHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key", "x-auth-token":
		return true
	default:
		return false
	}
}

func isCredentialQueryName(name string) bool {
	switch strings.ToLower(name) {
	case "api_key", "api-key", "apikey", "access_token", "access-token", "auth_token", "auth-token":
		return true
	default:
		return false
	}
}

func containsForbidden(value string, forbidden ...string) bool {
	for _, needle := range forbidden {
		if needle != "" && strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func cloneCorpus(c *Corpus) *Corpus {
	var clone Corpus
	raw, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(raw, &clone); err != nil {
		panic(err)
	}
	return &clone
}

func readPromotionDoc(t *testing.T, parts ...string) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed while locating promotion documents")
	}
	path := filepath.Join(append([]string{filepath.Dir(source), "..", "..", "..", ".."}, parts...)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read promotion contract %s: %v", filepath.Join(parts...), err)
	}
	return string(raw)
}

func assertInOrder(t *testing.T, document string, markers []string) {
	t.Helper()
	start := 0
	for _, marker := range markers {
		offset := strings.Index(document[start:], marker)
		if offset < 0 {
			t.Fatalf("document is missing ordered marker %q", marker)
		}
		start += offset + len(marker)
	}
}
