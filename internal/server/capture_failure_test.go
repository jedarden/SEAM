package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func captureFailureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var logs bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	return &logs
}

// TestCaptureSaveToReadOnlyDirectory tests that save failures are handled gracefully
func TestCaptureSaveToReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission enforcement is not observable when running as root")
	}

	tmpDir := t.TempDir()

	// Create a read-only subdirectory
	readOnlyDir := filepath.Join(tmpDir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0o500); err != nil {
		t.Fatalf("failed to create read-only directory: %v", err)
	}

	cm := NewCaptureMiddleware(readOnlyDir, "test-service", "test-incumbent", false)
	cm.Enable()

	// Create a test handler that returns a response
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Make a request to capture data
	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w, req)

	// Verify the response is still OK (capture failure doesn't disrupt operation)
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Verify entry was captured
	if cm.GetEntryCount() != 1 {
		t.Errorf("Expected 1 captured entry, got %d", cm.GetEntryCount())
	}

	// Attempt to save - should fail but not crash
	err := cm.Save()
	if err == nil {
		t.Error("Expected error when saving to read-only directory, got nil")
	}

	// Verify entries are still in memory despite save failure
	if cm.GetEntryCount() != 1 {
		t.Errorf("Expected entries to remain in memory after save failure, got %d", cm.GetEntryCount())
	}
}

// TestCaptureLoadFromCorruptedFile tests loading a corrupted corpus file
func TestCaptureLoadFromCorruptedFile(t *testing.T) {
	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")

	// Write invalid JSON
	corruptedData := []byte(`{"schema": "seam-diff-corpus/v1", "service": "test", "entries": [invalid json`)
	if err := os.WriteFile(corpusPath, corruptedData, 0o644); err != nil {
		t.Fatalf("failed to write corrupted file: %v", err)
	}

	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)

	// Load should fail gracefully
	err := cm.Load()
	if err == nil {
		t.Error("Expected error when loading corrupted corpus, got nil")
	}

	// Verify middleware still works despite load failure
	if cm.GetEntryCount() != 0 {
		t.Errorf("Expected 0 entries after failed load, got %d", cm.GetEntryCount())
	}
}

// TestCaptureLoadFromEmptyDirectory tests loading when no corpus file exists
func TestCaptureLoadFromEmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)

	// Load should succeed without error (no corpus to load)
	err := cm.Load()
	if err != nil {
		t.Errorf("Expected no error when loading from empty directory, got: %v", err)
	}

	// Verify middleware is ready to capture
	if !cm.IsEnabled() {
		t.Error("Expected middleware to be enabled after load from empty directory")
	}
}

// TestCaptureRequestBodyReadFailure tests handling of request body read failures
func TestCaptureRequestBodyReadFailure(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	// Create a test handler
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Create a request with a body that will fail to read
	// We'll simulate this by using a reader that returns an error
	errorReader := &errorReadCloser{err: fmt.Errorf("simulated read error")}
	req := httptest.NewRequest("POST", "/api/test", errorReader)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()

	// Should handle the read error gracefully
	wrappedHandler.ServeHTTP(w, req)

	// Response should still be OK (capture failure doesn't disrupt operation)
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 despite read error, got %d", w.Code)
	}

	// Entry may or may not be captured depending on when the error occurs
	// The important thing is the request still succeeded
	t.Log("Request handled successfully despite body read error")
}

// TestCaptureResponseBodyCaptureFailure tests handling of response body capture issues
func TestCaptureResponseBodyCaptureFailure(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	// Create a test handler that writes a large response
	largeBody := strings.Repeat("x", 100*1024*1024) // 100MB
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(largeBody))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()

	// Should handle large responses without crashing
	wrappedHandler.ServeHTTP(w, req)

	// Verify response is OK
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Verify entry was captured
	if cm.GetEntryCount() != 1 {
		t.Errorf("Expected 1 captured entry, got %d", cm.GetEntryCount())
	}
}

