package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const (
	captureMemoryTestRetention = 64
	captureMemoryTestRequests  = 1200
)

type captureMemoryProfile struct {
	heapAlloc   uint64
	heapObjects uint64
	totalAlloc  uint64
	mallocs     uint64
}

func readCaptureMemoryProfile() captureMemoryProfile {
	runtime.GC()
	runtime.GC()

	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return captureMemoryProfile{
		heapAlloc:   stats.HeapAlloc,
		heapObjects: stats.HeapObjects,
		totalAlloc:  stats.TotalAlloc,
		mallocs:     stats.Mallocs,
	}
}

func signedCaptureMemoryDelta(after, before uint64) int64 {
	if after >= before {
		return int64(after - before)
	}
	return -int64(before - after)
}

func muteCaptureMemoryTestLogs(t *testing.T) {
	t.Helper()

	previous := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(previous) })
}

func newCaptureMemoryTestHandler(
	t *testing.T,
	retentionLimit int,
	responsePayload []byte,
) (*CaptureMiddleware, http.Handler) {
	t.Helper()

	cm := newCaptureMiddlewareWithRetentionLimit(
		t.TempDir(),
		"memory-profile-test",
		"test-incumbent",
		false,
		retentionLimit,
	)
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(responsePayload)
	})
	return cm, cm.Wrap(backend)
}

func runCaptureMemoryRequests(
	t *testing.T,
	handler http.Handler,
	requestPayload []byte,
	start, count int,
) {
	t.Helper()

	for sequence := start; sequence < start+count; sequence++ {
		request := httptest.NewRequest(
			http.MethodPost,
			fmt.Sprintf("/memory-profile/%06d", sequence),
			bytes.NewReader(requestPayload),
		)
		request.Header.Set("Content-Type", "application/octet-stream")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("capture request %d returned status %d", sequence, response.Code)
		}
	}
}

// TestCaptureAllocationProfile tracks the allocations made by the capture
// path relative to the same handler while capture is disabled. The retained
// ring is deliberately small so repeated runs measure steady-state capture
// allocations instead of growing the corpus under test.
func TestCaptureAllocationProfile(t *testing.T) {
	muteCaptureMemoryTestLogs(t)

	requestPayload := bytes.Repeat([]byte("r"), 256)
	responsePayload := bytes.Repeat([]byte("s"), 512)
	disabledCapture, disabledHandler := newCaptureMemoryTestHandler(t, 8, responsePayload)
	disabledCapture.Disable()
	enabledCapture, enabledHandler := newCaptureMemoryTestHandler(t, 8, responsePayload)

	disabledAllocs := testing.AllocsPerRun(100, func() {
		runCaptureMemoryRequests(t, disabledHandler, requestPayload, 0, 1)
	})
	enabledAllocs := testing.AllocsPerRun(100, func() {
		runCaptureMemoryRequests(t, enabledHandler, requestPayload, 0, 1)
	})
	captureAllocs := enabledAllocs - disabledAllocs

	t.Logf(
		"allocations/request: disabled=%.1f enabled=%.1f capture-only=%.1f",
		disabledAllocs,
		enabledAllocs,
		captureAllocs,
	)
	if math.IsNaN(captureAllocs) || math.IsInf(captureAllocs, 0) {
		t.Fatalf("capture allocation profile is not finite: %v", captureAllocs)
	}
	if captureAllocs <= 0 {
		t.Errorf("capture allocation profile = %.1f, want a positive measured delta", captureAllocs)
	}
	// This is intentionally a broad leak guard rather than a micro-optimization
	// budget. A request currently needs only a few dozen capture allocations;
	// hundreds more per request would indicate accidentally retained copies or
	// an allocation loop.
	if captureAllocs > 200 {
		t.Errorf("capture allocation profile = %.1f allocations/request, want <= 200", captureAllocs)
	}
	if got := enabledCapture.GetEntryCount(); got != 8 {
		t.Errorf("retained entries after allocation profile = %d, want 8", got)
	}
	if got := disabledCapture.GetEntryCount(); got != 0 {
		t.Errorf("disabled capture retained %d entries, want 0", got)
	}
}

// TestSustainedCaptureMemoryPlateausAtRetentionLimit exercises more than one
// thousand requests after filling the corpus. Total allocations must continue
// to move (proving the workload ran), while live heap and object counts stay
// near the filled-ring baseline after garbage collection.
func TestSustainedCaptureMemoryPlateausAtRetentionLimit(t *testing.T) {
	if raceEnabled {
		t.Skip("runtime heap thresholds are not comparable under race instrumentation")
	}
	muteCaptureMemoryTestLogs(t)

	requestPayload := bytes.Repeat([]byte("r"), 8*1024)
	responsePayload := bytes.Repeat([]byte("s"), 8*1024)
	cm, handler := newCaptureMemoryTestHandler(t, captureMemoryTestRetention, responsePayload)

	runCaptureMemoryRequests(t, handler, requestPayload, 0, captureMemoryTestRetention)
	atLimit := readCaptureMemoryProfile()
	runCaptureMemoryRequests(
		t,
		handler,
		requestPayload,
		captureMemoryTestRetention,
		captureMemoryTestRequests,
	)
	afterSustained := readCaptureMemoryProfile()
	runtime.KeepAlive(handler)

	if got := cm.GetEntryCount(); got != captureMemoryTestRetention {
		t.Fatalf("retained entries = %d, want retention limit %d", got, captureMemoryTestRetention)
	}
	if afterSustained.totalAlloc <= atLimit.totalAlloc || afterSustained.mallocs <= atLimit.mallocs {
		t.Error("allocation counters did not advance during sustained capture workload")
	}

	heapGrowth := signedCaptureMemoryDelta(afterSustained.heapAlloc, atLimit.heapAlloc)
	objectGrowth := signedCaptureMemoryDelta(afterSustained.heapObjects, atLimit.heapObjects)
	t.Logf(
		"sustained captures=%d total-allocated=%dB mallocs=%d live-heap-delta=%dB live-object-delta=%d",
		captureMemoryTestRequests,
		afterSustained.totalAlloc-atLimit.totalAlloc,
		afterSustained.mallocs-atLimit.mallocs,
		heapGrowth,
		objectGrowth,
	)
	if heapGrowth > 3*1024*1024 {
		t.Errorf("live heap grew by %d bytes after retention was full, want <= 3 MiB", heapGrowth)
	}
	if objectGrowth > 3000 {
		t.Errorf("live heap objects grew by %d after retention was full, want <= 3000", objectGrowth)
	}
}

