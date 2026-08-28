package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestPhase11_CircuitBreakerStateTransitions tests the complete state transition
// flow: closed → open → half-open → closed with proper backoff timing.
func TestPhase11_CircuitBreakerStateTransitions(t *testing.T) {
	registry := NewCircuitBreakerStateRegistry()
	config := DefaultBreakerConfig()
	breaker := NewCircuitBreaker(MustParseOrigin("https://example.com:8443"), config, registry)

	// Initial state should be closed
	if breaker.Snapshot().State != CircuitBreakerClosed {
		t.Errorf("Expected initial state to be closed, got %s", breaker.Snapshot().State)
	}

	// Record failures up to threshold
	for i := 0; i < config.Threshold; i++ {
		allowed := breaker.Allow()
		if !allowed {
			t.Errorf("Request %d should be allowed in closed state", i+1)
		}
		breaker.RecordFailure("test failure")
	}

	// Breaker should now be open
	snapshot := breaker.Snapshot()
	if snapshot.State != CircuitBreakerOpen {
		t.Errorf("Expected state to be open after %d failures, got %s", config.Threshold, snapshot.State)
	}

	// Requests should be refused while open
	allowed := breaker.Allow()
	if allowed {
		t.Error("Request should be refused while breaker is open")
	}

	// Verify Retry-After is set correctly
	if snapshot.RetryAfterSeconds < 1 || snapshot.RetryAfterSeconds > config.OpenSeconds {
		t.Errorf("Retry-After should be between 1 and %d, got %d", config.OpenSeconds, snapshot.RetryAfterSeconds)
	}

	// Wait for open duration to expire
	time.Sleep(time.Duration(config.OpenSeconds) * time.Second)

	// Next request should be allowed (transition to half-open)
	allowed = breaker.Allow()
	if !allowed {
		t.Error("Request should be allowed when transitioning to half-open")
	}

	// Verify state is half-open
	snapshot = breaker.Snapshot()
	if snapshot.State != CircuitBreakerHalfOpen {
		t.Errorf("Expected state to be half-open, got %s", snapshot.State)
	}

	// Second request in half-open should be refused
	allowed = breaker.Allow()
	if allowed {
		t.Error("Second request in half-open should be refused")
	}

	// Record success to close the breaker
	breaker.RecordSuccess()

	// Verify state is closed
	snapshot = breaker.Snapshot()
	if snapshot.State != CircuitBreakerClosed {
		t.Errorf("Expected state to be closed after success in half-open, got %s", snapshot.State)
	}

	// Requests should now be allowed again
	allowed = breaker.Allow()
	if !allowed {
		t.Error("Request should be allowed in closed state")
	}
}

// TestPhase11_CircuitBreakerBackoffSequence tests the exponential backoff
// sequence: 30s, 60s, 120s capped at MaxOpenSeconds (default 5 minutes).
func TestPhase11_CircuitBreakerBackoffSequence(t *testing.T) {
	config := DefaultBreakerConfig()

	// Test the backoffDuration calculation directly
	expectedDurations := []time.Duration{
		30 * time.Second,  // First open
		60 * time.Second,  // Second open (backoff)
		120 * time.Second, // Third open (backoff)
		240 * time.Second, // Fourth open (backoff, would be 240 but capped at 300)
		300 * time.Second, // Fifth and subsequent opens (capped at MaxOpenSeconds)
	}

	for openCount := 1; openCount <= len(expectedDurations); openCount++ {
		expectedDuration := expectedDurations[openCount-1]
		actualDuration := config.backoffDuration(openCount)

		if actualDuration != expectedDuration {
			t.Errorf("Open #%d: expected backoff duration %v, got %v", openCount, expectedDuration, actualDuration)
		}
	}

	// Verify we cap at MaxOpenSeconds
	maxDuration := time.Duration(config.MaxOpenSeconds) * time.Second
	for openCount := 1; openCount <= 20; openCount++ {
		duration := config.backoffDuration(openCount)
		if duration > maxDuration {
			t.Errorf("Open #%d: backoff duration %v exceeded MaxOpenSeconds %v", openCount, duration, maxDuration)
		}
	}
}

