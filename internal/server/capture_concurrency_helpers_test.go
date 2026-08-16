package server

import (
	"sync"
	"testing"
)

// captureOperationResult records the outcome of one operation launched by
// runConcurrentCaptureOperations. Results are returned in operation-index
// order, while Index makes failures easy to identify in assertion messages.
type captureOperationResult struct {
	Index int
	Err   error
}

// captureTestBarrier coordinates the start of a group of test goroutines.
// Workers announce that they are ready, then wait on one shared release
// channel. This gives tests a common starting line without using sleeps and
// makes the intended concurrency model explicit: all operations begin only
// after every worker has been scheduled and is waiting at the gate.
type captureTestBarrier struct {
	ready   sync.WaitGroup
	release chan struct{}
	once    sync.Once
}

func newCaptureTestBarrier(participants int) *captureTestBarrier {
	barrier := &captureTestBarrier{
		release: make(chan struct{}),
	}
	barrier.ready.Add(participants)
	return barrier
}

func (b *captureTestBarrier) signalReady() {
	b.ready.Done()
}

func (b *captureTestBarrier) waitUntilReady() {
	b.ready.Wait()
}

func (b *captureTestBarrier) waitForRelease() {
	<-b.release
}

func (b *captureTestBarrier) releaseAll() {
	b.once.Do(func() {
		close(b.release)
	})
}

// runConcurrentCaptureOperations starts operationCount workers and releases
// them together once all workers have reached the barrier. The operation
// receives a stable zero-based worker index and should return an error instead
// of calling testing.T.Fatal from its goroutine. Returning errors keeps test
// failure reporting in the parent goroutine and avoids terminating a worker
// before the shared wait group can be completed.
func runConcurrentCaptureOperations(
	t *testing.T,
	operationCount int,
	operation func(index int) error,
) []captureOperationResult {
	t.Helper()

	if operationCount < 0 {
		t.Fatalf("operation count must not be negative: %d", operationCount)
	}
	if operation == nil {
		t.Fatal("capture operation must not be nil")
	}
	if operationCount == 0 {
		return nil
	}

	barrier := newCaptureTestBarrier(operationCount)
	resultsCh := make(chan captureOperationResult, operationCount)
	var workers sync.WaitGroup
	workers.Add(operationCount)

	for index := 0; index < operationCount; index++ {
		go func(index int) {
			defer workers.Done()

			barrier.signalReady()
			barrier.waitForRelease()
			resultsCh <- captureOperationResult{
				Index: index,
				Err:   operation(index),
			}
		}(index)
	}

	barrier.waitUntilReady()
	barrier.releaseAll()
	workers.Wait()
	close(resultsCh)

	results := make([]captureOperationResult, operationCount)
	seen := make([]bool, operationCount)
	for result := range resultsCh {
		if result.Index < 0 || result.Index >= operationCount {
			t.Fatalf("concurrent capture operation returned invalid index %d", result.Index)
		}
		if seen[result.Index] {
			t.Fatalf("concurrent capture operation index %d returned more than once", result.Index)
		}
		seen[result.Index] = true
		results[result.Index] = result
	}

	return results
}

// assertConcurrentCaptureResults verifies that the runner produced one
// result for every requested operation and that no worker reported an error.
// It is deliberately separate from assertCaptureEntryCount so callers can
// distinguish worker failures from middleware accounting failures.
func assertConcurrentCaptureResults(
	t *testing.T,
	results []captureOperationResult,
	expectedCount int,
) {
	t.Helper()

	if expectedCount < 0 {
		t.Fatalf("expected operation count must not be negative: %d", expectedCount)
	}
	if len(results) != expectedCount {
		t.Errorf("concurrent operation result count: got %d, want %d", len(results), expectedCount)
	}

	for position, result := range results {
		if result.Index != position {
			t.Errorf("result at position %d has worker index %d", position, result.Index)
		}
		if result.Err != nil {
			t.Errorf("concurrent operation %d failed: %v", result.Index, result.Err)
		}
	}
}

// assertCaptureEntryCount verifies the number of entries recorded by a
// CaptureMiddleware after a concurrent operation batch has completed.
func assertCaptureEntryCount(t *testing.T, middleware *CaptureMiddleware, expectedCount int) {
	t.Helper()

	if middleware == nil {
		t.Fatal("capture middleware must not be nil")
	}
	if expectedCount < 0 {
		t.Fatalf("expected capture entry count must not be negative: %d", expectedCount)
	}

	if got := middleware.GetEntryCount(); got != expectedCount {
		t.Errorf("captured entry count: got %d, want %d", got, expectedCount)
	}
}
