package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultCaptureRetentionLimit bounds the number of request/response pairs
// retained in memory and persisted to one corpus. Once the limit is reached,
// the oldest entry is replaced by the next capture.
const DefaultCaptureRetentionLimit = 1000

// CaptureMiddleware handles HTTP request/response capture for corpus collection
type CaptureMiddleware struct {
	enabled        bool
	corpusDir      string
	service        string
	incumbent      string
	mu             sync.Mutex
	entries        []CorpusEntry
	nextEntry      int
	retentionLimit int
	autoSave       bool
	saveCount      int
}

// CorpusEntry represents a single captured request/response pair
type CorpusEntry struct {
	ID          string           `json:"id"`
	Timestamp   string           `json:"timestamp"`
	Description string           `json:"description,omitempty"`
	Request     CapturedRequest  `json:"request"`
	Response    CapturedResponse `json:"response,omitempty"`
}

// CapturedRequest represents the captured HTTP request
type CapturedRequest struct {
	Method          string              `json:"method"`
	Path            string              `json:"path"`
	Query           string              `json:"query,omitempty"`
	Headers         map[string][]string `json:"headers,omitempty"`
	BodyB64         string              `json:"bodyB64,omitempty"`
	BodyContentType string              `json:"bodyContentType,omitempty"`
}

// CapturedResponse represents the captured HTTP response
type CapturedResponse struct {
	StatusCode      int                 `json:"statusCode"`
	Headers         map[string][]string `json:"headers,omitempty"`
	BodyB64         string              `json:"bodyB64,omitempty"`
	BodyContentType string              `json:"bodyContentType,omitempty"`
}

// CorpusFile represents the complete corpus file structure
type CorpusFile struct {
	Schema      string        `json:"schema"`
	Service     string        `json:"service"`
	Incumbent   string        `json:"incumbent"`
	CapturedAt  string        `json:"capturedAt"`
	Description string        `json:"description"`
	Entries     []CorpusEntry `json:"entries"`
}

// NewCaptureMiddleware creates a new capture middleware instance
func NewCaptureMiddleware(corpusDir, service, incumbent string, autoSave bool) *CaptureMiddleware {
	return newCaptureMiddlewareWithRetentionLimit(
		corpusDir,
		service,
		incumbent,
		autoSave,
		DefaultCaptureRetentionLimit,
	)
}

func newCaptureMiddlewareWithRetentionLimit(
	corpusDir, service, incumbent string,
	autoSave bool,
	retentionLimit int,
) *CaptureMiddleware {
	if retentionLimit <= 0 {
		retentionLimit = DefaultCaptureRetentionLimit
	}

	return &CaptureMiddleware{
		enabled:        true,
		corpusDir:      corpusDir,
		service:        service,
		incumbent:      incumbent,
		autoSave:       autoSave,
		retentionLimit: retentionLimit,
		entries:        make([]CorpusEntry, 0, min(100, retentionLimit)),
	}
}

// Wrap wraps an http.Handler with capture functionality
func (cm *CaptureMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Snapshot the mode when the request starts. A toggle can then affect
		// later requests without interrupting a capture already in progress.
		if !cm.IsEnabled() {
			next.ServeHTTP(w, r)
			return
		}

		// Skip capture for reserved paths
		if isReservedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Skip capture for health endpoints
		if r.URL.Path == "/_seam/healthz" || r.URL.Path == "/_seam/readyz" || r.URL.Path == "/_seam/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		// Capture the request
		req := cm.captureRequest(r)

		// Create response recorder
		rec := &responseRecorder{
			ResponseWriter: w,
			req:            r,
		}

		// Call the next handler
		next.ServeHTTP(rec, r)

		// Capture the response
		resp := cm.captureResponse(rec)

		// Generate entry ID and timestamp
		id := cm.generateEntryID(r)
		timestamp := time.Now().Format(time.RFC3339Nano)

		// Create corpus entry
		entry := CorpusEntry{
			ID:          id,
			Timestamp:   timestamp,
			Description: fmt.Sprintf("%s %s", r.Method, r.URL.Path),
			Request:     req,
			Response:    resp,
		}

		// Add to entries
		cm.mu.Lock()
		entryCount := cm.appendEntryLocked(entry)
		cm.saveCount++
		shouldSave := cm.autoSave && cm.saveCount%10 == 0
		cm.mu.Unlock()

		log.Printf("captured: %s %s (total %d entries)", r.Method, r.URL.Path, entryCount)

		// Auto-save every 10 entries
		if shouldSave {
			if err := cm.Save(); err != nil {
				log.Printf("failed to auto-save corpus: %v", err)
			}
		}
	})
}

