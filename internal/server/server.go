package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime"
	"sync"
)

// reservedPaths are the control-plane endpoints that short-circuit route-table lookup.
// Requests matching these paths (or their prefixes) are handled by the control-plane
// branch at stage-1 and never reach the route table.
var reservedPaths = struct {
	exact    map[string]bool
	prefixes []string
}{
	exact: map[string]bool{
		"/docs":               true,
		"/docs/route":         true,
		"/openapi.json":       true,
		"/whoami":             true,
		"/scopes":             true,
		"/changes":            true,
		"/health/credentials": true,
		"/health/upstreams":   true,
		"/config/status":      true,
	},
	prefixes: []string{
		"/docs/",
		"/health/",
		"/config/",
		"/approvals/",
		"/_seam/",
	},
}

// isReservedPath checks if a given path is in the reserved control-plane set.
func isReservedPath(path string) bool {
	// Check exact matches first
	if reservedPaths.exact[path] {
		return true
	}
	// Check prefix matches
	for _, prefix := range reservedPaths.prefixes {
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// Config holds the server configuration
type Config struct {
	CallerPort   int
	OperatorPort int
	BaseURL      string
	SpecDir      string
}

// Server represents the SEAM gateway server with two listeners
type Server struct {
	config           *Config
	callerMux        *http.ServeMux
	operatorMux      *http.ServeMux
	callerServer     *http.Server
	operatorServer   *http.Server
	callerListener   net.Listener
	operatorListener net.Listener
	wg               sync.WaitGroup
}

// New creates a new Server with the given configuration
func New(cfg *Config) *Server {
	s := &Server{
		config:      cfg,
		callerMux:   http.NewServeMux(),
		operatorMux: http.NewServeMux(),
	}

	s.setupRoutes()
	return s
}

// setupRoutes configures the HTTP routes for both listeners
func (s *Server) setupRoutes() {
	// Caller-facing routes
	s.callerMux.HandleFunc("/_seam/healthz", s.healthzHandler)
	s.callerMux.HandleFunc("/_seam/readyz", s.readyzHandler)

	// Operator-only routes
	s.operatorMux.HandleFunc("/_seam/metrics", s.metricsHandler)
	s.operatorMux.HandleFunc("/config/status", s.configStatusHandler)
}

// healthzHandler returns 200 OK for liveness checks
func (s *Server) healthzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// readyzHandler returns readiness status
// In Phase 1a, this always returns ready=true.
// Phase 6a (bf-38bj) will add login dependency gating.
func (s *Server) readyzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]bool{"ready": true})
}

// metricsHandler returns Prometheus-style metrics
// In Phase 1a, exposes Go runtime/build basics.
// Phase 8 (bf-4ks8) will add per-route-version series.
func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// Go runtime metrics
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	fmt.Fprintf(w, "# HELP go_goroutines Number of goroutines that currently exist.\n")
	fmt.Fprintf(w, "# TYPE go_goroutines gauge\n")
	fmt.Fprintf(w, "go_goroutines %d\n\n", runtime.NumGoroutine())

	fmt.Fprintf(w, "# HELP go_memstats_alloc_bytes Number of bytes allocated and still in use.\n")
	fmt.Fprintf(w, "# TYPE go_memstats_alloc_bytes gauge\n")
	fmt.Fprintf(w, "go_memstats_alloc_bytes %d\n\n", m.Alloc)

	fmt.Fprintf(w, "# HELP go_memstats_alloc_bytes_total Total number of bytes allocated, even if freed.\n")
	fmt.Fprintf(w, "# TYPE go_memstats_alloc_bytes_total counter\n")
	fmt.Fprintf(w, "go_memstats_alloc_bytes_total %d\n\n", m.TotalAlloc)

	fmt.Fprintf(w, "# HELP go_memstats_sys_bytes Number of bytes obtained from system.\n")
	fmt.Fprintf(w, "# TYPE go_memstats_sys_bytes gauge\n")
	fmt.Fprintf(w, "go_memstats_sys_bytes %d\n\n", m.Sys)

	fmt.Fprintf(w, "# HELP go_threads Number of OS threads created.\n")
	fmt.Fprintf(w, "# TYPE go_threads gauge\n")
	fmt.Fprintf(w, "go_threads %d\n\n", runtime.NumCPU())

	// Build info
	fmt.Fprintf(w, "# HELP seam_build_info Build information about the seam binary.\n")
	fmt.Fprintf(w, "# TYPE seam_build_info gauge\n")
	fmt.Fprintf(w, "seam_build_info{version=\"dev\"} 1\n")
}

// configStatusHandler returns configuration fragment status
// In Phase 1a, this returns a quiescent/empty payload.
// Phase 1b (bf-5m2) will populate it with fragment quarantine status.
func (s *Server) configStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	status := struct {
		FragmentsLoaded bool     `json:"fragments_loaded"`
		Conditions      []string `json:"conditions"`
	}{
		FragmentsLoaded: false,
		Conditions:      []string{},
	}

	json.NewEncoder(w).Encode(status)
}

// Start begins listening on both ports
func (s *Server) Start(ctx context.Context) error {
	callerAddr := fmt.Sprintf(":%d", s.config.CallerPort)
	operatorAddr := fmt.Sprintf(":%d", s.config.OperatorPort)

	log.Printf("Starting caller-facing listener on %s", callerAddr)
	log.Printf("Starting operator-only listener on %s", operatorAddr)

	// Create listeners
	callerListener, err := net.Listen("tcp", callerAddr)
	if err != nil {
		return fmt.Errorf("failed to bind caller port %d: %w", s.config.CallerPort, err)
	}

	operatorListener, err := net.Listen("tcp", operatorAddr)
	if err != nil {
		callerListener.Close()
		return fmt.Errorf("failed to bind operator port %d: %w", s.config.OperatorPort, err)
	}

	// Store listeners for shutdown
	s.callerListener = callerListener
	s.operatorListener = operatorListener

	// Create servers
	s.callerServer = &http.Server{
		Handler: s.callerMux,
	}
	s.operatorServer = &http.Server{
		Handler: s.operatorMux,
	}

	// Start caller-facing server
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		log.Printf("Caller-facing server listening on :%d", s.config.CallerPort)
		err := s.callerServer.Serve(callerListener)
		if err != nil && err != http.ErrServerClosed {
			log.Printf("Caller-facing server error: %v", err)
		}
	}()

	// Start operator-only server
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		log.Printf("Operator-only server listening on :%d", s.config.OperatorPort)
		err := s.operatorServer.Serve(operatorListener)
		if err != nil && err != http.ErrServerClosed {
			log.Printf("Operator-only server error: %v", err)
		}
	}()

	return nil
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	log.Println("Shutting down server...")

	// Close the caller server
	if s.callerServer != nil {
		if err := s.callerServer.Shutdown(ctx); err != nil {
			log.Printf("Error shutting down caller server: %v", err)
		}
	}

	// Close the operator server
	if s.operatorServer != nil {
		if err := s.operatorServer.Shutdown(ctx); err != nil {
			log.Printf("Error shutting down operator server: %v", err)
		}
	}

	// Wait for all goroutines to finish
	s.wg.Wait()

	log.Println("Server shut down complete")
	return nil
}
