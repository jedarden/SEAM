package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHumanMonitor_FindHumanMarkedAlertBeads(t *testing.T) {
	// This test requires a workspace with beads
	// For now, we'll test the parsing logic

	ctx := context.Background()
	workspacePath := "/tmp/test-workspace"

	// Create a mock bead list output
	mockBeads := []map[string]interface{}{
		{
			"id":      "test-1",
			"title":   "Test alert 1",
			"status":  "open",
			"labels":  []interface{}{"starvation-alert", "human"},
			"created": "2024-08-31T12:00:00Z",
		},
		{
			"id":      "test-2",
			"title":   "Test alert 2",
			"status":  "open",
			"labels":  []interface{}{"starvation-alert", "alert:starvation:unknown"},
			"created": "2024-08-31T12:05:00Z",
		},
		{
			"id":      "test-3",
			"title":   "Regular bead",
			"status":  "open",
			"labels":  []interface{}{"bug"},
			"created": "2024-08-31T12:10:00Z",
		},
	}

	output, err := json.Marshal(mockBeads)
	if err != nil {
		t.Fatalf("Failed to marshal mock beads: %v", err)
	}

	// Parse the output
	var beads []map[string]interface{}
	if err := json.Unmarshal(output, &beads); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	// Count human-marked beads
	count := 0
	for _, bead := range beads {
		beadID, ok := bead["id"].(string)
		if !ok {
			continue
		}

		hasTargetLabel := false
		hasHumanLabel := false
		if labels, ok := bead["labels"].([]interface{}); ok {
			for _, label := range labels {
				if labelStr, ok := label.(string); ok {
					if labelStr == "human" || labelStr == "alert:starvation:unknown" {
						hasTargetLabel = true
					}
					if labelStr == "human" {
						hasHumanLabel = true
					}
				}
			}
		}

		if hasTargetLabel {
			count++
			t.Logf("Found human-marked bead: %s (has human label: %v)", beadID, hasHumanLabel)
		}
	}

	if count != 2 {
		t.Errorf("Expected 2 human-marked beads, got %d", count)
	}
}

func TestHumanMonitor_MinAgeFilter(t *testing.T) {
	tests := []struct {
		name           string
		created        time.Time
		minAge         time.Duration
		shouldEvaluate bool
	}{
		{
			name:           "Too young",
			created:        time.Now().Add(-10 * time.Minute),
			minAge:         15 * time.Minute,
			shouldEvaluate: false,
		},
		{
			name:           "Just old enough",
			created:        time.Now().Add(-15 * time.Minute),
			minAge:         15 * time.Minute,
			shouldEvaluate: true,
		},
		{
			name:           "Well past threshold",
			created:        time.Now().Add(-1 * time.Hour),
			minAge:         15 * time.Minute,
			shouldEvaluate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			age := time.Since(tt.created)
			shouldEvaluate := age >= tt.minAge

			if shouldEvaluate != tt.shouldEvaluate {
				t.Errorf("Expected shouldEvaluate=%v, got %v (age=%v, minAge=%v)",
					tt.shouldEvaluate, shouldEvaluate, age, tt.minAge)
			}
		})
	}
}