// appendEntryLocked adds an entry to the bounded in-memory ring and returns
// the number of retained entries. Replacing the complete CorpusEntry drops all
// references to the evicted request and response data so the garbage collector
// can reclaim their bodies and header maps.
func (cm *CaptureMiddleware) appendEntryLocked(entry CorpusEntry) int {
	if len(cm.entries) < cm.retentionLimit {
		cm.entries = append(cm.entries, entry)
		return len(cm.entries)
	}

	cm.entries[cm.nextEntry] = entry
	cm.nextEntry = (cm.nextEntry + 1) % cm.retentionLimit
	return len(cm.entries)
}

// entriesInCaptureOrderLocked returns a snapshot ordered from oldest to newest.
// entries uses a ring once it reaches the retention limit, while corpus files
// keep their historical chronological ordering.
func (cm *CaptureMiddleware) entriesInCaptureOrderLocked() []CorpusEntry {
	entries := make([]CorpusEntry, len(cm.entries))
	if len(cm.entries) < cm.retentionLimit || cm.nextEntry == 0 {
		copy(entries, cm.entries)
		return entries
	}

	copied := copy(entries, cm.entries[cm.nextEntry:])
	copy(entries[copied:], cm.entries[:cm.nextEntry])
	return entries
}

// captureRequest captures the relevant parts of an HTTP request
func (cm *CaptureMiddleware) captureRequest(r *http.Request) CapturedRequest {
	req := CapturedRequest{
		Method:          r.Method,
		Path:            r.URL.Path,
		Query:           r.URL.RawQuery,
		Headers:         canonicalHeaders(r.Header.Clone()),
		BodyContentType: r.Header.Get("Content-Type"),
	}

	// Capture body if present
	if r.Body != nil && r.ContentLength > 0 {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("failed to read request body: %v", err)
			return req
		}
		// Restore the body for the handler
		r.Body = io.NopCloser(bytes.NewReader(body))
		req.BodyB64 = base64.StdEncoding.EncodeToString(body)
	}

	return req
}

// captureResponse captures the relevant parts of an HTTP response
func (cm *CaptureMiddleware) captureResponse(rec *responseRecorder) CapturedResponse {
	resp := CapturedResponse{
		StatusCode:      rec.statusCode,
		Headers:         canonicalHeaders(rec.Headers()),
		BodyContentType: rec.Header().Get("Content-Type"),
	}

	// Capture body if present
	if rec.bodyBuffer != nil {
		resp.BodyB64 = base64.StdEncoding.EncodeToString(rec.bodyBuffer.Bytes())
	}

	return resp
}

// generateEntryID generates a stable ID from the request
func (cm *CaptureMiddleware) generateEntryID(r *http.Request) string {
	path := strings.Trim(r.URL.Path, "/")
	if path == "" {
		path = "root"
	}

	// Convert path to safe ID format
	id := strings.ToLower(strings.ReplaceAll(path, "/", "-"))
	id = strings.ReplaceAll(id, "{", "")
	id = strings.ReplaceAll(id, "}", "")
	id = strings.ReplaceAll(id, ":", "-")

	return id + "-" + strings.ToLower(r.Method)
}

// Save saves the current corpus to a JSON file
func (cm *CaptureMiddleware) Save() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	entries := cm.entriesInCaptureOrderLocked()

	// Ensure corpus directory exists
	if err := os.MkdirAll(cm.corpusDir, 0o755); err != nil {
		return fmt.Errorf("create corpus directory: %w", err)
	}

	// Create corpus file structure
	corpus := CorpusFile{
		Schema:      "seam-diff-corpus/v1",
		Service:     cm.service,
		Incumbent:   cm.incumbent,
		CapturedAt:  time.Now().Format(time.RFC3339),
		Description: fmt.Sprintf("%s corpus captured from incumbent proxy", cm.service),
		Entries:     entries,
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(corpus, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal corpus: %w", err)
	}

	// Add trailing newline
	data = append(data, '\n')

	// Determine save path
	savePath := filepath.Join(cm.corpusDir, "corpus.json")

	// Write to file
	if err := os.WriteFile(savePath, data, 0o644); err != nil {
		return fmt.Errorf("write corpus file: %w", err)
	}

	log.Printf("corpus saved to %s (%d entries)", savePath, len(entries))
	return nil
}

