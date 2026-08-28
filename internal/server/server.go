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

	"github.com/ardenone/seam/internal/buildinfo"
	"github.com/ardenone/seam/internal/fanout"
	"github.com/ardenone/seam/internal/spec"
	"github.com/ardenone/seam/internal/vault"
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
		"/docs/paths":         true, // Phase 11.2: All paths with last-2xx status
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
	VaultBaseDir              string // Base directory for vault path validation (default: "rs-manager/seam/routes")
	MaxReplayableRequestBytes int64  // Phase 2.5: Max inbound request body size to buffer for replay (default 1 MiB, independent knob)
	MaxBufferedResponseBytes  int64  // Phase 2.6: Max decoded response body to hold for whole-response scrubbing (default 1 MiB, independent knob)
	HotReloadEnabled          bool   // Phase 3.1: Enable file-watch hot reload of route fragments
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
	routeTableHolder  *ThreadSafeTableHolder   // Thread-safe holder for route table (stage 4)
	proxyMap          map[string]*ReverseProxy // Map of upstream URL + TLS identity -> proxy instance (stages 6-11)
	proxyMapMu        sync.RWMutex             // Protects proxyMap
	upstreamClientMap map[string]*http.Client  // Map of TLS identity -> connection-pooled client
	captureMiddleware *CaptureMiddleware
	cache             *ResponseCache
	singleFlight      *SingleFlight
	cacheTTLs         map[string]int // route path -> cache TTL in seconds
	circuitBreakers   *CircuitBreakerStateRegistry
	breakerRegistry   *BreakerRegistry // Per-origin circuit breaker manager (Phase 11.1)
	last2xxTracker    *Last2xxTracker  // Per-path and per-upstream last-2xx tracking (Phase 11.2)
	quotaTracker      *QuotaTracker
	loopGuardRegistry *LoopGuardRegistry // Per-route loop guard manager (Phase 13.1)
	costPerCalls      map[string]float64 // route -> cost per call
	metrics           *Metrics
	mu                sync.RWMutex
	allowlistEnforcer *spec.AllowlistEnforcer // Allowlist enforcer for vault-path and upstream-host validation
	openBaoMu         sync.RWMutex
	openBaoReady      bool
	hotReloadManager  *HotReloadManager // Hot reload manager for fragment changes
}

// Circuit breaker context constants (using contextKey type from proxy.go)
const (
	circuitBreakerContextKey contextKey = iota + 1 // Start from 1 to avoid collision with replayableBodyKey
)

// breakerContext holds circuit breaker state in the request context
type breakerContext struct {
	breaker *CircuitBreaker
	origin  Origin
}

