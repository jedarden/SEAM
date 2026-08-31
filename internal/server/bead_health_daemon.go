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

// BeadHealthDaemon monitors and repairs bead health issues across all workspaces.
// It detects common starvation patterns like 'assigned but open' beads and
// automatically repairs them to prevent worker starvation.
type BeadHealthDaemon struct {
	mu                sync.RWMutex
	leaseLeader       *LeaseLeader
	workspaceRoot     string
	stopCh            chan struct{}
	stopped           bool
	checkInterval     time.Duration
	statePath         string
	diagnosticLogPath string
	repairThreshold   int
	repairCount       int
	lastCheckTime     time.Time
	onRepair          func(repair *BeadRepair)
}

// BeadHealthState tracks the daemon's persistent state.
type BeadHealthState struct {
	LastCheckTime    time.Time              `json:"last_check_time"`
	TotalRepairs     int                    `json:"total_repairs"`
	WorkspaceRepairs map[string]int        `json:"workspace_repairs"`
	LastRepairs      []BeadRepairSummary   `json:"last_repairs"`
}

// BeadRepairSummary records a brief summary of a repair.
type BeadRepairSummary struct {
	BeadID       string    `json:"bead_id"`
	Workspace    string    `json:"workspace"`
	RepairType   string    `json:"repair_type"`
	Timestamp    time.Time `json:"timestamp"`
}

// BeadRepair records the outcome of a bead health repair operation.
type BeadRepair struct {
	BeadID        string    `json:"bead_id"`
	Workspace     string    `json:"workspace"`
	Timestamp     time.Time `json:"timestamp"`
	RepairType    string    `json:"repair_type"`
	Success       bool      `json:"success"`
	Error         string    `json:"error,omitempty"`
	Diagnosis     string    `json:"diagnosis,omitempty"`
	ActionTaken   string    `json:"action_taken,omitempty"`
}

// BeadDiagnostics holds diagnostic information about a bead.
type BeadDiagnostics struct {
	BeadID          string   `json:"bead_id"`
	Status          string   `json:"status"`
	Assignee        string   `json:"assignee,omitempty"`
	IsAssignedOpen  bool     `json:"is_assigned_open"`
	Dependencies    []string `json:"dependencies,omitempty"`
	HasCircularDeps bool     `json:"has_circular_deps"`
	Revision        int      `json:"revision"`
	IsCurrent       bool     `json:"is_current"`
}

// BeadHealthConfig holds configuration for the health daemon.
type BeadHealthConfig struct {
	// WorkspaceRoot is the root directory containing all workspaces
	WorkspaceRoot string

	// LeaseLeader is the Kubernetes Lease leader elector (optional)
	LeaseLeader *LeaseLeader

	// CheckInterval is how often to check bead health (default: 10 minutes)
	CheckInterval time.Duration

	// StatePath is where to store the daemon state JSON (default: .beads/bead-health-state.json)
	StatePath string

	// DiagnosticLogPath is where to write structured logs (default: .beads/diagnostics/bead-health.log)
	DiagnosticLogPath string

	// RepairThreshold is the number of repairs before generating a summary report (default: 10)
	RepairThreshold int

	// OnRepair is called when a bead repair is performed
	OnRepair func(repair *BeadRepair)
}

