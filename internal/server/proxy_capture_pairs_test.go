package server

import (
	"encoding/base64"
	"io"
	"net/http"
	"testing"
)

func TestProxyCaptureEnabledPreservesSuccessfulResponsePair(t *testing.T) {
	h := NewProxyTestHarness(t, true)

	requestBody := []byte(`{"request":"preserve-success"}`)
	resp, err := h.MakeTestRequest(http.MethodPost, proxyTestPath+"?mode=success", requestBody, map[string]string{
		"Content-Type":  "application/json",
		"X-Test-Header": "success",
	})
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	defer resp.Body.Close()

	actualBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read proxied response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected proxied status %d, got %d", http.StatusOK, resp.StatusCode)
	}
	if string(actualBody) != `{"proxied":true}` {
		t.Fatalf("unexpected proxied response body: %q", actualBody)
	}
	assertHeaderValue(t, resp.Header, "Content-Type", "application/json")
	assertHeaderValue(t, resp.Header, "X-Upstream", "stub")

	upstream := h.waitForBackendRequest(t)
	if upstream.method != http.MethodPost || upstream.path != proxyTestPath || upstream.query != "mode=success" {
		t.Fatalf("unexpected upstream request: %#v", upstream)
	}
	if string(upstream.body) != string(requestBody) {
		t.Fatalf("upstream request body changed: got %q, want %q", upstream.body, requestBody)
	}

	corpus := saveAndLoadProxyCapture(t, h)
	if len(corpus.Entries) != 1 {
		t.Fatalf("expected one captured entry, got %d", len(corpus.Entries))
	}
	entry := corpus.Entries[0]
	if entry.Request.Method != http.MethodPost || entry.Request.Path != proxyTestPath || entry.Request.Query != "mode=success" {
		t.Fatalf("captured request metadata changed: %#v", entry.Request)
	}
	assertBase64Body(t, entry.Request.BodyB64, requestBody, "captured request")
	if entry.Response.StatusCode != resp.StatusCode {
		t.Fatalf("captured response status = %d, proxied status = %d", entry.Response.StatusCode, resp.StatusCode)
	}
	assertBase64Body(t, entry.Response.BodyB64, actualBody, "captured response")
	assertCapturedHeaderValue(t, entry.Response.Headers, "Content-Type", resp.Header.Get("Content-Type"))
	assertCapturedHeaderValue(t, entry.Response.Headers, "X-Upstream", resp.Header.Get("X-Upstream"))
}

func TestProxyCaptureEnabledPreservesErrorResponsePair(t *testing.T) {
	h := NewProxyTestHarness(t, true)

	requestBody := []byte(`{"request":"preserve-error"}`)
	resp, err := h.MakeTestRequest(http.MethodPost, proxyTestPath+"?mode=error", requestBody, map[string]string{
		"Content-Type":  "application/json",
		"X-Test-Header": "error",
	})
	if err != nil {
		t.Fatalf("proxy request failed before receiving upstream error: %v", err)
	}
	defer resp.Body.Close()

	actualBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read proxied error response: %v", err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected proxied status %d, got %d", http.StatusBadGateway, resp.StatusCode)
	}
	if string(actualBody) != `{"error":"upstream failure","request_id":"capture-test"}` {
		t.Fatalf("unexpected proxied error body: %q", actualBody)
	}
	assertHeaderValue(t, resp.Header, "Content-Type", "application/problem+json")
	assertHeaderValue(t, resp.Header, "X-Upstream", "error-stub")
	assertHeaderValue(t, resp.Header, "X-Error-Code", "upstream-failure")

	upstream := h.waitForBackendRequest(t)
	if upstream.method != http.MethodPost || upstream.path != proxyTestPath || upstream.query != "mode=error" {
		t.Fatalf("unexpected upstream request: %#v", upstream)
	}
	if string(upstream.body) != string(requestBody) {
		t.Fatalf("upstream error request body changed: got %q, want %q", upstream.body, requestBody)
	}

	corpus := saveAndLoadProxyCapture(t, h)
	if len(corpus.Entries) != 1 {
		t.Fatalf("expected one captured error entry, got %d", len(corpus.Entries))
	}
	entry := corpus.Entries[0]
	if entry.Request.Method != http.MethodPost || entry.Request.Path != proxyTestPath || entry.Request.Query != "mode=error" {
		t.Fatalf("captured error request metadata changed: %#v", entry.Request)
	}
	assertBase64Body(t, entry.Request.BodyB64, requestBody, "captured error request")
	if entry.Response.StatusCode != resp.StatusCode {
		t.Fatalf("captured error status = %d, proxied status = %d", entry.Response.StatusCode, resp.StatusCode)
	}
	assertBase64Body(t, entry.Response.BodyB64, actualBody, "captured error response")
	assertCapturedHeaderValue(t, entry.Response.Headers, "Content-Type", resp.Header.Get("Content-Type"))
	assertCapturedHeaderValue(t, entry.Response.Headers, "X-Upstream", resp.Header.Get("X-Upstream"))
	assertCapturedHeaderValue(t, entry.Response.Headers, "X-Error-Code", resp.Header.Get("X-Error-Code"))
}

func saveAndLoadProxyCapture(t *testing.T, h *ProxyTestHarness) *CorpusFile {
	t.Helper()
	if result, err := h.TriggerManualSave(); err != nil {
		t.Fatalf("save corpus: %v", err)
	} else if result["status"] != "saved" {
		t.Fatalf("expected corpus save status %q, got %v", "saved", result["status"])
	}

	corpus, err := h.LoadCorpus()
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	return corpus
}

func assertBase64Body(t *testing.T, encoded string, expected []byte, label string) {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode %s body: %v", label, err)
	}
	if string(decoded) != string(expected) {
		t.Fatalf("%s body = %q, want %q", label, decoded, expected)
	}
}

func assertHeaderValue(t *testing.T, headers http.Header, key, expected string) {
	t.Helper()
	if got := headers.Get(key); got != expected {
		t.Fatalf("response header %s = %q, want %q", key, got, expected)
	}
}

func assertCapturedHeaderValue(t *testing.T, headers map[string][]string, key, expected string) {
	t.Helper()
	values, ok := headers[http.CanonicalHeaderKey(key)]
	if !ok || len(values) != 1 || values[0] != expected {
		t.Fatalf("captured response header %s = %v, want [%q]", key, values, expected)
	}
}
