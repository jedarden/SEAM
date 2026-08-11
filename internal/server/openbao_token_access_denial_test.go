package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/testutil/openbao"
)

// TestOpenBaoTokenAccessDenial verifies that SEAM's OpenBao role CANNOT read
// the evaluator's token path, proving proper isolation between services.
//
// This test:
// 1. Creates a test OpenBao server
// 2. Writes SEAM policy (restricted to seam/routes/*, explicitly denied evaluators/*)
// 3. Creates evaluator token path
// 4. Creates a SEAM token with restricted policy
// 5. Attempts to read evaluator token using SEAM token
// 6. Verifies the read is denied with permission error
// 7. Test passes ONLY when access is denied
func TestOpenBaoTokenAccessDenial(t *testing.T) {
	t.Run("SEAM_cannot_read_evaluator_token", func(t *testing.T) {
		testSEAMCannotReadEvaluatorToken(t)
	})
}

// testSEAMCannotReadEvaluatorToken implements the actual test logic
func testSEAMCannotReadEvaluatorToken(t *testing.T) {
	// Step 1: Start OpenBao test server
	server, err := openbao.NewServer(openbao.ServerConfig{
		DevToken:   "test-root-token",
		ListenAddr: "localhost:18210",
	})
	if err != nil {
		t.Skipf("Failed to start OpenBao test server: %v (skipping integration test)", err)
		return
	}
	defer server.Close()

	// Wait for server to be ready
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rootClient := server.Client()
	baseURL := server.BaseURL()

	// Step 2: Create SEAM policy with restricted access
	seamPolicyHCL := `# Allow reading SEAM route secrets ONLY
path "secret/data/seam/routes/*" {
  capabilities = ["read"]
}

# Deny access to evaluator's secrets (explicit separation of concerns)
path "secret/data/evaluators/*" {
  capabilities = ["deny"]
}

# Deny access to all other secrets (default-deny)
path "secret/data/*" {
  capabilities = ["deny"]
}`

	if err := createPolicy(ctx, baseURL, server.DevToken(), "seam-test-policy", seamPolicyHCL); err != nil {
		t.Fatalf("Failed to create SEAM policy: %v", err)
	}

	// Step 3: Create evaluator token path
	evaluatorTokenPath := "evaluators/seam-retirement-evaluator/github-token"
	evaluatorSecret := map[string]interface{}{
		"token": "ghp_testevaluatortoken12345678",
		"type":  "github_pat",
	}

	if err := rootClient.WriteSecret(ctx, evaluatorTokenPath, evaluatorSecret); err != nil {
		t.Fatalf("Failed to create evaluator token path: %v", err)
	}

	// Verify evaluator path exists with root token
	secretData, err := rootClient.ReadSecret(ctx, evaluatorTokenPath)
	if err != nil {
		t.Fatalf("Evaluator token path should be accessible to root: %v", err)
	}
	if secretData["token"] != "ghp_testevaluatortoken12345678" {
		t.Fatalf("Evaluator token value mismatch: got %v", secretData)
	}

	// Step 4: Create a SEAM token with restricted policy
	seamToken, err := createToken(ctx, baseURL, server.DevToken(), "seam-test-policy")
	if err != nil {
		t.Fatalf("Failed to create SEAM token: %v", err)
	}

	// Step 5: Attempt to read evaluator token using SEAM token
	seamClient := openbao.NewClient(baseURL, seamToken)

	// This MUST fail - SEAM should not be able to read evaluator secrets
	_, err = seamClient.ReadSecret(ctx, evaluatorTokenPath)

	// Step 6: Verify the failure is due to permission denied
	if err == nil {
		// TEST FAILS - SEAM was able to read the evaluator token (security breach!)
		t.Fatalf("SECURITY BREACH: SEAM token was able to read evaluator token at path %s - isolation failed!", evaluatorTokenPath)
	}

	// Verify the error message indicates permission denied
	errorMsg := err.Error()
	if !strings.Contains(errorMsg, "403") &&
	   !strings.Contains(errorMsg, "permission denied") &&
	   !strings.Contains(errorMsg, "Permission denied") &&
	   !strings.Contains(errorMsg, "code 403") {
		t.Fatalf("Expected permission denied error (403), got: %v", err)
	}

	// Additional validation: Verify SEAM CAN read its own secrets
	seamOwnPath := "seam/routes/testservice/token"
	seamOwnSecret := map[string]interface{}{
		"token": "test-seam-token-abc123",
	}
	if err := rootClient.WriteSecret(ctx, seamOwnPath, seamOwnSecret); err != nil {
		t.Fatalf("Failed to create SEAM's own secret: %v", err)
	}

	seamOwnData, err := seamClient.ReadSecret(ctx, seamOwnPath)
	if err != nil {
		t.Fatalf("SEAM should be able to read its own secrets: %v", err)
	}
	if seamOwnData["token"] != "test-seam-token-abc123" {
		t.Fatalf("SEAM's own token value mismatch: got %v", seamOwnData)
	}

	// TEST PASSES - SEAM can read its own secrets but is denied evaluator access
	t.Logf("✓ SUCCESS: SEAM token correctly denied access to evaluator token path")
	t.Logf("✓ SUCCESS: SEAM token can still read its own secrets (seam/routes/*)")
	t.Logf("✓ Isolation verified: SEAM and evaluator secrets are properly separated")
}

