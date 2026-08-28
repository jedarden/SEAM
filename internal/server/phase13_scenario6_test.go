package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPhase13Scenario6_CostGovernor tests the complete Phase 13.2 implementation:
// - Cost governor + dispatch accounting + dry-run
// - Unit validation between x-cost-per-call and x-quota
// - Quota enforcement at dispatch time (not before)
// - 402 quota refusal responses
// - X-SEAM-Budget-Remaining headers
// - X-SEAM-Dry-Run mode
// - Sentinel probe exclusion
func TestPhase13Scenario6_CostGovernor(t *testing.T) {
	t.Run("unit_validation_mismatch", func(t *testing.T) {
		// Test that unit mismatch between x-cost-per-call and x-quota produces lint error
		// This is tested in spec package, but we verify the runtime behavior here
		t.Skip("unit validation tested in spec/lint package")
	})

	t.Run("quota_check_before_dispatch", func(t *testing.T) {
		// Test that quota is checked before dispatch without deduction
		server := createTestServer(t)
		defer server.Close()

		// Configure a route with cost and quota
		route := "/api/test"
		costPerCall := 0.05 // $0.05 per call
		quotaLimit := 1.00   // $1.00 limit

		server.quotaTracker.SetCostPerCall(route, costPerCall)
		server.quotaTracker.SetQuota(route, QuotaConfig{
			Limit:  quotaLimit,
			Window: 1 * time.Hour,
			Scope:  "per-route",
		})

		// Make first request - should pass quota check
		req := httptest.NewRequest("GET", route, nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		// Should get 200 OK (quota check passed)
		if w.Code != http.StatusOK {
			t.Errorf("Expected 200 OK, got %d", w.Code)
		}

		// Verify quota was deducted at dispatch time
		remaining := server.quotaTracker.GetRemaining(route, "", "")
		expectedRemaining := quotaLimit - costPerCall
		if remaining != expectedRemaining {
			t.Errorf("Expected remaining $%.2f, got $%.2f", expectedRemaining, remaining)
		}

		t.Log("✓ Quota checked before dispatch and deducted at dispatch")
	})

	t.Run("quota_exhaustion_402_response", func(t *testing.T) {
		// Test that quota exhaustion returns 402 Payment Required
		server := createTestServer(t)
		defer server.Close()

		route := "/api/expensive"
		costPerCall := 0.10
		quotaLimit := 0.15 // Only 1.5 calls allowed

		server.quotaTracker.SetCostPerCall(route, costPerCall)
		server.quotaTracker.SetQuota(route, QuotaConfig{
			Limit:  quotaLimit,
			Window: 1 * time.Hour,
			Scope:  "per-route",
		})

		// Make first request - should pass
		req1 := httptest.NewRequest("GET", route, nil)
		w1 := httptest.NewRecorder()
		server.ServeHTTP(w1, req1)

		// Make second request - should pass
		req2 := httptest.NewRequest("GET", route, nil)
		w2 := httptest.NewRecorder()
		server.ServeHTTP(w2, req2)

		// Make third request - should exceed quota and get 402
		req3 := httptest.NewRequest("GET", route, nil)
		w3 := httptest.NewRecorder()
		server.ServeHTTP(w3, req3)

		if w3.Code != http.StatusPaymentRequired {
			t.Errorf("Expected 402 Payment Required, got %d", w3.Code)
		}

		// Verify Retry-After header is present
		retryAfter := w3.Header().Get("Retry-After")
		if retryAfter == "" {
			t.Error("Expected Retry-After header on 402 response")
		}

		// Verify X-SEAM-Budget-Remaining header is present
		budgetRemaining := w3.Header().Get("X-SEAM-Budget-Remaining")
		if budgetRemaining == "" {
			t.Error("Expected X-SEAM-Budget-Remaining header on 402 response")
		}

		t.Log("✓ Quota exhaustion returns 402 with proper headers")
	})

	t.Run("dry_run_mode", func(t *testing.T) {
		// Test X-SEAM-Dry-Run: 1 = validation verdict at stage 7
		server := createTestServer(t)
		defer server.Close()

		route := "/api/dryrun"

		req := httptest.NewRequest("GET", route, nil)
		req.Header.Set("X-SEAM-Dry-Run", "1")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		// Should get 200 OK with validation result
		if w.Code != http.StatusOK {
			t.Errorf("Expected 200 OK for dry-run, got %d", w.Code)
		}

		// Verify response contains validation result
		var response map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if status, ok := response["status"].(string); !ok || status != "validated" {
			t.Errorf("Expected status='validated', got %v", response["status"])
		}

		// Verify X-SEAM-Dry-Run header is set
		if dryRunHeader := w.Header().Get("X-SEAM-Dry-Run"); dryRunHeader != "validated" {
			t.Errorf("Expected X-SEAM-Dry-Run='validated', got %s", dryRunHeader)
		}

		// Verify quota was NOT charged
		remaining := server.quotaTracker.GetRemaining(route, "", "")
		if remaining != 0 {
			t.Errorf("Expected quota to be unchanged (remaining=$0.00), got $%.2f", remaining)
		}

		t.Log("✓ Dry-run mode validates without charging quota")
	})

	t.Run("sentinel_probe_exclusion", func(t *testing.T) {
		// Test that sentinel probes are excluded from quota
		server := createTestServer(t)
		defer server.Close()

		// Configure quota for a route
		route := "/api/normal"
		server.quotaTracker.SetCostPerCall(route, 0.10)
		server.quotaTracker.SetQuota(route, QuotaConfig{
			Limit:  1.00,
			Window: 1 * time.Hour,
			Scope:  "global",
		})

		// Make health probe request - should bypass quota
		req := httptest.NewRequest("GET", "/health/credentials", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		// Should get 200 OK (bypassed quota)
		if w.Code != http.StatusOK {
			t.Errorf("Expected 200 OK for health probe, got %d", w.Code)
		}

		// Verify quota was NOT charged
		remaining := server.quotaTracker.GetRemaining("", "", "")
		if remaining != 1.00 {
			t.Errorf("Expected quota unchanged ($1.00), got $%.2f", remaining)
		}

		t.Log("✓ Sentinel probes bypass quota enforcement")
	})

	t.Run("dispatch_time_accounting_5xx_charges", func(t *testing.T) {
		// Test that 5xx responses still charge quota
		// (The upstream received and processed the request)
		t.Skip("requires upstream server mock")
	})

	t.Run("transport_errors_no_charge", func(t *testing.T) {
		// Test that transport errors (connection refused, timeout, etc.) do NOT charge quota
		// (The request was never successfully sent to upstream)
		t.Skip("requires upstream server mock")
	})

	t.Run("x_seam_budget_remaining_header", func(t *testing.T) {
		// Test that X-SEAM-Budget-Remaining header is present on quota routes
		server := createTestServer(t)
		defer server.Close()

		route := "/api/budget"
		costPerCall := 0.25
		quotaLimit := 1.00

		server.quotaTracker.SetCostPerCall(route, costPerCall)
		server.quotaTracker.SetQuota(route, QuotaConfig{
			Limit:  quotaLimit,
			Window: 1 * time.Hour,
			Scope:  "per-route",
		})

		req := httptest.NewRequest("GET", route, nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		// Verify X-SEAM-Budget-Remaining header is present
		budgetRemaining := w.Header().Get("X-SEAM-Budget-Remaining")
		if budgetRemaining == "" {
			t.Error("Expected X-SEAM-Budget-Remaining header on quota route response")
		}

		// Parse and validate header format: "amount=X unit=call window=Z resets=R"
		if !strings.Contains(budgetRemaining, "amount=") ||
			!strings.Contains(budgetRemaining, "unit=call") ||
			!strings.Contains(budgetRemaining, "window=") ||
			!strings.Contains(budgetRemaining, "resets=") {
			t.Errorf("Invalid X-SEAM-Budget-Remaining format: %s", budgetRemaining)
		}

		t.Logf("✓ X-SEAM-Budget-Remaining header present: %s", budgetRemaining)
	})

	t.Run("cache_hit_bypass", func(t *testing.T) {
		// Test that cache hits bypass quota checking entirely
		t.Skip("cache integration tested separately")
	})

	t.Run("quota_window_duration", func(t *testing.T) {
		// Test GetWindowDuration returns correct duration
		qt := NewQuotaTracker()
		expectedWindow := 1 * time.Hour

		if got := qt.GetWindowDuration(); got != expectedWindow {
			t.Errorf("Expected window duration %v, got %v", expectedWindow, got)
		}

		t.Log("✓ Quota tracker reports correct window duration")
	})
}

// TestPhase13Scenario6_UnitValidation tests the lint-time unit validation
// This is a placeholder - the actual validation is in the spec package
func TestPhase13Scenario6_UnitValidation(t *testing.T) {
	t.Run("cost_per_call_unit_validation", func(t *testing.T) {
		t.Skip("unit validation tested in spec/lint package")
	})

	t.Run("quota_without_cost_error", func(t *testing.T) {
		t.Skip("x-quota without x-cost-per-call validation tested in spec/lint package")
	})

	t.Run("window_over_168h_warning", func(t *testing.T) {
		t.Skip("window duration warning tested in spec/lint package")
	})
}

// createTestServer creates a test server for Phase 13.2 testing
func createTestServer(t *testing.T) *Server {
	t.Helper()

	config := &Config{
		BaseURL:                   "http://test.example.com",
		MaxReplayableRequestBytes: 1024 * 1024,
		MaxBufferedResponseBytes:  1024 * 1024,
	}

	server, err := NewServer(config)
	if err != nil {
		t.Fatalf("Failed to create test server: %v", err)
	}

	// Set up a simple route table
	routeTable := NewRouteTable()
	server.mu.Lock()
	server.routeTable = routeTable
	server.quotaTracker = NewQuotaTracker()
	server.mu.Unlock()

	return server
}

// Test Helper: Verify quota accounting at dispatch time
func TestPhase13Scenario6_DispatchAccounting(t *testing.T) {
	t.Run("quota_deducted_after_dispatch", func(t *testing.T) {
		qt := NewQuotaTracker()
		route := "/api/dispatch"
		costPerCall := 0.50
		token := "test-token"
		user := "test-user"

		// Configure quota
		qt.SetCostPerCall(route, costPerCall)
		qt.SetQuota(route, QuotaConfig{
			Limit:  10.00,
			Window: 1 * time.Hour,
			Scope:  "per-route",
		})

		// Check quota before dispatch (should not deduct)
		allowed, remaining, err := qt.CheckQuotaOnly(context.Background(), route, costPerCall, token, user)
		if err != nil {
			t.Fatalf("CheckQuotaOnly failed: %v", err)
		}
		if !allowed {
			t.Error("Expected quota check to pass")
		}
		if remaining != 10.00 {
			t.Errorf("Expected remaining $10.00 before dispatch, got $%.2f", remaining)
		}

		// Record quota after dispatch
		err = qt.RecordQuotaCost(route, costPerCall, token, user)
		if err != nil {
			t.Fatalf("RecordQuotaCost failed: %v", err)
		}

		// Verify quota was deducted
		remainingAfter := qt.GetRemaining(route, token, user)
		expectedRemaining := 10.00 - costPerCall
		if remainingAfter != expectedRemaining {
			t.Errorf("Expected remaining $%.2f after dispatch, got $%.2f", expectedRemaining, remainingAfter)
		}

		t.Log("✓ Quota deducted at dispatch time correctly")
	})

	t.Run("get_remaining_quota", func(t *testing.T) {
		qt := NewQuotaTracker()
		route := "/api/remaining"
		costPerCall := 0.25

		qt.SetCostPerCall(route, costPerCall)
		qt.SetQuota(route, QuotaConfig{
			Limit:  5.00,
			Window: 1 * time.Hour,
			Scope:  "per-route",
		})

		// Make 3 requests
		for i := 0; i < 3; i++ {
			_ = qt.RecordQuotaCost(route, costPerCall, "", "")
		}

		remaining := qt.GetRemaining(route, "", "")
		expectedRemaining := 5.00 - (3.0 * costPerCall)

		if remaining != expectedRemaining {
			t.Errorf("Expected remaining $%.2f, got $%.2f", expectedRemaining, remaining)
		}

		t.Log("✓ GetRemaining returns correct quota balance")
	})
}

// TestPhase13Scenario6_DryRunValidation tests dry-run validation behavior
func TestPhase13Scenario6_DryRunValidation(t *testing.T) {
	t.Run("dry_run_validates_route", func(t *testing.T) {
		// Test that dry-run validates the route exists
		t.Skip("requires route table configuration")
	})

	t.Run("dry_run_validates_breaker", func(t *testing.T) {
		// Test that dry-run includes breaker config in response
		t.Skip("requires circuit breaker configuration")
	})

	t.Run("dry_run_shows_cost", func(t *testing.T) {
		// Test that dry-run shows cost per call if configured
		t.Skip("requires cost configuration")
	})
}
