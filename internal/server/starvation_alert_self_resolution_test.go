package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewStarvationAlertSelfResolution(t *testing.T) {
	tempDir := t.TempDir()

	cfg := SelfResolutionConfig{
		WorkspaceRoot:       tempDir,
		CheckInterval:       5 * time.Minute,
		AlertLabel:          "starvation-alert",
		EnablePluckFallback: false,
		MaxConsecutiveChecks: 3,
	}

	daemon, err := NewStarvationAlertSelfResolution(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	if daemon == nil {
		t.Fatal("Daemon is nil")
	}

	if daemon.checkInterval != 5*time.Minute {
		t.Errorf("Expected check interval 5m, got %v", daemon.checkInterval)
	}

	if daemon.alertLabel != "starvation-alert" {
		t.Errorf("Expected alert label 'starvation-alert', got %s", daemon.alertLabel)
	}

	if daemon.maxConsecutiveChecks != 3 {
		t.Errorf("Expected max consecutive checks 3, got %d", daemon.maxConsecutiveChecks)
	}

	daemon.Stop()
}

func TestNewStarvationAlertSelfResolutionDefaults(t *testing.T) {
	tempDir := t.TempDir()

	cfg := SelfResolutionConfig{
		WorkspaceRoot: tempDir,
		// Leave other fields empty to test defaults
	}

	daemon, err := NewStarvationAlertSelfResolution(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	if daemon.checkInterval != 5*time.Minute {
		t.Errorf("Expected default check interval 5m, got %v", daemon.checkInterval)
	}

	if daemon.alertLabel != "starvation-alert" {
		t.Errorf("Expected default alert label 'starvation-alert', got %s", daemon.alertLabel)
	}

	if daemon.maxConsecutiveChecks != 3 {
		t.Errorf("Expected default max consecutive checks 3, got %d", daemon.maxConsecutiveChecks)
	}

	daemon.Stop()
}

func TestNewStarvationAlertSelfResolutionErrors(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SelfResolutionConfig
		wantErr string
	}{
		{
			name: "missing workspace root",
			cfg: SelfResolutionConfig{
				WorkspaceRoot: "",
			},
			wantErr: "workspace root is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewStarvationAlertSelfResolution(tt.cfg)
			if err == nil {
				t.Fatal("Expected error, got nil")
			}
			if err.Error() != tt.wantErr {
				t.Errorf("Expected error '%s', got '%s'", tt.wantErr, err.Error())
			}
		})
	}
}

func TestFindWorkspacesWithBeads(t *testing.T) {
	tempDir := t.TempDir()

	// Create mock workspaces with bead databases
	workspace1 := filepath.Join(tempDir, "workspace1")
	workspace2 := filepath.Join(tempDir, "workspace2")
	emptyWorkspace := filepath.Join(tempDir, "empty")

	for _, dir := range []string{workspace1, workspace2, emptyWorkspace} {
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0755); err != nil {
			t.Fatalf("Failed to create workspace: %v", err)
		}
	}

	// Create bead databases for workspace1 and workspace2
	for _, ws := range []string{workspace1, workspace2} {
		dbPath := filepath.Join(ws, ".beads", "beads.db")
		file, err := os.Create(dbPath)
		if err != nil {
			t.Fatalf("Failed to create bead.db: %v", err)
		}
		file.Close()
	}

	cfg := SelfResolutionConfig{
		WorkspaceRoot:       tempDir,
		EnablePluckFallback: false,
	}

	daemon, err := NewStarvationAlertSelfResolution(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}
	defer daemon.Stop()

	workspaces, err := daemon.findWorkspacesWithBeads()
	if err != nil {
		t.Fatalf("Failed to find workspaces: %v", err)
	}

	if len(workspaces) != 2 {
		t.Errorf("Expected 2 workspaces, got %d", len(workspaces))
	}

	// Check that the workspaces are the expected ones
	workspaceMap := make(map[string]bool)
	for _, ws := range workspaces {
		workspaceMap[filepath.Base(ws)] = true
	}

	if !workspaceMap["workspace1"] || !workspaceMap["workspace2"] {
		t.Errorf("Expected workspaces workspace1 and workspace2, got %v", workspaceMap)
	}

	if workspaceMap["empty"] {
		t.Error("Empty workspace should not be included")
	}
}

