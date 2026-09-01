package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ardenone/seam/internal/server"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: seam <command> [<args>]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Available commands:")
		fmt.Fprintln(os.Stderr, "  serve            Start the SEAM gateway server")
		fmt.Fprintln(os.Stderr, "  healthcheck      Probe the caller-facing liveness endpoint")
		fmt.Fprintln(os.Stderr, "  lint             Validate SEAM route fragments")
		fmt.Fprintln(os.Stderr, "  diff             Show differences between fragment versions")
		fmt.Fprintln(os.Stderr, "  import           Import fragments into SEAM")
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "serve":
		serveCommand(args)
	case "healthcheck":
		healthcheckCommand(args)
	case "lint":
		lintCommand(args)
	case "diff":
		diffCommand(args)
	case "import":
		importCommand(args)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		fmt.Fprintln(os.Stderr, "Available commands: serve, healthcheck, lint, diff, import")
		os.Exit(1)
	}
}

// runHealthcheck probes a liveness URL and reports whether the gateway is
// serving. Split from healthcheckCommand so it is testable without os.Exit.
func runHealthcheck(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("probe %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe %s: got HTTP %d, want %d", url, resp.StatusCode, http.StatusOK)
	}
	return nil
}

// healthcheckCommand is what the container image's HEALTHCHECK invokes. It
// probes /_seam/healthz on the caller-facing listener — the same port the
// kubelet liveness probe targets — and exits non-zero if the gateway is not
// serving. The runtime image is FROM scratch and has no shell, so this must
// remain a real subcommand: without it the HEALTHCHECK falls through to the
// unknown-command branch and the container reports unhealthy forever.
func healthcheckCommand(args []string) {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	callerPort := fs.Int("caller-port", 8080, "Port of the caller-facing listener to probe")
	timeout := fs.Duration("timeout", 2*time.Second, "Probe timeout")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	// Honour the same env var serve does, so a port override configured on the
	// Deployment cannot leave the healthcheck probing the wrong listener.
	if val := os.Getenv("SEAM_CALLER_PORT"); val != "" {
		if _, err := fmt.Sscanf(val, "%d", callerPort); err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: invalid SEAM_CALLER_PORT %q, keeping %d: %v\n", val, *callerPort, err)
		}
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/_seam/healthz", *callerPort)
	if err := runHealthcheck(url, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		os.Exit(1)
	}
}

