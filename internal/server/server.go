package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ardenone/seam/internal/spec"
	"github.com/ardenone/seam/internal/vault"
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
		"/docs/",      // Documentation endpoints (reserved namespace)
		"/health/",    // Health sentinel: all health check endpoints
		"/config/",    // Configuration management endpoints
		"/approvals/", // Approval workflow endpoints
		"/_seam/",     // Internal SEAM endpoints (metrics, health, ready)
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
//
// SERVER URL SOURCE:
// The BaseURL field comes from runtime configuration:
//   - CLI flag: -base-url (default: "http://localhost:8080")
//   - Environment variable: SEAM_BASE_URL (overrides CLI flag if set)
//
// This URL is passed to the spec loader and used to populate the OpenAPI spec's
// servers array. It should be the externally-accessible caller-facing endpoint URL.
//
// Example values:
//   - Development: "http://localhost:8080"
//   - Production: "https://api.example.com"
//   - Behind proxy: "https://seam.example.com"
//
// The URL is synthesized into the spec at runtime, not read from spec files,
// allowing the same spec fragments to be served from different environments.
type Config struct {
	CallerPort                int
	OperatorPort              int
	BaseURL                   string // Caller-facing endpoint URL (from SEAM_BASE_URL or -base-url flag)
	SpecDir                   string
	FragmentMode              bool
	SchemaPath                string
	CaptureEnabled            bool
	CorpusDir                 string
	FragmentsDir              string
	UpstreamURL               string // Default upstream URL for proxying (Phase 2.0: single upstream)
	UpstreamCADir             string // Directory for upstream CA bundles (default: /etc/gateway/upstream-ca, local dev: --upstream-ca-dir)
	AllowlistFile             string // Path to upstream host allowlist file (optional, for dev mode)
	VaultBaseDir              string // Base directory for vault path validation (default: "seam/routes")
	MaxReplayableRequestBytes int64  // Phase 2.5: Max inbound request body size to buffer for replay (default 1 MiB, independent knob)
	MaxBufferedResponseBytes  int64  // Phase 2.6: Max decoded response body to hold for whole-response scrubbing (default 1 MiB, independent knob)
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
	routeTable        *RouteTable              // Route table for request matching (stage 4)
	proxyMap          map[string]*ReverseProxy // Map of upstream URL -> proxy instance (stages 6-11)
	proxyMapMu        sync.RWMutex             // Protects proxyMap
	captureMiddleware *CaptureMiddleware
	cache             *ResponseCache
	singleFlight      *SingleFlight
	cacheTTLs         map[string]int // route path -> cache TTL in seconds
	circuitBreakers   *CircuitBreakerStateRegistry
	quotaTracker      *QuotaTracker
	costPerCalls      map[string]float64 // route -> cost per call
	mu                sync.RWMutex
	allowlistEnforcer *spec.AllowlistEnforcer // Allowlist enforcer for vault-path and upstream-host validation
	openBaoMu         sync.RWMutex
	openBaoReady      bool
}

