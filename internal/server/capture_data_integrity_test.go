package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestCaptureCorpusDataIntegrity verifies that a captured request/response
// pair survives the complete capture -> save -> JSON parse path without data
// loss or corruption.
func TestCaptureCorpusDataIntegrity(t *testing.T) {
	requestBody := []byte(`{"name":"capture-integrity"}`)
	responseBody := []byte(`{"id":42,"status":"created"}`)

	cm := NewCaptureMiddleware(t.TempDir(), "integrity-service", "https://incumbent.example.test", false)
	handler := cm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if !bytes.Equal(gotBody, requestBody) {
			t.Errorf("handler request body = %q, want %q", gotBody, requestBody)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Capture-Integrity", "verified")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(responseBody)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/integrity?mode=complete", bytes.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Capture-Request", "verified")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("proxied status = %d, want %d", resp.Code, http.StatusCreated)
	}
	if !bytes.Equal(resp.Body.Bytes(), responseBody) {
		t.Fatalf("proxied response body = %q, want %q", resp.Body.Bytes(), responseBody)
	}

	if err := cm.Save(); err != nil {
		t.Fatalf("save corpus: %v", err)
	}
	corpusPath := filepath.Join(cm.corpusDir, "corpus.json")
	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("read saved corpus: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatal("saved corpus is not valid JSON")
	}

	var corpus CorpusFile
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("decode saved corpus: %v", err)
	}
	if corpus.Schema != "seam-diff-corpus/v1" || corpus.Service != "integrity-service" || corpus.Incumbent == "" || corpus.Description == "" {
		t.Fatalf("saved corpus metadata is incomplete: %+v", corpus)
	}
	if _, err := time.Parse(time.RFC3339, corpus.CapturedAt); err != nil {
		t.Fatalf("saved corpus capturedAt is not RFC3339: %v", err)
	}
	if len(corpus.Entries) != 1 {
		t.Fatalf("saved corpus entries = %d, want 1", len(corpus.Entries))
	}

	entry := corpus.Entries[0]
	if entry.ID == "" || entry.Description == "" {
		t.Fatalf("captured entry metadata is incomplete: %+v", entry)
	}
	if _, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err != nil {
		t.Fatalf("captured entry timestamp is not RFC3339: %v", err)
	}
	if entry.Request.Method != http.MethodPost || entry.Request.Path != "/api/integrity" || entry.Request.Query != "mode=complete" {
		t.Fatalf("captured request metadata is incomplete: %+v", entry.Request)
	}
	if entry.Request.BodyContentType != "application/json" {
		t.Fatalf("captured request headers/content type are incomplete: %+v", entry.Request)
	}
	if got := entry.Request.Headers["X-Capture-Request"]; len(got) != 1 || got[0] != "verified" {
		t.Fatalf("captured request headers are incomplete: %+v", entry.Request.Headers)
	}
	decodedRequest, err := base64.StdEncoding.DecodeString(entry.Request.BodyB64)
	if err != nil {
		t.Fatalf("decode captured request body: %v", err)
	}
	if !bytes.Equal(decodedRequest, requestBody) {
		t.Fatalf("captured request body = %q, want %q", decodedRequest, requestBody)
	}

	if entry.Response.StatusCode != http.StatusCreated || entry.Response.BodyContentType != "application/json" {
		t.Fatalf("captured response metadata is incomplete: %+v", entry.Response)
	}
	if got := entry.Response.Headers["X-Capture-Integrity"]; len(got) != 1 || got[0] != "verified" {
		t.Fatalf("captured response headers are incomplete: %+v", entry.Response.Headers)
	}
	decodedResponse, err := base64.StdEncoding.DecodeString(entry.Response.BodyB64)
	if err != nil {
		t.Fatalf("decode captured response body: %v", err)
	}
	if !bytes.Equal(decodedResponse, responseBody) {
		t.Fatalf("captured response body = %q, want %q", decodedResponse, responseBody)
	}
	if strings.TrimSpace(string(raw)) == "" {
		t.Fatal("saved corpus is unexpectedly empty")
	}
}

