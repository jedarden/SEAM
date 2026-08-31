package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRootCauseAnalyzer_Analyze_PrimaryStrategy tests root cause analysis with primary strategy.
func TestRootCauseAnalyzer_Analyze_PrimaryStrategy(t *testing.T) {
	analyzer := NewRootCauseAnalyzer()

	// Create a temporary workspace
	tempDir := t.TempDir()
	workspacePath := filepath.Join(tempDir, "test-workspace")
	os.MkdirAll(workspacePath, 0755)
	os.MkdirAll(filepath.Join(workspacePath, ".beads"), 0755)

	// Create a minimal database and checkpoint
	dbPath := filepath.Join(workspacePath, ".beads", "beads.db")
	checkpointPath := filepath.Join(workspacePath, ".beads", "checkpoint", "current.json")
	os.WriteFile(dbPath, []byte("test db"), 0644)
	os.MkdirAll(filepath.Join(workspacePath, ".beads", "checkpoint"), 0755)
	os.WriteFile(checkpointPath, []byte(`{"issues":[]}`), 0644)

	// Test primary strategy (transient starvation)
	rootCause, autoRecovered, details := analyzer.Analyze(
		context.Background(),
		workspacePath,
		"primary",
		5,
		nil,
	)

	if rootCause != "transient-starvation" {
		t.Errorf("Expected root cause 'transient-starvation', got '%s'", rootCause)
	}

	if !autoRecovered {
		t.Errorf("Expected auto-recovered=true for primary strategy")
	}

	if details == "" {
		t.Errorf("Expected non-empty details")
	}

	t.Logf("Primary strategy analysis: rootCause=%s, autoRecovered=%v", rootCause, autoRecovered)
	t.Logf("Details:\n%s", details)
}

// TestRootCauseAnalyzer_Analyze_DirectDBStrategy tests root cause analysis with direct_db strategy.
func TestRootCauseAnalyzer_Analyze_DirectDBStrategy(t *testing.T) {
	analyzer := NewRootCauseAnalyzer()

	// Create a temporary workspace
	tempDir := t.TempDir()
	workspacePath := filepath.Join(tempDir, "test-workspace")
	os.MkdirAll(workspacePath, 0755)
	os.MkdirAll(filepath.Join(workspacePath, ".beads"), 0755)

	// Create a minimal database and checkpoint
	dbPath := filepath.Join(workspacePath, ".beads", "beads.db")
	checkpointPath := filepath.Join(workspacePath, ".beads", "checkpoint", "current.json")
	os.WriteFile(dbPath, []byte("test db"), 0644)
	os.MkdirAll(filepath.Join(workspacePath, ".beads", "checkpoint"), 0755)
	os.WriteFile(checkpointPath, []byte(`{"issues":[]}`), 0644)

	// Test direct_db strategy (CLI failure)
	rootCause, autoRecovered, details := analyzer.Analyze(
		context.Background(),
		workspacePath,
		"direct_db",
		3,
		nil,
	)

	if rootCause != "cli-failure" {
		t.Errorf("Expected root cause 'cli-failure', got '%s'", rootCause)
	}

	if !autoRecovered {
		t.Errorf("Expected auto-recovered=true for direct_db strategy")
	}

	t.Logf("Direct DB strategy analysis: rootCause=%s, autoRecovered=%v", rootCause, autoRecovered)
	t.Logf("Details:\n%s", details)
}

// TestRootCauseAnalyzer_Analyze_CheckpointStrategy tests root cause analysis with checkpoint strategy.
func TestRootCauseAnalyzer_Analyze_CheckpointStrategy(t *testing.T) {
	analyzer := NewRootCauseAnalyzer()

	// Create a temporary workspace
	tempDir := t.TempDir()
	workspacePath := filepath.Join(tempDir, "test-workspace")
	os.MkdirAll(workspacePath, 0755)
	os.MkdirAll(filepath.Join(workspacePath, ".beads"), 0755)

	// Create a minimal database and checkpoint
	dbPath := filepath.Join(workspacePath, ".beads", "beads.db")
	checkpointPath := filepath.Join(workspacePath, ".beads", "checkpoint", "current.json")
	os.WriteFile(dbPath, []byte("test db"), 0644)
	os.MkdirAll(filepath.Join(workspacePath, ".beads", "checkpoint"), 0755)
	os.WriteFile(checkpointPath, []byte(`{"issues":[]}`), 0644)

	// Test checkpoint strategy (database corruption/desync)
	rootCause, autoRecovered, details := analyzer.Analyze(
		context.Background(),
		workspacePath,
		"checkpoint",
		2,
		nil,
	)

	if rootCause != "database-corruption" {
		t.Errorf("Expected root cause 'database-corruption', got '%s'", rootCause)
	}

	if !autoRecovered {
		t.Errorf("Expected auto-recovered=true for checkpoint strategy")
	}

	t.Logf("Checkpoint strategy analysis: rootCause=%s, autoRecovered=%v", rootCause, autoRecovered)
	t.Logf("Details:\n%s", details)
}

