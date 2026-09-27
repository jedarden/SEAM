package server

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrentCaptureOperations tests that multiple concurrent requests
// can be captured without blocking or interfering with each other
func TestConcurrentCaptureOperations(t *testing.T) {
	cm := NewCaptureMiddleware("test-corpus", "test-service", "test-incumbent", false)

	// Create a simple handler
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Number of concurrent requests
	numRequests := 100
	var wg sync.WaitGroup
	wg.Add(numRequests)

	// Track successful captures
	var successfulCaptures atomic.Int64

	// Launch concurrent requests
	for i := 0; i < numRequests; i++ {
		go func(id int) {
			defer wg.Done()

			req := httptest.NewRequest("GET", "/api/test", nil)
			w := httptest.NewRecorder()

			wrappedHandler.ServeHTTP(w, req)

			if w.Code == http.StatusOK {
				successfulCaptures.Add(1)
			}
		}(i)
	}

	// Wait for all requests to complete
	wg.Wait()

	// Verify all requests succeeded
	if successfulCaptures.Load() != int64(numRequests) {
		t.Errorf("Expected %d successful requests, got %d", numRequests, successfulCaptures.Load())
	}

	// Verify all entries were captured
	entryCount := cm.GetEntryCount()
	if entryCount != numRequests {
		t.Errorf("Expected %d captured entries, got %d", numRequests, entryCount)
	}
}

// TestConcurrentCaptureWithToggle tests that capture can be toggled
// on/off while active requests are in flight
func TestConcurrentCaptureWithToggle(t *testing.T) {
	cm := NewCaptureMiddleware("test-corpus", "test-service", "test-incumbent", false)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	// Launch a continuous stream of requests
	numRequests := 200
	var wg sync.WaitGroup
	var capturedCount atomic.Int64
	var requestsWhileDisabled atomic.Int64

	// Start request goroutines
	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Add small delay to stagger requests
			time.Sleep(time.Duration(id) * time.Microsecond)

			req := httptest.NewRequest("GET", "/api/test", nil)
			w := httptest.NewRecorder()

			wrappedHandler.ServeHTTP(w, req)

			if w.Code == http.StatusOK {
				if cm.IsEnabled() {
					capturedCount.Add(1)
				} else {
					requestsWhileDisabled.Add(1)
				}
			}
		}(i)
	}

	// Toggle capture on and off while requests are running
	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(10 * time.Microsecond)
			cm.Disable()
			time.Sleep(10 * time.Microsecond)
			cm.Enable()
		}
	}()

	// Wait for all requests to complete
	wg.Wait()

	// Verify no deadlocks occurred
	totalRequests := capturedCount.Load() + requestsWhileDisabled.Load()
	if totalRequests != int64(numRequests) {
		t.Errorf("Expected %d total requests, got %d (captured: %d, while disabled: %d)",
			numRequests, totalRequests, capturedCount.Load(), requestsWhileDisabled.Load())
	}

	// Verify some requests were captured and some were not
	if capturedCount.Load() == 0 {
		t.Error("Expected some requests to be captured while enabled")
	}
	if requestsWhileDisabled.Load() == 0 {
		t.Error("Expected some requests to occur while disabled")
	}
}

// TestConcurrentSaveOperations tests that multiple concurrent save operations
// work correctly without data races
func TestConcurrentSaveOperations(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)

	// Capture some entries first
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(w, req)
	}

	// Launch concurrent save operations
	numSaves := 10
	var wg sync.WaitGroup
	var saveErrors atomic.Int64
	wg.Add(numSaves)

	for i := 0; i < numSaves; i++ {
		go func() {
			defer wg.Done()
			if err := cm.Save(); err != nil {
				saveErrors.Add(1)
			}
		}()
	}

	wg.Wait()

	if saveErrors.Load() != 0 {
		t.Errorf("Expected no save errors, got %d", saveErrors.Load())
	}
}

// TestConcurrentCaptureAndSave tests that capture and save operations
// can run concurrently without interference
func TestConcurrentCaptureAndSave(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(nextHandler)

	var wg sync.WaitGroup
	var captureErrors atomic.Int64
	var saveErrors atomic.Int64

	// Launch concurrent capture operations
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/api/test", nil)
			w := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(w, req)
		}()
	}

	// Launch concurrent save operations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := cm.Save(); err != nil {
				saveErrors.Add(1)
			}
		}()
	}

	wg.Wait()

	if captureErrors.Load() != 0 {
		t.Errorf("Expected no capture errors, got %d", captureErrors.Load())
	}
	if saveErrors.Load() != 0 {
		t.Errorf("Expected no save errors, got %d", saveErrors.Load())
	}
}

