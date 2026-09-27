package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCaptureDoesNotDisruptRequestResponse verifies that request/response pairs
// are correctly proxied when capture is enabled
func TestCaptureDoesNotDisruptRequestResponse(t *testing.T) {
	tmpDir := t.TempDir()
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	cm.Enable()

	// Create a backend handler that returns specific content
	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request details
		if r.Method != "POST" {
			t.Errorf("Expected POST method, got %s", r.Method)
		}
		if r.URL.Path != "/api/test" {
			t.Errorf("Expected path /api/test, got %s", r.URL.Path)
		}

		// Read and verify request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("Failed to read request body: %v", err)
		}
		expectedBody := []byte(`{"test":"data"}`)
		if string(body) != string(expectedBody) {
			t.Errorf("Expected body %s, got %s", expectedBody, body)
		}

		// Return a response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"result":"success"}`))
	})

	wrappedHandler := cm.Wrap(backendHandler)

	// Create a request with body
	req := httptest.NewRequest("POST", "/api/test", strings.NewReader(`{"test":"data"}`))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()

	// Execute the request
	wrappedHandler.ServeHTTP(w, req)

	// Verify response is correct
	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", resp.StatusCode)
	}

	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", resp.Header.Get("Content-Type"))
	}

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Errorf("Failed to read response body: %v", err)
	}

	if string(responseBody) != `{"result":"success"}` {
		t.Errorf("Expected response body %s, got %s", `{"result":"success"}`, responseBody)
	}

	// Verify capture occurred
	if cm.GetEntryCount() != 1 {
		t.Errorf("Expected 1 captured entry, got %d", cm.GetEntryCount())
	}
}

// TestCaptureLatencyWithinThresholds verifies that capture doesn't add
// significant latency beyond normal proxy operation
func TestCaptureLatencyWithinThresholds(t *testing.T) {
	tmpDir := t.TempDir()

	// Test with capture disabled first
	cmDisabled := NewCaptureMiddleware(tmpDir+"-disabled", "test-service", "test-incumbent", false)
	cmDisabled.Disable()

	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedDisabled := cmDisabled.Wrap(backendHandler)

	// Measure latency without capture
	var disabledLatencies []time.Duration
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()

		start := time.Now()
		wrappedDisabled.ServeHTTP(w, req)
		latency := time.Since(start)

		disabledLatencies = append(disabledLatencies, latency)
	}

	// Calculate average latency without capture
	var disabledTotal time.Duration
	for _, l := range disabledLatencies {
		disabledTotal += l
	}
	disabledAvg := disabledTotal / time.Duration(len(disabledLatencies))

	// Now test with capture enabled
	cmEnabled := NewCaptureMiddleware(tmpDir+"-enabled", "test-service", "test-incumbent", false)
	cmEnabled.Enable()

	wrappedEnabled := cmEnabled.Wrap(backendHandler)

	// Measure latency with capture
	var enabledLatencies []time.Duration
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()

		start := time.Now()
		wrappedEnabled.ServeHTTP(w, req)
		latency := time.Since(start)

		enabledLatencies = append(enabledLatencies, latency)
	}

	// Calculate average latency with capture
	var enabledTotal time.Duration
	for _, l := range enabledLatencies {
		enabledTotal += l
	}
	enabledAvg := enabledTotal / time.Duration(len(enabledLatencies))

	t.Logf("Average latency without capture: %v", disabledAvg)
	t.Logf("Average latency with capture: %v", enabledAvg)

	// Calculate overhead
	overhead := enabledAvg - disabledAvg
	t.Logf("Capture overhead: %v", overhead)

	// Verify overhead is within acceptable threshold (< 1ms)
	threshold := 1 * time.Millisecond
	if overhead > threshold {
		t.Errorf("Capture overhead %v exceeds threshold %v", overhead, threshold)
	}

	// Verify response correctness wasn't affected
	if cmEnabled.GetEntryCount() != 100 {
		t.Errorf("Expected 100 captured entries, got %d", cmEnabled.GetEntryCount())
	}
}

// TestConcurrentRequestsDuringCapture verifies that concurrent requests
// don't block or interfere with each other during capture
func TestConcurrentRequestsDuringCapture(t *testing.T) {
	tmpDir := t.TempDir()
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	cm.Enable()

	// Create a backend handler with slight delay to simulate real work
	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(backendHandler)

	// Launch concurrent requests
	numRequests := 50
	var wg sync.WaitGroup
	successCount := atomic.Int32{}
	errors := make([]error, 0)
	var errorsMu sync.Mutex

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(requestID int) {
			defer wg.Done()

			req := httptest.NewRequest("GET", "/api/test", nil)
			w := httptest.NewRecorder()

			wrappedHandler.ServeHTTP(w, req)

			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusOK {
				errorsMu.Lock()
				errors = append(errors, fmt.Errorf("request %d failed with status %d", requestID, resp.StatusCode))
				errorsMu.Unlock()
				return
			}

			successCount.Add(1)
		}(i)
	}

	wg.Wait()

	// Verify all requests succeeded
	if successCount.Load() != int32(numRequests) {
		t.Errorf("Expected %d successful requests, got %d", numRequests, successCount.Load())
	}

	if len(errors) > 0 {
		t.Errorf("Encountered %d errors during concurrent requests: %v", len(errors), errors)
	}

	// Verify all entries were captured
	if cm.GetEntryCount() != numRequests {
		t.Errorf("Expected %d captured entries, got %d", numRequests, cm.GetEntryCount())
	}
}

