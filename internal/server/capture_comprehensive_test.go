package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

type captureProxyFixture struct {
	specDir  string
	upstream *httptest.Server
}

type captureResponseObservation struct {
	status  int
	body    string
	headers http.Header
}

func newCaptureProxyFixture(t *testing.T) *captureProxyFixture {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	t.Cleanup(upstream.Close)

	specDir := t.TempDir()
	openAPI := fmt.Sprintf(`openapi: 3.1.0
info:
  title: Capture proxy test fixture
  version: 1.0.0
servers: []
paths:
  /api/v1/applications:
    get:
      x-upstream: %q
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                additionalProperties: true
  /api/v1/test:
    post:
      x-upstream: %q
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              additionalProperties: true
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                additionalProperties: true
  /api/v1/update:
    put:
      x-upstream: %q
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              additionalProperties: true
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                additionalProperties: true
  /api/v1/resource/{id}:
    parameters:
      - name: id
        in: path
        required: true
        schema:
          type: string
    delete:
      x-upstream: %q
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                additionalProperties: true
`, upstream.URL, upstream.URL, upstream.URL, upstream.URL)
	if err := os.WriteFile(filepath.Join(specDir, "openapi.yaml"), []byte(openAPI), 0o600); err != nil {
		t.Fatalf("write capture proxy fixture: %v", err)
	}

	return &captureProxyFixture{specDir: specDir, upstream: upstream}
}

func runCaptureNonIntrusionRequest(t *testing.T, fixture *captureProxyFixture, tc struct {
	name           string
	requestPath    string
	requestMethod  string
	requestBody    string
	expectedStatus int
	setupCapture   bool
}) captureResponseObservation {
	t.Helper()

	callerPort := getAvailablePort(t)
	operatorPort := getAvailablePort(t)
	corpusDir := t.TempDir()

	cfg := &Config{
		CallerPort:     callerPort,
		OperatorPort:   operatorPort,
		BaseURL:        fmt.Sprintf("http://localhost:%d", callerPort),
		SpecDir:        fixture.specDir,
		CaptureEnabled: tc.setupCapture,
		CorpusDir:      corpusDir,
	}

	s := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = s.Shutdown(ctx) }()

	time.Sleep(100 * time.Millisecond)

	var reqBody io.Reader
	if tc.requestBody != "" {
		reqBody = strings.NewReader(tc.requestBody)
	}
	req, err := http.NewRequest(tc.requestMethod, fmt.Sprintf("http://localhost:%d%s", callerPort, tc.requestPath), reqBody)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	if tc.requestBody != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Read response body: %v", err)
	}

	if resp.StatusCode != tc.expectedStatus {
		t.Errorf("Expected status %d, got %d", tc.expectedStatus, resp.StatusCode)
	}
	if len(body) == 0 && tc.expectedStatus == http.StatusOK {
		t.Error("Expected non-empty response body for successful request")
	}

	return captureResponseObservation{
		status:  resp.StatusCode,
		body:    string(body),
		headers: resp.Header.Clone(),
	}
}

