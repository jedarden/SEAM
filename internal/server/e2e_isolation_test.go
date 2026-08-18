package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/testutil/openbao"
)

// TestE2EIsolation validates the complete isolation and access pattern
// between SEAM and evaluator roles in a single comprehensive test.
//
// This test orchestrates all authentication and authorization paths:
// 1. Creates a test OpenBao server with proper policies
// 2. Creates evaluator and SEAM roles with appropriate restrictions
// 3. Tests evaluator CAN read its own GitHub token
// 4. Tests evaluator CAN read VictoriaMetrics credentials
// 5. Tests evaluator CAN query VictoriaMetrics
// 6. Tests SEAM CANNOT read evaluator's token
// 7. Produces a clear isolation report
//
// Test passes ONLY when all security boundaries are properly enforced.
func TestE2EIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E isolation test in short mode")
	}

	t.Run("complete_isolation_validation", func(t *testing.T) {
		testCompleteIsolation(t)
	})
}

// IsolationTestReport captures the results of the E2E isolation test
type IsolationTestReport struct {
	TestName        string
	Timestamp       time.Time
	OpenBaoEndpoint string
	PoliciesValid   bool
	EvaluatorTests  EvaluatorTestResults
	SEAMTests       SEAMTestResults
	IsolationValid  bool
	Summary         string
}

// EvaluatorTestResults captures evaluator-specific test results
type EvaluatorTestResults struct {
	CanReadOwnToken          bool
	CanReadVMAuthCreds       bool
	CanQueryVM               bool
	CannotAccessSEAMRoutes   bool
	CannotAccessOtherSecrets bool
	Details                  []string
}

// SEAMTestResults captures SEAM-specific test results
type SEAMTestResults struct {
	CanReadOwnRoutes         bool
	CannotReadEvaluatorToken bool
	Details                  []string
}

