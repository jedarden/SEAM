# Tailscale API Client

The `tailscale` package provides a Go client for the Tailscale API v2, specifically designed for creating and managing ephemeral auth keys for NEEDLE workers.

## Overview

Tailscale ephemeral keys provide automatic cleanup, short-lived access, and tag-based authorization for temporary workloads. This client implements:

- HTTP client for Tailscale API v2 communication
- Ephemeral key creation with configurable tags and expiry
- TTL-based caching with hold-down period after failures
- Comprehensive error handling for rate limits, auth failures, and network issues
- Thread-safe operations for concurrent use

## Installation

The package is part of SEAM and requires Go 1.25.7 or later:

```go
import "github.com/ardenone/seam/internal/tailscale"
```

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"time"
	
	"github.com/ardenone/seam/internal/tailscale"
)

func main() {
	// Create a new client
	client, err := tailscale.New(tailscale.Config{
		APIKey:  "your-api-key",
		Tailnet: "ardenone",
	})
	if err != nil {
		panic(err)
	}

	// Create an ephemeral key for a worker
	ctx := context.Background()
	key, err := client.CreateEphemeralKey(ctx, "worker-1")
	if err != nil {
		panic(err)
	}

	fmt.Printf("Created key: %s\n", key.Key)
	fmt.Printf("Expires: %s\n", key.Expires)
}
```

## Configuration

### Basic Configuration

```go
client, err := tailscale.New(tailscale.Config{
	APIKey:  "your-api-key",                    // Required
	Tailnet: "ardenone",                         // Required
	BaseURL: "https://api.tailscale.com",       // Optional (default shown)
})
```

### Custom Configuration

```go
client, err := tailscale.New(tailscale.Config{
	// API Configuration
	APIKey:  "tskey-api-...",
	Tailnet: "ardenone",
	BaseURL: "https://api.tailscale.com",
	
	// Ephemeral Key Settings
	DefaultExpiry: 30 * 24 * time.Hour,          // 30 days (default: 90 days)
	DefaultTags:   []string{"tag:needle-worker"}, // (default shown)
	
	// HTTP Client Settings
	Timeout: 30 * time.Second,                  // (default shown)
	
	// Cache Settings
	CacheTTL:       5 * time.Minute,             // (default shown)
	CacheHoldDown:  30 * time.Second,             // (default shown)
	
	// Debug Logging
	EnableDebugLogging: true,                     // Optional
})
```

### Environment Variables

The API key can be sourced from environment variables:

```go
apiKey := os.Getenv("TAILSCALE_API_KEY")
tailnet := os.Getenv("TAILNET")

client, err := tailscale.New(tailscale.Config{
	APIKey:  apiKey,
	Tailnet: tailnet,
})
```

## Creating Ephemeral Keys

### Default Tags and Expiry

```go
key, err := client.CreateEphemeralKey(ctx, "worker-1")
```

Creates a key with:
- Default tags: `["tag:needle-worker"]`
- Default expiry: 90 days
- Description: `"NEEDLE worker: worker-1"`

### Custom Tags

```go
customTags := []string{"tag:needle-worker", "tag:production", "tag:builder"}
key, err := client.CreateEphemeralKeyWithTags(ctx, "worker-1", customTags)
```

### Response Structure

```go
type Key struct {
	ID          string         // Unique key identifier
	Key         string         // The actual auth key (only returned on creation)
	KeyType     string         // Key type (e.g., "EPHEMERAL")
	Description string         // Key description
	Created     time.Time      // Creation timestamp
	Expires     time.Time      // Expiration timestamp
	Revoked     bool           // Whether key has been revoked
	Invalid     bool           // Whether key is invalid
	Capabilities KeyCapabilities // Key capabilities
}
```

## Error Handling

The client provides specific error types for different failure modes:

```go
key, err := client.CreateEphemeralKey(ctx, "worker-1")
if err != nil {
	switch err {
	case tailscale.ErrRateLimited:
		// Too many requests - implement backoff
		log.Println("Rate limited, waiting...")
		time.Sleep(30 * time.Second)
		
	case tailscale.ErrAuthFailed:
		// Invalid API key - check credentials
		log.Println("Auth failed - check API key")
		
	case tailscale.ErrCacheHoldDown:
		// In hold-down period after failure
		log.Println("In hold-down period, retry later")
		
	case tailscale.ErrKeyCreation:
		// General key creation failure
		log.Printf("Failed to create key: %v", err)
		
	default:
		// Other errors
		log.Printf("Unexpected error: %v", err)
	}
}
```

### Error Types

| Error | Description |
|-------|-------------|
| `ErrNoAPIKey` | API key not provided |
| `ErrNoTailnet` | Tailnet name not provided |
| `ErrInvalidResponse` | Invalid API response |
| `ErrRateLimited` | Rate limited by API (HTTP 429) |
| `ErrAuthFailed` | Authentication failed (HTTP 401/403) |
| `ErrKeyCreation` | Failed to create key |
| `ErrCacheHoldDown` | Cache in hold-down period |
| `ErrInvalidExpiry` | Expiry duration invalid (must be 1-90 days) |

## Listing Keys

```go
keys, err := client.ListKeys(ctx)
if err != nil {
	// Handle error
}