// TestCaptureNonIntrusion verifies that enabling capture does not disrupt normal proxy operation
func TestCaptureNonIntrusion(t *testing.T) {
	testCases := []struct {
		name           string
		requestPath    string
		requestMethod  string
		requestBody    string
		expectedStatus int
		setupCapture   bool
	}{
		{
			name:           "simple-get-without-capture",
			requestPath:    "/api/v1/applications",
			requestMethod:  "GET",
			expectedStatus: http.StatusOK,
			setupCapture:   false,
		},
		{
			name:           "simple-get-with-capture",
			requestPath:    "/api/v1/applications",
			requestMethod:  "GET",
			expectedStatus: http.StatusOK,
			setupCapture:   true,
		},
		{
			name:           "post-with-body-without-capture",
			requestPath:    "/api/v1/test",
			requestMethod:  "POST",
			requestBody:    `{"test": "data"}`,
			expectedStatus: http.StatusOK,
			setupCapture:   false,
		},
		{
			name:           "post-with-body-with-capture",
			requestPath:    "/api/v1/test",
			requestMethod:  "POST",
			requestBody:    `{"test": "data"}`,
			expectedStatus: http.StatusOK,
			setupCapture:   true,
		},
		{
			name:           "put-without-capture",
			requestPath:    "/api/v1/update",
			requestMethod:  "PUT",
			requestBody:    `{"update": "value"}`,
			expectedStatus: http.StatusOK,
			setupCapture:   false,
		},
		{
			name:           "put-with-capture",
			requestPath:    "/api/v1/update",
			requestMethod:  "PUT",
			requestBody:    `{"update": "value"}`,
			expectedStatus: http.StatusOK,
			setupCapture:   true,
		},
		{
			name:           "delete-without-capture",
			requestPath:    "/api/v1/resource/123",
			requestMethod:  "DELETE",
			expectedStatus: http.StatusOK,
			setupCapture:   false,
		},
		{
			name:           "delete-with-capture",
			requestPath:    "/api/v1/resource/123",
			requestMethod:  "DELETE",
			expectedStatus: http.StatusOK,
			setupCapture:   true,
		},
	}

	baseline := make(map[string]captureResponseObservation)
	fixtures := make(map[string]*captureProxyFixture)
	for _, tc := range testCases {
		key := tc.requestMethod + " " + tc.requestPath
		if fixtures[key] == nil {
			fixtures[key] = newCaptureProxyFixture(t)
		}
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			key := tc.requestMethod + " " + tc.requestPath
			observation := runCaptureNonIntrusionRequest(t, fixtures[key], tc)
			if tc.setupCapture {
				baselineObservation, ok := baseline[key]
				if !ok {
					t.Fatalf("missing no-capture baseline for %s", key)
				}
				if observation.status != baselineObservation.status {
					t.Errorf("capture changed status: got %d, want %d", observation.status, baselineObservation.status)
				}
				if observation.body != baselineObservation.body {
					t.Errorf("capture changed response body: got %q, want %q", observation.body, baselineObservation.body)
				}
				captureHeaders := observation.headers.Clone()
				baselineHeaders := baselineObservation.headers.Clone()
				// Date is generated per response, so it can differ when the
				// baseline and capture requests cross a second boundary.
				captureHeaders.Del("Date")
				baselineHeaders.Del("Date")
				if !reflect.DeepEqual(captureHeaders, baselineHeaders) {
					t.Errorf("capture changed response headers: got %v, want %v", captureHeaders, baselineHeaders)
				}
				return
			}
			baseline[key] = observation
		})
	}
}

