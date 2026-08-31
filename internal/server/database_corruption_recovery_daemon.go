package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DatabaseCorruptionRecoveryDaemon detects and recovers from database corruption
// by identifying the 'detection anomaly' pattern: CLI reports open beads but
// direct SQLite queries return 0. When detected, it runs automated recovery
// including bead doctor --repair and checkpoint rebuild, then creates diagnostic
// beads and closes resolved starvation alerts.
type DatabaseCorruptionRecoveryDaemon struct {
	mu                sync.RWMutex
	leaseLeader       *LeaseLeader
	workspaceRoot     string
	stopCh            chan struct{}
	stopped           bool
	checkInterval     time.Duration
	statePath         string
	diagnosticLogPath string
	recoveryCount     int
	lastCheckTime     time.Time
	onRecovery        func(result *DatabaseRecoveryResult)
}

// DatabaseRecoveryState tracks the daemon's persistent state.
type DatabaseRecoveryState struct {
	LastCheckTime      time.Time                   `json:"last_check_time"`
	TotalRecoveries    int                         `json:"total_recoveries"`
	WorkspaceRecoveries map[string]int             `json:"workspace_recoveries"`
	LastRecoveries     []DatabaseRecoverySummary   `json:"last_recoveries"`
}

// DatabaseRecoverySummary records a brief summary of a recovery.
type DatabaseRecoverySummary struct {
	Workspace    string    `json:"workspace"`
	RecoveryType string    `json:"recovery_type"`
	Timestamp    time.Time `json:"timestamp"`
	Success      bool      `json:"success"`
}

// DatabaseRecoveryResult records the outcome of a database corruption recovery.
type DatabaseRecoveryResult struct {
	Workspace         string    `json:"workspace"`
	Timestamp         time.Time `json:"timestamp"`
	Detected          bool      `json:"detected"`
	CliOpenCount      int       `json:"cli_open_count"`
	DbOpenCount       int       `json:"db_open_count"`
	RecoveryMethod    string    `json:"recovery_method"`
	Success           bool      `json:"success"`
	Error             string    `json:"error,omitempty"`
	DiagnosticsBefore string    `json:"diagnostics_before,omitempty"`
	DiagnosticsAfter  string    `json:"diagnostics_after,omitempty"`
	BeadsCreated      []string  `json:"beads_created,omitempty"`
	AlertsClosed      []string  `json:"alerts_closed,omitempty"`
}

// DatabaseCorruptionConfig holds configuration for the daemon.
type DatabaseCorruptionConfig struct {
	// WorkspaceRoot is the root directory containing all workspaces
	WorkspaceRoot string

	// LeaseLeader is the Kubernetes Lease leader elector (optional)
	LeaseLeader *LeaseLeader

	// CheckInterval is how often to check for corruption (default: 5 minutes)
	CheckInterval time.Duration

	// StatePath is where to store the daemon state JSON (default: .beads/database-recovery-state.json)
	StatePath string

	// DiagnosticLogPath is where to write structured logs (default: .beads/diagnostics/database-recovery.log)
	DiagnosticLogPath string

	// OnRecovery is called when a recovery is performed
	OnRecovery func(result *DatabaseRecoveryResult)
}

// NewDatabaseCorruptionRecoveryDaemon creates a new database corruption recovery daemon.
func NewDatabaseCorruptionRecoveryDaemon(cfg DatabaseCorruptionConfig) (*DatabaseCorruptionRecoveryDaemon, error) {
	if cfg.WorkspaceRoot == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 5 * time.Minute
	}
	if cfg.StatePath == "" {
		cfg.StatePath = filepath.Join(cfg.WorkspaceRoot, ".beads", "database-recovery-state.json")
	}
	if cfg.DiagnosticLogPath == "" {
		cfg.DiagnosticLogPath = filepath.Join(cfg.WorkspaceRoot, ".beads", "diagnostics", "database-recovery.log")
	}

	// Ensure diagnostic log directory exists
	if err := os.MkdirAll(filepath.Dir(cfg.DiagnosticLogPath), 0755); err != nil {
		return nil, fmt.Errorf("create diagnostic log directory: %w", err)
	}

	// Load existing state if it exists
	state := DatabaseRecoveryState{
		WorkspaceRecoveries: make(map[string]int),
		LastRecoveries:      make([]DatabaseRecoverySummary, 0, 100),
	}
	if stateData, err := os.ReadFile(cfg.StatePath); err == nil {
		if err := json.Unmarshal(stateData, &state); err != nil {
			log.Printf("[DatabaseRecovery] Failed to load state, starting fresh: %v", err)
		} else {
			log.Printf("[DatabaseRecovery] Loaded state: %d total recoveries, last check: %s",
				state.TotalRecoveries, state.LastCheckTime.Format(time.RFC3339))
		}
	}

	daemon := &DatabaseCorruptionRecoveryDaemon{
		leaseLeader:       cfg.LeaseLeader,
		workspaceRoot:     cfg.WorkspaceRoot,
		checkInterval:     cfg.CheckInterval,
		statePath:         cfg.StatePath,
		diagnosticLogPath: cfg.DiagnosticLogPath,
		stopCh:           make(chan struct{}),
		recoveryCount:    state.TotalRecoveries,
		lastCheckTime:    state.LastCheckTime,
		onRecovery:       cfg.OnRecovery,
	}

	return daemon, nil
}