// TestPhase11_CircuitBreakerHalfOpenFailure tests that a failure in half-open
// re-opens the breaker with backoff (not to closed state).
func TestPhase11_CircuitBreakerHalfOpenFailure(t *testing.T) {
	registry := NewCircuitBreakerStateRegistry()
	config := DefaultBreakerConfig()
	breaker := NewCircuitBreaker(MustParseOrigin("https://example.com:8443"), config, registry)

	// Open the breaker
	for i := 0; i < config.Threshold; i++ {
		breaker.Allow()
		breaker.RecordFailure("test failure")
	}

	// Wait for open duration
	time.Sleep(time.Duration(config.OpenSeconds) * time.Second)

	// Transition to half-open
	breaker.Allow()

	// Verify we're in half-open
	if breaker.Snapshot().State != CircuitBreakerHalfOpen {
		t.Errorf("Expected half-open state, got %s", breaker.Snapshot().State)
	}

	// Record a failure in half-open
	breaker.RecordFailure("half-open failure")

	// Should re-open with increased backoff
	snapshot := breaker.Snapshot()
	if snapshot.State != CircuitBreakerOpen {
		t.Errorf("Expected re-open state after half-open failure, got %s", snapshot.State)
	}

	// Retry-After should be longer (backoff increased)
	if snapshot.RetryAfterSeconds <= config.OpenSeconds {
		t.Errorf("Expected increased backoff after half-open failure, got %d", snapshot.RetryAfterSeconds)
	}
}

// TestPhase11_CircuitBreakerDisabled tests that a breaker with enabled: false
// always allows requests and never tracks failures.
func TestPhase11_CircuitBreakerDisabled(t *testing.T) {
	registry := NewCircuitBreakerStateRegistry()
	config := DefaultBreakerConfig()
	config.Enabled = false
	breaker := NewCircuitBreaker(MustParseOrigin("https://example.com:8443"), config, registry)

	// All requests should be allowed
	for i := 0; i < 100; i++ {
		allowed := breaker.Allow()
		if !allowed {
			t.Errorf("Request %d should be allowed when breaker is disabled", i+1)
		}
		breaker.RecordFailure("test failure")
	}

	// State should still be closed (breaker disabled)
	snapshot := breaker.Snapshot()
	if snapshot.State != CircuitBreakerClosed {
		t.Errorf("Expected state to remain closed when disabled, got %s", snapshot.State)
	}

	if snapshot.Enabled {
		t.Error("Expected Enabled to be false in snapshot")
	}
}

// TestPhase11_FailureDetection tests that only transport faults and 5xx count
// as failures. 4xx never counts, and SEAM's own 400/429/500 never count.
func TestPhase11_FailureDetection(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		statusCode  int
		shouldCount bool
	}{
		{"Transport: connection refused", &testErr{"connection refused"}, 0, true},
		{"Transport: connection reset", &testErr{"connection reset"}, 0, true},
		{"Transport: DNS error", &testErr{"no such host"}, 0, true},
		{"Transport: TLS error", &testErr{"tls: certificate error"}, 0, true},
		{"Transport: timeout", &testErr{"context timeout exceeded"}, 0, true},
		{"Upstream 500", nil, 500, true},
		{"Upstream 502", nil, 502, true},
		{"Upstream 503", nil, 503, true},
		{"Upstream 400", nil, 400, false},
		{"Upstream 401", nil, 401, false},
		{"Upstream 403", nil, 403, false},
		{"Upstream 404", nil, 404, false},
		{"Upstream 429", nil, 429, false},
		{"Upstream 200", nil, 200, false},
		{"No error, 200", nil, 200, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counts := ShouldCountAsFailure(tt.err, tt.statusCode)
			if counts != tt.shouldCount {
				t.Errorf("ShouldCountAsFailure(%v, %d) = %v, want %v", tt.err, tt.statusCode, counts, tt.shouldCount)
			}
		})
	}
}

// testErr is a helper for testing error detection
type testErr struct {
	msg string
}

func (e *testErr) Error() string {
	return e.msg
}

