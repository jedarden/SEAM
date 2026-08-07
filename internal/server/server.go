package server

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"text/template"

	"github.com/ardenone/seam/internal/spec"
)

//go:embed redoc.html
var redocHTMLTemplate string

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
	CallerPort     int
	OperatorPort   int
	BaseURL        string
	SpecDir        string
	FragmentMode   bool
	SchemaPath     string
	CaptureEnabled bool
	CorpusDir      string
	FragmentsDir   string
}

// Server represents the SEAM gateway server with two listeners
type Server struct {
	config            *Config
	callerMux         *http.ServeMux
	operatorMux       *http.ServeMux
	callerServer      *http.Server
	operatorServer    *http.Server
	callerListener    net.Listener
	operatorListener  net.Listener
	wg                sync.WaitGroup
	specLoader        *spec.Loader
	captureMiddleware *CaptureMiddleware
}

// New creates a new Server with the given configuration
func New(cfg *Config) *Server {
	// Initialize the spec loader
	var specLoader *spec.Loader
	var err error

	if cfg.FragmentMode {
		specLoader, err = spec.NewWithFragments(cfg.SpecDir, cfg.BaseURL, cfg.SchemaPath, cfg.FragmentsDir)
		if err != nil {
			log.Fatalf("Failed to initialize spec loader in fragment mode: %v", err)
		}
		if cfg.FragmentsDir != "" {
			log.Printf("Loaded spec from fragments in %s", cfg.FragmentsDir)
		} else {
			log.Printf("Loaded spec from fragments in %s/fragments.d", cfg.SpecDir)
		}
	} else {
		specLoader, err = spec.New(cfg.SpecDir, cfg.BaseURL)
		if err != nil {
			log.Fatalf("Failed to initialize spec loader: %v", err)
		}
		log.Printf("Loaded spec from %s", cfg.SpecDir)
	}

	s := &Server{
		config:      cfg,
		callerMux:   http.NewServeMux(),
		operatorMux: http.NewServeMux(),
		specLoader:  specLoader,
	}

	// Initialize capture middleware if enabled
	if cfg.CaptureEnabled {
		corpusDir := cfg.CorpusDir
		if corpusDir == "" {
			corpusDir = "corpus"
		}
		s.captureMiddleware = NewCaptureMiddleware(corpusDir, "seam", "seam-incumbent", true)
		// Load existing corpus if present
		if err := s.captureMiddleware.Load(); err != nil {
			log.Printf("Warning: failed to load existing corpus: %v", err)
		}
		log.Printf("Capture middleware enabled, corpus directory: %s", corpusDir)
	}

	s.setupRoutes()
	return s
}

// setupRoutes configures the HTTP routes for both listeners
func (s *Server) setupRoutes() {
	// Caller-facing routes
	s.callerMux.HandleFunc("/_seam/health", s.healthzHandler)
	s.callerMux.HandleFunc("/_seam/healthz", s.healthzHandler)
	s.callerMux.HandleFunc("/_seam/readyz", s.readyzHandler)
	s.callerMux.HandleFunc("/openapi.json", s.openapiJSONHandler)
	s.callerMux.HandleFunc("/docs", s.docsHandler)
	s.callerMux.HandleFunc("/docs/route", s.docsRouteHandler)
	s.callerMux.HandleFunc("/docs/static/", s.staticAssetHandler)

	// Operator-only routes
	s.operatorMux.HandleFunc("/_seam/metrics", s.metricsHandler)
	s.operatorMux.HandleFunc("/config/status", s.configStatusHandler)
	s.operatorMux.HandleFunc("/_seam/capture/save", s.captureSaveHandler)
	s.operatorMux.HandleFunc("/_seam/capture/status", s.captureStatusHandler)
}