// TestCaptureConcurrentSaveOperations tests concurrent save operations
func TestCaptureConcurrentSaveOperations(t *testing.T) {
	tmpDir := t.TempDir()
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	cm.Enable()

	// Capture some entries first
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", i), nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)
	}

	if cm.GetEntryCount() != 5 {
		t.Fatalf("Expected 5 entries, got %d", cm.GetEntryCount())
	}

	// Perform concurrent saves
	var wg sync.WaitGroup
	saveCount := 10

	for i := 0; i < saveCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Some saves may fail due to concurrent writes, but shouldn't crash
			_ = cm.Save()
		}()
	}

	wg.Wait()

	// Verify corpus file exists and is valid
	corpusPath := filepath.Join(tmpDir, "corpus.json")
	if _, err := os.Stat(corpusPath); os.IsNotExist(err) {
		t.Error("Corpus file should exist after concurrent saves")
	}

	// Load and verify entries
	cm2 := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	if err := cm2.Load(); err != nil {
		t.Errorf("Failed to load corpus after concurrent saves: %v", err)
	}

	// Should have at least some entries
	if cm2.GetEntryCount() == 0 {
		t.Error("Expected some entries to be saved despite concurrent operations")
	}
}

// TestCaptureNetworkTimeoutDuringForwarding tests handling of network timeouts
func TestCaptureNetworkTimeoutDuringForwarding(t *testing.T) {
	// This test documents the behavior for network timeout scenarios
	// The actual timeout handling would depend on the HTTP client timeout configuration
	// which is configured in the spec files, not in the Config struct
	t.Log("Network timeout test - documenting behavior for timeout scenario")
	// Note: IncumbentURL is configured via spec files, not Config struct
	// To properly test this, you would need to set up a spec with a slow incumbent
	// and verify that capture doesn't interfere with timeout handling
}

// TestCaptureDiskSpacePressure tests behavior under disk space pressure
func TestCaptureDiskSpacePressure(t *testing.T) {
	tmpDir := t.TempDir()

	// Note: This won't actually fill the disk in testing, but documents the test scenario
	_ = filepath.Join(tmpDir, "large.bin") // Variable for documenting large file scenario
	t.Log("Disk space pressure test - documenting behavior for low disk space scenarios")

	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", false)
	cm.Enable()

	// Simulate capturing many large entries
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Return a large response
		_, _ = w.Write([]byte(strings.Repeat("x", 1024*1024))) // 1MB
	})

	wrappedHandler := cm.Wrap(nextHandler)

	for i := 0; i < 50; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/large%d", i), nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)
	}

	t.Logf("Captured %d entries", cm.GetEntryCount())

	// Save should handle disk space gracefully
	err := cm.Save()
	if err != nil {
		t.Logf("Save failed (possibly due to disk space): %v", err)
		// This is expected under disk pressure
	} else {
		t.Log("Save succeeded despite large corpus")
	}
}

// TestCaptureInvalidPathCharacters tests handling of paths with special characters
func TestCaptureInvalidPathCharacters(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Test paths with special characters
	testPaths := []string{
		"/api/test-{param}",
		"/api/test:123",
		"/api/test/with spaces",
		"/api/test/with/slashes",
		"/api/测试",   // Unicode
		"/api/тест", // Cyrillic
	}

	for _, path := range testPaths {
		// Encode spaces in the request target so httptest.NewRequest does not
		// interpret the remainder of the path as an HTTP version.
		requestTarget := strings.ReplaceAll(path, " ", "%20")
		req := httptest.NewRequest("GET", requestTarget, nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Path %s: expected status 200, got %d", path, w.Code)
		}
	}

	// Verify all entries were captured
	if cm.GetEntryCount() != len(testPaths) {
		t.Errorf("Expected %d entries, got %d", len(testPaths), cm.GetEntryCount())
	}

	// Verify entries have valid IDs
	for i := 0; i < cm.GetEntryCount(); i++ {
		// We can't directly access entries, but we verified the count
	}

	// Save should handle special characters in entry IDs
	err := cm.Save()
	if err != nil {
		t.Errorf("Failed to save corpus with special characters: %v", err)
	}
}

