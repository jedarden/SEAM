package server

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestCaptureToggleStateWithActiveConnections verifies that toggling capture
// while requests are active leaves those requests healthy and changes the
// mode observed by requests that start afterward.
func TestCaptureToggleStateWithActiveConnections(t *testing.T) {
	const operationTimeout = time.Second

	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.Disable()
	if cm.IsEnabled() {
		t.Fatal("capture should start disabled")
	}

	activeDisabledStarted := make(chan struct{})
	activeEnabledStarted := make(chan struct{})
	releaseDisabled := make(chan struct{})
	releaseEnabled := make(chan struct{})
	var releaseDisabledOnce sync.Once
	var releaseEnabledOnce sync.Once
	releaseActiveDisabled := func() {
		releaseDisabledOnce.Do(func() { close(releaseDisabled) })
	}
	releaseActiveEnabled := func() {
		releaseEnabledOnce.Do(func() { close(releaseEnabled) })
	}
	t.Cleanup(func() {
		releaseActiveDisabled()
		releaseActiveEnabled()
	})

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/active-disabled":
			close(activeDisabledStarted)
			<-releaseDisabled
		case "/active-enabled":
			close(activeEnabledStarted)
			<-releaseEnabled
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	handler := cm.Wrap(backend)

	startRequest := func(path string) <-chan int {
		done := make(chan int, 1)
		go func() {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			done <- response.Code
		}()
		return done
	}
	waitForSignal := func(name string, signal <-chan struct{}) {
		t.Helper()
		timer := time.NewTimer(operationTimeout)
		defer timer.Stop()
		select {
		case <-signal:
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", name)
		}
	}
	waitForRequest := func(name string, done <-chan int) {
		t.Helper()
		timer := time.NewTimer(operationTimeout)
		defer timer.Stop()
		select {
		case status := <-done:
			if status != http.StatusOK {
				t.Fatalf("%s returned status %d, want %d", name, status, http.StatusOK)
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", name)
		}
	}
	toggle := func(operation func()) <-chan struct{} {
		done := make(chan struct{})
		go func() {
			operation()
			close(done)
		}()
		return done
	}

	// Start a non-capturing connection, then enable capture while it is
	// blocked in the backend. It must complete without becoming a capture.
	activeDisabled := startRequest("/active-disabled")
	waitForSignal("disabled active request", activeDisabledStarted)
	enableDone := toggle(cm.Enable)
	waitForSignal("capture enable", enableDone)
	if !cm.IsEnabled() {
		t.Fatal("capture should be enabled after the toggle")
	}

	newEnabled := startRequest("/new-enabled")
	waitForRequest("new enabled request", newEnabled)
	if got := cm.GetEntryCount(); got != 1 {
		t.Fatalf("new enabled request captured %d entries, want 1", got)
	}

	releaseActiveDisabled()
	waitForRequest("active disabled request", activeDisabled)
	if got := cm.GetEntryCount(); got != 1 {
		t.Fatalf("disabled active request changed capture count to %d, want 1", got)
	}

	// Start a capturing connection, then disable capture while it is blocked.
	// The in-progress capture must still record a complete request/response.
	activeEnabled := startRequest("/active-enabled")
	waitForSignal("enabled active request", activeEnabledStarted)
	disableDone := toggle(cm.Disable)
	waitForSignal("capture disable", disableDone)
	if cm.IsEnabled() {
		t.Fatal("capture should be disabled after the toggle")
	}

	newDisabled := startRequest("/new-disabled")
	waitForRequest("new disabled request", newDisabled)
	if got := cm.GetEntryCount(); got != 1 {
		t.Fatalf("new disabled request changed capture count to %d, want 1", got)
	}

	releaseActiveEnabled()
	waitForRequest("active enabled request", activeEnabled)
	if got := cm.GetEntryCount(); got != 2 {
		t.Fatalf("active capture did not complete after disable: got %d entries, want 2", got)
	}

	cm.Enable()
	if !cm.IsEnabled() {
		t.Fatal("capture should be enabled after the final state transition")
	}
}
