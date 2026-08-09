package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/ardenone/seam/internal/spec"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// reservedPaths are the control-plane endpoints that short-circuit route-table lookup.
// Requests matching these paths (or their prefixes) are handled by the control-plane
// branch at stage-1 and never reach the route table.
//
// Health Sentinel Integration:
// These paths bypass BOTH caching and quota enforcement to ensure infrastructure
// health checks operate reliably without consuming quota or polluting the cache.
// Health probes from Kubernetes, load balancers, and orchestrators expect instant
// responses and should never be rate-limited or cached.
//
// Paths with /health/ prefix are health sentinel probes (liveness/readiness checks).
// Paths with /_seam/ prefix are internal SEAM control plane endpoints.
// Both categories are excluded from:
//   - Cache middleware (no cache lookup, no cache storage)
//   - Quota middleware (no quota checking, no cost deduction)
//   - Metrics (no cache/quota metrics recorded for these paths)
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
		"/health/credentials": true, // Health sentinel: credential status check
		"/health/upstreams":   true, // Health sentinel: upstream connectivity check
		"/config/status":      true,
	},
	prefixes: []string{
		"/health/",     // Health sentinel: all health check endpoints
		"/config/",     // Configuration management endpoints
		"/approvals/",  // Approval workflow endpoints
		"/_seam/",      // Internal SEAM endpoints (metrics, health, ready)
	},
}

// isReservedPath checks if a given path is in the reserved control-plane set.
//
// Health Sentinel and Cache/Quota Integration:
// This function is the central gatekeeper for determining whether a request
// bypasses caching and quota enforcement. It is called by both the cache
// middleware and quota middleware to identify health sentinel probes and
// other control-plane traffic.
//
// Returns true for:
//   - Health sentinel probes (/_seam/health, /_seam/healthz, /_seam/readyz, /health/*)
//   - Control plane endpoints (/docs, /openapi.json, /config/*, /_seam/*)
//
// Integration points:
//   - Cache middleware: Bypasses cache lookup and storage for reserved paths
//   - Quota middleware: Bypasses quota checking and cost deduction for reserved paths
//   - Metrics: No cache or quota metrics are recorded for reserved paths
//
// For cache hit bypass logic (different from reserved path bypass), see quota middleware.
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
	cache             *ResponseCache
	singleFlight      *SingleFlight
	cacheTTLs         map[string]int // route path -> cache TTL in seconds
	quotaTracker      *QuotaTracker
	costPerCalls      map[string]float64 // route -> cost per call
	mu                sync.RWMutex
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
		config:       cfg,
		callerMux:    http.NewServeMux(),
		operatorMux:  http.NewServeMux(),
		specLoader:   specLoader,
		cache:        NewResponseCache(),
		singleFlight: NewSingleFlight(),
		cacheTTLs:    make(map[string]int),
		quotaTracker: NewQuotaTracker(),
		costPerCalls: make(map[string]float64),
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

	// Load cache TTL configuration from fragments
	s.cacheTTLs = s.specLoader.GetCacheTTLs()
	log.Printf("Loaded %d route cache TTL configurations", len(s.cacheTTLs))

	s.setupRoutes()
	return s
}

// loadCacheTTLs extracts cache TTL configuration from loaded fragments
func (s *Server) loadCacheTTLs() {
	if !s.config.FragmentMode {
		return
	}

	// Access fragment loader to get cache TTL settings
	// This will be called after fragment loading
	// For now, we'll extract this from the merged spec
	log.Printf("[Cache] Loading cache TTL configuration from fragments")
}