func TestCheckHistoryTracking(t *testing.T) {
	tempDir := t.TempDir()
	cfg := SelfResolutionConfig{
		WorkspaceRoot:       tempDir,
		EnablePluckFallback: false,
	}

	daemon, err := NewStarvationAlertSelfResolution(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}
	defer daemon.Stop()

	// Test that check history is tracked
	beadID := "test-abc123"
	workspace := "test-workspace"

	daemon.mu.Lock()
	history, exists := daemon.checkHistory[beadID]
	if exists {
		t.Fatal("History should not exist for new bead")
	}

	// Create history entry
	history = &CheckHistory{
		AlertID:    beadID,
		Workspace:  workspace,
		FirstCheck: time.Now(),
		CheckCount: 0,
	}
	daemon.checkHistory[beadID] = history
	daemon.mu.Unlock()

	// Verify it was stored
	daemon.mu.Lock()
	stored, ok := daemon.checkHistory[beadID]
	daemon.mu.Unlock()

	if !ok {
		t.Fatal("History was not stored")
	}

	if stored.AlertID != beadID {
		t.Errorf("Expected alert ID %s, got %s", beadID, stored.AlertID)
	}

	if stored.Workspace != workspace {
		t.Errorf("Expected workspace %s, got %s", workspace, stored.Workspace)
	}

	if stored.CheckCount != 0 {
		t.Errorf("Expected check count 0, got %d", stored.CheckCount)
	}
}

func TestGetCheckHistory(t *testing.T) {
	tempDir := t.TempDir()
	cfg := SelfResolutionConfig{
		WorkspaceRoot:       tempDir,
		EnablePluckFallback: false,
	}

	daemon, err := NewStarvationAlertSelfResolution(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}
	defer daemon.Stop()

	// Add some history
	daemon.mu.Lock()
	daemon.checkHistory["bead1"] = &CheckHistory{
		AlertID:   "bead1",
		Workspace: "ws1",
		CheckCount: 1,
	}
	daemon.checkHistory["bead2"] = &CheckHistory{
		AlertID:   "bead2",
		Workspace: "ws2",
		CheckCount: 2,
	}
	daemon.mu.Unlock()

	// Get history
	history := daemon.GetCheckHistory()

	if len(history) != 2 {
		t.Errorf("Expected 2 history entries, got %d", len(history))
	}

	if history["bead1"].CheckCount != 1 {
		t.Errorf("Expected bead1 check count 1, got %d", history["bead1"].CheckCount)
	}

	if history["bead2"].CheckCount != 2 {
		t.Errorf("Expected bead2 check count 2, got %d", history["bead2"].CheckCount)
	}
}

func TestStopAndIsRunning(t *testing.T) {
	tempDir := t.TempDir()
	cfg := SelfResolutionConfig{
		WorkspaceRoot:       tempDir,
		EnablePluckFallback: false,
	}

	daemon, err := NewStarvationAlertSelfResolution(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	// Initially not running (no Start called)
	if daemon.IsRunning() {
		t.Error("Daemon should not be running before Start")
	}

	// Stop should be idempotent
	daemon.Stop()
	daemon.Stop() // Call again - should not panic

	if daemon.IsRunning() {
		t.Error("Daemon should not be running after Stop")
	}
}

func TestCountReadyBeadsDirect(t *testing.T) {
	// This test verifies the fallback counting method works
	// We can't easily test this without a real bead database,
	// but we can at least ensure it doesn't crash
	tempDir := t.TempDir()
	cfg := SelfResolutionConfig{
		WorkspaceRoot:       tempDir,
		EnablePluckFallback: false,
	}

	daemon, err := NewStarvationAlertSelfResolution(cfg)
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}
	defer daemon.Stop()

	// Create a mock workspace
	workspacePath := filepath.Join(tempDir, "test-ws")
	if err := os.MkdirAll(workspacePath, 0755); err != nil {
		t.Fatalf("Failed to create workspace: %v", err)
	}

	ctx := context.Background()

	// This should not crash even without a real bead database
	count, err := daemon.countReadyBeadsDirect(ctx, workspacePath)
	if err != nil {
		// Error is acceptable for non-existent workspace
		// We're just checking it doesn't crash
	}

	// Count should be 0 or positive
	if count < 0 {
		t.Errorf("Expected non-negative count, got %d", count)
	}
}
