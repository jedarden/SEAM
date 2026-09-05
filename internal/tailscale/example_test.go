package tailscale_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ardenone/seam/internal/tailscale"
)

// Example_basic demonstrates basic usage of the Tailscale client
func Example_basic() {
	// Create a new client
	client, err := tailscale.New(tailscale.Config{
		APIKey:  "your-api-key",
		Tailnet: "ardenone",
	})
	if err != nil {
		log.Fatal(err)
	}

	// Create an ephemeral key for a worker
	ctx := context.Background()
	key, err := client.CreateEphemeralKey(ctx, "worker-1")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Created key: %s\n", key.Key)
	fmt.Printf("Expires: %s\n", key.Expires)
}

// Example_customTags demonstrates creating keys with custom tags
func Example_customTags() {
	client, err := tailscale.New(tailscale.Config{
		APIKey:  "your-api-key",
		Tailnet: "ardenone",
	})
	if err != nil {
		log.Fatal(err)
	}

	// Create key with custom tags
	customTags := []string{"tag:needle-worker", "tag:production"}
	ctx := context.Background()

	key, err := client.CreateEphemeralKeyWithTags(ctx, "worker-1", customTags)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Created key with tags: %v\n", key.Capabilities.Devices.Create.Tags)
}

// Example_withConfiguration demonstrates client with custom configuration
func Example_withConfiguration() {
	client, err := tailscale.New(tailscale.Config{
		APIKey:        "your-api-key",
		Tailnet:       "ardenone",
		DefaultExpiry: 30 * 24 * time.Hour, // 30 days
		DefaultTags:   []string{"tag:needle-worker"},
		CacheTTL:      10 * time.Minute,
		CacheHoldDown: 1 * time.Minute,
		Timeout:       60 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	key, err := client.CreateEphemeralKey(ctx, "worker-1")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Key expires in: %v\n", time.Until(key.Expires))
}

// Example_errorHandling demonstrates proper error handling
func Example_errorHandling() {
	client, err := tailscale.New(tailscale.Config{
		APIKey:  "your-api-key",
		Tailnet: "ardenone",
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	key, err := client.CreateEphemeralKey(ctx, "worker-1")
	if err != nil {
		// Handle specific errors
		switch err {
		case tailscale.ErrRateLimited:
			fmt.Println("Rate limited - implement backoff")
		case tailscale.ErrAuthFailed:
			fmt.Println("Auth failed - check API key")
		case tailscale.ErrCacheHoldDown:
			fmt.Println("In hold-down period - retry later")
		default:
			fmt.Printf("Other error: %v\n", err)
		}
		return
	}

	fmt.Printf("Successfully created key: %s\n", key.ID)
}

// Example_listKeys demonstrates listing all keys
func Example_listKeys() {
	client, err := tailscale.New(tailscale.Config{
		APIKey:  "your-api-key",
		Tailnet: "ardenone",
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	keys, err := client.ListKeys(ctx)
	if err != nil {
		log.Fatal(err)
	}

	for _, key := range keys {
		fmt.Printf("Key %s: %s (revoked: %v)\n", key.ID, key.Description, key.Revoked)
	}
}

// Example_deleteKey demonstrates deleting a key
func Example_deleteKey() {
	client, err := tailscale.New(tailscale.Config{
		APIKey:  "your-api-key",
		Tailnet: "ardenone",
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	err = client.DeleteKey(ctx, "key-id")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Key deleted successfully")
}

// Example_cacheManagement demonstrates cache operations
func Example_cacheManagement() {
	client, err := tailscale.New(tailscale.Config{
		APIKey:  "your-api-key",
		Tailnet: "ardenone",
	})
	if err != nil {
		log.Fatal(err)
	}

	// Check cache status
	size, inHoldDown := client.GetCacheStats()
	fmt.Printf("Cache size: %d, In hold-down: %v\n", size, inHoldDown)

	// Clear cache
	client.ClearCache()

	// Invalidate specific worker
	client.InvalidateWorker("worker-1")

	// Cleanup expired entries
	cleaned := client.CleanupExpired()
	fmt.Printf("Cleaned %d expired entries\n", cleaned)
}

// Example_apiKeyRotation demonstrates rotating API keys
func Example_apiKeyRotation() {
	client, err := tailscale.New(tailscale.Config{
		APIKey:  "old-api-key",
		Tailnet: "ardenone",
	})
	if err != nil {
		log.Fatal(err)
	}

	// Update API key (useful for credential rotation)
	client.SetAPIKey("new-api-key")

	ctx := context.Background()
	key, err := client.CreateEphemeralKey(ctx, "worker-1")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Created key with new credentials: %s\n", key.ID)
}
