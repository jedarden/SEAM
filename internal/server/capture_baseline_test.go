package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newBaselineAllowlistFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "allowlist.yaml")
	if err := os.WriteFile(path, []byte("allowed_hosts:\n  - localhost\n"), 0o600); err != nil {
		t.Fatalf("write baseline allowlist: %v", err)
	}
	return path
}

// TestArgoCDProxyBaselineOperation tests the baseline operation of the SEAM server
// acting as an ArgoCD read-only proxy without corpus capture enabled.
// This establishes the performance and functional baseline before adding capture.
func TestArgoCDProxyBaselineOperation(t *testing.T) {
	// Use available ports to avoid conflicts
	callerPort := getAvailablePort(t)
	operatorPort := getAvailablePort(t)

	cfg := &Config{
		CallerPort:     callerPort,
		OperatorPort:   operatorPort,
		BaseURL:        fmt.Sprintf("http://localhost:%d", callerPort),
		SpecDir:        "../../spec",
		CaptureEnabled: false, // BASELINE: No capture enabled
		CorpusDir:      "",
		AllowlistFile:  newBaselineAllowlistFile(t),
	}

	s := New(cfg)
	// The baseline covers identity-gated surfaces (/docs, /openapi.json, the
	// operator endpoints) that a loopback caller cannot resolve an identity
	// for; present the fixed test identity instead of asserting the deny.
	s.identityResolver = newLoopbackTestIdentityResolver()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = s.Shutdown(ctx) }()
	// This baseline exercises the server in an already-ready state. Startup
	// OpenBao login is covered separately; construct readiness explicitly so
	// an ambient OpenBao configuration cannot hold this test at 503.
	s.setOpenBaoReady(true)

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Test that all control plane endpoints respond correctly
	testCases := []struct {
		name           string
		path           string
		method         string
		expectedStatus int
		checkBody      bool
		bodyContains   string
		port           string // "caller" or "operator"
	}{
		// Caller-facing endpoints (ArgoCD clients would use these)
		{
			name:           "healthz-liveness",
			path:           "/_seam/healthz",
			method:         "GET",
			expectedStatus: http.StatusOK,
			checkBody:      true,
			bodyContains:   "OK",
			port:           "caller",
		},
		{
			name:           "readyz-readiness",
			path:           "/_seam/readyz",
			method:         "GET",
			expectedStatus: http.StatusOK,
			checkBody:      true,
			bodyContains:   "ready",
			port:           "caller",
		},
		{
			name:           "openapi-spec",
			path:           "/openapi.json",
			method:         "GET",
			expectedStatus: http.StatusOK,
			checkBody:      true,
			bodyContains:   "openapi",
			port:           "caller",
		},
		{
			name:           "api-documentation",
			path:           "/docs",
			method:         "GET",
			expectedStatus: http.StatusOK,
			checkBody:      true,
			bodyContains:   "SEAM API Documentation",
			port:           "caller",
		},
		// Operator-only endpoints (administrative access)
		{
			name:           "metrics-endpoint",
			path:           "/_seam/metrics",
			method:         "GET",
			expectedStatus: http.StatusOK,
			checkBody:      false,
			port:           "operator",
		},
		{
			name:           "config-status",
			path:           "/config/status",
			method:         "GET",
			expectedStatus: http.StatusOK,
			checkBody:      false,
			port:           "operator",
		},
		{
			name:           "capture-status-disabled",
			path:           "/_seam/capture/status",
			method:         "GET",
			expectedStatus: http.StatusOK,
			checkBody:      false,
			port:           "operator",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			port := s.config.CallerPort
			if tc.port == "operator" {
				port = s.config.OperatorPort
			}

			req, err := http.NewRequest(tc.method, fmt.Sprintf("http://localhost:%d%s", port, tc.path), nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tc.expectedStatus {
				t.Errorf("Expected status %d, got %d", tc.expectedStatus, resp.StatusCode)
			}

			if tc.checkBody {
				body, _ := io.ReadAll(resp.Body)
				if !strings.Contains(string(body), tc.bodyContains) {
					t.Errorf("Response body doesn't contain expected text '%s', got: %s", tc.bodyContains, string(body))
				}
			}
		})
	}
}