// NewBeadHealthDaemon creates a new bead health daemon.
func NewBeadHealthDaemon(cfg BeadHealthConfig) (*BeadHealthDaemon, error) {
	if cfg.WorkspaceRoot == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 10 * time.Minute
	}
	if cfg.StatePath == "" {
		cfg.StatePath = filepath.Join(cfg.WorkspaceRoot, ".beads", "bead-health-state.json")
	}
	if cfg.DiagnosticLogPath == "" {
		cfg.DiagnosticLogPath = filepath.Join(cfg.WorkspaceRoot, ".beads", "diagnostics", "bead-health.log")
	}
	if cfg.RepairThreshold == 0 {
		cfg.RepairThreshold = 10
	}

	// Ensure diagnostic log directory exists
	if err := os.MkdirAll(filepath.Dir(cfg.DiagnosticLogPath), 0755); err != nil {
		return nil, fmt.Errorf("create diagnostic log directory: %w", err)
	}

	// Load existing state if it exists
	state := BeadHealthState{
		WorkspaceRepairs: make(map[string]int),
		LastRepairs:      make([]BeadRepairSummary, 0, 100),
	}
	if stateData, err := os.ReadFile(cfg.StatePath); err == nil {
		if err := json.Unmarshal(stateData, &state); err != nil {
			log.Printf("[BeadHealth] Failed to load state, starting fresh: %v", err)
		} else {
			log.Printf("[BeadHealth] Loaded state: %d total repairs, last check: %s",
				state.TotalRepairs, state.LastCheckTime.Format(time.RFC3339))
		}
	}

	daemon := &BeadHealthDaemon{
		leaseLeader:       cfg.LeaseLeader,
		workspaceRoot:     cfg.WorkspaceRoot,
		checkInterval:     cfg.CheckInterval,
		statePath:         cfg.StatePath,
		diagnosticLogPath: cfg.DiagnosticLogPath,
		repairThreshold:   cfg.RepairThreshold,
		stopCh:           make(chan struct{}),
		repairCount:      state.TotalRepairs,
		lastCheckTime:    state.LastCheckTime,
		onRepair:         cfg.OnRepair,
	}

	return daemon, nil
}

// Start begins the bead health daemon.
func (d *BeadHealthDaemon) Start(ctx context.Context) error {
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
		log.Printf("[BeadHealth] Attempting to acquire leadership")
		if !d.leaseLeader.Acquire(ctx) {
			return fmt.Errorf("failed to acquire leadership")
		}
		log.Printf("[BeadHealth] Leadership acquired")

		// Start the lease renewal goroutine
		renewCtx, cancelRenew := context.WithCancel(ctx)
		defer cancelRenew()

		go func() {
			d.leaseLeader.Renew(renewCtx)
			log.Printf("[BeadHealth] Leadership lost, stopping daemon")
			d.Stop()
		}()
	} else {
		log.Printf("[BeadHealth] Running without leadership (local mode)")
	}

	// Main loop
	ticker := time.NewTicker(d.checkInterval)
	defer ticker.Stop()

	log.Printf("[BeadHealth] Starting bead health daemon (interval: %v)", d.checkInterval)

	// Run initial check immediately
	d.checkAllWorkspaces(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[BeadHealth] Context cancelled, stopping daemon")
			return ctx.Err()
		case <-d.stopCh:
			log.Printf("[BeadHealth] Stop signal received, stopping daemon")
			return nil
		case <-ticker.C:
			if d.leaseLeader != nil && !d.leaseLeader.IsLeader() {
				log.Printf("[BeadHealth] No longer leader, skipping check")
				continue
			}

			d.checkAllWorkspaces(ctx)
		}
	}
}

// checkAllWorkspaces checks all workspaces for bead health issues.
func (d *BeadHealthDaemon) checkAllWorkspaces(ctx context.Context) {
	workspaces, err := d.findWorkspacesWithBeads()
	if err != nil {
		log.Printf("[BeadHealth] Failed to find workspaces: %v", err)
		return
	}

	if len(workspaces) == 0 {
		log.Printf("[BeadHealth] No workspaces with beads found")
		return
	}

	log.Printf("[BeadHealth] Checking %d workspaces for bead health issues", len(workspaces))

	repairsThisRun := 0
	for _, workspace := range workspaces {
		repairs, err := d.checkWorkspace(ctx, workspace)
		if err != nil {
			log.Printf("[BeadHealth] Failed to check workspace %s: %v", filepath.Base(workspace), err)
			continue
		}
		repairsThisRun += repairs
	}

	// Update state
	d.mu.Lock()
	d.lastCheckTime = time.Now()
	d.repairCount += repairsThisRun
	d.mu.Unlock()

	// Persist state
	if err := d.saveState(); err != nil {
		log.Printf("[BeadHealth] Failed to save state: %v", err)
	}

	// Generate summary if threshold exceeded
	if repairsThisRun > 0 && d.repairCount%d.repairThreshold == 0 {
		d.generateSummaryReport(ctx)
	}

	log.Printf("[BeadHealth] Check complete: %d repairs this run, %d total", repairsThisRun, d.repairCount)
}