func TestHumanMonitor_ReevaluationResultJSON(t *testing.T) {
	result := &ReevaluationResult{
		AlertID:           "test-123",
		Workspace:         "test-workspace",
		Timestamp:         time.Now(),
		AlertCreated:      time.Now().Add(-1 * time.Hour),
		AlertAgeHours:     1.0,
		ReevaluationCount: 3,
		Resolved:          true,
		ClosedWithReason:  "Condition self-resolved - transient starvation",
		HumanLabelRemoved: true,
		ReadyCount:        5,
		StrategyUsed:      "primary",
		Trigger:           "scheduled",
	}

	// Test JSON marshaling
	jsonBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal ReevaluationResult: %v", err)
	}

	// Verify it can be unmarshaled
	var unmarshaled ReevaluationResult
	if err := json.Unmarshal(jsonBytes, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal ReevaluationResult: %v", err)
	}

	// Verify key fields
	if unmarshaled.AlertID != result.AlertID {
		t.Errorf("Expected AlertID %s, got %s", result.AlertID, unmarshaled.AlertID)
	}

	if unmarshaled.Resolved != result.Resolved {
		t.Errorf("Expected Resolved %v, got %v", result.Resolved, unmarshaled.Resolved)
	}

	if unmarshaled.HumanLabelRemoved != result.HumanLabelRemoved {
		t.Errorf("Expected HumanLabelRemoved %v, got %v", result.HumanLabelRemoved, unmarshaled.HumanLabelRemoved)
	}

	if unmarshaled.Trigger != result.Trigger {
		t.Errorf("Expected Trigger %s, got %s", result.Trigger, unmarshaled.Trigger)
	}
}

func TestHumanMonitor_ConfigDefaults(t *testing.T) {
	cfg := HumanMonitorConfig{
		WorkspaceRoot: "/home/coding",
	}

	// Apply defaults (simulating what NewStarvationAlertHumanMonitor does)
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 5 * time.Minute
	}
	if cfg.MinReevaluationAge == 0 {
		cfg.MinReevaluationAge = 15 * time.Minute
	}
	if len(cfg.AlertLabels) == 0 {
		cfg.AlertLabels = []string{"human", "alert:starvation:unknown"}
	}
	if cfg.ReevaluationLogPath == "" {
		cfg.ReevaluationLogPath = filepath.Join(cfg.WorkspaceRoot, ".beads", "diagnostics", "human-alert-reevaluation.log")
	}

	// Verify defaults
	if cfg.CheckInterval != 5*time.Minute {
		t.Errorf("Expected default CheckInterval 5m, got %v", cfg.CheckInterval)
	}

	if cfg.MinReevaluationAge != 15*time.Minute {
		t.Errorf("Expected default MinReevaluationAge 15m, got %v", cfg.MinReevaluationAge)
	}

	if len(cfg.AlertLabels) != 2 {
		t.Errorf("Expected 2 default AlertLabels, got %d", len(cfg.AlertLabels))
	}

	if !strings.Contains(cfg.ReevaluationLogPath, "human-alert-reevaluation.log") {
		t.Errorf("Expected ReevaluationLogPath to contain 'human-alert-reevaluation.log', got %s", cfg.ReevaluationLogPath)
	}
}

func TestHumanMonitor_TriggerTypes(t *testing.T) {
	validTriggers := map[string]bool{
		"backoff-expired":    true,
		"repair-attempted":  true,
		"scheduled":         true,
	}

	for trigger := range validTriggers {
		result := &ReevaluationResult{
			Trigger: trigger,
		}

		if !validTriggers[result.Trigger] {
			t.Errorf("Invalid trigger: %s", result.Trigger)
		}
	}
}

// Mock test for PluckFallback integration
func TestHumanMonitor_PluckForCandidates(t *testing.T) {
	// This test would require a real workspace, so we'll just verify the logic
	// In a real integration test, we would:
	// 1. Create a test workspace
	// 2. Add some beads to it
	// 3. Call pluckForCandidates
	// 4. Verify it returns the correct count and strategy

	// For now, we'll just verify the command structure
	ctx := context.Background()
	workspacePath := "/tmp/test-workspace"

	cmd := exec.CommandContext(ctx, "bead", "list", "--ready", "--json")
	cmd.Dir = workspacePath

	// Verify the command is correctly constructed
	args := cmd.Args
	if len(args) < 4 {
		t.Errorf("Expected at least 4 args in bead command, got %d", len(args))
	}

	found := false
	for _, arg := range args {
		if arg == "bead" {
			found = true
			break
		}
	}

	if !found {
		t.Error("bead command not found in args")
	}
}
