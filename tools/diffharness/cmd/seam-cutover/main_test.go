package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/tools/diffharness/internal/corpus"
)

func newTestClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Second}
}

func verdict(r *CheckReport, name string) string {
	for _, c := range r.Checks {
		if c.Name == name {
			return c.Verdict
		}
	}
	return ""
}

func writeCorpus(t *testing.T, entries []corpus.Entry) string {
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

func entry(id, method, path string) corpus.Entry {
	return corpus.Entry{ID: id, Request: corpus.Request{Method: method, Path: path}}
}

// --- pathMatchesTemplate -----------------------------------------------------

func TestPathMatchesTemplate(t *testing.T) {
	cases := []struct {
		concrete, tmpl string
		want           bool
	}{
		{"/api/v1/applications", "/api/v1/applications", true},
		{"/api/v1/applications/myapp", "/api/v1/applications/{name}", true},
		{"/k8s/iad-ci/api/v1/pods", "/k8s/{cluster}/api/v1/pods", true},
		{"/k8s/{cluster}/api/v1/pods", "/k8s/{cluster}/api/v1/pods", true}, // template equal to itself
		{"/api/v1/applications", "/api/v1/applications/{name}", false},     // segment count differs
		{"/api/v1/clusters", "/api/v1/applications", false},                // literal mismatch
		{"/api/v1//pods", "/api/v1/{res}/pods", false},                     // empty segment must not match a param
		{"/a/b/{x}/c", "/a/{y}/zzz/c", false},
	}
	for _, tc := range cases {
		if got := pathMatchesTemplate(tc.concrete, tc.tmpl); got != tc.want {
			t.Errorf("pathMatchesTemplate(%q, %q) = %v, want %v", tc.concrete, tc.tmpl, got, tc.want)
		}
	}
}

// --- operator-port refusal ---------------------------------------------------

func TestCheckOperatorRefusedPassesOnConnectionFailure(t *testing.T) {
	// A closed server: any connection attempt fails at the transport level,
	// which is what a tailnet ACL refusal looks like from a worker vantage.
	srv := httptest.NewServer(http.NotFoundHandler())
	closedURL := "http://" + srv.Listener.Addr().String()
	srv.Close()

	cfg := checkConfig{operatorURL: closedURL}
	res := checkOperatorRefused(newTestClient(), cfg)
	if res.Verdict != verdictPass {
		t.Errorf("verdict = %s (%s), want PASS", res.Verdict, res.Detail)
	}
}

func TestCheckOperatorRefusedFailsWhenPortAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // even a 403 means the port ANSWERED
	}))
	defer srv.Close()

	cfg := checkConfig{operatorURL: srv.URL}
	res := checkOperatorRefused(newTestClient(), cfg)
	if res.Verdict != verdictFail {
		t.Errorf("verdict = %s, want FAIL — any HTTP response means the ACL granted the operator port", res.Verdict)
	}
	if !strings.Contains(res.Detail, "ANSWERED") {
		t.Errorf("detail should name the problem clearly, got: %s", res.Detail)
	}
}

func TestCheckOperatorRefusedSkipsWhenNotArmed(t *testing.T) {
	res := checkOperatorRefused(newTestClient(), checkConfig{})
	if res.Verdict != verdictSkip {
		t.Errorf("verdict = %s, want SKIP", res.Verdict)
	}
}

// --- endpoint probes ---------------------------------------------------------

func TestCheckEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_seam/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if res := checkEndpoint(newTestClient(), "seam-healthz", srv.URL+"/_seam/healthz"); res.Verdict != verdictPass {
		t.Errorf("healthz verdict = %s, want PASS", res.Verdict)
	}
	if res := checkEndpoint(newTestClient(), "seam-readyz", srv.URL+"/_seam/readyz"); res.Verdict != verdictFail {
		t.Errorf("readyz verdict = %s, want FAIL on 503", res.Verdict)
	}
	if res := checkEndpoint(newTestClient(), "seam-healthz", srv.URL+"://bad"); res.Verdict != verdictFail {
		t.Errorf("unreachable verdict = %s, want FAIL", res.Verdict)
	}
}