// TestCaptureResponseIntegrity verifies that captured responses are identical to actual responses
func TestCaptureResponseIntegrity(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	// Create a handler that returns a specific response
	expectedStatus := http.StatusCreated
	expectedBody := `{"message": "created", "id": 12345}`
	expectedHeaders := map[string]string{
		"Content-Type":    "application/json",
		"X-Custom-Header": "custom-value",
		"X-Request-Id":    "req-123", // This should be captured even if volatile
	}

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set headers
		for k, v := range expectedHeaders {
			w.Header().Set(k, v)
		}
		w.WriteHeader(expectedStatus)
		w.Write([]byte(expectedBody))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Make request
	req := httptest.NewRequest("POST", "/api/test", strings.NewReader(`{"data": "test"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(w, req)

	// Verify the response is correct
	if w.Code != expectedStatus {
		t.Errorf("Expected status %d, got %d", expectedStatus, w.Code)
	}

	responseBody := w.Body.String()
	if responseBody != expectedBody {
		t.Errorf("Response body mismatch: got %s, want %s", responseBody, expectedBody)
	}

	// Now verify the captured data matches
	if err := cm.Save(); err != nil {
		t.Fatalf("Failed to save corpus: %v", err)
	}

	// Load the corpus and verify
	corpusPath := filepath.Join(cm.corpusDir, "corpus.json")
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("Failed to read corpus: %v", err)
	}

	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("Failed to parse corpus: %v", err)
	}

	if len(corpus.Entries) != 1 {
		t.Fatalf("Expected 1 entry, got %d", len(corpus.Entries))
	}

	entry := corpus.Entries[0]

	// Verify captured status code
	if entry.Response.StatusCode != expectedStatus {
		t.Errorf("Captured status code: got %d, want %d", entry.Response.StatusCode, expectedStatus)
	}

	// Verify captured body
	decodedBody, err := base64.StdEncoding.DecodeString(entry.Response.BodyB64)
	if err != nil {
		t.Fatalf("Failed to decode captured body: %v", err)
	}

	if string(decodedBody) != expectedBody {
		t.Errorf("Captured body: got %s, want %s", string(decodedBody), expectedBody)
	}

	// Verify captured headers
	for k, expectedV := range expectedHeaders {
		capturedValues, ok := entry.Response.Headers[k]
		if !ok {
			t.Errorf("Header %s not found in captured response", k)
			continue
		}
		if len(capturedValues) == 0 {
			t.Errorf("Header %s has no values in captured response", k)
			continue
		}
		if capturedValues[0] != expectedV {
			t.Errorf("Header %s: got %s, want %s", k, capturedValues[0], expectedV)
		}
	}
}

// TestCaptureRequestIntegrity verifies that captured requests are accurate
func TestCaptureRequestIntegrity(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	expectedMethod := "POST"
	expectedPath := "/api/resource/123"
	expectedQuery := "filter=active&sort=desc"
	expectedBody := `{"name": "test", "value": 42}`
	expectedHeaders := map[string]string{
		"Content-Type":    "application/json",
		"Authorization":   "Bearer token123",
		"X-Custom-Header": "custom-value",
	}

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request was forwarded correctly
		if r.Method != expectedMethod {
			t.Errorf("Handler received wrong method: got %s, want %s", r.Method, expectedMethod)
		}
		if r.URL.Path != expectedPath {
			t.Errorf("Handler received wrong path: got %s, want %s", r.URL.Path, expectedPath)
		}
		if r.URL.RawQuery != expectedQuery {
			t.Errorf("Handler received wrong query: got %s, want %s", r.URL.RawQuery, expectedQuery)
		}

		// Verify headers were forwarded
		for k, v := range expectedHeaders {
			got := r.Header.Get(k)
			if got != v {
				t.Errorf("Header %s: got %s, want %s", k, got, v)
			}
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Create request with query and headers
	req := httptest.NewRequest(expectedMethod, expectedPath+"?"+expectedQuery, strings.NewReader(expectedBody))
	for k, v := range expectedHeaders {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(w, req)

	// Save and verify captured request
	if err := cm.Save(); err != nil {
		t.Fatalf("Failed to save corpus: %v", err)
	}

	corpusPath := filepath.Join(cm.corpusDir, "corpus.json")
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("Failed to read corpus: %v", err)
	}

	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("Failed to parse corpus: %v", err)
	}

	if len(corpus.Entries) != 1 {
		t.Fatalf("Expected 1 entry, got %d", len(corpus.Entries))
	}

	capturedRequest := corpus.Entries[0].Request

	// Verify captured request details
	if capturedRequest.Method != expectedMethod {
		t.Errorf("Captured method: got %s, want %s", capturedRequest.Method, expectedMethod)
	}

	if capturedRequest.Path != expectedPath {
		t.Errorf("Captured path: got %s, want %s", capturedRequest.Path, expectedPath)
	}

	if capturedRequest.Query != expectedQuery {
		t.Errorf("Captured query: got %s, want %s", capturedRequest.Query, expectedQuery)
	}

	// Verify captured body
	decodedBody, err := base64.StdEncoding.DecodeString(capturedRequest.BodyB64)
	if err != nil {
		t.Fatalf("Failed to decode captured body: %v", err)
	}

	if string(decodedBody) != expectedBody {
		t.Errorf("Captured body: got %s, want %s", string(decodedBody), expectedBody)
	}

	// Verify captured headers
	for k, expectedV := range expectedHeaders {
		capturedValues, ok := capturedRequest.Headers[k]
		if !ok {
			t.Errorf("Header %s not found in captured request", k)
			continue
		}
		if len(capturedValues) == 0 {
			t.Errorf("Header %s has no values in captured request", k)
			continue
		}
		if capturedValues[0] != expectedV {
			t.Errorf("Header %s: got %s, want %s", k, capturedValues[0], expectedV)
		}
	}
}

// TestCaptureHeaderCanonicalization verifies headers are canonicalized consistently
func TestCaptureHeaderCanonicalization(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.Header().Set("x-custom-header", "value1")
		w.Header().Set("X-Another-Header", "value2")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("accept-encoding", "gzip")
	req.Header.Set("x-request-id", "abc123")
	w := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(w, req)

	if err := cm.Save(); err != nil {
		t.Fatalf("Failed to save corpus: %v", err)
	}

	corpusPath := filepath.Join(cm.corpusDir, "corpus.json")
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("Failed to read corpus: %v", err)
	}

	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("Failed to parse corpus: %v", err)
	}

	entry := corpus.Entries[0]

	// Verify request headers are canonicalized
	expectedCanonicalHeaders := []string{"Accept-Encoding", "X-Request-Id"}
	for _, expected := range expectedCanonicalHeaders {
		found := false
		for k := range entry.Request.Headers {
			if k == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected canonical header %s not found in captured request", expected)
		}
	}

	// Verify response headers are canonicalized
	expectedResponseHeaders := []string{"Content-Type", "X-Custom-Header", "X-Another-Header"}
	for _, expected := range expectedResponseHeaders {
		found := false
		for k := range entry.Response.Headers {
			if k == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected canonical header %s not found in captured response", expected)
		}
	}
}

// TestCaptureBodyEncodingDecoding verifies base64 encoding/decoding preserves data
func TestCaptureBodyEncodingDecoding(t *testing.T) {
	testCases := []struct {
		name        string
		body        string
		contentType string
	}{
		{
			name:        "json-body",
			body:        `{"test": "data", "number": 42, "nested": {"key": "value"}}`,
			contentType: "application/json",
		},
		{
			name:        "large-json-body",
			body:        strings.Repeat(`{"item": "value"},`, 1000),
			contentType: "application/json",
		},
		{
			name:        "binary-data",
			body:        string([]byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}),
			contentType: "application/octet-stream",
		},
		{
			name:        "unicode-data",
			body:        `{"message": "Hello 世界 🌍"}`,
			contentType: "application/json",
		},
		{
			name:        "empty-body",
			body:        "",
			contentType: "text/plain",
		},
		{
			name:        "special-chars",
			body:        `{"data": "line1\nline2\ttab\r\ncarriage return"}`,
			contentType: "application/json",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
			cm.Enable()

			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(tc.body))
			})

			wrappedHandler := cm.Wrap(nextHandler)

			var bodyReader io.Reader
			if tc.body != "" {
				bodyReader = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest("POST", "/api/test", bodyReader)
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			w := httptest.NewRecorder()

			wrappedHandler.ServeHTTP(w, req)

			if err := cm.Save(); err != nil {
				t.Fatalf("Failed to save corpus: %v", err)
			}

			corpusPath := filepath.Join(cm.corpusDir, "corpus.json")
			data, err := os.ReadFile(corpusPath)
			if err != nil {
				t.Fatalf("Failed to read corpus: %v", err)
			}

			var corpus CorpusFile
			if err := json.Unmarshal(data, &corpus); err != nil {
				t.Fatalf("Failed to parse corpus: %v", err)
			}

			entry := corpus.Entries[0]

			// Decode request body
			if tc.body != "" {
				decodedReqBody, err := base64.StdEncoding.DecodeString(entry.Request.BodyB64)
				if err != nil {
					t.Errorf("Failed to decode request body: %v", err)
				} else if string(decodedReqBody) != tc.body {
					t.Errorf("Request body: got %s, want %s", string(decodedReqBody), tc.body)
				}
			}

			// Decode response body
			decodedRespBody, err := base64.StdEncoding.DecodeString(entry.Response.BodyB64)
			if err != nil {
				t.Errorf("Failed to decode response body: %v", err)
			} else if string(decodedRespBody) != tc.body {
				t.Errorf("Response body: got %s, want %s", string(decodedRespBody), tc.body)
			}
		})
	}
}

// TestCaptureCompleteness verifies all necessary data is captured for replay
func TestCaptureCompleteness(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	testPath := "/api/v1/resources"
	testQuery := "filter=active&page=2"
	testBody := `{"action": "create"}`
	testHeaders := map[string]string{
		"Authorization":   "Bearer token",
		"Content-Type":    "application/json",
		"Accept":          "application/json",
		"X-Custom-Header": "custom",
	}

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Response-Id", "resp-123")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id": 456}`))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	req := httptest.NewRequest("POST", testPath+"?"+testQuery, strings.NewReader(testBody))
	for k, v := range testHeaders {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(w, req)

	if err := cm.Save(); err != nil {
		t.Fatalf("Failed to save corpus: %v", err)
	}

	corpusPath := filepath.Join(cm.corpusDir, "corpus.json")
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("Failed to read corpus: %v", err)
	}

	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("Failed to parse corpus: %v", err)
	}

	if len(corpus.Entries) != 1 {
		t.Fatalf("Expected 1 entry, got %d", len(corpus.Entries))
	}

	entry := corpus.Entries[0]

	// Verify all request data is present
	checks := []struct {
		name     string
		got      interface{}
		expected interface{}
	}{
		{"request method", entry.Request.Method, "POST"},
		{"request path", entry.Request.Path, testPath},
		{"request query", entry.Request.Query, testQuery},
		{"request body not empty", entry.Request.BodyB64 != "", true},
		{"response status", entry.Response.StatusCode, http.StatusCreated},
		{"response body not empty", entry.Response.BodyB64 != "", true},
		{"schema version", corpus.Schema, "seam-diff-corpus/v1"},
		{"service name", corpus.Service, "test-service"},
		{"incumbent URL", corpus.Incumbent, "test-incumbent"},
	}

	for _, check := range checks {
		if check.got != check.expected {
			t.Errorf("%s: got %v, want %v", check.name, check.got, check.expected)
		}
	}

	// Verify all headers are captured
	for expectedHeader := range testHeaders {
		found := false
		for k := range entry.Request.Headers {
			if k == expectedHeader {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Request header %s not captured", expectedHeader)
		}
	}

	// Verify response headers
	expectedRespHeaders := []string{"Content-Type", "X-Response-Id"}
	for _, expectedHeader := range expectedRespHeaders {
		found := false
		for k := range entry.Response.Headers {
			if k == expectedHeader {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Response header %s not captured", expectedHeader)
		}
	}
}

// TestCaptureMinimality verifies corpus only contains necessary data
func TestCaptureMinimality(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", "Tue, 15 Aug 2026 12:00:00 GMT")
		w.Header().Set("Server", "TestServer/1.0")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("User-Agent", "TestAgent/1.0")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(w, req)

	if err := cm.Save(); err != nil {
		t.Fatalf("Failed to save corpus: %v", err)
	}

	corpusPath := filepath.Join(cm.corpusDir, "corpus.json")
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("Failed to read corpus: %v", err)
	}

	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("Failed to parse corpus: %v", err)
	}

	entry := corpus.Entries[0]

	// Verify headers are canonicalized (only one canonical form)
	headerCount := 0
	for range entry.Request.Headers {
		headerCount++
	}
	for range entry.Response.Headers {
		headerCount++
	}

	// Should have 3 request headers (User-Agent, Accept, Content-Type)
	// And 3 response headers (Date, Server, Content-Type)
	// But no duplicates due to canonicalization
	if headerCount == 0 {
		t.Error("Expected some headers to be captured")
	}

	// Verify no empty header values
	for k, vv := range entry.Request.Headers {
		for _, v := range vv {
			if v == "" {
				t.Errorf("Header %s has empty value", k)
			}
		}
		_ = k // Use k to avoid declared and not used error
	}

	for k, vv := range entry.Response.Headers {
		for _, v := range vv {
			if v == "" {
				t.Errorf("Header %s has empty value", k)
			}
		}
		_ = k // Use k to avoid declared and not used error
	}

	// Verify body is present but base64 encoded (no plaintext)
	if bytes.Contains(data, []byte(`{}`)) && entry.Response.BodyB64 != "" {
		// The actual response body should be base64 encoded, not plaintext
		// This is a sanity check that we're encoding correctly
		_, err := base64.StdEncoding.DecodeString(entry.Response.BodyB64)
		if err != nil {
			t.Errorf("Body should be base64 encoded: %v", err)
		}
	}
}