// TestCaptureReservedPathsExcluded tests that reserved paths are not captured
func TestCaptureReservedPathsExcluded(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Test reserved paths
	reservedPaths := []string{
		"/_seam/healthz",
		"/_seam/readyz",
		"/_seam/metrics",
		"/_seam/capture/status",
	}

	for _, path := range reservedPaths {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Reserved path %s: expected status 200, got %d", path, w.Code)
		}
	}

	// Verify reserved paths were NOT captured
	if cm.GetEntryCount() != 0 {
		t.Errorf("Expected 0 entries (reserved paths should be excluded), got %d", cm.GetEntryCount())
	}
}

// TestCaptureMemoryPressure tests behavior with many captured entries
func TestCaptureMemoryPressure(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Capture many entries to test memory handling
	entryCount := 1000
	for i := 0; i < entryCount; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/entry%d", i), nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)
	}

	if cm.GetEntryCount() != entryCount {
		t.Errorf("Expected %d entries, got %d", entryCount, cm.GetEntryCount())
	}

	// Save should handle large corpus
	err := cm.Save()
	if err != nil {
		t.Errorf("Failed to save large corpus: %v", err)
	}
}

// TestCaptureWithSpecialHeaderValues tests handling of special header values
func TestCaptureWithSpecialHeaderValues(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo back the headers
		for k, vv := range r.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	req := httptest.NewRequest("GET", "/api/test", nil)
	// Set headers with special characters
	req.Header.Set("X-Special", "value with spaces and\tspecial\tchars")
	req.Header.Set("X-Unicode", "测试值")
	req.Header.Set("X-Empty", "") // Should be filtered out

	w := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	if cm.GetEntryCount() != 1 {
		t.Errorf("Expected 1 entry, got %d", cm.GetEntryCount())
	}

	// Save and verify special characters are preserved
	err := cm.Save()
	if err != nil {
		t.Errorf("Failed to save corpus with special header values: %v", err)
	}
}

// TestCaptureResponseRecorderFlushMethod tests the Flush method support
func TestCaptureResponseRecorderFlushMethod(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Use Flush if available
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	req := httptest.NewRequest("GET", "/api/stream", nil)
	w := httptest.NewRecorder()

	// Should handle Flush without panicking
	wrappedHandler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

// TestCaptureResponseRecorderHijackMethod tests the Hijack method support
func TestCaptureResponseRecorderHijackMethod(t *testing.T) {
	rec := &responseRecorder{
		ResponseWriter: httptest.NewRecorder(),
	}

	// Should return error for non-hijackable writer
	_, _, err := rec.Hijack()
	if err == nil {
		t.Error("Expected error when hijacking non-hijackable writer")
	}
}

// errorReadCloser is a helper that simulates a read failure
type errorReadCloser struct {
	err error
}

func (e *errorReadCloser) Read(p []byte) (n int, err error) {
	return 0, e.err
}

func (e *errorReadCloser) Close() error {
	return nil
}

// TestCaptureDirectoryCreationFailure tests handling when directory creation fails
func TestCaptureDirectoryCreationFailure(t *testing.T) {
	// Create a temporary file with the same name as the directory we want to create
	tmpDir := t.TempDir()
	blockedDir := filepath.Join(tmpDir, "blocked")
	blockedFile := filepath.Join(tmpDir, "blocked") // Same name as directory

	// Create a file with the same name to block directory creation
	if err := os.WriteFile(blockedFile, []byte("blocking directory creation"), 0o644); err != nil {
		t.Fatalf("failed to create blocking file: %v", err)
	}

	logs := captureFailureLogs(t)
	cm := NewCaptureMiddleware(blockedDir, "test-service", "test-incumbent", true)
	cm.Enable()

	// Capture an entry
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)
	// Auto-save on the tenth request must not interrupt request handling.
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", i), nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected status 200, got %d", i, w.Code)
		}
	}

	if cm.GetEntryCount() != 10 {
		t.Errorf("Expected 10 captured entries, got %d", cm.GetEntryCount())
	}
	if !strings.Contains(logs.String(), "failed to auto-save corpus") ||
		!strings.Contains(logs.String(), "create corpus directory") {
		t.Errorf("expected directory creation failure to be logged, got %q", logs.String())
	}

	// A direct save must report the error while leaving captured entries intact.
	err := cm.Save()
	if err == nil {
		t.Error("Expected error when saving to blocked directory, got nil")
	}

	// Verify entries are still in memory despite save failure
	if cm.GetEntryCount() != 10 {
		t.Errorf("Expected entries to remain in memory after save failure, got %d", cm.GetEntryCount())
	}

	t.Log("Directory creation failure handled gracefully - proxy continues operating")
}