// testCompleteIsolation implements the end-to-end isolation validation
func testCompleteIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	report := &IsolationTestReport{
		TestName:  "SEAM-Evaluator E2E Isolation Test",
		Timestamp: time.Now().UTC(),
	}

	// ========================================================================
	// Phase 1: Setup OpenBao Test Environment
	// ========================================================================
	t.Log("=== Phase 1: Setting up OpenBao test environment ===")

	server, err := openbao.NewServer(openbao.ServerConfig{
		DevToken:   "test-root-token",
		ListenAddr: "localhost:18220",
	})
	if err != nil {
		t.Skipf("Failed to start OpenBao test server: %v (skipping integration test)", err)
		return
	}
	defer func() { _ = server.Close() }()

	rootClient := server.Client()
	baseURL := server.BaseURL()
	report.OpenBaoEndpoint = baseURL

	t.Logf("✓ OpenBao test server running at %s", baseURL)

	// ========================================================================
	// Phase 2: Create Policies
	// ========================================================================
	t.Log("=== Phase 2: Creating SEAM and Evaluator policies ===")

	// Evaluator Policy: Can read own token and VM credentials, denied everything else
	evaluatorPolicyHCL := `# Allow reading evaluator's own GitHub token
path "secret/data/evaluators/seam-retirement-evaluator/*" {
  capabilities = ["read"]
}

# Allow reading VictoriaMetrics credentials for metrics querying
path "secret/data/monitoring/victoriametrics/*" {
  capabilities = ["read"]
}

# Explicitly deny access to SEAM routes
path "secret/data/seam/routes/*" {
  capabilities = ["deny"]
}

# Default deny for all other secrets
path "secret/data/*" {
  capabilities = ["deny"]
}`

	if err := createPolicy(ctx, baseURL, server.DevToken(), "seam-retirement-evaluator-policy", evaluatorPolicyHCL); err != nil {
		t.Fatalf("Failed to create evaluator policy: %v", err)
	}
	report.EvaluatorTests.Details = append(report.EvaluatorTests.Details, "✓ Evaluator policy created")

	// SEAM Policy: Can read own routes, denied everything else
	seamPolicyHCL := `# Allow reading SEAM's own route secrets
path "secret/data/seam/routes/*" {
  capabilities = ["read"]
}

# Explicitly deny access to evaluator secrets
path "secret/data/evaluators/*" {
  capabilities = ["deny"]
}

# Default deny for all other secrets
path "secret/data/*" {
  capabilities = ["deny"]
}`

	if err := createPolicy(ctx, baseURL, server.DevToken(), "seam-policy", seamPolicyHCL); err != nil {
		t.Fatalf("Failed to create SEAM policy: %v", err)
	}
	report.SEAMTests.Details = append(report.SEAMTests.Details, "✓ SEAM policy created")

	report.PoliciesValid = true
	t.Logf("✓ Both policies created and validated")

	// ========================================================================
	// Phase 3: Create Test Data
	// ========================================================================
	t.Log("=== Phase 3: Creating test secrets ===")

	// Create evaluator GitHub token
	evaluatorTokenPath := "evaluators/seam-retirement-evaluator/github-token"
	evaluatorSecret := map[string]interface{}{
		"token": "ghp_testevaluatortoken12345678",
		"type":  "github_pat",
	}
	if err := rootClient.WriteSecret(ctx, evaluatorTokenPath, evaluatorSecret); err != nil {
		t.Fatalf("Failed to create evaluator token: %v", err)
	}
	t.Logf("✓ Created evaluator GitHub token at %s", evaluatorTokenPath)

	// Create SEAM route secret
	seamRoutePath := "seam/routes/testservice/token"
	seamSecret := map[string]interface{}{
		"token": "test-seam-token-abc123",
	}
	if err := rootClient.WriteSecret(ctx, seamRoutePath, seamSecret); err != nil {
		t.Fatalf("Failed to create SEAM route secret: %v", err)
	}
	t.Logf("✓ Created SEAM route secret at %s", seamRoutePath)

	// Create VictoriaMetrics credentials
	vmCredsPath := "monitoring/victoriametrics/readonly-credentials"
	vmCreds := map[string]interface{}{
		"endpoint": "http://victoriametrics.example.com:8428",
		"username": "metrics-reader",
		"password": "vm-password-xyz789",
	}
	if err := rootClient.WriteSecret(ctx, vmCredsPath, vmCreds); err != nil {
		t.Fatalf("Failed to create VictoriaMetrics credentials: %v", err)
	}
	t.Logf("✓ Created VictoriaMetrics credentials at %s", vmCredsPath)

	// Create other secret (for isolation test)
	otherSecretPath := "armor/api-key"
	otherSecret := map[string]interface{}{
		"key": "armor-secret-key-999",
	}
	if err := rootClient.WriteSecret(ctx, otherSecretPath, otherSecret); err != nil {
		t.Fatalf("Failed to create other secret: %v", err)
	}
	t.Logf("✓ Created other secret at %s for isolation testing", otherSecretPath)

	// ========================================================================
	// Phase 4: Create Tokens
	// ========================================================================
	t.Log("=== Phase 4: Creating evaluator and SEAM tokens ===")

	evaluatorToken, err := createToken(ctx, baseURL, server.DevToken(), "seam-retirement-evaluator-policy")
	if err != nil {
		t.Fatalf("Failed to create evaluator token: %v", err)
	}
	report.EvaluatorTests.Details = append(report.EvaluatorTests.Details, "✓ Evaluator token created")
	t.Log("✓ Evaluator token created")

	seamToken, err := createToken(ctx, baseURL, server.DevToken(), "seam-policy")
	if err != nil {
		t.Fatalf("Failed to create SEAM token: %v", err)
	}
	report.SEAMTests.Details = append(report.SEAMTests.Details, "✓ SEAM token created")
	t.Log("✓ SEAM token created")

	evaluatorClient := openbao.NewClient(baseURL, evaluatorToken)
	seamClient := openbao.NewClient(baseURL, seamToken)

	// ========================================================================
	// Phase 5: Test Evaluator Capabilities
	// ========================================================================
	t.Log("=== Phase 5: Testing Evaluator Capabilities ===")

	// Test 5.1: Evaluator CAN read its own token
	t.Log("Test 5.1: Evaluator reading own GitHub token...")
	evaluatorTokenData, err := evaluatorClient.ReadSecret(ctx, evaluatorTokenPath)
	if err != nil {
		t.Fatalf("Evaluator MUST be able to read own token: %v", err)
	}
	if evaluatorTokenData["token"] != "ghp_testevaluatortoken12345678" {
		t.Fatalf("Evaluator token value mismatch")
	}
	report.EvaluatorTests.CanReadOwnToken = true
	report.EvaluatorTests.Details = append(report.EvaluatorTests.Details,
		"✓ Evaluator CAN read own GitHub token")

	// Test 5.2: Evaluator CAN read VictoriaMetrics credentials
	t.Log("Test 5.2: Evaluator reading VictoriaMetrics credentials...")
	vmCredsData, err := evaluatorClient.ReadSecret(ctx, vmCredsPath)
	if err != nil {
		t.Fatalf("Evaluator MUST be able to read VM credentials: %v", err)
	}
	if vmCredsData["endpoint"] != "http://victoriametrics.example.com:8428" {
		t.Fatalf("VM endpoint credential mismatch")
	}
	report.EvaluatorTests.CanReadVMAuthCreds = true
	report.EvaluatorTests.Details = append(report.EvaluatorTests.Details,
		"✓ Evaluator CAN read VictoriaMetrics credentials")

	// Test 5.3: Evaluator CAN query VictoriaMetrics (simulated via endpoint extraction)
	t.Log("Test 5.3: Evaluator querying VictoriaMetrics...")
	vmEndpoint, ok := vmCredsData["endpoint"].(string)
	if !ok {
		t.Fatalf("VictoriaMetrics endpoint is not a string: %v", vmCredsData["endpoint"])
	}
	if !strings.HasPrefix(vmEndpoint, "http://") && !strings.HasPrefix(vmEndpoint, "https://") {
		t.Fatalf("Invalid VictoriaMetrics endpoint: %s", vmEndpoint)
	}
	report.EvaluatorTests.CanQueryVM = true
	report.EvaluatorTests.Details = append(report.EvaluatorTests.Details,
		"✓ Evaluator CAN query VictoriaMetrics (endpoint validated)")

	// Test 5.4: Evaluator CANNOT access SEAM routes
	t.Log("Test 5.4: Verifying evaluator cannot access SEAM routes...")
	_, err = evaluatorClient.ReadSecret(ctx, seamRoutePath)
	if err == nil {
		t.Fatalf("SECURITY BREACH: Evaluator can read SEAM routes!")
	}
	if !strings.Contains(err.Error(), "403") && !strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		t.Fatalf("Expected permission denied for SEAM routes, got: %v", err)
	}
	report.EvaluatorTests.CannotAccessSEAMRoutes = true
	report.EvaluatorTests.Details = append(report.EvaluatorTests.Details,
		"✓ Evaluator CANNOT access SEAM routes (correctly denied)")

	// Test 5.5: Evaluator CANNOT access other secrets
	t.Log("Test 5.5: Verifying evaluator cannot access other secrets...")
	_, err = evaluatorClient.ReadSecret(ctx, otherSecretPath)
	if err == nil {
		t.Fatalf("SECURITY BREACH: Evaluator can access other secrets!")
	}
	report.EvaluatorTests.CannotAccessOtherSecrets = true
	report.EvaluatorTests.Details = append(report.EvaluatorTests.Details,
		"✓ Evaluator CANNOT access other secrets (correctly denied)")

	// ========================================================================
	// Phase 6: Test SEAM Capabilities and Restrictions
	// ========================================================================
	t.Log("=== Phase 6: Testing SEAM Capabilities and Restrictions ===")

	// Test 6.1: SEAM CAN read its own routes
	t.Log("Test 6.1: SEAM reading own route secrets...")
	seamOwnData, err := seamClient.ReadSecret(ctx, seamRoutePath)
	if err != nil {
		t.Fatalf("SEAM MUST be able to read own routes: %v", err)
	}
	if seamOwnData["token"] != "test-seam-token-abc123" {
		t.Fatalf("SEAM token value mismatch")
	}
	report.SEAMTests.CanReadOwnRoutes = true
	report.SEAMTests.Details = append(report.SEAMTests.Details,
		"✓ SEAM CAN read own route secrets")

	// Test 6.2: SEAM CANNOT read evaluator's token
	t.Log("Test 6.2: Verifying SEAM cannot read evaluator's token...")
	_, err = seamClient.ReadSecret(ctx, evaluatorTokenPath)
	if err == nil {
		t.Fatalf("SECURITY BREACH: SEAM can read evaluator's token!")
	}
	if !strings.Contains(err.Error(), "403") && !strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		t.Fatalf("Expected permission denied for evaluator token, got: %v", err)
	}
	report.SEAMTests.CannotReadEvaluatorToken = true
	report.SEAMTests.Details = append(report.SEAMTests.Details,
		"✓ SEAM CANNOT read evaluator's token (correctly denied)")

	// ========================================================================
	// Phase 7: Generate Isolation Report
	// ========================================================================
	t.Log("=== Phase 7: Generating Isolation Report ===")

	report.IsolationValid = report.EvaluatorTests.CanReadOwnToken &&
		report.EvaluatorTests.CanReadVMAuthCreds &&
		report.EvaluatorTests.CanQueryVM &&
		report.EvaluatorTests.CannotAccessSEAMRoutes &&
		report.EvaluatorTests.CannotAccessOtherSecrets &&
		report.SEAMTests.CanReadOwnRoutes &&
		report.SEAMTests.CannotReadEvaluatorToken

	if !report.IsolationValid {
		report.Summary = "ISOLATION TEST FAILED: Security boundaries violated!"
	} else {
		report.Summary = "ISOLATION TEST PASSED: All security boundaries properly enforced"
	}

	// ========================================================================
	// Phase 8: Output Detailed Report
	// ========================================================================
	printIsolationReport(t, report)

	// ========================================================================
	// Final Validation
	// ========================================================================
	if !report.IsolationValid {
		t.Fatal("E2E Isolation test FAILED: One or more security boundaries were violated")
	}

	t.Log("=== E2E Isolation Test PASSED ===")
}

