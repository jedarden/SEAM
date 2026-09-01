package tailscale

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr error
	}{
		{
			name: "valid config",
			config: Config{
				APIKey:  "test-api-key",
				Tailnet: "test-tailnet",
			},
			wantErr: nil,
		},
		{
			name:    "missing API key",
			config: Config{
				Tailnet: "test-tailnet",
			},
			wantErr: ErrNoAPIKey,
		},
		{
			name: "missing tailnet",
			config: Config{
				APIKey: "test-api-key",
			},
			wantErr: ErrNoTailnet,
		},
		{
			name: "invalid expiry - too short",
			config: Config{
				APIKey:       "test-api-key",
				Tailnet:      "test-tailnet",
				DefaultExpiry: 1 * time.Hour,
			},
			wantErr: ErrInvalidExpiry,
		},
		{
			name: "invalid expiry - too long",
			config: Config{
				APIKey:       "test-api-key",
				Tailnet:      "test-tailnet",
				DefaultExpiry: 100 * 24 * time.Hour,
			},
			wantErr: ErrInvalidExpiry,
		},
		{
			name: "valid config with defaults",
			config: Config{
				APIKey:  "test-api-key",
				Tailnet: "test-tailnet",
			},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := New(tt.config)

			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("Expected error %v, got nil", tt.wantErr)
				}
				if err != tt.wantErr && !strings.Contains(err.Error(), tt.wantErr.Error()) {
					t.Errorf("Expected error %v, got %v", tt.wantErr, err)
				}
				if client != nil {
					t.Errorf("Expected nil client on error, got non-nil")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got %v", err)
				}
				if client == nil {
					t.Errorf("Expected non-nil client, got nil")
				}
			}
		})
	}
}