// Start begins the database corruption recovery daemon.
func (d *DatabaseCorruptionRecoveryDaemon) Start(ctx context.Context) error {
	if d == nil {
		return fmt.Errorf("daemon is nil")
	}

	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return fmt.Errorf("daemon already stopped")
	}
	d.mu.Unlock()

	// Acquire leadership if configured
	if d.leaseLeader != nil {
		log.Printf("[DatabaseRecovery] Attempting to acquire leadership")
		if !d.leaseLeader.Acquire(ctx) {
			return fmt.Errorf("failed to acquire leadership")
		}
		log.Printf("[DatabaseRecovery] Leadership acquired")

		// Start the lease renewal goroutine
		renewCtx, cancelRenew := context.WithCancel(ctx)
		defer cancelRenew()

		go func() {
			d.leaseLeader.Renew(renewCtx)
			log.Printf("[DatabaseRecovery] Leadership lost, stopping daemon")
			d.Stop()
		}()
	} else {
		log.Printf("[DatabaseRecovery] Running without leadership (local mode)")
	}

	// Main loop
	ticker := time.NewTicker(d.checkInterval)
	defer ticker.Stop()

	log.Printf("[DatabaseRecovery] Starting database corruption recovery daemon (interval: %v)", d.checkInterval)

	// Run initial check immediately
	d.checkAllWorkspaces(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[DatabaseRecovery] Context cancelled, stopping daemon")
			return ctx.Err()
		case <-d.stopCh:
			log.Printf("[DatabaseRecovery] Stop signal received, stopping daemon")
			return nil
		case <-ticker.C:
			if d.leaseLeader != nil && !d.leaseLeader.IsLeader() {
				log.Printf("[DatabaseRecovery] No longer leader, skipping check")
				continue
			}

			d.checkAllWorkspaces(ctx)
		}
	}
}

// checkAllWorkspaces checks all workspaces for database corruption.
func (d *DatabaseCorruptionRecoveryDaemon) checkAllWorkspaces(ctx context.Context) {
	workspaces, err := d.findWorkspacesWithBeads()
	if err != nil {
		log.Printf("[DatabaseRecovery] Failed to find workspaces: %v", err)
		return
	}

	if len(workspaces) == 0 {
		log.Printf("[DatabaseRecovery] No workspaces with beads found")
		return
	}

	log.Printf("[DatabaseRecovery] Checking %d workspaces for database corruption", len(workspaces))

	recoveriesThisRun := 0
	for _, workspace := range workspaces {
		result := d.checkWorkspace(ctx, workspace)
		if result.Detected {
			recoveriesThisRun++
			if d.onRecovery != nil {
				d.onRecovery(result)
			}
		}
	}

	// Update state
	d.mu.Lock()
	d.lastCheckTime = time.Now()
	d.recoveryCount += recoveriesThisRun
	d.mu.Unlock()

	// Persist state
	if err := d.saveState(); err != nil {
		log.Printf("[DatabaseRecovery] Failed to save state: %v", err)
	}

	log.Printf("[DatabaseRecovery] Check complete: %d recoveries this run, %d total", recoveriesThisRun, d.recoveryCount)
}

