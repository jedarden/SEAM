package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardenone/seam/tools/diffharness/internal/corpus"
)

// The runbook's exit-code contract: 0 = no mechanical failures, 1 = at least
// one mechanical FAIL, 2 = usage error.

func TestRunMainUsageErrorsExit2(t *testing.T) {
	cases := [][]string{
		nil,                              // no subcommand
		{"frobnicate"},                   // unknown subcommand
		{"check"},                        // missing required flags
		{"check", "--service", "argocd"}, // missing --seam
		{"rollback"},                     // missing --service
		{"rollback", "--service", "x", "--level", "sideways"},
	}
	for _, args := range cases {
		var out, errBuf bytes.Buffer
		if code := runMain(args, &out, &errBuf); code != 2 {
			t.Errorf("runMain(%q) = %d, want 2 (usage error)", args, code)
		}
	}
}

func runCheckForTest(t *testing.T, extra ...string) (int, string, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := runMain(append([]string{"check", "--service", "testsvc"}, extra...), &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func stubSeam(t *testing.T, spec string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_seam/healthz", "/_seam/readyz":
			w.WriteHeader(http.StatusOK)
		case "/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(spec))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func writeCorpusFile(t *testing.T, entries []corpus.Entry) string {
	t.Helper()
	cp := &corpus.Corpus{
		Schema:    corpus.SchemaVersion,
		Service:   "testsvc",
		Incumbent: "http://127.0.0.1:1",
		Entries:   entries,
	}
	path := filepath.Join(t.TempDir(), "corpus.json")
	if err := cp.Save(path); err != nil {
		t.Fatalf("save corpus: %v", err)
	}
	return path
}

func TestRunMainCheckGoWritesReport(t *testing.T) {
	srv := stubSeam(t, `{"paths": {"/api/v1/applications": {"get": {}}}}`)
	defer srv.Close()
	corpusPath := writeCorpusFile(t, []corpus.Entry{
		{ID: "list-apps", Request: corpus.Request{Method: "GET", Path: "/api/v1/applications"}},
	})
	dir := t.TempDir()
	report := filepath.Join(dir, "nested", "cutover-check.json")

	code, stdout, stderr := runCheckForTest(t,
		"--seam", srv.URL,
		"--corpus", corpusPath,
		"--report", report,
	)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s\nstdout: %s", code, stderr, stdout)
	}
	body, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("report not written: %v", err)
	}
	var rep CheckReport
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	if rep.GoNoGo != "GO" || rep.FailCount != 0 || rep.Service != "testsvc" {
		t.Errorf("report = %s/%d service %s, want GO/0/testsvc", rep.GoNoGo, rep.FailCount, rep.Service)
	}
}

func TestRunMainCheckNoGoExits1(t *testing.T) {
	corpusPath := writeCorpusFile(t, []corpus.Entry{
		{ID: "list-apps", Request: corpus.Request{Method: "GET", Path: "/api/v1/applications"}},
	})
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()

	code, _, _ := runCheckForTest(t, "--seam", url, "--corpus", corpusPath)
	if code != 1 {
		t.Errorf("exit = %d, want 1 (NO-GO: SEAM unreachable)", code)
	}
}

func TestRunMainRollbackLevelFiltering(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runMain([]string{"rollback", "--service", "argocd", "--level", "agent", "--bead", "seam-test"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, errBuf.String())
	}
	got := out.String()
	if !strings.Contains(got, "L1 — agent traffic") {
		t.Error("agent level should print L1")
	}
	if strings.Contains(got, "L2 — fragment") || strings.Contains(got, "L3 — binary") {
		t.Error("agent level should not print L2/L3")
	}
	if !strings.Contains(got, defaultRoutesDir("argocd")) {
		t.Errorf("routes dir should default to the derived per-service path, got: %s", got)
	}
}
