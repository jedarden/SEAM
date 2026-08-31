package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDatabaseCorruptionRecoveryDaemon_DetectAnomalyPattern(t *testing.T) {
	// Create a temporary workspace for testing
	tempDir := t.TempDir()
	workspacePath := filepath.Join(tempDir, "test-workspace")

	// Initialize the workspace with bead init
	ctx := context.Background()
	cmd := prepareTestCommand(ctx, "bead", "init", workspacePath)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to initialize test workspace: %v", err)
	}

	daemon, err := NewDatabaseCorruptionRecoveryDaemon(DatabaseCorruptionConfig{
		WorkspaceRoot:     tempDir,
		CheckInterval:     time.Minute,
		StatePath:         filepath.Join(tempDir, "state.json"),
		DiagnosticLogPath: filepath.Join(tempDir, "recovery.log"),
	})
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	// Test 1: Normal case - no anomaly (CLI and DB agree at 0)
	cliCount, dbCount, err := daemon.detectAnomalyPattern(ctx, workspacePath)
	if err != nil {
		t.Errorf("detectAnomalyPattern failed: %v", err)
	}
	if cliCount != 0 || dbCount != 0 {
		t.Errorf("Expected no beads in empty workspace, got CLI=%d DB=%d", cliCount, dbCount)
	}

	// Test 2: Create a test bead
	cmd = prepareTestCommand(ctx, "bead", "create", workspacePath,
		"--title", "Test Bead",
		"--priority", "2",
		"--issue-type", "task",
		"--status", "open")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to create test bead: %v", err)
	}

	// Wait a moment for the database to sync
	time.Sleep(100 * time.Millisecond)

	// Test 3: Normal case - CLI and DB agree at 1
	cliCount, dbCount, err = daemon.detectAnomalyPattern(ctx, workspacePath)
	if err != nil {
		t.Errorf("detectAnomalyPattern failed after bead creation: %v", err)
	}
	if cliCount != 1 || dbCount != 1 {
		t.Errorf("Expected 1 bead, got CLI=%d DB=%d", cliCount, dbCount)
	}

	// Test 4: Simulate database corruption by zeroing the database
	dbPath := filepath.Join(workspacePath, ".beads", "beads.db")
	if err := os.WriteFile(dbPath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to simulate database corruption: %v", err)
	}

	// Test 5: Anomaly detected - CLI reports 1, DB reports 0
	cliCount, dbCount, err = daemon.detectAnomalyPattern(ctx, workspacePath)
	if err != nil {
		t.Errorf("detectAnomalyPattern failed after corruption: %v", err)
	}
	if cliCount != 1 {
		t.Errorf("Expected CLI to still report 1 bead, got %d", cliCount)
	}
	if dbCount != 0 {
		t.Errorf("Expected DB to report 0 beads after corruption, got %d", dbCount)
	}
	// This is the anomaly pattern we're looking for: CLI > 0 && DB == 0
	if !(cliCount > 0 && dbCount == 0) {
		t.Error("Failed to detect anomaly pattern (CLI > 0, DB == 0)")
	}
}

func TestDatabaseCorruptionRecoveryDaemon_RunBeadDoctorDiagnostics(t *testing.T) {
	tempDir := t.TempDir()
	workspacePath := filepath.Join(tempDir, "test-workspace")

	ctx := context.Background()
	cmd := prepareTestCommand(ctx, "bead", "init", workspacePath)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to initialize test workspace: %v", err)
	}

	daemon, err := NewDatabaseCorruptionRecoveryDaemon(DatabaseCorruptionConfig{
		WorkspaceRoot:     tempDir,
		CheckInterval:     time.Minute,
		StatePath:         filepath.Join(tempDir, "state.json"),
		DiagnosticLogPath: filepath.Join(tempDir, "recovery.log"),
	})
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	// Test diagnostics on healthy database
	output, healthy, err := daemon.runBeadDoctorDiagnostics(ctx, workspacePath)
	if err != nil {
		t.Errorf("runBeadDoctorDiagnostics failed on healthy database: %v", err)
	}
	if !healthy {
		t.Errorf("Expected healthy database, got unhealthy. Output: %s", output)
	}
}

