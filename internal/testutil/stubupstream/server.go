// Package stubupstream provides a scriptable HTTP server for testing SEAM's
// integration with upstream services. It can simulate various upstream behaviors
// including credential echo, authentication failures, transport faults, timeouts,
// protocol upgrades, and oversized responses.
package stubupstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Behavior controls how the stub upstream responds to requests.
type Behavior string

const (
	// BehaviorEcho echoes back the injected credential in an error body
	BehaviorEcho Behavior = "echo"
	// Behavior401 returns a 401 Unauthorized response (stale credential)
	Behavior401 Behavior = "401"
	// Behavior5xx returns a 500 Internal Server Error
	Behavior5xx Behavior = "5xx"
	// BehaviorTimeout hangs until timeout or cancellation
	BehaviorTimeout Behavior = "timeout"
	// BehaviorUpgrade attempts a protocol upgrade (e.g., to WebSocket)
	BehaviorUpgrade Behavior = "upgrade"
	// BehaviorOversized returns an oversized response body
	BehaviorOversized Behavior = "oversized"
	// BehaviorNormal returns a normal 200 OK response
	BehaviorNormal Behavior = "normal"
	// BehaviorTransportFault simulates transport-level failures
	BehaviorTransportFault Behavior = "transport_fault"
)

// Config holds the stub upstream server configuration.
type Config struct {
	// Addr is the listen address (e.g., ":8080")
	Addr string
	// Behavior is the current behavior mode
	Behavior Behavior
	// OversizedSize is the size of the oversized response in bytes
	OversizedSize int64
	// TimeoutDelay is how long to hang before responding (for BehaviorTimeout)
	TimeoutDelay time.Duration
	// FailCount tracks consecutive failures for breaker testing
	FailCount int
	// FailThreshold is how many failures before returning success (for breaker recovery)
	FailThreshold int
}

// Server is a scriptable HTTP server for testing upstream behavior.
type Server struct {
	cfg     Config
	cfgMu   sync.RWMutex
	server  *http.Server
	callLog []CallRecord
	callMu  sync.Mutex
	started bool
	startMu sync.Mutex
}

// CallRecord records information about a request received by the stub upstream.
type CallRecord struct {
	Timestamp  time.Time
	Method     string
	Path       string
	Headers    http.Header
	AuthHeader string // The Authorization header value, if present
	CustomAuth string // Custom auth header value (e.g., X-API-Key)
	BodySize   int64
	RemoteAddr string
}

// New creates a new stub upstream server.
func New(cfg Config) *Server {
	if cfg.OversizedSize == 0 {
		cfg.OversizedSize = 10 * 1024 * 1024 // 10 MiB default
	}
	if cfg.TimeoutDelay == 0 {
		cfg.TimeoutDelay = 30 * time.Second
	}
	if cfg.FailThreshold == 0 {
		cfg.FailThreshold = 5 // Default breaker threshold
	}

	return &Server{
		cfg:     cfg,
		callLog: make([]CallRecord, 0),
	}
}

// SetBehavior changes the server's behavior mode.
func (s *Server) SetBehavior(behavior Behavior) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfg.Behavior = behavior
	log.Printf("[stubupstream] Behavior changed to: %s", behavior)
}

// GetBehavior returns the current behavior mode.
func (s *Server) GetBehavior() Behavior {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.Behavior
}

// Start begins listening for HTTP requests.
func (s *Server) Start() error {
	s.startMu.Lock()
	defer s.startMu.Unlock()

	if s.started {
		return fmt.Errorf("server already started")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRequest)
	mux.HandleFunc("/_control", s.handleControl)

	s.server = &http.Server{
		Addr:    s.cfg.Addr,
		Handler: mux,
	}

	log.Printf("[stubupstream] Starting on %s with behavior: %s", s.cfg.Addr, s.cfg.Behavior)

	s.started = true
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[stubupstream] ListenAndServe error: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop(ctx context.Context) error {
	s.startMu.Lock()
	defer s.startMu.Unlock()

	if !s.started {
		return nil
	}

	log.Printf("[stubupstream] Shutting down...")
	return s.server.Shutdown(ctx)
}

// URL returns the base URL of the server.
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s", s.cfg.Addr)
}

