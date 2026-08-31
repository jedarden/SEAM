package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNewTransientStarvationBackoff validates daemon initialization.
func TestNewTransientStarvationBackoff(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "backoff-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := BackoffConfig{
		WorkspaceRoot: tempDir,
	}

	daemon, err := NewTransientStarvationBackoff(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	if daemon == nil {
		t.Fatal("Daemon is nil")
	}

	if daemon.workspaceRoot != tempDir {
		t.Errorf("Expected workspace root %s, got %s", tempDir, daemon.workspaceRoot)
	}

	if daemon.checkInterval != 30*time.Second {
		t.Errorf("Expected default check interval 30s, got %v", daemon.checkInterval)
	}

	if len(daemon.backoffIntervals) != 4 {
		t.Errorf("Expected 4 backoff intervals, got %d", len(daemon.backoffIntervals))
	}

	// Verify backoff intervals are 30s, 2m, 5m, 15m
	expectedIntervals := []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute}
	for i, interval := range daemon.backoffIntervals {
		if interval != expectedIntervals[i] {
			t.Errorf("Interval %d: expected %v, got %v", i, expectedIntervals[i], interval)
		}
	}
}

// TestNewTransientStarvationBackoffWithDefaults validates custom configuration.
func TestNewTransientStarvationBackoffWithDefaults(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "backoff-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	customIntervals := []time.Duration{
		10 * time.Second,
		30 * time.Second,
		1 * time.Minute,
		2 * time.Minute,
	}

	cfg := BackoffConfig{
		WorkspaceRoot:    tempDir,
		CheckInterval:    1 * time.Minute,
		BackoffIntervals: customIntervals,
	}

	daemon, err := NewTransientStarvationBackoff(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	if daemon.checkInterval != 1*time.Minute {
		t.Errorf("Expected custom check interval 1m, got %v", daemon.checkInterval)
	}

	if len(daemon.backoffIntervals) != 4 {
		t.Errorf("Expected 4 custom backoff intervals, got %d", len(daemon.backoffIntervals))
	}

	for i, interval := range daemon.backoffIntervals {
		if interval != customIntervals[i] {
			t.Errorf("Custom interval %d: expected %v, got %v", i, customIntervals[i], interval)
		}
	}
}

// TestNewTransientStarvationBackoffRequiresWorkspaceRoot validates error handling.
func TestNewTransientStarvationBackoffRequiresWorkspaceRoot(t *testing.T) {
	cfg := BackoffConfig{
		WorkspaceRoot: "",
	}

	_, err := NewTransientStarvationBackoff(cfg)
	if err == nil {
		t.Fatal("Expected error when workspace root is empty, got nil")
	}

	expectedError := "workspace root is required"
	if err.Error() != expectedError {
		t.Errorf("Expected error '%s', got '%s'", expectedError, err.Error())
	}
}