func TestDatabaseCorruptionRecoveryDaemon_RunCheckpointRebuild(t *testing.T) {
	tempDir := t.TempDir()
	workspacePath := filepath.Join(tempDir, "test-workspace")

	ctx := context.Background()
	cmd := prepareTestCommand(ctx, "bead", "init", workspacePath)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to initialize test workspace: %v", err)
	}

	// Create a test bead
	cmd = prepareTestCommand(ctx, "bead", "create", workspacePath,
		"--title", "Test Bead",
		"--priority", "2",
		"--issue-type", "task",
		"--status", "open")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to create test bead: %v", err)
	}

	// Flush checkpoint
	cmd = prepareTestCommand(ctx, "bead", "sync", "flush-only", workspacePath)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to flush checkpoint: %v", err)
	}

	daemon, err := NewDatabaseCorruptionRecoveryDaemon(DatabaseCorruptionConfig{
		WorkspaceRoot:     tempDir,
		CheckInterval:     time.Minute,
		StatePath:         filepath.Join(tempDir, "state.json"),
		DiagnosticLogPath: filepath.Join(tempDir, "recovery.log"),
	})
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	// Corrupt the database
	dbPath := filepath.Join(workspacePath, ".beads", "beads.db")
	if err := os.WriteFile(dbPath, []byte("corrupted"), 0644); err != nil {
		t.Fatalf("Failed to corrupt database: %v", err)
	}

	// Run checkpoint rebuild
	if err := daemon.runCheckpointRebuild(ctx, workspacePath); err != nil {
		t.Errorf("runCheckpointRebuild failed: %v", err)
	}

	// Verify database is restored
	cliCount, dbCount, err := daemon.detectAnomalyPattern(ctx, workspacePath)
	if err != nil {
		t.Errorf("detectAnomalyPattern failed after rebuild: %v", err)
	}
	if cliCount != 1 || dbCount != 1 {
		t.Errorf("Expected 1 bead after rebuild, got CLI=%d DB=%d", cliCount, dbCount)
	}
}

func TestDatabaseCorruptionRecoveryDaemon_CloseStarvationAlerts(t *testing.T) {
	tempDir := t.TempDir()
	workspacePath := filepath.Join(tempDir, "test-workspace")

	ctx := context.Background()
	cmd := prepareTestCommand(ctx, "bead", "init", workspacePath)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to initialize test workspace: %v", err)
	}

	// Create a starvation alert bead
	cmd = prepareTestCommand(ctx, "bead", "create", workspacePath,
		"--title", "Starvation Alert: Test",
		"--priority", "1",
		"--issue-type", "task",
		"--status", "open",
		"--label", "starvation-alert")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to create starvation alert bead: %v", err)
	}

	daemon, err := NewDatabaseCorruptionRecoveryDaemon(DatabaseCorruptionConfig{
		WorkspaceRoot:     tempDir,
		CheckInterval:     time.Minute,
		StatePath:         filepath.Join(tempDir, "state.json"),
		DiagnosticLogPath: filepath.Join(tempDir, "recovery.log"),
	})
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	// Close starvation alerts
	closedAlerts := daemon.closeStarvationAlerts(ctx, workspacePath)
	if len(closedAlerts) != 1 {
		t.Errorf("Expected 1 closed alert, got %d", len(closedAlerts))
	}
}

func TestDatabaseCorruptionRecoveryDaemon_CreateDiagnosticBead(t *testing.T) {
	tempDir := t.TempDir()
	workspacePath := filepath.Join(tempDir, "test-workspace")

	ctx := context.Background()
	cmd := prepareTestCommand(ctx, "bead", "init", workspacePath)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to initialize test workspace: %v", err)
	}

	daemon, err := NewDatabaseCorruptionRecoveryDaemon(DatabaseCorruptionConfig{
		WorkspaceRoot:     tempDir,
		CheckInterval:     time.Minute,
		StatePath:         filepath.Join(tempDir, "state.json"),
		DiagnosticLogPath: filepath.Join(tempDir, "recovery.log"),
	})
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	result := &DatabaseRecoveryResult{
		Workspace:      filepath.Base(workspacePath),
		Timestamp:      time.Now(),
		Detected:       true,
		CliOpenCount:   5,
		DbOpenCount:    0,
		RecoveryMethod: "checkpoint-rebuild",
		Success:        true,
	}

	// Create diagnostic bead
	beadID, err := daemon.createDiagnosticBead(ctx, workspacePath, result)
	if err != nil {
		t.Errorf("createDiagnosticBead failed: %v", err)
	}
	if beadID == "" {
		t.Error("Expected non-empty bead ID")
	}
}