// Load loads an existing corpus file
func (cm *CaptureMiddleware) Load() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	corpusPath := filepath.Join(cm.corpusDir, "corpus.json")
	if _, err := os.Stat(corpusPath); err != nil {
		// File doesn't exist yet, that's ok
		return nil
	}

	data, err := os.ReadFile(corpusPath)
	if err != nil {
		return fmt.Errorf("read corpus file: %w", err)
	}

	var corpus CorpusFile
	if err := json.Unmarshal(data, &corpus); err != nil {
		return fmt.Errorf("parse corpus file: %w", err)
	}

	entries := corpus.Entries
	if len(entries) > cm.retentionLimit {
		entries = entries[len(entries)-cm.retentionLimit:]
	}
	cm.entries = append(make([]CorpusEntry, 0, len(entries)), entries...)
	cm.nextEntry = 0
	log.Printf("loaded existing corpus (%d entries)", len(cm.entries))
	return nil
}

// Enable enables capture mode
func (cm *CaptureMiddleware) Enable() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.enabled = true
}

// Disable disables capture mode
func (cm *CaptureMiddleware) Disable() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.enabled = false
}

// IsEnabled returns whether capture is enabled
func (cm *CaptureMiddleware) IsEnabled() bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.enabled
}

// GetEntryCount returns the number of captured entries
func (cm *CaptureMiddleware) GetEntryCount() int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return len(cm.entries)
}

// canonicalHeaders returns a copy with textproto-canonicalized keys and empty
// value slices dropped. This ensures consistent header formatting.
func canonicalHeaders(h http.Header) map[string][]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string][]string, len(h))
	for k, vv := range h {
		ck := textproto.CanonicalMIMEHeaderKey(k)
		var keep []string
		for _, v := range vv {
			if v != "" {
				keep = append(keep, v)
			}
		}
		if len(keep) > 0 {
			out[ck] = keep
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// responseRecorder wraps ResponseWriter to capture status code, headers, and body
type responseRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
	bodyBuffer  *bytes.Buffer
	req         *http.Request
	headers     http.Header
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	if r.wroteHeader {
		return
	}
	r.statusCode = statusCode
	r.wroteHeader = true
	// Capture headers at WriteHeader time
	r.headers = r.ResponseWriter.Header().Clone()
	r.ResponseWriter.WriteHeader(statusCode)
}

// Headers returns the captured headers
func (r *responseRecorder) Headers() http.Header {
	if r.headers != nil {
		return r.headers
	}
	// If WriteHeader wasn't called yet, return current headers
	return r.Header()
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}

	// Capture body if not already captured
	if r.bodyBuffer == nil {
		r.bodyBuffer = &bytes.Buffer{}
	}
	r.bodyBuffer.Write(b)

	return r.ResponseWriter.Write(b)
}

func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// Hijack support for websocket connections (if needed in future)
func (r *responseRecorder) Hijack() (c interface{}, rw interface{}, err error) {
	if hijacker, ok := r.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, fmt.Errorf("responseRecorder: underlying ResponseWriter does not implement Hijacker")
}

// Flush support for streaming responses
func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// CloseNotify support (deprecated but might be needed)
//
// The deprecated http.CloseNotifier reference is deliberate: responseRecorder
// wraps an arbitrary http.ResponseWriter, and dropping this method would stop
// forwarding CloseNotify for any wrapped writer that still implements it.
// Detecting a deprecated interface in order to proxy it is exactly the case
// SA1019 cannot distinguish from using it in new code.
//
//nolint:staticcheck // SA1019: intentional passthrough of a deprecated interface
func (r *responseRecorder) CloseNotify() <-chan bool {
	if notifier, ok := r.ResponseWriter.(http.CloseNotifier); ok {
		return notifier.CloseNotify()
	}
	return nil
}
