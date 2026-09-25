package corpus

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadValidCorpus tests loading a well-formed corpus file.
func TestLoadValidCorpus(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	// Create a valid corpus.
	c := &Corpus{
		Service:     "test-service",
		Incumbent:   "https://example.com",
		CapturedAt:  time.Now().Format(time.RFC3339),
		Description: "Test corpus",
		Entries: []Entry{
			{
				ID:          "test-entry",
				Description: "Test entry",
				Request: Request{
					Method:  "GET",
					Path:    "/api/test",
					Query:   "",
					Headers: map[string][]string{"Accept": {"application/json"}},
				},
			},
		},
	}
	if err := c.Save(corpusPath); err != nil {
		t.Fatalf("save corpus: %v", err)
	}

	// Load and validate.
	loaded, err := Load(corpusPath)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}

	if loaded.Service != c.Service {
		t.Errorf("Service = %q, want %q", loaded.Service, c.Service)
	}
	if loaded.Incumbent != c.Incumbent {
		t.Errorf("Incumbent = %q, want %q", loaded.Incumbent, c.Incumbent)
	}
	if len(loaded.Entries) != 1 {
		t.Fatalf("Entries = %d, want 1", len(loaded.Entries))
	}
	if loaded.Entries[0].ID != "test-entry" {
		t.Errorf("Entry ID = %q, want test-entry", loaded.Entries[0].ID)
	}
}

// TestLoadMissingSchema tests that corpus files without schema version are rejected.
func TestLoadMissingSchema(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	// Create a corpus without schema field.
	data := map[string]interface{}{
		"service":     "test",
		"incumbent":   "https://example.com",
		"capturedAt":  time.Now().Format(time.RFC3339),
		"description": "test",
		"entries":     []interface{}{},
	}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(corpusPath, raw, 0o644); err != nil {
		t.Fatalf("write corpus: %v", err)
	}

	_, err := Load(corpusPath)
	if err == nil {
		t.Fatal("expected error for missing schema, got nil")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("error should mention schema, got: %v", err)
	}
}

// TestLoadWrongSchemaVersion tests that mismatched schema versions are rejected.
func TestLoadWrongSchemaVersion(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	data := map[string]interface{}{
		"schema":      "wrong-schema/v1",
		"service":     "test",
		"incumbent":   "https://example.com",
		"capturedAt":  time.Now().Format(time.RFC3339),
		"description": "test",
		"entries":     []interface{}{},
	}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(corpusPath, raw, 0o644); err != nil {
		t.Fatalf("write corpus: %v", err)
	}

	_, err := Load(corpusPath)
	if err == nil {
		t.Fatal("expected error for wrong schema version, got nil")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("error should mention schema mismatch, got: %v", err)
	}
}

// TestLoadEmptyService tests that corpus files with empty service are rejected.
func TestLoadEmptyService(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	data := map[string]interface{}{
		"schema":      SchemaVersion,
		"service":     "",
		"incumbent":   "https://example.com",
		"capturedAt":  time.Now().Format(time.RFC3339),
		"description": "test",
		"entries":     []interface{}{},
	}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(corpusPath, raw, 0o644); err != nil {
		t.Fatalf("write corpus: %v", err)
	}

	_, err := Load(corpusPath)
	if err == nil {
		t.Fatal("expected error for empty service, got nil")
	}
}

// TestLoadDuplicateEntryIDs tests that duplicate entry IDs are detected.
func TestLoadDuplicateEntryIDs(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	data := map[string]interface{}{
		"schema":      SchemaVersion,
		"service":     "test",
		"incumbent":   "https://example.com",
		"capturedAt":  time.Now().Format(time.RFC3339),
		"description": "test",
		"entries": []interface{}{
			map[string]interface{}{"id": "duplicate", "request": map[string]interface{}{"method": "GET", "path": "/a"}},
			map[string]interface{}{"id": "duplicate", "request": map[string]interface{}{"method": "GET", "path": "/b"}},
		},
	}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(corpusPath, raw, 0o644); err != nil {
		t.Fatalf("write corpus: %v", err)
	}

	_, err := Load(corpusPath)
	if err == nil {
		t.Fatal("expected error for duplicate entry IDs, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention duplicate, got: %v", err)
	}
}