// TestArgoCDProxyBaselineResponseTimes establishes baseline response time metrics
// for the ArgoCD proxy operating without capture overhead.
func TestArgoCDProxyBaselineResponseTimes(t *testing.T) {
	if raceEnabled {
		t.Skip("absolute latency budget is not meaningful under -race: instrumentation overhead dominates")
	}

	callerPort := getAvailablePort(t)
	operatorPort := getAvailablePort(t)

	cfg := &Config{
		CallerPort:     callerPort,
		OperatorPort:   operatorPort,
		BaseURL:        fmt.Sprintf("http://localhost:%d", callerPort),
		SpecDir:        "../../spec",
		CaptureEnabled: false,
		CorpusDir:      "",
		AllowlistFile:  newBaselineAllowlistFile(t),
	}

	s := New(cfg)
	s.identityResolver = newLoopbackTestIdentityResolver()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = s.Shutdown(ctx) }()
	// Keep the response-time baseline focused on endpoint overhead after the
	// server is ready; startup login has its own readiness tests.
	s.setOpenBaoReady(true)

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Test endpoints that simulate typical ArgoCD API patterns
	endpoints := []struct {
		name string
		path string
	}{
		{"healthz", "/_seam/healthz"},
		{"readyz", "/_seam/readyz"},
		{"openapi", "/openapi.json"},
		{"docs", "/docs"},
	}

	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			iterations := 50
			var totalTime time.Duration
			var maxTime time.Duration
			minTime := time.Hour // Initialize to high value

			// Warmup
			for i := 0; i < 5; i++ {
				resp, err := http.Get(fmt.Sprintf("http://localhost:%d%s", s.config.CallerPort, endpoint.path))
				if err != nil {
					t.Fatalf("Warmup request failed: %v", err)
				}
				_ = resp.Body.Close()
			}

			// Actual measurements
			for i := 0; i < iterations; i++ {
				start := time.Now()
				resp, err := http.Get(fmt.Sprintf("http://localhost:%d%s", s.config.CallerPort, endpoint.path))
				if err != nil {
					t.Fatalf("Request failed: %v", err)
				}
				_ = resp.Body.Close()

				elapsed := time.Since(start)
				totalTime += elapsed

				if elapsed > maxTime {
					maxTime = elapsed
				}
				if elapsed < minTime {
					minTime = elapsed
				}

				if resp.StatusCode != http.StatusOK {
					t.Errorf("Request failed with status %d", resp.StatusCode)
				}
			}

			avgTime := totalTime / time.Duration(iterations)

			t.Logf("Baseline Response Times for %s:", endpoint.path)
			t.Logf("  Average: %v", avgTime)
			t.Logf("  Min: %v", minTime)
			t.Logf("  Max: %v", maxTime)

			// Baseline should be reasonably fast for local requests
			// Threshold: 20ms for average, 100ms for max
			if avgTime > 20*time.Millisecond {
				t.Errorf("Average response time too slow: %v (expected < 20ms)", avgTime)
			}
			if maxTime > 100*time.Millisecond {
				t.Errorf("Max response time too slow: %v (expected < 100ms)", maxTime)
			}
		})
	}
}

// TestArgoCDProxyBaselineConsistency tests that the proxy responds consistently
// across multiple requests without capture enabled.
//
// This is the one baseline case that deliberately does not install
// newLoopbackTestIdentityResolver: /_seam/readyz is a probe path, and a probe
// caller has no identity to resolve in production either. Stage 3 steps aside
// for it (identityExemptPaths), so these requests reach the readyz handler
// unauthenticated, exactly as a kubelet probe does. That is the property under
// test: stage 3's deny envelope carries a per-request request_id, so had the
// probe been left to default-deny, the twenty bodies below would differ and
// the comparison would fail. Passing here therefore shows the answer is stable
// because of the exemption, not because the caller holds a privileged
// identity.
func TestArgoCDProxyBaselineConsistency(t *testing.T) {
	callerPort := getAvailablePort(t)
	operatorPort := getAvailablePort(t)

	cfg := &Config{
		CallerPort:     callerPort,
		OperatorPort:   operatorPort,
		BaseURL:        fmt.Sprintf("http://localhost:%d", callerPort),
		SpecDir:        "../../spec",
		CaptureEnabled: false,
		CorpusDir:      "",
		AllowlistFile:  newBaselineAllowlistFile(t),
	}

	s := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = s.Shutdown(ctx) }()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Test consistency across multiple requests
	iterations := 20
	testPath := "/_seam/readyz"

	var responses []string
	var statusCodes []int

	for i := 0; i < iterations; i++ {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d%s", s.config.CallerPort, testPath))
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}

		statusCodes = append(statusCodes, resp.StatusCode)

		body, _ := io.ReadAll(resp.Body)
		responses = append(responses, string(body))
		_ = resp.Body.Close()
	}

	// Verify all responses are consistent
	firstStatus := statusCodes[0]
	firstBody := responses[0]

	for i := 1; i < iterations; i++ {
		if statusCodes[i] != firstStatus {
			t.Errorf("Status code inconsistent on iteration %d: got %d, expected %d",
				i, statusCodes[i], firstStatus)
		}
		if responses[i] != firstBody {
			t.Errorf("Response body inconsistent on iteration %d", i)
		}
	}

	t.Logf("Consistency check passed: %d identical responses", iterations)
}