// --- DNS ---------------------------------------------------------------------

func TestCheckDNS(t *testing.T) {
	if res := checkDNS("seam-resolves", "http://127.0.0.1:8080"); res.Verdict != verdictPass {
		t.Errorf("loopback verdict = %s (%s), want PASS", res.Verdict, res.Detail)
	}
	if res := checkDNS("seam-resolves", "http://this-host-does-not-exist-anywhere.invalid"); res.Verdict != verdictFail {
		t.Errorf("nonexistent verdict = %s, want FAIL", res.Verdict)
	}
}

// --- routes-in-spec ----------------------------------------------------------

func serveSpec(t *testing.T, specBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_seam/healthz", "/_seam/readyz":
			w.WriteHeader(http.StatusOK)
		case "/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(specBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestCheckRoutesInSpecPassesWhenAllCorpusRoutesServed(t *testing.T) {
	srv := serveSpec(t, `{"paths": {
		"/api/v1/applications": {"get": {}},
		"/api/v1/applications/{name}": {"get": {}},
		"/api/v1/clusters": {"get": {}}
	}}`)
	defer srv.Close()

	cp := &corpus.Corpus{Entries: []corpus.Entry{
		entry("list-apps", "GET", "/api/v1/applications"),
		entry("get-app", "GET", "/api/v1/applications/myapp"),
		{ID: "skipped", Request: corpus.Request{Method: "GET", Path: "/not/on/seam"}, Expect: &corpus.Expect{Skip: "upstream not onboarded"}},
	}}
	res := checkRoutesInSpec(newTestClient(), checkConfig{seamURL: srv.URL}, cp, nil)
	if res.Verdict != verdictPass {
		t.Errorf("verdict = %s (%s), want PASS — skipped entries must not count", res.Verdict, res.Detail)
	}
}

func TestCheckRoutesInSpecFailsNamingMissingEntries(t *testing.T) {
	srv := serveSpec(t, `{"paths": {"/api/v1/applications": {"get": {}}}}`)
	defer srv.Close()

	cp := &corpus.Corpus{Entries: []corpus.Entry{
		entry("list-apps", "GET", "/api/v1/applications"),
		entry("list-clusters", "GET", "/api/v1/clusters"),
		entry("post-app", "POST", "/api/v1/applications"),
	}}
	res := checkRoutesInSpec(newTestClient(), checkConfig{seamURL: srv.URL}, cp, nil)
	if res.Verdict != verdictFail {
		t.Fatalf("verdict = %s, want FAIL", res.Verdict)
	}
	for _, want := range []string{"list-clusters", "post-app"} {
		if !strings.Contains(res.Detail, want) {
			t.Errorf("detail should name missing entry %q, got: %s", want, res.Detail)
		}
	}
	if strings.Contains(res.Detail, "list-apps") {
		t.Errorf("detail should not name served entry list-apps, got: %s", res.Detail)
	}
}

func TestCheckRoutesInSpecFailsOnUnreachableSpec(t *testing.T) {
	srv := serveSpec(t, `{}`)
	url := srv.URL
	srv.Close()
	res := checkRoutesInSpec(newTestClient(), checkConfig{seamURL: url}, &corpus.Corpus{}, nil)
	if res.Verdict != verdictFail {
		t.Errorf("verdict = %s, want FAIL on unreachable /openapi.json", res.Verdict)
	}
}

// --- agent-doc prose guard ---------------------------------------------------