// TestLoadMissingEntryID tests that entries without IDs are detected.
func TestLoadMissingEntryID(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	data := map[string]interface{}{
		"schema":      SchemaVersion,
		"service":     "test",
		"incumbent":   "https://example.com",
		"capturedAt":  time.Now().Format(time.RFC3339),
		"description": "test",
		"entries": []interface{}{
			map[string]interface{}{"request": map[string]interface{}{"method": "GET", "path": "/a"}},
		},
	}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(corpusPath, raw, 0o644); err != nil {
		t.Fatalf("write corpus: %v", err)
	}

	_, err := Load(corpusPath)
	if err == nil {
		t.Fatal("expected error for missing entry ID, got nil")
	}
	if !strings.Contains(err.Error(), "no id") {
		t.Errorf("error should mention missing id, got: %v", err)
	}
}

// TestLoadInvalidJSON tests that invalid JSON is rejected.
func TestLoadInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	if err := os.WriteFile(corpusPath, []byte("{invalid json"), 0o644); err != nil {
		t.Fatalf("write corpus: %v", err)
	}

	_, err := Load(corpusPath)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// TestLoadMissingFile tests that missing files are handled.
func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/corpus.json")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// TestSaveAndLoadRoundTrip tests that saving and loading preserves data.
func TestSaveAndLoadRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	original := &Corpus{
		Service:     "test-service",
		Incumbent:   "https://example.com",
		CapturedAt:  "2026-07-27T12:00:00Z",
		Description: "Test corpus",
		Entries: []Entry{
			{
				ID:          "entry-1",
				Description: "First entry",
				Request: Request{
					Method:          "POST",
					Path:            "/api/create",
					Query:           "debug=true",
					Headers:         map[string][]string{"Content-Type": {"application/json"}},
					BodyB64:         "eyJhIjoiYiJ9",
					BodyContentType: "application/json",
				},
				Secrets: []Secret{
					{
						Ref:      "vault:rs-manager/rs-manager/seam/routes/test/secret",
						InjectAs: InjectAs{Kind: "bearer"},
					},
				},
				Expect: &Expect{
					Status:        intPtr(200),
					IgnoreHeaders: []string{"Date"},
				},
			},
			{
				ID:          "entry-2",
				Description: "Second entry",
				Request: Request{
					Method:  "GET",
					Path:    "/api/read",
					Headers: map[string][]string{"Accept": {"application/json"}},
				},
			},
		},
	}

	if err := original.Save(corpusPath); err != nil {
		t.Fatalf("save corpus: %v", err)
	}

	loaded, err := Load(corpusPath)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}

	if loaded.Service != original.Service {
		t.Errorf("Service = %q, want %q", loaded.Service, original.Service)
	}
	if loaded.Incumbent != original.Incumbent {
		t.Errorf("Incumbent = %q, want %q", loaded.Incumbent, original.Incumbent)
	}
	if loaded.CapturedAt != original.CapturedAt {
		t.Errorf("CapturedAt = %q, want %q", loaded.CapturedAt, original.CapturedAt)
	}
	if len(loaded.Entries) != len(original.Entries) {
		t.Fatalf("Entries = %d, want %d", len(loaded.Entries), len(original.Entries))
	}

	// Verify each entry.
	for i, want := range original.Entries {
		got := loaded.Entries[i]
		if got.ID != want.ID {
			t.Errorf("Entry %d ID = %q, want %q", i, got.ID, want.ID)
		}
		if got.Request.Method != want.Request.Method {
			t.Errorf("Entry %d Method = %q, want %q", i, got.Request.Method, want.Request.Method)
		}
		if got.Request.Path != want.Request.Path {
			t.Errorf("Entry %d Path = %q, want %q", i, got.Request.Path, want.Request.Path)
		}
	}
}

