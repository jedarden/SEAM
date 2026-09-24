package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// durabilityRequestBody returns the deterministic request body for capture
// sequence number i. Bodies differ per entry so cross-entry corruption is
// visible after a save/load round trip.
func durabilityRequestBody(i int) string {
	return fmt.Sprintf(`{"seq":%d,"payload":"body-%d"}`, i, i)
}

// durabilityResponseBody returns the deterministic response body for capture
// sequence number i.
func durabilityResponseBody(i int) string {
	return fmt.Sprintf(`{"seq":%d}`, i)
}

// durabilityEchoHandler answers POST /<prefix>/<i> with the deterministic
// response body for i after checking the request body survived capture. A
// mismatch fails the request with a non-200 status so the caller's status
// assertion catches it.
func durabilityEchoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index, err := strconv.Atoi(r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:])
		if err != nil {
			http.Error(w, "bad capture test path", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != durabilityRequestBody(index) {
			http.Error(w, "request body changed by capture", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, durabilityResponseBody(index))
	})
}

// serveDurabilityRequests sends count numbered POSTs under prefix through
// handler, failing if capture changes any response status. Requests carry a
// body so request-body durability is exercised alongside the response; query
// is appended verbatim when non-empty.
func serveDurabilityRequests(t *testing.T, handler http.Handler, prefix string, start, count int, query string) {
	t.Helper()
	for i := start; i < start+count; i++ {
		path := fmt.Sprintf("%s/%d", prefix, i)
		if query != "" {
			path += "?" + query
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(durabilityRequestBody(i))))
		if w.Code != http.StatusOK {
			t.Fatalf("POST %s status = %d, want 200 (body %q)", path, w.Code, w.Body.String())
		}
	}
}

// TestCaptureAutoSaveFiresOnlyOnThreshold pins the autosave trigger to the
// documented every-10-entries interval: a write lands exactly on the 10th and
// 20th entries and never between thresholds. Save runs synchronously inside
// Wrap, so the counted writes are settled after each request.
func TestCaptureAutoSaveFiresOnlyOnThreshold(t *testing.T) {
	corpusDir := t.TempDir()
	cm := NewCaptureMiddleware(corpusDir, "test-service", "test-incumbent", true)

	writes := 0
	entriesAtLastWrite := 0
	cm.writeCorpusFile = func(name string, data []byte, perm os.FileMode) error {
		writes++
		var corpus CorpusFile
		if err := json.Unmarshal(data, &corpus); err != nil {
			return fmt.Errorf("autosave produced invalid JSON: %w", err)
		}
		entriesAtLastWrite = len(corpus.Entries)
		return os.WriteFile(name, data, perm)
	}

	handler := cm.Wrap(durabilityEchoHandler())

	// Nine entries stay below the threshold: nothing is written yet.
	serveDurabilityRequests(t, handler, "/below", 0, 9, "")
	if writes != 0 {
		t.Fatalf("autosave writes before the 10th entry = %d, want 0", writes)
	}
	if _, err := os.Stat(filepath.Join(corpusDir, "corpus.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corpus file exists below the autosave threshold: %v", err)
	}

	// The 10th entry crosses the threshold and flushes all ten.
	serveDurabilityRequests(t, handler, "/tenth", 9, 1, "")
	if writes != 1 {
		t.Fatalf("autosave writes after the 10th entry = %d, want 1", writes)
	}
	if entriesAtLastWrite != 10 {
		t.Fatalf("entries persisted by the threshold autosave = %d, want 10", entriesAtLastWrite)
	}

	// Entries 11 through 19 stay inside the next window.
	serveDurabilityRequests(t, handler, "/window", 10, 9, "")
	if writes != 1 {
		t.Fatalf("autosave writes between thresholds = %d, want 1", writes)
	}

	// The 20th entry flushes again, with the full retained history.
	serveDurabilityRequests(t, handler, "/twentieth", 19, 1, "")
	if writes != 2 {
		t.Fatalf("autosave writes after the 20th entry = %d, want 2", writes)
	}
	if entriesAtLastWrite != 20 {
		t.Fatalf("entries persisted by the second autosave = %d, want 20", entriesAtLastWrite)
	}
	if got := cm.GetEntryCount(); got != 20 {
		t.Fatalf("retained entries = %d, want 20", got)
	}
}