// GetCallLog returns a copy of the call log.
func (s *Server) GetCallLog() []CallRecord {
	s.callMu.Lock()
	defer s.callMu.Unlock()
	log := make([]CallRecord, len(s.callLog))
	copy(log, s.callLog)
	return log
}

// ClearCallLog clears the call log.
func (s *Server) ClearCallLog() {
	s.callMu.Lock()
	defer s.callMu.Unlock()
	s.callLog = make([]CallRecord, 0)
}

// GetCallCount returns the number of recorded calls.
func (s *Server) GetCallCount() int {
	s.callMu.Lock()
	defer s.callMu.Unlock()
	return len(s.callLog)
}

// ResetFailCount resets the failure counter.
func (s *Server) ResetFailCount() {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfg.FailCount = 0
	log.Printf("[stubupstream] Fail count reset")
}

// GetOversizedSize returns the configured oversized response size.
func (s *Server) GetOversizedSize() int64 {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.OversizedSize
}

// handleControl handles control-plane requests for changing server behavior.
func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Method == http.MethodGet {
		// Return current status
		s.cfgMu.RLock()
		status := map[string]interface{}{
			"behavior":      s.cfg.Behavior,
			"failCount":     s.cfg.FailCount,
			"failThreshold": s.cfg.FailThreshold,
			"callCount":     s.GetCallCount(),
		}
		s.cfgMu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
		return
	}

	// POST: Update behavior
	var cmd struct {
		Behavior     Behavior `json:"behavior"`
		ResetFailCnt bool     `json:"resetFailCount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if cmd.Behavior != "" {
		s.SetBehavior(cmd.Behavior)
	}
	if cmd.ResetFailCnt {
		s.ResetFailCount()
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// handleRequest handles incoming requests based on the current behavior.
func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	// Record the call
	record := CallRecord{
		Timestamp:  time.Now(),
		Method:     r.Method,
		Path:       r.URL.Path,
		Headers:    r.Header.Clone(),
		RemoteAddr: r.RemoteAddr,
	}

	// Extract auth headers
	if auth := r.Header.Get("Authorization"); auth != "" {
		record.AuthHeader = auth
	}
	// Also check for common custom auth headers
	for _, headerName := range []string{"X-API-Key", "X-Auth-Token", "API-Key"} {
		if val := r.Header.Get(headerName); val != "" {
			record.CustomAuth = val
			break
		}
	}

	// Track body size if present
	if r.Body != nil {
		// Read and discard body to track size
		n, _ := io.Copy(io.Discard, r.Body)
		record.BodySize = n
		_ = r.Body.Close()
	}

	s.callMu.Lock()
	s.callLog = append(s.callLog, record)
	s.callMu.Unlock()

	// Get current behavior
	s.cfgMu.RLock()
	behavior := s.cfg.Behavior
	failCount := s.cfg.FailCount
	failThreshold := s.cfg.FailThreshold
	oversizedSize := s.cfg.OversizedSize
	timeoutDelay := s.cfg.TimeoutDelay
	s.cfgMu.RUnlock()

	switch behavior {
	case BehaviorEcho:
		s.handleEcho(w, r, record)
	case Behavior401:
		s.handle401(w, r)
	case Behavior5xx:
		s.handle5xx(w, r)
	case BehaviorTimeout:
		s.handleTimeout(w, r, timeoutDelay)
	case BehaviorUpgrade:
		s.handleUpgrade(w, r)
	case BehaviorOversized:
		s.handleOversized(w, r, oversizedSize)
	case BehaviorTransportFault:
		s.handleTransportFault(w, r, failCount, failThreshold)
	case BehaviorNormal:
		s.handleNormal(w, r)
	default:
		s.handleNormal(w, r)
	}
}

// handleEcho echoes back any credential in an error response body.
func (s *Server) handleEcho(w http.ResponseWriter, r *http.Request, record CallRecord) {
	credential := record.AuthHeader
	if credential == "" && record.CustomAuth != "" {
		credential = record.CustomAuth
	}
	if credential == "" {
		credential = "(no credential found)"
	}

	// Strip common prefixes to get the raw value
	credential = strings.TrimPrefix(credential, "Bearer ")

	errorResp := map[string]interface{}{
		"error": "authentication_failed",
		"message": fmt.Sprintf(
			"Your credential '%s' was rejected. This is a test error body "+
				"intentionally echoing the credential to test scrubbing.",
			credential,
		),
		"credential_echo": credential,
	}

	w.Header().Set("Content-Type", "application/json")
	// Echo the credential in every response location that the gateway promises
	// to scrub. This keeps the scriptable fixture aligned with Scenario 1's
	// body/header/trailer acceptance boundary.
	w.Header().Set("X-Credential-Echo", credential)
	w.Header().Add("Trailer", "X-Credential-Trailer")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(errorResp)
	w.Header().Set("X-Credential-Trailer", credential)
}

// handle401 returns a 401 Unauthorized response.
func (s *Server) handle401(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="stub upstream"`)
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   "invalid_token",
		"message": "The provided credential is stale or invalid",
	})
}