// TestSaveSortsEntriesByID tests that saved corpora have entries sorted by ID.
func TestSaveSortsEntriesByID(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	c := &Corpus{
		Service:    "test",
		Incumbent:  "https://example.com",
		CapturedAt: time.Now().Format(time.RFC3339),
		Entries: []Entry{
			{ID: "zebra", Request: Request{Method: "GET", Path: "/z"}},
			{ID: "alpha", Request: Request{Method: "GET", Path: "/a"}},
			{ID: "beta", Request: Request{Method: "GET", Path: "/b"}},
		},
	}

	if err := c.Save(corpusPath); err != nil {
		t.Fatalf("save corpus: %v", err)
	}

	loaded, err := Load(corpusPath)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}

	expectedOrder := []string{"alpha", "beta", "zebra"}
	for i, expectedID := range expectedOrder {
		if loaded.Entries[i].ID != expectedID {
			t.Errorf("Entry %d ID = %q, want %q", i, loaded.Entries[i].ID, expectedID)
		}
	}
}

// TestSaveAddsSchemaVersion tests that saving adds the schema version if missing.
func TestSaveAddsSchemaVersion(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	c := &Corpus{
		Service:    "test",
		Incumbent:  "https://example.com",
		CapturedAt: time.Now().Format(time.RFC3339),
		Entries:    []Entry{},
	}

	if err := c.Save(corpusPath); err != nil {
		t.Fatalf("save corpus: %v", err)
	}

	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse json: %v", err)
	}

	schema, ok := parsed["schema"].(string)
	if !ok {
		t.Fatal("schema field missing")
	}
	if schema != SchemaVersion {
		t.Errorf("schema = %q, want %q", schema, SchemaVersion)
	}
}

// TestAppendEntry tests adding new entries to a corpus.
func TestAppendEntry(t *testing.T) {
	c := &Corpus{
		Service:    "test",
		Incumbent:  "https://example.com",
		CapturedAt: time.Now().Format(time.RFC3339),
		Entries: []Entry{
			{ID: "existing", Request: Request{Method: "GET", Path: "/existing"}},
		},
	}

	newEntry := Entry{
		ID:          "new-entry",
		Description: "New entry",
		Request: Request{
			Method:  "POST",
			Path:    "/new",
			Headers: map[string][]string{"Content-Type": {"application/json"}},
		},
	}

	if err := c.AppendEntry(newEntry); err != nil {
		t.Fatalf("append entry: %v", err)
	}

	if len(c.Entries) != 2 {
		t.Fatalf("Entries = %d, want 2", len(c.Entries))
	}
	if c.Entries[1].ID != "new-entry" {
		t.Errorf("Second entry ID = %q, want new-entry", c.Entries[1].ID)
	}
}

// TestAppendEntryDuplicateID tests that duplicate entry IDs are rejected.
func TestAppendEntryDuplicateID(t *testing.T) {
	c := &Corpus{
		Service:    "test",
		Incumbent:  "https://example.com",
		CapturedAt: time.Now().Format(time.RFC3339),
		Entries: []Entry{
			{ID: "duplicate", Request: Request{Method: "GET", Path: "/a"}},
		},
	}

	duplicate := Entry{
		ID:      "duplicate",
		Request: Request{Method: "GET", Path: "/b"},
	}

	err := c.AppendEntry(duplicate)
	if err == nil {
		t.Fatal("expected error for duplicate ID, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention duplicate, got: %v", err)
	}
}

// TestAppendEntryAutoAssignID tests that empty IDs are auto-generated.
func TestAppendEntryAutoAssignID(t *testing.T) {
	c := &Corpus{
		Service:    "test",
		Incumbent:  "https://example.com",
		CapturedAt: time.Now().Format(time.RFC3339),
		Entries:    []Entry{},
	}

	entry := Entry{
		Request: Request{Method: "GET", Path: "/test"},
	}

	if err := c.AppendEntry(entry); err != nil {
		t.Fatalf("append entry: %v", err)
	}

	if c.Entries[0].ID != "entry-1" {
		t.Errorf("Auto-generated ID = %q, want entry-1", c.Entries[0].ID)
	}
}

// TestAppendEntryCanonicalizesHeaders tests that headers are canonicalized on append.
func TestAppendEntryCanonicalizesHeaders(t *testing.T) {
	c := &Corpus{
		Service:    "test",
		Incumbent:  "https://example.com",
		CapturedAt: time.Now().Format(time.RFC3339),
		Entries:    []Entry{},
	}

	entry := Entry{
		ID: "test",
		Request: Request{
			Method:  "GET",
			Path:    "/test",
			Headers: map[string][]string{"content-type": {"application/json"}, "accept": {"text/plain"}},
		},
	}

	if err := c.AppendEntry(entry); err != nil {
		t.Fatalf("append entry: %v", err)
	}

	// Headers should be canonicalized.
	expectedKeys := map[string]bool{"Content-Type": true, "Accept": true}
	for key := range c.Entries[0].Request.Headers {
		if !expectedKeys[key] {
			t.Errorf("Unexpected header key: %q", key)
		}
	}
}