// TestCaptureCorpusBinaryRoundTrip verifies the full in-memory capture ->
// serialize -> deserialize -> load lifecycle. The payloads deliberately
// contain invalid UTF-8 so byte preservation depends on the corpus' base64
// representation rather than an implicit string conversion.
func TestCaptureCorpusBinaryRoundTrip(t *testing.T) {
	type exchange struct {
		path         string
		query        string
		requestBody  []byte
		responseBody []byte
	}

	exchanges := []exchange{
		{
			path:         "/api/binary/first",
			query:        "part=1&mode=raw",
			requestBody:  []byte{0x00, 0xff, 0xfe, 0x80, 0x41, 0xc3, 0x28},
			responseBody: []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0xff},
		},
		{
			path:         "/api/binary/second",
			query:        "part=2&mode=raw",
			requestBody:  []byte{0xf5, 0x80, 0x80, 0x80, 0x00, 0x7f},
			responseBody: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0xff},
		},
	}

	byPath := make(map[string]exchange, len(exchanges))
	for _, item := range exchanges {
		byPath[item.path] = item
	}

	corpusDir := t.TempDir()
	cm := NewCaptureMiddleware(corpusDir, "binary-integrity-service", "https://incumbent.example.test", false)
	handler := cm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, ok := byPath[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read binary request body: %v", err)
		}
		if !bytes.Equal(gotBody, item.requestBody) {
			t.Errorf("handler request body = %v, want %v", gotBody, item.requestBody)
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Add("X-Binary-Response", "first-value")
		w.Header().Add("X-Binary-Response", "second-value")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(item.responseBody)
	}))

	type captureWindow struct {
		start time.Time
		end   time.Time
	}
	windows := make([]captureWindow, 0, len(exchanges))
	for index, item := range exchanges {
		if index > 0 {
			// The persisted representation has nanosecond precision. Advancing
			// the wall clock makes strict ordering deterministic even on hosts
			// whose clock source has coarser resolution.
			time.Sleep(time.Millisecond)
		}
		start := time.Now()
		req := httptest.NewRequest(http.MethodPut, item.path+"?"+item.query, bytes.NewReader(item.requestBody))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Add("X-Binary-Request", "first-value")
		req.Header.Add("X-Binary-Request", "second-value")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		end := time.Now()
		windows = append(windows, captureWindow{start: start, end: end})

		if resp.Code != http.StatusPartialContent {
			t.Fatalf("exchange %d status = %d, want %d", index, resp.Code, http.StatusPartialContent)
		}
		if !bytes.Equal(resp.Body.Bytes(), item.responseBody) {
			t.Fatalf("exchange %d response body = %v, want %v", index, resp.Body.Bytes(), item.responseBody)
		}
	}

	var beforeSerialization CorpusFile
	cm.marshalCorpus = func(corpus CorpusFile) ([]byte, error) {
		beforeSerialization = corpus
		return json.MarshalIndent(corpus, "", "  ")
	}
	saveStart := time.Now()
	if err := cm.Save(); err != nil {
		t.Fatalf("save binary corpus: %v", err)
	}
	saveEnd := time.Now()

	raw, err := os.ReadFile(filepath.Join(corpusDir, "corpus.json"))
	if err != nil {
		t.Fatalf("read binary corpus: %v", err)
	}
	var afterDeserialization CorpusFile
	if err := json.Unmarshal(raw, &afterDeserialization); err != nil {
		t.Fatalf("deserialize binary corpus: %v", err)
	}
	if !reflect.DeepEqual(afterDeserialization, beforeSerialization) {
		t.Fatalf("corpus changed during serialize/deserialize round trip:\n got: %#v\nwant: %#v", afterDeserialization, beforeSerialization)
	}

	if len(afterDeserialization.Entries) != len(exchanges) {
		t.Fatalf("deserialized entries = %d, want %d", len(afterDeserialization.Entries), len(exchanges))
	}
	var previousTimestamp time.Time
	for index, item := range exchanges {
		entry := afterDeserialization.Entries[index]
		if entry.ID == "" || entry.Description != http.MethodPut+" "+item.path {
			t.Fatalf("entry %d metadata was not preserved: %+v", index, entry)
		}
		if entry.Request.Method != http.MethodPut || entry.Request.Path != item.path || entry.Request.Query != item.query {
			t.Fatalf("entry %d request metadata was not preserved: %+v", index, entry.Request)
		}
		if entry.Request.BodyContentType != "application/octet-stream" {
			t.Fatalf("entry %d request content type = %q", index, entry.Request.BodyContentType)
		}
		if got := entry.Request.Headers["X-Binary-Request"]; !reflect.DeepEqual(got, []string{"first-value", "second-value"}) {
			t.Fatalf("entry %d request headers = %v", index, entry.Request.Headers)
		}
		assertBase64BodyEquals(t, "request", index, entry.Request.BodyB64, item.requestBody)

		if entry.Response.StatusCode != http.StatusPartialContent || entry.Response.BodyContentType != "application/octet-stream" {
			t.Fatalf("entry %d response metadata was not preserved: %+v", index, entry.Response)
		}
		if got := entry.Response.Headers["X-Binary-Response"]; !reflect.DeepEqual(got, []string{"first-value", "second-value"}) {
			t.Fatalf("entry %d response headers = %v", index, entry.Response.Headers)
		}
		assertBase64BodyEquals(t, "response", index, entry.Response.BodyB64, item.responseBody)

		timestamp, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
		if err != nil {
			t.Fatalf("entry %d timestamp %q is invalid: %v", index, entry.Timestamp, err)
		}
		if timestamp.Before(windows[index].start) || timestamp.After(windows[index].end) {
			t.Fatalf("entry %d timestamp %s is outside capture window [%s, %s]", index, timestamp, windows[index].start, windows[index].end)
		}
		if index > 0 && !timestamp.After(previousTimestamp) {
			t.Fatalf("entry timestamps are not strictly increasing: %s then %s", previousTimestamp, timestamp)
		}
		previousTimestamp = timestamp
	}

	capturedAt, err := time.Parse(time.RFC3339, afterDeserialization.CapturedAt)
	if err != nil {
		t.Fatalf("capturedAt %q is invalid: %v", afterDeserialization.CapturedAt, err)
	}
	if capturedAt.Before(saveStart.Truncate(time.Second)) || capturedAt.After(saveEnd) {
		t.Fatalf("capturedAt %s is outside save window [%s, %s]", capturedAt, saveStart, saveEnd)
	}

	reloaded := NewCaptureMiddleware(corpusDir, "binary-integrity-service", "https://incumbent.example.test", false)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("load serialized corpus: %v", err)
	}
	reloaded.mu.Lock()
	reloadedEntries := reloaded.entriesInCaptureOrderLocked()
	reloaded.mu.Unlock()
	if !reflect.DeepEqual(reloadedEntries, beforeSerialization.Entries) {
		t.Fatalf("corpus entries changed after loading from disk:\n got: %#v\nwant: %#v", reloadedEntries, beforeSerialization.Entries)
	}
}

func assertBase64BodyEquals(t *testing.T, side string, index int, encoded string, want []byte) {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("entry %d %s body is not valid base64: %v", index, side, err)
	}
	if !bytes.Equal(decoded, want) {
		t.Fatalf("entry %d %s body = %v, want %v", index, side, decoded, want)
	}
}