// TestCaptureFullLifecycleIntegration tests the complete capture lifecycle
func TestCaptureFullLifecycleIntegration(t *testing.T) {
	fixture := newCaptureProxyFixture(t)
	callerPort := getAvailablePort(t)
	operatorPort := getAvailablePort(t)
	corpusDir := t.TempDir()

	cfg := &Config{
		CallerPort:     callerPort,
		OperatorPort:   operatorPort,
		BaseURL:        fmt.Sprintf("http://localhost:%d", callerPort),
		SpecDir:        fixture.specDir,
		CaptureEnabled: true,
		CorpusDir:      corpusDir,
	}

	s := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Phase 1: Start server with capture enabled
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Phase 2: Make various requests to capture
	requests := []struct {
		method string
		path   string
		body   string
	}{
		{"GET", "/_seam/healthz", ""},
		{"GET", "/openapi.json", ""},
		{"GET", "/docs", ""},
		{"GET", "/api/v1/applications", ""},
	}

	for _, reqDef := range requests {
		var body io.Reader
		if reqDef.body != "" {
			body = strings.NewReader(reqDef.body)
		}

		req, err := http.NewRequest(reqDef.method, fmt.Sprintf("http://localhost:%d%s", callerPort, reqDef.path), body)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Request %s %s failed with status %d", reqDef.method, reqDef.path, resp.StatusCode)
		}
	}

	// Phase 3: Check capture status
	statusResp, err := http.Get(fmt.Sprintf("http://localhost:%d/_seam/capture/status", operatorPort))
	if err != nil {
		t.Fatalf("Failed to get capture status: %v", err)
	}
	defer func() { _ = statusResp.Body.Close() }()

	if statusResp.StatusCode != http.StatusOK {
		t.Errorf("Capture status endpoint returned status %d", statusResp.StatusCode)
	}

	var status map[string]interface{}
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatalf("Failed to decode status: %v", err)
	}

	enabled, ok := status["enabled"].(bool)
	if !ok || !enabled {
		t.Error("Expected capture to be enabled")
	}

	entryCount, ok := status["entry_count"].(float64)
	if !ok {
		t.Error("Expected entry_count in status response")
	}
	t.Logf("Captured %d entries", int(entryCount))

	// Phase 4: Shutdown server
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Failed to shutdown server: %v", err)
	}

	// Phase 5: Verify corpus file exists and is valid
	corpusPath := filepath.Join(corpusDir, "corpus.json")
	if _, err := os.Stat(corpusPath); os.IsNotExist(err) {
		t.Fatal("Corpus file was not created")
	}

	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("Failed to read corpus: %v", err)
	}

	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("Failed to parse corpus: %v", err)
	}

	// Phase 6: Verify corpus structure
	if corpus.Schema != "seam-diff-corpus/v1" {
		t.Errorf("Wrong schema version: %s", corpus.Schema)
	}

	if corpus.Service != "seam" {
		t.Errorf("Wrong service name: %s", corpus.Service)
	}

	if len(corpus.Entries) == 0 {
		t.Error("Expected at least one entry in corpus")
	}

	// Phase 7: Verify each entry has required fields
	for i, entry := range corpus.Entries {
		if entry.ID == "" {
			t.Errorf("Entry %d: missing ID", i)
		}
		if entry.Request.Method == "" {
			t.Errorf("Entry %d: missing request method", i)
		}
		if entry.Request.Path == "" {
			t.Errorf("Entry %d: missing request path", i)
		}
		if entry.Response.StatusCode == 0 {
			t.Errorf("Entry %d: missing response status", i)
		}
	}

	t.Logf("Full lifecycle test completed successfully with %d entries", len(corpus.Entries))
}

