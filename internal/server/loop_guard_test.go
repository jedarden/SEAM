package server

import (
	"testing"
	"time"
)

// TestLoopGuardConfig_ParseWindow tests window duration parsing.
func TestLoopGuardConfig_ParseWindow(t *testing.T) {
	tests := []struct {
		name        string
		window      string
		wantSeconds int
		wantErr     bool
	}{
		{
			name:        "seconds",
			window:      "30s",
			wantSeconds: 30,
			wantErr:     false,
		},
		{
			name:        "minutes",
			window:      "10m",
			wantSeconds: 600,
			wantErr:     false,
		},
		{
			name:        "hours",
			window:      "2h",
			wantSeconds: 7200,
			wantErr:     false,
		},
		{
			name:        "days",
			window:      "1d",
			wantSeconds: 86400,
			wantErr:     false,
		},
		{
			name:        "empty window",
			window:      "",
			wantSeconds: 0,
			wantErr:     true,
		},
		{
			name:        "invalid format",
			window:      "10x",
			wantSeconds: 0,
			wantErr:     true,
		},
		{
			name:        "missing number",
			window:      "m",
			wantSeconds: 0,
			wantErr:     true,
		},
		{
			name:        "unsupported week",
			window:      "1w",
			wantSeconds: 0,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := LoopGuardConfig{Window: tt.window}
			err := config.Validate()

			if tt.wantErr && err == nil {
				t.Errorf("Expected error but got none")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if !tt.wantErr && config.windowDuration.Seconds() != float64(tt.wantSeconds) {
				t.Errorf("Window duration = %v seconds, want %d seconds", config.windowDuration.Seconds(), tt.wantSeconds)
			}
		})
	}
}

// TestLoopGuardConfig_DefaultValues tests default configuration.
func TestLoopGuardConfig_DefaultValues(t *testing.T) {
	config := DefaultLoopGuardConfig()

	if config.MaxRepeats != 5 {
		t.Errorf("Default MaxRepeats = %d, want 5", config.MaxRepeats)
	}
	if config.Window != "10m" {
		t.Errorf("Default Window = %s, want 10m", config.Window)
	}
	if config.windowDuration != 10*time.Minute {
		t.Errorf("Default windowDuration = %v, want 10m", config.windowDuration)
	}
}

// TestLoopGuard_AllowBeforeThreshold tests that requests are allowed
// before hitting the max repeat threshold.
func TestLoopGuard_AllowBeforeThreshold(t *testing.T) {
	config := LoopGuardConfig{
		MaxRepeats:     3,
		Window:         "1m",
		windowDuration: 1 * time.Minute,
	}
	guard, err := NewLoopGuard("test-route", config)
	if err != nil {
		t.Fatalf("Failed to create loop guard: %v", err)
	}

	hash := "abc123"

	// First request should be allowed
	allowed, _, _ := guard.CheckRequest(hash)
	if !allowed {
		t.Errorf("First request should be allowed")
	}

	// Record a failure
	guard.RecordFailure(hash)

	// Second request should still be allowed (below threshold)
	allowed, _, _ = guard.CheckRequest(hash)
	if !allowed {
		t.Errorf("Second request should be allowed")
	}

	// Record another failure
	guard.RecordFailure(hash)

	// Third request should still be allowed
	allowed, _, _ = guard.CheckRequest(hash)
	if !allowed {
		t.Errorf("Third request should be allowed")
	}
}

// TestLoopGuard_BlockAfterThreshold tests that requests are blocked
// after hitting the max repeat threshold.
func TestLoopGuard_BlockAfterThreshold(t *testing.T) {
	config := LoopGuardConfig{
		MaxRepeats:     3,
		Window:         "1m",
		windowDuration: 1 * time.Minute,
	}
	guard, err := NewLoopGuard("test-route", config)
	if err != nil {
		t.Fatalf("Failed to create loop guard: %v", err)
	}

	hash := "abc123"

	// Record 3 failures (at threshold)
	for i := 0; i < 3; i++ {
		guard.RecordFailure(hash)
	}

	// Request at threshold should still be allowed (only blocks on 4th)
	allowed, _, _ := guard.CheckRequest(hash)
	if !allowed {
		t.Errorf("Request at threshold should be allowed")
	}

	// Record one more failure (exceeds threshold)
	guard.RecordFailure(hash)

	// Next request should be blocked
	allowed, retryAfter, _ := guard.CheckRequest(hash)
	if allowed {
		t.Errorf("Request exceeding threshold should be blocked")
	}
	if retryAfter < 1 || retryAfter > 60 {
		t.Errorf("Retry-After should be between 1 and 60 seconds, got %d", retryAfter)
	}
}

