// Package vault contains SEAM's only OpenBao client and secret cache.
//
// Secret values returned by this package are intended to be short-lived
// process-memory values. This package deliberately does not log values, write
// them to disk, or include them in status snapshots.
package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

const (
	DefaultCacheTTL       = 30 * time.Second
	DefaultFetchHoldDown  = 5 * time.Second
	DefaultMount          = "secret"
	DefaultOpenBaoAddress = "http://127.0.0.1:8200"
	DefaultKubernetesRole = "seam"
	DefaultServiceAccount = "/var/run/secrets/kubernetes.io/serviceaccount/token"

	// The response is metadata plus a bounded data object. A secret larger than
	// this is not useful for SEAM's header/query injection and should not be
	// allowed to consume unbounded memory.
	maxSecretResponseBytes = 16 << 20
)

// AuthMode is the authentication mechanism used by the client.
type AuthMode string

const (
	AuthModeKubernetes AuthMode = "kubernetes"
	AuthModeDevToken   AuthMode = "dev-token"
)

// Config configures a Client. Address, Role, DevToken, MountPath, and
// AuthMountPath have corresponding environment variables in
// ConfigFromEnv/NewFromEnv.
type Config struct {
	Address string
	// Addr is accepted as an alias for Address for callers that use the OpenBao
	// SDK's conventional field name.
	Addr string
	Role string

	// DevToken is permitted only outside a detected Kubernetes environment.
	// Token is an alias retained for callers that call the local credential a
	// token rather than a dev token.
	DevToken string
	Token    string

	// AuthToken supplies an already-authenticated in-memory token. It is useful
	// for embedding the client behind another auth mechanism and is never
	// persisted. DevToken takes precedence when selecting dev-token mode.
	AuthToken string

	// MountPath is the KV secrets engine mount, such as "secret".
	MountPath string
	// AuthMountPath is the Kubernetes auth method mount, such as
	// "k8s-rs-manager". It is separate from MountPath because OpenBao may
	// mount auth methods and secret engines at different paths.
	AuthMountPath string

	ServiceAccountTokenPath string
	// ServiceAccountToken is a test/developer injection point. Production
	// callers should leave it empty so the projected token is read from disk.
	ServiceAccountToken string

	// InCluster overrides environment/filesystem detection when non-nil. It is
	// intentionally an explicit test hook; a production caller should leave it
	// nil and use Kubernetes' projected token/environment.
	InCluster *bool

	CacheTTL      time.Duration
	FetchHoldDown time.Duration
	HTTPClient    *http.Client
	// Now is injectable for deterministic cache/hold-down tests.
	Now func() time.Time
}

// ConfigFromEnv builds local or in-cluster configuration without reading a
// secret value into a log or status object. The accepted names are kept here,
// at the package boundary, so the server does not need to know auth details.
func ConfigFromEnv() Config {
	devToken := firstNonEmpty(
		os.Getenv("SEAM_OPENBAO_DEV_TOKEN"),
		os.Getenv("OPENBAO_DEV_TOKEN"),
		os.Getenv("OPENBAO_TOKEN"),
	)
	return Config{
		Address:                 firstNonEmpty(os.Getenv("SEAM_OPENBAO_ADDR"), os.Getenv("OPENBAO_ADDR")),
		Role:                    firstNonEmpty(os.Getenv("SEAM_OPENBAO_ROLE"), os.Getenv("OPENBAO_ROLE")),
		DevToken:                devToken,
		MountPath:               os.Getenv("SEAM_OPENBAO_MOUNT"),
		AuthMountPath:           firstNonEmpty(os.Getenv("SEAM_OPENBAO_AUTH_MOUNT"), os.Getenv("OPENBAO_AUTH_MOUNT")),
		ServiceAccountTokenPath: os.Getenv("SEAM_OPENBAO_SA_TOKEN_PATH"),
	}
}

// NewFromEnv creates a client using ConfigFromEnv.
func NewFromEnv() (*Client, error) { return New(ConfigFromEnv()) }

// NewClient is an alias for New.
func NewClient(cfg Config) (*Client, error) { return New(cfg) }