// TestCaptureAutoSaveDisabledNeverWrites verifies the autoSave flag gates the
// periodic trigger: with autosave disabled the middleware captures entries but
// persists nothing until Save is called explicitly.
func TestCaptureAutoSaveDisabledNeverWrites(t *testing.T) {
	corpusDir := t.TempDir()
	cm := NewCaptureMiddleware(corpusDir, "test-service", "test-incumbent", false)

	writes := 0
	cm.writeCorpusFile = func(name string, data []byte, perm os.FileMode) error {
		writes++
		return os.WriteFile(name, data, perm)
	}

	handler := cm.Wrap(durabilityEchoHandler())

	// Cross two autosave thresholds: without autoSave neither may write.
	serveDurabilityRequests(t, handler, "/manual", 0, 25, "")
	if writes != 0 {
		t.Fatalf("writes without autoSave = %d, want 0", writes)
	}
	if got := cm.GetEntryCount(); got != 25 {
		t.Fatalf("retained entries without autoSave = %d, want 25", got)
	}
	if _, err := os.Stat(filepath.Join(corpusDir, "corpus.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corpus file written without autoSave or an explicit save: %v", err)
	}

	if err := cm.Save(); err != nil {
		t.Fatalf("explicit save with autoSave disabled: %v", err)
	}
	if writes != 1 {
		t.Fatalf("writes after explicit save = %d, want 1", writes)
	}
}

// TestShutdownFlushesCorpusBelowAutoSaveThreshold pins the graceful-shutdown
// trigger: Server.Shutdown persists a corpus that never crossed the autosave
// threshold, so entries captured since the last periodic save survive a
// restart.
func TestShutdownFlushesCorpusBelowAutoSaveThreshold(t *testing.T) {
	corpusDir := t.TempDir()
	cm := NewCaptureMiddleware(corpusDir, "seam", "seam-incumbent", true)
	handler := cm.Wrap(durabilityEchoHandler())

	// Four entries stay below the threshold of ten: only the shutdown flush
	// can persist them.
	serveDurabilityRequests(t, handler, "/flush", 0, 4, "")
	if _, err := os.Stat(filepath.Join(corpusDir, "corpus.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corpus file created before shutdown without crossing the autosave threshold: %v", err)
	}

	s := &Server{captureMiddleware: cm}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown with a pending corpus flush: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(corpusDir, "corpus.json"))
	if err != nil {
		t.Fatalf("read corpus flushed on shutdown: %v", err)
	}
	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("decode corpus flushed on shutdown: %v", err)
	}
	if corpus.Schema != "seam-diff-corpus/v1" {
		t.Errorf("flushed corpus schema = %q, want seam-diff-corpus/v1", corpus.Schema)
	}
	if corpus.Service != "seam" || corpus.Incumbent != "seam-incumbent" {
		t.Errorf("flushed corpus service/incumbent = %q/%q, want seam/seam-incumbent", corpus.Service, corpus.Incumbent)
	}
	if len(corpus.Entries) != 4 {
		t.Fatalf("entries flushed on shutdown = %d, want 4", len(corpus.Entries))
	}
	for i, entry := range corpus.Entries {
		if want := fmt.Sprintf("/flush/%d", i); entry.Request.Path != want {
			t.Errorf("flushed entry %d path = %q, want %q (capture order must be preserved)", i, entry.Request.Path, want)
		}
	}
}

// TestShutdownFlushRespectsCaptureToggle verifies shutdown only writes when
// capture is enabled, never clobbers an existing corpus with data captured
// while disabled, and tolerates a server built without a capture middleware.
func TestShutdownFlushRespectsCaptureToggle(t *testing.T) {
	t.Run("disabled capture writes nothing", func(t *testing.T) {
		corpusDir := t.TempDir()
		cm := NewCaptureMiddleware(corpusDir, "seam", "seam-incumbent", true)
		cm.Disable()
		serveDurabilityRequests(t, cm.Wrap(durabilityEchoHandler()), "/disabled", 0, 4, "")
		if got := cm.GetEntryCount(); got != 0 {
			t.Fatalf("entries captured while disabled = %d, want 0", got)
		}

		s := &Server{captureMiddleware: cm}
		if err := s.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown with capture disabled: %v", err)
		}
		if _, err := os.Stat(filepath.Join(corpusDir, "corpus.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("shutdown flushed a corpus while capture was disabled: %v", err)
		}
	})

	t.Run("disabled capture keeps earlier corpus intact", func(t *testing.T) {
		corpusDir := t.TempDir()
		cm := NewCaptureMiddleware(corpusDir, "seam", "seam-incumbent", true)
		serveDurabilityRequests(t, cm.Wrap(durabilityEchoHandler()), "/before", 0, 2, "")
		if err := cm.Save(); err != nil {
			t.Fatalf("save corpus before disabling capture: %v", err)
		}
		corpusPath := filepath.Join(corpusDir, "corpus.json")
		before, err := os.ReadFile(corpusPath)
		if err != nil {
			t.Fatalf("read corpus before disabling capture: %v", err)
		}

		cm.Disable()
		serveDurabilityRequests(t, cm.Wrap(durabilityEchoHandler()), "/after", 0, 5, "")

		s := &Server{captureMiddleware: cm}
		if err := s.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown with capture disabled: %v", err)
		}
		after, err := os.ReadFile(corpusPath)
		if err != nil {
			t.Fatalf("read corpus after shutdown: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Error("shutdown rewrote the on-disk corpus while capture was disabled")
		}
	})

	t.Run("nil capture middleware", func(t *testing.T) {
		s := &Server{}
		if err := s.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown without a capture middleware: %v", err)
		}
	})
}

