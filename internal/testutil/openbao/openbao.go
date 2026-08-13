// Package openbao provides utilities for testing SEAM's integration with OpenBao
// using a local development instance in dev-token mode.
package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Client represents an OpenBao client for testing.
type Client struct {
	baseURL    string
	devToken   string
	httpClient *http.Client
	mu         sync.RWMutex
}

// ServerConfig holds configuration for the OpenBao dev server.
type ServerConfig struct {
	// DevToken is the root token for dev mode (default: "dev-root-token")
	DevToken string
	// ListenAddr is the address OpenBao listens on (default: "localhost:8200")
	ListenAddr string
}

// Server represents a running OpenBao dev server.
type Server struct {
	cfg     ServerConfig
	cmd     *exec.Cmd
	client  *Client
	tmpDir  string
	cleanup func()
}

// NewClient creates a new OpenBao client for testing.
func NewClient(baseURL, devToken string) *Client {
	return &Client{
		baseURL:    baseURL,
		devToken:   devToken,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// WriteSecret writes a secret to the given path.
func (c *Client) WriteSecret(ctx context.Context, path string, data map[string]interface{}) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	url := fmt.Sprintf("%s/v1/secret/%s", c.baseURL, path)
	payload := map[string]interface{}{
		"data": data,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("X-Vault-Token", c.devToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("write secret failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// ReadSecret reads a secret from the given path.
func (c *Client) ReadSecret(ctx context.Context, path string) (map[string]interface{}, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	url := fmt.Sprintf("%s/v1/secret/%s", c.baseURL, path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("X-Vault-Token", c.devToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("read secret failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Data map[string]interface{} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Data, nil
}

// DeleteSecret deletes a secret from the given path.
func (c *Client) DeleteSecret(ctx context.Context, path string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	url := fmt.Sprintf("%s/v1/secret/%s", c.baseURL, path)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("X-Vault-Token", c.devToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete secret failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// NewServer starts a new OpenBao dev server for testing.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.DevToken == "" {
		cfg.DevToken = "dev-root-token"
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "localhost:8200"
	}

	// Create a temporary directory for OpenBao data
	tmpDir, err := os.MkdirTemp("", "openbao-test-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	// Create logs directory
	logDir := filepath.Join(tmpDir, "logs")
	if err := os.Mkdir(logDir, 0755); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	// Find the openbao binary
	openbaoPath, err := exec.LookPath("openbao" /* or "vault" */)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("openbao not found in PATH: %w (install from https://openbao.org)", err)
	}

	logFile := filepath.Join(logDir, "openbao.log")

	// Start OpenBao in dev mode
	cmd := exec.Command(openbaoPath, "server",
		"-dev",
		"-dev-root-token-id="+cfg.DevToken,
		"-dev-listen-address="+cfg.ListenAddr,
		"-log-file="+logFile,
		"-log-level=info",
	)

	cmd.Dir = tmpDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("start openbao server: %w", err)
	}

	// Give the server a moment to start
	time.Sleep(2 * time.Second)

	server := &Server{
		cfg:    cfg,
		cmd:    cmd,
		tmpDir: tmpDir,
	}

	// Create a client for this server
	server.client = NewClient(fmt.Sprintf("http://%s", cfg.ListenAddr), cfg.DevToken)

	// Wait for server to be ready
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.waitForReady(ctx); err != nil {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("wait for server ready: %w", err)
	}

	// Set up cleanup function
	server.cleanup = func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
		os.RemoveAll(tmpDir)
	}

	return server, nil
}

// waitForReady waits for the OpenBao server to be ready to accept requests.
func (s *Server) waitForReady(ctx context.Context) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	healthURL := fmt.Sprintf("http://%s/v1/sys/health", s.cfg.ListenAddr)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
			// Health endpoint doesn't need auth
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK || resp.StatusCode == 501 {
					// 200 = initialized, 501 = not initialized (but ready in dev mode)
					return nil
				}
			}
		}
	}
}

// Close stops the OpenBao server and cleans up resources.
func (s *Server) Close() error {
	if s.cleanup != nil {
		s.cleanup()
	}
	return nil
}

// Client returns the OpenBao client for this server.
func (s *Server) Client() *Client {
	return s.client
}

// BaseURL returns the base URL of the OpenBao server.
func (s *Server) BaseURL() string {
	return fmt.Sprintf("http://%s", s.cfg.ListenAddr)
}

// DevToken returns the dev token for this server.
func (s *Server) DevToken() string {
	return s.cfg.DevToken
}

// SetupTestSecrets creates test secrets for SEAM integration tests.
func (s *Server) SetupTestSecrets(ctx context.Context) error {
	// Create the seam/routes prefix structure
	secrets := map[string]map[string]interface{}{
		"seam/routes/testservice/token": {
			"token": "test-service-token-12345",
			"type":  "bearer",
		},
		"seam/routes/testservice/api_key": {
			"api_key": "test-api-key-67890",
			"type":    "header",
		},
		"seam/routes/k8s/readonly-token": {
			"token": "k8s-readonly-token-abcde",
		},
		"seam/routes/twitterapi/bearer": {
			"token": "twitter-api-test-token",
		},
		"seam/routes/zai/credential": {
			"api_key": "zai-test-credential",
		},
	}

	for path, data := range secrets {
		if err := s.client.WriteSecret(ctx, path, data); err != nil {
			return fmt.Errorf("write secret %s: %w", path, err)
		}
	}

	return nil
}

// RotateCredential rotates a credential to test 401 self-heal behavior.
func (s *Server) RotateCredential(ctx context.Context, path, key string) (string, error) {
	// Read current value
	data, err := s.client.ReadSecret(ctx, path)
	if err != nil {
		return "", fmt.Errorf("read current secret: %w", err)
	}

	// Generate new value
	oldValue, ok := data[key].(string)
	if !ok {
		return "", fmt.Errorf("key %s not found or not a string", key)
	}

	// Simple rotation: append "-rotated"
	newValue := oldValue + "-rotated-" + time.Now().Format("20060102150405")

	// Write back
	data[key] = newValue
	if err := s.client.WriteSecret(ctx, path, data); err != nil {
		return "", fmt.Errorf("write rotated secret: %w", err)
	}

	return newValue, nil
}

// NewClientForTesting creates an OpenBao client for use in tests.
// It attempts to connect to a running OpenBao instance.
// If TEST_OPENBAO_ADDR is set, it uses that; otherwise it tries localhost:8200.
// If TEST_OPENBAO_TOKEN is set, it uses that; otherwise it uses "dev-root-token".
func NewClientForTesting() (*Client, error) {
	addr := os.Getenv("TEST_OPENBAO_ADDR")
	if addr == "" {
		addr = "http://localhost:8200"
	}

	token := os.Getenv("TEST_OPENBAO_TOKEN")
	if token == "" {
		token = "dev-root-token"
	}

	client := NewClient(addr, token)

	// Verify connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try to read a path (even if it doesn't exist, we should get a proper response)
	_, err := client.ReadSecret(ctx, "test/connection")
	if err != nil {
		// Check if it's a connection error vs a "secret not found" error
		if strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "connect: connection refused") {
			return nil, fmt.Errorf("cannot connect to OpenBao at %s: %w (set TEST_OPENBAO_ADDR or start a dev server)", addr, err)
		}
		// Other errors are OK (like 404 for non-existent secret)
	}

	return client, nil
}

// SkipIfNoOpenBao skips the test if OpenBao is not available.
// This is a convenience function for test setup.
func SkipIfNoOpenBao(t *testing.T) {
	t.Helper()

	_, err := NewClientForTesting()
	if err != nil {
		t.Skipf("OpenBao not available: %v", err)
	}
}

// ManageTestServer is a test helper that manages an OpenBao server lifecycle.
// It starts a server before the test and stops it after.
func ManageTestServer(t *testing.T) *Server {
	t.Helper()

	cfg := ServerConfig{
		DevToken:   "test-root-token",
		ListenAddr: "localhost:18200", // Non-default port to avoid conflicts
	}

	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("failed to start OpenBao test server: %v", err)
	}

	// Set up test secrets
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.SetupTestSecrets(ctx); err != nil {
		server.Close()
		t.Fatalf("failed to set up test secrets: %v", err)
	}

	t.Cleanup(func() {
		server.Close()
	})

	return server
}