// TestBackoffStateTracking validates state tracking during backoff sequence.
func TestBackoffStateTracking(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "backoff-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a mock workspace with .beads/beads.db
	workspacePath := filepath.Join(tempDir, "test-workspace")
	if err := os.MkdirAll(filepath.Join(workspacePath, ".beads"), 0755); err != nil {
		t.Fatalf("Failed to create workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, ".beads", "beads.db"), []byte("mock"), 0644); err != nil {
		t.Fatalf("Failed to create beads.db: %v", err)
	}

	cfg := BackoffConfig{
		WorkspaceRoot: tempDir,
	}

	daemon, err := NewTransientStarvationBackoff(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	// Test starting backoff tracking
	now := time.Now()
	daemon.startBackoffTracking(workspacePath, now, 10, 0)

	state := daemon.pendingEvents[workspacePath]
	if state == nil {
		t.Fatal("State not tracked after startBackoffTracking")
	}

	if state.Workspace != workspacePath {
		t.Errorf("Expected workspace %s, got %s", workspacePath, state.Workspace)
	}

	if state.FirstDetected != now {
		t.Errorf("Expected FirstDetected %v, got %v", now, state.FirstDetected)
	}

	if state.CurrentRetryIndex != 0 {
		t.Errorf("Expected CurrentRetryIndex 0, got %d", state.CurrentRetryIndex)
	}

	if state.OpenBeadsCount != 10 {
		t.Errorf("Expected OpenBeadsCount 10, got %d", state.OpenBeadsCount)
	}

	if state.ReadyBeadsCount != 0 {
		t.Errorf("Expected ReadyBeadsCount 0, got %d", state.ReadyBeadsCount)
	}

	if len(state.CheckHistory) != 1 {
		t.Errorf("Expected 1 check history entry, got %d", len(state.CheckHistory))
	}

	if !state.InfrastructureOK {
		t.Error("Expected InfrastructureOK to be true")
	}
}

// TestIsTimeForNextRetry validates backoff interval timing.
func TestIsTimeForNextRetry(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "backoff-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := BackoffConfig{
		WorkspaceRoot: tempDir,
	}

	daemon, err := NewTransientStarvationBackoff(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	intervals := daemon.backoffIntervals

	tests := []struct {
		name               string
		firstDetected      time.Time
		currentTime       time.Time
		retryIndex        int
		expectedReady     bool
	}{
		{
			name:           "before first interval",
			firstDetected:  time.Now(),
			currentTime:    time.Now().Add(10 * time.Second),
			retryIndex:     0,
			expectedReady:  false, // 10s < 30s
		},
		{
			name:           "at first interval",
			firstDetected:  time.Now(),
			currentTime:    time.Now().Add(30 * time.Second),
			retryIndex:     0,
			expectedReady:  true, // 30s >= 30s
		},
		{
			name:           "before second interval",
			firstDetected:  time.Now(),
			currentTime:    time.Now().Add(45 * time.Second),
			retryIndex:     1,
			expectedReady:  false, // 45s < 2m
		},
		{
			name:           "at second interval",
			firstDetected:  time.Now(),
			currentTime:    time.Now().Add(2 * time.Minute),
			retryIndex:     1,
			expectedReady:  true, // 2m >= 2m
		},
		{
			name:           "before third interval",
			firstDetected:  time.Now(),
			currentTime:    time.Now().Add(3 * time.Minute),
			retryIndex:     2,
			expectedReady:  false, // 3m < 5m
		},
		{
			name:           "at third interval",
			firstDetected:  time.Now(),
			currentTime:    time.Now().Add(5 * time.Minute),
			retryIndex:     2,
			expectedReady:  true, // 5m >= 5m
		},
		{
			name:           "before fourth interval",
			firstDetected:  time.Now(),
			currentTime:    time.Now().Add(10 * time.Minute),
			retryIndex:     3,
			expectedReady:  false, // 10m < 15m
		},
		{
			name:           "at fourth interval",
			firstDetected:  time.Now(),
			currentTime:    time.Now().Add(15 * time.Minute),
			retryIndex:     3,
			expectedReady:  true, // 15m >= 15m
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &BackoffState{
				FirstDetected:      tt.firstDetected,
				CurrentRetryIndex: tt.retryIndex,
			}

			ready := daemon.isTimeForNextRetry(state, tt.currentTime, intervals)
			if ready != tt.expectedReady {
				t.Errorf("isTimeForNextRetry() = %v, want %v", ready, tt.expectedReady)
			}
		})
	}
}

// TestClearPendingEvent validates event cleanup.
func TestClearPendingEvent(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "backoff-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := BackoffConfig{
		WorkspaceRoot: tempDir,
	}

	daemon, err := NewTransientStarvationBackoff(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	workspacePath := filepath.Join(tempDir, "test-workspace")
	now := time.Now()

	// Add a pending event
	daemon.startBackoffTracking(workspacePath, now, 10, 0)
	if _, exists := daemon.pendingEvents[workspacePath]; !exists {
		t.Fatal("Pending event not created")
	}

	// Clear it
	daemon.clearPendingEvent(workspacePath)
	if _, exists := daemon.pendingEvents[workspacePath]; exists {
		t.Error("Pending event not cleared")
	}
}

// TestGetPendingEvents validates event retrieval.
func TestGetPendingEvents(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "backoff-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := BackoffConfig{
		WorkspaceRoot: tempDir,
	}

	daemon, err := NewTransientStarvationBackoff(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	// Add multiple pending events
	workspace1 := filepath.Join(tempDir, "workspace1")
	workspace2 := filepath.Join(tempDir, "workspace2")
	now := time.Now()

	daemon.startBackoffTracking(workspace1, now, 10, 0)
	daemon.startBackoffTracking(workspace2, now.Add(1*time.Minute), 20, 0)

	// Get all events
	events := daemon.GetPendingEvents()
	if len(events) != 2 {
		t.Errorf("Expected 2 pending events, got %d", len(events))
	}

	// Verify it's a copy (modifying returned map shouldn't affect daemon)
	delete(events, workspace1)
	if _, exists := daemon.pendingEvents[workspace1]; !exists {
		t.Error("GetPendingEvents returned direct reference instead of copy")
	}
}

// TestStop validates daemon shutdown.
func TestStop(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "backoff-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := BackoffConfig{
		WorkspaceRoot: tempDir,
	}

	daemon, err := NewTransientStarvationBackoff(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	if !daemon.IsRunning() {
		t.Error("Daemon should be running initially")
	}

	daemon.Stop()

	if daemon.IsRunning() {
		t.Error("Daemon should be stopped after Stop()")
	}

	// Second stop should be idempotent
	daemon.Stop()
	if daemon.IsRunning() {
		t.Error("Daemon should still be stopped after second Stop()")
	}
}

// TestIsTimeForNextRetryExhausted validates behavior after all intervals exhausted.
func TestIsTimeForNextRetryExhausted(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "backoff-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := BackoffConfig{
		WorkspaceRoot: tempDir,
	}

	daemon, err := NewTransientStarvationBackoff(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	intervals := daemon.backoffIntervals
	state := &BackoffState{
		FirstDetected:      time.Now(),
		CurrentRetryIndex:  len(intervals), // Beyond last interval
	}

	// When all intervals are exhausted, should not be ready for next retry
	ready := daemon.isTimeForNextRetry(state, time.Now().Add(24*time.Hour), intervals)
	if ready {
		t.Error("Should not be ready for next retry when all intervals exhausted")
	}
}

// TestHelperFunctions validates utility functions.
func TestHelperFunctions(t *testing.T) {
	t.Run("countJSONItems", func(t *testing.T) {
		tests := []struct {
			json string
			expected int
		}{
			{`[]`, 0},
			{`[{"id": "1"}]`, 1},
			{`[{"id": "1"}, {"id": "2"}]`, 2},
			{`[{"id": "1"}, {"id": "2"}, {"id": "3"}]`, 3},
			{`[{"name": "test"}]`, 0}, // No "id" field
		}

		for _, tt := range tests {
			result := countJSONItems(tt.json)
			if result != tt.expected {
				t.Errorf("countJSONItems(%q) = %d, want %d", tt.json, result, tt.expected)
			}
		}
	})

	t.Run("splitLines", func(t *testing.T) {
		tests := []struct {
			input string
			expected []string
		}{
			{"", []string{}},
			{"single", []string{"single"}},
			{"line1\nline2", []string{"line1", "line2"}},
			{"line1\nline2\nline3", []string{"line1", "line2", "line3"}},
			{"line1\n\nline3", []string{"line1", "", "line3"}},
		}

		for _, tt := range tests {
			result := splitLines(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("splitLines(%q) length = %d, want %d", tt.input, len(result), len(tt.expected))
				continue
			}
			for i, line := range result {
				if line != tt.expected[i] {
					t.Errorf("splitLines(%q)[%d] = %q, want %q", tt.input, i, line, tt.expected[i])
				}
			}
		}
	})

	t.Run("trimSpace", func(t *testing.T) {
		tests := []struct {
			input string
			expected string
		}{
			{"", ""},
			{"nospace", "nospace"},
			{"  spaces  ", "spaces"},
			{"\t\ttabs\t\t", "tabs"},
			{"\n\nnewlines\n\n", "newlines"},
			{"  mixed \t\n", "mixed"},
		}

		for _, tt := range tests {
			result := trimSpace(tt.input)
			if result != tt.expected {
				t.Errorf("trimSpace(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		}
	})

	t.Run("index", func(t *testing.T) {
		tests := []struct {
			s string
			substr string
			start int
			expected int
		}{
			{"hello world", "world", 0, 6},
			{"hello world", "world", 6, 6},
			{"hello world", "world", 7, -1}, // Start after substring
			{"hello world", "xyz", 0, -1},   // Not found
			{"", "test", 0, -1},             // Empty string
			{"test", "", 0, -1},             // Empty substring
		}

		for _, tt := range tests {
			result := index(tt.s, tt.substr, tt.start)
			if result != tt.expected {
				t.Errorf("index(%q, %q, %d) = %d, want %d", tt.s, tt.substr, tt.start, result, tt.expected)
			}
		}
	})
}