// TestConcurrentSavesDuringRequests verifies that saving corpus doesn't
// interfere with ongoing request processing
func TestConcurrentSavesDuringRequests(t *testing.T) {
	tmpDir := t.TempDir()
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	cm.Enable()

	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(backendHandler)

	// Start processing requests concurrently
	requestCount := 30
	requestsDone := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := 0; i < requestCount; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := httptest.NewRequest("GET", "/api/test", nil)
				w := httptest.NewRecorder()
				wrappedHandler.ServeHTTP(w, req)
			}()
		}
		wg.Wait()
		close(requestsDone)
	}()

	// Intersperse saves throughout the request processing
	savesDone := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			time.Sleep(2 * time.Millisecond)
			err := cm.Save()
			if err != nil {
				t.Errorf("Save failed during concurrent requests: %v", err)
			}
		}
		close(savesDone)
	}()

	// Wait for both to complete
	<-requestsDone
	<-savesDone

	// Verify final state
	if cm.GetEntryCount() != requestCount {
		t.Errorf("Expected %d entries, got %d", requestCount, cm.GetEntryCount())
	}

	// Verify corpus file is valid
	corpusPath := filepath.Join(tmpDir, "corpus.json")
	if _, err := os.Stat(corpusPath); os.IsNotExist(err) {
		t.Error("Corpus file should exist after concurrent operations")
	}

	// Load and verify
	cm2 := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	if err := cm2.Load(); err != nil {
		t.Errorf("Failed to load corpus after concurrent operations: %v", err)
	}

	if cm2.GetEntryCount() == 0 {
		t.Error("Expected entries to be saved and loaded correctly")
	}
}

// TestCaptureToggleDuringActiveConnections verifies that enabling/disabling
// capture doesn't disrupt active connections
func TestCaptureToggleDuringActiveConnections(t *testing.T) {
	tmpDir := t.TempDir()
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	cm.Enable()

	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(backendHandler)

	// Make some requests with capture enabled
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Request failed with capture enabled")
		}
	}

	initialCount := cm.GetEntryCount()
	if initialCount != 10 {
		t.Errorf("Expected 10 entries, got %d", initialCount)
	}

	// Disable capture
	cm.Disable()

	// Make more requests - should not be captured
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Request failed with capture disabled")
		}
	}

	// Verify no new entries were captured
	if cm.GetEntryCount() != initialCount {
		t.Errorf("Entry count changed after disabling capture: %d -> %d",
			initialCount, cm.GetEntryCount())
	}

	// Re-enable capture
	cm.Enable()

	// Make more requests - should be captured again
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Request failed after re-enabling capture")
		}
	}

	// Verify new entries were captured
	finalCount := cm.GetEntryCount()
	expectedCount := initialCount + 10
	if finalCount != expectedCount {
		t.Errorf("Expected %d entries after re-enabling, got %d", expectedCount, finalCount)
	}

	// Save and verify persistence
	err := cm.Save()
	if err != nil {
		t.Errorf("Failed to save after toggling: %v", err)
	}

	// Load into new instance
	cm2 := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	if err := cm2.Load(); err != nil {
		t.Errorf("Failed to load after toggling: %v", err)
	}

	if cm2.GetEntryCount() != finalCount {
		t.Errorf("Loaded count %d doesn't match saved count %d",
			cm2.GetEntryCount(), finalCount)
	}
}

// TestNoMemoryLeaksDuringCapture verifies that repeated capture operations
// don't leak memory
func TestNoMemoryLeaksDuringCapture(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory leak test in short mode")
	}

	tmpDir := t.TempDir()
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	cm.Enable()

	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(backendHandler)

	// Force GC and get initial memory stats
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	// Perform many capture operations
	iterations := 1000
	for i := 0; i < iterations; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)

		// Periodically save to test that too
		if i%100 == 0 {
			_ = cm.Save()
		}
	}

	// Force GC again and get final memory stats
	runtime.GC()
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	// Calculate memory growth
	allocGrowth := m2.TotalAlloc - m1.TotalAlloc
	heapGrowth := m2.HeapAlloc - m1.HeapAlloc

	t.Logf("Allocation growth: %d bytes", allocGrowth)
	t.Logf("Heap growth: %d bytes", heapGrowth)
	t.Logf("Final entry count: %d", cm.GetEntryCount())

	// The growth should be reasonable for the amount of work done
	// We expect some growth due to the captured data, but not unbounded
	// Allow up to 10MB growth for 1000 entries (10KB per entry is generous)
	maxGrowth := int64(10 * 1024 * 1024) // 10MB
	if int64(heapGrowth) > maxGrowth {
		t.Errorf("Heap growth %d bytes exceeds maximum %d bytes, possible memory leak",
			heapGrowth, maxGrowth)
	}

	// Verify all entries were captured
	if cm.GetEntryCount() != iterations {
		t.Errorf("Expected %d entries, got %d", iterations, cm.GetEntryCount())
	}

	// Verify corpus can be saved and loaded
	err := cm.Save()
	if err != nil {
		t.Errorf("Failed to save after memory test: %v", err)
	}

	// Clear the middleware entries
	cm.mu.Lock()
	cm.entries = nil
	cm.mu.Unlock()

	// Load into new instance
	cm2 := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	if err := cm2.Load(); err != nil {
		t.Errorf("Failed to load after memory test: %v", err)
	}

	// Verify loaded correctly
	if cm2.GetEntryCount() != iterations {
		t.Errorf("Loaded count %d doesn't match saved count %d",
			cm2.GetEntryCount(), iterations)
	}
}

