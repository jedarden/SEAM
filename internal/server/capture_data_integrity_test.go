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