// findWorkspacesWithBeads finds all directories with .beads/beads.db.
func (d *DatabaseCorruptionRecoveryDaemon) findWorkspacesWithBeads() ([]string, error) {
	var workspaces []string

	entries, err := os.ReadDir(d.workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("read workspace root: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		workspacePath := filepath.Join(d.workspaceRoot, entry.Name())
		beadDB := filepath.Join(workspacePath, ".beads", "beads.db")

		if _, err := os.Stat(beadDB); err == nil {
			workspaces = append(workspaces, workspacePath)
		}
	}

	return workspaces, nil
}

// checkWorkspace checks a single workspace for database corruption and attempts recovery.
func (d *DatabaseCorruptionRecoveryDaemon) checkWorkspace(ctx context.Context, workspacePath string) *DatabaseRecoveryResult {
	workspaceName := filepath.Base(workspacePath)
	result := &DatabaseRecoveryResult{
		Workspace: workspaceName,
		Timestamp: time.Now(),
	}

	log.Printf("[DatabaseRecovery] Checking workspace: %s", workspaceName)

	// Step 1: Detect the anomaly pattern
	cliCount, dbCount, err := d.detectAnomalyPattern(ctx, workspacePath)
	if err != nil {
		result.Error = fmt.Sprintf("Detection failed: %v", err)
		log.Printf("[DatabaseRecovery] %s: %v", workspaceName, result.Error)
		return result
	}

	result.CliOpenCount = cliCount
	result.DbOpenCount = dbCount

	// Only proceed if the specific anomaly pattern is detected
	if cliCount == 0 || dbCount > 0 {
		log.Printf("[DatabaseRecovery] No anomaly pattern in %s (CLI=%d, DB=%d)", workspaceName, cliCount, dbCount)
		return result
	}

	result.Detected = true
	log.Printf("[DatabaseRecovery] ✓ Anomaly detected in %s (CLI=%d open, DB=%d open)", workspaceName, cliCount, dbCount)

	// Step 2: Run bead doctor diagnostics first
	diagnosticOutput, dbHealthy, err := d.runBeadDoctorDiagnostics(ctx, workspacePath)
	result.DiagnosticsBefore = diagnosticOutput
	if err != nil {
		log.Printf("[DatabaseRecovery] bead doctor diagnostics failed in %s: %v", workspaceName, err)
	}

	// Step 3: Run bead doctor --repair
	repairErr := d.runBeadDoctorRepair(ctx, workspacePath)
	if repairErr != nil {
		result.Error += fmt.Sprintf("Repair failed: %v; ", repairErr)
		log.Printf("[DatabaseRecovery] bead doctor --repair failed in %s: %v", workspaceName, repairErr)

		// Step 4: If repair fails, attempt checkpoint rebuild
		log.Printf("[DatabaseRecovery] Attempting checkpoint rebuild for %s", workspaceName)
		if rebuildErr := d.runCheckpointRebuild(ctx, workspacePath); rebuildErr != nil {
			result.Error += fmt.Sprintf("Checkpoint rebuild failed: %v", rebuildErr)
			result.RecoveryMethod = "none"
			result.Success = false
			d.logRecovery(result)
			return result
		}
		result.RecoveryMethod = "checkpoint-rebuild"
	} else {
		result.RecoveryMethod = "doctor-repair"
	}

	// Step 5: Verify recovery by re-running both CLI and SQLite queries
	cliCountAfter, dbCountAfter, verifyErr := d.detectAnomalyPattern(ctx, workspacePath)
	if verifyErr != nil {
		result.Error += fmt.Sprintf("Verification failed: %v", verifyErr)
		result.Success = false
		d.logRecovery(result)
		return result
	}

	// Run diagnostics after recovery to verify health
	diagnosticOutputAfter, dbHealthyAfter, _ := d.runBeadDoctorDiagnostics(ctx, workspacePath)
	result.DiagnosticsAfter = diagnosticOutputAfter

	// Step 6: Check if recovery succeeded
	if dbCountAfter > 0 && dbHealthyAfter {
		result.Success = true
		log.Printf("[DatabaseRecovery] ✓ Recovery succeeded in %s (CLI=%d, DB=%d)", workspaceName, cliCountAfter, dbCountAfter)

		// Step 7: Close any starvation alerts for this workspace
		alertsClosed := d.closeStarvationAlerts(ctx, workspacePath)
		result.AlertsClosed = alertsClosed
		if len(alertsClosed) > 0 {
			log.Printf("[DatabaseRecovery] Closed %d starvation alerts in %s", len(alertsClosed), workspaceName)
		}

		// Step 8: Create a diagnostic bead recording the recovery
		beadID, err := d.createDiagnosticBead(ctx, workspacePath, result)
		if err != nil {
			log.Printf("[DatabaseRecovery] Failed to create diagnostic bead in %s: %v", workspaceName, err)
		} else {
			result.BeadsCreated = []string{beadID}
			log.Printf("[DatabaseRecovery] ✓ Created diagnostic bead %s in %s", beadID, workspaceName)
		}
	} else {
		result.Success = false
		result.Error += fmt.Sprintf("Recovery incomplete: CLI=%d, DB=%d after recovery", cliCountAfter, dbCountAfter)
		log.Printf("[DatabaseRecovery] Recovery incomplete in %s (CLI=%d, DB=%d)", workspaceName, cliCountAfter, dbCountAfter)
	}

	d.logRecovery(result)
	return result
}

// detectAnomalyPattern detects the CLI-vs-DB discrepancy pattern.
// Returns (cliCount, dbCount, error).
func (d *DatabaseCorruptionRecoveryDaemon) detectAnomalyPattern(ctx context.Context, workspacePath string) (int, int, error) {
	// Count open beads via CLI
	cliCount, err := d.countBeadsByStatus(ctx, workspacePath, "open")
	if err != nil {
		return 0, 0, fmt.Errorf("count CLI open beads: %w", err)
	}

	// Count open beads via direct SQLite query
	dbCount, err := d.countOpenBeadsDirectDB(ctx, workspacePath)
	if err != nil {
		return cliCount, 0, fmt.Errorf("count DB open beads: %w", err)
	}

	return cliCount, dbCount, nil
}

// countBeadsByStatus counts beads with a given status via CLI.
func (d *DatabaseCorruptionRecoveryDaemon) countBeadsByStatus(ctx context.Context, workspacePath string, status string) (int, error) {
	cmd := exec.CommandContext(ctx, "bead", "list", "--status", status, "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("bead list --status %s: %w", status, err)
	}

	// Parse JSON output and count beads
	var beads []map[string]interface{}
	if err := json.Unmarshal(output, &beads); err != nil {
		// Fallback: count by parsing ID occurrences
		count := strings.Count(string(output), `"id":`)
		return count, nil
	}

	return len(beads), nil
}

// countOpenBeadsDirectDB counts open beads via direct SQLite query.
func (d *DatabaseCorruptionRecoveryDaemon) countOpenBeadsDirectDB(ctx context.Context, workspacePath string) (int, error) {
	dbPath := filepath.Join(workspacePath, ".beads", "beads.db")
	if _, err := os.Stat(dbPath); err != nil {
		return 0, fmt.Errorf("database not found: %s", dbPath)
	}

	// Direct database query: SELECT COUNT(*) FROM issues WHERE status = 'open'
	// Status enum: 0=open, 1=in_progress, 2=deferred, 3=closed
	query := `SELECT COUNT(*) FROM issues WHERE status = 0`
	cmd := exec.CommandContext(ctx, "sqlite3", dbPath, query)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("sqlite3 query failed: %w", err)
	}

	// Parse the count from output
	countStr := strings.TrimSpace(string(output))
	if countStr == "" {
		return 0, nil
	}

	var count int
	if _, err := fmt.Sscanf(countStr, "%d", &count); err != nil {
		return 0, fmt.Errorf("parse count from sqlite output: %w", err)
	}

	return count, nil
}

