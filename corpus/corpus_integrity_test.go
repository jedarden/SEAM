package corpus_test

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
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
