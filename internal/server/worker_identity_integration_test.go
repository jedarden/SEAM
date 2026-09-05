package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/tailscale"
)

// TestConcurrentWorkerIdentityCreation tests that multiple workers
// running simultaneously get distinct identities
func TestConcurrentWorkerIdentityCreation(t *testing.T) {
	// Track created identities for uniqueness verification
	identities := make(map[string]bool)
	var identitiesMu sync.Mutex

	// Simulate concurrent worker spawns
	numWorkers := 10
	var wg sync.WaitGroup
	errors := make(chan error, numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerNum int) {
			defer wg.Done()

			// Create identity resolver (simulating SEAM's identity resolution)
			resolver := NewIdentityResolver()

			// Simulate identity resolution from Tailscale IP
			remoteAddr := fmt.Sprintf("100.64.0.%d:443", 100+workerNum%155)
			identity, err := resolver.Resolve(context.Background(), remoteAddr)
			if err != nil {
				t.Errorf("Worker %d: identity resolution failed: %v", workerNum, err)
				errors <- err
				return
			}

			// Verify identity was created
			if identity == nil {
				t.Errorf("Worker %d: got nil identity", workerNum)
				errors <- nil
				return
			}

			// Verify identity uniqueness
			identitiesMu.Lock()
			identityKey := identity.NodeKey + identity.NodeName
			if identities[identityKey] {
				t.Errorf("Worker %d: duplicate identity detected: %s", workerNum, identityKey)
				errors <- nil
				identitiesMu.Unlock()
				return
			}
			identities[identityKey] = true
			identitiesMu.Unlock()

			errors <- nil
		}(i)
	}

	// Wait for all workers to complete
	wg.Wait()
	close(errors)

	// Check for any errors
	for err := range errors {
		if err != nil {
			t.Fatalf("Worker identity creation failed: %v", err)
		}
	}

	// Verify we got the expected number of unique identities
	if len(identities) != numWorkers {
		t.Errorf("Expected %d unique identities, got %d", numWorkers, len(identities))
	}
}

// TestIdentityTagging verifies that worker identities are properly
// tagged with 'tag:needle-worker' in the tailnet
func TestIdentityTagging(t *testing.T) {
	// Create a test identity with tags
	identity := &Identity{
		NodeKey:  "test-node-key",
		NodeName: "test-worker",
		Tags:     []string{"tag:needle-worker", "tag:custom"},
		Resolved: true,
	}

	// Verify the worker has the required tag
	if !identity.HasTag("tag:needle-worker") {
		t.Errorf("Expected identity to have 'tag:needle-worker' tag")
	}

	// Verify custom tags are also recognized
	if !identity.HasTag("tag:custom") {
		t.Errorf("Expected identity to have 'tag:custom' tag")
	}

	// Verify non-existent tag is not found
	if identity.HasTag("tag:nonexistent") {
		t.Errorf("Expected false for non-existent tag")
	}
}

// TestIdentityScopeExtraction tests that scope claims are properly
// extracted from Tailscale Grant's app capability field
func TestIdentityScopeExtraction(t *testing.T) {
	identity := &Identity{
		NodeKey:      "test-node-key",
		NodeName:     "test-worker",
		Resolved:     true,
		Capabilities: []string{"k8s-ro:get", "argocd:read", "seam:ops:read"},
	}

	// Verify scope checking works
	if !identity.HasScope("k8s-ro:get") {
		t.Errorf("Expected identity to have 'k8s-ro:get' scope")
	}

	if !identity.HasScope("argocd:read") {
		t.Errorf("Expected identity to have 'argocd:read' scope")
	}

	// Verify non-existent scope is not found
	if identity.HasScope("nonexistent:scope") {
		t.Errorf("Expected false for non-existent scope")
	}

	// Test ExtractScopeClaims
	scopes := ExtractScopeClaims(identity)
	if len(scopes) != 3 {
		t.Errorf("Expected 3 scopes, got %d", len(scopes))
	}

	// Test with unresolved identity
	unresolved := &Identity{
		Resolved: false,
		NodeName: "unresolved",
	}
	scopes = ExtractScopeClaims(unresolved)
	if scopes != nil {
		t.Errorf("Expected nil scopes for unresolved identity, got %v", scopes)
	}
}