// handle5xx returns a 500 Internal Server Error.
func (s *Server) handle5xx(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   "internal_server_error",
		"message": "The upstream service encountered an error",
	})
}

// handleTimeout hangs until the context is canceled or timeout elapses.
func (s *Server) handleTimeout(w http.ResponseWriter, r *http.Request, delay time.Duration) {
	// We're intentionally not writing a response, just hanging
	ctx := r.Context()
	log.Printf("[stubupstream] Timeout mode: hanging for %v", delay)

	select {
	case <-time.After(delay):
		// Finally respond after delay
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Response after timeout"))
	case <-ctx.Done():
		// Client canceled
		log.Printf("[stubupstream] Client canceled during timeout")
		return
	}
}

// handleUpgrade attempts a protocol upgrade.
func (s *Server) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	// Signal a protocol upgrade (e.g., WebSocket)
	w.Header().Set("Connection", "Upgrade")
	w.Header().Set("Upgrade", "websocket")
	w.Header().Set("Sec-WebSocket-Version", "13")
	w.WriteHeader(http.StatusSwitchingProtocols)

	// We don't actually implement the protocol, just signal the upgrade
	log.Printf("[stubupstream] Protocol upgrade signaled")
}

// handleOversized returns an oversized response body.
func (s *Server) handleOversized(w http.ResponseWriter, r *http.Request, size int64) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)

	// Write a large response
	chunk := make([]byte, 4096)
	for i := range chunk {
		chunk[i] = byte('A' + (i % 26))
	}

	written := int64(0)
	for written < size {
		toWrite := int64(len(chunk))
		if written+toWrite > size {
			toWrite = size - written
		}
		n, err := w.Write(chunk[:toWrite])
		if err != nil {
			log.Printf("[stubupstream] Error writing oversized response: %v", err)
			return
		}
		written += int64(n)

		// Flush periodically
		if written%8192 == 0 {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}

	log.Printf("[stubupstream] Wrote %d bytes oversized response", written)
}

// handleTransportFault simulates transport-level failures.
func (s *Server) handleTransportFault(w http.ResponseWriter, r *http.Request, failCount, failThreshold int) {
	s.cfgMu.Lock()
	s.cfg.FailCount++
	currentCount := s.cfg.FailCount
	s.cfgMu.Unlock()

	log.Printf("[stubupstream] Transport fault: fail count %d/%d", currentCount, failThreshold)

	// Simulate connection reset by just closing without response
	// This will appear as a transport error to the client
	hj, ok := w.(http.Hijacker)
	if !ok {
		// If we can't hijack, just return an error
		http.Error(w, "Transport fault", http.StatusBadGateway)
		return
	}

	conn, _, err := hj.Hijack()
	if err != nil {
		http.Error(w, "Transport fault", http.StatusBadGateway)
		return
	}

	// Close immediately to simulate connection reset
	_ = conn.Close()
}

// handleNormal returns a normal 200 OK response.
func (s *Server) handleNormal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"message": "Request processed successfully",
		"path":    r.URL.Path,
		"method":  r.Method,
	})
}