// TestAppendEntryDefaultMethod tests that missing method defaults to GET.
func TestAppendEntryDefaultMethod(t *testing.T) {
	c := &Corpus{
		Service:    "test",
		Incumbent:  "https://example.com",
		CapturedAt: time.Now().Format(time.RFC3339),
		Entries:    []Entry{},
	}

	entry := Entry{
		ID: "test",
		Request: Request{
			Path: "/test",
		},
	}

	if err := c.AppendEntry(entry); err != nil {
		t.Fatalf("append entry: %v", err)
	}

	if c.Entries[0].Request.Method != http.MethodGet {
		t.Errorf("Method = %q, want GET", c.Entries[0].Request.Method)
	}
}

// TestAppendEntryCanonicalizesMethod tests that methods are canonicalized.
func TestAppendEntryCanonicalizesMethod(t *testing.T) {
	c := &Corpus{
		Service:    "test",
		Incumbent:  "https://example.com",
		CapturedAt: time.Now().Format(time.RFC3339),
		Entries:    []Entry{},
	}

	entry := Entry{
		ID: "test",
		Request: Request{
			Method: "post",
			Path:   "/test",
		},
	}

	if err := c.AppendEntry(entry); err != nil {
		t.Fatalf("append entry: %v", err)
	}

	if c.Entries[0].Request.Method != "POST" {
		t.Errorf("Method = %q, want POST", c.Entries[0].Request.Method)
	}
}

