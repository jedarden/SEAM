package main

// The write-contract acceptance check. The evaluator is detection-only since
// seam-d1120e75: it holds no git-host credential, opens no PR, and its entire
// output is one structured log record and one counter per deprecation
// candidate. These tests are the teeth behind that sentence — they fail the
// build if a write path (a git-host SDK, a command exec, a file write, a
// non-GET HTTP method, or runtime egress beyond the read-only VictoriaMetrics
// query) comes back, documented or not.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// forbiddenWritePrimitives are the source shapes a detection-only evaluator
// must never carry. Patterns run against the module's non-test Go sources with
// full-line comments skipped, so prose about this contract cannot trip its own
// guard. The test file carrying this list is itself a _test.go file and is
// excluded from the scan.
var forbiddenWritePrimitives = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"git-host SDK (go-github)", regexp.MustCompile(`go-github`)},
	{"git-host SDK (githubv4)", regexp.MustCompile(`githubv4`)},
	{"git-host SDK (gitea/forgejo/codeberg)", regexp.MustCompile(`code\.gitea\.io|forgejo\.org|codeberg\.org`)},
	{"command execution", regexp.MustCompile(`os/exec|exec\.Command(Context)?`)},
	{"file write", regexp.MustCompile(`os\.(WriteFile|Create|OpenFile)|ioutil\.WriteFile`)},
	{"non-GET http method constant", regexp.MustCompile(`http\.Method(Post|Put|Patch|Delete|Connect|Trace)\b`)},
	{"non-GET http method literal", regexp.MustCompile(`"(POST|PUT|PATCH|DELETE|CONNECT|TRACE)"`)},
}

func TestWriteContractSourcesCarryNoWritePrimitive(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, forbidden := range forbiddenWritePrimitives {
				if forbidden.pattern.MatchString(line) {
					t.Errorf("%s:%d: %s — the evaluator is detection-only (seam-d1120e75); a write path must not return",
						path, i+1, forbidden.name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking module sources: %v", err)
	}
}

// The module must not depend on any git-host SDK: a client library is the
// enabling half of a write path even with no call site.
func TestWriteContractModuleHasNoGitHostDependency(t *testing.T) {
	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		for _, marker := range []string{"go-github", "githubv4", "code.gitea.io", "forgejo", "codeberg"} {
			if strings.Contains(line, marker) {
				t.Errorf("go.mod:%d carries git-host SDK marker %q (%s) — the evaluator is detection-only",
					i+1, marker, strings.TrimSpace(line))
			}
		}
	}
}

// TestWriteContractRuntimeEgressIsReadOnly drives a full evaluation against a
// recording stub of VictoriaMetrics and asserts the evaluator's only egress is
// the read-only query: every observed request is a GET and the run still emits
// its detection. The source-level subtests above pin that no non-GET path can
// exist in this module; this one pins what the process actually does.
func TestWriteContractRuntimeEgressIsReadOnly(t *testing.T) {
	observingLogger(t)

	var mu sync.Mutex
	var methods []string
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		if r.Method != http.MethodGet {
			t.Errorf("outbound request method = %s, want GET only — the evaluator is detection-only", r.Method)
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"route":"/users","spec_version":"abc123"},"value":[1757000000,"0"]},
			{"metric":{"route":"/orders","spec_version":"def456"},"value":[1757000000,"4213"]}
		]}}`)
	}))
	defer vm.Close()

	evaluator := testEvaluator(t, vm.URL)
	if err := evaluator.RunEvaluation(context.Background()); err != nil {
		t.Fatalf("RunEvaluation: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(methods) == 0 {
		t.Fatal("no request observed; the evaluation cannot have queried VictoriaMetrics")
	}
	for i, method := range methods {
		if method != http.MethodGet {
			t.Errorf("request %d method = %s, want GET", i+1, method)
		}
	}

	// The egress above must have happened inside a real evaluation: the quiet
	// route comes out the other end as a detection, not swallowed.
	rendered := evaluator.metrics.render()
	if !strings.Contains(rendered, `candidates_total{route="/users"`) {
		t.Errorf("no detection emitted for the quiet route:\n%s", rendered)
	}
}