// New creates a new Server with the given configuration
func New(cfg *Config) *Server {
	if cfg.MaxBufferedResponseBytes <= 0 {
		cfg.MaxBufferedResponseBytes = DefaultMaxBufferedResponseBytes
	}
	// Initialize the spec loader
	var specLoader *spec.Loader
	var err error

	// Set default vault base directory
	vaultBaseDir := cfg.VaultBaseDir
	if vaultBaseDir == "" {
		vaultBaseDir = "seam/routes"
	}

	// Initialize allowlist enforcer
	allowlistEnforcer, err := spec.NewAllowlistEnforcer(vaultBaseDir, cfg.AllowlistFile)
	if err != nil {
		log.Printf("Warning: Failed to initialize allowlist enforcer: %v", err)
		// Continue without allowlist - will be fail-closed
		allowlistEnforcer = nil
	}

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

		// Configure allowlist enforcer for fragment loader
		if allowlistEnforcer != nil && specLoader.FragmentLoader != nil {
			specLoader.FragmentLoader.SetAllowlistEnforcer(allowlistEnforcer)
			log.Printf("Allowlist enforcer configured for fragment validation")
		}
	} else {
		specLoader, err = spec.New(cfg.SpecDir, cfg.BaseURL)
		if err != nil {
			log.Fatalf("Failed to initialize spec loader: %v", err)
		}
		log.Printf("Loaded spec from %s", cfg.SpecDir)
	}

	s := &Server{
		config:            cfg,
		callerMux:         http.NewServeMux(),
		operatorMux:       http.NewServeMux(),
		specLoader:        specLoader,
		routeTable:        NewRouteTable(specLoader),
		proxyMap:          make(map[string]*ReverseProxy),
		cache:             NewResponseCache(),
		singleFlight:      NewSingleFlight(),
		cacheTTLs:         make(map[string]int),
		circuitBreakers:   NewCircuitBreakerStateRegistry(),
		quotaTracker:      NewQuotaTracker(),
		costPerCalls:      make(map[string]float64),
		allowlistEnforcer: allowlistEnforcer,
		openBaoReady:      true,
	}
	log.Printf("Route table initialized with %d routes", s.routeTable.RouteCount())

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
//
//nolint:unused // Stub retained for the fragment-mode TTL work; no caller yet.
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

	// Catch-all dispatch handler for upstream proxying (Phase 2.0)
	// This must be registered last so it doesn't intercept the specific handlers above
	s.callerMux.HandleFunc("/", s.dispatchHandler)

	// Operator-only routes
	s.operatorMux.HandleFunc("/_seam/metrics", s.metricsHandler)
	s.operatorMux.HandleFunc("/config/status", s.configStatusHandler)
	s.operatorMux.HandleFunc("/_seam/capture/save", s.captureSaveHandler)
	s.operatorMux.HandleFunc("/_seam/capture/status", s.captureStatusHandler)
	s.operatorMux.HandleFunc("/_seam/cache/status", s.cacheStatusHandler)
	s.operatorMux.HandleFunc("/_seam/cache/cleanup", s.cacheCleanupHandler)
	s.operatorMux.HandleFunc("/health/credentials", s.credentialsHealthHandler)
}

// healthzHandler returns 200 OK for liveness checks
func (s *Server) healthzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// readyzHandler returns readiness status
// Returns ready=false when allowlist is in fail-closed state (no hosts permitted)
// Phase 2.2: Allowlist enforcement gating
func (s *Server) readyzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	// Check allowlist status
	ready := true
	if s.allowlistEnforcer != nil && s.allowlistEnforcer.IsFailClosed() {
		ready = false
	}
	if !s.isOpenBaoReady() {
		ready = false
	}

	statusCode := http.StatusOK
	if !ready {
		statusCode = http.StatusServiceUnavailable
	}

	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]bool{"ready": ready})
}

// metricsHandler returns Prometheus-style metrics
// Exposes Go runtime metrics, cache metrics (hits, misses, hit rate, size, evictions),
// and quota metrics via the Prometheus handler.
func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}

	// Use Prometheus handler to expose all registered metrics
	// This includes cache metrics (seam_cache_hits_total, seam_cache_misses_total, etc.)
	// and quota metrics (seam_quota_cost_total, seam_quota_remaining, etc.)
	promhttp.Handler().ServeHTTP(w, r)
}

// configStatusHandler returns comprehensive runtime configuration status
// Returns: current configuration values, spec hash, corpus status, enabled route count, and health status
func (s *Server) configStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(s.runtimeConfigStatus())
}