func serveCommand(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)

	callerPort := fs.Int("caller-port", 8080, "Port for the caller-facing listener")
	operatorPort := fs.Int("operator-port", 8081, "Port for the operator-only listener")
	baseURL := fs.String("base-url", "http://localhost:8080", "Base URL for the caller-facing interface")
	specDir := fs.String("spec-dir", "./spec", "Directory containing local OpenAPI spec files")
	fragmentMode := fs.Bool("fragment-mode", false, "Enable fragment merge mode (reads from spec-dir/fragments.d)")
	schemaPath := fs.String("schema-path", "./spec/route-fragment-schema.json", "Path to route-fragment JSON schema for validation")
	captureEnabled := fs.Bool("capture-enabled", false, "Enable HTTP request/response capture")
	corpusDir := fs.String("corpus-dir", "corpus", "Directory to store captured corpus files")
	fragmentsDir := fs.String("fragments-dir", "./fragments", "Directory containing OpenAPI fragment files")
	upstreamCADir := fs.String("upstream-ca-dir", "", "Directory for upstream CA bundles (default: /etc/gateway/upstream-ca, refused in-cluster)")
	allowlistFile := fs.String("allowlist-file", "", "Path to the upstream host allowlist (refused in-cluster)")
	maxReplayableRequestBytes := fs.Int64("max-replayable-request-bytes", 1024*1024, "Phase 2.5: Maximum request body size to buffer for replay in bytes (default 1 MiB)")
	maxBufferedResponseBytes := fs.Int64("max-buffered-response-bytes", 1024*1024, "Phase 2.6: Maximum decoded response body size to hold for whole-response scrubbing in bytes (default 1 MiB)")
	hotReloadEnabled := fs.Bool("enable-hot-reload", false, "Phase 3.1: Enable file-watch hot reload of route fragments")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	// Override with environment variables if set
	if val := os.Getenv("SEAM_CALLER_PORT"); val != "" {
		if _, err := fmt.Sscanf(val, "%d", callerPort); err != nil {
			log.Printf("[config] invalid SEAM_CALLER_PORT %q, keeping %d: %v", val, *callerPort, err)
		}
	}
	if val := os.Getenv("SEAM_FRAGMENTS_DIR"); val != "" {
		*fragmentsDir = val
	}
	if val := os.Getenv("SEAM_OPERATOR_PORT"); val != "" {
		if _, err := fmt.Sscanf(val, "%d", operatorPort); err != nil {
			log.Printf("[config] invalid SEAM_OPERATOR_PORT %q, keeping %d: %v", val, *operatorPort, err)
		}
	}
	if val := os.Getenv("SEAM_BASE_URL"); val != "" {
		*baseURL = val
	}
	if val := os.Getenv("SEAM_SPEC_DIR"); val != "" {
		*specDir = val
	}
	if val := os.Getenv("SEAM_FRAGMENT_MODE"); val != "" {
		*fragmentMode = val == "true" || val == "1"
	}
	if val := os.Getenv("SEAM_SCHEMA_PATH"); val != "" {
		*schemaPath = val
	}
	if val := os.Getenv("SEAM_CAPTURE_ENABLED"); val != "" {
		*captureEnabled = val == "true" || val == "1"
	}
	if val := os.Getenv("SEAM_CORPUS_DIR"); val != "" {
		*corpusDir = val
	}
	if val := os.Getenv("SEAM_UPSTREAM_CA_DIR"); val != "" {
		*upstreamCADir = val
	}
	if val := os.Getenv("SEAM_UPSTREAM_ALLOWLIST"); val != "" {
		*allowlistFile = val
	}
	if val := os.Getenv("SEAM_MAX_REPLAYABLE_REQUEST_BYTES"); val != "" {
		if parsed, err := fmt.Sscanf(val, "%d", maxReplayableRequestBytes); err == nil && parsed == 1 {
			log.Printf("[config] SEAM_MAX_REPLAYABLE_REQUEST_BYTES=%s", val)
		} else {
			log.Printf("[config] invalid SEAM_MAX_REPLAYABLE_REQUEST_BYTES %q, keeping %d: %v", val, *maxReplayableRequestBytes, err)
		}
	}
	if val := os.Getenv("SEAM_MAX_BUFFERED_RESPONSE_BYTES"); val != "" {
		if parsed, err := fmt.Sscanf(val, "%d", maxBufferedResponseBytes); err == nil && parsed == 1 {
			log.Printf("[config] SEAM_MAX_BUFFERED_RESPONSE_BYTES=%s", val)
		} else {
			log.Printf("[config] invalid SEAM_MAX_BUFFERED_RESPONSE_BYTES %q, keeping %d: %v", val, *maxBufferedResponseBytes, err)
		}
	}
	if val := os.Getenv("SEAM_HOT_RELOAD_ENABLED"); val != "" {
		if val == "true" || val == "1" {
			*hotReloadEnabled = true
			log.Printf("[config] SEAM_HOT_RELOAD_ENABLED=%s", val)
		}
	}

	// Determine final upstream CA directory
	finalUpstreamCADir := *upstreamCADir
	if finalUpstreamCADir == "" {
		finalUpstreamCADir = server.DefaultUpstreamCADir
	}

	// Detect if running in-cluster and refuse custom upstream CA directory
	isInCluster := detectInClusterEnvironment()
	if isInCluster && *upstreamCADir != "" {
		log.Printf("[config] WARNING: --upstream-ca-dir is refused in-cluster; using %s", server.DefaultUpstreamCADir)
		finalUpstreamCADir = server.DefaultUpstreamCADir
	}

	// The allowlist is operator-owned in Kubernetes and arrives through the
	// ConfigMap volume. A developer-supplied path must never be able to replace
	// that mounted control in a pod.
	finalAllowlistFile := resolveAllowlistFile(*allowlistFile, isInCluster)
	if isInCluster {
		if *allowlistFile != "" {
			log.Printf("[config] WARNING: --allowlist-file is refused in-cluster; using %s", server.DefaultUpstreamAllowlistFile)
		}
		finalAllowlistFile = server.DefaultUpstreamAllowlistFile
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("Starting SEAM gateway server:")
	log.Printf("  Caller-facing port: %d", *callerPort)
	log.Printf("  Operator-only port: %d", *operatorPort)
	log.Printf("  Base URL: %s", *baseURL)
	log.Printf("  Spec directory: %s", *specDir)
	log.Printf("  Fragment mode: %v", *fragmentMode)
	if *fragmentMode {
		log.Printf("  Fragments directory: %s", *fragmentsDir)
		log.Printf("  Schema path: %s", *schemaPath)
	}
	log.Printf("  Capture enabled: %v", *captureEnabled)
	if *captureEnabled {
		log.Printf("  Corpus directory: %s", *corpusDir)
	}
	log.Printf("  Upstream CA directory: %s", finalUpstreamCADir)
	if isInCluster && *upstreamCADir != "" {
		log.Printf("  (Running in-cluster, custom --upstream-ca-dir refused)")
	}
	log.Printf("  Upstream allowlist file: %s", finalAllowlistFile)
	if isInCluster && *allowlistFile != "" {
		log.Printf("  (Running in-cluster, custom --allowlist-file refused)")
	}
	log.Printf("  Max replayable request bytes: %d", *maxReplayableRequestBytes)
	log.Printf("  Max buffered response bytes: %d", *maxBufferedResponseBytes)
	log.Printf("  Hot reload enabled: %v", *hotReloadEnabled)

	cfg := &server.Config{
		CallerPort:                *callerPort,
		OperatorPort:              *operatorPort,
		BaseURL:                   *baseURL,
		SpecDir:                   *specDir,
		FragmentMode:              *fragmentMode,
		SchemaPath:                *schemaPath,
		CaptureEnabled:            *captureEnabled,
		CorpusDir:                 *corpusDir,
		FragmentsDir:              *fragmentsDir,
		UpstreamCADir:             finalUpstreamCADir,
		AllowlistFile:             finalAllowlistFile,
		MaxReplayableRequestBytes: *maxReplayableRequestBytes,
		MaxBufferedResponseBytes:  *maxBufferedResponseBytes,
		HotReloadEnabled:          *hotReloadEnabled,
	}

	srv := server.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	// Wait for interrupt signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Received shutdown signal")
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Error during shutdown: %v", err)
	}

	log.Println("Server stopped")
}

// detectInClusterEnvironment checks if SEAM is running in a Kubernetes cluster.
// It uses the standard Kubernetes Downward API environment variables.
func detectInClusterEnvironment() bool {
	// Check for standard Kubernetes environment variables
	return os.Getenv("KUBERNETES_SERVICE_HOST") != "" && os.Getenv("KUBERNETES_PORT") != ""
}

func resolveAllowlistFile(requested string, inCluster bool) string {
	if inCluster {
		return server.DefaultUpstreamAllowlistFile
	}
	return requested
}