// TestLoopGuard_SuccessClearsCounter tests that a 2xx response clears
// the failure counter for the same hash.
func TestLoopGuard_SuccessClearsCounter(t *testing.T) {
	config := LoopGuardConfig{
		MaxRepeats:     3,
		Window:         "1m",
		windowDuration: 1 * time.Minute,
	}
	guard, err := NewLoopGuard("test-route", config)
	if err != nil {
		t.Fatalf("Failed to create loop guard: %v", err)
	}

	hash := "abc123"

	// Record 2 failures
	guard.RecordFailure(hash)
	guard.RecordFailure(hash)

	// Record success (should clear the failure run)
	guard.RecordSuccess(hash)

	// Record another failure (should start a new run)
	guard.RecordFailure(hash)

	// Request should be allowed (counter was reset by success)
	allowed, _, _ := guard.CheckRequest(hash)
	if !allowed {
		t.Errorf("Request after success should be allowed")
	}

	// Need 4 more failures to hit threshold (not just 1 more)
	for i := 0; i < 3; i++ {
		guard.RecordFailure(hash)
	}

	// Still allowed at threshold
	allowed, _, _ = guard.CheckRequest(hash)
	if !allowed {
		t.Errorf("Request at threshold should be allowed")
	}

	// One more exceeds threshold
	guard.RecordFailure(hash)
	allowed, _, _ = guard.CheckRequest(hash)
	if allowed {
		t.Errorf("Request exceeding threshold should be blocked")
	}
}

// TestLoopGuard_DifferentHashesIndependent tests that different hashes
// are tracked independently.
func TestLoopGuard_DifferentHashesIndependent(t *testing.T) {
	config := LoopGuardConfig{
		MaxRepeats:     3,
		Window:         "1m",
		windowDuration: 1 * time.Minute,
	}
	guard, err := NewLoopGuard("test-route", config)
	if err != nil {
		t.Fatalf("Failed to create loop guard: %v", err)
	}

	hash1 := "abc123"
	hash2 := "def456"

	// Block hash1
	for i := 0; i < 4; i++ {
		guard.RecordFailure(hash1)
	}

	// hash1 should be blocked
	allowed, _, _ := guard.CheckRequest(hash1)
	if allowed {
		t.Errorf("hash1 should be blocked after threshold")
	}

	// hash2 should still be allowed (independent tracking)
	allowed, _, _ = guard.CheckRequest(hash2)
	if !allowed {
		t.Errorf("hash2 should be allowed (independent tracking)")
	}
}

// TestLoopGuard_WindowRolling tests that the tumbling window resets
// correctly and clears failure counters.
func TestLoopGuard_WindowRolling(t *testing.T) {
	config := LoopGuardConfig{
		MaxRepeats:     3,
		Window:         "100ms",
		windowDuration: 100 * time.Millisecond,
	}
	guard, err := NewLoopGuard("test-route", config)
	if err != nil {
		t.Fatalf("Failed to create loop guard: %v", err)
	}

	hash := "abc123"

	// Record failures
	for i := 0; i < 4; i++ {
		guard.RecordFailure(hash)
	}

	// Should be blocked
	allowed, _, _ := guard.CheckRequest(hash)
	if allowed {
		t.Errorf("Request should be blocked immediately after failures")
	}

	// Wait for window to roll
	time.Sleep(150 * time.Millisecond)

	// Check request (triggers window roll if needed)
	allowed, _, _ = guard.CheckRequest(hash)
	if !allowed {
		t.Errorf("Request should be allowed after window rolls")
	}

	// Record failures again
	for i := 0; i < 4; i++ {
		guard.RecordFailure(hash)
	}

	// Should be blocked again
	allowed, _, _ = guard.CheckRequest(hash)
	if allowed {
		t.Errorf("Request should be blocked after threshold in new window")
	}
}

// TestLoopGuard_RetryAfterCalculation tests that Retry-After is
// calculated correctly as seconds to window close.
func TestLoopGuard_RetryAfterCalculation(t *testing.T) {
	config := LoopGuardConfig{
		MaxRepeats:     3,
		Window:         "10s",
		windowDuration: 10 * time.Second,
	}
	guard, err := NewLoopGuard("test-route", config)
	if err != nil {
		t.Fatalf("Failed to create loop guard: %v", err)
	}

	hash := "abc123"

	// Block the hash
	for i := 0; i < 4; i++ {
		guard.RecordFailure(hash)
	}

	// Check Retry-After
	_, retryAfter, _ := guard.CheckRequest(hash)

	// Retry-After should be between 1 and 10 seconds
	if retryAfter < 1 || retryAfter > 10 {
		t.Errorf("Retry-After should be between 1 and 10 seconds, got %d", retryAfter)
	}

	// Wait a bit and check again
	time.Sleep(2 * time.Second)
	_, retryAfter2, _ := guard.CheckRequest(hash)

	// Retry-After should have decreased
	if retryAfter2 >= retryAfter {
		t.Errorf("Retry-After should decrease over time, got %d then %d", retryAfter, retryAfter2)
	}
}