// healthzHandler returns 200 OK for liveness checks
func (s *Server) healthzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
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
	_ = json.NewEncoder(w).Encode(map[string]bool{"ready": true})
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

	_, _ = fmt.Fprintf(w, "# HELP go_goroutines Number of goroutines that currently exist.\n")
	_, _ = fmt.Fprintf(w, "# TYPE go_goroutines gauge\n")
	_, _ = fmt.Fprintf(w, "go_goroutines %d\n\n", runtime.NumGoroutine())

	_, _ = fmt.Fprintf(w, "# HELP go_memstats_alloc_bytes Number of bytes allocated and still in use.\n")
	_, _ = fmt.Fprintf(w, "# TYPE go_memstats_alloc_bytes gauge\n")
	_, _ = fmt.Fprintf(w, "go_memstats_alloc_bytes %d\n\n", m.Alloc)

	_, _ = fmt.Fprintf(w, "# HELP go_memstats_alloc_bytes_total Total number of bytes allocated, even if freed.\n")
	_, _ = fmt.Fprintf(w, "# TYPE go_memstats_alloc_bytes_total counter\n")
	_, _ = fmt.Fprintf(w, "go_memstats_alloc_bytes_total %d\n\n", m.TotalAlloc)

	_, _ = fmt.Fprintf(w, "# HELP go_memstats_sys_bytes Number of bytes obtained from system.\n")
	_, _ = fmt.Fprintf(w, "# TYPE go_memstats_sys_bytes gauge\n")
	_, _ = fmt.Fprintf(w, "go_memstats_sys_bytes %d\n\n", m.Sys)

	_, _ = fmt.Fprintf(w, "# HELP go_threads Number of OS threads created.\n")
	_, _ = fmt.Fprintf(w, "# TYPE go_threads gauge\n")
	_, _ = fmt.Fprintf(w, "go_threads %d\n\n", runtime.NumCPU())

	// Build info
	_, _ = fmt.Fprintf(w, "# HELP seam_build_info Build information about the seam binary.\n")
	_, _ = fmt.Fprintf(w, "# TYPE seam_build_info gauge\n")
	_, _ = fmt.Fprintf(w, "seam_build_info{version=\"dev\"} 1\n")
}

// configStatusHandler returns configuration fragment status
func (s *Server) configStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	// Get fragment status from the spec loader
	status := s.specLoader.GetFragmentStatus()
	_ = json.NewEncoder(w).Encode(status)
}

// captureSaveHandler manually saves the corpus
func (s *Server) captureSaveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.captureMiddleware == nil {
		http.Error(w, "Capture middleware not enabled", http.StatusServiceUnavailable)
		return
	}

	if err := s.captureMiddleware.Save(); err != nil {
		log.Printf("Failed to save corpus: %v", err)
		http.Error(w, "Failed to save corpus", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "saved",
		"entry_count": s.captureMiddleware.GetEntryCount(),
	})
}

// captureStatusHandler returns the current status of corpus capture
func (s *Server) captureStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	status := map[string]interface{}{
		"enabled":     s.captureMiddleware != nil && s.captureMiddleware.IsEnabled(),
		"entry_count": 0,
		"corpus_dir":  "",
	}

	if s.captureMiddleware != nil {
		status["entry_count"] = s.captureMiddleware.GetEntryCount()
		status["corpus_dir"] = s.captureMiddleware.corpusDir
	}

	_ = json.NewEncoder(w).Encode(status)
}

// openapiJSONHandler returns the OpenAPI spec as JSON
func (s *Server) openapiJSONHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get the spec JSON with servers populated
	specJSON, err := s.specLoader.GetRawJSON()
	if err != nil {
		log.Printf("[openapi.json] Failed to get spec JSON: %v", err)
		http.Error(w, "Failed to load spec", http.StatusInternalServerError)
		return
	}

	// Check if no fragments were loaded (fragment mode only)
	fragmentStatus := s.specLoader.GetFragmentStatus()
	if fragmentsLoaded, ok := fragmentStatus["fragments_loaded"].(bool); ok && fragmentsLoaded {
		if validCount, ok := fragmentStatus["valid_count"].(int); ok && validCount == 0 {
			log.Printf("[openapi.json] Warning: No valid fragments loaded, returning empty spec")
		}
	}

	// Set headers
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-SEAM-Spec-Version", s.specLoader.GetHash())
	w.Header().Set("X-Spec-Version", s.specLoader.GetHash())
	w.Header().Set("X-SEAM-API-Version", s.specLoader.GetAPIVersion())
	w.WriteHeader(http.StatusOK)

	_, _ = w.Write(specJSON)
}

// docsHandler returns the API documentation using ReDoc
func (s *Server) docsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check Accept header for content negotiation
	accept := r.Header.Get("Accept")

	// If client wants JSON, return the spec as JSON
	if contains(accept, "application/json") {
		s.openapiJSONHandler(w, r)
		return
	}

	// Render the ReDoc UI with embedded JavaScript
	html, err := s.renderReDocHTML()
	if err != nil {
		log.Printf("[docs] Failed to render ReDoc HTML: %v", err)
		http.Error(w, "Failed to render documentation", http.StatusInternalServerError)
		return
	}

	// Set headers
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-SEAM-Spec-Version", s.specLoader.GetHash())
	w.Header().Set("X-Spec-Version", s.specLoader.GetHash())
	w.Header().Set("X-SEAM-API-Version", s.specLoader.GetAPIVersion())
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(html)
}

