package corpus_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestCorpusJSONDocumentsAreValid keeps every checked-in corpus artifact
// parseable. This includes the large kubectl response snapshots and the
// optional per-endpoint capture files, not just the primary corpus.json files.
func TestCorpusJSONDocumentsAreValid(t *testing.T) {
	root := corpusDirectory(t)
	var paths []string

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk corpus directory: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no JSON corpus documents found")
	}
	t.Logf("validating %d JSON corpus documents", len(paths))

	for _, path := range paths {
		path := path
		relative, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("relative path for %q: %v", path, err)
		}
		t.Run(filepath.ToSlash(relative), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read corpus document: %v", err)
			}
			if len(strings.TrimSpace(string(raw))) == 0 {
				t.Fatal("corpus document is empty; use valid JSON such as [] for an empty capture")
			}
			if !json.Valid(raw) {
				t.Fatal("corpus document is not valid JSON")
			}
		})
	}
}

// TestDifferentialCorpusRequestsAreComplete verifies the request portion of
// the standalone differential corpus format. Responses are intentionally
// observed during replay rather than stored in this format; the server-side
// capture test covers the full request/response pair format.
func TestDifferentialCorpusRequestsAreComplete(t *testing.T) {
	root := corpusDirectory(t)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		base := filepath.Base(path)
		if base != "corpus.json" && base != "corpus-template.json" {
			return nil
		}

		t.Run(filepath.ToSlash(mustRelativePath(t, root, path)), func(t *testing.T) {
			validateDifferentialCorpus(t, path)
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk corpus directory: %v", err)
	}
}

type differentialCorpusDocument struct {
	Schema      string            `json:"schema"`
	Service     string            `json:"service"`
	Incumbent   string            `json:"incumbent"`
	CapturedAt  string            `json:"capturedAt"`
	Description string            `json:"description"`
	Entries     []json.RawMessage `json:"entries"`
}

type differentialCorpusEntry struct {
	ID      string `json:"id"`
	Request struct {
		Method          string              `json:"method"`
		Path            string              `json:"path"`
		BodyB64         string              `json:"bodyB64"`
		Headers         map[string][]string `json:"headers"`
		BodyContentType string              `json:"bodyContentType"`
	} `json:"request"`
}

func validateDifferentialCorpus(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}

	var document differentialCorpusDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode corpus: %v", err)
	}
	if document.Schema != "seam-diff-corpus/v1" || document.Service == "" || document.Description == "" {
		t.Fatalf("corpus metadata is incomplete: schema=%q service=%q description=%q", document.Schema, document.Service, document.Description)
	}
	incumbent, err := url.Parse(document.Incumbent)
	if err != nil || incumbent.Scheme == "" || incumbent.Host == "" {
		t.Fatalf("incumbent must be an absolute URL, got %q", document.Incumbent)
	}
	if _, err := time.Parse(time.RFC3339Nano, document.CapturedAt); err != nil {
		t.Fatalf("capturedAt is not RFC3339: %v", err)
	}
	if len(document.Entries) == 0 {
		t.Fatal("corpus has no entries")
	}

	seenIDs := make(map[string]struct{}, len(document.Entries))
	for index, rawEntry := range document.Entries {
		var entry differentialCorpusEntry
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			t.Fatalf("entry %d is not an object: %v", index, err)
		}
		if entry.ID == "" {
			t.Fatalf("entry %d has no id", index)
		}
		if _, exists := seenIDs[entry.ID]; exists {
			t.Fatalf("entry %d duplicates id %q", index, entry.ID)
		}
		seenIDs[entry.ID] = struct{}{}
		if entry.Request.Method == "" || strings.TrimSpace(entry.Request.Method) == "" {
			t.Fatalf("entry %q has no request method", entry.ID)
		}
		if !strings.HasPrefix(entry.Request.Path, "/") || strings.Contains(entry.Request.Path, "?") {
			t.Fatalf("entry %q has invalid request path %q", entry.ID, entry.Request.Path)
		}
		if entry.Request.BodyB64 != "" {
			if _, err := base64.StdEncoding.DecodeString(entry.Request.BodyB64); err != nil {
				t.Fatalf("entry %q has invalid base64 request body: %v", entry.ID, err)
			}
		}
		for header, values := range entry.Request.Headers {
			if strings.TrimSpace(header) == "" || len(values) == 0 {
				t.Fatalf("entry %q has incomplete request header %q", entry.ID, header)
			}
		}
	}
}

