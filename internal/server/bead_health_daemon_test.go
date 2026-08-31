package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBeadHealthDaemon_StaleWorkerDetection(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "bead-health-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create heartbeats.jsonl with a stale worker
	heartbeatsPath := filepath.Join(tmpDir, ".beads", "heartbeats.jsonl")
	if err := os.MkdirAll(filepath.Dir(heartbeatsPath), 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Write a heartbeat that's 2 hours old (well beyond 30 min threshold)
	staleTime := time.Now().Add(-2 * time.Hour)
	heartbeat := map[string]interface{}{
		"worker":     "test-worker",
		"state":      "idle",
		"ts":         staleTime.Format(time.RFC3339Nano),
		"last_strand": nil,
	}
	heartbeatData, _ := json.Marshal(heartbeat)
	if err := os.WriteFile(heartbeatsPath, append(heartbeatData, '\n'), 0644); err != nil {
		t.Fatalf("Failed to write heartbeat: %v", err)
	}

	// Create daemon with short threshold for testing
	daemon, err := NewBeadHealthDaemon(BeadHealthConfig{
		WorkspaceRoot:        tmpDir,
		CheckInterval:        time.Hour,
		StaleWorkerThreshold: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	// Test stale worker detection
	isStale, duration, err := daemon.isWorkerStale("test-worker")
	if err != nil {
		t.Fatalf("isWorkerStale failed: %v", err)
	}

	if !isStale {
		t.Error("Expected worker to be stale, but it's not")
	}

	if duration < 2*time.Hour {
		t.Errorf("Expected inactive duration ~2h, got %v", duration)
	}

	t.Logf("✓ Worker correctly detected as stale (inactive for %v)", duration)
}

func TestBeadHealthDaemon_ActiveWorkerDetection(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "bead-health-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create heartbeats.jsonl with an active worker
	heartbeatsPath := filepath.Join(tmpDir, ".beads", "heartbeats.jsonl")
	if err := os.MkdirAll(filepath.Dir(heartbeatsPath), 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Write a heartbeat that's 5 minutes old (within 30 min threshold)
	recentTime := time.Now().Add(-5 * time.Minute)
	heartbeat := map[string]interface{}{
		"worker":      "test-worker",
		"state":       "idle",
		"ts":          recentTime.Format(time.RFC3339Nano),
		"last_strand": nil,
	}
	heartbeatData, _ := json.Marshal(heartbeat)
	if err := os.WriteFile(heartbeatsPath, append(heartbeatData, '\n'), 0644); err != nil {
		t.Fatalf("Failed to write heartbeat: %v", err)
	}

	// Create daemon with 30 minute threshold
	daemon, err := NewBeadHealthDaemon(BeadHealthConfig{
		WorkspaceRoot:        tmpDir,
		CheckInterval:        time.Hour,
		StaleWorkerThreshold: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	// Test active worker detection
	isStale, duration, err := daemon.isWorkerStale("test-worker")
	if err != nil {
		t.Fatalf("isWorkerStale failed: %v", err)
	}

	if isStale {
		t.Error("Expected worker to be active, but it's stale")
	}

	if duration > 10*time.Minute {
		t.Errorf("Expected inactive duration ~5m, got %v", duration)
	}

	t.Logf("✓ Worker correctly detected as active (inactive for %v)", duration)
}

func TestBeadHealthDaemon_LogAssigneeClear(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "bead-health-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create daemon
	daemon, err := NewBeadHealthDaemon(BeadHealthConfig{
		WorkspaceRoot:        tmpDir,
		CheckInterval:        time.Hour,
		StaleWorkerThreshold: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	// Create a test repair
	repair := &BeadRepair{
		BeadID:           "test-bead-1",
		Workspace:        "test-workspace",
		Timestamp:        time.Now(),
		RepairType:       "clear-assignee",
		Success:          true,
		PreviousAssignee: "stale-worker",
		WorkerInactive:   true,
		InactiveDuration: "2h30m15s",
		ActionTaken:      "Cleared stale assignee: stale-worker (inactive for 2h30m15s)",
	}

	// Log the assignee clear
	daemon.logAssigneeClear(repair)

	// Verify the log file exists and contains the entry
	logPath := filepath.Join(tmpDir, ".beads", "diagnostics", "assignee-clears.log")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Fatalf("Assignee clear log file was not created: %v", err)
	}

	// Read and verify the log entry
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	var logEntry map[string]interface{}
	if err := json.Unmarshal(data, &logEntry); err != nil {
		t.Fatalf("Failed to parse log entry: %v", err)
	}

	if logEntry["bead_id"] != "test-bead-1" {
		t.Errorf("Expected bead_id test-bead-1, got %v", logEntry["bead_id"])
	}

	if logEntry["previous_assignee"] != "stale-worker" {
		t.Errorf("Expected previous_assignee stale-worker, got %v", logEntry["previous_assignee"])
	}

	if logEntry["worker_inactive"] != true {
		t.Errorf("Expected worker_inactive true, got %v", logEntry["worker_inactive"])
	}

	t.Logf("✓ Assignee clear log created successfully with correct data")
}

func TestExclusionTracker_StaleAssigneeReason(t *testing.T) {
	// This would require a full workspace setup with bead-rs
	// For now, we'll skip this test
	t.Skip("Requires full bead workspace setup")
}