// New creates a client. Authentication is lazy: this validates the auth mode
// and configuration without making a network request. Call Login when startup
// readiness needs to gate first traffic on a successful Kubernetes login.
func New(cfg Config) (*Client, error) {
	address := strings.TrimRight(firstNonEmpty(cfg.Address, cfg.Addr, os.Getenv("OPENBAO_ADDR")), "/")
	if address == "" {
		address = DefaultOpenBaoAddress
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid OpenBao address")
	}

	role := firstNonEmpty(cfg.Role, os.Getenv("OPENBAO_ROLE"), DefaultKubernetesRole)
	mount := strings.Trim(strings.TrimSpace(firstNonEmpty(cfg.MountPath, "secret")), "/")
	if mount == "" {
		return nil, fmt.Errorf("OpenBao mount path must not be empty")
	}
	authMount := strings.Trim(strings.TrimSpace(firstNonEmpty(cfg.AuthMountPath, "kubernetes")), "/")
	if authMount == "" {
		return nil, fmt.Errorf("OpenBao auth mount path must not be empty")
	}

	saPath := firstNonEmpty(cfg.ServiceAccountTokenPath, DefaultServiceAccount)
	inCluster := detectInCluster(cfg.InCluster, saPath)
	devToken := firstNonEmpty(cfg.DevToken, cfg.Token)
	if inCluster && devToken != "" {
		return nil, ErrDevTokenInCluster
	}

	mode := AuthModeKubernetes
	if !inCluster {
		if devToken != "" {
			mode = AuthModeDevToken
		} else if cfg.AuthToken == "" {
			return nil, fmt.Errorf("OpenBao requires a dev token outside Kubernetes")
		}
	}

	ttl := cfg.CacheTTL
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	holdDown := cfg.FetchHoldDown
	if holdDown <= 0 {
		holdDown = DefaultFetchHoldDown
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	c := &Client{
		address:                 address,
		role:                    role,
		mountPath:               mount,
		authMountPath:           authMount,
		authMode:                mode,
		devToken:                devToken,
		authToken:               cfg.AuthToken,
		serviceAccountTokenPath: saPath,
		serviceAccountToken:     cfg.ServiceAccountToken,
		cacheTTL:                ttl,
		fetchHoldDown:           holdDown,
		httpClient:              client,
		now:                     now,
		cache:                   make(map[string]cacheEntry),
		failures:                make(map[string]failureEntry),
		inflight:                make(map[string]*fetchCall),
	}
	return c, nil
}

// ErrDevTokenInCluster is returned when a local token is supplied in an
// environment that looks like a Kubernetes pod. This fail-closed check avoids
// accidentally deploying a developer credential.
var ErrDevTokenInCluster = errors.New("dev-token auth is refused in a Kubernetes environment")

// Client is an OpenBao client with a per-path in-memory cache. Client is safe
// for concurrent use.
type Client struct {
	address                 string
	role                    string
	mountPath               string
	authMountPath           string
	authMode                AuthMode
	devToken                string
	authToken               string
	authTokenExpiry         time.Time
	serviceAccountTokenPath string
	serviceAccountToken     string

	cacheTTL      time.Duration
	fetchHoldDown time.Duration
	httpClient    *http.Client
	now           func() time.Time

	mu       sync.Mutex
	cache    map[string]cacheEntry
	failures map[string]failureEntry
	inflight map[string]*fetchCall

	hits    uint64
	misses  uint64
	fetches uint64
}

type cacheEntry struct {
	secret    Secret
	expiresAt time.Time
}

type failureEntry struct {
	until time.Time
	class FailureClass
}

type fetchCall struct {
	done   chan struct{}
	secret Secret
	err    error
}

// Secret is the decoded KV data at an OpenBao path. It is deliberately owned
// by this package so route handlers do not need to implement OpenBao's wire
// format. Callers should use it immediately and not retain it.
type Secret map[string]any

// Value returns a string-valued field without logging or formatting the whole
// secret. Header/query injection should normally use this helper.
func (s Secret) Value(key string) ([]byte, error) {
	value, ok := s[key]
	if !ok {
		return nil, fmt.Errorf("secret field is missing")
	}
	switch typed := value.(type) {
	case string:
		return []byte(typed), nil
	case []byte:
		return append([]byte(nil), typed...), nil
	default:
		return nil, fmt.Errorf("secret field is not a string")
	}
}

// GetSecret returns a cached or freshly fetched secret. The cache key is the
// canonical resolved vault path, not the request route.
func (c *Client) GetSecret(ctx context.Context, vaultPath string) (Secret, error) {
	if c == nil {
		return nil, fmt.Errorf("OpenBao client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key, err := canonicalPath(vaultPath)
	if err != nil {
		return nil, err
	}
	now := c.now()

	c.mu.Lock()
	if entry, ok := c.cache[key]; ok {
		if now.Before(entry.expiresAt) {
			c.hits++
			secret := cloneSecret(entry.secret)
			c.mu.Unlock()
			return secret, nil
		}
		// Expired values are deleted before any remote attempt. This is the
		// invariant that prevents stale-on-outage behavior.
		delete(c.cache, key)
	}
	c.misses++
	if failure, ok := c.failures[key]; ok {
		if now.Before(failure.until) {
			err := newUnavailableError(failure.class, failure.until, now)
			c.mu.Unlock()
			return nil, err
		}
		delete(c.failures, key)
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-call.done:
			if call.err != nil {
				return nil, call.err
			}
			return cloneSecret(call.secret), nil
		case <-ctxDone(ctx):
			return nil, ctx.Err()
		}
	}
	call := &fetchCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.fetches++
	c.mu.Unlock()

	secret, fetchErr := c.fetch(ctx, key)
	completedAt := c.now()
	c.mu.Lock()
	delete(c.inflight, key)
	call.secret = cloneSecret(secret)
	call.err = fetchErr
	if fetchErr == nil {
		c.cache[key] = cacheEntry{secret: cloneSecret(secret), expiresAt: completedAt.Add(c.cacheTTL)}
		delete(c.failures, key)
	} else if class, ok := failureClassOf(fetchErr); ok {
		c.failures[key] = failureEntry{until: completedAt.Add(c.fetchHoldDown), class: class}
		call.err = newUnavailableError(class, completedAt.Add(c.fetchHoldDown), completedAt)
	} else {
		// Configuration/shape errors do not indicate an OpenBao outage and
		// therefore do not trigger the hold-down.
		call.err = fetchErr
	}
	close(call.done)
	result := cloneSecret(call.secret)
	resultErr := call.err
	c.mu.Unlock()
	return result, resultErr
}

// Get is a concise alias for GetSecret.
func (c *Client) Get(ctx context.Context, vaultPath string) (Secret, error) {
	return c.GetSecret(ctx, vaultPath)
}

// ReadSecret is an alias matching OpenBao's read terminology.
func (c *Client) ReadSecret(ctx context.Context, vaultPath string) (Secret, error) {
	return c.GetSecret(ctx, vaultPath)
}

// RefreshAfterUnauthorized eagerly invalidates a path and fetches it again.
// If OpenBao is unavailable, the returned error is the same structured
// secret-store-unavailable error used for an ordinary cache miss.
func (c *Client) RefreshAfterUnauthorized(ctx context.Context, vaultPath string) (Secret, error) {
	c.Invalidate(vaultPath)
	return c.GetSecret(ctx, vaultPath)
}

// FetchFresh is a descriptive alias for RefreshAfterUnauthorized. It is
// useful to callers that have already observed a 401 and need to make the
// invalidation/refetch boundary explicit.
func (c *Client) FetchFresh(ctx context.Context, vaultPath string) (Secret, error) {
	return c.RefreshAfterUnauthorized(ctx, vaultPath)
}

// Invalidate removes a path from the cache. It intentionally does not remove
// an active failure hold-down: after a failed refresh, retry storms must still
// be suppressed until the operator-selected interval elapses.
func (c *Client) Invalidate(vaultPath string) {
	if c == nil {
		return
	}
	key, err := canonicalPath(vaultPath)
	if err != nil {
		return
	}
	c.mu.Lock()
	delete(c.cache, key)
	c.mu.Unlock()
}

// InvalidateSecret is an alias for Invalidate.
func (c *Client) InvalidateSecret(vaultPath string) { c.Invalidate(vaultPath) }

// Login performs the initial authentication. It is safe to call repeatedly;
// an already-valid in-memory token is reused. A mid-life outage does not alter
// readiness—callers use this for startup gating, while GetSecret reports
// request-scoped failures after startup.
func (c *Client) Login(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("OpenBao client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if c.authMode == AuthModeDevToken {
		return nil
	}
	_, err := c.token(ctx, false)
	return err
}

// AuthMode reports the stable status value used by /config/status.
func (c *Client) AuthMode() string {
	if c == nil {
		return ""
	}
	return string(c.authMode)
}

// Status is safe to expose to an operator. It contains no token, secret, or
// vault path.
type Status struct {
	AuthMode             string `json:"authMode"`
	CacheTTLSeconds      int    `json:"cacheTTLSeconds"`
	FetchHoldDownSeconds int    `json:"fetchHoldDownSeconds"`
	CachedPaths          int    `json:"cachedPaths"`
	LastFailureClass     string `json:"lastFailureClass,omitempty"`
}

// Status returns cache/auth metadata without secret material.
func (c *Client) Status() Status {
	if c == nil {
		return Status{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	status := Status{
		AuthMode:             string(c.authMode),
		CacheTTLSeconds:      int(c.cacheTTL / time.Second),
		FetchHoldDownSeconds: int(c.fetchHoldDown / time.Second),
		CachedPaths:          len(c.cache),
	}
	for _, failure := range c.failures {
		status.LastFailureClass = string(failure.class)
		break
	}
	return status
}

// ConfigStatus returns a map convenient for embedding in a larger status
// response. It intentionally exposes only operator-safe metadata.
func (c *Client) ConfigStatus() map[string]any {
	status := c.Status()
	return map[string]any{
		"authMode":             status.AuthMode,
		"cacheTTLSeconds":      status.CacheTTLSeconds,
		"fetchHoldDownSeconds": status.FetchHoldDownSeconds,
		"cachedPaths":          status.CachedPaths,
	}
}

// CacheStats contains non-secret counters useful to tests and operator
// metrics. It never reports a path or value.
type CacheStats struct {
	Entries uint64
	Hits    uint64
	Misses  uint64
	Fetches uint64
}

func (c *Client) CacheStats() CacheStats {
	if c == nil {
		return CacheStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{Entries: uint64(len(c.cache)), Hits: c.hits, Misses: c.misses, Fetches: c.fetches}
}

// FailureClass is a safe classification of an OpenBao fetch failure. It does
// not contain server response text, URLs, or credentials.
type FailureClass string

const (
	FailureNetwork     FailureClass = "network"
	FailureTimeout     FailureClass = "timeout"
	FailureUnavailable FailureClass = "unavailable"
	FailureAuth        FailureClass = "authentication"
	FailureResponse    FailureClass = "invalid-response"
)

// SecretStoreUnavailableError is the request-safe error for an OpenBao outage.
// HTTP handlers can use RetryAfterSeconds to set Retry-After and Code to emit
// the structured error condition without naming an upstream.
type SecretStoreUnavailableError struct {
	Dependency string
	Class      FailureClass
	RetryAt    time.Time
	Now        time.Time
}

func newUnavailableError(class FailureClass, retryAt, now time.Time) *SecretStoreUnavailableError {
	return &SecretStoreUnavailableError{Dependency: "OpenBao", Class: class, RetryAt: retryAt, Now: now}
}

func (e *SecretStoreUnavailableError) Error() string {
	if e == nil {
		return "secret store unavailable"
	}
	return "secret store unavailable: OpenBao"
}

// Code is the stable structured error condition.
func (e *SecretStoreUnavailableError) Code() string { return "secret-store-unavailable" }

// HTTPStatus is the status a request handler should use for this condition.
func (e *SecretStoreUnavailableError) HTTPStatus() int { return http.StatusServiceUnavailable }

// RetryAfter returns the remaining hold-down duration, rounded up to avoid
// telling a caller to retry before a new fetch is allowed.
func (e *SecretStoreUnavailableError) RetryAfter() time.Duration {
	if e == nil {
		return 0
	}
	remaining := e.RetryAt.Sub(e.Now)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (e *SecretStoreUnavailableError) RetryAfterSeconds() int {
	if e == nil {
		return 0
	}
	remaining := e.RetryAfter()
	seconds := int((remaining + time.Second - 1) / time.Second)
	if seconds < 1 && remaining > 0 {
		return 1
	}
	return seconds
}

// IsSecretStoreUnavailable reports whether err is the OpenBao outage error.
func IsSecretStoreUnavailable(err error) bool {
	var target *SecretStoreUnavailableError
	return errors.As(err, &target)
}

// RetryAfterSeconds extracts the caller-facing retry delay from an outage
// error. Non-outage errors return zero, allowing handlers to set Retry-After
// only for secret-store failures.
func RetryAfterSeconds(err error) int {
	var target *SecretStoreUnavailableError
	if !errors.As(err, &target) {
		return 0
	}
	return target.RetryAfterSeconds()
}

func failureClassOf(err error) (FailureClass, bool) {
	var target *fetchFailure
	if !errors.As(err, &target) {
		return "", false
	}
	return target.class, true
}

// fetchFailure is internal and intentionally hides the originating error.
type fetchFailure struct{ class FailureClass }

func (e *fetchFailure) Error() string { return "OpenBao fetch failed" }

func (c *Client) fetch(ctx context.Context, vaultPath string) (Secret, error) {
	token, err := c.token(ctx, false)
	if err != nil {
		return nil, err
	}
	secret, status, err := c.readWithToken(ctx, vaultPath, token)
	if err == nil {
		return secret, nil
	}
	if status == http.StatusUnauthorized && c.authMode == AuthModeKubernetes {
		// OpenBao may have expired the auth token. Re-authenticate once, but
		// never retry an arbitrary secret read more than once.
		c.clearAuthToken()
		freshToken, authErr := c.token(ctx, true)
		if authErr != nil {
			return nil, authErr
		}
		secret, _, readErr := c.readWithToken(ctx, vaultPath, freshToken)
		return secret, readErr
	}
	return nil, err
}

func (c *Client) readWithToken(ctx context.Context, vaultPath, token string) (Secret, int, error) {
	requestURL := c.address + "/v1/" + url.PathEscape(c.mountPath) + "/data/" + escapePath(vaultPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, 0, &fetchFailure{class: FailureResponse}
	}
	req.Header.Set("X-Vault-Token", token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		class := FailureNetwork
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			class = FailureTimeout
		}
		return nil, 0, &fetchFailure{class: class}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, resp.StatusCode, &fetchFailure{class: FailureAuth}
	}
	if resp.StatusCode >= http.StatusInternalServerError || resp.StatusCode == http.StatusTooManyRequests {
		return nil, resp.StatusCode, &fetchFailure{class: FailureUnavailable}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, resp.StatusCode, &fetchFailure{class: FailureResponse}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSecretResponseBytes+1))
	if err != nil || len(body) > maxSecretResponseBytes {
		return nil, resp.StatusCode, &fetchFailure{class: FailureResponse}
	}
	secret, err := decodeSecret(body)
	if err != nil {
		return nil, resp.StatusCode, &fetchFailure{class: FailureResponse}
	}
	return secret, resp.StatusCode, nil
}

func (c *Client) token(ctx context.Context, force bool) (string, error) {
	if c.authMode == AuthModeDevToken {
		return c.devToken, nil
	}
	c.mu.Lock()
	if !force && c.authToken != "" && (c.authTokenExpiry.IsZero() || c.now().Before(c.authTokenExpiry)) {
		token := c.authToken
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	jwt := c.serviceAccountToken
	if jwt == "" {
		data, err := os.ReadFile(c.serviceAccountTokenPath)
		if err != nil || len(data) == 0 {
			return "", nilFetchFailure(FailureAuth)
		}
		jwt = string(data)
	}
	payload, err := json.Marshal(map[string]string{"role": c.role, "jwt": jwt})
	if err != nil {
		return "", nilFetchFailure(FailureResponse)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.address+"/v1/auth/"+c.authMountPath+"/login", strings.NewReader(string(payload)))
	if err != nil {
		return "", nilFetchFailure(FailureResponse)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", nilFetchFailure(FailureTimeout)
		}
		return "", nilFetchFailure(FailureNetwork)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusInternalServerError || resp.StatusCode == http.StatusTooManyRequests {
		return "", nilFetchFailure(FailureUnavailable)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", nilFetchFailure(FailureAuth)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSecretResponseBytes))
	if err != nil {
		return "", nilFetchFailure(FailureResponse)
	}
	var login struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &login); err != nil || login.Auth.ClientToken == "" {
		return "", nilFetchFailure(FailureAuth)
	}
	expires := time.Time{}
	if login.Auth.LeaseDuration > 0 {
		expires = c.now().Add(time.Duration(login.Auth.LeaseDuration) * time.Second)
		// Re-authenticate before the server-side lease expires.
		expires = expires.Add(-time.Second)
	}
	c.mu.Lock()
	c.authToken = login.Auth.ClientToken
	c.authTokenExpiry = expires
	c.mu.Unlock()
	return login.Auth.ClientToken, nil
}

func (c *Client) clearAuthToken() {
	c.mu.Lock()
	c.authToken = ""
	c.authTokenExpiry = time.Time{}
	c.mu.Unlock()
}

func nilFetchFailure(class FailureClass) error { return &fetchFailure{class: class} }

func decodeSecret(body []byte) (Secret, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	raw, ok := envelope["data"]
	if !ok {
		return nil, fmt.Errorf("OpenBao response missing data")
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	// KV v2 wraps the actual values in data.data. Supporting the direct
	// shape as well keeps the client compatible with KV v1-compatible test
	// servers without changing the caller-facing API.
	if nested, ok := data["data"].(map[string]any); ok {
		return Secret(nested), nil
	}
	return Secret(data), nil
}

func canonicalPath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "/")
	if value == "" {
		return "", fmt.Errorf("vault path must not be empty")
	}
	if strings.ContainsAny(value, "*?[]{}$()") {
		return "", fmt.Errorf("vault path contains a forbidden pattern")
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("vault path contains an invalid segment")
		}
	}
	clean := path.Join(parts...)
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("vault path escapes its root")
	}
	return clean, nil
}

// ResolvePath canonicalizes the logical x-vault-path used as the cache key.
// Leading slashes are ignored so equivalent references cannot create separate
// cache entries; traversal, glob, and template syntax is rejected.
func ResolvePath(raw string) (string, error) { return canonicalPath(raw) }

func escapePath(value string) string {
	parts := strings.Split(value, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func cloneSecret(secret Secret) Secret {
	if secret == nil {
		return nil
	}
	clone := make(Secret, len(secret))
	for key, value := range secret {
		clone[key] = cloneValue(value)
	}
	return clone
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(typed))
		for key, child := range typed {
			clone[key] = cloneValue(child)
		}
		return clone
	case []any:
		clone := make([]any, len(typed))
		for i, child := range typed {
			clone[i] = cloneValue(child)
		}
		return clone
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

func detectInCluster(override *bool, serviceAccountPath string) bool {
	if override != nil {
		return *override
	}
	if _, present := os.LookupEnv("KUBERNETES_SERVICE_HOST"); present {
		return true
	}
	info, err := os.Stat(serviceAccountPath)
	if err == nil {
		return !info.IsDir()
	}
	// An inaccessible projected token is safer to classify as in-cluster than
	// to fall back to a developer token.
	return !os.IsNotExist(err)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func ctxDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return context.Background().Done()
	}
	return ctx.Done()
}