// TestRootCauseAnalyzer_CheckCheckpointFreshness tests checkpoint freshness detection.
func TestRootCauseAnalyzer_CheckCheckpointFreshness(t *testing.T) {
	analyzer := NewRootCauseAnalyzer()

	tempDir := t.TempDir()
	workspacePath := filepath.Join(tempDir, "test-workspace")
	os.MkdirAll(workspacePath, 0755)

	// Create database and checkpoint with same timestamp
	dbPath := filepath.Join(workspacePath, ".beads", "beads.db")
	checkpointPath := filepath.Join(workspacePath, ".beads", "checkpoint", "current.json")
	os.MkdirAll(filepath.Join(workspacePath, ".beads", "checkpoint"), 0755)

	now := time.Now()
	os.WriteFile(dbPath, []byte("test db"), 0644)
	os.WriteFile(checkpointPath, []byte(`{"issues":[]}`), 0644)

	// Set modification times to be recent and close
	os.Chtimes(dbPath, now, now)
	os.Chtimes(checkpointPath, now.Add(-1*time.Minute), now.Add(-1*time.Minute))

	healthy, details := analyzer.checkCheckpointFreshness(context.Background(), workspacePath)

	if !healthy {
		t.Errorf("Expected checkpoint freshness check to pass when age difference is small")
	}

	t.Logf("Checkpoint freshness check (fresh): healthy=%v, details=%v", healthy, details)

	// Now test with stale checkpoint (checkpoint much older than database)
	oldTime := now.Add(-10 * time.Minute)
	os.Chtimes(checkpointPath, oldTime, oldTime)

	healthy, details = analyzer.checkCheckpointFreshness(context.Background(), workspacePath)

	if healthy {
		t.Errorf("Expected checkpoint freshness check to fail when checkpoint is stale")
	}

	t.Logf("Checkpoint freshness check (stale): healthy=%v, details=%v", healthy, details)
}

// TestRootCauseAnalyzer_CheckDatabaseIntegrity tests database integrity checking.
func TestRootCauseAnalyzer_CheckDatabaseIntegrity(t *testing.T) {
	analyzer := NewRootCauseAnalyzer()

	tempDir := t.TempDir()
	workspacePath := filepath.Join(tempDir, "test-workspace")
	os.MkdirAll(workspacePath, 0755)
	os.MkdirAll(filepath.Join(workspacePath, ".beads"), 0755)

	// Create a minimal database
	dbPath := filepath.Join(workspacePath, ".beads", "beads.db")
	os.WriteFile(dbPath, []byte("test db"), 0644)

	healthy, details := analyzer.checkDatabaseIntegrity(context.Background(), workspacePath)

	// Should be healthy or assume healthy (bead doctor might not be available)
	t.Logf("Database integrity check: healthy=%v, details=%v", healthy, details)
}