// New creates a new Server with the given configuration
func New(cfg *Config) *Server {
	if cfg.MaxBufferedResponseBytes <= 0 {
		cfg.MaxBufferedResponseBytes = DefaultMaxBufferedResponseBytes
	}
	// Initialize the spec loader
	var specLoader *spec.Loader
	var err error

	// Set default vault base directory. This is the canonical, portable
	// prefix documented in docs/notes/route-fragment-schema.md and encoded
	// in the route-fragment-schema.json pattern — it must stay cluster-
	// agnostic. A cluster's own OpenBao ACL policy is what's responsible for
	// granting read on secret/data/seam/routes/*; backup/replication
	// coverage for that prefix is a cluster-ops concern (see rs-manager's
	// openbao-replicator-config, which explicitly walks both "seam/" and
	// its own cluster-name prefix), not something this default should bend
	// to accommodate.
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
		routeTableHolder:  NewThreadSafeTableHolder(NewRouteTable(specLoader)),
		proxyMap:          make(map[string]*ReverseProxy),
		upstreamClientMap: make(map[string]*http.Client),
		cache:             NewResponseCache(),
		singleFlight:      NewSingleFlight(),
		cacheTTLs:         make(map[string]int),
		circuitBreakers:   NewCircuitBreakerStateRegistry(),
		breakerRegistry:   NewBreakerRegistry(NewCircuitBreakerStateRegistry()),
		last2xxTracker:    NewLast2xxTracker(),
		quotaTracker:      NewQuotaTracker(),
		loopGuardRegistry: NewLoopGuardRegistry(),
		costPerCalls:      make(map[string]float64),
		allowlistEnforcer: allowlistEnforcer,
		openBaoReady:      true,
	}
	s.metrics = newMetrics(s.cache, s.routeTableHolder, s.circuitBreakers, buildinfo.Read())
	log.Printf("Route table initialized with thread-safe holder")

	// Initialize capture middleware if enabled
	if cfg.CaptureEnabled {
		corpusDir := cfg.CorpusDir
		if corpusDir == "" {
			corpusDir = "corpus"
		}
		s.captureMiddleware = NewCaptureMiddleware(corpusDir, "seam", "seam-incumbent", true)
		s.captureMiddleware.setRouteTableHolder(s.routeTableHolder)
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

	// Initialize hot reload manager if enabled
	if cfg.HotReloadEnabled {
		s.hotReloadManager = NewHotReloadManager(s)
		if err := s.hotReloadManager.Enable(); err != nil {
			log.Printf("Warning: Failed to enable hot reload: %v", err)
			log.Printf("Warning: Server will continue without hot reload")
		}
	}

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
	s.callerMux.HandleFunc("/docs/paths", s.docsPathsHandler)

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
	s.operatorMux.HandleFunc("/health/upstreams", s.healthUpstreamsHandler)
	s.operatorMux.HandleFunc("/", s.operatorNotFoundHandler)
}

func (s *Server) operatorNotFoundHandler(w http.ResponseWriter, r *http.Request) {
	NotFound("Operator endpoint not found").
		WithDetail("method", r.Method).
		WithDetail("path", r.URL.Path).
		WithDocsURL("/docs").
		Write(w, r)
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
	ready := s.allowlistEnforcer == nil || !s.allowlistEnforcer.IsFailClosed()
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

// metricsHandler exposes the server-scoped Prometheus registry on the
// operator-only listener.
func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}

	s.ensureMetrics().handler().ServeHTTP(w, r)
}

func (s *Server) ensureMetrics() *Metrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metrics == nil {
		if s.cache == nil {
			s.cache = NewResponseCache()
		}
		if s.routeTableHolder == nil {
			s.routeTableHolder = NewThreadSafeTableHolder(NewRouteTable(nil))
		}
		if s.circuitBreakers == nil {
			s.circuitBreakers = NewCircuitBreakerStateRegistry()
		}
		s.metrics = newMetrics(s.cache, s.routeTableHolder, s.circuitBreakers, buildinfo.Read())
	}
	return s.metrics
}