func TestDatabaseCorruptionRecoveryDaemon_SaveState(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "state.json")

	daemon, err := NewDatabaseCorruptionRecoveryDaemon(DatabaseCorruptionConfig{
		WorkspaceRoot:     tempDir,
		CheckInterval:     time.Minute,
		StatePath:         statePath,
		DiagnosticLogPath: filepath.Join(tempDir, "recovery.log"),
	})
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	// Save initial state
	if err := daemon.saveState(); err != nil {
		t.Errorf("saveState failed: %v", err)
	}

	// Verify state file exists
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Error("State file was not created")
	}

	// Modify state and save again
	daemon.recoveryCount = 5
	daemon.lastCheckTime = time.Now()

	if err := daemon.saveState(); err != nil {
		t.Errorf("saveState failed after modification: %v", err)
	}

	// Create new daemon and load state
	daemon2, err := NewDatabaseCorruptionRecoveryDaemon(DatabaseCorruptionConfig{
		WorkspaceRoot:     tempDir,
		CheckInterval:     time.Minute,
		StatePath:         statePath,
		DiagnosticLogPath: filepath.Join(tempDir, "recovery.log"),
	})
	if err != nil {
		t.Fatalf("Failed to create second daemon: %v", err)
	}

	if daemon2.GetRecoveryCount() != 5 {
		t.Errorf("Expected recovery count 5, got %d", daemon2.GetRecoveryCount())
	}
}

func TestDatabaseCorruptionRecoveryDaemon_IsRunning(t *testing.T) {
	tempDir := t.TempDir()

	daemon, err := NewDatabaseCorruptionRecoveryDaemon(DatabaseCorruptionConfig{
		WorkspaceRoot:     tempDir,
		CheckInterval:     time.Minute,
		StatePath:         filepath.Join(tempDir, "state.json"),
		DiagnosticLogPath: filepath.Join(tempDir, "recovery.log"),
	})
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	// Daemon should be running initially (no lease leader)
	if !daemon.IsRunning() {
		t.Error("Expected daemon to be running initially")
	}

	// Stop the daemon
	daemon.Stop()

	// Daemon should not be running after stop
	if daemon.IsRunning() {
		t.Error("Expected daemon to be stopped after Stop()")
	}
}

func TestDatabaseCorruptionRecoveryDaemon_GetRecoveryCount(t *testing.T) {
	tempDir := t.TempDir()

	daemon, err := NewDatabaseCorruptionRecoveryDaemon(DatabaseCorruptionConfig{
		WorkspaceRoot:     tempDir,
		CheckInterval:     time.Minute,
		StatePath:         filepath.Join(tempDir, "state.json"),
		DiagnosticLogPath: filepath.Join(tempDir, "recovery.log"),
	})
	if err != nil {
		t.Fatalf("Failed to create daemon: %v", err)
	}

	// Initial count should be 0
	if count := daemon.GetRecoveryCount(); count != 0 {
		t.Errorf("Expected initial recovery count 0, got %d", count)
	}

	// Modify internal state
	daemon.recoveryCount = 10

	// Verify count is returned correctly
	if count := daemon.GetRecoveryCount(); count != 10 {
		t.Errorf("Expected recovery count 10, got %d", count)
	}
}

// Helper function to prepare test commands
func prepareTestCommand(ctx context.Context, name string, workspacePath string, args ...string) *exec.Cmd {
	allArgs := append([]string{name}, args...)
	cmd := exec.CommandContext(ctx, "bead", allArgs...)
	cmd.Dir = workspacePath
	return cmd
}