// TestCaptureDoesNotModifyRequestHeaders verifies that capture doesn't
// modify request headers before they reach the backend
func TestCaptureDoesNotModifyRequestHeaders(t *testing.T) {
	tmpDir := t.TempDir()
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	cm.Enable()

	// Handler that checks for expected headers
	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify all expected headers are present and correct
		expectedHeaders := map[string]string{
			"X-Custom-1":    "value1",
			"X-Custom-2":    "value2",
			"Authorization": "Bearer token123",
		}

		for key, expectedValue := range expectedHeaders {
			actualValue := r.Header.Get(key)
			if actualValue != expectedValue {
				t.Errorf("Header %s: expected %s, got %s", key, expectedValue, actualValue)
			}
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(backendHandler)

	// Create request with custom headers
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-Custom-1", "value1")
	req.Header.Set("X-Custom-2", "value2")
	req.Header.Set("Authorization", "Bearer token123")

	w := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Request failed: %d", w.Code)
	}

	// Verify entry was captured
	if cm.GetEntryCount() != 1 {
		t.Errorf("Expected 1 entry, got %d", cm.GetEntryCount())
	}
}

// TestCaptureDoesNotModifyResponseBody verifies that capture doesn't
// corrupt the response body
func TestCaptureDoesNotModifyResponseBody(t *testing.T) {
	tmpDir := t.TempDir()
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	cm.Enable()

	// Create test data
	testData := make([]byte, 1024*1024) // 1MB
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(testData)
	})

	wrappedHandler := cm.Wrap(backendHandler)

	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	// Read response body
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Errorf("Failed to read response body: %v", err)
	}

	// Verify response body wasn't modified
	if len(responseBody) != len(testData) {
		t.Errorf("Response body length mismatch: expected %d, got %d",
			len(testData), len(responseBody))
	}

	for i := range testData {
		if i < len(responseBody) && responseBody[i] != testData[i] {
			t.Errorf("Response body corrupted at byte %d: expected %d, got %d",
				i, testData[i], responseBody[i])
			break
		}
	}

	// Verify entry was captured
	if cm.GetEntryCount() != 1 {
		t.Errorf("Expected 1 entry, got %d", cm.GetEntryCount())
	}
}

// TestCaptureWithStreamingResponse verifies that capture works correctly
// with streaming responses
func TestCaptureWithStreamingResponse(t *testing.T) {
	tmpDir := t.TempDir()
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	cm.Enable()

	chunks := []string{"chunk1", "chunk2", "chunk3"}

	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter should implement Flusher")
		}

		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)

		for _, chunk := range chunks {
			w.Write([]byte(chunk))
			if flusher != nil {
				flusher.Flush()
			}
		}
	})

	wrappedHandler := cm.Wrap(backendHandler)

	req := httptest.NewRequest("GET", "/api/stream", nil)
	w := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// Verify response body is correct
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Errorf("Failed to read response body: %v", err)
	}

	expectedBody := strings.Join(chunks, "")
	if string(responseBody) != expectedBody {
		t.Errorf("Expected body %s, got %s", expectedBody, responseBody)
	}

	// Verify entry was captured
	if cm.GetEntryCount() != 1 {
		t.Errorf("Expected 1 entry, got %d", cm.GetEntryCount())
	}
}

// TestCaptureStatusDoesNotBlock verifies that checking capture status
// doesn't interfere with ongoing capture operations
func TestCaptureStatusDoesNotBlock(t *testing.T) {
	tmpDir := t.TempDir()
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	cm.Enable()

	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(backendHandler)

	// Launch many requests
	numRequests := 100
	var wg sync.WaitGroup
	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/api/test", nil)
			w := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(w, req)

			// Intersperse status checks
			if i%10 == 0 {
				count := cm.GetEntryCount()
				enabled := cm.IsEnabled()
				_ = count
				_ = enabled
			}
		}()
	}

	wg.Wait()

	// Verify all requests completed successfully
	if cm.GetEntryCount() != numRequests {
		t.Errorf("Expected %d entries, got %d", numRequests, cm.GetEntryCount())
	}
}
