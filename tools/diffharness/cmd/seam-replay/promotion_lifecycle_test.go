package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ardenone/seam/tools/diffharness/internal/corpus"
	"github.com/ardenone/seam/tools/diffharness/internal/secref"
)

const (
	promotionRedactionMarker = "[REDACTED-BY-SEAM]"
	promotionFixtureName     = "capture-promotion-lifecycle.json"
)

// TestPromotedFixtureReplaysAndCollectsFreshResponse exercises the complete
// handoff represented by capture-promotion-lifecycle.json: the checked-in
// artifact retains only redacted request data and replay policy, while replay
// collects a fresh response from each target instead of consulting a stored
// capture-time response.
func TestPromotedFixtureReplaysAndCollectsFreshResponse(t *testing.T) {
	fixturePath := promotionFixturePath(t)
	persisted, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read checked-in promotion fixture: %v", err)
	}
	for _, forbidden := range []string{
		"standalone-capture-query-not-a-credential",
		"Bearer seam-capture-records-verbatim-not-a-credential",
		"fixture-promotion-response-sentinel",
	} {
		if bytes.Contains(persisted, []byte(forbidden)) {
			t.Fatalf("checked-in fixture contains unsanitized promotion value %q", forbidden)
		}
	}
	if bytes.Contains(persisted, []byte(`"response"`)) {
		t.Fatal("checked-in fixture retains a capture-time response")
	}

	fixture, err := corpus.Load(fixturePath)
	if err != nil {
		t.Fatalf("load checked-in promotion fixture: %v", err)
	}
	if len(fixture.Entries) != 1 {
		t.Fatalf("promotion fixture entries = %d, want 1", len(fixture.Entries))
	}
	entry := fixture.Entries[0]
	if entry.Response != nil {
		t.Fatal("promoted entry has a capture-time response")
	}
	if entry.Expect == nil || len(entry.Expect.IgnoreHeaders) == 0 {
		t.Fatalf("promoted entry expectation = %+v, want replay header policy", entry.Expect)
	}
	if got := entry.Request.Headers["Authorization"]; len(got) != 1 || got[0] != promotionRedactionMarker {
		t.Fatalf("promoted Authorization = %v, want redaction marker", got)
	}
	if got := entry.Request.Headers["X-Capture-Token"]; len(got) != 1 || got[0] != promotionRedactionMarker {
		t.Fatalf("promoted X-Capture-Token = %v, want redaction marker", got)
	}
	if entry.Request.Query != "api_key=%5BREDACTED-BY-SEAM%5D" {
		t.Fatalf("promoted query = %q, want escaped redaction marker", entry.Request.Query)
	}

	const freshBody = `{"items":["fresh-from-replay"]}`
	newTarget := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v1/clusters" || r.URL.RawQuery != entry.Request.Query {
				t.Errorf("%s received request %s %s?%s", name, r.Method, r.URL.Path, r.URL.RawQuery)
			}
			if got := r.Header.Get("Authorization"); got != promotionRedactionMarker {
				t.Errorf("%s Authorization = %q, want redaction marker", name, got)
			}
			if got := r.Header.Get("X-Capture-Token"); got != promotionRedactionMarker {
				t.Errorf("%s X-Capture-Token = %q, want redaction marker", name, got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Target", "fresh")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, freshBody)
		}))
	}
	incumbent := newTarget("incumbent")
	defer incumbent.Close()
	seam := newTarget("seam")
	defer seam.Close()

	// The resolver value is an explicit test placeholder, not a credential. It
	// proves that the fixture's reference is resolved only for replay and never
	// serialized into the fixture.
	t.Setenv("SEAM_DIFF_SECRET_VAULT_RS_MANAGER_RS_MANAGER_SEAM_ROUTES_ARGOCD_RO_RO_TOKEN", "example")
	resolver, err := secref.NewResolver("")
	if err != nil {
		t.Fatalf("create replay resolver: %v", err)
	}
	replay := &replayer{
		incumbentURL: incumbent.URL,
		seamURL:      seam.URL,
		corpusPath:   fixturePath,
		cp:           fixture,
		resolver:     resolver,
	}
	report := replay.run()
	if report.PassCount != 1 || report.FailCount != 0 || report.SkipCount != 0 {
		t.Fatalf("promotion replay report = pass %d fail %d skip %d entries=%+v, want one pass",
			report.PassCount, report.FailCount, report.SkipCount, report.Entries)
	}
	if report.Entries[0].Verdict != "PASS" {
		t.Fatalf("promotion replay verdict = %s, want PASS", report.Entries[0].Verdict)
	}

	// Inspect the transport boundary directly as well: replay collected this
	// fresh response from the target, rather than loading a response from the
	// fixture or treating the capture-time response as an oracle.
	replay.client = &http.Client{}
	collected, err := replay.replayOne(incumbent.URL, entry)
	if err != nil {
		t.Fatalf("collect replay response: %v", err)
	}
	if collected.Status != http.StatusOK || !bytes.Equal(collected.Body, []byte(freshBody)) {
		t.Fatalf("collected response = status %d body %q, want 200 %q", collected.Status, collected.Body, freshBody)
	}
	if got := collected.Headers["X-Target"]; len(got) != 1 || got[0] != "fresh" {
		t.Fatalf("collected response headers = %v, want X-Target fresh", collected.Headers)
	}
}

func promotionFixturePath(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed while locating promotion fixture")
	}
	return filepath.Join(filepath.Dir(source), "..", "..", "testdata", promotionFixtureName)
}