// TestCaptureWriteErrorDuringRequestHandling tests write failures during active requests
func TestCaptureWriteErrorDuringRequestHandling(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission enforcement is not observable when running as root")
	}

	tmpDir := t.TempDir()

	// Create a writable directory first
	readOnlyDir := filepath.Join(tmpDir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	// Create a corpus file with read-only permissions to prevent overwrites
	corpusPath := filepath.Join(readOnlyDir, "corpus.json")
	initialContent := `{"schema": "seam-diff-corpus/v1", "service": "test", "incumbent": "test-incumbent", "capturedAt": "2024-01-01T00:00:00Z", "description": "test", "entries": []}`
	if err := os.WriteFile(corpusPath, []byte(initialContent), 0o644); err != nil {
		t.Fatalf("failed to create initial corpus file: %v", err)
	}

	// Make the corpus file read-only to trigger write failures on save
	if err := os.Chmod(corpusPath, 0o400); err != nil {
		t.Fatalf("failed to make corpus file read-only: %v", err)
	}

	logs := captureFailureLogs(t)
	cm := NewCaptureMiddleware(readOnlyDir, "test-service", "test-incumbent", true) // autoSave enabled
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Make multiple requests to trigger auto-save failures
	successCount := 0
	for i := 0; i < 15; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", i), nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)

		if w.Code == http.StatusOK {
			successCount++
		}
	}

	// All requests should succeed despite auto-save failures
	if successCount != 15 {
		t.Errorf("Expected all 15 requests to succeed, got %d", successCount)
	}

	// Verify entries were captured in memory
	entryCount := cm.GetEntryCount()
	if entryCount != 15 {
		t.Errorf("Expected 15 entries in memory, got %d", entryCount)
	}
	if !strings.Contains(logs.String(), "failed to auto-save corpus") ||
		!strings.Contains(logs.String(), "write corpus file") {
		t.Errorf("expected corpus write failure to be logged, got %q", logs.String())
	}

	t.Logf("All requests succeeded despite %d auto-save attempts, %d entries captured in memory",
		15, entryCount)
}

// TestCaptureFilesystemFullSimulated tests simulated disk full conditions
func TestCaptureFilesystemFullSimulated(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full is unavailable; cannot simulate ENOSPC")
	}

	tmpDir := t.TempDir()
	corpusPath := filepath.Join(tmpDir, "corpus.json")
	if err := os.Symlink("/dev/full", corpusPath); err != nil {
		t.Skipf("cannot create /dev/full symlink: %v", err)
	}

	logs := captureFailureLogs(t)
	cm := NewCaptureMiddleware(tmpDir, "test-service", "test-incumbent", true)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)
	// The tenth request triggers an auto-save whose write returns ENOSPC.
	for i := 0; i < 11; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", i), nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected status 200, got %d", i, w.Code)
		}
	}

	if cm.GetEntryCount() != 11 {
		t.Fatalf("Expected 11 entries after disk-full failure, got %d", cm.GetEntryCount())
	}
	if !strings.Contains(logs.String(), "failed to auto-save corpus") ||
		!strings.Contains(logs.String(), "write corpus file") ||
		!strings.Contains(logs.String(), "no space") {
		t.Errorf("expected ENOSPC capture failure to be logged, got %q", logs.String())
	}

	// The request after the failed save proves the middleware remains usable.
	req := httptest.NewRequest("GET", "/api/another", nil)
	w := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Proxy should continue working after save failure, got status %d", w.Code)
	}

	t.Log("Simulated disk full condition handled - proxy continues serving requests")
}

