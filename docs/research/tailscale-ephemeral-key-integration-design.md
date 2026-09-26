# Tailscale Ephemeral Key Integration Design

**Purpose**: Design the integration with Tailscale's API for generating real ephemeral auth keys for NEEDLE workers.

**Date**: 2026-08-28  
**Status**: Design Phase  
**Related Bead**: seam-72a55401

## Implementation status (2026-09-26)

This document was written at design time; the implemented system diverges from
it in the places below. The design text above is preserved as authored.

- **Implemented**: Phases 1–2 minus the registry — `internal/tailscale/` ships
  `client.go`, `types.go`, `errors.go`, and `cache.go` (client + cache +
  hold-down; there is no `registry.go` / `IdentityRegistry`). The endpoint is
  live: `POST /api/v1/tailscale/ephemeral-key`, a reserved caller-listener path
  (`internal/server/server.go`) served by `tailscaleEphemeralKeyHandler`
  (`internal/server/control_plane_handlers.go`), gated on the
  `seam:tailscale:key-create` scope; a client-cache hit re-serves the same key,
  and an upstream failure enters the hold-down window, after which the endpoint
  answers `503 service_unavailable` with `details.retry_after: "30s"`.
- **OpenBao path**: the client reads the API key from
  `secret/rs-manager/tailscale/api-key` (see `getTailscaleAPIKeyFromVault`),
  not the `secret/seam/tailscale/api-key` path proposed below.
- **Environment variables**: implemented as `TS_API_KEY`, `TS_TAILNET`, and
  `TS_DEBUG`, not the `SEAM_TAILSCALE_*` names below.
- **Not implemented (as of this date)**: retry/backoff on upstream failures
  (failures mark hold-down immediately), the admin key-management endpoints of
  Phase 5, and the Prometheus metrics listed under Monitoring.

## Overview

This design document outlines the integration with Tailscale's API v2 to create and manage ephemeral auth keys for NEEDLE workers. The integration follows SEAM's existing patterns for external API communication, credential management, and lifecycle management.

## Background

### Need for Ephemeral Keys

NEEDLE workers require Tailscale identities to access protected services and coordinate with other workers. Ephemeral keys provide:
- **Automatic cleanup**: Keys and associated nodes are removed when workers go offline
- **Short-lived access**: Keys expire after a configurable period (1-90 days)
- **Tag-based authorization**: Keys can be tagged with `tag:needle-worker` for ACL scoping
- **No manual management**: No need to manually revoke keys for terminated workers

### Tailscale API v2 Endpoint

**Endpoint**: `POST /api/v2/tailnet/{tailnet}/keys`

**Request Structure**:
```json
{
  "capabilities": {
    "devices": {
      "create": {
        "reusable": false,
        "ephemeral": true,
        "tags": ["tag:needle-worker"],
        "preauthorized": true
      }
    }
  },
  "expirySeconds": 7776000,
  "description": "NEEDLE worker key"
}
```

**Response Structure**:
```json
{
  "id": "key-id",
  "key": "tskey-auth-...",
  "description": "NEEDLE worker key",
  "created": "2026-08-28T00:00:00Z",
  "expires": "2026-11-28T00:00:00Z",
  "capabilities": {
    "devices": {
      "create": {
        "reusable": false,
        "ephemeral": true,
        "tags": ["tag:needle-worker"]
      }
    }
  }
}
```

## Architecture Design

### Component Structure

```
internal/tailscale/
├── client.go           # Main Tailscale API client
├── types.go            # Request/response structs
├── errors.go           # Error handling
├── registry.go         # Identity registry for key tracking
└── cache.go            # Key caching and lifecycle
```

### Core Components

#### 1. Tailscale Client (`client.go`)

Follows SEAM's `vault.Client` pattern:

```go
package tailscale

import (
    "context"
    "net/http"
    "time"
)

// Config configures a Tailscale client
type Config struct {
    // API endpoint configuration
    APIKey      string              // Tailscale API key from OpenBao
    Tailnet     string              // Tailnet name (e.g., "ardenone")
    BaseURL     string              // Default: "https://api.tailscale.com"
    
    // Ephemeral key configuration
    DefaultExpiry time.Duration     // Default: 90 days
    DefaultTags   []string          // Default: ["tag:needle-worker"]
    
    // HTTP client configuration
    HTTPClient  *http.Client
    Timeout     time.Duration
    
    // Cache configuration
    CacheTTL    time.Duration       // Default: 5 minutes
    CacheHoldDown time.Duration     // Default: 30 seconds
}

// Client is the Tailscale API client
type Client struct {
    config      Config
    apiKey      string              // Cached API key
    cache       *keyCache
    httpClient  *http.Client
    mu          sync.RWMutex
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
    
    // Configure HTTP client with connection pooling
    if cfg.HTTPClient == nil {
        cfg.HTTPClient = &http.Client{
            Timeout: cfg.Timeout,
            Transport: &http.Transport{
                MaxIdleConns:        100,
                MaxIdleConnsPerHost: 10,
                IdleConnTimeout:     90 * time.Second,
            },
        }
    }
    
    return &Client{
        config:     cfg,
        apiKey:     cfg.APIKey,
        httpClient: cfg.HTTPClient,
        cache:      newKeyCache(cfg.CacheTTL, cfg.CacheHoldDown),
    }, nil
}
```

#### 2. Request/Response Types (`types.go`)

```go
package tailscale
import "time"

// CreateKeyRequest represents a request to create an API key
type CreateKeyRequest struct {
    Capabilities KeyCapabilities `json:"capabilities"`
    ExpirySeconds int64          `json:"expirySeconds,omitempty"`
    Description  string          `json:"description,omitempty"`
}

// KeyCapabilities defines what the key can do
type KeyCapabilities struct {
    Devices DeviceCapabilities `json:"devices"`
}

// DeviceCapabilities defines device creation options
type DeviceCapabilities struct {
    Create DeviceCreateOptions `json:"create"`
}

// DeviceCreateOptions defines how devices can be created
type DeviceCreateOptions struct {
    Reusable      bool     `json:"reusable"`
    Ephemeral     bool     `json:"ephemeral"`
    Tags          []string `json:"tags"`
    Preauthorized bool     `json:"preauthorized"`
}

// Key represents a Tailscale API key
type Key struct {
    ID           string          `json:"id"`
    Key          string          `json:"key"`           // Only returned on creation
    KeyType      string          `json:"keyType"`
    Description  string          `json:"description"`
    Created      time.Time       `json:"created"`
    Expires      time.Time       `json:"expires"`
    Revoked      bool            `json:"revoked"`
    Invalid      bool            `json:"invalid"`
    Capabilities KeyCapabilities  `json:"capabilities"`
}

// CreateEphemeralKey creates a new ephemeral key for a NEEDLE worker
func (c *Client) CreateEphemeralKey(ctx context.Context, workerID string) (*Key, error) {
    req := CreateKeyRequest{
        Capabilities: KeyCapabilities{
            Devices: DeviceCapabilities{
                Create: DeviceCreateOptions{
                    Reusable:      false,
                    Ephemeral:     true,
                    Tags:          c.config.DefaultTags,
                    Preauthorized: true,
                },
            },
        },
        ExpirySeconds: int64(c.config.DefaultExpiry.Seconds()),
        Description:   fmt.Sprintf("NEEDLE worker: %s", workerID),
    }
    
    return c.createKey(ctx, req)
}
```

#### 3. Identity Registry (`registry.go`)

Follows SEAM's `CircuitBreakerRegistry` pattern:

```go
package tailscale

import (
    "sync"
    "time"
)

// IdentityRegistry tracks Tailscale identities for NEEDLE workers
type IdentityRegistry struct {
    mu        sync.RWMutex
    identities map[string]*WorkerIdentity  // workerID -> identity
    client    *Client
}

// WorkerIdentity represents a worker's Tailscale identity
type WorkerIdentity struct {
    WorkerID      string
    KeyID         string
    AuthKey       string              // Redacted in logs/status
    Description   string
    Tags          []string
    CreatedAt     time.Time
    ExpiresAt     time.Time
    LastUsed      time.Time
    Status        IdentityStatus
}

// IdentityStatus represents the current status of an identity
type IdentityStatus string

const (
    StatusActive     IdentityStatus = "active"
    StatusExpired    IdentityStatus = "expired"
    StatusRevoked    IdentityStatus = "revoked"
    StatusProvisioning IdentityStatus = "provisioning"
)

// NewIdentityRegistry creates a new identity registry
func NewIdentityRegistry(client *Client) *IdentityRegistry {
    return &IdentityRegistry{
        identities: make(map[string]*WorkerIdentity),
        client:    client,
    }
}

// ProvisionIdentity provisions a new Tailscale identity for a worker
func (r *IdentityRegistry) ProvisionIdentity(ctx context.Context, workerID string) (*WorkerIdentity, error) {
    r.mu.Lock()
    defer r.mu.Unlock()
    
    // Check if identity already exists
    if existing, ok := r.identities[workerID]; ok {
        if existing.Status == StatusActive && existing.ExpiresAt.After(time.Now()) {
            return existing, nil
        }
    }
    
    // Create new ephemeral key
    key, err := r.client.CreateEphemeralKey(ctx, workerID)
    if err != nil {
        return nil, fmt.Errorf("failed to create ephemeral key: %w", err)
    }
    
    identity := &WorkerIdentity{
        WorkerID:    workerID,
        KeyID:       key.ID,
        AuthKey:     key.Key,
        Description: key.Description,
        Tags:        key.Capabilities.Devices.Create.Tags,
        CreatedAt:   time.Now(),
        ExpiresAt:   key.Expires,
        LastUsed:    time.Now(),
        Status:      StatusActive,
    }
    
    r.identities[workerID] = identity
    return identity, nil
}

// GetIdentity retrieves a worker's identity
func (r *IdentityRegistry) GetIdentity(workerID string) (*WorkerIdentity, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    
    identity, ok := r.identities[workerID]
    return identity, ok
}

// ListActiveIdentities returns all active identities
func (r *IdentityRegistry) ListActiveIdentities() []*WorkerIdentity {
    r.mu.RLock()
    defer r.mu.RUnlock()
    
    var active []*WorkerIdentity
    for _, identity := range r.identities {
        if identity.Status == StatusActive && identity.ExpiresAt.After(time.Now()) {
            active = append(active, identity)
        }
    }
    return active
}

// CleanupExpiredIdentities removes expired identities
func (r *IdentityRegistry) CleanupExpiredIdentities() int {
    r.mu.Lock()
    defer r.mu.Unlock()
    
    now := time.Now()
    cleaned := 0
    
    for workerID, identity := range r.identities {
        if identity.ExpiresAt.Before(now) {
            identity.Status = StatusExpired
            delete(r.identities, workerID)
            cleaned++
        }
    }
    
    return cleaned
}
```

#### 4. Key Caching (`cache.go`)

Follows SEAM's vault caching pattern:

```go
package tailscale

import (
    "sync"
    "time"
)

// keyCache provides TTL-based caching for Tailscale keys
type keyCache struct {
    mu          sync.RWMutex
    entries     map[string]*cacheEntry
    ttl         time.Duration
    holdDown    time.Duration
    lastFailure time.Time
}

type cacheEntry struct {
    key       *Key
    expiresAt time.Time
}

func newKeyCache(ttl, holdDown time.Duration) *keyCache {
    return &keyCache{
        entries:  make(map[string]*cacheEntry),
        ttl:      ttl,
        holdDown: holdDown,
    }
}

// Get retrieves a cached key if valid
func (c *keyCache) Get(workerID string) (*Key, bool) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    
    entry, ok := c.entries[workerID]
    if !ok {
        return nil, false
    }
    
    if time.Now().After(entry.expiresAt) {
        return nil, false
    }
    
    return entry.key, true
}

// Set stores a key in the cache
func (c *keyCache) Set(workerID string, key *Key) {
    c.mu.Lock()
    defer c.mu.Unlock()
    
    c.entries[workerID] = &cacheEntry{
        key:       key,
        expiresAt: time.Now().Add(c.ttl),
    }
}

// IsInHoldDown checks if we're in hold-down period after a failure
func (c *keyCache) IsInHoldDown() bool {
    c.mu.RLock()
    defer c.mu.RUnlock()
    
    if c.lastFailure.IsZero() {
        return false
    }
    
    return time.Now().Before(c.lastFailure.Add(c.holdDown))
}

// MarkFailure records a failure and starts hold-down period
func (c *keyCache) MarkFailure() {
    c.mu.Lock()
    defer c.mu.Unlock()
    
    c.lastFailure = time.Now()
}
```

