package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"time"
)

// Config configures a Tailscale client
type Config struct {
	// API endpoint configuration
	APIKey  string // Tailscale API key from OpenBao or environment
	Tailnet string // Tailnet name (e.g., "ardenone")
	BaseURL string // Default: "https://api.tailscale.com"

	// Ephemeral key configuration
	DefaultExpiry time.Duration // Default: 90 days
	DefaultTags   []string      // Default: ["tag:needle-worker"]

	// HTTP client configuration
	HTTPClient *http.Client
	Timeout    time.Duration

	// Cache configuration
	CacheTTL      time.Duration // Default: 5 minutes
	CacheHoldDown time.Duration // Default: 30 seconds

	// Logging
	EnableDebugLogging bool
}

// Client is the Tailscale API client
type Client struct {
	config     Config
	apiKey     string // Cached API key
	cache      *keyCache
	httpClient *http.Client
	logger     *log.Logger
}

// New creates a new Tailscale client
func New(cfg Config) (*Client, error) {
	// Validate configuration
	if cfg.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	if cfg.Tailnet == "" {
		return nil, ErrNoTailnet
	}

	// Set defaults
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.tailscale.com"
	}
	if cfg.DefaultExpiry == 0 {
		cfg.DefaultExpiry = 90 * 24 * time.Hour // 90 days
	}
	if len(cfg.DefaultTags) == 0 {
		cfg.DefaultTags = []string{"tag:needle-worker"}
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 5 * time.Minute
	}
	if cfg.CacheHoldDown == 0 {
		cfg.CacheHoldDown = 30 * time.Second
	}

	// Validate expiry duration
	if cfg.DefaultExpiry < 24*time.Hour || cfg.DefaultExpiry > 90*24*time.Hour {
		return nil, fmt.Errorf("%w: expiry must be between 1 and 90 days", ErrInvalidExpiry)
	}

	// Configure HTTP client with connection pooling
	if cfg.HTTPClient == nil {
		timeout := cfg.Timeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		cfg.HTTPClient = &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	}

	// Create logger
	logger := log.New(log.Writer(), "[tailscale] ", log.LstdFlags)
	if !cfg.EnableDebugLogging {
		logger.SetOutput(io.Discard)
	}

	return &Client{
		config:     cfg,
		apiKey:     cfg.APIKey,
		httpClient: cfg.HTTPClient,
		cache:      newKeyCache(cfg.CacheTTL, cfg.CacheHoldDown),
		logger:     logger,
	}, nil
}

// CreateEphemeralKey creates a new ephemeral key for a NEEDLE worker
func (c *Client) CreateEphemeralKey(ctx context.Context, workerID string) (*Key, error) {
	return c.CreateEphemeralKeyWithTags(ctx, workerID, c.config.DefaultTags)
}

// CreateEphemeralKeyWithTags creates a new ephemeral key with custom tags
func (c *Client) CreateEphemeralKeyWithTags(ctx context.Context, workerID string, tags []string) (*Key, error) {
	// Check cache first
	if cachedKey, ok := c.cache.Get(workerID); ok {
		c.logger.Printf("Cache hit for worker %s", workerID)
		return cachedKey, nil
	}

	// Check hold-down period
	if c.cache.IsInHoldDown() {
		c.logger.Printf("Cache in hold-down period for worker %s", workerID)
		return nil, ErrCacheHoldDown
	}

	req := CreateKeyRequest{
		Capabilities: KeyCapabilities{
			Devices: DeviceCapabilities{
				Create: DeviceCreateOptions{
					Reusable:      false,
					Ephemeral:     true,
					Tags:          tags,
					Preauthorized: true,
				},
			},
		},
		ExpirySeconds: int64(c.config.DefaultExpiry.Seconds()),
		Description:   fmt.Sprintf("NEEDLE worker: %s", workerID),
	}

	key, err := c.createKey(ctx, req)
	if err != nil {
		c.cache.MarkFailure()
		return nil, err
	}

	// Cache the key
	c.cache.Set(workerID, key)
	c.logger.Printf("Created ephemeral key for worker %s (expires: %s)", workerID, key.Expires.Format(time.RFC3339))

	return key, nil
}

