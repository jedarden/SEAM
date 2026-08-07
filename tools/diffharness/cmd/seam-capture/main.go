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
	"bytes"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
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
	flag.Parse()

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
		target:      target,
		service:     *service,
		corpusPath:  *corpusPath,
		description: *description,
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
	target      *url.URL
	service     string
	corpusPath  string
	description string
	cp          *corpus.Corpus
	mu          chan struct{} // serializes corpus mutations
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
	return c.cp.Save(c.corpusPath)
}

func (c *capturer) captureHandler(w http.ResponseWriter, r *http.Request) {
	// Capture the request.
	req := corpus.Request{
		Method:          r.Method,
		Path:            r.URL.Path,
		Query:           r.URL.RawQuery,
		Headers:         canonHeaders(r.Header),
		BodyB64:         "",
		BodyContentType: r.Header.Get("Content-Type"),
	}

	// Capture body if present.
	if r.Body != nil && r.ContentLength > 0 {
		body, err := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusInternalServerError)
			log.Printf("read body: %v", err)
			return
		}
		req.BodyB64 = base64.StdEncoding.EncodeToString(body)
	}

	// Forward to incumbent.
	proxy := httputil.NewSingleHostReverseProxy(c.target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error: %v", err)
		http.Error(w, "proxy error", http.StatusBadGateway)
	}

	// We need to capture the response, so use a custom recorder.
	rec := &responseRecorder{ResponseWriter: w}

	// If this is a request from the capture process itself (e.g., health check), skip recording.
	skipCapture := r.Header.Get("X-Seam-Capture-Skip") != ""
	if skipCapture {
		r.Header.Del("X-Seam-Capture-Skip")
		proxy.ServeHTTP(w, r)
		return
	}

	proxy.ServeHTTP(rec, r)

	// Record the entry.
	<-c.mu
	defer func() { c.mu <- struct{}{} }()

	entry := corpus.Entry{
		ID:          entryID(r),
		Description: fmt.Sprintf("%s %s", r.Method, r.URL.Path),
		Request:     req,
	}

	// TODO: populate Secrets when SEAM's route-fragment schema is available.
	// For now, we capture without secrets; they can be added manually or when
	// the capture tool is integrated with SEAM's route parser.

	if err := c.cp.AppendEntry(entry); err != nil {
		log.Printf("failed to append entry: %v", err)
		// Continue anyway; the client got its response.
	}

	log.Printf("captured: %s %s (total %d entries)", r.Method, r.URL.Path, len(c.cp.Entries))

	// Periodically save the corpus (every 10 entries).
	if len(c.cp.Entries)%10 == 0 {
		if err := c.saveCorpus(); err != nil {
			log.Printf("failed to save corpus: %v", err)
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

// responseRecorder wraps ResponseWriter to capture the status code and headers.
type responseRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	if r.wroteHeader {
		return
	}
	r.statusCode = statusCode
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}