#### 5. Error Handling (`errors.go`)

```go
package tailscale

import "fmt"

// Error types
var (
    ErrNoAPIKey         = fmt.Errorf("Tailscale API key is required")
    ErrNoTailnet        = fmt.Errorf("tailnet name is required")
    ErrInvalidResponse  = fmt.Errorf("invalid API response")
    ErrRateLimited      = fmt.Errorf("rate limited by Tailscale API")
    ErrAuthFailed       = fmt.Errorf("authentication failed")
    ErrKeyCreation      = fmt.Errorf("failed to create ephemeral key")
    ErrCacheHoldDown    = fmt.Errorf("cache in hold-down period")
)

// APIError represents a Tailscale API error
type APIError struct {
    StatusCode int
    Message    string
    Err        error
}

func (e *APIError) Error() string {
    return fmt.Sprintf("Tailscale API error (status %d): %s", e.StatusCode, e.Message)
}

func (e *APIError) Unwrap() error {
    return e.Err
}
```

## Integration Plan

### Phase 1: Core Client Implementation
1. Implement `client.go` with basic API communication
2. Implement `types.go` with request/response structures
3. Implement `errors.go` with error handling
4. Add unit tests for client operations

### Phase 2: Caching and Registry
1. Implement `cache.go` with TTL-based caching
2. Implement `registry.go` with identity tracking
3. Add cleanup logic for expired identities
4. Add integration tests

### Phase 3: OpenBao Integration
1. Store Tailscale API key in OpenBao at `secret/seam/tailscale/api-key`
2. Update SEAM config to load API key from OpenBao
3. Add API key rotation support
4. Add readiness checks for API key availability

### Phase 4: NEEDLE Integration
1. Add identity provisioning to NEEDLE worker startup
2. Add identity injection into worker environment
3. Add key revocation on worker shutdown
4. Add monitoring and metrics

### Phase 5: Route Fragment
1. Create SEAM route fragment at `routes/tailscale/tailscale-keys.yaml`
2. Add endpoints for key management (admin-only)
3. Add metrics for key lifecycle
4. Add documentation

## Error Handling Strategy

### API Failures

**Rate Limiting (HTTP 429)**:
- Implement exponential backoff: 1s, 2s, 4s, 8s, 16s
- Cache hold-down for 30 seconds after rate limit
- Return `ErrRateLimited` to caller

**Authentication Failures (HTTP 401/403)**:
- Immediately fail-fast - no retry
- Log error without exposing API key
- Trigger OpenBao credential refresh
- Return `ErrAuthFailed` to caller

**Network Failures**:
- Retry with exponential backoff: 1s, 2s, 4s
- Max 3 retries before giving up
- Mark cache as in hold-down for 5 seconds
- Return wrapped error to caller

**Invalid Responses**:
- Validate response structure before parsing
- Return `ErrInvalidResponse` with details
- Don't cache invalid responses

### Cache Failures

**Cache Misses**:
- Fetch from API on miss
- Update cache on successful fetch
- Return error if fetch fails

**Cache Corruption**:
- Clear entire cache on corruption detection
- Log warning without exposing secrets
- Re-fetch from API

**Hold-Down Period**:
- Return `ErrCacheHoldDown` during hold-down
- Allow bypass with force flag for admin operations
- Automatically exit hold-down after timeout

## Security Considerations

### API Key Management
- API key stored in OpenBao at `secret/seam/tailscale/api-key`
- Loaded at startup via SEAM's vault client
- Never logged or exposed in status/metrics
- Rotate via OpenBao without restart (cached with TTL)

### Key Distribution
- Ephemeral keys injected into worker environment only
- Keys never written to disk or logs
- Worker processes receive key as environment variable
- Keys expire automatically after 90 days

