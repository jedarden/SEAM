// seam-capture is a differential capture proxy: it sits in front of an incumbent
// proxy, records each request/response pair into a corpus file, and forwards the
// request through to the incumbent. The captured corpus is the oracle for the
// Phase 6b cutover: a service's SEAM route must pass replay against this corpus
// before it ships.
//
// Usage:
//
//	seam-capture --incumbent https://proxy.example.com \
//	             --service argocd \
//	             --corpus argocd-corpus.json \
//	             --listen :8080
//
// The proxy listens on --listen (default :8080), forwards every request to
// --incumbent, and appends the captured request/response to --corpus.
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ardenone/seam/tools/diffharness/internal/corpus"
)

func main() {
	incumbentURL := flag.String("incumbent", "", "Base URL of the incumbent proxy (required)")
	service := flag.String("service", "", "Service name token for the corpus (required)")
	corpusPath := flag.String("corpus", "", "Path to the corpus JSON file (required)")
	listen := flag.String("listen", ":8080", "Address to listen on")
	description := flag.String("description", "", "Free-form description for the corpus header")
	enabled := flag.Bool("capture-enabled", true, "Record request/response pairs (also configurable with SEAM_CAPTURE_ENABLED)")
	flag.Parse()
	if value := os.Getenv("SEAM_CAPTURE_ENABLED"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			log.Printf("invalid SEAM_CAPTURE_ENABLED=%q; keeping --capture-enabled=%t", value, *enabled)
		} else {
			*enabled = parsed
		}
	}

	if *incumbentURL == "" || *service == "" || *corpusPath == "" {
		fmt.Fprintf(os.Stderr, "seam-capture: missing required flags\n")
		flag.PrintDefaults()
		os.Exit(2)
	}

	target, err := url.Parse(*incumbentURL)
	if err != nil {
		log.Fatalf("invalid incumbent URL %q: %v", *incumbentURL, err)
	}

	cap := &capturer{
		target:            target,
		service:           *service,
		corpusPath:        *corpusPath,
		description:       *description,
		enabled:           *enabled,
		captureConfigured: true,
	}

	if err := cap.loadCorpus(); err != nil {
		log.Fatalf("load corpus: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", cap.captureHandler)

	srv := &http.Server{
		Addr:    *listen,
		Handler: mux,
	}

	go func() {
		log.Printf("seam-capture listening on %s, forwarding to %s", *listen, *incumbentURL)
		log.Printf("corpus: %s", *corpusPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("shutting down...")
	if err := srv.Close(); err != nil {
		log.Fatalf("close: %v", err)
	}

	if err := cap.saveCorpus(); err != nil {
		log.Fatalf("save corpus: %v", err)
	}
	log.Printf("corpus saved (%d entries)", len(cap.cp.Entries))
}

type capturer struct {
	target            *url.URL
	service           string
	corpusPath        string
	description       string
	enabled           bool
	captureConfigured bool
	cp                *corpus.Corpus
	mu                chan struct{} // serializes corpus mutations
}

func (c *capturer) loadCorpus() error {
	c.mu = make(chan struct{}, 1)
	c.mu <- struct{}{}

	// Try to load existing corpus; start a new one if it doesn't exist.
	if _, err := os.Stat(c.corpusPath); err == nil {
		var err error
		c.cp, err = corpus.Load(c.corpusPath)
		if err != nil {
			return fmt.Errorf("load existing corpus: %w", err)
		}
		if c.cp.Service != c.service {
			return fmt.Errorf("corpus service mismatch: corpus has %q, flag is %q", c.cp.Service, c.service)
		}
		if c.cp.Incumbent != c.target.String() {
			log.Printf("warning: incumbent URL changed from %q to %q", c.cp.Incumbent, c.target.String())
		}
		log.Printf("loaded existing corpus (%d entries)", len(c.cp.Entries))
		return nil
	}

	// New corpus.
	c.cp = &corpus.Corpus{
		Service:     c.service,
		Incumbent:   c.target.String(),
		CapturedAt:  time.Now().Format(time.RFC3339),
		Description: c.description,
		Entries:     []corpus.Entry{},
	}
	return nil
}

func (c *capturer) saveCorpus() error {
	<-c.mu
	defer func() { c.mu <- struct{}{} }()
	return c.cp.Save(c.corpusPath)
}

func (c *capturer) captureHandler(w http.ResponseWriter, r *http.Request) {
	proxy := httputil.NewSingleHostReverseProxy(c.target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error: %v", err)
		http.Error(w, "proxy error", http.StatusBadGateway)
	}

	// The skip header is a local control signal and must never reach the
	// incumbent. Disabled capture remains a transparent forwarding path.
	skipCapture := r.Header.Get("X-Seam-Capture-Skip") != ""
	r.Header.Del("X-Seam-Capture-Skip")
	if skipCapture || !c.captureEnabled() {
		proxy.ServeHTTP(w, r)
		return
	}

	// Capture the request.
	req := corpus.Request{
		Method:          r.Method,
		Path:            r.URL.Path,
		Query:           r.URL.RawQuery,
		Headers:         canonHeaders(r.Header),
		BodyB64:         "",
		BodyContentType: r.Header.Get("Content-Type"),
	}

	// Capture body if present, including chunked requests whose ContentLength
	// is -1. If reading fails, restore the bytes already read and continue
	// forwarding so capture cannot disrupt normal proxy operation.
	if r.Body != nil && r.Body != http.NoBody {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("read body: %v", err)
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		} else {
			r.Body = io.NopCloser(bytes.NewReader(body))
			req.BodyB64 = base64.StdEncoding.EncodeToString(body)
		}
	}

	// Forward to incumbent through a recorder that forwards every write while
	// retaining the status, headers, and body sent to the caller.
	rec := newResponseRecorder(w)
	proxy.ServeHTTP(rec, r)
	response := rec.capturedResponse()

	// Record the entry.
	<-c.mu
	entry := corpus.Entry{
		ID:          entryID(r),
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		Description: fmt.Sprintf("%s %s", r.Method, r.URL.Path),
		Request:     req,
		Response:    response,
	}
	entry.ID = c.uniqueEntryIDLocked(entry.ID)

	// TODO: populate Secrets when SEAM's route-fragment schema is available.
	// For now, we capture without secrets; they can be added manually or when
	// the capture tool is integrated with SEAM's route parser.

	appendErr := c.cp.AppendEntry(entry)
	entryCount := len(c.cp.Entries)
	var saveErr error
	if appendErr == nil && entryCount%10 == 0 {
		saveErr = c.cp.Save(c.corpusPath)
	}
	c.mu <- struct{}{}

	if appendErr != nil {
		log.Printf("failed to append entry: %v", appendErr)
	}
	if saveErr != nil {
		log.Printf("failed to save corpus: %v", saveErr)
	}
	log.Printf("captured: %s %s (total %d entries)", r.Method, r.URL.Path, entryCount)
}

func (c *capturer) captureEnabled() bool {
	// Direct users and older tests that construct a capturer retain the
	// historical default (enabled). The command sets captureConfigured so an
	// explicit --capture-enabled=false is distinguishable from the zero value.
	if !c.captureConfigured {
		return true
	}
	return c.enabled
}

func (c *capturer) uniqueEntryIDLocked(base string) string {
	used := make(map[string]struct{}, len(c.cp.Entries))
	for _, existing := range c.cp.Entries {
		used[existing.ID] = struct{}{}
	}
	if _, exists := used[base]; !exists {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func entryID(r *http.Request) string {
	// Generate a stable ID from method + path.
	path := strings.Trim(r.URL.Path, "/")
	if path == "" {
		path = "root"
	}
	return strings.ToLower(strings.ReplaceAll(path, "/", "-")) + "-" + strings.ToLower(r.Method)
}

func canonHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, vv := range h {
		ck := http.CanonicalHeaderKey(k)
		if sensitiveHeader(ck) {
			out[ck] = []string{"[REDACTED-BY-SEAM]"}
			continue
		}
		var filtered []string
		for _, v := range vv {
			if v != "" {
				filtered = append(filtered, v)
			}
		}
		if len(filtered) > 0 {
			out[ck] = filtered
		}
	}
	return out
}

func sensitiveHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key", "x-auth-token":
		return true
	default:
		return false
	}
}