// captureSaveHandler manually saves the corpus
func (s *Server) captureSaveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed("Only POST method is allowed").Write(w, r)
		return
	}

	if s.captureMiddleware == nil {
		NewErrorResponse(ErrCodeServiceUnavailable, "Capture middleware not enabled").Write(w, r)
		return
	}

	if err := s.captureMiddleware.Save(); err != nil {
		log.Printf("Failed to save corpus: %v", err)
		NewErrorResponse(ErrCodeInternalServer, "Failed to save corpus").WithDetail("error", err.Error()).Write(w, r)
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
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
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
//
// Query parameters:
//
//	version - the API version (optional, defaults to _unversioned)
func (s *Server) openapiJSONHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}

	// Parse and validate version parameter
	query := r.URL.Query()
	version := query.Get("version")

	// Set default version
	if version == "" {
		version = "_unversioned"
	}

	// Validate version parameter - only "_unversioned" is accepted in Phase 1a
	if version != "_unversioned" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-SEAM-Spec-Version", s.specLoader.GetHash())
		w.Header().Set("X-SEAM-API-Version", s.specLoader.GetAPIVersion())
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":            "invalid_version_parameter",
			"message":          fmt.Sprintf("Invalid version parameter: %s. Only _unversioned is supported in Phase 1a.", version),
			"expected_version": "_unversioned",
			"actual_version":   version,
			"docs_url":         "/docs",
		})
		return
	}

	// Get the spec JSON with servers populated
	specJSON, err := s.specLoader.GetRawJSON()
	if err != nil {
		log.Printf("[openapi.json] Failed to get spec JSON: %v", err)
		NewErrorResponse(ErrCodeSpecLoadFailed, "Failed to load spec").WithDetail("error", err.Error()).Write(w, r)
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
//
// Query parameters:
//
//	version - the API version (optional, defaults to _unversioned)
func (s *Server) docsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}

	// Parse and validate version parameter
	query := r.URL.Query()
	version := query.Get("version")

	// Set default version
	if version == "" {
		version = "_unversioned"
	}

	// Validate version parameter - only "_unversioned" is accepted in Phase 1a
	if version != "_unversioned" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-SEAM-Spec-Version", s.specLoader.GetHash())
		w.Header().Set("X-SEAM-API-Version", s.specLoader.GetAPIVersion())
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":            "invalid_version_parameter",
			"message":          fmt.Sprintf("Invalid version parameter: %s. Only _unversioned is supported in Phase 1a.", version),
			"expected_version": "_unversioned",
			"actual_version":   version,
			"docs_url":         "/docs",
		})
		return
	}

	// Fetch the merged OpenAPI spec from internal spec loader
	specJSON, err := s.specLoader.GetRawJSON()
	if err != nil {
		log.Printf("[/docs] Failed to fetch merged spec: %v", err)
		NewErrorResponse(ErrCodeSpecLoadFailed, "Failed to load API specification").WithDetail("error", err.Error()).Write(w, r)
		return
	}

	// Validate that the spec is valid JSON
	var specJSONCheck interface{}
	if err := json.Unmarshal(specJSON, &specJSONCheck); err != nil {
		log.Printf("[/docs] Spec validation failed - invalid JSON: %v", err)
		NewErrorResponse(ErrCodeSpecLoadFailed, "API specification is not valid JSON").WithDetail("error", err.Error()).Write(w, r)
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

	// Check Accept header for content negotiation
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") {
		// Return raw OpenAPI spec as JSON
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-SEAM-Spec-Version", s.specLoader.GetHash())
		w.Header().Set("X-Spec-Version", s.specLoader.GetHash())
		w.Header().Set("X-SEAM-API-Version", s.specLoader.GetAPIVersion())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(specJSON)
		return
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
      spec: specData,
      theme: 'default',
      metaData: {
        title: 'SEAM API Documentation',
        description: 'Interactive API documentation for SEAM Gateway'
      },
      // Enable interactive features
      isEditable: false,
      hideTryIt: false,
      tryItCorsProxy: false,
      showSidebar: true,
      matchPaths: [],
      allowedLabels: [],
      baseServerURL: '',
      defaultShowAllExamples: true,
      darkMode: false,
      layout: 'classic',
      search: {
        open: true
      },
      seo: {
        title: 'SEAM API Documentation',
        description: 'Interactive API documentation for SEAM Gateway'
      },
      routing: {
        basePath: '/docs'
      }
    });
  </script>