### Tag-Based Authorization
- All keys tagged with `tag:needle-worker`
- ACL rules scoped to this tag
- No cross-contamination with other services

### Audit Trail
- Log key creation events (without key value)
- Log key expiration events
- Log cleanup operations
- Metrics for active/expired keys

## Key Lifecycle

### Provision (Worker Startup)
1. NEEDLE worker requests identity
2. IdentityRegistry checks for existing identity
3. If none exists, Client.CreateEphemeralKey called
4. Key stored in registry and cache
5. Key injected into worker environment
6. Worker starts with Tailscale identity

### Active (Worker Running)
1. Key used for Tailscale operations
2. Registry tracks LastUsed timestamp
3. Periodic cache refresh (5-minute TTL)
4. Health checks verify key validity

### Expiration (Worker Shutdown/Timeout)
1. Worker stops or key expires
2. Tailscale auto-removes inactive node
3. IdentityRegistry marks identity expired
4. Next cleanup cycle removes identity
5. No manual revocation needed

## Testing Strategy

### Unit Tests
- Client operations (create, list, revoke)
- Cache operations (get, set, expiry)
- Registry operations (provision, get, cleanup)
- Error handling (rate limits, auth failures)

### Integration Tests
- End-to-end key creation
- Cache invalidation
- Identity lifecycle
- Error scenarios

### Manual Testing
- Real Tailscale API calls
- Worker provisioning
- Key expiration
- Cleanup operations

## Configuration

### Environment Variables
```bash
# Tailscale API configuration
SEAM_TAILSCALE_API_KEY=vault:secret/seam/tailscale/api-key
SEAM_TAILNET=ardenone
SEAM_TAILSCALE_BASE_URL=https://api.tailscale.com

# Key configuration
SEAM_TAILSCALE_DEFAULT_EXPIRY=90d
SEAM_TAILSCALE_DEFAULT_TAGS=tag:needle-worker

# Cache configuration
SEAM_TAILSCALE_CACHE_TTL=5m
SEAM_TAILSCALE_CACHE_HOLD_DOWN=30s
```

### OpenBao Paths
```
secret/seam/tailscale/api-key        # Tailscale API key
secret/seam/tailscale/config         # Optional config overrides
```

## Monitoring and Metrics

### Prometheus Metrics
```prometheus
# Key creation metrics
tailscale_keys_created_total{tailnet="ardenone"}
tailscale_keys_created_failed_total

# Active keys
tailscale_keys_active{tailnet="ardenone"}
tailscale_keys_expired{tailnet="ardenone"}

# Cache metrics
tailscale_cache_hits_total
tailscale_cache_misses_total
tailscale_cache_hold_down_total

# API call metrics
tailscale_api_calls_total{operation="create_key",status="success"}
tailscale_api_latency_seconds{operation="create_key"}
```

### Health Checks
- API key availability check
- API connectivity check
- Cache health check
- Active identity count check

## Future Enhancements

### Short-Term
- Key rotation before expiration
- Bulk key operations for fleet provisioning
- Key usage analytics

### Long-Term
- Multi-tailnet support
- Custom tag templates per worker type
- Integration with Tailscale SSH
- Webhook notifications for key events

## References

- [Tailscale API Documentation](https://tailscale.com/api-docs)
- [Auth Keys Documentation](https://tailscale.com/docs/features/access-control/auth-keys)
- [Tailscale Go Client](https://github.com/tailscale/tailscale-client-go-v2)
- SEAM vault client patterns: `/home/coding/SEAM/internal/vault/vault.go`
- SEAM proxy patterns: `/home/coding/SEAM/internal/server/proxy.go`
- SEAM registry patterns: `/home/coding/SEAM/internal/server/circuit_breaker_registry.go`

---

**Acceptance Criteria Status:**
- ✅ Design document with API endpoint and authentication approach
- ✅ Go struct definitions for request/response
- ✅ Error handling strategy documented  
- ✅ Integration plan with existing patterns (vault, proxy, registry)

**Next Steps:**
1. Review and approve design document
2. Begin Phase 1 implementation (core client)
3. Create OpenBao secret for API key
4. Add unit tests
5. Proceed to Phase 2 (caching and registry)