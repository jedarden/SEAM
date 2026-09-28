package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardenone/seam/tools/diffharness/internal/corpus"
	"github.com/ardenone/seam/tools/diffharness/internal/secref"
)

// The exit-code contract is X1 in docs/design/differential-replay-contract.md:
// 0 = no FAIL (passes and skips both fine); 1 = at least one FAIL or a harness
// failure; 2 = usage error. seam-cutover gates on this contract alone (G1), so
// it is pinned here at the source rather than only at the consumer.

func writeCorpusFile(t *testing.T, entries []corpus.Entry) string {
	t.Helper()
	cp := &corpus.Corpus{
		Schema:    corpus.SchemaVersion,
		Service:   "contract-test",
		Incumbent: "http://incumbent.invalid",
		Entries:   entries,
	}
	path := filepath.Join(t.TempDir(), "corpus.json")
	if err := cp.Save(path); err != nil {
		t.Fatalf("save corpus: %v", err)
	}
	return path
}

func contractEntry(id, path string) corpus.Entry {
	return corpus.Entry{
		ID:          id,
		Description: "contract test entry " + id,
		Request:     corpus.Request{Method: "GET", Path: path},
	}
}

func replayArgs(corpusPath, incumbent, seam string, extra ...string) []string {
	return append([]string{
		"--incumbent", incumbent,
		"--seam", seam,
		"--corpus", corpusPath,
	}, extra...)
}

func TestRunMainUsageErrorsExit2(t *testing.T) {
	cases := [][]string{
		nil,                         // no flags at all
		{"--incumbent", "http://x"}, // missing --seam and --corpus
		{"--seam", "http://x"},      // missing --incumbent and --corpus
		{"--corpus", "c.json"},      // missing --incumbent and --seam
		{"--incumbent", "http://x", "--seam", "http://y"}, // missing --corpus
		{"--nonsense"}, // unknown flag
	}
	for _, args := range cases {
		var out, errBuf bytes.Buffer
		if code := runMain(args, &out, &errBuf); code != 2 {
			t.Errorf("runMain(%q) = %d, want 2 (usage error)", args, code)
		}
	}
}

func TestRunMainAllPassExits0(t *testing.T) {
	body := `{"items":["alpha"]}`
	incumbent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer incumbent.Close()
	seam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer seam.Close()

	corpusPath := writeCorpusFile(t, []corpus.Entry{contractEntry("list", "/v1/items")})
	reportPath := filepath.Join(t.TempDir(), "report.json")

	var out, errBuf bytes.Buffer
	code := runMain(replayArgs(corpusPath, incumbent.URL, seam.URL, "--report", reportPath), &out, &errBuf)
	if code != 0 {
		t.Fatalf("runMain = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out.String(), errBuf.String())
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(raw), `"passCount": 1`) {
		t.Errorf("report should record one pass:\n%s", raw)
	}
}

func TestRunMainReplayFailureExits1(t *testing.T) {
	incumbent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":["alpha","beta"]}`))
	}))
	defer incumbent.Close()
	seam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":["alpha"]}`)) // divergent body
	}))
	defer seam.Close()

	corpusPath := writeCorpusFile(t, []corpus.Entry{contractEntry("list", "/v1/items")})

	var out, errBuf bytes.Buffer
	code := runMain(replayArgs(corpusPath, incumbent.URL, seam.URL), &out, &errBuf)
	if code != 1 {
		t.Fatalf("runMain = %d, want 1 on a divergent body\nstdout:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "FAIL") {
		t.Errorf("summary should name the FAIL:\n%s", out.String())
	}
}

func TestRunMainMissingCorpusExits1(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runMain(replayArgs(filepath.Join(t.TempDir(), "absent.json"), "http://x", "http://y"), &out, &errBuf)
	if code != 1 {
		t.Fatalf("runMain = %d, want 1 on an unreadable corpus (harness failure)", code)
	}
	if !strings.Contains(errBuf.String(), "load corpus") {
		t.Errorf("stderr should carry the load failure:\n%s", errBuf.String())
	}
}

func TestRunMainAllSkippedCorpusExits1(t *testing.T) {
	// N3 + X1: a corpus whose entries are all skipped proves nothing, and a
	// gate that proved nothing must not read as green — the nothing-to-prove
	// guard is a harness failure (exit 1), not a pass.
	entries := []corpus.Entry{{
		ID:      "not-onboarded",
		Request: corpus.Request{Method: "GET", Path: "/v1/items"},
		Expect:  &corpus.Expect{Skip: "upstream not onboarded"},
	}}
	corpusPath := writeCorpusFile(t, entries)

	var out, errBuf bytes.Buffer
	code := runMain(replayArgs(corpusPath, "http://x", "http://y"), &out, &errBuf)
	if code != 1 {
		t.Fatalf("runMain = %d, want 1 on an all-skipped corpus", code)
	}
	if !strings.Contains(errBuf.String(), "no replayable entries") {
		t.Errorf("stderr should name the nothing-to-prove guard:\n%s", errBuf.String())
	}
}

func TestReplayOneCanonicalizesResponseHeaders(t *testing.T) {
	// Contract C1's transport half: whatever casing the wire carried, the
	// response handed to compare carries textproto-canonical keys — the
	// comparator itself never canonicalizes (pinned in the compare package),
	// so this boundary is the only place it can happen.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set via the raw map so the wire form is deliberately non-canonical.
		w.Header()["x-upstream-case"] = []string{"lower"}
		w.Write([]byte(`ok`))
	}))
	defer srv.Close()

	r := &replayer{client: &http.Client{}}
	resp, err := r.replayOne(srv.URL, contractEntry("probe", "/probe"))
	if err != nil {
		t.Fatalf("replayOne: %v", err)
	}
	if _, ok := resp.Headers["X-Upstream-Case"]; !ok {
		t.Errorf("response headers should carry the canonical key X-Upstream-Case, got %v", resp.Headers)
	}
}

func TestIncumbentFailureSkipsSeamFailureFails(t *testing.T) {
	// Contract F3's transport asymmetry: the differential needs both sides, so
	// an incumbent outage SKIPs the entry, but the cutover target failing to
	// answer is exactly what the gate exists to catch — a FAIL.
	closed := httptest.NewServer(http.NotFoundHandler())
	deadURL := closed.URL
	closed.Close()

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`ok`))
	}))
	defer live.Close()

	entry := contractEntry("probe", "/probe")

	incumbentDown := &replayer{
		incumbentURL: deadURL,
		seamURL:      live.URL,
		client:       &http.Client{},
		resolver:     contractResolver(t),
	}
	er := incumbentDown.replayEntry(entry)
	if er.Verdict != "SKIP" {
		t.Errorf("incumbent outage verdict = %s (%s), want SKIP", er.Verdict, er.SkipReason)
	}

	seamDown := &replayer{
		incumbentURL: live.URL,
		seamURL:      deadURL,
		client:       &http.Client{},
		resolver:     contractResolver(t),
	}
	er = seamDown.replayEntry(entry)
	if er.Verdict != "FAIL" {
		t.Errorf("SEAM outage verdict = %s, want FAIL — the cutover target must answer", er.Verdict)
	}
}

// contractResolver builds the env-only resolver; the entries used here carry
// no secrets, so it is never consulted.
func contractResolver(t *testing.T) *secref.Resolver {
	t.Helper()
	resolver, err := secref.NewResolver("")
	if err != nil {
		t.Fatalf("build resolver: %v", err)
	}
	return resolver
}