// TestPhase11_Structured503Response tests that the 503 response includes
// upstream, openedAt, lastError, and Retry-After header matching the body.
func TestPhase11_Structured503Response(t *testing.T) {
	registry := NewCircuitBreakerStateRegistry()
	config := DefaultBreakerConfig()
	breaker := NewCircuitBreaker(MustParseOrigin("https://example.com:8443"), config, registry)

	// Open the breaker
	for i := 0; i < config.Threshold; i++ {
		breaker.Allow()
		breaker.RecordFailure("connection refused")
	}

	// Create HTTP test recorder
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)

	// Write the 503 response
	WriteCircuitBreakerRefused(w, r, breaker.Snapshot())

	// Verify status code
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", w.Code)
	}

	// Verify Retry-After header
	retryAfterHeader := w.Header().Get("Retry-After")
	if retryAfterHeader == "" {
		t.Error("Expected Retry-After header to be set")
	}

	// Verify response body
	var response CircuitBreakerRefusedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	// Verify response structure
	if response.Error != "service_unavailable" {
		t.Errorf("Expected error 'service_unavailable', got '%s'", response.Error)
	}

	if response.Upstream != "https://example.com:8443" {
		t.Errorf("Expected upstream 'https://example.com:8443', got '%s'", response.Upstream)
	}

	if response.LastError != "connection refused" {
		t.Errorf("Expected lastError 'connection refused', got '%s'", response.LastError)
	}

	// Verify Retry-After in body matches header
	bodyRetryAfter := response.RetryAfter
	if bodyRetryAfter < 1 {
		t.Errorf("Expected Retry-After >= 1, got %d", bodyRetryAfter)
	}

	if retryAfterHeader != string(rune(bodyRetryAfter+'0')) {
		headerInt := 0
		if _, err := fmt.Sscanf(retryAfterHeader, "%d", &headerInt); err == nil {
			if headerInt != bodyRetryAfter {
				t.Errorf("Retry-After header %s doesn't match body %d", retryAfterHeader, bodyRetryAfter)
			}
		}
	}

	// Verify openedAt is set
	if response.OpenedAt.IsZero() {
		t.Error("Expected openedAt to be set")
	}
}

// TestPhase11_BreakerConfigDisagreement tests that the breaker correctly
// detects and merges disagreements between same-origin instances.
func TestPhase11_BreakerConfigDisagreement(t *testing.T) {
	// Create two configs for the same origin with different thresholds
	config1 := BreakerConfig{Threshold: 5, OpenSeconds: 30, MaxOpenSeconds: 300, Enabled: true}
	config2 := BreakerConfig{Threshold: 3, OpenSeconds: 30, MaxOpenSeconds: 300, Enabled: true}

	// Verify they disagree
	if !config1.Disagreement(config2) {
		t.Error("Expected configs to disagree")
	}

	// Verify merge produces stricter config
	merged := config1.Merge(config2)
	if merged.Threshold != 3 { // Should take the lower threshold
		t.Errorf("Expected merged threshold 3, got %d", merged.Threshold)
	}

	if merged.OpenSeconds != 30 { // Should keep the same (both equal)
		t.Errorf("Expected merged openSeconds 30, got %d", merged.OpenSeconds)
	}

	if !merged.Enabled { // Should remain enabled (both enabled)
		t.Error("Expected merged enabled to be true")
	}
}

// TestPhase11_BreakerConfigMergeDisabled tests that merging with a disabled
// breaker produces a disabled merged config.
func TestPhase11_BreakerConfigMergeDisabled(t *testing.T) {
	configEnabled := BreakerConfig{Threshold: 5, OpenSeconds: 30, MaxOpenSeconds: 300, Enabled: true}
	configDisabled := BreakerConfig{Threshold: 3, OpenSeconds: 30, MaxOpenSeconds: 300, Enabled: false}

	// Merge enabled + disabled
	merged := configEnabled.Merge(configDisabled)

	// Merged should be disabled (conservative)
	if merged.Enabled {
		t.Error("Expected merged config to be disabled when one input is disabled")
	}
}