func TestCheckAgentDoc(t *testing.T) {
	dir := t.TempDir()

	present := filepath.Join(dir, "CLAUDE.md")
	os.WriteFile(present, []byte("use https://argocd-ro-ardenone-manager-ts.ardenone.com:8444/api/v1/applications\n"), 0o644)
	deleted := filepath.Join(dir, "CLAUDE-deleted.md")
	os.WriteFile(deleted, []byte("# nothing here anymore\n"), 0o644)

	incumbent := "https://argocd-ro-ardenone-manager-ts.ardenone.com:8444"

	if res := checkAgentDoc(checkConfig{agentDoc: present, incumbentURL: incumbent}); res.Verdict != verdictPass {
		t.Errorf("present prose verdict = %s (%s), want PASS", res.Verdict, res.Detail)
	}
	res := checkAgentDoc(checkConfig{agentDoc: deleted, incumbentURL: incumbent})
	if res.Verdict != verdictFail {
		t.Errorf("deleted prose verdict = %s, want FAIL", res.Verdict)
	}
	if !strings.Contains(res.Detail, "already deleted") {
		t.Errorf("detail should say the prose appears deleted, got: %s", res.Detail)
	}
	if res := checkAgentDoc(checkConfig{}); res.Verdict != verdictSkip {
		t.Errorf("not-armed verdict = %s, want SKIP", res.Verdict)
	}
	if res := checkAgentDoc(checkConfig{agentDoc: filepath.Join(dir, "nope.md")}); res.Verdict != verdictFail {
		t.Errorf("missing file verdict = %s, want FAIL", res.Verdict)
	}
}

// --- replay subprocess -------------------------------------------------------

func fakeReplayBin(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-seam-replay")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}
	return path
}

func TestCheckReplayGatesOnExitCode(t *testing.T) {
	cfg := checkConfig{
		replayBin:    fakeReplayBin(t, "exit 0"),
		incumbentURL: "http://127.0.0.1:1",
		seamURL:      "http://127.0.0.1:2",
		corpusPath:   "corpus.json",
		replayReport: filepath.Join(t.TempDir(), "replay.json"),
	}
	if res := checkReplay(cfg); res.Verdict != verdictPass {
		t.Errorf("exit-0 verdict = %s (%s), want PASS", res.Verdict, res.Detail)
	}

	cfg.replayBin = fakeReplayBin(t, "echo 'FAIL list-apps: status diff' >&2; exit 1")
	res := checkReplay(cfg)
	if res.Verdict != verdictFail {
		t.Errorf("exit-1 verdict = %s, want FAIL", res.Verdict)
	}
	if !strings.Contains(res.Detail, "status diff") {
		t.Errorf("detail should carry replay output tail, got: %s", res.Detail)
	}

	if res := checkReplay(checkConfig{}); res.Verdict != verdictSkip {
		t.Errorf("not-armed verdict = %s, want SKIP", res.Verdict)
	}
}

// --- full gate ---------------------------------------------------------------

func TestRunChecksAggregatesToGo(t *testing.T) {
	spec := `{"paths": {"/api/v1/applications": {"get": {}}}}`
	srv := serveSpec(t, spec)
	defer srv.Close()

	dir := t.TempDir()
	doc := filepath.Join(dir, "CLAUDE.md")
	os.WriteFile(doc, []byte("proxy: http://127.0.0.1:9\n"), 0o644)
	corpusPath := writeCorpus(t, []corpus.Entry{entry("list-apps", "GET", "/api/v1/applications")})

	cfg := checkConfig{
		service:      "testsvc",
		seamURL:      srv.URL,
		incumbentURL: "http://127.0.0.1:9",
		corpusPath:   corpusPath,
		agentDoc:     doc,
	}
	rep := runChecks(cfg)

	if rep.GoNoGo != "GO" || rep.FailCount != 0 {
		t.Fatalf("GoNoGo = %s fail = %d, want GO/0; checks: %+v", rep.GoNoGo, rep.FailCount, rep.Checks)
	}
	if verdict(rep, "seam-healthz") != verdictPass || verdict(rep, "seam-readyz") != verdictPass {
		t.Error("health probes should pass against the stub")
	}
	if verdict(rep, "routes-in-spec") != verdictPass {
		t.Error("routes-in-spec should pass")
	}
	// Not armed: operator URL and replay bin absent.
	if verdict(rep, "operator-port-refused") != verdictSkip || verdict(rep, "corpus-replay") != verdictSkip {
		t.Error("unarmed checks should SKIP, not silently pass")
	}
	if verdict(rep, "agent-retry-budget") != verdictManual {
		t.Error("retry budget is a MANUAL item")
	}
	if rep.ManualCount != 1 {
		t.Errorf("manual count = %d, want 1 (non-metered service)", rep.ManualCount)
	}
}