// runBeadDoctorDiagnostics executes `bead doctor` in read-only mode.
func (d *DatabaseCorruptionRecoveryDaemon) runBeadDoctorDiagnostics(ctx context.Context, workspacePath string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "bead", "doctor", "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		return string(output), false, fmt.Errorf("%w: %s", err, string(output))
	}

	// Parse JSON output to determine if database is healthy
	var diagnostics struct {
		Healthy bool     `json:"healthy"`
		Issues  []string `json:"issues"`
	}

	if err := json.Unmarshal(output, &diagnostics); err != nil {
		// If we can't parse JSON, check output content
		outputStr := string(output)
		healthy := !strings.Contains(strings.ToLower(outputStr), "error") &&
		            !strings.Contains(strings.ToLower(outputStr), "corruption") &&
		            !strings.Contains(strings.ToLower(outputStr), "missing")
		return outputStr, healthy, nil
	}

	healthy := diagnostics.Healthy || len(diagnostics.Issues) == 0
	return string(output), healthy, nil
}

// runBeadDoctorRepair executes `bead doctor --repair`.
func (d *DatabaseCorruptionRecoveryDaemon) runBeadDoctorRepair(ctx context.Context, workspacePath string) error {
	cmd := exec.CommandContext(ctx, "bead", "doctor", "--repair")
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}

	log.Printf("[DatabaseRecovery] bead doctor --repair completed in %s", filepath.Base(workspacePath))
	return nil
}