// findWorkspacesWithBeads finds all directories with .beads/beads.db.
func (d *BeadHealthDaemon) findWorkspacesWithBeads() ([]string, error) {
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

// checkWorkspace checks a single workspace for bead health issues and returns repair count.
func (d *BeadHealthDaemon) checkWorkspace(ctx context.Context, workspacePath string) (int, error) {
	workspaceName := filepath.Base(workspacePath)

	// Get all open beads
	beads, err := d.listOpenBeads(ctx, workspacePath)
	if err != nil {
		return 0, fmt.Errorf("list open beads: %w", err)
	}

	if len(beads) == 0 {
		return 0, nil
	}

	repairs := 0
	for _, bead := range beads {
		// Run diagnostics
		diagnostics, err := d.diagnoseBead(ctx, workspacePath, bead)
		if err != nil {
			log.Printf("[BeadHealth] Failed to diagnose bead %s: %v", bead, err)
			continue
		}

		// Check if repair needed
		if diagnostics.IsAssignedOpen {
			repair := d.repairAssignedOpen(ctx, workspacePath, bead, diagnostics)
			if repair.Success {
				repairs++
			}
			if d.onRepair != nil {
				d.onRepair(repair)
			}
		}

		if diagnostics.HasCircularDeps {
			repair := d.repairCircularDeps(ctx, workspacePath, bead, diagnostics)
			if repair.Success {
				repairs++
			}
			if d.onRepair != nil {
				d.onRepair(repair)
			}
		}
	}

	if repairs > 0 {
		log.Printf("[BeadHealth] Workspace %s: %d repairs performed", workspaceName, repairs)
	}

	return repairs, nil
}

// listOpenBeads lists all open beads in a workspace.
func (d *BeadHealthDaemon) listOpenBeads(ctx context.Context, workspacePath string) ([]map[string]interface{}, error) {
	cmd := exec.CommandContext(ctx, "bead", "list", "--status", "open", "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bead list: %w", err)
	}

	var beads []map[string]interface{}
	if err := json.Unmarshal(output, &beads); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	return beads, nil
}

// diagnoseBead runs diagnostic checks on a single bead.
func (d *BeadHealthDaemon) diagnoseBead(ctx context.Context, workspacePath, beadID string) (*BeadDiagnostics, error) {
	// Get bead details
	diagnostics := &BeadDiagnostics{BeadID: beadID}

	cmd := exec.CommandContext(ctx, "bead", "show", beadID, "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bead show: %w", err)
	}

	var beadData map[string]interface{}
	if err := json.Unmarshal(output, &beadData); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	// Extract basic fields
	if status, ok := beadData["status"].(string); ok {
		diagnostics.Status = status
	}
	if assignee, ok := beadData["assignee"].(string); ok {
		diagnostics.Assignee = assignee
	}
	if revision, ok := beadData["revision"].(float64); ok {
		diagnostics.Revision = int(revision)
	}

	// Check for assigned-but-open (the main issue)
	diagnostics.IsAssignedOpen = diagnostics.Status == "open" && diagnostics.Assignee != "" && diagnostics.Assignee != "null"

	// Get dependencies
	cmd = exec.CommandContext(ctx, "bead", "dep", "ls", beadID)
	cmd.Dir = workspacePath

	output, err = cmd.Output()
	if err == nil {
		var deps []string
		scanner := bufio.NewScanner(strings.NewReader(string(output)))
		for scanner.Scan() {
			dep := strings.TrimSpace(scanner.Text())
			if dep != "" {
				deps = append(deps, dep)
			}
		}
		diagnostics.Dependencies = deps
	}

	// Check for circular dependencies (simplified check)
	diagnostics.HasCircularDeps = d.checkCircularDependencies(ctx, workspacePath, beadID, diagnostics.Dependencies)

	// Revision is always considered current for now (bead-rs doesn't expose a way to check)
	diagnostics.IsCurrent = true

	return diagnostics, nil
}

