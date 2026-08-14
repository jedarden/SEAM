// +build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ardenone/seam/internal/testutil/openbao"
)

func main() {
	fmt.Println("=== OpenBao Client Connection Test ===")

	// Step 1: Create OpenBao client with proper authentication
	fmt.Println("\n[Step 1] Creating OpenBao client...")

	// Configuration from environment variables with fallback defaults
	openbaoAddr := os.Getenv("OPENBAO_ADDR")
	if openbaoAddr == "" {
		openbaoAddr = "http://localhost:8200"
	}

	openbaoToken := os.Getenv("OPENBAO_TOKEN")
	if openbaoToken == "" {
		openbaoToken = "dev-root-token" // Default for dev mode
	}

	fmt.Printf("  OpenBao Address: %s\n", openbaoAddr)
	fmt.Printf("  OpenBao Token: %s\n", maskToken(openbaoToken))

	client := openbao.NewClient(openbaoAddr, openbaoToken)
	fmt.Println("  ✓ Client created successfully")

	// Step 2: Verify connection to OpenBao server succeeds
	fmt.Println("\n[Step 2] Verifying connection to OpenBao server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Test connection by attempting to read a path (even if it doesn't exist)
	testPath := "test/connection-check"
	_, err := client.ReadSecret(ctx, testPath)

	// We expect either success (if path exists) or a 404 (if path doesn't exist)
	// Connection errors indicate the server is unreachable
	if err != nil {
		if isConnectionError(err) {
			log.Fatalf("  ✗ Failed to connect to OpenBao server: %v\n", err)
		}
		// 404 or other non-connection errors are fine - means server is reachable
		fmt.Printf("  ℹ Server reachable (expected error for non-existent path): %v\n", err)
	} else {
		fmt.Println("  ✓ Successfully connected to OpenBao server")
	}

	// Step 3: Test basic read access with a simple vault read command
	fmt.Println("\n[Step 3] Testing basic read access...")

	// Try to read a known test path
	secretPath := "secret/test" // Adjust based on your actual secrets
	secretData, err := client.ReadSecret(ctx, secretPath)

	if err != nil {
		if isNotFoundError(err) {
			fmt.Printf("  ℹ Path '%s' does not exist (expected in clean state)\n", secretPath)
			fmt.Println("  → Read access verified (got proper 404 response)")
		} else {
			log.Fatalf("  ✗ Failed to read secret: %v\n", err)
		}
	} else {
		fmt.Printf("  ✓ Successfully read secret from '%s'\n", secretPath)
		fmt.Printf("    Secret data: %v\n", redactSecrets(secretData))
	}

	// Step 4: Document connection method
	fmt.Println("\n[Step 4] Connection Method Documentation:")
	fmt.Println("  ✓ Client Authentication: Token-based (X-Vault-Token header)")
	fmt.Println("  ✓ Connection Method: HTTP/HTTPS to OpenBao server")
	fmt.Println("  ✓ Required Credentials:")
	fmt.Printf("    - Server Address: OPENBAO_ADDR env var (default: http://localhost:8200)\n")
	fmt.Printf("    - Authentication Token: OPENBAO_TOKEN env var (default: dev-root-token)\n")
	fmt.Println("  ✓ Client Implementation: internal/testutil/openbao/openbao.go")
	fmt.Println("  ✓ API Operations Supported:")
	fmt.Println("    - ReadSecret(ctx, path)")
	fmt.Println("    - WriteSecret(ctx, path, data)")
	fmt.Println("    - DeleteSecret(ctx, path)")

	fmt.Println("\n=== Test Complete ===")
	fmt.Println("✓ OpenBao client connection verified successfully")
}

// maskToken masks the token for display purposes
func maskToken(token string) string {
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

// isConnectionError checks if an error indicates a connection problem
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	// Check for common connection error patterns
	connectionPatterns := []string{
		"connection refused",
		"connect: connection refused",
		"no such host",
		"timeout",
		"connection reset",
	}

	for _, pattern := range connectionPatterns {
		if contains(errMsg, pattern) {
			return true
		}
	}
	return false
}

// isNotFoundError checks if an error indicates a 404/not found
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	return contains(errMsg, "404") ||
	       contains(errMsg, "not found") ||
	       contains(errMsg, "Invalid path")
}

// contains is a simple string contains helper
func contains(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// redactSecrets removes sensitive values from secret data for logging
func redactSecrets(data map[string]interface{}) map[string]interface{} {
	redacted := make(map[string]interface{})
	for k, v := range data {
		// Redact common secret key names
		if contains(k, "token") || contains(k, "password") || contains(k, "secret") || contains(k, "key") {
			redacted[k] = "***REDACTED***"
		} else {
			redacted[k] = v
		}
	}
	return redacted
}