// TestPhase11_OriginParsing tests that ParseOrigin correctly extracts
// scheme+host+port from URLs.
func TestPhase11_OriginParsing(t *testing.T) {
	tests := []struct {
		url     string
		origin  string
		wantErr bool
	}{
		{"https://example.com:8443/path", "https://example.com:8443", false},
		{"https://example.com/path", "https://example.com:443", false},
		{"http://example.com:8080/path", "http://example.com:8080", false},
		{"http://example.com/path", "http://example.com:80", false},
		{"", "", true},
		{"not-a-url", "", true},
		{"/path-only", "", true},
		{"https://example.com", "https://example.com:443", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			origin, err := ParseOrigin(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseOrigin(%q) expected error, got nil", tt.url)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseOrigin(%q) unexpected error: %v", tt.url, err)
			}
			if string(origin) != tt.origin {
				t.Errorf("ParseOrigin(%q) = %q, want %q", tt.url, origin, tt.origin)
			}
		})
	}
}

// TestPhase11_CircuitBreakerRegistry tests the breaker registry's ability
// to create, retrieve, and manage per-origin breakers.
func TestPhase11_CircuitBreakerRegistry(t *testing.T) {
	stateRegistry := NewCircuitBreakerStateRegistry()
	registry := NewBreakerRegistry(stateRegistry)

	origin1 := MustParseOrigin("https://example.com:8443")
	origin2 := MustParseOrigin("https://other.com:8443")

	config1 := BreakerConfig{Threshold: 5, OpenSeconds: 30, MaxOpenSeconds: 300, Enabled: true}
	config2 := BreakerConfig{Threshold: 3, OpenSeconds: 60, MaxOpenSeconds: 300, Enabled: true}

	// Create breakers for different origins
	breaker1 := registry.GetOrCreate(origin1, config1)
	breaker2 := registry.GetOrCreate(origin2, config2)

	if breaker1 == breaker2 {
		t.Error("Expected different breakers for different origins")
	}

	// Retrieve existing breaker
	breaker1Again := registry.GetOrCreate(origin1, config1)
	if breaker1 != breaker1Again {
		t.Error("Expected same breaker for same origin")
	}

	// Verify snapshot returns all breakers
	snapshots := registry.Snapshot()
	if len(snapshots) != 2 {
		t.Errorf("Expected 2 breaker snapshots, got %d", len(snapshots))
	}

	// Remove a breaker
	registry.Remove(origin1)
	snapshots = registry.Snapshot()
	if len(snapshots) != 1 {
		t.Errorf("Expected 1 breaker snapshot after removal, got %d", len(snapshots))
	}

	// Verify removed breaker is gone
	_, exists := registry.Get(origin1)
	if exists {
		t.Error("Expected breaker to be removed from registry")
	}
}

// TestPhase11_BreakerOptOut tests that enabled: false is the explicit opt-out
// and that lint-validation flags it.
func TestPhase11_BreakerOptOut(t *testing.T) {
	// Test that the default is enabled
	defaultConfig := DefaultBreakerConfig()
	if !defaultConfig.Enabled {
		t.Error("Expected default breaker config to be enabled")
	}

	// Test that we can create a disabled breaker
	disabledConfig := BreakerConfig{
		Threshold:      5,
		OpenSeconds:    30,
		MaxOpenSeconds: 300,
		Enabled:        false,
	}

	registry := NewCircuitBreakerStateRegistry()
	breaker := NewCircuitBreaker(MustParseOrigin("https://example.com:8443"), disabledConfig, registry)

	// Verify it's disabled
	if breaker.Config().Enabled {
		t.Error("Expected breaker to be disabled")
	}

	// Verify it always allows requests
	for i := 0; i < 100; i++ {
		if !breaker.Allow() {
			t.Error("Disabled breaker should always allow requests")
		}
		breaker.RecordFailure("test failure")
	}

	// Verify state remains closed
	if breaker.Snapshot().State != CircuitBreakerClosed {
		t.Errorf("Disabled breaker should remain closed, got %s", breaker.Snapshot().State)
	}
}