</body>
</html>`

	_, _ = w.Write([]byte(html))
}

// docsRouteHandler returns the route slice of the served OpenAPI document.
// Query parameters:
//
//	path - the OpenAPI path template (required)
//	method - the HTTP method (optional; omitted means every method on path)
//	version - the API version (optional, defaults to _unversioned)
func (s *Server) docsRouteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}

	// Check Accept header for HTML redirect
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "text/html") {
		// Generate route anchor and redirect to /docs
		query := r.URL.Query()
		path := query.Get("path")
		method := query.Get("method")

		// Create route anchor identifier
		// Format: /path/method -> path-method (e.g., /test/get -> test-get)
		routeAnchor := strings.TrimPrefix(path, "/")
		routeAnchor = strings.ReplaceAll(routeAnchor, "/", "-")
		if method != "" {
			routeAnchor = routeAnchor + "-" + strings.ToLower(method)
		}

		redirectURL := "/docs#" + routeAnchor
		http.Redirect(w, r, redirectURL, http.StatusFound)
		return
	}

	// Parse query parameters
	query := r.URL.Query()
	path := query.Get("path")
	method := strings.ToUpper(strings.TrimSpace(query.Get("method")))
	version := query.Get("version")

	// Set default version
	if version == "" {
		version = "_unversioned"
	}

	// Validate version parameter - only "_unversioned" is accepted in Phase 1a
	if version != "_unversioned" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-SEAM-Spec-Version", s.specLoader.GetHash())
		w.Header().Set("X-SEAM-API-Version", s.specLoader.GetAPIVersion())
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":            "invalid_version_parameter",
			"message":          fmt.Sprintf("Invalid version parameter: %s. Only _unversioned is supported in Phase 1a.", version),
			"expected_version": "_unversioned",
			"actual_version":   version,
			"docs_url":         "/docs",
		})
		return
	}

	// Validate required parameters
	if path == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "missing_required_parameter",
			"message": "The 'path' query parameter is required",
			"example": "/docs/route?path=/test/get&method=GET",
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

	// Read the same served document that /openapi.json returns. Using its raw
	// path item keeps request/response schemas, examples, and x-* annotations
	// intact instead of rebuilding a lossy approximation from the high-level
	// model.
	specJSON, err := s.specLoader.GetRawJSON()
	if err != nil {
		NewErrorResponse(ErrCodeSpecLoadFailed, "Failed to load API specification").WithDetail("error", err.Error()).Write(w, r)
		return
	}
	var document map[string]interface{}
	if err := json.Unmarshal(specJSON, &document); err != nil {
		NewErrorResponse(ErrCodeSpecLoadFailed, "API specification is not valid JSON").WithDetail("error", err.Error()).Write(w, r)
		return
	}
	pathItems, ok := document["paths"].(map[string]interface{})
	if !ok {
		NewErrorResponse(ErrCodeSpecLoadFailed, "API specification has no paths").Write(w, r)
		return
	}
	pathItem, ok := pathItems[path].(map[string]interface{})
	if !ok {
		NewErrorResponse(ErrCodeSpecLoadFailed, "API specification route is not an object").Write(w, r)
		return
	}

	response := map[string]interface{}{
		"path":                           routeInfo.Path,
		"version":                        routeInfo.Version,
		"isDefaultForUnversionedCallers": true,
		"metadata": map[string]interface{}{
			"description":  "Route documentation for SEAM API",
			"spec_version": s.specLoader.GetVersion(),
			"api_version":  s.specLoader.GetAPIVersion(),
		},
	}

	if parameters, ok := pathItem["parameters"]; ok {
		response["parameters"] = parameters
	}

	var exampleMethod string
	var exampleOperation map[string]interface{}
	// If we have multiple methods (no specific method requested), return the
	// complete operation objects keyed by their HTTP method.
	if method == "" && len(routeInfo.Operations) > 0 {
		methods := map[string]interface{}{}
		for _, op := range routeInfo.Operations {
			methodData, found := rawRouteOperation(pathItem, op.Method)
			if !found {
				continue
			}
			methodData["method"] = op.Method
			methods[op.Method] = methodData
			if exampleOperation == nil {
				exampleMethod = op.Method
				exampleOperation = methodData
			}
		}
		response["methods"] = methods
	} else if routeInfo.Operation != nil {
		// Single method requested
		response["method"] = method
		methodData, found := rawRouteOperation(pathItem, method)
		if !found {
			NewErrorResponse(ErrCodeSpecLoadFailed, "API specification route operation is missing").Write(w, r)
			return
		}
		methodData["method"] = method
		response["operation"] = methodData
		exampleMethod = method
		exampleOperation = methodData
	}

	response["annotations"] = routeAnnotations(pathItem, exampleOperation)
	response["example"] = buildWorkedExample(path, exampleMethod, pathItem, exampleOperation, document)

	// Set headers and return response
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-SEAM-Spec-Version", s.specLoader.GetHash())
	w.Header().Set("X-Spec-Version", s.specLoader.GetHash())
	w.Header().Set("X-SEAM-API-Version", s.specLoader.GetAPIVersion())
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func rawRouteOperation(pathItem map[string]interface{}, method string) (map[string]interface{}, bool) {
	operation, ok := pathItem[strings.ToLower(method)].(map[string]interface{})
	if !ok {
		return nil, false
	}
	clone := make(map[string]interface{}, len(operation)+1)
	for key, value := range operation {
		clone[key] = value
	}
	return clone, true
}

func routeAnnotations(pathItem, operation map[string]interface{}) map[string]interface{} {
	annotations := make(map[string]interface{})
	for _, source := range []map[string]interface{}{pathItem, operation} {
		for key, value := range source {
			if strings.HasPrefix(strings.ToLower(key), "x-") {
				annotations[key] = value
			}
		}
	}
	return annotations
}

func buildWorkedExample(path, method string, pathItem, operation, document map[string]interface{}) map[string]interface{} {
	example := map[string]interface{}{
		"method": method,
		"path":   path,
	}
	if operation == nil {
		return example
	}

	components, _ := document["components"].(map[string]interface{})
	parameters := append([]interface{}{}, toInterfaceSlice(pathItem["parameters"])...)
	parameters = append(parameters, toInterfaceSlice(operation["parameters"])...)
	query := make(map[string]interface{})
	headers := make(map[string]interface{})
	pathValues := make(map[string]string)
	for _, rawParameter := range parameters {
		parameter, ok := rawParameter.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := parameter["name"].(string)
		location, _ := parameter["in"].(string)
		if name == "" {
			continue
		}
		value := parameterExample(parameter, components)
		switch location {
		case "path":
			pathValues[name] = fmt.Sprint(value)
		case "query":
			query[name] = value
		case "header":
			headers[name] = value
		}
	}

	for name, value := range pathValues {
		path = strings.ReplaceAll(path, "{"+name+"}", value)
	}
	example["path"] = path
	if len(query) > 0 {
		example["query"] = query
	}

	requestBody, _ := operation["requestBody"].(map[string]interface{})
	if requestBody != nil {
		content, _ := requestBody["content"].(map[string]interface{})
		mediaType, media, ok := firstMapEntry(content)
		if ok {
			headers["Content-Type"] = mediaType
			exampleValue := media["example"]
			if exampleValue == nil {
				exampleValue = firstNamedExampleValue(media["examples"])
			}
			if exampleValue == nil {
				exampleValue = exampleFromSchema(media["schema"], components, make(map[string]bool), 0)
			}
			if exampleValue != nil {
				example["body"] = exampleValue
			}
		}
	}
	if len(headers) > 0 {
		example["headers"] = headers
	}
	return example
}

func toInterfaceSlice(value interface{}) []interface{} {
	values, _ := value.([]interface{})
	return values
}

func parameterExample(parameter map[string]interface{}, components map[string]interface{}) interface{} {
	if value, ok := parameter["example"]; ok {
		return value
	}
	if value := firstNamedExampleValue(parameter["examples"]); value != nil {
		return value
	}
	if value := exampleFromSchema(parameter["schema"], components, make(map[string]bool), 0); value != nil {
		return value
	}
	return "example"
}

func firstNamedExampleValue(value interface{}) interface{} {
	examples, ok := value.(map[string]interface{})
	if !ok || len(examples) == 0 {
		return nil
	}
	keys := make([]string, 0, len(examples))
	for key := range examples {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	selected, _ := examples[keys[0]].(map[string]interface{})
	if selected != nil {
		if example, ok := selected["value"]; ok {
			return example
		}
	}
	return examples[keys[0]]
}

func firstMapEntry(value map[string]interface{}) (string, map[string]interface{}, bool) {
	if len(value) == 0 {
		return "", nil, false
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entry, _ := value[keys[0]].(map[string]interface{})
	if entry == nil {
		return "", nil, false
	}
	return keys[0], entry, true
}

func exampleFromSchema(value interface{}, components map[string]interface{}, seen map[string]bool, depth int) interface{} {
	if depth > 6 {
		return nil
	}
	schema, ok := value.(map[string]interface{})
	if !ok || schema == nil {
		return nil
	}
	if example, ok := schema["example"]; ok {
		return example
	}
	if defaultValue, ok := schema["default"]; ok {
		return defaultValue
	}
	if enum, ok := schema["enum"].([]interface{}); ok && len(enum) > 0 {
		return enum[0]
	}
	if ref, ok := schema["$ref"].(string); ok {
		const prefix = "#/components/schemas/"
		if strings.HasPrefix(ref, prefix) && !seen[ref] {
			seen[ref] = true
			if schemas, ok := components["schemas"].(map[string]interface{}); ok {
				return exampleFromSchema(schemas[strings.TrimPrefix(ref, prefix)], components, seen, depth+1)
			}
		}
	}
	if properties, ok := schema["properties"].(map[string]interface{}); ok {
		object := make(map[string]interface{}, len(properties))
		keys := make([]string, 0, len(properties))
		for key := range properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if propertyExample := exampleFromSchema(properties[key], components, seen, depth+1); propertyExample != nil {
				object[key] = propertyExample
			}
		}
		return object
	}
	if items, ok := schema["items"]; ok {
		if itemExample := exampleFromSchema(items, components, seen, depth+1); itemExample != nil {
			return []interface{}{itemExample}
		}
		return []interface{}{}
	}
	switch schema["type"] {
	case "string":
		return "example"
	case "integer", "number":
		return 1
	case "boolean":
		return true
	case "array":
		return []interface{}{}
	case "object":
		return map[string]interface{}{}
	}
	return nil
}

// Start begins listening on both ports
func (s *Server) Start(ctx context.Context) error {
	callerAddr := fmt.Sprintf(":%d", s.config.CallerPort)
	operatorAddr := fmt.Sprintf(":%d", s.config.OperatorPort)

	// In-cluster deployments must authenticate to OpenBao before Kubernetes
	// considers the pod ready. Local/dev runs do not have the projected token
	// or explicit SEAM_OPENBAO_ADDR, so they retain the Phase 1 behavior.
	s.startOpenBaoLogin(ctx)

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

	// Wrap with header-stripping middleware (stage 2 - strips X-SEAM-* headers)
	callerHandler = s.headerStrippingMiddleware(callerHandler)
	log.Printf("Header-stripping middleware active on caller-facing port (stage 2)")

	// Wrap with validation middleware (stage 1 - control-plane detection)
	callerHandler = s.validationMiddleware(callerHandler)
	log.Printf("Validation middleware active on caller-facing port (stage 1)")

	// Wrap with metrics middleware to track HTTP requests
	callerHandler = s.metricsMiddleware(callerHandler)
	log.Printf("Metrics middleware active on caller-facing port")

	// Wrap with capture middleware if enabled
	if s.captureMiddleware != nil {
		callerHandler = s.captureMiddleware.Wrap(callerHandler)
		log.Printf("Capture middleware active on caller-facing port")
	}

	// Wrap with version injection middleware (outermost - adds version headers to all responses)
	callerHandler = s.versionInjectionMiddleware(callerHandler)
	log.Printf("Version injection middleware active on caller-facing port")

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

// isOpenBaoConfigured reports whether this process is expected to use the
// projected Kubernetes service-account token. The deployment sets the
// address explicitly; checking the token path also covers an in-cluster
// caller that relies on the standard OpenBao defaults.
func isOpenBaoConfigured() bool {
	if strings.TrimSpace(os.Getenv("SEAM_OPENBAO_ADDR")) != "" ||
		strings.TrimSpace(os.Getenv("OPENBAO_ADDR")) != "" {
		return true
	}
	tokenPath := strings.TrimSpace(os.Getenv("SEAM_OPENBAO_SA_TOKEN_PATH"))
	if tokenPath == "" {
		tokenPath = vault.DefaultServiceAccount
	}
	_, err := os.Stat(tokenPath)
	return err == nil
}

func (s *Server) isOpenBaoReady() bool {
	s.openBaoMu.RLock()
	defer s.openBaoMu.RUnlock()
	return s.openBaoReady
}

func (s *Server) setOpenBaoReady(ready bool) {
	s.openBaoMu.Lock()
	s.openBaoReady = ready
	s.openBaoMu.Unlock()
}

// startOpenBaoLogin performs the first Kubernetes-auth login asynchronously.
// The listeners can serve health checks while readyz remains 503, allowing
// the Deployment's readiness probe to gate Service traffic without turning a
// transient OpenBao outage into a CrashLoopBackOff. No token or secret value is
// logged; client errors contain only non-secret authentication diagnostics.
func (s *Server) startOpenBaoLogin(ctx context.Context) {
	if !isOpenBaoConfigured() {
		return
	}

	s.setOpenBaoReady(false)
	go func() {
		for {
			client, err := vault.NewFromEnv()
			if err == nil {
				loginCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err = client.Login(loginCtx)
				cancel()
			}
			if err == nil {
				s.setOpenBaoReady(true)
				log.Printf("OpenBao startup Kubernetes login succeeded")
				return
			}

			log.Printf("OpenBao startup login not ready: %v", err)
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
		}
	}()
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
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
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
		"enabled":           true,
		"size":              stats.Size,
		"hits":              stats.Hits,
		"misses":            stats.Misses,
		"evictions":         stats.Evictions,
		"hit_rate":          hitRate,
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
		MethodNotAllowed("Only POST method is allowed").Write(w, r)
		return
	}

	s.cache.Cleanup()

	// Update metrics after cleanup
	stats := s.cache.Stats()
	updateCacheMetrics(stats)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "cleanup_complete",
		"size":      stats.Size,
		"evictions": stats.Evictions,
	})
}

// dispatchHandler is the catch-all handler that dispatches requests to upstream services
// This is the core proxy primitive that implements stages 4-11 of the request pipeline:
//   - Stage 4: Match request against route table
//   - Stages 5: (Reserved for future - validation, guards, etc.)
//   - Stage 6-11: Dispatch to upstream via ReverseProxy
//
// Phase 2.0: Route-based upstream selection - each route has its own upstream target
// The upstream URL is extracted from the matched route's UpstreamTarget field
func (s *Server) dispatchHandler(w http.ResponseWriter, r *http.Request) {
	// Stage 4: Match the request against the route table
	routeMatch, err := s.routeTable.Match(r)
	if err != nil {
		// No route found - return 404
		s.handleNotFound(w, r)
		return
	}

	// Extract upstream URL from the matched route
	upstreamURL := routeMatch.Route.UpstreamTarget
	if upstreamURL == "" {
		// No upstream configured for this route - return 503
		s.handleNoUpstream(w, r)
		return
	}

	// Get or create proxy for this upstream
	proxy := s.getOrCreateProxy(upstreamURL)
	if proxy == nil {
		// Proxy creation failed - return 503
		s.handleProxyCreationFailed(w, r, upstreamURL)
		return
	}

	// Stages 6-11: Dispatch to upstream and stream response back
	// The ReverseProxy handles all stages: building request, dispatching, streaming response
	proxy.ServeHTTP(w, r)
}

// getOrCreateProxy gets an existing proxy from the proxyMap or creates a new one
// This is the swap point for hot reload (Phase 3.1) - proxies are cached by upstream URL
func (s *Server) getOrCreateProxy(upstreamURL string) *ReverseProxy {
	s.proxyMapMu.Lock()
	defer s.proxyMapMu.Unlock()

	// Check if proxy already exists
	if proxy, exists := s.proxyMap[upstreamURL]; exists {
		return proxy
	}

	// Create new proxy
	proxy, err := NewReverseProxyWithConfig(upstreamURL, &ReverseProxyConfig{
		MaxReplayableRequestBytes: s.config.MaxReplayableRequestBytes,
		MaxBufferedResponseBytes:  s.config.MaxBufferedResponseBytes,
	})
	if err != nil {
		log.Printf("[dispatch] Failed to create proxy for upstream %s: %v", upstreamURL, err)
		return nil
	}

	s.proxyMap[upstreamURL] = proxy
	log.Printf("[dispatch] Created new proxy for upstream %s", upstreamURL)
	return proxy
}

// handleNotFound returns a 404 response when no route matches
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   "route_not_found",
		"message": fmt.Sprintf("No route found for %s %s", r.Method, r.URL.Path),
		"path":    r.URL.Path,
		"method":  r.Method,
		"docs":    "/docs",
	})
}

// handleNoUpstream returns a 503 response when no upstream is configured
func (s *Server) handleNoUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   "no_upstream_configured",
		"message": "No upstream URL configured for this route",
		"path":    r.URL.Path,
		"method":  r.Method,
	})
}

// handleProxyCreationFailed returns a 503 response when proxy creation fails
func (s *Server) handleProxyCreationFailed(w http.ResponseWriter, r *http.Request, upstreamURL string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":        "proxy_creation_failed",
		"message":      "Failed to create proxy for upstream",
		"upstream_url": upstreamURL,
		"path":         r.URL.Path,
		"method":       r.Method,
	})
}

// metricsMiddleware tracks HTTP request metrics (count, latency, in-flight)
func (s *Server) metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip metrics for reserved paths (control plane endpoints)
		if isReservedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Track route and method
		route := r.URL.Path
		method := r.Method

		// Increment in-flight counter
		incrementInFlight(route, method)
		defer decrementInFlight(route, method)

		// Record start time
		startTime := time.Now()

		// Wrap response writer to capture status code
		wrapped := &metricsResponseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK, // Default status code
		}

		// Call next handler
		next.ServeHTTP(wrapped, r)

		// Calculate duration
		duration := time.Since(startTime).Seconds()

		// Record request metrics
		recordHTTPRequest(route, method, wrapped.statusCode, duration)
	})
}

// metricsResponseWriter wraps http.ResponseWriter to capture status code
type metricsResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

// WriteHeader captures the status code
func (w *metricsResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}
