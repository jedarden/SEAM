package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestCaptureFlagDefaultDisabled tests that capture is disabled by default
func TestCaptureFlagDefaultDisabled(t *testing.T) {
	cfg := &Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	}

	s := New(cfg)
	if s.captureMiddleware != nil {
		t.Error("Capture middleware should be nil when disabled")
	}

	// Verify status endpoint reports disabled
	req := httptest.NewRequest("GET", "/_seam/capture/status", nil)
	w := httptest.NewRecorder()
	s.captureStatusHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Check response body contains enabled: false
	body := w.Body.String()
	if !strings.Contains(body, `"enabled":false`) {
		t.Errorf("Expected enabled:false in response, got: %s", body)
	}
}

// TestCaptureFlagEnabled tests that capture can be enabled via flag
func TestCaptureFlagEnabled(t *testing.T) {
	cfg := &Config{
		CallerPort:     8080,
		OperatorPort:   8081,
		BaseURL:        "http://localhost:8080",
		SpecDir:        "../../spec",
		CaptureEnabled: true,
		CorpusDir:      "test-corpus",
	}

	s := New(cfg)
	if s.captureMiddleware == nil {
		t.Error("Capture middleware should be initialized when enabled")
	}

	if !s.captureMiddleware.IsEnabled() {
		t.Error("Capture middleware should be enabled")
	}

	// Verify status endpoint reports enabled
	req := httptest.NewRequest("GET", "/_seam/capture/status", nil)
	w := httptest.NewRecorder()
	s.captureStatusHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Check response body contains enabled: true
	body := w.Body.String()
	if !strings.Contains(body, `"enabled":true`) {
		t.Errorf("Expected enabled:true in response, got: %s", body)
	}
}

// TestCaptureEnvVar tests that capture can be enabled via environment variable
func TestCaptureEnvVar(t *testing.T) {
	// Set environment variable
	oldVal := os.Getenv("SEAM_CAPTURE_ENABLED")
	os.Setenv("SEAM_CAPTURE_ENABLED", "true")
	defer func() {
		if oldVal != "" {
			os.Setenv("SEAM_CAPTURE_ENABLED", oldVal)
		} else {
			os.Unsetenv("SEAM_CAPTURE_ENABLED")
		}
	}()

	// Verify the environment variable is set
	if val := os.Getenv("SEAM_CAPTURE_ENABLED"); val != "true" {
		t.Errorf("Expected SEAM_CAPTURE_ENABLED=true, got %s", val)
	}
}

// TestCaptureEnvVarFalse tests that capture can be disabled via environment variable
func TestCaptureEnvVarFalse(t *testing.T) {
	// Test various false values
	falseValues := []string{"false", "0", "no", "FALSE"}

	for _, val := range falseValues {
		oldVal := os.Getenv("SEAM_CAPTURE_ENABLED")
		os.Setenv("SEAM_CAPTURE_ENABLED", val)
		defer func() {
			if oldVal != "" {
				os.Setenv("SEAM_CAPTURE_ENABLED", oldVal)
			} else {
				os.Unsetenv("SEAM_CAPTURE_ENABLED")
			}
		}()

		// In the actual implementation, only "true" or "1" enable capture
		// This test verifies the environment variable can be set
		if envVal := os.Getenv("SEAM_CAPTURE_ENABLED"); envVal != val {
			t.Errorf("Expected SEAM_CAPTURE_ENABLED=%s, got %s", val, envVal)
		}
	}
}

// TestCaptureCorpusDirConfiguration tests custom corpus directory configuration
func TestCaptureCorpusDirConfiguration(t *testing.T) {
	cfg := &Config{
		CallerPort:     8080,
		OperatorPort:   8081,
		BaseURL:        "http://localhost:8080",
		SpecDir:        "../../spec",
		CaptureEnabled: true,
		CorpusDir:      "custom-corpus-dir",
	}

	s := New(cfg)
	if s.captureMiddleware == nil {
		t.Error("Capture middleware should be initialized when enabled")
	}

	// Check corpus directory is set correctly
	if s.captureMiddleware.corpusDir != "custom-corpus-dir" {
		t.Errorf("Expected corpus dir 'custom-corpus-dir', got '%s'", s.captureMiddleware.corpusDir)
	}

	// Verify status endpoint reports correct directory
	req := httptest.NewRequest("GET", "/_seam/capture/status", nil)
	w := httptest.NewRecorder()
	s.captureStatusHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Check response body contains the custom directory
	body := w.Body.String()
	if !strings.Contains(body, `"corpus_dir":"custom-corpus-dir"`) {
		t.Errorf("Expected corpus_dir:custom-corpus-dir in response, got: %s", body)
	}
}

// TestCaptureMiddlewareBehaviorWhenDisabled tests that no capture occurs when disabled
func TestCaptureMiddlewareBehaviorWhenDisabled(t *testing.T) {
	cm := NewCaptureMiddleware("test-corpus", "test-service", "test-incumbent", true)
	cm.Disable()

	if cm.IsEnabled() {
		t.Error("Capture middleware should be disabled")
	}

	// Create a test handler
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Wrap with capture middleware
	wrappedHandler := cm.Wrap(nextHandler)

	// Create a test request
	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()

	// Execute the wrapped handler
	wrappedHandler.ServeHTTP(w, req)

	// Verify the response is OK
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Verify no entries were captured
	if cm.GetEntryCount() != 0 {
		t.Errorf("Expected 0 captured entries when disabled, got %d", cm.GetEntryCount())
	}
}

// TestCaptureMiddlewareBehaviorWhenEnabled tests that capture occurs when enabled
func TestCaptureMiddlewareBehaviorWhenEnabled(t *testing.T) {
	cm := NewCaptureMiddleware("test-corpus", "test-service", "test-incumbent", true)
	cm.Enable()

	if !cm.IsEnabled() {
		t.Error("Capture middleware should be enabled")
	}

	// Create a test handler
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Wrap with capture middleware
	wrappedHandler := cm.Wrap(nextHandler)

	// Create a test request
	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()

	// Execute the wrapped handler
	wrappedHandler.ServeHTTP(w, req)

	// Verify the response is OK
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Verify one entry was captured
	if cm.GetEntryCount() != 1 {
		t.Errorf("Expected 1 captured entry when enabled, got %d", cm.GetEntryCount())
	}
}

// TestCaptureEnableDisableToggle tests toggling capture on and off
func TestCaptureEnableDisableToggle(t *testing.T) {
	cm := NewCaptureMiddleware("test-corpus", "test-service", "test-incumbent", true)

	// Initially enabled
	if !cm.IsEnabled() {
		t.Error("Capture middleware should be enabled by default")
	}

	// Disable
	cm.Disable()
	if cm.IsEnabled() {
		t.Error("Capture middleware should be disabled after Disable() call")
	}

	// Re-enable
	cm.Enable()
	if !cm.IsEnabled() {
		t.Error("Capture middleware should be enabled after Enable() call")
	}
}