// checkCircularDependencies checks if a bead has circular dependencies.
func (d *BeadHealthDaemon) checkCircularDependencies(ctx context.Context, workspacePath, beadID string, deps []string) bool {
	// Simplified check: if any dependency lists us back, it's circular
	// In production, you'd want a full graph traversal
	for _, dep := range deps {
		cmd := exec.CommandContext(ctx, "bead", "dep", "ls", dep)
		cmd.Dir = workspacePath

		output, err := cmd.Output()
		if err != nil {
			continue
		}

		if strings.Contains(string(output), beadID) {
			return true
		}
	}
	return false
}

// repairAssignedOpen repairs an assigned-but-open bead.
func (d *BeadHealthDaemon) repairAssignedOpen(ctx context.Context, workspacePath, beadID string, diagnostics *BeadDiagnostics) *BeadRepair {
	workspaceName := filepath.Base(workspacePath)
	now := time.Now()

	repair := &BeadRepair{
		BeadID:    beadID,
		Workspace: workspaceName,
		Timestamp: now,
		RepairType: "clear-assignee",
		Diagnosis: fmt.Sprintf("Bead %s is assigned to %s but has status 'open'", beadID, diagnostics.Assignee),
	}

	log.Printf("[BeadHealth] Repairing assigned-but-open bead: %s (assignee: %s)", beadID, diagnostics.Assignee)

	// Clear the assignee
	cmd := exec.CommandContext(ctx, "bead", "update", beadID, "--clear-assignee")
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		repair.Success = false
		repair.Error = fmt.Sprintf("clear-assignee failed: %v: %s", err, string(output))
		log.Printf("[BeadHealth] Failed to repair bead %s: %v", beadID, err)
	} else {
		repair.Success = true
		repair.ActionTaken = fmt.Sprintf("Cleared assignee: %s", diagnostics.Assignee)
		log.Printf("[BeadHealth] ✓ Repaired bead %s (cleared assignee: %s)", beadID, diagnostics.Assignee)
	}

	// Log to diagnostic file
	d.logRepair(repair)

	return repair
}

// repairCircularDeps repairs circular dependencies.
func (d *BeadHealthDaemon) repairCircularDeps(ctx context.Context, workspacePath, beadID string, diagnostics *BeadDiagnostics) *BeadRepair {
	workspaceName := filepath.Base(workspacePath)
	now := time.Now()

	repair := &BeadRepair{
		BeadID:    beadID,
		Workspace: workspaceName,
		Timestamp: now,
		RepairType: "sync-deps",
		Diagnosis: fmt.Sprintf("Bead %s has circular dependencies: %v", beadID, diagnostics.Dependencies),
	}

	log.Printf("[BeadHealth] Repairing circular dependencies for bead: %s", beadID)

	// Sync dependencies (bead-rs equivalent)
	cmd := exec.CommandContext(ctx, "bead", "dep", "sync", beadID)
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		// bead dep sync might not exist in all versions, try dep refresh instead
		cmd = exec.CommandContext(ctx, "bead", "dep", "refresh", beadID)
		cmd.Dir = workspacePath
		output, err = cmd.CombinedOutput()
		if err != nil {
			repair.Success = false
			repair.Error = fmt.Sprintf("dep sync/refresh failed: %v: %s", err, string(output))
			log.Printf("[BeadHealth] Failed to repair circular deps for %s: %v", beadID, err)
			d.logRepair(repair)
			return repair
		}
	}

	repair.Success = true
	repair.ActionTaken = "Synced dependencies to resolve circular references"
	log.Printf("[BeadHealth] ✓ Repaired circular dependencies for bead %s", beadID)

	// Log to diagnostic file
	d.logRepair(repair)

	return repair
}