// TestShutdownSaveFailureIsContained verifies a corpus write failure during
// graceful shutdown neither fails the shutdown nor loses the retained entries:
// the error is logged and shutdown completes so the process can still exit.
func TestShutdownSaveFailureIsContained(t *testing.T) {
	logs := captureFailureLogs(t)
	corpusDir := t.TempDir()
	cm := NewCaptureMiddleware(corpusDir, "seam", "seam-incumbent", true)
	writeErr := errors.New("simulated shutdown corpus write failure")
	cm.writeCorpusFile = func(string, []byte, os.FileMode) error {
		return writeErr
	}
	serveDurabilityRequests(t, cm.Wrap(durabilityEchoHandler()), "/shutdown-failure", 0, 4, "")

	s := &Server{captureMiddleware: cm}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown must complete despite the corpus save failure: %v", err)
	}
	if got := cm.GetEntryCount(); got != 4 {
		t.Fatalf("entries retained after a failed shutdown flush = %d, want 4", got)
	}
	if _, err := os.Stat(filepath.Join(corpusDir, "corpus.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corpus file exists after a failed shutdown flush: %v", err)
	}
	if !strings.Contains(logs.String(), "Failed to save corpus on shutdown") ||
		!strings.Contains(logs.String(), writeErr.Error()) {
		t.Errorf("expected the shutdown save failure to be logged, got %q", logs.String())
	}
}