// createKey makes the actual API call to create a key
func (c *Client) createKey(ctx context.Context, req CreateKeyRequest) (*Key, error) {
	url := fmt.Sprintf("%s/api/v2/tailnet/%s/keys", c.config.BaseURL, c.config.Tailnet)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	if c.config.EnableDebugLogging {
		dump, err := httputil.DumpRequestOut(httpReq, false)
		if err == nil {
			c.logger.Printf("Request:\n%s", dump)
		}
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if c.config.EnableDebugLogging {
		c.logger.Printf("Response status: %d", resp.StatusCode)
		c.logger.Printf("Response body: %s", string(respBody))
	}

	// Handle error responses
	if resp.StatusCode != http.StatusOK {
		return c.handleErrorResponse(resp.StatusCode, respBody)
	}

	// Parse successful response
	var key Key
	if err := json.Unmarshal(respBody, &key); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidResponse, err)
	}

	return &key, nil
}

// handleErrorResponse processes error responses from the API
func (c *Client) handleErrorResponse(statusCode int, body []byte) (*Key, error) {
	// Try to extract error message from response
	var errMsg struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &errMsg)

	msg := errMsg.Message
	if msg == "" {
		msg = errMsg.Error
	}
	if msg == "" {
		msg = http.StatusText(statusCode)
	}

	apiErr := NewAPIError(statusCode, msg, nil)

	switch statusCode {
	case http.StatusTooManyRequests:
		c.logger.Printf("Rate limited by Tailscale API")
		return nil, fmt.Errorf("%w: %s", ErrRateLimited, apiErr)
	case http.StatusUnauthorized, http.StatusForbidden:
		c.logger.Printf("Authentication failed with Tailscale API")
		return nil, fmt.Errorf("%w: %s", ErrAuthFailed, apiErr)
	default:
		c.logger.Printf("API error (status %d): %s", statusCode, msg)
		return nil, fmt.Errorf("%w: %s", ErrKeyCreation, apiErr)
	}
}

// ListKeys retrieves all keys for the tailnet
func (c *Client) ListKeys(ctx context.Context) ([]Key, error) {
	url := fmt.Sprintf("%s/api/v2/tailnet/%s/keys", c.config.BaseURL, c.config.Tailnet)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		_, err := c.handleErrorResponse(resp.StatusCode, body)
		return nil, err
	}

	var listResp ListKeysResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidResponse, err)
	}

	return listResp.Keys, nil
}

// DeleteKey deletes a key by ID
func (c *Client) DeleteKey(ctx context.Context, keyID string) error {
	url := fmt.Sprintf("%s/api/v2/tailnet/%s/keys/%s", c.config.BaseURL, c.config.Tailnet, keyID)

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		_, err := c.handleErrorResponse(resp.StatusCode, body)
		if err != nil {
			return err
		}
	}

	return nil
}

// GetCacheStats returns cache statistics
func (c *Client) GetCacheStats() (size int, inHoldDown bool) {
	return c.cache.Size(), c.cache.IsInHoldDown()
}

// ClearCache clears the key cache
func (c *Client) ClearCache() {
	c.cache.Clear()
}

// InvalidateWorker removes a specific worker's key from cache
func (c *Client) InvalidateWorker(workerID string) {
	c.cache.Delete(workerID)
}

// CleanupExpired removes expired entries from the cache and returns count
func (c *Client) CleanupExpired() int {
	return c.cache.Cleanup()
}

// SetAPIKey updates the API key (useful for rotation)
func (c *Client) SetAPIKey(apiKey string) {
	c.apiKey = apiKey
}

// GetBaseURL returns the configured base URL
func (c *Client) GetBaseURL() string {
	return c.config.BaseURL
}

// GetTailnet returns the configured tailnet name
func (c *Client) GetTailnet() string {
	return c.config.Tailnet
}