// TestToggleStateTransitions tests that enable/disable transitions
// work correctly and the state is consistent
func TestToggleStateTransitions(t *testing.T) {
	cm := NewCaptureMiddleware("test-corpus", "test-service", "test-incumbent", false)

	// Initially enabled
	if !cm.IsEnabled() {
		t.Error("Expected capture to be enabled initially")
	}

	// Disable
	cm.Disable()
	if cm.IsEnabled() {
		t.Error("Expected capture to be disabled after Disable()")
	}

	// Enable
	cm.Enable()
	if !cm.IsEnabled() {
		t.Error("Expected capture to be enabled after Enable()")
	}

	// Multiple disable calls should be idempotent
	cm.Disable()
	cm.Disable()
	if cm.IsEnabled() {
		t.Error("Expected capture to remain disabled")
	}

	// Multiple enable calls should be idempotent
	cm.Enable()
	cm.Enable()
	if !cm.IsEnabled() {
		t.Error("Expected capture to remain enabled")
	}
}

// TestConcurrentToggleStateTransitions tests that concurrent enable/disable
// operations don't cause data races or inconsistent state
func TestConcurrentToggleStateTransitions(t *testing.T) {
	cm := NewCaptureMiddleware("test-corpus", "test-service", "test-incumbent", false)

	var wg sync.WaitGroup

	// Launch concurrent toggle operations
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			cm.Enable()
		}()
		go func() {
			defer wg.Done()
			cm.Disable()
		}()
	}

	wg.Wait()

	// Verify the middleware is still functional regardless of final state
	// (we don't assert the final state because it's nondeterministic)
	_ = cm.IsEnabled() // Just ensure no panic/data race
}

// TestCaptureWithActiveConnections tests that capture can be toggled
// while connections are actively being processed
func TestCaptureWithActiveConnections(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)

	// Create a handler that simulates a slow connection
	slowHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(slowHandler)

	var wg sync.WaitGroup
	var successfulRequests atomic.Int64

	// Start multiple concurrent slow requests
	numRequests := 20
	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/api/test", nil)
			w := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(w, req)
			if w.Code == http.StatusOK {
				successfulRequests.Add(1)
			}
		}()
	}

	// Toggle capture while requests are in flight
	time.Sleep(2 * time.Millisecond)
	cm.Disable()
	time.Sleep(2 * time.Millisecond)
	cm.Enable()

	wg.Wait()

	if successfulRequests.Load() != int64(numRequests) {
		t.Errorf("Expected %d successful requests, got %d", numRequests, successfulRequests.Load())
	}
}

// TestDeadlockPrevention tests that no deadlocks occur with complex
// concurrent patterns involving captures, saves, and toggles
func TestDeadlockPrevention(t *testing.T) {
	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(handler)

	done := make(chan bool, 1)

	// Launch a complex concurrent pattern
	go func() {
		var wg sync.WaitGroup

		// Captures
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := httptest.NewRequest("GET", "/api/test", nil)
				w := httptest.NewRecorder()
				wrappedHandler.ServeHTTP(w, req)
			}()
		}

		// Saves
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cm.Save()
			}()
		}

		// Toggles
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cm.Enable()
				cm.Disable()
				cm.Enable()
			}()
		}

		wg.Wait()
		done <- true
	}()

	// Set a timeout to detect deadlocks
	select {
	case <-done:
		// Success - no deadlock
	case <-time.After(10 * time.Second):
		t.Fatal("Detected potential deadlock - operations did not complete within timeout")
	}
}

// TestConcurrentEntryCountReads tests that GetEntryCount can be called
// concurrently with writes without data races
func TestConcurrentEntryCountReads(t *testing.T) {
	cm := NewCaptureMiddleware("test-corpus", "test-service", "test-incumbent", false)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := cm.Wrap(handler)

	var wg sync.WaitGroup

	// Concurrent captures
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/api/test", nil)
			w := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(w, req)
		}()
	}

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cm.GetEntryCount()
		}()
	}

	wg.Wait()

	// Verify final count
	if cm.GetEntryCount() != 20 {
		t.Errorf("Expected 20 entries, got %d", cm.GetEntryCount())
	}
}