func TestClientDefaults(t *testing.T) {
	client, err := New(Config{
		APIKey:  "test-api-key",
		Tailnet: "test-tailnet",
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	if client.config.BaseURL != "https://api.tailscale.com" {
		t.Errorf("Expected default BaseURL, got %s", client.config.BaseURL)
	}

	if client.config.DefaultExpiry != 90*24*time.Hour {
		t.Errorf("Expected default expiry of 90 days, got %v", client.config.DefaultExpiry)
	}

	if len(client.config.DefaultTags) != 1 || client.config.DefaultTags[0] != "tag:needle-worker" {
		t.Errorf("Expected default tag 'tag:needle-worker', got %v", client.config.DefaultTags)
	}

	if client.config.CacheTTL != 5*time.Minute {
		t.Errorf("Expected default cache TTL of 5 minutes, got %v", client.config.CacheTTL)
	}

	if client.config.CacheHoldDown != 30*time.Second {
		t.Errorf("Expected default hold-down of 30 seconds, got %v", client.config.CacheHoldDown)
	}
}

func TestCreateEphemeralKey(t *testing.T) {
	tests := []struct {
		name           string
		workerID       string
		responseStatus int
		responseBody   interface{}
		wantErr        error
		validateKey    func(*testing.T, *Key)
	}{
		{
			name:     "successful key creation",
			workerID: "test-worker-1",
			responseStatus: http.StatusOK,
			responseBody: Key{
				ID:          "key-123",
				Key:         "tskey-auth-testkey",
				KeyType:     "EPHEMERAL",
				Description: "NEEDLE worker: test-worker-1",
				Created:     time.Now(),
				Expires:     time.Now().Add(90 * 24 * time.Hour),
				Revoked:     false,
				Invalid:     false,
				Capabilities: KeyCapabilities{
					Devices: DeviceCapabilities{
						Create: DeviceCreateOptions{
							Reusable:      false,
							Ephemeral:     true,
							Tags:          []string{"tag:needle-worker"},
							Preauthorized: true,
						},
					},
				},
			},
			wantErr: nil,
			validateKey: func(t *testing.T, k *Key) {
				if k.ID != "key-123" {
					t.Errorf("Expected key ID 'key-123', got '%s'", k.ID)
				}
				if k.Key != "tskey-auth-testkey" {
					t.Errorf("Expected key value 'tskey-auth-testkey', got '%s'", k.Key)
				}
				if !k.Capabilities.Devices.Create.Ephemeral {
					t.Errorf("Expected ephemeral key")
				}
			},
		},
		{
			name:           "rate limited",
			workerID:       "test-worker-2",
			responseStatus: http.StatusTooManyRequests,
			responseBody: map[string]string{
				"message": "Rate limit exceeded",
			},
			wantErr: ErrRateLimited,
		},
		{
			name:           "authentication failed",
			workerID:       "test-worker-3",
			responseStatus: http.StatusUnauthorized,
			responseBody: map[string]string{
				"error": "Invalid API key",
			},
			wantErr: ErrAuthFailed,
		},
		{
			name:           "forbidden",
			workerID:       "test-worker-4",
			responseStatus: http.StatusForbidden,
			responseBody: map[string]string{
				"error": "Insufficient permissions",
			},
			wantErr: ErrAuthFailed,
		},
		{
			name:           "server error",
			workerID:       "test-worker-5",
			responseStatus: http.StatusInternalServerError,
			responseBody: map[string]string{
				"message": "Internal server error",
			},
			wantErr: ErrKeyCreation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Validate request
				if r.Method != "POST" {
					t.Errorf("Expected POST request, got %s", r.Method)
				}

				auth := r.Header.Get("Authorization")
				if !strings.HasPrefix(auth, "Bearer ") {
					t.Errorf("Expected Bearer token, got %s", auth)
				}

				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Expected application/json content type")
				}

				// Parse request body
				var req CreateKeyRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("Failed to decode request: %v", err)
				}

				// Validate request structure
				if !req.Capabilities.Devices.Create.Ephemeral {
					t.Errorf("Expected ephemeral=true in request")
				}
				if req.Capabilities.Devices.Create.Reusable {
					t.Errorf("Expected reusable=false in request")
				}
				if !req.Capabilities.Devices.Create.Preauthorized {
					t.Errorf("Expected preauthorized=true in request")
				}
				if len(req.Capabilities.Devices.Create.Tags) == 0 {
					t.Errorf("Expected tags in request")
				}

				// Send response
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.responseStatus)
				json.NewEncoder(w).Encode(tt.responseBody)
			}))
			defer server.Close()

			client, err := New(Config{
				APIKey:      "test-api-key",
				Tailnet:     "test-tailnet",
				BaseURL:     server.URL,
				CacheTTL:    1 * time.Hour,
				CacheHoldDown: 1 * time.Hour,
			})
			if err != nil {
				t.Fatalf("Failed to create client: %v", err)
			}

			ctx := context.Background()
			key, err := client.CreateEphemeralKey(ctx, tt.workerID)

			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("Expected error %v, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr.Error()) && err != tt.wantErr {
					t.Errorf("Expected error %v, got %v", tt.wantErr, err)
				}
				if key != nil {
					t.Errorf("Expected nil key on error, got non-nil")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got %v", err)
				}
				if key == nil {
					t.Errorf("Expected non-nil key, got nil")
				}
				if tt.validateKey != nil && key != nil {
					tt.validateKey(t, key)
				}
			}
		})
	}
}

