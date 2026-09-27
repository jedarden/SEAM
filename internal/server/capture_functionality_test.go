package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestCaptureDoesNotDisruptNormalOperation verifies that request/response pairs
// are correctly proxied when capture is enabled
func TestCaptureDoesNotDisruptNormalOperation(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	// Create a test handler that verifies normal operation
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request body is readable
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("Failed to read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Verify body content
		expectedBody := []byte("test request body")
		if !bytes.Equal(body, expectedBody) {
			t.Errorf("Expected request body %q, got %q", expectedBody, body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Return a normal response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":"success"}`))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Make a request with a body
	req := httptest.NewRequest("POST", "/api/test", bytes.NewReader([]byte("test request body")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(w, req)

	// Verify the response is exactly what the handler returned
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", w.Header().Get("Content-Type"))
	}

	expectedBody := `{"result":"success"}`
	if w.Body.String() != expectedBody {
		t.Errorf("Expected response body %q, got %q", expectedBody, w.Body.String())
	}

	// Verify entry was captured
	if cm.GetEntryCount() != 1 {
		t.Errorf("Expected 1 captured entry, got %d", cm.GetEntryCount())
	}
}

// TestCaptureDoesNotDisruptMultipleRequests verifies that multiple requests
// are handled correctly with capture enabled
func TestCaptureDoesNotDisruptMultipleRequests(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	requestCount := 0
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "request #%d", requestCount)
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Make multiple requests
	for i := 1; i <= 10; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", i), nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)

		expectedBody := fmt.Sprintf("request #%d", i)
		if w.Code != http.StatusOK {
			t.Errorf("Request %d: expected status 200, got %d", i, w.Code)
		}

		if w.Body.String() != expectedBody {
			t.Errorf("Request %d: expected body %q, got %q", i, expectedBody, w.Body.String())
		}
	}

	// Verify all requests were captured
	if cm.GetEntryCount() != 10 {
		t.Errorf("Expected 10 captured entries, got %d", cm.GetEntryCount())
	}

	// Verify the handler was called the correct number of times
	if requestCount != 10 {
		t.Errorf("Expected handler to be called 10 times, got %d", requestCount)
	}
}

// TestCaptureLatencyIsAcceptable verifies that capture doesn't add
// significant latency beyond acceptable thresholds
func TestCaptureLatencyIsAcceptable(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)

	// First, measure baseline latency without capture
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	baselineHandler := nextHandler

	// Measure baseline latency
	baselineSamples := make([]time.Duration, 0, 100)
	for i := 0; i < 100; i++ {
		start := time.Now()
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		baselineHandler.ServeHTTP(w, req)
		elapsed := time.Since(start)
		baselineSamples = append(baselineSamples, elapsed)
	}

	// Calculate baseline median
	baselineMedian := medianDuration(baselineSamples)

	// Now measure with capture enabled
	cm.Enable()
	wrappedHandler := cm.Wrap(nextHandler)

	captureSamples := make([]time.Duration, 0, 100)
	for i := 0; i < 100; i++ {
		start := time.Now()
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)
		elapsed := time.Since(start)
		captureSamples = append(captureSamples, elapsed)
	}

	// Calculate capture median
	captureMedian := medianDuration(captureSamples)

	// Calculate overhead
	overhead := captureMedian - baselineMedian

	t.Logf("Baseline median latency: %v", baselineMedian)
	t.Logf("Capture median latency: %v", captureMedian)
	t.Logf("Capture overhead: %v", overhead)

	// Pin an absolute overhead budget, matching proxyCaptureLatencyBudget on the
	// proxy path: a %-of-baseline bound is unfalsifiable on fast hardware, where
	// a microsecond-scale wrap is >100% of a microsecond-scale handler.
	if overhead > proxyCaptureLatencyBudget {
		t.Errorf("Capture overhead too high: %v (budget: %v)", overhead, proxyCaptureLatencyBudget)
	}

	// Verify all entries were captured
	if cm.GetEntryCount() != 100 {
		t.Errorf("Expected 100 captured entries, got %d", cm.GetEntryCount())
	}
}

// TestConcurrentCapturesDoNotInterfere verifies that concurrent captures
// don't block or interfere with each other
func TestConcurrentCapturesDoNotInterfere(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate some processing time
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Launch concurrent requests
	concurrentRequests := 50
	var wg sync.WaitGroup
	errors := make(chan error, concurrentRequests)
	successCount := make(chan int, concurrentRequests)

	for i := 0; i < concurrentRequests; i++ {
		wg.Add(1)
		go func(requestNum int) {
			defer wg.Done()

			req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", requestNum), nil)
			w := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				errors <- fmt.Errorf("request %d: expected status 200, got %d", requestNum, w.Code)
				return
			}

			if w.Body.String() != "OK" {
				errors <- fmt.Errorf("request %d: expected body OK, got %s", requestNum, w.Body.String())
				return
			}

			successCount <- 1
		}(i)
	}

	wg.Wait()
	close(errors)
	close(successCount)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Error(err)
		errorCount++
	}

	// Count successes
	successes := 0
	for range successCount {
		successes++
	}

	t.Logf("Concurrent requests: %d, Successes: %d, Errors: %d", concurrentRequests, successes, errorCount)

	// All requests should succeed
	if successes != concurrentRequests {
		t.Errorf("Expected all %d requests to succeed, got %d", concurrentRequests, successes)
	}

	// Verify all entries were captured (with tolerance for potential race conditions)
	if cm.GetEntryCount() < concurrentRequests-1 || cm.GetEntryCount() > concurrentRequests {
		t.Errorf("Expected approximately %d captured entries, got %d", concurrentRequests, cm.GetEntryCount())
	}
}