// TestCaptureCorpusReadableAfterRestart drives the production restart path:
// a first process crosses the autosave threshold and is flushed on shutdown, a
// fresh middleware loads the corpus the way New does at startup, and every
// persisted entry round-trips with IDs, capture order, and request and
// response bodies intact, under the corpus schema/service/incumbent header.
// The restarted instance then continues the autosave cadence with the loaded
// history included, appending without clobbering what it loaded.
func TestCaptureCorpusReadableAfterRestart(t *testing.T) {
	corpusDir := t.TempDir()

	// First process: 12 requests cross one autosave threshold (write on the
	// 10th) and the remaining two are flushed on shutdown.
	cm1 := NewCaptureMiddleware(corpusDir, "restart-service", "https://incumbent.example.test", true)
	handler1 := cm1.Wrap(durabilityEchoHandler())
	serveDurabilityRequests(t, handler1, "/roundtrip", 0, 12, "")
	s1 := &Server{captureMiddleware: cm1}
	if err := s1.Shutdown(context.Background()); err != nil {
		t.Fatalf("first process shutdown: %v", err)
	}

	// The shutdown flush is what the restart reads: record the persisted IDs
	// so the restarted instance's next save can be proven to carry them
	// through unchanged instead of regenerating them.
	flushed, err := os.ReadFile(filepath.Join(corpusDir, "corpus.json"))
	if err != nil {
		t.Fatalf("read corpus after first shutdown: %v", err)
	}
	var flushedCorpus CorpusFile
	if err := json.Unmarshal(flushed, &flushedCorpus); err != nil {
		t.Fatalf("decode corpus after first shutdown: %v", err)
	}
	if len(flushedCorpus.Entries) != 12 {
		t.Fatalf("shutdown-flushed entries = %d, want 12", len(flushedCorpus.Entries))
	}
	flushedIDs := make([]string, 0, len(flushedCorpus.Entries))
	for _, entry := range flushedCorpus.Entries {
		flushedIDs = append(flushedIDs, entry.ID)
	}

	// Restart: load through the production startup path.
	cm2 := NewCaptureMiddleware(corpusDir, "restart-service", "https://incumbent.example.test", true)
	if err := cm2.Load(); err != nil {
		t.Fatalf("load corpus after restart: %v", err)
	}
	if got := cm2.GetEntryCount(); got != 12 {
		t.Fatalf("entries loaded after restart = %d, want 12", got)
	}

	// The restarted instance continues the autosave cadence: the 10th new
	// entry triggers a write holding the loaded history plus the new entries.
	cm2Writes := 0
	entriesAtLastWrite := 0
	cm2.writeCorpusFile = func(name string, data []byte, perm os.FileMode) error {
		cm2Writes++
		var corpus CorpusFile
		if err := json.Unmarshal(data, &corpus); err != nil {
			return fmt.Errorf("restarted autosave produced invalid JSON: %w", err)
		}
		entriesAtLastWrite = len(corpus.Entries)
		return os.WriteFile(name, data, perm)
	}
	serveDurabilityRequests(t, cm2.Wrap(durabilityEchoHandler()), "/resume", 0, 10, "src=resume")
	if cm2Writes != 1 {
		t.Fatalf("autosave writes after restart = %d, want 1", cm2Writes)
	}
	if entriesAtLastWrite != 22 {
		t.Fatalf("entries persisted by the restarted autosave = %d, want 22", entriesAtLastWrite)
	}

	// Every persisted entry round-trips: decode both bodies, match the
	// deterministic payloads, and confirm status and ordering survive.
	data, err := os.ReadFile(filepath.Join(corpusDir, "corpus.json"))
	if err != nil {
		t.Fatalf("read corpus after restart autosave: %v", err)
	}
	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("decode corpus after restart autosave: %v", err)
	}
	if len(corpus.Entries) != 22 {
		t.Fatalf("persisted entries after restart = %d, want 22", len(corpus.Entries))
	}
	// The restarted save must still be a complete corpus document for the
	// same service and incumbent.
	if corpus.Schema != "seam-diff-corpus/v1" {
		t.Errorf("restarted corpus schema = %q, want seam-diff-corpus/v1", corpus.Schema)
	}
	if corpus.Service != "restart-service" || corpus.Incumbent != "https://incumbent.example.test" {
		t.Errorf("restarted corpus service/incumbent = %q/%q, want restart-service/https://incumbent.example.test",
			corpus.Service, corpus.Incumbent)
	}
	for i, entry := range corpus.Entries {
		var prefix string
		var seq int
		if i < 12 {
			prefix, seq = "/roundtrip", i
		} else {
			prefix, seq = "/resume", i-12
		}
		if want := fmt.Sprintf("%s/%d", prefix, seq); entry.Request.Path != want {
			t.Errorf("entry %d path = %q, want %q", i, entry.Request.Path, want)
		}
		// Loaded entries keep the IDs the first process generated; new
		// entries get fresh IDs in the same path-method shape.
		if want := fmt.Sprintf("%s-%d-post", strings.Trim(prefix, "/"), seq); entry.ID != want {
			t.Errorf("entry %d id = %q, want %q", i, entry.ID, want)
		}
		if i < 12 && entry.ID != flushedIDs[i] {
			t.Errorf("entry %d id = %q, does not match the id persisted before restart %q",
				i, entry.ID, flushedIDs[i])
		}
		gotRequest, err := base64.StdEncoding.DecodeString(entry.Request.BodyB64)
		if err != nil {
			t.Errorf("entry %d request body does not decode from base64: %v", i, err)
			continue
		}
		if string(gotRequest) != durabilityRequestBody(seq) {
			t.Errorf("entry %d request body = %q, want %q", i, gotRequest, durabilityRequestBody(seq))
		}
		gotResponse, err := base64.StdEncoding.DecodeString(entry.Response.BodyB64)
		if err != nil {
			t.Errorf("entry %d response body does not decode from base64: %v", i, err)
			continue
		}
		if string(gotResponse) != durabilityResponseBody(seq) {
			t.Errorf("entry %d response body = %q, want %q", i, gotResponse, durabilityResponseBody(seq))
		}
		if entry.Response.StatusCode != http.StatusOK {
			t.Errorf("entry %d response status = %d, want 200", i, entry.Response.StatusCode)
		}
	}
	if last := corpus.Entries[len(corpus.Entries)-1]; last.Request.Query != "src=resume" {
		t.Errorf("newest entry query = %q, want src=resume", last.Request.Query)
	}
}
