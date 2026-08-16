package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestConcurrentCaptureReadOperations verifies that independent reads of a
// captured corpus can run together and all observe the same data.
func TestConcurrentCaptureReadOperations(t *testing.T) {
	const (
		capturedEntryCount = 24
		concurrentReads    = 100
	)

	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	handler := cm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(r.URL.Path))
	}))

	for i := 0; i < capturedEntryCount; i++ {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/capture/%d", i), nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("seed capture %d returned status %d, want %d", i, response.Code, http.StatusOK)
		}
	}

	expectedEntries := concurrentReadCaptureEntries(cm)
	if len(expectedEntries) != capturedEntryCount {
		t.Fatalf("seeded capture count: got %d, want %d", len(expectedEntries), capturedEntryCount)
	}

	results := runConcurrentCaptureOperations(t, concurrentReads, func(index int) error {
		entries := concurrentReadCaptureEntries(cm)
		if !reflect.DeepEqual(entries, expectedEntries) {
			return fmt.Errorf("read %d observed captured data different from the seed", index)
		}
		if len(entries) != capturedEntryCount {
			return fmt.Errorf("read %d observed %d entries, want %d", index, len(entries), capturedEntryCount)
		}
		if got := cm.GetEntryCount(); got != capturedEntryCount {
			return fmt.Errorf("read %d observed entry count %d, want %d", index, got, capturedEntryCount)
		}
		return nil
	})

	assertConcurrentCaptureResults(t, results, concurrentReads)
	assertCaptureEntryCount(t, cm, capturedEntryCount)
}

// concurrentReadCaptureEntries returns a stable copy for one concurrent-read
// operation. The test intentionally takes the middleware lock just as the
// production read helper does, so a lock leak or inconsistent shared state
// causes the synchronized operation batch to fail or hang.
func concurrentReadCaptureEntries(cm *CaptureMiddleware) []CorpusEntry {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	return append([]CorpusEntry(nil), cm.entries...)
}