// TestDisabledCaptureReleasesTransientMemory verifies that disabling capture
// freezes the retained corpus and that allocations from a further sustained
// request workload are collectable. The retained entries remain intentionally
// available for Save; the middleware and its bounded corpus are reclaimed once
// the disabled capture lifecycle ends.
func TestDisabledCaptureReleasesTransientMemory(t *testing.T) {
	if raceEnabled {
		t.Skip("runtime heap thresholds are not comparable under race instrumentation")
	}
	muteCaptureMemoryTestLogs(t)

	var atDisable captureMemoryProfile
	func() {
		requestPayload := bytes.Repeat([]byte("r"), 16*1024)
		responsePayload := bytes.Repeat([]byte("s"), 16*1024)
		cm, handler := newCaptureMemoryTestHandler(t, 32, responsePayload)
		runCaptureMemoryRequests(t, handler, requestPayload, 0, 32)
		cm.Disable()

		atDisable = readCaptureMemoryProfile()
		runCaptureMemoryRequests(t, handler, requestPayload, 32, captureMemoryTestRequests)
		afterDisabledWork := readCaptureMemoryProfile()
		if got := cm.GetEntryCount(); got != 32 {
			t.Fatalf("disabled capture retained %d entries, want the original 32", got)
		}
		if afterDisabledWork.totalAlloc <= atDisable.totalAlloc {
			t.Error("allocation counter did not advance during disabled request workload")
		}

		heapGrowth := signedCaptureMemoryDelta(afterDisabledWork.heapAlloc, atDisable.heapAlloc)
		objectGrowth := signedCaptureMemoryDelta(afterDisabledWork.heapObjects, atDisable.heapObjects)
		t.Logf(
			"disabled requests=%d total-allocated=%dB live-heap-delta=%dB live-object-delta=%d",
			captureMemoryTestRequests,
			afterDisabledWork.totalAlloc-atDisable.totalAlloc,
			heapGrowth,
			objectGrowth,
		)
		if heapGrowth > 2*1024*1024 {
			t.Errorf("live heap grew by %d bytes after capture was disabled, want <= 2 MiB", heapGrowth)
		}
		if objectGrowth > 2000 {
			t.Errorf("live heap objects grew by %d after capture was disabled, want <= 2000", objectGrowth)
		}
		runtime.KeepAlive(handler)
	}()

	afterTeardown := readCaptureMemoryProfile()
	released := signedCaptureMemoryDelta(atDisable.heapAlloc, afterTeardown.heapAlloc)
	t.Logf("live heap released after disabled capture teardown=%dB", released)
	if released < 1024*1024 {
		t.Errorf("disabled capture lifecycle released %d bytes, want at least 1 MiB", released)
	}
}

// TestCaptureRetentionLimitBoundsPersistedCorpus verifies that the in-memory
// ring and the saved corpus contain only the newest entries after sustained
// capture goes beyond the default retention limit.
func TestCaptureRetentionLimitBoundsPersistedCorpus(t *testing.T) {
	muteCaptureMemoryTestLogs(t)

	const requestCount = DefaultCaptureRetentionLimit + 25
	cm, handler := newCaptureMemoryTestHandler(t, DefaultCaptureRetentionLimit, []byte("response"))
	runCaptureMemoryRequests(t, handler, []byte("request"), 0, requestCount)

	if got := cm.GetEntryCount(); got != DefaultCaptureRetentionLimit {
		t.Fatalf("retained entries = %d, want %d", got, DefaultCaptureRetentionLimit)
	}
	if err := cm.Save(); err != nil {
		t.Fatalf("save retained corpus: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(cm.corpusDir, "corpus.json"))
	if err != nil {
		t.Fatalf("read retained corpus: %v", err)
	}
	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("decode retained corpus: %v", err)
	}
	if got := len(corpus.Entries); got != DefaultCaptureRetentionLimit {
		t.Fatalf("persisted entries = %d, want %d", got, DefaultCaptureRetentionLimit)
	}

	wantFirst := fmt.Sprintf("memory-profile-%06d-post", requestCount-DefaultCaptureRetentionLimit)
	wantLast := fmt.Sprintf("memory-profile-%06d-post", requestCount-1)
	if got := corpus.Entries[0].ID; got != wantFirst {
		t.Errorf("oldest persisted entry = %q, want %q", got, wantFirst)
	}
	if got := corpus.Entries[len(corpus.Entries)-1].ID; got != wantLast {
		t.Errorf("newest persisted entry = %q, want %q", got, wantLast)
	}
}