// TestArgoCDProxyBaselineCaptureStatusDisabled verifies that capture status
// correctly reports disabled state when capture is not enabled.
func TestArgoCDProxyBaselineCaptureStatusDisabled(t *testing.T) {
	callerPort := getAvailablePort(t)
	operatorPort := getAvailablePort(t)

	cfg := &Config{
		CallerPort:     callerPort,
		OperatorPort:   operatorPort,
		BaseURL:        fmt.Sprintf("http://localhost:%d", callerPort),
		SpecDir:        "../../spec",
		CaptureEnabled: false, // Explicitly disabled
		CorpusDir:      "",
	}

	s := New(cfg)
	// Capture status lives on the operator port behind stage 3, and it reports
	// capture state the two-tier rule exists to withhold — it is identity-gated
	// on purpose, so the loopback caller presents the test identity instead of
	// asserting the deny.
	s.identityResolver = newLoopbackTestIdentityResolver()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = s.Shutdown(ctx) }()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Check capture status endpoint
	statusResp, err := http.Get(fmt.Sprintf("http://localhost:%d/_seam/capture/status", s.config.OperatorPort))
	if err != nil {
		t.Fatalf("Failed to get capture status: %v", err)
	}
	defer func() { _ = statusResp.Body.Close() }()

	if statusResp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 from capture status, got %d", statusResp.StatusCode)
	}

	var status map[string]interface{}
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatalf("Failed to decode status: %v", err)
	}

	// Verify capture is reported as disabled
	enabled, ok := status["enabled"].(bool)
	if !ok {
		t.Fatal("Expected 'enabled' field in status response")
	}
	if enabled {
		t.Error("Expected capture to be disabled, but status reports enabled")
	}

	// Verify entry count is 0 when disabled
	entryCount, ok := status["entry_count"].(float64)
	if !ok {
		t.Fatal("Expected 'entry_count' field in status response")
	}
	if entryCount != 0 {
		t.Errorf("Expected entry_count to be 0 when disabled, got %d", int(entryCount))
	}

	t.Log("Capture status correctly reports disabled state")
}

// TestArgoCDProxyBaselineNoCorpusLeakage verifies that no corpus files or
// directories are created when capture is disabled.
func TestArgoCDProxyBaselineNoCorpusLeakage(t *testing.T) {
	callerPort := getAvailablePort(t)
	operatorPort := getAvailablePort(t)
	tempDir := t.TempDir()

	cfg := &Config{
		CallerPort:     callerPort,
		OperatorPort:   operatorPort,
		BaseURL:        fmt.Sprintf("http://localhost:%d", callerPort),
		SpecDir:        "../../spec",
		CaptureEnabled: false,
		CorpusDir:      tempDir,
	}

	s := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = s.Shutdown(ctx) }()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Make some requests
	for i := 0; i < 10; i++ {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/docs", s.config.CallerPort))
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		_ = resp.Body.Close()
	}

	// Verify no corpus files were created
	files, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("Failed to read temp dir: %v", err)
	}

	if len(files) > 0 {
		t.Errorf("Expected no files in corpus directory when capture disabled, found %d files", len(files))
		for _, f := range files {
			t.Logf("  Unexpected file: %s", f.Name())
		}
	}

	t.Log("No corpus files created with capture disabled")
}

// getAvailablePort binds an ephemeral TCP port, closes the listener, and
// returns the port number so a caller can pre-compute config (e.g. BaseURL)
// before the server itself binds it.
func getAvailablePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate available port: %v", err)
	}
	defer func() { _ = l.Close() }()

	return l.Addr().(*net.TCPAddr).Port
}