// createPolicy creates an OpenBao policy via HTTP API
func createPolicy(ctx context.Context, baseURL, token, policyName, policyHCL string) error {
	payload := map[string]string{
		"policy": policyHCL,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal policy payload: %w", err)
	}

	url := fmt.Sprintf("%s/v1/sys/policies/acl/%s", baseURL, policyName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("X-Vault-Token", token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create policy failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// createToken creates an OpenBao token with specific policies via HTTP API
func createToken(ctx context.Context, baseURL, token, policies string) (string, error) {
	payload := map[string]interface{}{
		"policies": []string{policies},
		"ttl":      "24h",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal token payload: %w", err)
	}

	url := fmt.Sprintf("%s/v1/auth/token/create", baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("X-Vault-Token", token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create token failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if result.Auth.ClientToken == "" {
		return "", fmt.Errorf("received empty token from OpenBao")
	}

	return result.Auth.ClientToken, nil
}

// TestOpenBaoTokenAccessDenial_ManualVerification provides a manual verification
// test that can be run against a real OpenBao instance for production validation.
// This is useful for CI/CD pipelines that need to verify actual cluster isolation.
func TestOpenBaoTokenAccessDenial_ManualVerification(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping manual verification test in short mode")
	}

	// This test requires manual setup or environment variables
	// It's designed for production validation, not unit testing

	openbaoAddr := openBaoEndpoint()
	seamToken := seamTokenForTesting()
	evaluatorPath := "evaluators/seam-retirement-evaluator/github-token"

	if openbaoAddr == "" || seamToken == "" {
		t.Skip("Manual verification requires TEST_OPENBAO_ADDR and TEST_SEAM_TOKEN environment variables")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	seamClient := openbao.NewClient(openbaoAddr, seamToken)

	// Attempt to read evaluator token using SEAM credentials
	_, err := seamClient.ReadSecret(ctx, evaluatorPath)

	// Verify access is denied
	if err == nil {
		t.Fatalf("PRODUCTION SECURITY ISSUE: SEAM can read evaluator token at %s", evaluatorPath)
	}

	errorMsg := err.Error()
	if !strings.Contains(errorMsg, "403") && !strings.Contains(strings.ToLower(errorMsg), "permission denied") {
		t.Fatalf("Expected permission denied, got: %v", err)
	}

	t.Logf("✓ Production isolation verified: SEAM cannot access evaluator token path")
}

// openBaoEndpoint returns the OpenBao endpoint from environment or default
func openBaoEndpoint() string {
	if addr := openbaoEnv("TEST_OPENBAO_ADDR"); addr != "" {
		return addr
	}
	// Try common production endpoints
	endpoints := []string{
		"http://openbao-rs-manager.openbao.svc.cluster.local:8200",
		"http://openbao-ardenone.tail1b1987.ts.net:8200",
		"http://localhost:8200",
	}
	for _, addr := range endpoints {
		// Quick connectivity check
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/v1/sys/health", nil)
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			return addr
		}
	}
	return ""
}

// seamTokenForTesting returns a SEAM token for testing from environment
func seamTokenForTesting() string {
	return openbaoEnv("TEST_SEAM_TOKEN")
}

// openbaoEnv reads an environment variable
func openbaoEnv(key string) string {
	return os.Getenv(key)
}