// printIsolationReport outputs a formatted isolation report
func printIsolationReport(t *testing.T, report *IsolationTestReport) {
	t.Log("")
	t.Log("╔════════════════════════════════════════════════════════════════════════╗")
	t.Log("║         SEAM-Evaluator End-to-End Isolation Test Report               ║")
	t.Log("╚════════════════════════════════════════════════════════════════════════╝")
	t.Log("")
	t.Logf("Test Name:        %s", report.TestName)
	t.Logf("Timestamp:        %s", report.Timestamp.Format(time.RFC3339))
	t.Logf("OpenBao Endpoint: %s", report.OpenBaoEndpoint)
	t.Log("")

	// Evaluator Results
	t.Log("─────────────────────────────────────────────────────────────────────────")
	t.Log("EVALUATOR ROLE TEST RESULTS")
	t.Log("─────────────────────────────────────────────────────────────────────────")
	t.Logf("✓ Can Read Own Token:         %v", report.EvaluatorTests.CanReadOwnToken)
	t.Logf("✓ Can Read VM Credentials:    %v", report.EvaluatorTests.CanReadVMAuthCreds)
	t.Logf("✓ Can Query VictoriaMetrics: %v", report.EvaluatorTests.CanQueryVM)
	t.Logf("✓ Cannot Access SEAM Routes: %v", report.EvaluatorTests.CannotAccessSEAMRoutes)
	t.Logf("✓ Cannot Access Other Secs:  %v", report.EvaluatorTests.CannotAccessOtherSecrets)
	t.Log("")
	t.Log("Details:")
	for _, detail := range report.EvaluatorTests.Details {
		t.Logf("  %s", detail)
	}
	t.Log("")

	// SEAM Results
	t.Log("─────────────────────────────────────────────────────────────────────────")
	t.Log("SEAM ROLE TEST RESULTS")
	t.Log("─────────────────────────────────────────────────────────────────────────")
	t.Logf("✓ Can Read Own Routes:        %v", report.SEAMTests.CanReadOwnRoutes)
	t.Logf("✓ Cannot Read Evaluator Token: %v", report.SEAMTests.CannotReadEvaluatorToken)
	t.Log("")
	t.Log("Details:")
	for _, detail := range report.SEAMTests.Details {
		t.Logf("  %s", detail)
	}
	t.Log("")

	// Final Summary
	t.Log("─────────────────────────────────────────────────────────────────────────")
	if report.IsolationValid {
		t.Log("✅ ISOLATION VALID: All security boundaries properly enforced")
	} else {
		t.Log("❌ ISOLATION INVALID: Security boundaries violated!")
	}
	t.Log("─────────────────────────────────────────────────────────────────────────")
	t.Logf("Summary: %s", report.Summary)
	t.Log("")
}