// runCheckpointRebuild rebuilds the database from checkpoint.
func (d *DatabaseCorruptionRecoveryDaemon) runCheckpointRebuild(ctx context.Context, workspacePath string) error {
	workspaceName := filepath.Base(workspacePath)

	// Backup existing database
	dbPath := filepath.Join(workspacePath, ".beads", "beads.db")
	backupPath := dbPath + ".backup"
	if _, err := os.Stat(dbPath); err == nil {
		if err := os.Rename(dbPath, backupPath); err != nil {
			return fmt.Errorf("backup database: %w", err)
		}
		log.Printf("[DatabaseRecovery] Backed up database to %s", backupPath)
	}

	// Reinitialize database
	initCmd := exec.CommandContext(ctx, "bead", "init")
	initCmd.Dir = workspacePath
	if output, err := initCmd.CombinedOutput(); err != nil {
		// Restore backup on failure
		if _, statErr := os.Stat(backupPath); statErr == nil {
			os.Rename(backupPath, dbPath)
		}
		return fmt.Errorf("bead init failed: %w: %s", err, string(output))
	}

	// Import from checkpoint
	checkpointPath := filepath.Join(workspacePath, ".beads", "checkpoint", "forensic.jsonl")
	importCmd := exec.CommandContext(ctx, "bead", "sync", "import-only",
		"--input", checkpointPath,
		"--restore-into-empty",
		"--actor", "system-recovery")
	importCmd.Dir = workspacePath
	if output, err := importCmd.CombinedOutput(); err != nil {
		// Restore backup on failure
		if _, statErr := os.Stat(backupPath); statErr == nil {
			os.Rename(backupPath, dbPath)
		}
		return fmt.Errorf("bead sync import-only failed: %w: %s", err, string(output))
	}

	// Remove backup on success
	if _, err := os.Stat(backupPath); err == nil {
		os.Remove(backupPath)
	}

	log.Printf("[DatabaseRecovery] ✓ Checkpoint rebuild completed in %s", workspaceName)
	return nil
}

// closeStarvationAlerts closes any starvation-alert beads in the workspace with reason 'database-corruption-recovered'.
func (d *DatabaseCorruptionRecoveryDaemon) closeStarvationAlerts(ctx context.Context, workspacePath string) []string {
	var closedAlerts []string

	// Find all open starvation-alert beads
	cmd := exec.CommandContext(ctx, "bead", "list", "--status", "open", "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		log.Printf("[DatabaseRecovery] Failed to list beads for closing alerts: %v", err)
		return closedAlerts
	}

	var beads []map[string]interface{}
	if err := json.Unmarshal(output, &beads); err != nil {
		return closedAlerts
	}

	// Close starvation-alert beads
	for _, bead := range beads {
		beadID, ok := bead["id"].(string)
		if !ok {
			continue
		}

		title, _ := bead["title"].(string)
		labels := extractBeadLabels(bead)

		// Check if this is a starvation alert bead
		isStarvationAlert := false
		for _, label := range labels {
			if label == "starvation-alert" {
				isStarvationAlert = true
				break
			}
		}

		if !isStarvationAlert {
			continue
		}

		// Close the bead with specific reason
		closeCmd := exec.CommandContext(ctx, "bead", "close", beadID,
			"--reason", "database-corruption-recovered: Database corruption detected and recovered via checkpoint rebuild. CLI-vs-DB anomaly resolved.")
		closeCmd.Dir = workspacePath

		if output, err := closeCmd.CombinedOutput(); err != nil {
			log.Printf("[DatabaseRecovery] Failed to close alert %s: %v (output: %s)", beadID, err, string(output))
			continue
		}

		closedAlerts = append(closedAlerts, beadID)
		log.Printf("[DatabaseRecovery] ✓ Closed starvation alert %s: %s", beadID, title)
	}

	return closedAlerts
}