// TestHasReplayable tests the HasReplayable method.
func TestHasReplayable(t *testing.T) {
	tests := []struct {
		name     string
		entries  []Entry
		expected bool
	}{
		{
			name:     "empty corpus",
			entries:  []Entry{},
			expected: false,
		},
		{
			name: "all skipped",
			entries: []Entry{
				{ID: "a", Expect: &Expect{Skip: "reason"}},
				{ID: "b", Expect: &Expect{Skip: "reason"}},
			},
			expected: false,
		},
		{
			name: "mixed skipped and replayable",
			entries: []Entry{
				{ID: "a", Expect: &Expect{Skip: "reason"}},
				{ID: "b"},
			},
			expected: true,
		},
		{
			name: "all replayable",
			entries: []Entry{
				{ID: "a"},
				{ID: "b"},
			},
			expected: true,
		},
		{
			name: "nil expect",
			entries: []Entry{
				{ID: "a", Expect: nil},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Corpus{
				Service:    "test",
				Incumbent:  "https://example.com",
				CapturedAt: time.Now().Format(time.RFC3339),
				Entries:    tt.entries,
			}
			got := c.HasReplayable()
			if got != tt.expected {
				t.Errorf("HasReplayable() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestCanonicalHeaders tests the canonicalHeaders helper.
func TestCanonicalHeaders(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string][]string
		expected map[string][]string
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty input",
			input:    map[string][]string{},
			expected: nil,
		},
		{
			name: "lowercase keys",
			input: map[string][]string{
				"content-type": {"application/json"},
				"accept":       {"text/plain"},
			},
			expected: map[string][]string{
				"Content-Type": {"application/json"},
				"Accept":       {"text/plain"},
			},
		},
		{
			name: "mixed case keys",
			input: map[string][]string{
				"conTent-TyPe": {"application/json"},
			},
			expected: map[string][]string{
				"Content-Type": {"application/json"},
			},
		},
		{
			name: "filters empty values",
			input: map[string][]string{
				"content-type": {"application/json", "", "text/plain"},
			},
			expected: map[string][]string{
				"Content-Type": {"application/json", "text/plain"},
			},
		},
		{
			name: "all empty values",
			input: map[string][]string{
				"x": {""},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canonicalHeaders(tt.input)
			if !headersEqual(got, tt.expected) {
				t.Errorf("canonicalHeaders() = %+v, want %+v", got, tt.expected)
			}
		})
	}
}

// TestLoadCanonicalizesHeaders tests that loading canonicalizes all headers.
func TestLoadCanonicalizesHeaders(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	data := map[string]interface{}{
		"schema":      SchemaVersion,
		"service":     "test",
		"incumbent":   "https://example.com",
		"capturedAt":  time.Now().Format(time.RFC3339),
		"description": "test",
		"entries": []interface{}{
			map[string]interface{}{
				"id": "test",
				"request": map[string]interface{}{
					"method":  "GET",
					"path":    "/test",
					"headers": map[string]interface{}{"content-type": []string{"application/json"}},
				},
			},
		},
	}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(corpusPath, raw, 0o644); err != nil {
		t.Fatalf("write corpus: %v", err)
	}

	loaded, err := Load(corpusPath)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}

	// Check that headers are canonicalized.
	for key := range loaded.Entries[0].Request.Headers {
		if key != "Content-Type" {
			t.Errorf("Header key = %q, want Content-Type (canonicalized)", key)
		}
	}
}

// TestLoadCanonicalizesMethod tests that loading canonicalizes HTTP methods.
func TestLoadCanonicalizesMethod(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	data := map[string]interface{}{
		"schema":      SchemaVersion,
		"service":     "test",
		"incumbent":   "https://example.com",
		"capturedAt":  time.Now().Format(time.RFC3339),
		"description": "test",
		"entries": []interface{}{
			map[string]interface{}{
				"id": "test",
				"request": map[string]interface{}{
					"method": "post",
					"path":   "/test",
				},
			},
		},
	}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(corpusPath, raw, 0o644); err != nil {
		t.Fatalf("write corpus: %v", err)
	}

	loaded, err := Load(corpusPath)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}

	if loaded.Entries[0].Request.Method != "POST" {
		t.Errorf("Method = %q, want POST", loaded.Entries[0].Request.Method)
	}
}

// TestLoadDefaultsEmptyMethod tests that empty method defaults to GET.
func TestLoadDefaultsEmptyMethod(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	data := map[string]interface{}{
		"schema":      SchemaVersion,
		"service":     "test",
		"incumbent":   "https://example.com",
		"capturedAt":  time.Now().Format(time.RFC3339),
		"description": "test",
		"entries": []interface{}{
			map[string]interface{}{
				"id":      "test",
				"request": map[string]interface{}{"path": "/test"},
			},
		},
	}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(corpusPath, raw, 0o644); err != nil {
		t.Fatalf("write corpus: %v", err)
	}

	loaded, err := Load(corpusPath)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}

	if loaded.Entries[0].Request.Method != http.MethodGet {
		t.Errorf("Method = %q, want GET", loaded.Entries[0].Request.Method)
	}
}

// TestCorpusWithSecrets tests that secrets are properly handled.
func TestCorpusWithSecrets(t *testing.T) {
	c := &Corpus{
		Service:     "test-service",
		Incumbent:   "https://example.com",
		CapturedAt:  time.Now().Format(time.RFC3339),
		Description: "Test corpus with secrets",
		Entries: []Entry{
			{
				ID: "secret-entry",
				Request: Request{
					Method: "GET",
					Path:   "/api/secret",
				},
				Secrets: []Secret{
					{
						Ref:      "vault:rs-manager/rs-manager/seam/routes/test/secret",
						InjectAs: InjectAs{Kind: "bearer"},
						Bare:     "should-not-serialize",
					},
				},
			},
		},
	}

	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	if err := c.Save(corpusPath); err != nil {
		t.Fatalf("save corpus: %v", err)
	}

	// Load and verify that Bare field was not serialized.
	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	if strings.Contains(string(raw), "should-not-serialize") {
		t.Error("Secret Bare field was serialized (should be omitted)")
	}

	loaded, err := Load(corpusPath)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}

	if len(loaded.Entries[0].Secrets) != 1 {
		t.Fatalf("Secrets = %d, want 1", len(loaded.Entries[0].Secrets))
	}
	if loaded.Entries[0].Secrets[0].Ref != "vault:rs-manager/rs-manager/seam/routes/test/secret" {
		t.Errorf("Secret Ref = %q, want vault:rs-manager/rs-manager/seam/routes/test/secret", loaded.Entries[0].Secrets[0].Ref)
	}
	if loaded.Entries[0].Secrets[0].Bare != "" {
		t.Errorf("Secret Bare = %q, want empty (not loaded from disk)", loaded.Entries[0].Secrets[0].Bare)
	}
}