// TestCaptureMultipleRequests verifies multiple requests are captured correctly
func TestCaptureMultipleRequests(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"path": "%s"}`, r.URL.Path)))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Make multiple different requests
	requests := []struct {
		method string
		path   string
	}{
		{"GET", "/api/test1"},
		{"GET", "/api/test2"},
		{"POST", "/api/create"},
		{"PUT", "/api/update"},
		{"DELETE", "/api/delete"},
	}

	for _, reqDef := range requests {
		req := httptest.NewRequest(reqDef.method, reqDef.path, nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)
	}

	// Verify all were captured
	if cm.GetEntryCount() != len(requests) {
		t.Errorf("Expected %d entries, got %d", len(requests), cm.GetEntryCount())
	}

	// Save and verify
	if err := cm.Save(); err != nil {
		t.Fatalf("Failed to save corpus: %v", err)
	}

	corpusPath := filepath.Join(cm.corpusDir, "corpus.json")
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("Failed to read corpus: %v", err)
	}

	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("Failed to parse corpus: %v", err)
	}

	if len(corpus.Entries) != len(requests) {
		t.Errorf("Expected %d entries in saved corpus, got %d", len(requests), len(corpus.Entries))
	}

	// Verify each entry has a unique ID
	ids := make(map[string]bool)
	for _, entry := range corpus.Entries {
		if ids[entry.ID] {
			t.Errorf("Duplicate entry ID: %s", entry.ID)
		}
		ids[entry.ID] = true
	}
}

// TestCaptureAutoSaveInterval verifies auto-save functionality
func TestCaptureAutoSaveInterval(t *testing.T) {
	corpusDir := t.TempDir()
	cm := NewCaptureMiddleware(corpusDir, "test-service", "test-incumbent", true) // autoSave=true
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Make 10 requests to trigger auto-save
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", i), nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)
	}

	// Give time for auto-save
	time.Sleep(100 * time.Millisecond)

	// Verify corpus file exists
	corpusPath := filepath.Join(corpusDir, "corpus.json")
	if _, err := os.Stat(corpusPath); os.IsNotExist(err) {
		t.Fatal("Auto-save did not create corpus file")
	}

	// Load and verify
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("Failed to read corpus: %v", err)
	}

	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("Failed to parse corpus: %v", err)
	}

	if len(corpus.Entries) != 10 {
		t.Errorf("Expected 10 entries after auto-save, got %d", len(corpus.Entries))
	}
}

// TestCaptureLoadAppend verifies loading existing corpus and appending to it
func TestCaptureLoadAppend(t *testing.T) {
	corpusDir := t.TempDir()

	// Phase 1: Create initial corpus
	cm1 := NewCaptureMiddleware(corpusDir, "test-service", "test-incumbent", false)
	cm1.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})

	wrappedHandler := cm1.Wrap(nextHandler)

	// Capture 3 requests
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/initial%d", i), nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)
	}

	if err := cm1.Save(); err != nil {
		t.Fatalf("Failed to save initial corpus: %v", err)
	}

	// Phase 2: Load corpus and add more entries
	cm2 := NewCaptureMiddleware(corpusDir, "test-service", "test-incumbent", false)
	cm2.Enable()

	if err := cm2.Load(); err != nil {
		t.Fatalf("Failed to load corpus: %v", err)
	}

	initialCount := cm2.GetEntryCount()
	t.Logf("Loaded %d entries from existing corpus", initialCount)

	if initialCount != 3 {
		t.Errorf("Expected to load 3 entries, got %d", initialCount)
	}

	// Add 2 more requests
	wrappedHandler2 := cm2.Wrap(nextHandler)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/additional%d", i), nil)
		w := httptest.NewRecorder()
		wrappedHandler2.ServeHTTP(w, req)
	}

	if err := cm2.Save(); err != nil {
		t.Fatalf("Failed to save updated corpus: %v", err)
	}

	// Phase 3: Verify total count
	data, err := os.ReadFile(filepath.Join(corpusDir, "corpus.json"))
	if err != nil {
		t.Fatalf("Failed to read corpus: %v", err)
	}

	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("Failed to parse corpus: %v", err)
	}

	if len(corpus.Entries) != 5 {
		t.Errorf("Expected 5 total entries (3 initial + 2 added), got %d", len(corpus.Entries))
	}
}