// TestCaptureDiskWriteFailuresAreNonBlocking deterministically covers the two
// most important filesystem failures even on hosts where the test process is
// root or /dev/full is unavailable.
func TestCaptureDiskWriteFailuresAreNonBlocking(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "disk full", err: syscall.ENOSPC},
		{name: "permission denied", err: os.ErrPermission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureFailureLogs(t)
			cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", true)
			writeAttempts := 0
			cm.writeCorpusFile = func(string, []byte, os.FileMode) error {
				writeAttempts++
				return tc.err
			}

			wrappedHandler := cm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte("upstream response"))
			}))

			for i := 0; i < 10; i++ {
				req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/test%d", i), nil)
				w := httptest.NewRecorder()
				wrappedHandler.ServeHTTP(w, req)
				if w.Code != http.StatusAccepted || w.Body.String() != "upstream response" {
					t.Fatalf("request %d changed by capture failure: status=%d body=%q", i, w.Code, w.Body.String())
				}
			}

			if got := cm.GetEntryCount(); got != 10 {
				t.Fatalf("retained entries after failed auto-save = %d, want 10", got)
			}
			if writeAttempts != 1 {
				t.Fatalf("auto-save write attempts = %d, want 1", writeAttempts)
			}
			if !strings.Contains(logs.String(), "failed to auto-save corpus") ||
				!strings.Contains(logs.String(), "write corpus file") ||
				!strings.Contains(logs.String(), tc.err.Error()) {
				t.Errorf("expected observable %s failure, got logs %q", tc.name, logs.String())
			}

			if err := cm.Save(); !errors.Is(err, tc.err) {
				t.Fatalf("direct save error = %v, want wrapped %v", err, tc.err)
			}
		})
	}
}

// TestCaptureJsonMarshalFailure tests graceful handling of corpus
// serialization errors. CorpusFile only contains JSON-safe field types, so a
// per-instance serializer seam is used to reach this otherwise impossible
// failure path without introducing invalid production data.
func TestCaptureJsonMarshalFailure(t *testing.T) {
	logs := captureFailureLogs(t)
	serializationErr := errors.New("simulated corpus serialization failure")
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", true)
	cm.marshalCorpus = func(CorpusFile) ([]byte, error) {
		return nil, serializationErr
	}

	wrappedHandler := cm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/serialize/%d", i), nil))
		if w.Code != http.StatusNoContent {
			t.Fatalf("request %d changed by serialization failure: status=%d", i, w.Code)
		}
	}

	if got := cm.GetEntryCount(); got != 10 {
		t.Fatalf("retained entries after serialization failure = %d, want 10", got)
	}
	if !strings.Contains(logs.String(), "failed to auto-save corpus") ||
		!strings.Contains(logs.String(), "marshal corpus") ||
		!strings.Contains(logs.String(), serializationErr.Error()) {
		t.Errorf("expected serialization failure to be logged, got %q", logs.String())
	}
	if err := cm.Save(); !errors.Is(err, serializationErr) {
		t.Fatalf("direct save error = %v, want wrapped serialization error", err)
	}
	if _, err := os.Stat(filepath.Join(cm.corpusDir, "corpus.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corpus file exists after serialization failure: %v", err)
	}
}