for _, key := range keys {
	fmt.Printf("Key %s: %s (revoked: %v)\n", key.ID, key.Description, key.Revoked)
}
```

## Deleting Keys

```go
err := client.DeleteKey(ctx, "key-id")
if err != nil {
	// Handle error
}
```

## Cache Management

### Check Cache Status

```go
size, inHoldDown := client.GetCacheStats()
fmt.Printf("Cache size: %d, In hold-down: %v\n", size, inHoldDown)
```

### Clear Cache

```go
client.ClearCache()
```

### Invalidate Specific Worker

```go
client.InvalidateWorker("worker-1")
```

### Cleanup Expired Entries

```go
cleaned := client.CleanupExpired()
fmt.Printf("Cleaned %d expired entries\n", cleaned)
```

### API Key Rotation

```go
// Update API key (useful for credential rotation)
client.SetAPIKey("new-api-key")
```

## Testing

The package includes comprehensive unit tests:

```bash
# Run all tests
go test ./internal/tailscale/...

# Run with coverage
go test -cover ./internal/tailscale/...

# Run with race detection
go test -race ./internal/tailscale/...

# Run specific test
go test -run TestCreateEphemeralKey ./internal/tailscale/...
```

### Test Coverage

- Client creation and validation
- Key creation with various scenarios
- Caching behavior and hold-down periods
- Error handling for all failure modes
- Concurrent access patterns
- Cache operations (get, set, delete, clear, cleanup)

## Concurrency

The client is thread-safe and can be used concurrently:

```go
var wg sync.WaitGroup

for i := 0; i < 10; i++ {
	wg.Add(1)
	go func(workerID string) {
		defer wg.Done()
		key, err := client.CreateEphemeralKey(ctx, workerID)
		// Use key
	}(fmt.Sprintf("worker-%d", i))
}

wg.Wait()
```

### Thread-Safety Guarantees

- Multiple goroutines can safely call `CreateEphemeralKey` concurrently
- Cache operations are protected by mutex locks
- HTTP client uses connection pooling
- No global state - each client instance is independent

## Performance

### Connection Pooling

The HTTP client is configured with connection pooling:

```go
Transport: &http.Transport{
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 10,
	IdleConnTimeout:     90 * time.Second,
}
```

### Caching

Keys are cached for 5 minutes (default) to reduce API calls:

```go
// First call hits API
key1, err := client.CreateEphemeralKey(ctx, "worker-1")

// Second call uses cache (no API call)
key2, err := client.CreateEphemeralKey(ctx, "worker-1")
```

### Hold-Down Period

After failures, the client enters a 30-second hold-down period:

```go
// First call fails with rate limit
_, err := client.CreateEphemeralKey(ctx, "worker-1")

// Subsequent calls return ErrCacheHoldDown without hitting API
_, err = client.CreateEphemeralKey(ctx, "worker-1")
```

## Security Considerations

### API Key Management

- **Never log the API key**: The client redacts the key in debug output
- **Rotate keys regularly**: Use `SetAPIKey()` to update credentials
- **Use environment variables**: Don't hardcode keys in source code
- **Limit key permissions**: Use keys with minimal required scopes

### Key Distribution

- Ephemeral keys are only returned during creation
- Keys should be injected into worker processes via environment variables
- Never write keys to disk or logs
- Keys expire automatically (1-90 days)

### Tag-Based Authorization

- All keys are tagged for ACL scoping
- Default tag: `tag:needle-worker`
- Custom tags can be specified for different worker types
- Tags enforce authorization boundaries

## Best Practices

### 1. Error Handling

Always handle errors and implement appropriate retry logic:

```go
retryCount := 0
maxRetries := 3

for retryCount < maxRetries {
	key, err := client.CreateEphemeralKey(ctx, workerID)
	if err == nil {
		// Success
		return key, nil
	}

	if errors.Is(err, tailscale.ErrRateLimited) {
		// Exponential backoff
		time.Sleep(time.Duration(retryCount+1) * time.Second)
		retryCount++
		continue
	}

	if errors.Is(err, tailscale.ErrAuthFailed) {
		// Don't retry auth failures
		return nil, err
	}

	// Other errors
	return nil, err
}
```

### 2. Resource Cleanup

Always clean up resources:

```go
defer client.CleanupExpired()
```

### 3. Monitoring

Monitor cache statistics and API call patterns:

```go
size, inHoldDown := client.GetCacheStats()
log.Printf("Cache size: %d, Hold-down: %v", size, inHoldDown)
```

### 4. Context Usage

Always use context for cancellation:

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

key, err := client.CreateEphemeralKey(ctx, workerID)
```

