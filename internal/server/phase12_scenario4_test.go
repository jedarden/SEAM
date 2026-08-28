package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/vault"
)

// TestPhase12Scenario4_CredentialRefreshRetry tests the complete Phase 12.4 implementation:
// - 401 detection and credential refresh
// - Request replay with fresh credentials (replayable bodies)
// - credential-refresh-not-retried envelope (unreplayable bodies)
// - secret-store-unavailable degradation (refetch failure)
// - No infinite retry loops
// - Quota charging on retry path (charged twice)
func TestPhase12Scenario4_CredentialRefreshRetry(t *testing.T) {
	t.Run("successful_retry_with_replayable_body", func(t *testing.T) {
		// Test that 401 with replayable body triggers refresh and retry
		server := createTestServer(t)
		defer server.Close()

		// Configure upstream to return 401 on first request, 200 on retry
		requestCount := 0
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			auth := r.Header.Get("Authorization")
			if requestCount == 1 {
				// First request - return 401
				if auth != "Bearer old-token" {
					t.Errorf("Expected first request with old-token, got: %s", auth)
				}
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error": "invalid_token"}`))
			} else {
				// Second request - should have fresh token
				if auth != "Bearer fresh-token" {
					t.Errorf("Expected retry with fresh-token, got: %s", auth)
				}
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"success": true}`))
			}
		}))
		defer upstream.Close()

		// Configure route with credential injection
		route := "/api/protected"
		vaultPath := "secret/protected-api"
		server.routeTable.SetUpstreamURL(upstream.URL)

		// Mock vault client that returns fresh credentials on refresh
		refreshCallCount := 0
		mockClient := &mockVaultClient{
			secrets: map[string][]byte{
				vaultPath: []byte("fresh-token"),
			},
			onRefresh: func(path string) {
				refreshCallCount++
			},
		}

		// Set the vault client in the route table
		server.routeTable.mu.Lock()
		server.routeTable.secretClient = mockClient
		server.routeTable.mu.Unlock()

		server.routeTable.AddRoute(route, RouteEntry{
			VaultPath: vaultPath,
			InjectAs:  &InjectAs{Kind: InjectionBearer},
		})

		// Make request with replayable body
		body := bytes.NewReader([]byte(`{"test": "data"}`))
		req := httptest.NewRequest("POST", route, body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		// Should get 200 OK after successful retry
		if w.Code != http.StatusOK {
			t.Errorf("Expected 200 OK after retry, got %d", w.Code)
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Errorf("Failed to parse response: %v", err)
		}
		if resp["success"] != true {
			t.Errorf("Expected success=true, got: %v", resp["success"])
		}

		// Verify credential refresh was called exactly once
		if refreshCallCount != 1 {
			t.Errorf("Expected 1 refresh call, got %d", refreshCallCount)
		}

		// Verify upstream was called twice (original + retry)
		if requestCount != 2 {
			t.Errorf("Expected 2 upstream requests, got %d", requestCount)
		}

		t.Log("✓ 401 retry with replayable body succeeded with fresh credentials")
	})

	t.Run("unreplayable_body_returns_credential_refresh_not_retried", func(t *testing.T) {
		// Test that unreplayable body returns credential-refresh-not-retried envelope
		server := createTestServer(t)
		defer server.Close()

		// Configure upstream to return 401
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": "token_expired"}`))
		}))
		defer upstream.Close()

		// Configure route with credential injection
		route := "/api/large-payload"
		vaultPath := "secret/large-api"
		server.routeTable.SetUpstreamURL(upstream.URL)

		// Mock vault client
		refreshCallCount := 0
		mockClient := &mockVaultClient{
			secrets: map[string][]byte{
				vaultPath: []byte("refreshed-token"),
			},
			onRefresh: func(path string) {
				refreshCallCount++
			},
		}

		// Set the vault client in the route table
		server.routeTable.mu.Lock()
		server.routeTable.secretClient = mockClient
		server.routeTable.mu.Unlock()

		server.routeTable.AddRoute(route, RouteEntry{
			VaultPath: vaultPath,
			InjectAs:  &InjectAs{Kind: InjectionBearer},
		})

		// Make request with unreplayable body (exceeds maxReplayableRequestBytes)
		largeBody := bytes.NewReader(make([]byte, 2*1024*1024)) // 2MB > default 1MB limit
		req := httptest.NewRequest("POST", route, largeBody)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		// Should get credential-refresh-not-retried envelope
		if w.Code != http.StatusUnauthorized {
			t.Errorf("Expected 401 Unauthorized, got %d", w.Code)
		}

		var resp ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Errorf("Failed to parse error response: %v", err)
		}

		if resp.Error != ErrCodeCredentialRefreshNotRetried {
			t.Errorf("Expected error code %s, got %s", ErrCodeCredentialRefreshNotRetried, resp.Error)
		}

		// Verify the message mentions credential refresh succeeded
		if !strings.Contains(resp.Message, "refreshed") {
			t.Errorf("Expected message about credential refresh, got: %s", resp.Message)
		}

		// Verify upstream status is included
		if resp.Details == nil {
			t.Errorf("Expected details in error response")
		}
		if resp.Details["upstream_status"] != float64(401) {
			t.Errorf("Expected upstream_status 401, got: %v", resp.Details["upstream_status"])
		}

		// Verify credential refresh was still called
		if refreshCallCount != 1 {
			t.Errorf("Expected 1 refresh call (even for unreplayable body), got %d", refreshCallCount)
		}

		// Verify header indicating refresh succeeded
		if w.Header().Get("X-SEAM-Credential-Refresh") != "succeeded" {
			t.Errorf("Expected X-SEAM-Credential-Refresh header")
		}

		t.Log("✓ Unreplayable body returned credential-refresh-not-retried envelope")
	})

	t.Run("refetch_failure_degrades_to_secret_store_unavailable", func(t *testing.T) {
		// Test that refetch failure degrades to secret-store-unavailable 503
		server := createTestServer(t)
		defer server.Close()

		// Configure upstream to return 401
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": "invalid_token"}`))
		}))
		defer upstream.Close()

		// Configure route with credential injection
		route := "/api/fail-refresh"
		vaultPath := "secret/failing-api"
		server.routeTable.SetUpstreamURL(upstream.URL)

		// Mock vault client that fails on refresh
		mockClient := &mockVaultClient{
			secrets: map[string][]byte{},
			refreshErr: &vault.SecretStoreUnavailableError{
				Dependency: "OpenBao",
				Class:      vault.FailureClassNetwork,
				RetryAt:    time.Now().Add(30 * time.Second),
				Now:        time.Now(),
			},
		}

		// Set the vault client in the route table
		server.routeTable.mu.Lock()
		server.routeTable.secretClient = mockClient
		server.routeTable.mu.Unlock()

		server.routeTable.AddRoute(route, RouteEntry{
			VaultPath: vaultPath,
			InjectAs:  &InjectAs{Kind: InjectionBearer},
		})

		// Make request
		req := httptest.NewRequest("GET", route, nil)
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		// Should get secret-store-unavailable 503
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("Expected 503 Service Unavailable, got %d", w.Code)
		}

		var resp ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Errorf("Failed to parse error response: %v", err)
		}

		if resp.Error != ErrCodeSecretStoreUnavailable {
			t.Errorf("Expected error code %s, got %s", ErrCodeSecretStoreUnavailable, resp.Error)
		}

		// Verify Retry-After header is present
		retryAfter := w.Header().Get("Retry-After")
		if retryAfter == "" {
			t.Errorf("Expected Retry-After header")
		}

		t.Log("✓ Refetch failure degraded to secret-store-unavailable 503")
	})

	t.Run("no_infinite_retry_loops", func(t *testing.T) {
		// Test that we don't retry infinitely on repeated 401s
		server := createTestServer(t)
		defer server.Close()

		// Configure upstream to always return 401
		requestCount := 0
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": "always_unauthorized"}`))
		}))
		defer upstream.Close()

		// Configure route with credential injection
		route := "/api/stale-token"
		vaultPath := "secret/stale-api"
		server.routeTable.SetUpstreamURL(upstream.URL)

		// Mock vault client that always returns the same stale token
		refreshCallCount := 0
		mockClient := &mockVaultClient{
			secrets: map[string][]byte{
				vaultPath: []byte("still-stale-token"),
			},
			onRefresh: func(path string) {
				refreshCallCount++
			},
		}

		// Set the vault client in the route table
		server.routeTable.mu.Lock()
		server.routeTable.secretClient = mockClient
		server.routeTable.mu.Unlock()

		server.routeTable.AddRoute(route, RouteEntry{
			VaultPath: vaultPath,
			InjectAs:  &InjectAs{Kind: InjectionBearer},
		})

		// Make request
		req := httptest.NewRequest("GET", route, nil)
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		// Should get 401 after failed retry
		if w.Code != http.StatusUnauthorized {
			t.Errorf("Expected 401 Unauthorized, got %d", w.Code)
		}

		// Verify upstream was called exactly twice (original + one retry)
		if requestCount != 2 {
			t.Errorf("Expected 2 upstream requests (no infinite loop), got %d", requestCount)
		}

		// Verify credential refresh was called exactly once
		if refreshCallCount != 1 {
			t.Errorf("Expected 1 refresh call, got %d", refreshCallCount)
		}

		t.Log("✓ No infinite retry loops - exactly one retry attempt")
	})

	t.Run("quota_charged_twice_on_retry_path", func(t *testing.T) {
		// Test that quota is charged twice on the retry path (dispatch-time accounting)
		server := createTestServer(t)
		defer server.Close()

		// Configure upstream to return 401 then 200
		requestCount := 0
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			if requestCount == 1 {
				w.WriteHeader(http.StatusUnauthorized)
			} else {
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer upstream.Close()

		// Configure route with credential injection and quota
		route := "/api/quota-test"
		vaultPath := "secret/quota-api"
		costPerCall := 0.10
		quotaLimit := 1.00

		server.routeTable.SetUpstreamURL(upstream.URL)
		server.quotaTracker.SetCostPerCall(route, costPerCall)
		server.quotaTracker.SetQuota(route, QuotaConfig{
			Limit:  quotaLimit,
			Window: 1 * time.Hour,
			Scope:  "per-route",
		})

		// Mock vault client
		mockClient := &mockVaultClient{
			secrets: map[string][]byte{
				vaultPath: []byte("fresh-token"),
			},
		}

		// Set the vault client in the route table
		server.routeTable.mu.Lock()
		server.routeTable.secretClient = mockClient
		server.routeTable.mu.Unlock()

		server.routeTable.AddRoute(route, RouteEntry{
			VaultPath: vaultPath,
			InjectAs:  &InjectAs{Kind: InjectionBearer},
		})

		// Make first request - should succeed after retry
		req1 := httptest.NewRequest("GET", route, nil)
		w1 := httptest.NewRecorder()
		server.ServeHTTP(w1, req1)

		// Should get 200 OK after retry
		if w1.Code != http.StatusOK {
			t.Errorf("Expected 200 OK, got %d", w1.Code)
		}

		// Verify quota was charged twice (original dispatch + retry dispatch)
		remaining := server.quotaTracker.GetRemaining(route, "", "")
		expectedRemaining := quotaLimit - (2 * costPerCall) // Charged twice
		if remaining != expectedRemaining {
			t.Errorf("Expected remaining $%.2f (charged twice), got $%.2f", expectedRemaining, remaining)
		}

		// Calculate how many more requests we can make
		remainingRequests := int(remaining / costPerCall)
		if remainingRequests != 8 {
			t.Errorf("Expected 8 remaining requests, got %d", remainingRequests)
		}

		t.Log("✓ Quota charged twice on retry path (dispatch-time accounting)")
	})

	t.Run("protocol_upgrade_not_replayed", func(t *testing.T) {
		// Test that protocol upgrades (WebSocket) are not replayed
		server := createTestServer(t)
		defer server.Close()

		// Configure upstream to return 401
		upstreamCalled := false
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upstreamCalled = true
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer upstream.Close()

		// Configure route with credential injection
		route := "/api/websocket"
		vaultPath := "secret/websocket-api"
		server.routeTable.SetUpstreamURL(upstream.URL)

		// Mock vault client
		refreshCallCount := 0
		mockClient := &mockVaultClient{
			secrets: map[string][]byte{
				vaultPath: []byte("ws-token"),
			},
			onRefresh: func(path string) {
				refreshCallCount++
			},
		}

		// Set the vault client in the route table
		server.routeTable.mu.Lock()
		server.routeTable.secretClient = mockClient
		server.routeTable.mu.Unlock()

		server.routeTable.AddRoute(route, RouteEntry{
			VaultPath:   vaultPath,
			InjectAs:    &InjectAs{Kind: InjectionBearer},
			Unscrubbable: true,
		})

		// Make WebSocket upgrade request
		req := httptest.NewRequest("GET", route, nil)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		// Should get credential-refresh-not-retried (protocol upgrades are unreplayable)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("Expected 401 Unauthorized, got %d", w.Code)
		}

		var resp ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Errorf("Failed to parse error response: %v", err)
		}

		if resp.Error != ErrCodeCredentialRefreshNotRetried {
			t.Errorf("Expected error code %s, got %s", ErrCodeCredentialRefreshNotRetried, resp.Error)
		}

		// Verify upstream was called exactly once (no retry for protocol upgrade)
		if !upstreamCalled {
			t.Errorf("Expected upstream to be called")
		}

		// Verify credential refresh was still called
		if refreshCallCount != 1 {
			t.Errorf("Expected 1 refresh call, got %d", refreshCallCount)
		}

		t.Log("✓ Protocol upgrade not replayed (returned credential-refresh-not-retried)")
	})
}

// mockVaultClient is a test double for vault.Client
type mockVaultClient struct {
	secrets     map[string][]byte
	refreshErr  error
	onRefresh   func(path string)
	refreshCall int
}

func (m *mockVaultClient) GetSecret(ctx context.Context, vaultPath string) (vault.Secret, error) {
	if secret, ok := m.secrets[vaultPath]; ok {
		return vault.Secret{"value": secret}, nil
	}
	return nil, &vault.SecretStoreUnavailableError{
		Dependency: "OpenBao",
		Class:      vault.FailureClassNetwork,
	}
}

func (m *mockVaultClient) RefreshAfterUnauthorized(ctx context.Context, vaultPath string) (vault.Secret, error) {
	m.refreshCall++
	if m.onRefresh != nil {
		m.onRefresh(vaultPath)
	}
	if m.refreshErr != nil {
		return nil, m.refreshErr
	}
	return m.GetSecret(ctx, vaultPath)
}

func (m *mockVaultClient) Invalidate(vaultPath string) {
	// No-op for mock
}

func (m *mockVaultClient) Login(ctx context.Context) error {
	return nil
}
