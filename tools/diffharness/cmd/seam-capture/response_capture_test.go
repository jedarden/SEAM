package main

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/tools/diffharness/internal/corpus"
)

func TestCaptureRecordsFullExchange(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Seam-Capture-Skip") != "" {
			t.Error("capture control header reached upstream")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		if string(body) != "request-body" {
			t.Errorf("upstream body = %q, want request-body", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "test")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := &capturer{
		target:            target,
		service:           "test",
		corpusPath:        filepath.Join(t.TempDir(), "corpus.json"),
		description:       "test corpus",
		enabled:           true,
		captureConfigured: true,
	}
	if err := c.loadCorpus(); err != nil {
		t.Fatalf("load corpus: %v", err)
	}

	proxy := httptest.NewServer(http.HandlerFunc(c.captureHandler))
	defer proxy.Close()
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/api/test?mode=full", io.NopCloser(strings.NewReader("request-body")))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request through capture proxy: %v", err)
	}
	defer resp.Body.Close()
	forwarded, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted || string(forwarded) != `{"ok":true}` {
		t.Fatalf("forwarded response = %d %q", resp.StatusCode, forwarded)
	}

	if err := c.saveCorpus(); err != nil {
		t.Fatalf("save corpus: %v", err)
	}
	got, err := corpus.Load(c.corpusPath)
	if err != nil {
		t.Fatalf("reload corpus: %v", err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(got.Entries))
	}
	entry := got.Entries[0]
	if entry.Timestamp == "" {
		t.Fatal("entry timestamp is empty")
	}
	if _, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err != nil {
		t.Fatalf("invalid entry timestamp %q: %v", entry.Timestamp, err)
	}
	if entry.Request.Headers["Authorization"][0] != "[REDACTED-BY-SEAM]" {
		t.Fatalf("authorization was not redacted: %#v", entry.Request.Headers["Authorization"])
	}
	if entry.Response == nil {
		t.Fatal("response is nil")
	}
	if entry.Response.StatusCode != http.StatusAccepted {
		t.Errorf("response status = %d, want %d", entry.Response.StatusCode, http.StatusAccepted)
	}
	if entry.Response.Headers["X-Upstream"][0] != "test" {
		t.Errorf("response headers = %#v", entry.Response.Headers)
	}
	responseBody, err := base64.StdEncoding.DecodeString(entry.Response.BodyB64)
	if err != nil {
		t.Fatal(err)
	}
	if string(responseBody) != `{"ok":true}` {
		t.Errorf("captured response body = %q", responseBody)
	}
}

func TestCaptureDisabledStillForwardsWithoutRecording(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Seam-Capture-Skip") != "" {
			t.Error("capture control header reached upstream")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	c := &capturer{
		target:            target,
		service:           "test",
		corpusPath:        filepath.Join(t.TempDir(), "corpus.json"),
		enabled:           false,
		captureConfigured: true,
	}
	if err := c.loadCorpus(); err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	proxy := httptest.NewServer(http.HandlerFunc(c.captureHandler))
	defer proxy.Close()
	resp, err := http.Get(proxy.URL + "/disabled")
	if err != nil {
		t.Fatalf("disabled proxy request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	if err := c.saveCorpus(); err != nil {
		t.Fatalf("save disabled corpus: %v", err)
	}
	got, err := corpus.Load(c.corpusPath)
	if err != nil {
		t.Fatalf("reload disabled corpus: %v", err)
	}
	if len(got.Entries) != 0 {
		t.Fatalf("disabled capture entries = %d, want 0", len(got.Entries))
	}
}