## Examples

### Complete Worker Provisioning Example

```go
package main

import (
	"context"
	"log"
	"os"
	"time"
	
	"github.com/ardenone/seam/internal/tailscale"
)

func main() {
	// Load configuration
	apiKey := os.Getenv("TAILSCALE_API_KEY")
	tailnet := os.Getenv("TAILNET")
	
	// Create client
	client, err := tailscale.New(tailscale.Config{
		APIKey:  apiKey,
		Tailnet: tailnet,
		DefaultExpiry: 30 * 24 * time.Hour, // 30 days
		DefaultTags:   []string{"tag:needle-worker"},
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// Create key for worker
	ctx := context.Background()
	key, err := client.CreateEphemeralKey(ctx, "worker-1")
	if err != nil {
		log.Fatalf("Failed to create key: %v", err)
	}

	// Use the key (inject into worker process)
	log.Printf("Created key: %s (expires: %s)", key.ID, key.Expires)
	
	// Cleanup
	defer client.CleanupExpired()
}
```

### Custom Tags Example

```go
// Production worker with custom tags
prodTags := []string{
	"tag:needle-worker",
	"tag:production",
	"tag:high-availability",
}

key, err := client.CreateEphemeralKeyWithTags(ctx, "prod-worker-1", prodTags)
if err != nil {
	log.Fatalf("Failed to create production key: %v", err)
}
```

### Error Handling Example

```go
func provisionWorker(client *tailscale.Client, workerID string) (*tailscale.Key, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	retryCount := 0
	maxRetries := 3

	for retryCount < maxRetries {
		key, err := client.CreateEphemeralKey(ctx, workerID)
		if err == nil {
			return key, nil
		}

		// Handle specific errors
		switch {
		case errors.Is(err, tailscale.ErrRateLimited):
			wait := time.Duration(retryCount+1) * time.Second
			log.Printf("Rate limited, waiting %v...", wait)
			time.Sleep(wait)
			retryCount++

		case errors.Is(err, tailscale.ErrAuthFailed):
			return nil, fmt.Errorf("auth failed: %w", err)

		case errors.Is(err, tailscale.ErrCacheHoldDown):
			return nil, fmt.Errorf("in hold-down period: %w", err)

		default:
			return nil, fmt.Errorf("failed to create key: %w", err)
		}
	}

	return nil, fmt.Errorf("max retries exceeded")
}
```

## API Reference

### Type: Config

```go
type Config struct {
	APIKey              string        // Required: Tailscale API key
	Tailnet             string        // Required: Tailnet name
	BaseURL             string        // Optional: API base URL (default: https://api.tailscale.com)
	DefaultExpiry       time.Duration // Optional: Key expiry (default: 90 days)
	DefaultTags         []string      // Optional: Default tags (default: ["tag:needle-worker"])
	HTTPClient          *http.Client  // Optional: Custom HTTP client
	Timeout             time.Duration // Optional: Request timeout (default: 30s)
	CacheTTL            time.Duration // Optional: Cache TTL (default: 5 minutes)
	CacheHoldDown       time.Duration // Optional: Hold-down period (default: 30s)
	EnableDebugLogging  bool          // Optional: Enable debug logging
}
```

### Function: New

```go
func New(cfg Config) (*Client, error)
```

Creates a new Tailscale client with the given configuration.

### Method: CreateEphemeralKey

```go
func (c *Client) CreateEphemeralKey(ctx context.Context, workerID string) (*Key, error)
```

Creates an ephemeral key with default tags and expiry.

### Method: CreateEphemeralKeyWithTags

```go
func (c *Client) CreateEphemeralKeyWithTags(ctx context.Context, workerID string, tags []string) (*Key, error)
```

Creates an ephemeral key with custom tags.

### Method: ListKeys

```go
func (c *Client) ListKeys(ctx context.Context) ([]Key, error)
```

Lists all keys for the configured tailnet.

### Method: DeleteKey

```go
func (c *Client) DeleteKey(ctx context.Context, keyID string) error
```

Deletes a key by ID.

## License

This package is part of SEAM and follows the same license terms.

## Contributing

When contributing to this package:

1. Add unit tests for new functionality
2. Maintain test coverage above 80%
3. Update this documentation for API changes
4. Follow existing code style and patterns
5. Test with race detection enabled: `go test -race`