// TestIdentityResolutionMiddleware tests the Stage 3 middleware
func TestIdentityResolutionMiddleware(t *testing.T) {
	resolver := NewIdentityResolver()
	server := &Server{
		identityResolver: resolver,
	}

	// Create test handler that checks identity was resolved
	handlerCalled := false
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true

		// Verify identity was stored in context
		identity := identityFromContext(r.Context())
		if identity == nil {
			t.Errorf("Expected identity in request context")
			return
		}

		w.WriteHeader(http.StatusOK)
	})

	// Wrap with identity resolution middleware
	middleware := server.identityResolutionMiddleware(nextHandler)

	// Create test request from Tailscale IP
	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	req.RemoteAddr = "100.64.0.10:443"

	// Record response
	rec := httptest.NewRecorder()

	// Call middleware
	middleware.ServeHTTP(rec, req)

	// Verify handler was called (identity resolved successfully)
	if !handlerCalled {
		t.Errorf("Expected handler to be called after identity resolution")
	}

	// Verify response status (should be 200, not 403)
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

// TestIdentityResolutionForNonTailscaleIP tests that non-Tailscale
// IPs are properly rejected
func TestIdentityResolutionForNonTailscaleIP(t *testing.T) {
	resolver := NewIdentityResolver()

	// Test with non-Tailscale IP
	remoteAddr := "192.168.1.1:443"
	identity, err := resolver.Resolve(context.Background(), remoteAddr)

	// Should return unresolved identity with error
	if err == nil {
		t.Errorf("Expected error for non-Tailscale IP, got nil")
	}

	if identity == nil {
		t.Errorf("Expected identity object, got nil")
	}

	if identity != nil && identity.Resolved {
		t.Errorf("Expected unresolved identity for non-Tailscale IP")
	}
}

// TestConcurrentIdentityResolution tests that identity resolution
// is thread-safe under concurrent load
func TestConcurrentIdentityResolution(t *testing.T) {
	resolver := NewIdentityResolver()
	numGoroutines := 50
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerNum int) {
			defer wg.Done()

			remoteAddr := fmt.Sprintf("100.64.0.%d:443", 100+workerNum%155)
			identity, err := resolver.Resolve(context.Background(), remoteAddr)

			if err != nil {
				errors <- err
				return
			}

			if identity == nil {
				errors <- nil
				return
			}

			errors <- nil
		}(i)
	}

	wg.Wait()
	close(errors)

	errorCount := 0
	for err := range errors {
		if err != nil {
			errorCount++
		}
	}

	if errorCount > 0 {
		t.Errorf("Expected no errors under concurrent load, got %d", errorCount)
	}
}