// TestArgoCDCapturedCorpusEntriesAreCompleteAndMockable validates the live
// ArgoCD capture, whose entries are full request/response records. The older
// twitterapi and zai corpora are request-only replay inputs and are therefore
// intentionally covered by validateDifferentialCorpus instead.
func TestArgoCDCapturedCorpusEntriesAreCompleteAndMockable(t *testing.T) {
	document := loadArgoCDCapturedCorpus(t)
	if len(document.Entries) != 8 {
		t.Fatalf("ArgoCD captured corpus has %d entries, want 8", len(document.Entries))
	}

	entriesByID := make(map[string]capturedCorpusEntry, len(document.Entries))
	for _, entry := range document.Entries {
		if entry.ID == "" {
			t.Fatal("captured entry has no id")
		}
		if entry.Request == nil {
			t.Fatalf("captured entry %q has no request", entry.ID)
		}
		if strings.TrimSpace(entry.Request.Method) == "" || !strings.HasPrefix(entry.Request.Path, "/") {
			t.Fatalf("captured entry %q has incomplete request: method=%q path=%q", entry.ID, entry.Request.Method, entry.Request.Path)
		}
		if entry.Response == nil {
			t.Fatalf("captured entry %q has no response", entry.ID)
		}
		if entry.Response.StatusCode < 100 || entry.Response.StatusCode > 599 {
			t.Fatalf("captured entry %q has invalid response status %d", entry.ID, entry.Response.StatusCode)
		}
		if entry.Response.Headers == nil {
			t.Fatalf("captured entry %q has no response headers", entry.ID)
		}
		if entry.Response.BodyB64 == nil {
			t.Fatalf("captured entry %q has no response bodyB64 field", entry.ID)
		}
		body, err := base64.StdEncoding.DecodeString(*entry.Response.BodyB64)
		if err != nil {
			t.Fatalf("captured entry %q has invalid response body base64: %v", entry.ID, err)
		}
		if len(body) == 0 {
			t.Fatalf("captured entry %q has an empty response body", entry.ID)
		}
		if !json.Valid(body) {
			t.Fatalf("captured entry %q response body is not valid JSON", entry.ID)
		}
		entriesByID[entry.ID] = entry
	}

	// Serve one captured response through a real HTTP mock and parse it as a
	// caller would. This proves the persisted response is usable as fixture
	// data, not merely syntactically present in the corpus file.
	sample := entriesByID["applications-list-empty-get"]
	sampleBody := mustCapturedResponseBody(t, sample)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for name, values := range sample.Response.Headers {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(sample.Response.StatusCode)
		_, _ = w.Write(sampleBody)
	}))
	defer mock.Close()

	resp, err := http.Get(mock.URL + "/api/v1/applications?name=mock")
	if err != nil {
		t.Fatalf("request mock response: %v", err)
	}
	defer resp.Body.Close()
	gotBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read mock response: %v", err)
	}
	if resp.StatusCode != sample.Response.StatusCode {
		t.Fatalf("mock response status = %d, want %d", resp.StatusCode, sample.Response.StatusCode)
	}
	if !bytes.Equal(gotBody, sampleBody) {
		t.Fatalf("mock response body differs from captured body")
	}
	var parsed any
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("parse mock response body: %v", err)
	}
	t.Logf("parsed and served captured response %q as a mock (%d bytes)", sample.ID, len(gotBody))
}

// TestArgoCDRouteCatalogIsCoveredByCapturedCorpus is the executable form of
// the route checklist in notes/bf-3w8n.md. Path parameters are represented by
// the concrete success and error requests captured in corpus.json.
func TestArgoCDRouteCatalogIsCoveredByCapturedCorpus(t *testing.T) {
	document := loadArgoCDCapturedCorpus(t)
	paths := make([]string, 0, len(document.Entries))
	for _, entry := range document.Entries {
		if entry.Request != nil {
			paths = append(paths, entry.Request.Method+" "+entry.Request.Path)
		}
	}

	routes := []struct {
		name string
		seen func(string) bool
	}{
		{name: "GET /api/v1/applications", seen: func(route string) bool {
			return route == "GET /api/v1/applications"
		}},
		{name: "GET /api/v1/applications/{app-name}", seen: func(route string) bool {
			return strings.HasPrefix(route, "GET /api/v1/applications/")
		}},
		{name: "GET /api/v1/clusters", seen: func(route string) bool {
			return route == "GET /api/v1/clusters"
		}},
		{name: "GET /api/v1/projects", seen: func(route string) bool {
			return route == "GET /api/v1/projects"
		}},
		{name: "GET /api/v1/repositories", seen: func(route string) bool {
			return route == "GET /api/v1/repositories"
		}},
	}
	for _, route := range routes {
		route := route
		t.Run(route.name, func(t *testing.T) {
			for _, captured := range paths {
				if route.seen(captured) {
					return
				}
			}
			t.Fatalf("route %s is absent from captured corpus", route.name)
		})
	}
}