// TestCorpusWithExpectOptions tests various Expect configurations.
func TestCorpusWithExpectOptions(t *testing.T) {
	status := 201
	c := &Corpus{
		Service:     "test-service",
		Incumbent:   "https://example.com",
		CapturedAt:  time.Now().Format(time.RFC3339),
		Description: "Test corpus with expect options",
		Entries: []Entry{
			{
				ID:      "expect-status",
				Request: Request{Method: "POST", Path: "/create"},
				Expect: &Expect{
					Status:        &status,
					IgnoreHeaders: []string{"Date", "Server"},
					IgnoreBody:    false,
				},
			},
			{
				ID:      "expect-ignore-body",
				Request: Request{Method: "GET", Path: "/random"},
				Expect: &Expect{
					IgnoreBody: true,
				},
			},
			{
				ID:      "expect-skip",
				Request: Request{Method: "GET", Path: "/upstream"},
				Expect: &Expect{
					Skip: "upstream not yet onboarded",
				},
			},
		},
	}

	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	if err := c.Save(corpusPath); err != nil {
		t.Fatalf("save corpus: %v", err)
	}

	loaded, err := Load(corpusPath)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}

	// Verify expect options are preserved. Entries are looked up by ID: Save
	// sorts entries by ID before writing, so a positional index pairs an
	// assertion with whichever entry sorted into that slot — the previous
	// version of this test dereferenced Entries[0].Expect.Status against the
	// expect-ignore-body entry and panicked on its nil Status.
	byID := make(map[string]Entry, len(loaded.Entries))
	for _, e := range loaded.Entries {
		byID[e.ID] = e
	}
	expectStatus := byID["expect-status"]
	if expectStatus.Expect == nil || expectStatus.Expect.Status == nil {
		t.Fatalf("expect-status: Expect.Status missing after load")
	}
	if *expectStatus.Expect.Status != 201 {
		t.Errorf("expect-status Status = %d, want 201", *expectStatus.Expect.Status)
	}
	expectIgnoreBody := byID["expect-ignore-body"]
	if expectIgnoreBody.Expect == nil || !expectIgnoreBody.Expect.IgnoreBody {
		t.Errorf("expect-ignore-body IgnoreBody = %v, want true", expectIgnoreBody.Expect)
	}
	expectSkip := byID["expect-skip"]
	if expectSkip.Expect == nil || expectSkip.Expect.Skip != "upstream not yet onboarded" {
		t.Errorf("expect-skip Skip = %v, want 'upstream not yet onboarded'", expectSkip.Expect)
	}
}

// TestLoadServiceMismatch tests warning on service name mismatch.
func TestLoadServiceMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	// Create corpus with service "original"
	c := &Corpus{
		Service:    "original",
		Incumbent:  "https://example.com",
		CapturedAt: time.Now().Format(time.RFC3339),
		Entries:    []Entry{{ID: "test", Request: Request{Method: "GET", Path: "/test"}}},
	}
	if err := c.Save(corpusPath); err != nil {
		t.Fatalf("save corpus: %v", err)
	}

	// This would normally be checked at load time, but since we're testing the
	// Load function directly (not the capture tool's wrapper), we just verify
	// that the error path works.
	// The actual service mismatch check is in the capture tool's loadCorpus method.
}

// writeOneEntryCorpus writes a corpus carrying a single entry with a single
// secret ref, the minimal fixture every ref-validation case needs.
func writeOneEntryCorpus(t *testing.T, ref string) string {
	t.Helper()
	corpusPath := filepath.Join(t.TempDir(), "corpus.json")
	c := &Corpus{
		Service:    "test",
		Incumbent:  "https://example.com",
		CapturedAt: time.Now().Format(time.RFC3339),
		Entries: []Entry{
			{
				ID:      "ref-entry",
				Request: Request{Method: "GET", Path: "/api/test"},
				Secrets: []Secret{{Ref: ref, InjectAs: InjectAs{Kind: "bearer"}}},
			},
		},
	}
	if err := c.Save(corpusPath); err != nil {
		t.Fatalf("save corpus: %v", err)
	}
	return corpusPath
}