// TestRootCauseAnalyzer_CategorizeRootCause tests root cause categorization.
func TestRootCauseAnalyzer_CategorizeRootCause(t *testing.T) {
	analyzer := NewRootCauseAnalyzer()

	tests := []struct {
		name          string
		strategy      string
		dbHealthy     bool
		checkpointFresh bool
		readyCount    int
		expectedCause string
		expectedAuto  bool
	}{
		{
			name:          "primary strategy",
			strategy:      "primary",
			dbHealthy:     true,
			checkpointFresh: true,
			readyCount:    5,
			expectedCause: "transient-starvation",
			expectedAuto:  true,
		},
		{
			name:          "open_unassigned strategy",
			strategy:      "open_unassigned",
			dbHealthy:     true,
			checkpointFresh: true,
			readyCount:    3,
			expectedCause: "stale-assignment",
			expectedAuto:  true,
		},
		{
			name:          "open_status strategy",
			strategy:      "open_status",
			dbHealthy:     true,
			checkpointFresh: true,
			readyCount:    2,
			expectedCause: "query-bug",
			expectedAuto:  true,
		},
		{
			name:          "direct_db strategy",
			strategy:      "direct_db",
			dbHealthy:     true,
			checkpointFresh: true,
			readyCount:    4,
			expectedCause: "cli-failure",
			expectedAuto:  true,
		},
		{
			name:          "checkpoint strategy with unhealthy DB",
			strategy:      "checkpoint",
			dbHealthy:     false,
			checkpointFresh: true,
			readyCount:    1,
			expectedCause: "database-corruption",
			expectedAuto:  true,
		},
		{
			name:          "checkpoint strategy with stale checkpoint",
			strategy:      "checkpoint",
			dbHealthy:     true,
			checkpointFresh: false,
			readyCount:    1,
			expectedCause: "checkpoint-desync",
			expectedAuto:  true,
		},
		{
			name:          "checkpoint strategy healthy",
			strategy:      "checkpoint",
			dbHealthy:     true,
			checkpointFresh: true,
			readyCount:    1,
			expectedCause: "database-corruption",
			expectedAuto:  true,
		},
		{
			name:          "unknown strategy",
			strategy:      "unknown_strategy",
			dbHealthy:     true,
			checkpointFresh: true,
			readyCount:    0,
			expectedCause: "unknown",
			expectedAuto:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootCause, autoRecovered := analyzer.categorizeRootCause(
				tt.strategy,
				tt.dbHealthy,
				tt.checkpointFresh,
				tt.readyCount,
			)

			if rootCause != tt.expectedCause {
				t.Errorf("Expected root cause '%s', got '%s'", tt.expectedCause, rootCause)
			}

			if autoRecovered != tt.expectedAuto {
				t.Errorf("Expected auto-recovered=%v, got %v", tt.expectedAuto, autoRecovered)
			}

			t.Logf("%s: rootCause=%s, autoRecovered=%v", tt.name, rootCause, autoRecovered)
		})
	}
}

// TestRootCauseAnalyzer_Analyze_Integration tests full analysis workflow.
func TestRootCauseAnalyzer_Analyze_Integration(t *testing.T) {
	analyzer := NewRootCauseAnalyzer()

	tempDir := t.TempDir()
	workspacePath := filepath.Join(tempDir, "test-workspace")
	os.MkdirAll(workspacePath, 0755)
	os.MkdirAll(filepath.Join(workspacePath, ".beads"), 0755)
	os.MkdirAll(filepath.Join(workspacePath, ".beads", "checkpoint"), 0755)

	// Create database and checkpoint
	dbPath := filepath.Join(workspacePath, ".beads", "beads.db")
	checkpointPath := filepath.Join(workspacePath, ".beads", "checkpoint", "current.json")
	os.WriteFile(dbPath, []byte("test db"), 0644)
	os.WriteFile(checkpointPath, []byte(`{"issues":[]}`), 0644)

	// Set recent timestamps
	now := time.Now()
	os.Chtimes(dbPath, now, now)
	os.Chtimes(checkpointPath, now.Add(-30*time.Second), now.Add(-30*time.Second))

	// Test all strategies
	strategies := []struct {
		strategy         string
		expectedRootCause string
	}{
		{"primary", "transient-starvation"},
		{"open_unassigned", "stale-assignment"},
		{"open_status", "query-bug"},
		{"direct_db", "cli-failure"},
		{"checkpoint", "database-corruption"},
	}

	for _, tc := range strategies {
		t.Run(tc.strategy, func(t *testing.T) {
			rootCause, autoRecovered, details := analyzer.Analyze(
				context.Background(),
				workspacePath,
				tc.strategy,
				3,
				nil,
			)

			if rootCause != tc.expectedRootCause {
				t.Errorf("Expected root cause '%s', got '%s'", tc.expectedRootCause, rootCause)
			}

			if !autoRecovered {
				t.Errorf("Expected auto-recovered=true for %s strategy", tc.strategy)
			}

			if details == "" {
				t.Errorf("Expected non-empty details")
			}

			// Verify details contains expected sections
			if !contains(details, "**Root Cause:**") {
				t.Errorf("Details missing root cause section")
			}
			if !contains(details, "**Auto-Recovered:**") {
				t.Errorf("Details missing auto-recovered section")
			}
			if !contains(details, "**Analysis:**") {
				t.Errorf("Details missing analysis section")
			}
			if !contains(details, "**Diagnostic Results:**") {
				t.Errorf("Details missing diagnostic results section")
			}

			t.Logf("%s strategy: rootCause=%s, autoRecovered=%v", tc.strategy, rootCause, autoRecovered)
		})
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
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