// docsRouteHandler returns route documentation for a specific endpoint
// Query parameters:
//
//	path - the OpenAPI path template (required)
//	method - the HTTP method (optional, if omitted returns all methods)
//	version - the API version (optional, defaults to _unversioned)
func (s *Server) docsRouteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse query parameters
	query := r.URL.Query()
	path := query.Get("path")
	method := query.Get("method")
	version := query.Get("version")

	// Set default version
	if version == "" {
		version = "_unversioned"
	}

	// Validate required parameters
	if path == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "missing_required_parameter",
			"message": "The 'path' query parameter is required",
			"example": "/docs/route?path=/openapi.json&method=GET",
		})
		return
	}

	// Get route information from the spec loader
	routeInfo, err := s.specLoader.GetRoute(path, method, version)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "route_not_found",
			"message": fmt.Sprintf("Route not found: %s", err),
			"path":    path,
			"method":  method,
			"version": version,
		})
		return
	}

	// Build the response
	response := map[string]interface{}{
		"path":    routeInfo.Path,
		"version": routeInfo.Version,
		"metadata": map[string]interface{}{
			"description":  "Route documentation for SEAM API",
			"spec_version": s.specLoader.GetVersion(),
			"api_version":  s.specLoader.GetAPIVersion(),
		},
	}

	// If we have multiple methods (no specific method requested)
	if method == "" && len(routeInfo.Operations) > 0 {
		methods := map[string]interface{}{}
		for _, op := range routeInfo.Operations {
			methodData := map[string]interface{}{
				"summary":     op.Operation.Summary,
				"description": op.Operation.Description,
				"operationId": op.Operation.OperationId,
				"tags":        op.Operation.Tags,
			}

			// Add parameters if present
			if len(op.Operation.Parameters) > 0 {
				params := []map[string]interface{}{}
				for _, param := range op.Operation.Parameters {
					params = append(params, map[string]interface{}{
						"name":        param.Name,
						"in":          param.In,
						"description": param.Description,
						"required":    param.Required,
						"schema":      param.Schema,
					})
				}
				methodData["parameters"] = params
			}

			// Add request body if present
			if op.Operation.RequestBody != nil {
				methodData["request_body"] = map[string]interface{}{
					"description": op.Operation.RequestBody.Description,
					"required":    op.Operation.RequestBody.Required,
					"content":     op.Operation.RequestBody.Content,
				}
			}

			// Add responses if present
			if op.Operation.Responses != nil && op.Operation.Responses.Codes != nil {
				responses := map[string]interface{}{}
				for code, response := range op.Operation.Responses.Codes.FromOldest() {
					responses[code] = map[string]interface{}{
						"description": response.Description,
						"content":     response.Content,
					}
				}
				methodData["responses"] = responses
			}

			methods[op.Method] = methodData
		}
		response["methods"] = methods
	} else if routeInfo.Operation != nil {
		// Single method requested
		response["method"] = method
		methodData := map[string]interface{}{
			"method":      method,
			"summary":     routeInfo.Operation.Summary,
			"description": routeInfo.Operation.Description,
			"operationId": routeInfo.Operation.OperationId,
			"tags":        routeInfo.Operation.Tags,
		}

		// Add parameters if present
		if len(routeInfo.Operation.Parameters) > 0 {
			params := []map[string]interface{}{}
			for _, param := range routeInfo.Operation.Parameters {
				params = append(params, map[string]interface{}{
					"name":        param.Name,
					"in":          param.In,
					"description": param.Description,
					"required":    param.Required,
					"schema":      param.Schema,
				})
			}
			methodData["parameters"] = params
		}

		// Add request body if present
		if routeInfo.Operation.RequestBody != nil {
			methodData["request_body"] = map[string]interface{}{
				"description": routeInfo.Operation.RequestBody.Description,
				"required":    routeInfo.Operation.RequestBody.Required,
				"content":     routeInfo.Operation.RequestBody.Content,
			}
		}

		// Add responses if present
		if routeInfo.Operation.Responses != nil && routeInfo.Operation.Responses.Codes != nil {
			responses := map[string]interface{}{}
			for code, response := range routeInfo.Operation.Responses.Codes.FromOldest() {
				responses[code] = map[string]interface{}{
					"description": response.Description,
					"content":     response.Content,
				}
			}
			methodData["responses"] = responses
		}

		response["operation"] = methodData
	}

	// Set headers and return response
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-SEAM-Spec-Version", s.specLoader.GetVersion())
	w.Header().Set("X-Spec-Version", s.specLoader.GetHash())
	w.Header().Set("X-SEAM-API-Version", s.specLoader.GetAPIVersion())
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || indexOf(s, substr) >= 0))
}