// configStatusHandler returns comprehensive runtime configuration status
// Returns: current configuration values, spec hash, corpus status, enabled route count, and health status
func (s *Server) configStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
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
		requestErr := WrapRequestError(ErrCodeCaptureFailed, "Failed to save corpus", err)
		logRequestError(r, "capture-save", requestErr)
		requestErr.Write(w, r)
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

	// Get the spec JSON with servers populated
	specJSON, err := s.specLoader.GetRawJSON()
	if err != nil {
		requestErr := WrapRequestError(ErrCodeSpecLoadFailed, "Failed to load API specification", err)
		logRequestError(r, "openapi-document", requestErr)
		requestErr.Write(w, r)
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

	// Fetch the merged OpenAPI spec from internal spec loader
	specJSON, err := s.specLoader.GetRawJSON()
	if err != nil {
		requestErr := WrapRequestError(ErrCodeSpecLoadFailed, "Failed to load API specification", err)
		logRequestError(r, "docs-document", requestErr)
		requestErr.Write(w, r)
		return
	}

	// Validate that the spec is valid JSON
	var specJSONCheck interface{}
	if err := json.Unmarshal(specJSON, &specJSONCheck); err != nil {
		requestErr := WrapRequestError(ErrCodeSpecLoadFailed, "API specification is not valid JSON", err)
		logRequestError(r, "docs-document", requestErr)
		requestErr.Write(w, r)
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
		version = unversionedAPIVersion
	}

	// Validate required parameters
	if path == "" {
		MissingParameter("path").
			WithDetail("location", "query").
			WithDetail("example", "/docs/route?path=/test/get&method=GET").
			WithDocsURL("/docs/route").
			Write(w, r)
		return
	}

	// Get route information from the spec loader
	routeInfo, err := s.specLoader.GetRoute(path, method, version)
	if err != nil {
		requestErr := WrapRequestError(ErrCodeRouteNotFound, "Requested route documentation was not found", err).
			WithDetail("path", path).
			WithDetail("method", method).
			WithDetail("version", version).
			WithDocsURL("/docs")
		logRequestError(r, "docs-route", requestErr)
		requestErr.Write(w, r)
		return
	}

	// Read the same served document that /openapi.json returns. Using its raw
	// path item keeps request/response schemas, examples, and x-* annotations
	// intact instead of rebuilding a lossy approximation from the high-level
	// model.
	specJSON, err := s.specLoader.GetRawJSON()
	if err != nil {
		requestErr := WrapRequestError(ErrCodeSpecLoadFailed, "Failed to load API specification", err)
		logRequestError(r, "docs-route", requestErr)
		requestErr.Write(w, r)
		return
	}
	var document map[string]interface{}
	if err := json.Unmarshal(specJSON, &document); err != nil {
		requestErr := WrapRequestError(ErrCodeSpecLoadFailed, "API specification is not valid JSON", err)
		logRequestError(r, "docs-route", requestErr)
		requestErr.Write(w, r)
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

	// Phase 11.2: Add last-2xx status for the path
	// This tracking is in-memory and restart-scoped (lost on process restart)
	last2xxStatus := s.last2xxTracker.GetPathStatus(path)
	response["last_2xx"] = last2xxStatus

	// Set headers and return response
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-SEAM-Spec-Version", s.specLoader.GetHash())
	w.Header().Set("X-Spec-Version", s.specLoader.GetHash())
	w.Header().Set("X-SEAM-API-Version", s.specLoader.GetAPIVersion())
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

// docsPathsHandler returns all paths with their last-2xx status.
// Query parameters: none
//
// Phase 11.2: Returns a list of all tracked paths with their three-state last-2xx tracking.
// This tracking is in-memory and restart-scoped (lost on process restart).
func (s *Server) docsPathsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}

	// Get all path-level statuses
	pathStatuses := s.last2xxTracker.GetAllPathStatuses()

	// Build response
	response := map[string]interface{}{
		"metadata": map[string]interface{}{
			"description":  "All paths with last-2xx status (Phase 11.2)",
			"spec_version": s.specLoader.GetVersion(),
			"api_version":  s.specLoader.GetAPIVersion(),
			"total_paths":  len(pathStatuses),
		},
		"paths": pathStatuses,
	}

	// Set headers and return response
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-SEAM-Spec-Version", s.specLoader.GetHash())
	w.Header().Set("X-Spec-Version", s.specLoader.GetHash())
	w.Header().Set("X-SEAM-API-Version", s.specLoader.GetAPIVersion())
	w.Header().Set("Cache-Control", "no-store")
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
	callerHandler = s.versionMiddleware(callerHandler)
	callerHandler = s.versionInjectionMiddleware(callerHandler)
	callerHandler = s.requestIDMiddleware(callerHandler)
	log.Printf("Version validation, injection, and request ID middleware active on caller-facing port")

	// Version validation applies to the operator listener as well. Keep header
	// injection outermost so rejected operator requests also identify the API
	// and spec versions that evaluated them.
	operatorHandler := s.versionMiddleware(s.operatorMux)
	operatorHandler = s.versionInjectionMiddleware(operatorHandler)
	operatorHandler = s.requestIDMiddleware(operatorHandler)
	log.Printf("Version validation, injection, and request ID middleware active on operator-only port")

	// Create servers
	s.callerServer = &http.Server{
		Handler: callerHandler,
	}
	s.operatorServer = &http.Server{
		Handler: operatorHandler,
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

	// Stop hot reload manager if enabled
	if s.hotReloadManager != nil {
		s.hotReloadManager.Disable()
		log.Printf("Hot reload manager stopped")
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

	stats := s.cache.Stats()

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
	routeMatch, err := s.routeTableHolder.Match(r)
	if err != nil {
		// No route found - return 404
		s.handleNotFound(w, r)
		return
	}

	// Check if this is a fan-out request (_all instance parameter)
	if routeMatch.Route.IsFanOutRequest(routeMatch.PathParams) {
		s.handleFanOutRequest(w, r, routeMatch)
		return
	}

	// Resolve the instance target if using x-instance-param
	route := routeMatch.Route
	if route.InstanceParam != "" && len(route.UpstreamMap) > 0 {
		instance := routeMatch.PathParams[route.InstanceParam]
		if instance == "" {
			s.handleNoUpstream(w, r)
			return
		}

		// Resolve to specific instance target
		if target, ok := route.UpstreamMap[instance]; ok && target.URL != "" {
			route.UpstreamTarget = target.URL
			if target.VaultPath != "" {
				route.VaultPath = target.VaultPath
			}
			if target.InjectAs != nil {
				route.InjectAs = target.InjectAs
			}
		} else if target, ok := route.UpstreamMap["_default"]; ok && target.URL != "" {
			route.UpstreamTarget = target.URL
			if target.VaultPath != "" {
				route.VaultPath = target.VaultPath
			}
			if target.InjectAs != nil {
				route.InjectAs = target.InjectAs
			}
		} else {
			s.handleNoUpstream(w, r)
			return
		}
	}

	// Single-instance request path (existing behavior)
	upstreamURL := route.UpstreamTarget
	if upstreamURL == "" {
		// No upstream configured for this route - return 503
		s.handleNoUpstream(w, r)
		return
	}

	// Check circuit breaker before dispatching
	if s.breakerRegistry != nil {
		origin, err := ParseOrigin(upstreamURL)
		if err != nil {
			log.Printf("[circuit-breaker] Failed to parse origin for %s: %v", upstreamURL, err)
		} else {
			// Get or create breaker for this origin
			config := route.BreakerConfig
			if config == nil {
				defaultConfig := DefaultBreakerConfig()
				config = &defaultConfig
			}
			breaker := s.breakerRegistry.GetOrCreate(origin, *config)

			// Check if breaker allows the request
			if !breaker.Allow() {
				// Breaker is open, return structured 503
				log.Printf("[circuit-breaker] Request refused by breaker for origin %s", origin)
				WriteCircuitBreakerRefused(w, r, breaker.Snapshot())
				return
			}

			// Store breaker in request context for post-response tracking
			ctx := context.WithValue(r.Context(), circuitBreakerContextKey, breakerContext{breaker: breaker, origin: origin})
			r = r.WithContext(ctx)
		}
	}

	// Get or create proxy for this upstream
	proxy, err := s.getOrCreateProxyWithError(upstreamURL, route.TLSConfig)
	if err != nil {
		// Proxy creation failed - return 503
		s.handleProxyCreationFailed(w, r, err)
		return
	}

	// Phase 11.2: Record attempt before dispatch (per-path and per-upstream)
	// This tracking is in-memory and restart-scoped (lost on process restart)
	pathTemplate := route.PathTemplate
	origin := upstreamURL // Full upstream URL as origin identifier
	s.last2xxTracker.RecordAttempt(pathTemplate, origin)

	// Wrap response writer to track status for last-2xx recording
	trackedWriter := &responseWriterTracker{ResponseWriter: w}

	// Stages 6-11: Dispatch to upstream and stream response back
	// The ReverseProxy handles all stages: building request, dispatching, streaming response
	proxy.ServeHTTP(trackedWriter, r)

	// Phase 11.2: Record success or error based on response status
	// This tracking is in-memory and restart-scoped (lost on process restart)
	statusCode := trackedWriter.StatusCode()
	if statusCode >= 200 && statusCode < 300 {
		// Success: record with source (empty for passive requests, populated by probes in Phase 12)
		s.last2xxTracker.RecordSuccess(pathTemplate, origin, "")
	} else {
		// Error: record the error message
		// Get last error from circuit breaker if available
		var errMsg string
		if ctxVal := r.Context().Value(circuitBreakerContextKey); ctxVal != nil {
			if bc, ok := ctxVal.(breakerContext); ok {
				breakerSnapshot := bc.breaker.Snapshot()
				errMsg = breakerSnapshot.LastError
			}
		}
		// Fallback error message if no breaker error available
		if errMsg == "" {
			if statusCode > 0 {
				errMsg = fmt.Sprintf("HTTP %d", statusCode)
			} else {
				errMsg = "request failed"
			}
		}
		s.last2xxTracker.RecordError(pathTemplate, origin, errMsg)
	}
}

// handleFanOutRequest handles a _all fan-out request by dispatching to all instances
// in the upstream map and returning a 207 Multi-Status envelope.
func (s *Server) handleFanOutRequest(w http.ResponseWriter, r *http.Request, routeMatch *RouteMatch) {
	// Extract all instance targets from the upstream map
	instanceTargets := routeMatch.Route.GetAllInstanceTargets()
	if len(instanceTargets) == 0 {
		s.handleNoUpstream(w, r)
		return
	}

	// Build instance dispatch list
	dispatches := make([]fanout.InstanceDispatch, 0, len(instanceTargets))
	for instanceID, target := range instanceTargets {
		dispatch := fanout.InstanceDispatch{
			InstanceID: instanceID,
			Target:     target.URL,
			VaultPath:  target.VaultPath,
		}

		// Set InjectAs configuration
		if target.InjectAs != nil {
			dispatch.InjectAs = &fanout.InjectAsConfig{
				Kind: string(target.InjectAs.Kind),
				Name: target.InjectAs.Name,
			}
		}

		// Set TLS configuration from the route (applies to all instances)
		if routeMatch.Route.TLSConfig != nil {
			dispatch.TLSConfig = &fanout.TLSConfig{
				CaBundle:           routeMatch.Route.TLSConfig.CaBundle,
				ServerName:         routeMatch.Route.TLSConfig.ServerName,
				InsecureSkipVerify: routeMatch.Route.TLSConfig.InsecureSkipVerify,
				PlaintextAck:       routeMatch.Route.TLSConfig.PlaintextAck,
			}
		}

		dispatches = append(dispatches, dispatch)
	}

	// Calculate max envelope size
	maxBufferedResponseBytes := int64(1 * 1024 * 1024) // Default 1 MiB
	if s.config != nil && s.config.MaxBufferedResponseBytes > 0 {
		maxBufferedResponseBytes = s.config.MaxBufferedResponseBytes
	}
	maxEnvelopeBytes := fanout.MaxFanoutEnvelopeBytes(maxBufferedResponseBytes, len(dispatches))

	// Create fan-out dispatcher configuration
	dispatchConfig := &fanout.DispatchConfig{
		MaxEnvelopeBytes: maxEnvelopeBytes,
		Timeout:          30 * time.Second,
		ConcurrentLimit:  10,
	}

	// Create executor that uses existing proxy infrastructure
	executor := &fanoutExecutor{
		server:     s,
		routeMatch: routeMatch,
		request:    r,
	}

	// Create circuit breaker check
	breakerCheck := func(instanceID string, targetURL string) bool {
		if s.circuitBreakers == nil {
			return false // No breaker state, allow all
		}
		// Check if circuit is open for this target
		snapshot := s.circuitBreakers.Snapshot()
		for _, status := range snapshot {
			if status.Origin == targetURL && status.State == CircuitBreakerOpen {
				return true
			}
		}
		return false
	}

	// Create scope check (from x-fanout-scope fragment field)
	scopeCheck := func(instanceID string) bool {
		// TODO: Extract scope configuration from route entry
		// For now, allow all instances (no scope filtering)
		return true
	}

	// Create dispatcher and execute
	dispatcher := fanout.NewDispatcher(dispatchConfig, executor, breakerCheck, scopeCheck)
	envelope := dispatcher.Dispatch(r.Context(), dispatches)

	// Derive final HTTP status and partial header
	statusCode, partialHeader := envelope.DeriveStatus()

	// Write the response
	w.Header().Set("Content-Type", "application/json")
	if partialHeader != "" {
		w.Header().Set("X-SEAM-Fanout-Partial", partialHeader)
	}

	// Handle special case: all breaker-refused = 503 (not 207)
	if statusCode == http.StatusServiceUnavailable && envelope.Summary.BreakerRefused == envelope.Summary.Dispatched {
		NewErrorResponse(ErrCodeServiceUnavailable, "All instances refused by circuit breaker").
			WithDetail("total_instances", envelope.Summary.Total).
			WithDetail("refused_count", envelope.Summary.BreakerRefused).
			Write(w, r)
		return
	}

	// Write envelope with appropriate status code
	w.WriteHeader(statusCode)
	jsonBytes, err := envelope.JSONBytes()
	if err != nil {
		log.Printf("[fanout] Failed to encode envelope: %v", err)
		NewErrorResponse(ErrCodeInternalServer, "Failed to encode fan-out response").Write(w, r)
		return
	}
	_, _ = w.Write(append(jsonBytes, '\n'))
}

// getOrCreateProxy gets an existing proxy from the proxyMap or creates a new one.
// Proxies are keyed by upstream URL and TLS identity so routes sharing an
// upstream cannot accidentally reuse a client configured for another route.
// The client itself is shared by TLS identity to preserve connection pooling.
func (s *Server) getOrCreateProxy(upstreamURL string, tlsConfigs ...*UpstreamTLSConfig) *ReverseProxy {
	proxy, _ := s.getOrCreateProxyWithError(upstreamURL, tlsConfigs...)
	return proxy
}

// getOrCreateProxyWithError is the error-preserving form used by the request
// path. The compatibility wrapper above remains useful to construction-only
// callers that only need the proxy-or-nil result.
func (s *Server) getOrCreateProxyWithError(upstreamURL string, tlsConfigs ...*UpstreamTLSConfig) (*ReverseProxy, error) {
	s.proxyMapMu.Lock()
	defer s.proxyMapMu.Unlock()

	var tlsConfig *UpstreamTLSConfig
	if len(tlsConfigs) > 0 {
		tlsConfig = tlsConfigs[0]
	}
	if s.proxyMap == nil {
		s.proxyMap = make(map[string]*ReverseProxy)
	}

	proxyKey := upstreamProxyCacheKey(upstreamURL, tlsConfig)
	// Check if proxy already exists
	if proxy, exists := s.proxyMap[proxyKey]; exists {
		return proxy, nil
	}

	clientKey := upstreamTLSConfigKey(tlsConfig)
	if s.upstreamClientMap == nil {
		s.upstreamClientMap = make(map[string]*http.Client)
	}
	client, exists := s.upstreamClientMap[clientKey]
	if !exists {
		if tlsConfig == nil {
			client = defaultUpstreamClient
		} else {
			upstreamCADir := DefaultUpstreamCADir
			if s.config != nil && s.config.UpstreamCADir != "" {
				upstreamCADir = s.config.UpstreamCADir
			}
			var err error
			client, err = newUpstreamHTTPClientWithTLS(tlsConfig, upstreamCADir)
			if err != nil {
				return nil, fmt.Errorf("create upstream client for TLS config %s: %w", clientKey, err)
			}
		}
		s.upstreamClientMap[clientKey] = client
	}

	maxReplayableRequestBytes := int64(0)
	maxBufferedResponseBytes := int64(0)
	if s.config != nil {
		maxReplayableRequestBytes = s.config.MaxReplayableRequestBytes
		maxBufferedResponseBytes = s.config.MaxBufferedResponseBytes
	}

	// Create new proxy
	proxy, err := NewReverseProxyWithConfig(upstreamURL, &ReverseProxyConfig{
		MaxReplayableRequestBytes: maxReplayableRequestBytes,
		MaxBufferedResponseBytes:  maxBufferedResponseBytes,
		TLSConfig:                 tlsConfig,
		Client:                    client,
	})
	if err != nil {
		return nil, fmt.Errorf("create proxy: %w", err)
	}

	// Phase 13.2: Wire up quota tracker for dispatch-time accounting
	proxy.QuotaTracker = s.quotaTracker

	s.proxyMap[proxyKey] = proxy
	log.Printf("[dispatch] Created new proxy for upstream %s (TLS identity %s)", upstreamURL, clientKey)
	return proxy, nil
}

// handleNotFound returns a 404 response when no route matches
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	RouteNotFound(r.Method, r.URL.Path).Write(w, r)
}

// handleNoUpstream returns a 503 response when no upstream is configured
func (s *Server) handleNoUpstream(w http.ResponseWriter, r *http.Request) {
	NewErrorResponse(ErrCodeNoUpstreamConfigured, "No upstream URL configured for this route").
		WithDetail("path", r.URL.Path).
		WithDetail("method", r.Method).
		Write(w, r)
}

// handleProxyCreationFailed returns a 503 response when proxy creation fails
func (s *Server) handleProxyCreationFailed(w http.ResponseWriter, r *http.Request, cause error) {
	requestErr := WrapRequestError(ErrCodeProxyCreationFailed, "Failed to create proxy for upstream", cause).
		WithDetail("path", r.URL.Path).
		WithDetail("method", r.Method)
	logRequestError(r, "dispatch", requestErr)
	requestErr.Write(w, r)
}

// metricsMiddleware tracks HTTP request metrics (count, latency, in-flight)
func (s *Server) metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip metrics for reserved paths (control plane endpoints)
		if isReservedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		metrics := s.ensureMetrics()
		labels := s.metricLabels(r)
		ctx := context.WithValue(r.Context(), metricRouteContextKey{}, labels)
		*r = *r.WithContext(ctx)

		metrics.incrementInFlight(labels, r.Method)
		startTime := time.Now()

		// Wrap response writer to capture status code
		wrapped := &metricsResponseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}
		defer func() {
			metrics.decrementInFlight(labels, r.Method)
			metrics.recordHTTPRequest(labels, r.Method, wrapped.statusCode, time.Since(startTime))
		}()

		next.ServeHTTP(wrapped, r)
	})
}