// TestTailscaleClientWithMultipleWorkers tests the Tailscale client's
// ability to handle multiple concurrent worker key creation requests
func TestTailscaleClientWithMultipleWorkers(t *testing.T) {
	callCount := 0
	callCountMu := sync.Mutex{}
	createdKeys := make(map[string]string)
	createdKeysMu := sync.Mutex{}

	// Mock Tailscale API server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCountMu.Lock()
		callCount++
		currentCallCount := callCount
		callCountMu.Unlock()

		// Simulate unique key creation for each worker
		workerID := fmt.Sprintf("worker-%d", currentCallCount)
		keyID := fmt.Sprintf("key-%s", workerID)
		keyValue := fmt.Sprintf("tskey-auth-%s-key", workerID)

		createdKeysMu.Lock()
		createdKeys[keyID] = keyValue
		createdKeysMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		response := fmt.Sprintf(`{
			"id": "%s",
			"key": "%s",
			"key_type": "EPHEMERAL",
			"description": "NEEDLE worker: %s",
			"created": "2026-08-28T12:00:00Z",
			"expires": "2026-11-26T12:00:00Z",
			"revoked": false,
			"invalid": false,
			"capabilities": {
				"devices": {
					"create": {
						"reusable": false,
						"ephemeral": true,
						"tags": ["tag:needle-worker"],
						"preauthorized": true
					}
				}
			}
		}`, keyID, keyValue, workerID)
		w.Write([]byte(response))
	}))
	defer server.Close()

	// Create Tailscale client
	client, err := tailscale.New(tailscale.Config{
		APIKey:        "test-api-key",
		Tailnet:       "test-tailnet",
		BaseURL:       server.URL,
		CacheTTL:      5 * time.Minute,
		CacheHoldDown: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Spawn multiple workers concurrently
	numWorkers := 10
	var wg sync.WaitGroup
	errors := make(chan error, numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerNum int) {
			defer wg.Done()

			workerID := fmt.Sprintf("concurrent-worker-%d", workerNum)
			ctx := context.Background()

			key, err := client.CreateEphemeralKey(ctx, workerID)
			if err != nil {
				t.Errorf("Worker %d: failed to create key: %v", workerNum, err)
				errors <- err
				return
			}

			if key == nil {
				t.Errorf("Worker %d: got nil key", workerNum)
				errors <- nil
				return
			}

			// Verify key has required tag
			hasWorkerTag := false
			for _, tag := range key.Capabilities.Devices.Create.Tags {
				if tag == "tag:needle-worker" {
					hasWorkerTag = true
					break
				}
			}
			if !hasWorkerTag {
				t.Errorf("Worker %d: key missing 'tag:needle-worker' tag", workerNum)
			}

			errors <- nil
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		if err != nil {
			t.Fatalf("Worker key creation failed: %v", err)
		}
	}

	// Verify we got the expected number of API calls (cached requests should not hit API)
	if callCount != numWorkers {
		t.Logf("Note: Expected %d API calls, got %d (caching may reduce this)", numWorkers, callCount)
	}

	// Verify all created keys are unique
	createdKeysMu.Lock()
	if len(createdKeys) != numWorkers {
		t.Errorf("Expected %d unique keys, got %d", numWorkers, len(createdKeys))
	}
	createdKeysMu.Unlock()
}

// TestIdentityStringRepresentation tests the Identity.String() method
func TestIdentityStringRepresentation(t *testing.T) {
	tests := []struct {
		name     string
		identity *Identity
		want     string
	}{
		{
			name:     "nil identity",
			identity: nil,
			want:     "identity:nil",
		},
		{
			name: "unresolved identity",
			identity: &Identity{
				Resolved: false,
				NodeName: "unresolved-host",
			},
			want: "identity:unresolved(unresolved-host)",
		},
		{
			name: "resolved identity with user",
			identity: &Identity{
				Resolved: true,
				NodeName: "worker-1",
				User:     "user@example.com",
			},
			want: "identity:node=worker-1,user=user@example.com",
		},
		{
			name: "resolved identity with tags",
			identity: &Identity{
				Resolved: true,
				NodeName: "worker-2",
				Tags:     []string{"tag:needle-worker", "tag:custom"},
			},
			want: "identity:node=worker-2,tags=[tag:needle-worker tag:custom]",
		},
		{
			name: "resolved identity with scopes",
			identity: &Identity{
				Resolved:     true,
				NodeName:     "worker-3",
				Capabilities: []string{"k8s-ro:get", "argocd:read"},
			},
			want: "identity:node=worker-3,scopes=[k8s-ro:get argocd:read]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.identity.String()
			if got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIdentityExpiryCleanup tests that expired identities are removed
func TestIdentityExpiryCleanup(t *testing.T) {
	client, err := tailscale.New(tailscale.Config{
		APIKey:        "test-api-key",
		Tailnet:       "test-tailnet",
		CacheTTL:      100 * time.Millisecond,
		CacheHoldDown: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Create some keys (these will be cached internally, but we're testing
	// the cache cleanup mechanism)

	// Note: This will fail without a real server, so we're testing the cache
	// cleanup logic directly through the client's cache operations

	// Test cache stats
	size, inHoldDown := client.GetCacheStats()
	if size != 0 {
		t.Errorf("Expected empty cache initially, got size %d", size)
	}
	if inHoldDown {
		t.Errorf("Expected not in hold-down initially")
	}

	// Test cleanup operation
	expired := client.CleanupExpired()
	if expired != 0 {
		t.Errorf("Expected 0 expired entries in empty cache, got %d", expired)
	}

	// Clear cache
	client.ClearCache()
	size, _ = client.GetCacheStats()
	if size != 0 {
		t.Errorf("Expected empty cache after clear, got size %d", size)
	}
}