// indexOf finds the index of a substring
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// renderReDocHTML renders the ReDoc documentation page
func (s *Server) renderReDocHTML() ([]byte, error) {
	tmpl, err := template.New("redoc").Parse(redocHTMLTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	buf := &bytes.Buffer{}
	data := struct {
		Title       string
		Description string
		SpecURL     string
	}{
		Title:       "SEAM API Documentation",
		Description: "SEAM (Semantic Endpoint Access and Management) is an API gateway that provides OpenAPI 3.1 specification validation, route fragment merging, and request/response validation.",
		SpecURL:     "/openapi.json",
	}

	if err := tmpl.Execute(buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.Bytes(), nil
}

// staticAssetHandler serves static assets (CSS, JS) for the docs UI
func (s *Server) staticAssetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract the filename from the path
	// Path format: /docs/static/filename.ext
	requestPath := r.URL.Path
	if !strings.HasPrefix(requestPath, "/docs/static/") {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	filename := strings.TrimPrefix(requestPath, "/docs/static/")

	// Security: prevent directory traversal
	if strings.Contains(filename, "..") || strings.Contains(filename, "\\") {
		http.Error(w, "Invalid path", http.StatusForbidden)
		return
	}

	// Determine content type based on file extension
	var contentType string
	switch {
	case strings.HasSuffix(filename, ".css"):
		contentType = "text/css; charset=utf-8"
	case strings.HasSuffix(filename, ".js"):
		contentType = "application/javascript; charset=utf-8"
	case strings.HasSuffix(filename, ".json"):
		contentType = "application/json; charset=utf-8"
	case strings.HasSuffix(filename, ".png"):
		contentType = "image/png"
	case strings.HasSuffix(filename, ".jpg"), strings.HasSuffix(filename, ".jpeg"):
		contentType = "image/jpeg"
	case strings.HasSuffix(filename, ".svg"):
		contentType = "image/svg+xml"
	case strings.HasSuffix(filename, ".woff"):
		contentType = "font/woff"
	case strings.HasSuffix(filename, ".woff2"):
		contentType = "font/woff2"
	case strings.HasSuffix(filename, ".ttf"):
		contentType = "font/ttf"
	case strings.HasSuffix(filename, ".eot"):
		contentType = "application/vnd.ms-fontobject"
	default:
		contentType = "application/octet-stream"
	}

	// Try to read the file from the assets directory
	content, err := readAssetFile(filename)
	if err != nil {
		log.Printf("[static] Failed to read asset %s: %v", filename, err)
		http.Error(w, "Asset not found", http.StatusNotFound)
		return
	}

	// Set caching headers for better performance
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400") // Cache for 1 day
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write(content); err != nil {
		log.Printf("[static] Error writing asset %s: %v", filename, err)
	}
}

// readAssetFile reads a file from the assets directory
func readAssetFile(filename string) ([]byte, error) {
	// In production, this could use go:embed for assets
	// For now, read from the filesystem
	return readFromFile(filename)
}

// readFromFile reads a file from the filesystem
func readFromFile(filepath string) ([]byte, error) {
	file, err := http.Dir("internal/server/assets").Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return io.ReadAll(file)
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
		_ = callerListener.Close()
		return fmt.Errorf("failed to bind operator port %d: %w", s.config.OperatorPort, err)
	}

	// Store listeners for shutdown
	s.callerListener = callerListener
	s.operatorListener = operatorListener

	// Wrap caller mux with validation middleware (always active for caller port)
	callerHandler := s.validationMiddleware(s.callerMux)
	log.Printf("Validation middleware active on caller-facing port")

	// Wrap with capture middleware if enabled
	if s.captureMiddleware != nil {
		callerHandler = s.captureMiddleware.Wrap(callerHandler)
		log.Printf("Capture middleware active on caller-facing port")
	}

	// Create servers
	s.callerServer = &http.Server{
		Handler: callerHandler,
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

	// Save corpus before shutdown if capture is enabled
	if s.captureMiddleware != nil && s.captureMiddleware.IsEnabled() {
		if err := s.captureMiddleware.Save(); err != nil {
			log.Printf("Failed to save corpus on shutdown: %v", err)
		} else {
			log.Printf("Corpus saved successfully (%d entries)", s.captureMiddleware.GetEntryCount())
		}
	}

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