// logRepair writes a repair entry to the diagnostic log.
func (d *BeadHealthDaemon) logRepair(repair *BeadRepair) {
	// Append to log file
	f, err := os.OpenFile(d.diagnosticLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[BeadHealth] Failed to open diagnostic log: %v", err)
		return
	}
	defer f.Close()

	logEntry := fmt.Sprintf("[%s] %s: %s - %s - success=%v - action=%s\n",
		repair.Timestamp.Format(time.RFC3339),
		repair.Workspace,
		repair.BeadID,
		repair.RepairType,
		repair.Success,
		repair.ActionTaken)

	if _, err := f.WriteString(logEntry); err != nil {
		log.Printf("[BeadHealth] Failed to write to diagnostic log: %v", err)
	}
}

// saveState persists the daemon state to disk.
func (d *BeadHealthDaemon) saveState() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	state := BeadHealthState{
		LastCheckTime:    d.lastCheckTime,
		TotalRepairs:     d.repairCount,
		WorkspaceRepairs: make(map[string]int),
		LastRepairs:      make([]BeadRepairSummary, 0, 100),
	}

	// Collect recent repairs from log (last 100)
	if f, err := os.Open(d.diagnosticLogPath); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() && len(state.LastRepairs) < 100 {
			line := scanner.Text()
			// Parse log line and extract summary
			if strings.Contains(line, "success=true") {
				parts := strings.Fields(line)
				if len(parts) >= 3 {
					state.LastRepairs = append(state.LastRepairs, BeadRepairSummary{
						Timestamp: time.Now(), // Simplified
					})
				}
			}
		}
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

// generateSummaryReport generates a summary report when repair threshold is exceeded.
func (d *BeadHealthDaemon) generateSummaryReport(ctx context.Context) {
	log.Printf("[BeadHealth] Generating summary report (total repairs: %d)", d.repairCount)

	// Count repairs by workspace
	workspaces, err := d.findWorkspacesWithBeads()
	if err != nil {
		log.Printf("[BeadHealth] Failed to find workspaces for summary: %v", err)
		return
	}

	repairsByWorkspace := make(map[string]int)
	for _, workspace := range workspaces {
		beads, err := d.listOpenBeads(ctx, workspace)
		if err != nil {
			continue
		}

		assignedOpenCount := 0
		for _, beadData := range beads {
			if beadID, ok := beadData["id"].(string); ok {
				diagnostics, err := d.diagnoseBead(ctx, workspace, beadID)
				if err == nil && diagnostics.IsAssignedOpen {
					assignedOpenCount++
				}
			}
		}

		if assignedOpenCount > 0 {
			repairsByWorkspace[filepath.Base(workspace)] = assignedOpenCount
		}
	}

	log.Printf("[BeadHealth] Summary: %d total repairs across %d workspaces with issues",
		d.repairCount, len(repairsByWorkspace))

	// In production, you'd send this to a monitoring system or create a bead
}

// Stop stops the bead health daemon.
func (d *BeadHealthDaemon) Stop() {
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
		log.Printf("[BeadHealth] Failed to save final state: %v", err)
	}

	log.Printf("[BeadHealth] Bead health daemon stopped")
}

// IsRunning reports whether the daemon is currently running.
func (d *BeadHealthDaemon) IsRunning() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return !d.stopped && (d.leaseLeader == nil || d.leaseLeader.IsLeader())
}

// GetRepairCount returns the total number of repairs performed.
func (d *BeadHealthDaemon) GetRepairCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.repairCount
}