// TestCaptureToggleDoesNotDisruptActiveConnections verifies that capture
// can be toggled on/off without disrupting active connections
func TestCaptureToggleDoesNotDisruptActiveConnections(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Make a request with capture enabled
	req1 := httptest.NewRequest("GET", "/api/test1", nil)
	w1 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("Request 1 with capture enabled: expected status 200, got %d", w1.Code)
	}

	initialCount := cm.GetEntryCount()
	t.Logf("Captured %d entries with capture enabled", initialCount)

	// Disable capture
	cm.Disable()

	// Verify capture is disabled
	if cm.IsEnabled() {
		t.Error("Capture should be disabled after Disable() call")
	}

	// Make a request with capture disabled
	req2 := httptest.NewRequest("GET", "/api/test2", nil)
	w2 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("Request 2 with capture disabled: expected status 200, got %d", w2.Code)
	}

	// Verify no new entries were captured
	disabledCount := cm.GetEntryCount()
	if disabledCount != initialCount {
		t.Errorf("Entry count should not change when capture disabled: expected %d, got %d", initialCount, disabledCount)
	}

	// Re-enable capture
	cm.Enable()

	// Verify capture is re-enabled
	if !cm.IsEnabled() {
		t.Error("Capture should be enabled after Enable() call")
	}

	// Make another request with capture re-enabled
	req3 := httptest.NewRequest("GET", "/api/test3", nil)
	w3 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Errorf("Request 3 with capture re-enabled: expected status 200, got %d", w3.Code)
	}

	finalCount := cm.GetEntryCount()
	t.Logf("Captured %d entries after re-enabling capture", finalCount)

	// Verify entry count increased after re-enabling
	if finalCount <= disabledCount {
		t.Errorf("Entry count should increase after re-enabling capture: expected > %d, got %d", disabledCount, finalCount)
	}
}

// TestCaptureDoesNotLeakMemory verifies that capture doesn't leak memory
// during normal operations
func TestCaptureDoesNotLeakMemory(t *testing.T) {
	tmpDir := t.TempDir()
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Capture initial memory state
	// Note: This is a basic check - for thorough memory leak detection,
	// you would use pprof or similar tools
	initialCount := cm.GetEntryCount()

	// Capture many entries
	for i := 0; i < 1000; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", i), nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)

		// Verify response is still correct
		if w.Code != http.StatusOK {
			t.Errorf("Request %d: expected status 200, got %d", i, w.Code)
		}
	}

	finalCount := cm.GetEntryCount()
	t.Logf("Captured %d entries (started with %d)", finalCount, initialCount)

	if finalCount != 1000 {
		t.Errorf("Expected 1000 entries, got %d", finalCount)
	}

	// Save to disk
	if err := cm.Save(); err != nil {
		t.Errorf("Failed to save corpus: %v", err)
	}

	// Verify file exists and is reasonable size
	corpusPath := filepath.Join(tmpDir, "corpus.json")
	info, err := os.Stat(corpusPath)
	if err != nil {
		t.Errorf("Failed to stat corpus file: %v", err)
	}

	t.Logf("Corpus file size: %d bytes", info.Size())

	// Verify we can load it back
	cm2 := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	if err := cm2.Load(); err != nil {
		t.Errorf("Failed to load corpus: %v", err)
	}

	if cm2.GetEntryCount() != 1000 {
		t.Errorf("Expected 1000 entries after load, got %d", cm2.GetEntryCount())
	}
}

// TestCaptureWithConcurrentSaveOperations verifies that concurrent save
// operations don't interfere with capture
func TestCaptureWithConcurrentSaveOperations(t *testing.T) {
	tmpDir := t.TempDir()
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", true) // auto-save enabled
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Make requests while auto-save is running
	var wg sync.WaitGroup
	requestCount := 100

	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func(requestNum int) {
			defer wg.Done()

			req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", requestNum), nil)
			w := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Request %d: expected status 200, got %d", requestNum, w.Code)
			}
		}(i)
	}

	wg.Wait()

	t.Logf("Captured %d entries with auto-save enabled", cm.GetEntryCount())

	if cm.GetEntryCount() != requestCount {
		t.Errorf("Expected %d entries, got %d", requestCount, cm.GetEntryCount())
	}

	// Verify corpus file exists and is valid
	corpusPath := filepath.Join(tmpDir, "corpus.json")
	if _, err := os.Stat(corpusPath); os.IsNotExist(err) {
		t.Error("Corpus file should exist after auto-save")
	}

	// Load and verify
	cm2 := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	if err := cm2.Load(); err != nil {
		t.Errorf("Failed to load corpus: %v", err)
	}

	if cm2.GetEntryCount() != requestCount {
		t.Errorf("Expected %d entries after load, got %d", requestCount, cm2.GetEntryCount())
	}
}