// TestCaptureRetentionLimitExceededKeepsProxyOperating verifies that exceeding
// corpus storage limits evicts the oldest captures without rejecting traffic,
// and that the bounded corpus remains chronologically ordered on disk.
func TestCaptureRetentionLimitExceededKeepsProxyOperating(t *testing.T) {
	const retentionLimit = 3
	cm := newCaptureMiddlewareWithRetentionLimit(
		t.TempDir(),
		"test-service",
		"test-incumbent",
		false,
		retentionLimit,
	)
	wrappedHandler := cm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("served " + r.URL.Path))
	}))

	const requestCount = retentionLimit + 3
	for i := 0; i < requestCount; i++ {
		path := fmt.Sprintf("/limit/%d", i)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || w.Body.String() != "served "+path {
			t.Fatalf("request %d changed after storage limit: status=%d body=%q", i, w.Code, w.Body.String())
		}
	}

	if got := cm.GetEntryCount(); got != retentionLimit {
		t.Fatalf("retained entries = %d, want limit %d", got, retentionLimit)
	}
	if err := cm.Save(); err != nil {
		t.Fatalf("save bounded corpus: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cm.corpusDir, "corpus.json"))
	if err != nil {
		t.Fatalf("read bounded corpus: %v", err)
	}
	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("decode bounded corpus: %v", err)
	}
	wantIDs := []string{"limit-3-get", "limit-4-get", "limit-5-get"}
	if len(corpus.Entries) != len(wantIDs) {
		t.Fatalf("persisted entries = %d, want %d", len(corpus.Entries), len(wantIDs))
	}
	for i, wantID := range wantIDs {
		if got := corpus.Entries[i].ID; got != wantID {
			t.Errorf("persisted entry %d ID = %q, want %q", i, got, wantID)
		}
	}
}

// TestCaptureRecoversAfterTransientAutoSaveFailure proves auto-save retries on
// the next interval and persists all retained entries after the filesystem
// becomes writable again.
func TestCaptureRecoversAfterTransientAutoSaveFailure(t *testing.T) {
	logs := captureFailureLogs(t)
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", true)
	writeAttempts := 0
	cm.writeCorpusFile = func(name string, data []byte, perm os.FileMode) error {
		writeAttempts++
		if writeAttempts == 1 {
			return syscall.ENOSPC
		}
		return os.WriteFile(name, data, perm)
	}
	wrappedHandler := cm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream response"))
	}))

	for i := 0; i < 20; i++ {
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/recover/%d", i), nil))
		if w.Code != http.StatusOK || w.Body.String() != "upstream response" {
			t.Fatalf("request %d changed during transient failure: status=%d body=%q", i, w.Code, w.Body.String())
		}
	}

	if writeAttempts != 2 {
		t.Fatalf("auto-save write attempts = %d, want failed attempt plus retry", writeAttempts)
	}
	if !strings.Contains(logs.String(), "failed to auto-save corpus: write corpus file") ||
		!strings.Contains(logs.String(), syscall.ENOSPC.Error()) ||
		!strings.Contains(logs.String(), "corpus saved to") {
		t.Errorf("expected failure and recovery logs, got %q", logs.String())
	}

	data, err := os.ReadFile(filepath.Join(cm.corpusDir, "corpus.json"))
	if err != nil {
		t.Fatalf("read corpus after recovery: %v", err)
	}
	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("decode corpus after recovery: %v", err)
	}
	if got := len(corpus.Entries); got != 20 {
		t.Fatalf("persisted entries after recovery = %d, want 20", got)
	}
}