func TestCreateEphemeralKeyCaching(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(Key{
			ID:    "key-123",
			Key:   "tskey-auth-testkey",
			Expires: time.Now().Add(90 * 24 * time.Hour),
			Capabilities: KeyCapabilities{
				Devices: DeviceCapabilities{
					Create: DeviceCreateOptions{
						Ephemeral: true,
						Tags:      []string{"tag:needle-worker"},
					},
				},
			},
		})
	}))
	defer server.Close()

	client, err := New(Config{
		APIKey:        "test-api-key",
		Tailnet:       "test-tailnet",
		BaseURL:       server.URL,
		CacheTTL:      5 * time.Minute,
		CacheHoldDown: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	ctx := context.Background()

	// First call should hit API
	key1, err := client.CreateEphemeralKey(ctx, "worker-1")
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}
	if callCount != 1 {
		t.Errorf("Expected 1 API call after first request, got %d", callCount)
	}

	// Second call should use cache
	key2, err := client.CreateEphemeralKey(ctx, "worker-1")
	if err != nil {
		t.Fatalf("Second call failed: %v", err)
	}
	if callCount != 1 {
		t.Errorf("Expected still 1 API call after cached request, got %d", callCount)
	}

	// Keys should be identical
	if key1.ID != key2.ID {
		t.Errorf("Cached key ID mismatch: %s vs %s", key1.ID, key2.ID)
	}

	// Different worker should hit API again
	_, err = client.CreateEphemeralKey(ctx, "worker-2")
	if err != nil {
		t.Fatalf("Third call failed: %v", err)
	}
	if callCount != 2 {
		t.Errorf("Expected 2 API calls after different worker, got %d", callCount)
	}
}

func TestCreateEphemeralKeyHoldDown(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "Rate limit exceeded",
		})
	}))
	defer server.Close()

	client, err := New(Config{
		APIKey:        "test-api-key",
		Tailnet:       "test-tailnet",
		BaseURL:       server.URL,
		CacheHoldDown: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	ctx := context.Background()

	// First call should fail with rate limit
	_, err = client.CreateEphemeralKey(ctx, "worker-1")
	if err != ErrRateLimited {
		t.Errorf("Expected ErrRateLimited, got %v", err)
	}
	if callCount != 1 {
		t.Errorf("Expected 1 API call, got %d", callCount)
	}

	// Second call should fail with hold-down without hitting API
	_, err = client.CreateEphemeralKey(ctx, "worker-1")
	if err != ErrCacheHoldDown {
		t.Errorf("Expected ErrCacheHoldDown, got %v", err)
	}
	if callCount != 1 {
		t.Errorf("Expected still 1 API call (hold-down), got %d", callCount)
	}
}

func TestListKeys(t *testing.T) {
	expectedKeys := []Key{
		{
			ID:          "key-1",
			KeyType:     "EPHEMERAL",
			Description: "Key 1",
			Revoked:     false,
		},
		{
			ID:          "key-2",
			KeyType:     "EPHEMERAL",
			Description: "Key 2",
			Revoked:     true,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("Expected GET request, got %s", r.Method)
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("Expected Bearer token, got %s", auth)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ListKeysResponse{Keys: expectedKeys})
	}))
	defer server.Close()

	client, err := New(Config{
		APIKey:  "test-api-key",
		Tailnet: "test-tailnet",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	ctx := context.Background()
	keys, err := client.ListKeys(ctx)
	if err != nil {
		t.Fatalf("ListKeys failed: %v", err)
	}

	if len(keys) != len(expectedKeys) {
		t.Errorf("Expected %d keys, got %d", len(expectedKeys), len(keys))
	}

	for i, key := range keys {
		if key.ID != expectedKeys[i].ID {
			t.Errorf("Key %d: expected ID %s, got %s", i, expectedKeys[i].ID, key.ID)
		}
	}
}

func TestDeleteKey(t *testing.T) {
	tests := []struct {
		name           string
		keyID          string
		responseStatus int
		wantErr        bool
	}{
		{
			name:           "successful deletion",
			keyID:          "key-123",
			responseStatus: http.StatusOK,
			wantErr:        false,
		},
		{
			name:           "successful deletion with 204",
			keyID:          "key-456",
			responseStatus: http.StatusNoContent,
			wantErr:        false,
		},
		{
			name:           "not found",
			keyID:          "key-999",
			responseStatus: http.StatusNotFound,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "DELETE" {
					t.Errorf("Expected DELETE request, got %s", r.Method)
				}

				auth := r.Header.Get("Authorization")
				if !strings.HasPrefix(auth, "Bearer ") {
					t.Errorf("Expected Bearer token, got %s", auth)
				}

				w.WriteHeader(tt.responseStatus)
			}))
			defer server.Close()

			client, err := New(Config{
				APIKey:  "test-api-key",
				Tailnet: "test-tailnet",
				BaseURL: server.URL,
			})
			if err != nil {
				t.Fatalf("Failed to create client: %v", err)
			}

			ctx := context.Background()
			err = client.DeleteKey(ctx, tt.keyID)

			if tt.wantErr && err == nil {
				t.Errorf("Expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Expected no error, got %v", err)
			}
		})
	}
}