func TestRunChecksNoGoWhenServiceUnready(t *testing.T) {
	srv := serveSpec(t, `{"paths": {"/api/v1/applications": {"get": {}}}}`)
	defer srv.Close()
	corpusPath := writeCorpus(t, []corpus.Entry{entry("list-apps", "GET", "/api/v1/not-served")})

	// Point --seam at a closed port: healthz, readyz, routes-in-spec all fail.
	closed := httptest.NewServer(http.NotFoundHandler())
	deadURL := closed.URL
	closed.Close()

	rep := runChecks(checkConfig{
		service:      "testsvc",
		seamURL:      deadURL,
		incumbentURL: "http://127.0.0.1:9",
		corpusPath:   corpusPath,
	})
	if rep.GoNoGo != "NO-GO" || rep.FailCount == 0 {
		t.Fatalf("GoNoGo = %s fail = %d, want NO-GO with failures", rep.GoNoGo, rep.FailCount)
	}
}

func TestRunChecksMeteredAddsPhase13ManualItem(t *testing.T) {
	srv := serveSpec(t, `{}`)
	defer srv.Close()
	cfg := checkConfig{
		service:      "zai",
		seamURL:      srv.URL,
		incumbentURL: "http://127.0.0.1:9",
		corpusPath:   writeCorpus(t, []corpus.Entry{entry("e", "GET", "/x")}),
		metered:      true,
	}
	rep := runChecks(cfg)
	if verdict(rep, "phase13-cost-governor") != verdictManual {
		t.Error("metered service should carry the Phase 13 MANUAL item")
	}
}

// --- rollback printing -------------------------------------------------------

func TestPrintRollbackAllLevels(t *testing.T) {
	cfg := rollbackConfig{
		service:   "argocd",
		level:     "all",
		routesDir: "k8s/rs-manager/seam/routes/argocd",
		docsRepo:  "/home/coding/SEAM",
		proseFile: "CLAUDE.md",
		bead:      "seam-172b04c0",
	}
	var b strings.Builder
	printRollback(cfg, &b)
	out := b.String()

	for _, want := range []string{
		"L1 — agent traffic",
		"L2 — fragment",
		"L3 — binary",
		"git revert",                        // the mechanism, everywhere
		"never a live mutation",             // the binding rule
		"k8s/rs-manager/seam/routes/argocd", // routes dir substituted
		"seam-172b04c0",                     // bead named in the search command
		"/_seam/readyz",                     // L3's mechanical 2-minute trigger
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rollback output missing %q", want)
		}
	}
	for _, banned := range []string{"kubectl rollout undo", "kubectl apply", "kubectl edit"} {
		if strings.Contains(out, banned) {
			t.Errorf("rollback output must not recommend %q", banned)
		}
	}
}

func TestPrintRollbackSingleLevel(t *testing.T) {
	var b strings.Builder
	printRollback(rollbackConfig{service: "argocd", level: "agent"}, &b)
	out := b.String()
	if !strings.Contains(out, "L1 — agent traffic") {
		t.Error("agent level should print L1")
	}
	if strings.Contains(out, "L2 — fragment") || strings.Contains(out, "L3 — binary") {
		t.Error("agent level should not print L2/L3")
	}
	if !strings.Contains(out, "k8s/rs-manager/seam/routes/argocd") {
		t.Error("routes dir default should be derived from the service name")
	}
}

// --- helpers -----------------------------------------------------------------

func TestDeriveReplayReport(t *testing.T) {
	if got := deriveReplayReport(""); got != "cutover-replay.json" {
		t.Errorf("empty report path → %q, want cutover-replay.json", got)
	}
	if got := deriveReplayReport("corpus/argocd/cutover-check.json"); got != "corpus/argocd/cutover-check-replay.json" {
		t.Errorf("derived = %q, want cutover-check-replay.json beside the report", got)
	}
}