// createDiagnosticBead creates a diagnostic bead recording the recovery action.
func (d *DatabaseCorruptionRecoveryDaemon) createDiagnosticBead(ctx context.Context, workspacePath string, result *DatabaseRecoveryResult) (string, error) {
	timestamp := time.Now().Format(time.RFC3339)
	title := fmt.Sprintf("Database Corruption Recovery - %s", filepath.Base(workspacePath))

	description := fmt.Sprintf(`## Database Corruption Recovery

**Timestamp:** %s
**Workspace:** %s
**Recovery Method:** %s

### Detection Anomaly
- CLI reported open beads: %d
- Database reported open beads: %d

### Recovery Action
%s

### Diagnostics (Before)
%s

### Diagnostics (After)
%s

### Outcome
- **Success:** %v
- **Alerts Closed:** %d
- **Beads Created:** %d

This diagnostic bead was automatically created by the database corruption recovery daemon.
`,
		timestamp,
		result.Workspace,
		result.RecoveryMethod,
		result.CliOpenCount,
		result.DbOpenCount,
		func() string {
			if result.Success {
				return "✓ Database corruption successfully recovered via checkpoint rebuild."
			}
			return "✗ Recovery incomplete - manual intervention may be required."
		}(),
		result.DiagnosticsBefore,
		result.DiagnosticsAfter,
		result.Success,
		len(result.AlertsClosed),
		len(result.BeadsCreated),
	)

	labels := []string{
		"diagnostic",
		"infrastructure",
		"database-recovery",
		"automated-recovery",
	}

	cmd := exec.CommandContext(ctx, "bead", "create",
		"--title", title,
		"--priority", "2",
		"--issue-type", "task",
		"--notes", description,
		"--status", "closed")
	for _, label := range labels {
		cmd = exec.CommandContext(ctx, cmd.Args[0], append(cmd.Args[1:], "--label", label)...)
	}
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("bead create: %w: %s", err, string(output))
	}

	// Extract bead ID from output
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "Created") || strings.Contains(line, "bead") {
			// Extract ID from line like "Created bead abc123"
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "bead" && i+1 < len(parts) {
					return parts[i+1], nil
				}
			}
		}
	}

	return "", fmt.Errorf("could not extract bead ID from output: %s", string(output))
}

// logRecovery writes a recovery entry to the diagnostic log.
func (d *DatabaseCorruptionRecoveryDaemon) logRecovery(result *DatabaseRecoveryResult) {
	// Append to log file
	f, err := os.OpenFile(d.diagnosticLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[DatabaseRecovery] Failed to open diagnostic log: %v", err)
		return
	}
	defer f.Close()

	logEntry := fmt.Sprintf("[%s] %s: detected=%v cli=%d db=%d method=%s success=%v alerts_closed=%d beads_created=%d\n",
		result.Timestamp.Format(time.RFC3339),
		result.Workspace,
		result.Detected,
		result.CliOpenCount,
		result.DbOpenCount,
		result.RecoveryMethod,
		result.Success,
		len(result.AlertsClosed),
		len(result.BeadsCreated))

	if _, err := f.WriteString(logEntry); err != nil {
		log.Printf("[DatabaseRecovery] Failed to write to diagnostic log: %v", err)
	}
}

// saveState persists the daemon state to disk.
func (d *DatabaseCorruptionRecoveryDaemon) saveState() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	state := DatabaseRecoveryState{
		LastCheckTime:      d.lastCheckTime,
		TotalRecoveries:    d.recoveryCount,
		WorkspaceRecoveries: make(map[string]int),
		LastRecoveries:     make([]DatabaseRecoverySummary, 0, 100),
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	if err := os.WriteFile(d.statePath, data, 0644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}

	return nil
}

// Stop stops the database corruption recovery daemon.
func (d *DatabaseCorruptionRecoveryDaemon) Stop() {
	if d == nil {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.stopped {
		return
	}

	d.stopped = true
	close(d.stopCh)

	// Release lease leadership if configured
	if d.leaseLeader != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.leaseLeader.Release(ctx)
	}

	// Save final state
	if err := d.saveState(); err != nil {
		log.Printf("[DatabaseRecovery] Failed to save final state: %v", err)
	}

	log.Printf("[DatabaseRecovery] Database corruption recovery daemon stopped")
}

// IsRunning reports whether the daemon is currently running.
func (d *DatabaseCorruptionRecoveryDaemon) IsRunning() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return !d.stopped && (d.leaseLeader == nil || d.leaseLeader.IsLeader())
}

// GetRecoveryCount returns the total number of recoveries performed.
func (d *DatabaseCorruptionRecoveryDaemon) GetRecoveryCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.recoveryCount
}
