package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestConcurrentCaptureStressWithRaceDetector intentionally combines capture,
// rapid mode changes, entry-count reads, and saves. Run it with
// `go test -race ./internal/server -run TestConcurrentCaptureStressWithRaceDetector`
// to detect unsynchronized access to middleware state.
func TestConcurrentCaptureStressWithRaceDetector(t *testing.T) {
	const (
		workers    = 16
		iterations = 100
	)

	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	handler := cm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(r.URL.Path))
	}))

	start := make(chan struct{})
	results := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func(worker int) {
			defer group.Done()
			<-start

			for iteration := 0; iteration < iterations; iteration++ {
				// Every worker changes the mode around requests so requests
				// regularly cross state transitions rather than only racing with
				// one long-lived toggle operation.
				switch (worker + iteration) % 4 {
				case 0:
					cm.Enable()
				case 1:
					cm.Disable()
				case 2:
					cm.Disable()
					cm.Enable()
				default:
					cm.Enable()
				}

				request := httptest.NewRequest(
					http.MethodGet,
					fmt.Sprintf("/stress/%d/%d", worker, iteration),
					nil,
				)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusOK {
					results <- fmt.Errorf("worker %d iteration %d returned status %d", worker, iteration, response.Code)
					return
				}

				// Save while other workers are appending and toggling. This also
				// verifies that the lock is released after file I/O completes.
				if iteration%10 == 0 {
					if err := cm.Save(); err != nil {
						results <- fmt.Errorf("worker %d iteration %d save failed: %w", worker, iteration, err)
						return
					}
				}
				_ = cm.GetEntryCount()
			}

			results <- nil
		}(worker)
	}
	close(start)
	waitForCaptureRaceWorkers(t, &group, 20*time.Second)

	for worker := 0; worker < workers; worker++ {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}

	// Finish with a deterministic enabled batch. It proves that all worker
	// locks were released and that capture remains usable after the stress.
	cm.Enable()
	before := cm.GetEntryCount()
	var finalGroup sync.WaitGroup
	finalGroup.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func(worker int) {
			defer finalGroup.Done()
			request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/final/%d", worker), nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Errorf("final worker %d returned status %d, want %d", worker, response.Code, http.StatusOK)
			}
		}(worker)
	}
	waitForCaptureRaceWorkers(t, &finalGroup, 5*time.Second)

	if got, want := cm.GetEntryCount()-before, workers; got != want {
		t.Fatalf("final enabled batch captured %d entries, want %d", got, want)
	}
	if err := cm.Save(); err != nil {
		t.Fatalf("save after stress failed: %v", err)
	}
}

// TestCaptureRapidTogglesDoNotDeadlock runs more than ten togglers alongside
// capture and save operations. The bounded wait is the assertion: a missed
// unlock or lock-order deadlock must fail the test instead of hanging the
// package indefinitely.
func TestCaptureRapidTogglesDoNotDeadlock(t *testing.T) {
	const (
		togglers       = 12
		captureWorkers = 12
		toggleRounds   = 300
		captureRounds  = 80
	)

	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	handler := cm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	participants := togglers + captureWorkers
	start := make(chan struct{})
	results := make(chan error, participants)
	var group sync.WaitGroup
	group.Add(participants)

	for worker := 0; worker < togglers; worker++ {
		go func(worker int) {
			defer group.Done()
			<-start
			for round := 0; round < toggleRounds; round++ {
				if (worker+round)%2 == 0 {
					cm.Enable()
				} else {
					cm.Disable()
				}
				_ = cm.IsEnabled()
			}
			results <- nil
		}(worker)
	}

	for worker := 0; worker < captureWorkers; worker++ {
		go func(worker int) {
			defer group.Done()
			<-start
			for round := 0; round < captureRounds; round++ {
				request := httptest.NewRequest(
					http.MethodGet,
					fmt.Sprintf("/toggle/%d/%d", worker, round),
					nil,
				)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusNoContent {
					results <- fmt.Errorf("capture worker %d round %d returned status %d", worker, round, response.Code)
					return
				}
				if round%20 == 0 {
					if err := cm.Save(); err != nil {
						results <- fmt.Errorf("capture worker %d round %d save failed: %w", worker, round, err)
						return
					}
				}
			}
			results <- nil
		}(worker)
	}

	close(start)
	waitForCaptureRaceWorkers(t, &group, 20*time.Second)
	for participant := 0; participant < participants; participant++ {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}

	// Exercise every public lock-protected operation after all concurrent
	// work. Any lock left held by a worker would time out in the bounded wait
	// above or here rather than silently passing.
	cm.Enable()
	if !cm.IsEnabled() {
		t.Fatal("capture should be enabled after the final toggle")
	}
	if err := cm.Save(); err != nil {
		t.Fatalf("final save failed: %v", err)
	}
	if cm.GetEntryCount() < 0 {
		t.Fatal("capture entry count must not be negative")
	}
}

func waitForCaptureRaceWorkers(t *testing.T, group *sync.WaitGroup, timeout time.Duration) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Fatalf("concurrent capture operations did not complete within %s", timeout)
	}
}
