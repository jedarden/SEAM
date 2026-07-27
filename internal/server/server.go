package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
)

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

	// Operator-only routes (placeholder for now)
	s.operatorMux.HandleFunc("/_seam/metrics", s.metricsHandler)
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

// readyzHandler returns 200 OK for readiness checks (placeholder)
func (s *Server) readyzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// metricsHandler returns metrics (placeholder for now)
func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("# Metrics placeholder\n"))
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
