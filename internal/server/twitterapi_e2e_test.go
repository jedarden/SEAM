//go:build integration
// +build integration

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/testutil/openbao"
	"github.com/ardenone/seam/internal/testutil/stubupstream"
)

// TestE2E_TwitterAPIInjection tests end-to-end credential injection for twitterapi.io:
// 1. Fragment loads with x-inject-as: header/x-api-key
// 2. OpenBao secret is fetched from rs-manager/seam/routes/twitterapi/api-key
// 3. Credential is injected as x-api-key header in upstream request
// 4. Response is scrubbed to remove the credential
// 5. Caller receives clean response
func TestE2E_TwitterAPIInjection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping twitterapi e2e test in short mode")
	}

	openbao.SkipIfNoOpenBao(t)

	// Phase 1: Setup OpenBao with test twitterapi credential
	t.Log("=== Phase 1: Setting up OpenBao with twitterapi credential ===")

	obServer, err := openbao.NewServer(openbao.ServerConfig{
		DevToken:   "test-root-token",
		ListenAddr: "localhost:18240",
	})
	if err != nil {
		t.Skipf("Failed to start OpenBao test server: %v (skipping integration test)", err)
		return
	}
	defer func() { _ = obServer.Close() }()

	obClient := obServer.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create test secret at the correct path
	testAPIKey := "test-twitterapi-key-" + time.Now().Format("20060102150405")
	err = obClient.WriteSecret(ctx, "seam/routes/twitterapi/api-key", map[string]interface{}{
		"api-key": testAPIKey,
		"type":    "header",
	})
	if err != nil {
		t.Fatalf("Failed to write twitterapi test secret: %v", err)
	}
	defer func() { _ = obClient.DeleteSecret(ctx, "seam/routes/twitterapi/api-key") }()

	t.Logf("✓ OpenBao test server running with twitterapi credential")

	// Phase 2: Start stub upstream server that verifies x-api-key injection
	t.Log("=== Phase 2: Starting stub upstream server ===")

	var receivedHeaders http.Header
	stub := stubupstream.New(stubupstream.Config{
		Addr: "localhost:15840",
		Behavior: func(w http.ResponseWriter, r *http.Request) {
			// Capture headers for verification
			receivedHeaders = r.Header.Clone()

			// Return mock twitterapi.io response
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			// Mock response matching twitterapi.io envelope
			mockResponse := map[string]interface{}{
				"status": "success",
				"msg":    "success",
				"data": map[string]interface{}{
					"id":        "123456789",
					"userName":  "testuser",
					"followers": 100,
				},
			}
			json.NewEncoder(w).Encode(mockResponse)
		},
	})

	if err := stub.Start(); err != nil {
		t.Fatalf("Failed to start stub upstream: %v", err)
	}
	defer func() { _ = stub.Stop(context.Background()) }()

	time.Sleep(100 * time.Millisecond)
	t.Logf("✓ Stub upstream server running at %s", stub.URL())

	// Phase 3: Start SEAM server with fragment mode enabled
	t.Log("=== Phase 3: Starting SEAM server ===")

	cfg := &Config{
		CallerPort:   8084,
		OperatorPort: 8085,
		BaseURL:      "http://localhost:8084",
		SpecDir:      "../../spec",
	}

	seamServer := New(cfg)
	startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startCancel()

	if err := seamServer.Start(startCtx); err != nil {
		t.Fatalf("Failed to start SEAM server: %v", err)
	}
	defer func() { _ = seamServer.Shutdown(context.Background()) }()

	time.Sleep(200 * time.Millisecond)
	t.Logf("✓ SEAM server running")

	// Phase 4: Make request through SEAM and verify injection
	t.Log("=== Phase 4: Testing credential injection ===")

	// Make request to /twitterapi/user/info (mapped from fragment)
	reqURL := "http://localhost:8084/twitterapi/user/info?userName=testuser"
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	// Verify response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to parse response JSON: %v", err)
	}

	if result["status"] != "success" {
		t.Errorf("Expected status 'success', got '%v'", result["status"])
	}

	t.Logf("✓ Received successful response from SEAM")

	// Phase 5: Verify credential was injected upstream
	t.Log("=== Phase 5: Verifying upstream credential injection ===")

	receivedAPIKey := receivedHeaders.Get("x-api-key")
	if receivedAPIKey == "" {
		t.Fatal("x-api-key header was NOT injected into upstream request")
	}

	if receivedAPIKey != testAPIKey {
		t.Errorf("Injected x-api-key mismatch: got %s, want %s", receivedAPIKey, testAPIKey)
	}

	t.Logf("✓ Credential correctly injected as x-api-key header")

	// Phase 6: Verify credential was scrubbed from response
	t.Log("=== Phase 6: Verifying credential scrubbing ===")

	respStr := string(body)
	if strings.Contains(respStr, testAPIKey) {
		t.Error("Credential was NOT scrubbed from response body")
	}

	// Check response headers don't leak the credential
	for key, values := range resp.Header {
		for _, value := range values {
			if strings.Contains(strings.ToLower(value), strings.ToLower(testAPIKey)) {
				t.Errorf("Credential leaked in response header %s: %s", key, value)
			}
		}
	}

	t.Logf("✓ Credential properly scrubbed from response")

	t.Log("=== All E2E Tests Passed ===")
}
