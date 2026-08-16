package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const proxyTestPath = "/proxy/echo"

type proxyBackendRequest struct {
	method string
	path   string
	query  string
	body   []byte
	header http.Header
}

// ProxyTestHarness runs a real SEAM server and a local upstream so tests can
// exercise a caller request with corpus capture enabled or disabled.
type ProxyTestHarness struct {
	server        *Server
	callerURL     string
	operatorURL   string
	corpusDir     string
	backendServer *httptest.Server
	backendURL    string
	backendReqs   chan proxyBackendRequest
	client        *http.Client
	closeOnce     sync.Once
}

// NewProxyTestHarness starts a caller-facing SEAM server, an operator server,
// and an httputil reverse proxy backed by an in-process HTTP server. Ports and
// corpus storage are ephemeral, so each test is isolated from other tests.
func NewProxyTestHarness(t *testing.T, captureEnabled bool) *ProxyTestHarness {
	t.Helper()

	backendReqs := make(chan proxyBackendRequest, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request", http.StatusBadRequest)
			return
		}
		backendReqs <- proxyBackendRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			body:   body,
			header: r.Header.Clone(),
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "stub")
		if r.URL.Query().Get("mode") == "error" {
			w.Header().Set("Content-Type", "application/problem+json")
			w.Header().Set("X-Upstream", "error-stub")
			w.Header().Set("X-Error-Code", "upstream-failure")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":"upstream failure","request_id":"capture-test"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"proxied":true}`)
	}))

	tmpDir := t.TempDir()
	specDir := filepath.Join(tmpDir, "spec")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		backend.Close()
		t.Fatalf("create test spec directory: %v", err)
	}
	const testSpec = `{"openapi":"3.0.0","info":{"title":"Proxy test API","version":"1.0.0"},"paths":{"/proxy/echo":{"post":{"responses":{"200":{"description":"OK"}}}}}}`
	if err := os.WriteFile(filepath.Join(specDir, "openapi.yaml"), []byte(testSpec), 0o644); err != nil {
		backend.Close()
		t.Fatalf("write test spec: %v", err)
	}
	harness := &ProxyTestHarness{
		corpusDir:     filepath.Join(tmpDir, "corpus"),
		backendServer: backend,
		backendURL:    backend.URL,
		backendReqs:   backendReqs,
		client:        &http.Client{Timeout: 5 * time.Second},
	}

	target, err := url.Parse(backend.URL)
	if err != nil {
		backend.Close()
		t.Fatalf("parse backend URL: %v", err)
	}

	cfg := &Config{
		CallerPort:     0,
		OperatorPort:   0,
		BaseURL:        "http://127.0.0.1",
		SpecDir:        specDir,
		CaptureEnabled: captureEnabled,
		CorpusDir:      harness.corpusDir,
	}

	harness.server = New(cfg)
	// Mount the test proxy after production routes are initialized. The
	// capture middleware wraps the caller mux in Server.Start, so requests
	// through this route exercise the same capture boundary as production.
	harness.server.callerMux.Handle("/proxy/", httputil.NewSingleHostReverseProxy(target))

	if err := harness.server.Start(context.Background()); err != nil {
		backend.Close()
		t.Fatalf("start SEAM test server: %v", err)
	}

	harness.callerURL = listenerURL(t, harness.server.callerListener)
	harness.operatorURL = listenerURL(t, harness.server.operatorListener)
	t.Cleanup(harness.Close)
	return harness
}

func listenerURL(t *testing.T, listener net.Listener) string {
	t.Helper()
	port := listener.Addr().(*net.TCPAddr).Port
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// Close shuts down both test servers. It is safe to call more than once.
func (h *ProxyTestHarness) Close() {
	h.closeOnce.Do(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if h.server != nil {
			_ = h.server.Shutdown(shutdownCtx)
		}
		if h.backendServer != nil {
			h.backendServer.Close()
		}
	})
}

// MakeTestRequest sends a request through the caller-facing proxy port.
func (h *ProxyTestHarness) MakeTestRequest(method, path string, body []byte, headers map[string]string) (*http.Response, error) {
	return h.MakeRequestToPort(method, path, body, headers, h.callerURL)
}

// MakeOperatorRequest sends a request to the operator-only port.
func (h *ProxyTestHarness) MakeOperatorRequest(method, path string, body []byte, headers map[string]string) (*http.Response, error) {
	return h.MakeRequestToPort(method, path, body, headers, h.operatorURL)
}

// MakeRequestToPort is the shared test client used by caller and operator
// requests. It accepts a path with a query string and optional request body.
func (h *ProxyTestHarness) MakeRequestToPort(method, path string, body []byte, headers map[string]string, baseURL string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, strings.TrimRight(baseURL, "/")+path, reader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	return resp, nil
}

// GetCaptureStatus reads the capture status exposed on the operator port.
func (h *ProxyTestHarness) GetCaptureStatus() (map[string]any, error) {
	resp, err := h.MakeOperatorRequest(http.MethodGet, "/_seam/capture/status", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get capture status: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("capture status returned %d", resp.StatusCode)
	}

	var status map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("decode capture status: %w", err)
	}
	return status, nil
}

// TriggerManualSave persists the in-memory corpus and returns the operator
// endpoint response.
func (h *ProxyTestHarness) TriggerManualSave() (map[string]any, error) {
	resp, err := h.MakeOperatorRequest(http.MethodPost, "/_seam/capture/save", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("save corpus: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("save corpus returned %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode save response: %w", err)
	}
	return result, nil
}

// LoadCorpus reads the corpus written by TriggerManualSave.
func (h *ProxyTestHarness) LoadCorpus() (*CorpusFile, error) {
	data, err := os.ReadFile(filepath.Join(h.corpusDir, "corpus.json"))
	if err != nil {
		return nil, fmt.Errorf("read corpus: %w", err)
	}

	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		return nil, fmt.Errorf("decode corpus: %w", err)
	}
	return &corpus, nil
}