// TestLoopGuard_Snapshot tests that the snapshot returns correct state.
func TestLoopGuard_Snapshot(t *testing.T) {
	config := LoopGuardConfig{
		MaxRepeats:     3,
		Window:         "1m",
		windowDuration: 1 * time.Minute,
	}
	guard, err := NewLoopGuard("test-route", config)
	if err != nil {
		t.Fatalf("Failed to create loop guard: %v", err)
	}

	hash := "abc123"

	// Record some failures
	guard.RecordFailure(hash)
	guard.RecordFailure(hash)

	// Get snapshot
	snapshot := guard.Snapshot()

	// Check snapshot fields
	if snapshot["route_id"] != "test-route" {
		t.Errorf("Snapshot route_id = %v, want test-route", snapshot["route_id"])
	}
	if snapshot["max_repeats"] != 3 {
		t.Errorf("Snapshot max_repeats = %v, want 3", snapshot["max_repeats"])
	}

	// Check hash tracks
	hashTracks := snapshot["hash_tracks"].(map[string]interface{})
	if len(hashTracks) != 1 {
		t.Errorf("Expected 1 hash track, got %d", len(hashTracks))
	}

	track := hashTracks[hash].(map[string]interface{})
	if track["failure_count"] != 2 {
		t.Errorf("Track failure_count = %v, want 2", track["failure_count"])
	}
}

// TestLoopGuard_ConfigValidation tests that configuration validation works.
func TestLoopGuard_ConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      LoopGuardConfig
		wantErr     bool
		errContains string
	}{
		{
			name: "valid config",
			config: LoopGuardConfig{
				MaxRepeats: 5,
				Window:     "10m",
			},
			wantErr: false,
		},
		{
			name: "maxRepeats below 1",
			config: LoopGuardConfig{
				MaxRepeats: 0,
				Window:     "10m",
			},
			wantErr:     true,
			errContains: "maxRepeats must be at least 1",
		},
		{
			name: "empty window",
			config: LoopGuardConfig{
				MaxRepeats: 5,
				Window:     "",
			},
			wantErr:     true,
			errContains: "window cannot be empty",
		},
		{
			name: "invalid window format",
			config: LoopGuardConfig{
				MaxRepeats: 5,
				Window:     "invalid",
			},
			wantErr:     true,
			errContains: "invalid window format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr && err == nil {
				t.Errorf("Expected error but got none")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if tt.wantErr && err != nil {
				if !containsString(err.Error(), tt.errContains) {
					t.Errorf("Error should contain %q, got %q", tt.errContains, err.Error())
				}
			}
		})
	}
}

// TestLoopGuard_RouteID tests that RouteID returns the correct ID.
func TestLoopGuard_RouteID(t *testing.T) {
	config := LoopGuardConfig{
		MaxRepeats:     3,
		Window:         "1m",
		windowDuration: 1 * time.Minute,
	}
	guard, err := NewLoopGuard("test-route-123", config)
	if err != nil {
		t.Fatalf("Failed to create loop guard: %v", err)
	}

	if guard.RouteID() != "test-route-123" {
		t.Errorf("RouteID = %s, want test-route-123", guard.RouteID())
	}
}

// TestLoopGuard_Config tests that Config returns the correct configuration.
func TestLoopGuard_Config(t *testing.T) {
	config := LoopGuardConfig{
		MaxRepeats:     7,
		Window:         "5m",
		windowDuration: 5 * time.Minute,
	}
	guard, err := NewLoopGuard("test-route", config)
	if err != nil {
		t.Fatalf("Failed to create loop guard: %v", err)
	}

	returnedConfig := guard.Config()
	if returnedConfig.MaxRepeats != 7 {
		t.Errorf("Config MaxRepeats = %d, want 7", returnedConfig.MaxRepeats)
	}
	if returnedConfig.Window != "5m" {
		t.Errorf("Config Window = %s, want 5m", returnedConfig.Window)
	}
}

// Helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