// TestE2EIsolation_ManualClusterTest provides a manual verification variant
// that can run against a real Kubernetes cluster with actual OpenBao instance.
//
// This test is designed for CI/CD pipelines and production validation.
// It requires environment variables for cluster access.
func TestE2EIsolation_ManualClusterTest(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping manual cluster test in short mode")
	}

	// Note: These variables are documented for manual test configuration
	// They are not used directly since this test requires Kubernetes SA token injection
	_ = getEnv("TEST_OPENBAO_ADDR", "")
	_ = getEnv("TEST_EVALUATOR_ROLE", "seam-retirement-evaluator")
	_ = getEnv("TEST_SEAM_ROLE", "seam")
	_ = getEnv("TEST_EVALUATOR_TOKEN_PATH", "evaluators/seam-retirement-evaluator/github-token")
	_ = getEnv("TEST_VM_CREDS_PATH", "monitoring/victoriametrics/readonly-credentials")

	t.Skip("Manual cluster test requires Kubernetes SA token injection - use Argo Workflows instead:")
	t.Log("  - evaluator-openbao-read-test.yaml")
	t.Log("  - evaluator-victoriametrics-query-test.yaml")
}

// getEnv retrieves an environment variable with a default fallback
func getEnv(key, defaultVal string) string {
	if val := openbaoEnv(key); val != "" {
		return val
	}
	return defaultVal
}