func TestCacheOperations(t *testing.T) {
	client, err := New(Config{
		APIKey:        "test-api-key",
		Tailnet:       "test-tailnet",
		CacheTTL:      1 * time.Hour,
		CacheHoldDown: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Test cache stats
	size, inHoldDown := client.GetCacheStats()
	if size != 0 {
		t.Errorf("Expected empty cache, got size %d", size)
	}
	if inHoldDown {
		t.Errorf("Expected not in hold-down initially")
	}

	// Test clear cache
	client.ClearCache()
	size, _ = client.GetCacheStats()
	if size != 0 {
		t.Errorf("Expected empty cache after clear, got size %d", size)
	}

	// Test cleanup
	expired := client.CleanupExpired()
	if expired != 0 {
		t.Errorf("Expected 0 expired entries, got %d", expired)
	}

	// Test invalidate worker
	client.InvalidateWorker("nonexistent")
	size, _ = client.GetCacheStats()
	if size != 0 {
		t.Errorf("Expected empty cache after invalidate, got size %d", size)
	}
}

func TestSetAPIKey(t *testing.T) {
	client, err := New(Config{
		APIKey:  "old-key",
		Tailnet: "test-tailnet",
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	if client.apiKey != "old-key" {
		t.Errorf("Initial API key mismatch")
	}

	client.SetAPIKey("new-key")
	if client.apiKey != "new-key" {
		t.Errorf("API key not updated")
	}
}

func TestGetters(t *testing.T) {
	customBaseURL := "https://custom.tailscale.com"
	customTailnet := "custom-tailnet"

	client, err := New(Config{
		APIKey:  "test-api-key",
		Tailnet: customTailnet,
		BaseURL: customBaseURL,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	if client.GetBaseURL() != customBaseURL {
		t.Errorf("Expected BaseURL %s, got %s", customBaseURL, client.GetBaseURL())
	}

	if client.GetTailnet() != customTailnet {
		t.Errorf("Expected Tailnet %s, got %s", customTailnet, client.GetTailnet())
	}
}

func TestCreateEphemeralKeyWithTags(t *testing.T) {
	customTags := []string{"tag:custom", "tag:worker"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req CreateKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("Failed to decode request: %v", err)
		}

		// Verify custom tags were used
		if len(req.Capabilities.Devices.Create.Tags) != 2 {
			t.Errorf("Expected 2 custom tags, got %d", len(req.Capabilities.Devices.Create.Tags))
		}
		if req.Capabilities.Devices.Create.Tags[0] != "tag:custom" {
			t.Errorf("Expected tag 'tag:custom', got %s", req.Capabilities.Devices.Create.Tags[0])
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(Key{
			ID:    "key-123",
			Key:   "tskey-auth-testkey",
			Expires: time.Now().Add(90 * 24 * time.Hour),
			Capabilities: KeyCapabilities{
				Devices: DeviceCapabilities{
					Create: DeviceCreateOptions{
						Ephemeral: true,
						Tags:      customTags,
					},
				},
			},
		})
	}))
	defer server.Close()

	client, err := New(Config{
		APIKey:  "test-api-key",
		Tailnet: "test-tailnet",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	ctx := context.Background()
	key, err := client.CreateEphemeralKeyWithTags(ctx, "worker-1", customTags)
	if err != nil {
		t.Fatalf("CreateEphemeralKeyWithTags failed: %v", err)
	}

	if len(key.Capabilities.Devices.Create.Tags) != 2 {
		t.Errorf("Expected 2 tags in response, got %d", len(key.Capabilities.Devices.Create.Tags))
	}
}