// setupRoutes configures the HTTP routes for both listeners
func (s *Server) setupRoutes() {
	// Caller-facing routes
	s.callerMux.HandleFunc("/_seam/health", s.healthzHandler)
	s.callerMux.HandleFunc("/_seam/healthz", s.healthzHandler)
	s.callerMux.HandleFunc("/_seam/readyz", s.readyzHandler)
	s.callerMux.HandleFunc("/openapi.json", s.openapiJSONHandler)

	// Setup docs handler - fetches spec internally and serves ReDoc UI
	s.callerMux.HandleFunc("/docs", s.docsHandler)

	s.callerMux.HandleFunc("/docs/route", s.docsRouteHandler)

	// Operator-only routes
	s.operatorMux.HandleFunc("/_seam/metrics", s.metricsHandler)
	s.operatorMux.HandleFunc("/config/status", s.configStatusHandler)
	s.operatorMux.HandleFunc("/_seam/capture/save", s.captureSaveHandler)
	s.operatorMux.HandleFunc("/_seam/capture/status", s.captureStatusHandler)
	s.operatorMux.HandleFunc("/_seam/cache/status", s.cacheStatusHandler)
	s.operatorMux.HandleFunc("/_seam/cache/cleanup", s.cacheCleanupHandler)
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
// Exposes Go runtime metrics, cache metrics (hits, misses, hit rate, size, evictions),
// and quota metrics via the Prometheus handler.
func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Use Prometheus handler to expose all registered metrics
	// This includes cache metrics (seam_cache_hits_total, seam_cache_misses_total, etc.)
	// and quota metrics (seam_quota_cost_total, seam_quota_remaining, etc.)
	promhttp.Handler().ServeHTTP(w, r)
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

// docsHandler serves the OpenAPI documentation UI with embedded spec
// Fetches the merged OpenAPI spec from the spec loader and serves it with Scalar API Reference
func (s *Server) docsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Fetch the merged OpenAPI spec from internal spec loader
	specJSON, err := s.specLoader.GetRawJSON()
	if err != nil {
		log.Printf("[/docs] Failed to fetch merged spec: %v", err)
		http.Error(w, "Failed to load API specification", http.StatusInternalServerError)
		return
	}

	// Validate that the spec is valid JSON
	var specJSONCheck interface{}
	if err := json.Unmarshal(specJSON, &specJSONCheck); err != nil {
		log.Printf("[/docs] Spec validation failed - invalid JSON: %v", err)
		http.Error(w, "API specification is not valid JSON", http.StatusInternalServerError)
		return
	}

	log.Printf("[/docs] Successfully fetched and validated merged spec (%d bytes)", len(specJSON))

	// Check if no fragments were loaded (fragment mode only) - log warning
	fragmentStatus := s.specLoader.GetFragmentStatus()
	if fragmentsLoaded, ok := fragmentStatus["fragments_loaded"].(bool); ok && fragmentsLoaded {
		if validCount, ok := fragmentStatus["valid_count"].(int); ok && validCount == 0 {
			log.Printf("[/docs] Warning: No valid fragments loaded, serving empty spec")
		}
	}

	// Serve the documentation UI with the spec embedded
	// Using Scalar API Reference with the spec embedded in a script tag
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-SEAM-Spec-Version", s.specLoader.GetHash())
	w.Header().Set("X-Spec-Version", s.specLoader.GetHash())
	w.Header().Set("X-SEAM-API-Version", s.specLoader.GetAPIVersion())
	w.WriteHeader(http.StatusOK)

	// Embed the spec JSON in the HTML page
	specJSONEscaped := string(specJSON)
	// Escape the JSON for safe embedding in HTML script tag
	specJSONEscaped = strings.ReplaceAll(specJSONEscaped, "</script>", `<\/script>`)

	html := `<!DOCTYPE html>
<html>
<head>
  <title>SEAM API Documentation</title>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    body { margin: 0; padding: 0; }
    #scalar-app { height: 100vh; }
  </style>
</head>
<body>
  <div id="scalar-app"></div>
  <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
  <script>
    var specData = ` + specJSONEscaped + `;
    Scalar.createApiReference('#scalar-app', {
      spec: {
        content: specData
      },
      theme: 'default',
      metaData: {
        title: 'SEAM API Documentation',
        description: 'Interactive API documentation for SEAM Gateway'
      }
    });
  </script>
</body>
</html>`

	_, _ = w.Write([]byte(html))
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

	// Start cache cleanup goroutine
	s.startCacheCleanup()
	log.Printf("Cache cleanup goroutine started")

	// Wrap caller mux with quota middleware (inner layer)
	callerHandler := s.quotaMiddleware(s.callerMux)
	log.Printf("Quota middleware active on caller-facing port")

	// Wrap with cache middleware (outer layer - checks cache first, bypasses quota on hits)
	callerHandler = s.cacheMiddleware(callerHandler)
	log.Printf("Cache middleware active on caller-facing port")

	// Wrap with validation middleware (always active for caller port)
	callerHandler = s.validationMiddleware(callerHandler)
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

// cacheStatusHandler returns cache statistics
func (s *Server) cacheStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	stats := s.cache.Stats()
	sfStats := s.singleFlight.Stats()

	// Calculate hit rate safely (avoid division by zero)
	hitRate := 0.0
	total := stats.Hits + stats.Misses
	if total > 0 {
		hitRate = float64(stats.Hits) / float64(total)
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled":     true,
		"size":        stats.Size,
		"hits":        stats.Hits,
		"misses":      stats.Misses,
		"evictions":   stats.Evictions,
		"hit_rate":    hitRate,
		"routes_with_cache": len(s.cacheTTLs),
		"single_flight": map[string]interface{}{
			"active_requests": sfStats.ActiveRequests,
			"total_calls":     sfStats.TotalCalls,
			"deduped_calls":   sfStats.DedupedCalls,
			"coalesce_rate":   sfStats.CoalesceRate,
		},
	})
}

// cacheCleanupHandler manually triggers cache cleanup
func (s *Server) cacheCleanupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.cache.Cleanup()

	// Update metrics after cleanup
	stats := s.cache.Stats()
	updateCacheMetrics(stats)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "cleanup_complete",
		"size":        stats.Size,
		"evictions":   stats.Evictions,
	})
}