// responseRecorder wraps ResponseWriter to capture the status code, headers,
// and body while forwarding writes immediately. Optional interfaces are
// passed through so httputil.ReverseProxy keeps streaming and protocol-upgrade
// behavior unchanged.
type responseRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
	headers     http.Header
	body        bytes.Buffer
}

func newResponseRecorder(w http.ResponseWriter) *responseRecorder {
	return &responseRecorder{ResponseWriter: w}
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	// Preserve informational responses such as 103 Early Hints. The first
	// non-1xx response is the response recorded for the corpus entry.
	if statusCode >= 100 && statusCode < 200 && statusCode != http.StatusSwitchingProtocols {
		r.ResponseWriter.WriteHeader(statusCode)
		return
	}
	if r.wroteHeader {
		return
	}
	r.statusCode = statusCode
	r.wroteHeader = true
	r.headers = r.ResponseWriter.Header().Clone()
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		if r.Header().Get("Content-Type") == "" && len(b) > 0 {
			probe := b
			if len(probe) > 512 {
				probe = probe[:512]
			}
			r.Header().Set("Content-Type", http.DetectContentType(probe))
		}
		r.WriteHeader(http.StatusOK)
	}
	_, _ = r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

func (r *responseRecorder) ReadFrom(src io.Reader) (int64, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return io.Copy(io.MultiWriter(&r.body, r.ResponseWriter), src)
}

func (r *responseRecorder) Flush() {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := r.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, fmt.Errorf("responseRecorder: underlying ResponseWriter does not implement Hijacker")
}

func (r *responseRecorder) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := r.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

//nolint:staticcheck // intentional passthrough for legacy handlers.
func (r *responseRecorder) CloseNotify() <-chan bool {
	if notifier, ok := r.ResponseWriter.(http.CloseNotifier); ok {
		return notifier.CloseNotify()
	}
	return nil
}

func (r *responseRecorder) capturedResponse() *corpus.Response {
	statusCode := r.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	headers := r.headers
	if headers == nil {
		headers = r.ResponseWriter.Header()
	}
	return &corpus.Response{
		StatusCode:      statusCode,
		Headers:         canonHeaders(headers),
		BodyB64:         base64.StdEncoding.EncodeToString(r.body.Bytes()),
		BodyContentType: headers.Get("Content-Type"),
	}
}