// AssertResponseCorrect checks a proxy response and consumes its body.
func (h *ProxyTestHarness) AssertResponseCorrect(t *testing.T, resp *http.Response, expectedStatus int, expectedBody string) {
	t.Helper()
	defer resp.Body.Close()

	if resp.StatusCode != expectedStatus {
		t.Errorf("expected status %d, got %d", expectedStatus, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if expectedBody != "" && string(body) != expectedBody {
		t.Errorf("expected body %q, got %q", expectedBody, string(body))
	}
}

func (h *ProxyTestHarness) waitForBackendRequest(t *testing.T) proxyBackendRequest {
	t.Helper()
	select {
	case req := <-h.backendReqs:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the upstream request")
		return proxyBackendRequest{}
	}
}

func TestProxyCaptureDisabled(t *testing.T) {
	h := NewProxyTestHarness(t, false)

	status, err := h.GetCaptureStatus()
	if err != nil {
		t.Fatalf("get capture status: %v", err)
	}
	if enabled, ok := status["enabled"].(bool); !ok || enabled {
		t.Fatalf("expected capture to be disabled, got %v", status["enabled"])
	}

	resp, err := h.MakeTestRequest(http.MethodPost, proxyTestPath+"?mode=disabled", []byte(`{"request":"body"}`), map[string]string{
		"Content-Type":  "application/json",
		"X-Test-Header": "disabled",
	})
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	h.AssertResponseCorrect(t, resp, http.StatusOK, `{"proxied":true}`)

	upstream := h.waitForBackendRequest(t)
	if upstream.method != http.MethodPost || upstream.path != proxyTestPath || upstream.query != "mode=disabled" {
		t.Fatalf("unexpected upstream request: %#v", upstream)
	}
	if string(upstream.body) != `{"request":"body"}` || upstream.header.Get("X-Test-Header") != "disabled" {
		t.Fatalf("request was not forwarded intact: %#v", upstream)
	}

	if _, err := os.Stat(filepath.Join(h.corpusDir, "corpus.json")); !os.IsNotExist(err) {
		t.Fatalf("capture-disabled request created a corpus: %v", err)
	}
}

func TestProxyCaptureEnabled(t *testing.T) {
	h := NewProxyTestHarness(t, true)

	status, err := h.GetCaptureStatus()
	if err != nil {
		t.Fatalf("get capture status: %v", err)
	}
	if enabled, ok := status["enabled"].(bool); !ok || !enabled {
		t.Fatalf("expected capture to be enabled, got %v", status["enabled"])
	}

	body := []byte(`{"request":"body"}`)
	resp, err := h.MakeTestRequest(http.MethodPost, proxyTestPath+"?mode=enabled", body, map[string]string{
		"Content-Type":  "application/json",
		"X-Test-Header": "enabled",
	})
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	h.AssertResponseCorrect(t, resp, http.StatusOK, `{"proxied":true}`)

	upstream := h.waitForBackendRequest(t)
	if upstream.method != http.MethodPost || upstream.path != proxyTestPath || upstream.query != "mode=enabled" {
		t.Fatalf("unexpected upstream request: %#v", upstream)
	}

	result, err := h.TriggerManualSave()
	if err != nil {
		t.Fatalf("save corpus: %v", err)
	}
	if result["status"] != "saved" {
		t.Fatalf("expected saved status, got %v", result["status"])
	}

	corpus, err := h.LoadCorpus()
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	if corpus.Schema != "seam-diff-corpus/v1" || len(corpus.Entries) != 1 {
		t.Fatalf("unexpected corpus metadata: schema=%q entries=%d", corpus.Schema, len(corpus.Entries))
	}
	entry := corpus.Entries[0]
	if entry.Request.Method != http.MethodPost || entry.Request.Path != proxyTestPath || entry.Request.Query != "mode=enabled" {
		t.Fatalf("unexpected captured request: %#v", entry.Request)
	}
	decodedBody, err := base64.StdEncoding.DecodeString(entry.Request.BodyB64)
	if err != nil || string(decodedBody) != string(body) {
		t.Fatalf("captured request body mismatch: %q, %v", decodedBody, err)
	}
	if entry.Response.StatusCode != http.StatusOK || entry.Response.BodyB64 == "" {
		t.Fatalf("unexpected captured response: %#v", entry.Response)
	}
}

func TestProxyCaptureModesReturnSameResponse(t *testing.T) {
	const requestBody = `{"same":"request"}`

	for _, captureEnabled := range []bool{false, true} {
		name := "disabled"
		if captureEnabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			h := NewProxyTestHarness(t, captureEnabled)
			resp, err := h.MakeTestRequest(http.MethodPost, proxyTestPath, []byte(requestBody), map[string]string{
				"Content-Type": "application/json",
			})
			if err != nil {
				t.Fatalf("proxy request failed: %v", err)
			}
			h.AssertResponseCorrect(t, resp, http.StatusOK, `{"proxied":true}`)
			_ = h.waitForBackendRequest(t)

			if captureEnabled {
				if _, err := h.TriggerManualSave(); err != nil {
					t.Fatalf("save corpus: %v", err)
				}
				corpus, err := h.LoadCorpus()
				if err != nil {
					t.Fatalf("load corpus: %v", err)
				}
				if len(corpus.Entries) != 1 {
					t.Fatalf("expected one captured request, got %d", len(corpus.Entries))
				}
			}
		})
	}
}