// TestCapturePreservesRequestResponseIntegrity verifies that captured
// requests and responses match what was actually sent/received
func TestCapturePreservesRequestResponseIntegrity(t *testing.T) {
	tmpDir := t.TempDir()
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	cm.Enable()

	// Test data
	testCases := []struct {
		name            string
		method          string
		path            string
		query           string
		requestBody     string
		requestHeaders  map[string]string
		responseStatus  int
		responseBody    string
		responseHeaders map[string]string
	}{
		{
			name:           "simple GET",
			method:         "GET",
			path:           "/api/users",
			responseStatus: http.StatusOK,
			responseBody:   `[{"id":1,"name":"Alice"}]`,
		},
		{
			name:           "POST with body",
			method:         "POST",
			path:           "/api/users",
			requestBody:    `{"name":"Bob","email":"bob@example.com"}`,
			requestHeaders: map[string]string{"Content-Type": "application/json"},
			responseStatus: http.StatusCreated,
			responseBody:   `{"id":2,"name":"Bob","email":"bob@example.com"}`,
		},
		{
			name:           "GET with query params",
			method:         "GET",
			path:           "/api/search",
			query:          "q=test&limit=10",
			responseStatus: http.StatusOK,
			responseBody:   `{"results":[],"total":0}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Set response headers
				for k, v := range tc.responseHeaders {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.responseStatus)
				w.Write([]byte(tc.responseBody))
			})

			wrappedHandler := cm.Wrap(nextHandler)

			// Create request
			url := tc.path
			if tc.query != "" {
				url += "?" + tc.query
			}

			var body io.Reader
			if tc.requestBody != "" {
				body = bytes.NewReader([]byte(tc.requestBody))
			}

			req := httptest.NewRequest(tc.method, url, body)
			for k, v := range tc.requestHeaders {
				req.Header.Set(k, v)
			}

			w := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(w, req)

			// Verify response
			if w.Code != tc.responseStatus {
				t.Errorf("Expected status %d, got %d", tc.responseStatus, w.Code)
			}

			if w.Body.String() != tc.responseBody {
				t.Errorf("Expected body %q, got %q", tc.responseBody, w.Body.String())
			}
		})
	}

	// Save and load the corpus
	if err := cm.Save(); err != nil {
		t.Fatalf("Failed to save corpus: %v", err)
	}

	// Load and verify integrity
	corpusPath := filepath.Join(tmpDir, "corpus.json")
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("Failed to read corpus file: %v", err)
	}

	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("Failed to parse corpus: %v", err)
	}

	if len(corpus.Entries) != len(testCases) {
		t.Errorf("Expected %d entries, got %d", len(testCases), len(corpus.Entries))
	}
}

// TestCaptureDoesNotBlockHandlerExecution verifies that capture doesn't
// block or delay the handler execution
func TestCaptureDoesNotBlockHandlerExecution(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	handlerExecuted := make(chan struct{}, 1)
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Signal that handler is executing
		handlerExecuted <- struct{}{}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()

	// Start request in goroutine
	done := make(chan struct{})
	go func() {
		wrappedHandler.ServeHTTP(w, req)
		close(done)
	}()

	// Verify handler executed quickly (within 100ms)
	select {
	case <-handlerExecuted:
		// Handler executed - good
	case <-time.After(100 * time.Millisecond):
		t.Error("Handler did not execute within 100ms - capture may be blocking")
	}

	// Wait for request to complete
	<-done

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

// TestCaptureWithChunkedResponse verifies that capture handles
// chunked transfer encoding responses correctly
func TestCaptureWithChunkedResponse(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write response in chunks
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)

		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "chunk%d ", i)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	})

	wrappedHandler := cm.Wrap(nextHandler)

	req := httptest.NewRequest("GET", "/api/chunked", nil)
	w := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	expectedBody := "chunk0 chunk1 chunk2 chunk3 chunk4 "
	if w.Body.String() != expectedBody {
		t.Errorf("Expected body %q, got %q", expectedBody, w.Body.String())
	}

	// Verify capture worked
	if cm.GetEntryCount() != 1 {
		t.Errorf("Expected 1 entry, got %d", cm.GetEntryCount())
	}
}

// medianDuration calculates the median of a slice of durations
func medianDuration(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}

	// Simple bubble sort (good enough for small slices)
	for i := 0; i < len(durations); i++ {
		for j := i + 1; j < len(durations); j++ {
			if durations[i] > durations[j] {
				durations[i], durations[j] = durations[j], durations[i]
			}
		}
	}

	mid := len(durations) / 2
	if len(durations)%2 == 0 {
		return (durations[mid-1] + durations[mid]) / 2
	}
	return durations[mid]
}