func (s *Server) metricLabels(r *http.Request) metricRouteLabels {
	if s.routeTableHolder != nil {
		requestCopy := r.Clone(r.Context())
		if match, err := s.routeTableHolder.Match(requestCopy); err == nil {
			return metricRouteLabels{
				Route:   match.Route.PathTemplate,
				Version: match.Route.APIVersion,
			}
		}
	}
	return metricRouteLabels{Route: unmatchedMetricRoute, Version: "unknown"}
}

func metricLabelsFromRequest(r *http.Request) metricRouteLabels {
	if r != nil {
		if labels, ok := r.Context().Value(metricRouteContextKey{}).(metricRouteLabels); ok {
			return labels
		}
		version := r.Header.Get("X-SEAM-API-Version")
		if version == "" {
			version = unversionedAPIVersion
		}
		return metricRouteLabels{Route: r.URL.Path, Version: version}
	}
	return metricRouteLabels{Route: unmatchedMetricRoute, Version: "unknown"}
}

// metricsResponseWriter wraps http.ResponseWriter to capture status code
type metricsResponseWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

// WriteHeader captures the status code
func (w *metricsResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *metricsResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// Unwrap lets net/http.ResponseController preserve streaming, flushing, and
// connection-hijacking capabilities of the underlying writer.
func (w *metricsResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// fanoutExecutor implements the fanout.Executor interface using the server's proxy infrastructure.
type fanoutExecutor struct {
	server     *Server
	routeMatch *RouteMatch
	request    *http.Request
}

// ExecuteRequest executes a single upstream request for one instance.
// This implements stages 10-11 of the SEAM pipeline per instance.
func (fe *fanoutExecutor) ExecuteRequest(ctx context.Context, dispatch *fanout.InstanceDispatch) (int, []byte, error) {
	// Create a modified route entry for this specific instance
	instanceRoute := fe.routeMatch.Route
	instanceRoute.UpstreamTarget = dispatch.Target
	instanceRoute.VaultPath = dispatch.VaultPath

	// Set instance-specific InjectAs if provided
	if dispatch.InjectAs != nil {
		instanceRoute.InjectAs = &InjectAs{
			Kind: InjectionKind(dispatch.InjectAs.Kind),
			Name: dispatch.InjectAs.Name,
		}
	}

	// Set TLS config if provided
	if dispatch.TLSConfig != nil {
		instanceRoute.TLSConfig = &UpstreamTLSConfig{
			CaBundle:           dispatch.TLSConfig.CaBundle,
			ServerName:         dispatch.TLSConfig.ServerName,
			InsecureSkipVerify: dispatch.TLSConfig.InsecureSkipVerify,
			PlaintextAck:       dispatch.TLSConfig.PlaintextAck,
		}
	}

	// Get or create proxy for this instance's upstream
	proxy, err := fe.server.getOrCreateProxyWithError(dispatch.Target, instanceRoute.TLSConfig)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to create proxy for instance %s: %w", dispatch.InstanceID, err)
	}

	// Create a response recorder to capture the response
	recorder := &fanoutResponseRecorder{
		header: make(http.Header),
	}

	// Create a copy of the request for this instance
	instanceReq := fe.cloneRequestForInstance(fe.request, dispatch.InstanceID)

	// Execute the request through the proxy
	// The proxy will handle stages 10-11 (request building, dispatch, response streaming)
	proxy.ServeHTTP(recorder, instanceReq)

	// Return the results
	return recorder.status, recorder.body, recorder.err
}

// cloneRequestForInstance creates a copy of the request for a specific instance.
// The instance parameter is added/replaced in the path parameters.
func (fe *fanoutExecutor) cloneRequestForInstance(req *http.Request, instanceID string) *http.Request {
	// Clone the request body and headers
	cloned := req.Clone(req.Context())

	// Update path parameters for this instance
	// This ensures the instance ID is available to the proxy for logging/metrics
	cloned = cloned.WithContext(context.WithValue(req.Context(), pathParamsContextKey{},
		mergePathParams(getPathParams(req.Context()), map[string]string{
			fe.routeMatch.Route.InstanceParam: instanceID,
		})))

	return cloned
}

// fanoutResponseRecorder captures an HTTP response for fan-out envelope building.
// It is named differently from responseRecorder in capture.go to avoid collision.
type fanoutResponseRecorder struct {
	status int
	body   []byte
	header http.Header
	err    error
}

func (r *fanoutResponseRecorder) Header() http.Header {
	if r.header == nil {
		r.header = make(http.Header)
	}
	return r.header
}

func (r *fanoutResponseRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body = append(r.body, b...)
	return len(b), nil
}

func (r *fanoutResponseRecorder) WriteHeader(statusCode int) {
	r.status = statusCode
}

// Context key types for path parameters and other request context.
type pathParamsContextKey struct{}

// routeMatchContextKey is declared in request_pipeline.go to avoid duplication

// getPathParams extracts path parameters from the request context.
func getPathParams(ctx context.Context) map[string]string {
	if params, ok := ctx.Value(pathParamsContextKey{}).(map[string]string); ok {
		return params
	}
	return nil
}

// mergePathParams merges two path parameter maps.
func mergePathParams(base, overlay map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		result[k] = v
	}
	return result
}