// TestArgoCDResponseSnapshotsMatchCapturedPairs ensures the convenient
// per-scenario JSON fixtures remain aligned with the response bodies embedded
// in the complete request/response corpus.
func TestArgoCDResponseSnapshotsMatchCapturedPairs(t *testing.T) {
	document := loadArgoCDCapturedCorpus(t)
	entries := make(map[string]capturedCorpusEntry, len(document.Entries))
	for _, entry := range document.Entries {
		entries[entry.ID] = entry
	}

	snapshots := map[string]string{
		"application-detail-actualbudget-nina-ns-ardenone-cluster.json": "application-detail-success-actualbudget-nina-ns-ardenone-cluster-get",
		"application-detail-missing-error.json":                         "application-detail-error-missing-get",
		"applications-list-empty.json":                                  "applications-list-empty-get",
		"applications-list.json":                                        "applications-list-success-get",
		"clusters-list-empty.json":                                      "clusters-list-empty-get",
		"clusters-list.json":                                            "clusters-list-success-get",
		"projects-list.json":                                            "projects-list-success-get",
		"repositories-list.json":                                        "repositories-list-success-get",
	}

	for filename, entryID := range snapshots {
		filename, entryID := filename, entryID
		t.Run(filename, func(t *testing.T) {
			entry, ok := entries[entryID]
			if !ok {
				t.Fatalf("snapshot maps to missing captured entry %q", entryID)
			}
			var want, got any
			if err := json.Unmarshal(mustCapturedResponseBody(t, entry), &want); err != nil {
				t.Fatalf("parse captured response: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(corpusDirectory(t), "argocd-proxy", filename))
			if err != nil {
				t.Fatalf("read response snapshot: %v", err)
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("parse response snapshot: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("response snapshot does not match captured entry %q", entryID)
			}
		})
	}
}

type capturedCorpusDocument struct {
	Entries []capturedCorpusEntry `json:"entries"`
}

type capturedCorpusEntry struct {
	ID       string                  `json:"id"`
	Request  *capturedCorpusRequest  `json:"request"`
	Response *capturedCorpusResponse `json:"response"`
}

type capturedCorpusRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

type capturedCorpusResponse struct {
	StatusCode int                 `json:"statusCode"`
	Headers    map[string][]string `json:"headers"`
	BodyB64    *string             `json:"bodyB64"`
}

func loadArgoCDCapturedCorpus(t *testing.T) capturedCorpusDocument {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(corpusDirectory(t), "argocd-proxy", "corpus.json"))
	if err != nil {
		t.Fatalf("read ArgoCD captured corpus: %v", err)
	}
	var document capturedCorpusDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse ArgoCD captured corpus: %v", err)
	}
	return document
}

func mustCapturedResponseBody(t *testing.T, entry capturedCorpusEntry) []byte {
	t.Helper()
	if entry.Response == nil || entry.Response.BodyB64 == nil {
		t.Fatalf("entry %q has no captured response body", entry.ID)
	}
	body, err := base64.StdEncoding.DecodeString(*entry.Response.BodyB64)
	if err != nil {
		t.Fatalf("decode captured response body for %q: %v", entry.ID, err)
	}
	if len(body) == 0 {
		t.Fatalf("captured response body for %q is empty", entry.ID)
	}
	return body
}

func corpusDirectory(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate corpus test")
	}
	return filepath.Dir(filename)
}

func mustRelativePath(t *testing.T, root, path string) string {
	t.Helper()
	relative, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("relative path for %q: %v", path, err)
	}
	return relative
}