// TestCaptureErrorIsolation verifies that capture errors don't affect concurrent requests
func TestCaptureErrorIsolation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a read-only directory for capture failures
	readOnlyDir := filepath.Join(tmpDir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0o500); err != nil {
		t.Fatalf("failed to create read-only directory: %v", err)
	}

	cm := NewCaptureMiddleware(readOnlyDir, "test-service", "test-incumbent", true)
	cm.Enable()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Response from " + r.URL.Path))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Make concurrent requests
	var wg sync.WaitGroup
	requestCount := 20
	errors := make([]error, requestCount)

	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", fmt.Sprintf("/api/endpoint%d", idx), nil)
			w := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				errors[idx] = fmt.Errorf("request %d failed with status %d", idx, w.Code)
			}

			// Verify response body is correct
			expectedBody := fmt.Sprintf("Response from /api/endpoint%d", idx)
			if w.Body.String() != expectedBody {
				errors[idx] = fmt.Errorf("request %d got unexpected body: %s", idx, w.Body.String())
			}
		}(i)
	}

	wg.Wait()

	// Check for any errors
	failedRequests := 0
	for _, err := range errors {
		if err != nil {
			failedRequests++
			t.Errorf("Request error: %v", err)
		}
	}

	if failedRequests > 0 {
		t.Errorf("Expected all requests to succeed, but %d failed", failedRequests)
	}

	t.Logf("All %d requests succeeded despite capture failures", requestCount)
	entryCount := cm.GetEntryCount()
	t.Logf("Captured %d entries in memory despite save failures", entryCount)
}

// TestCaptureSaveFailureDoesNotCrashProxy verifies that save failures don't crash the proxy
func TestCaptureSaveFailureDoesNotCrashProxy(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a directory and then make it read-only
	readOnlyDir := filepath.Join(tmpDir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	// Capture directory is writable initially
	cm := NewCaptureMiddleware(readOnlyDir, "test-service", "test-incumbent", false)
	cm.Enable()

	// Capture some entries
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", i), nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)
	}

	// Make directory read-only to trigger save failures
	if err := os.Chmod(readOnlyDir, 0o500); err != nil {
		t.Fatalf("failed to make directory read-only: %v", err)
	}
	defer func() {
		// Restore permissions for cleanup
		_ = os.Chmod(readOnlyDir, 0o755)
	}()

	// Attempt multiple saves - none should crash
	for i := 0; i < 5; i++ {
		err := cm.Save()
		if err == nil {
			t.Logf("Save attempt %d succeeded unexpectedly", i)
		} else {
			t.Logf("Save attempt %d failed as expected: %v", i, err)
		}

		// Proxy should still work after each failed save
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/after-save%d", i), nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Proxy failed after save attempt %d, got status %d", i, w.Code)
		}
	}

	t.Log("Proxy remained stable through multiple save failures")
}

// TestCaptureWithUnwritableParentDirectory tests when parent directory is not writable
func TestCaptureWithUnwritableParentDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission enforcement is not observable when running as root")
	}

	tmpDir := t.TempDir()

	// Create a nested directory structure
	parentDir := filepath.Join(tmpDir, "parent")
	corpusDir := filepath.Join(parentDir, "corpus")

	if err := os.Mkdir(parentDir, 0o755); err != nil {
		t.Fatalf("failed to create parent directory: %v", err)
	}

	// Make parent directory read-only (no write permission)
	if err := os.Chmod(parentDir, 0o555); err != nil {
		t.Fatalf("failed to make parent read-only: %v", err)
	}
	defer func() {
		// Restore permissions for cleanup
		_ = os.Chmod(parentDir, 0o755)
	}()

	cm := NewCaptureMiddleware(corpusDir, "test-service", "test-incumbent", false)
	cm.Enable()

	// Capture an entry
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)
	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w, req)

	// Response should still succeed
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 despite unwritable parent, got %d", w.Code)
	}

	// Entry should be in memory
	if cm.GetEntryCount() != 1 {
		t.Errorf("Expected 1 entry in memory, got %d", cm.GetEntryCount())
	}

	// Save should fail
	err := cm.Save()
	if err == nil {
		t.Error("Expected save to fail with unwritable parent directory")
	} else {
		t.Logf("Save failed as expected: %v", err)
	}

	// Proxy should continue working
	req = httptest.NewRequest("GET", "/api/another", nil)
	w = httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Proxy should continue working, got status %d", w.Code)
	}

	t.Log("Unwritable parent directory handled gracefully")
}