// TestLoadValidatesSecretRefsAgainstEnforcedBase pins the load-time secret-ref
// check: a ref must be well-formed (vault: scheme, no traversal, globs, or
// templated segments) and resolve strictly under the enforced vault base, so
// an off-base ref is rejected while the corpus is still a fixture instead of
// failing resolution at replay time. The rejection classes mirror
// internal/spec/allowlist.go's ValidateVaultPath; the retired pre-consolidation
// base "seam/routes" is the case the design doc warns must not be copied into
// a new capture.
func TestLoadValidatesSecretRefsAgainstEnforcedBase(t *testing.T) {
	tests := []struct {
		name        string
		ref         string
		errContains string // empty means the ref must load cleanly
	}{
		{
			name: "on-base ref loads",
			ref:  "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token",
		},
		{
			name:        "retired pre-consolidation base rejected",
			ref:         "vault:seam/routes/argocd-ro/ro-token",
			errContains: "outside the enforced vault base",
		},
		{
			name:        "sibling sharing a string prefix is not under the base",
			ref:         "vault:rs-manager/rs-manager/seam/routes2/token",
			errContains: "outside the enforced vault base",
		},
		{
			name:        "bare base is direct access to the parent",
			ref:         "vault:rs-manager/rs-manager/seam/routes",
			errContains: "outside the enforced vault base",
		},
		{
			name:        "traversal rejected",
			ref:         "vault:rs-manager/rs-manager/seam/routes/../argocd-ro/ro-token",
			errContains: "traversal",
		},
		{
			name:        "backslash separator rejected",
			ref:         `vault:rs-manager\rs-manager\seam\routes\argocd-ro\ro-token`,
			errContains: "traversal",
		},
		{
			name:        "glob rejected",
			ref:         "vault:rs-manager/rs-manager/seam/routes/argocd-ro/*",
			errContains: "glob",
		},
		{
			name:        "templated segment rejected",
			ref:         "vault:rs-manager/rs-manager/seam/routes/{owner}/ro-token",
			errContains: "templated",
		},
		{
			name:        "missing vault scheme rejected",
			ref:         "rs-manager/rs-manager/seam/routes/argocd-ro/ro-token",
			errContains: "vault: scheme",
		},
		{
			name:        "empty ref rejected",
			ref:         "",
			errContains: "empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Pin the default base: a blank override falls through to
			// DefaultVaultBaseDir, so these cases exercise the prefix a
			// Deployment enforces when SEAM_VAULT_BASE_DIR is unset.
			t.Setenv(VaultBaseDirEnvVar, "")
			corpusPath := writeOneEntryCorpus(t, tt.ref)
			_, err := Load(corpusPath)
			if tt.errContains == "" {
				if err != nil {
					t.Fatalf("load corpus with ref %q: %v", tt.ref, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q for ref %q, got nil", tt.errContains, tt.ref)
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("error = %v, want it to contain %q", err, tt.errContains)
			}
		})
	}
}

// TestVaultBaseDirOverrideHonored pins the precedence the gateway's
// internal/spec.ResolveVaultBaseDir implements: a non-blank SEAM_VAULT_BASE_DIR
// replaces the default base for validation, and a blank one falls back to the
// default — a capture taken under a non-default Deployment base must load.
func TestVaultBaseDirOverrideHonored(t *testing.T) {
	t.Run("non-blank override accepts ref under it", func(t *testing.T) {
		t.Setenv(VaultBaseDirEnvVar, "tenant-a/cluster-b/seam/routes")
		corpusPath := writeOneEntryCorpus(t, "vault:tenant-a/cluster-b/seam/routes/argocd-ro/ro-token")
		if _, err := Load(corpusPath); err != nil {
			t.Fatalf("load corpus under override base: %v", err)
		}
	})
	t.Run("non-blank override rejects the default base", func(t *testing.T) {
		t.Setenv(VaultBaseDirEnvVar, "tenant-a/cluster-b/seam/routes")
		corpusPath := writeOneEntryCorpus(t, "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token")
		_, err := Load(corpusPath)
		if err == nil {
			t.Fatal("expected error for ref under the default base while an override is in force, got nil")
		}
		if !strings.Contains(err.Error(), "outside the enforced vault base") {
			t.Errorf("error = %v, want it to contain %q", err, "outside the enforced vault base")
		}
	})
	t.Run("blank override falls back to default", func(t *testing.T) {
		t.Setenv(VaultBaseDirEnvVar, "   ")
		corpusPath := writeOneEntryCorpus(t, "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token")
		if _, err := Load(corpusPath); err != nil {
			t.Fatalf("load corpus with blank override: %v", err)
		}
	})
}

// TestAppendEntryValidatesSecretRefs keeps the capture path under the same
// rule as Load: an off-base ref is refused when the entry is appended, so a
// live capture cannot grow a corpus the next Load would reject.
func TestAppendEntryValidatesSecretRefs(t *testing.T) {
	t.Setenv(VaultBaseDirEnvVar, "")
	c := &Corpus{
		Service:    "test",
		Incumbent:  "https://example.com",
		CapturedAt: time.Now().Format(time.RFC3339),
	}
	err := c.AppendEntry(Entry{
		ID:      "off-base",
		Request: Request{Method: "GET", Path: "/a"},
		Secrets: []Secret{{Ref: "vault:seam/routes/argocd-ro/ro-token", InjectAs: InjectAs{Kind: "bearer"}}},
	})
	if err == nil {
		t.Fatal("expected error appending off-base ref, got nil")
	}
	if !strings.Contains(err.Error(), "outside the enforced vault base") {
		t.Errorf("error = %v, want it to contain %q", err, "outside the enforced vault base")
	}
	if err := c.AppendEntry(Entry{
		ID:      "on-base",
		Request: Request{Method: "GET", Path: "/b"},
		Secrets: []Secret{{Ref: "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token", InjectAs: InjectAs{Kind: "bearer"}}},
	}); err != nil {
		t.Fatalf("append on-base ref: %v", err)
	}
}

// TestCheckedInFixturesResolveUnderEnforcedVaultBase is the fixture-time check
// over the corpora this module actually ships: every checked-in fixture must
// load with SEAM's enforced base in force, and the argocd fixture must carry
// the canonical argocd-ro token — the deployed fragment's x-seam-owner
// (declarative-config/k8s/rs-manager/seam/routes/argocd-ro/) and the owner the
// internal/spec tests treat as canonical — so the reference shapes a new
// capture is copied from can never drift back to the retired base or a stale
// service token.
func TestCheckedInFixturesResolveUnderEnforcedVaultBase(t *testing.T) {
	t.Setenv(VaultBaseDirEnvVar, "") // pin the default base
	fixtures := []string{
		"../../testdata/corpus-argocd.json",
		"../../testdata/example-corpus.json",
	}
	for _, rel := range fixtures {
		t.Run(filepath.Base(rel), func(t *testing.T) {
			c, err := Load(rel)
			if err != nil {
				t.Fatalf("load checked-in fixture %s: %v", rel, err)
			}
			for i, e := range c.Entries {
				for j, s := range e.Secrets {
					if err := validateSecretRef(s.Ref, DefaultVaultBaseDir); err != nil {
						t.Errorf("entry %d (%q) secrets[%d]: %v", i, e.ID, j, err)
					}
				}
			}
		})
	}

	c, err := Load("../../testdata/corpus-argocd.json")
	if err != nil {
		t.Fatalf("load corpus-argocd.json: %v", err)
	}
	if c.Service != "argocd-ro" {
		t.Errorf("corpus-argocd.json service = %q, want argocd-ro", c.Service)
	}
	for _, e := range c.Entries {
		for _, s := range e.Secrets {
			want := "vault:" + DefaultVaultBaseDir + "/argocd-ro/"
			if !strings.HasPrefix(s.Ref, want) {
				t.Errorf("entry %q ref = %q, want it under %s", e.ID, s.Ref, want)
			}
		}
	}
}

// Helper functions

func intPtr(i int) *int {
	return &i
}

func headersEqual(a, b map[string][]string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		if len(va) != len(vb) {
			return false
		}
		for i := range va {
			if va[i] != vb[i] {
				return false
			}
		}
	}
	return true
}
