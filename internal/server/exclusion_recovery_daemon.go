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

	"github.com/ardenone/seam/internal/pluckfallback"
)

// ExclusionRecoveryDaemon processes excluded beads from ExclusionTracker reports
// and executes targeted auto-recovery based on the exclusion reason.
type ExclusionRecoveryDaemon struct {
	mu                   sync.RWMutex
	leaseLeader          *LeaseLeader
	workspaceRoot        string
	stopCh               chan struct{}
	stopped              bool
	checkInterval        time.Duration
	statePath            string
	recoveryLogPath      string
	staleWorkerThreshold time.Duration
	exclusionTracker     *pluckfallback.ExclusionTracker
	recoveryCount        int
	onRecovery           func(result *ExclusionRecovery)
	lastCheckTime        time.Time
}

// ExclusionRecovery records the outcome of an exclusion recovery operation.
type ExclusionRecovery struct {
	BeadID             string              `json:"bead_id"`
	Workspace          string              `json:"workspace"`
	Timestamp          time.Time           `json:"timestamp"`
	ExclusionReason    pluckfallback.ExclusionReason `json:"exclusion_reason"`
	RecoveryAction     string              `json:"recovery_action"`
	Success            bool                `json:"success"`
	Error              string              `json:"error,omitempty"`
	DiagnosticBeadID   string              `json:"diagnostic_bead_id,omitempty"`
	ActionTaken        string              `json:"action_taken,omitempty"`
	Details            string              `json:"details,omitempty"`
}

// ExclusionRecoveryState tracks the daemon's persistent state.
type ExclusionRecoveryState struct {
	LastCheckTime     time.Time                  `json:"last_check_time"`
	TotalRecoveries   int                        `json:"total_recoveries"`
	RecoveriesByType map[pluckfallback.ExclusionReason]int `json:"recoveries_by_type"`
	LastRecoveries    []ExclusionRecoverySummary `json:"last_recoveries"`
}

// ExclusionRecoverySummary records a brief summary of a recovery.
type ExclusionRecoverySummary struct {
	BeadID          string              `json:"bead_id"`
	Workspace       string              `json:"workspace"`
	ExclusionReason pluckfallback.ExclusionReason `json:"exclusion_reason"`
	RecoveryAction  string              `json:"recovery_action"`
	Timestamp       time.Time           `json:"timestamp"`
	Success         bool                `json:"success"`
}

// ExclusionRecoveryConfig holds configuration for the exclusion recovery daemon.
type ExclusionRecoveryConfig struct {
	// WorkspaceRoot is the root directory containing all workspaces
	WorkspaceRoot string

	// LeaseLeader is the Kubernetes Lease leader elector (optional)
	LeaseLeader *LeaseLeader

	// CheckInterval is how often to check for exclusions (default: 10 minutes)
	CheckInterval time.Duration

	// StatePath is where to store the daemon state JSON (default: .beads/exclusion-recovery-state.json)
	StatePath string

	// RecoveryLogPath is where to write structured logs (default: .beads/diagnostics/exclusion-recovery.log)
	RecoveryLogPath string

	// StaleWorkerThreshold is how long a worker can be inactive before its assignee is cleared (default: 30 minutes)
	StaleWorkerThreshold time.Duration

	// OnRecovery is called when a recovery operation is performed
	OnRecovery func(result *ExclusionRecovery)
}

// NewExclusionRecoveryDaemon creates a new exclusion recovery daemon.
func NewExclusionRecoveryDaemon(cfg ExclusionRecoveryConfig) (*ExclusionRecoveryDaemon, error) {
	if cfg.WorkspaceRoot == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 10 * time.Minute
	}
	if cfg.StatePath == "" {
		cfg.StatePath = filepath.Join(cfg.WorkspaceRoot, ".beads", "exclusion-recovery-state.json")
	}
	if cfg.RecoveryLogPath == "" {
		cfg.RecoveryLogPath = filepath.Join(cfg.WorkspaceRoot, ".beads", "diagnostics", "exclusion-recovery.log")
	}
	if cfg.StaleWorkerThreshold == 0 {
		cfg.StaleWorkerThreshold = 30 * time.Minute
	}

	// Ensure diagnostic log directory exists
	if err := os.MkdirAll(filepath.Dir(cfg.RecoveryLogPath), 0755); err != nil {
		return nil, fmt.Errorf("create diagnostic log directory: %w", err)
	}

	// Create exclusion tracker for reading reports
	logPath := filepath.Join(cfg.WorkspaceRoot, ".beads", "diagnostics", "exclusion-tracker-reports.jsonl")
	tracker, err := pluckfallback.NewExclusionTracker(logPath, true, cfg.WorkspaceRoot, cfg.StaleWorkerThreshold)
	if err != nil {
		return nil, fmt.Errorf("create exclusion tracker: %w", err)
	}

	// Load existing state if it exists
	state := ExclusionRecoveryState{
		RecoveriesByType: make(map[pluckfallback.ExclusionReason]int),
		LastRecoveries:    make([]ExclusionRecoverySummary, 0, 100),
	}
	if stateData, err := os.ReadFile(cfg.StatePath); err == nil {
		if err := json.Unmarshal(stateData, &state); err != nil {
			log.Printf("[ExclusionRecovery] Failed to load state, starting fresh: %v", err)
		} else {
			log.Printf("[ExclusionRecovery] Loaded state: %d total recoveries, last check: %s",
				state.TotalRecoveries, state.LastCheckTime.Format(time.RFC3339))
		}
	}

	daemon := &ExclusionRecoveryDaemon{
		leaseLeader:          cfg.LeaseLeader,
		workspaceRoot:        cfg.WorkspaceRoot,
		checkInterval:        cfg.CheckInterval,
		statePath:            cfg.StatePath,
		recoveryLogPath:      cfg.RecoveryLogPath,
		staleWorkerThreshold: cfg.StaleWorkerThreshold,
		exclusionTracker:     tracker,
		stopCh:               make(chan struct{}),
		recoveryCount:        state.TotalRecoveries,
		lastCheckTime:        state.LastCheckTime,
		onRecovery:           cfg.OnRecovery,
	}

	return daemon, nil
}

// Start begins the exclusion recovery daemon.
func (d *ExclusionRecoveryDaemon) Start(ctx context.Context) error {
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
		log.Printf("[ExclusionRecovery] Attempting to acquire leadership")
		if !d.leaseLeader.Acquire(ctx) {
			return fmt.Errorf("failed to acquire leadership")
		}
		log.Printf("[ExclusionRecovery] Leadership acquired")

		// Start the lease renewal goroutine
		renewCtx, cancelRenew := context.WithCancel(ctx)
		defer cancelRenew()

		go func() {
			d.leaseLeader.Renew(renewCtx)
			log.Printf("[ExclusionRecovery] Leadership lost, stopping daemon")
			d.Stop()
		}()
	} else {
		log.Printf("[ExclusionRecovery] Running without leadership (local mode)")
	}

	// Main loop
	ticker := time.NewTicker(d.checkInterval)
	defer ticker.Stop()

	log.Printf("[ExclusionRecovery] Starting exclusion recovery daemon (interval: %v)", d.checkInterval)

	// Run initial check immediately
	d.processAllExclusionReports(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[ExclusionRecovery] Context cancelled, stopping daemon")
			return ctx.Err()
		case <-d.stopCh:
			log.Printf("[ExclusionRecovery] Stop signal received, stopping daemon")
			return nil
		case <-ticker.C:
			if d.leaseLeader != nil && !d.leaseLeader.IsLeader() {
				log.Printf("[ExclusionRecovery] No longer leader, skipping check")
				continue
			}

			d.processAllExclusionReports(ctx)
		}
	}
}

// processAllExclusionReports processes the latest exclusion reports for all workspaces.
func (d *ExclusionRecoveryDaemon) processAllExclusionReports(ctx context.Context) {
	workspaces, err := d.findWorkspacesWithBeads()
	if err != nil {
		log.Printf("[ExclusionRecovery] Failed to find workspaces: %v", err)
		return
	}

	if len(workspaces) == 0 {
		log.Printf("[ExclusionRecovery] No workspaces with beads found")
		return
	}

	log.Printf("[ExclusionRecovery] Checking %d workspaces for excluded beads requiring recovery", len(workspaces))

	recoveriesThisRun := 0
	for _, workspace := range workspaces {
		// Generate a fresh exclusion report for this workspace
		report, err := d.exclusionTracker.TrackExclusions(ctx, workspace)
		if err != nil {
			log.Printf("[ExclusionRecovery] Failed to get exclusion report for %s: %v", filepath.Base(workspace), err)
			continue
		}

		// Process each excluded bead
		for _, exclusion := range report.ExcludedBeads {
			recovery, err := d.executeExclusionRecovery(ctx, workspace, exclusion)
			if err != nil {
				log.Printf("[ExclusionRecovery] Failed to execute recovery for %s: %v", exclusion.BeadID, err)
				continue
			}

			if recovery != nil {
				recoveriesThisRun++
				d.logRecovery(recovery)
				if d.onRecovery != nil {
					d.onRecovery(recovery)
				}
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
		log.Printf("[ExclusionRecovery] Failed to save state: %v", err)
	}

	log.Printf("[ExclusionRecovery] Check complete: %d recoveries this run, %d total", recoveriesThisRun, d.recoveryCount)
}

// findWorkspacesWithBeads finds all directories with .beads/beads.db.
func (d *ExclusionRecoveryDaemon) findWorkspacesWithBeads() ([]string, error) {
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

// executeExclusionRecovery executes the appropriate recovery action based on exclusion reason.
func (d *ExclusionRecoveryDaemon) executeExclusionRecovery(ctx context.Context, workspacePath string, exclusion pluckfallback.BeadExclusion) (*ExclusionRecovery, error) {
	workspaceName := filepath.Base(workspacePath)
	now := time.Now()

	recovery := &ExclusionRecovery{
		BeadID:          exclusion.BeadID,
		Workspace:       workspaceName,
		Timestamp:       now,
		ExclusionReason: exclusion.Reason,
	}

	log.Printf("[ExclusionRecovery] Processing bead %s (exclusion: %s)", exclusion.BeadID, exclusion.Reason)

	switch exclusion.Reason {
	case pluckfallback.ExclusionStaleAssignee:
		// Auto-clear assignee with worker liveness check
		return d.recoverStaleAssignee(ctx, workspacePath, exclusion, recovery)

	case pluckfallback.ExclusionBlockedByDeps:
		// Check if deps are actually closed and update dependency graph
		return d.recoverBlockedByDeps(ctx, workspacePath, exclusion, recovery)

	case pluckfallback.ExclusionDependencyLoop:
		// Auto-break loops by removing the newest dependency in the cycle
		return d.recoverDependencyLoop(ctx, workspacePath, exclusion, recovery)

	case pluckfallback.ExclusionLabelsExclude:
		// Remove stale 'no-auto-claim' or 'manual-only' labels if bead is >24 hours old
		return d.recoverExclusionaryLabels(ctx, workspacePath, exclusion, recovery)

	case pluckfallback.ExclusionDatabaseCorruption:
		// Trigger checkpoint rebuild
		return d.recoverDatabaseCorruption(ctx, workspacePath, exclusion, recovery)

	case pluckfallback.ExclusionStaleRevision:
		// Run bead doctor --repair
		return d.recoverStaleRevision(ctx, workspacePath, exclusion, recovery)

	case pluckfallback.ExclusionActiveAssignee:
		// Worker is still active - no auto-recovery needed, but log it
		log.Printf("[ExclusionRecovery] Skipping %s - worker %s is still active", exclusion.BeadID, exclusion.Assignee)
		return nil, nil

	case pluckfallback.ExclusionStatusNotOpen, pluckfallback.ExclusionOther:
		// These are not auto-recoverable
		log.Printf("[ExclusionRecovery] Skipping %s - exclusion reason %s is not auto-recoverable", exclusion.BeadID, exclusion.Reason)
		return nil, nil

	default:
		return nil, fmt.Errorf("unknown exclusion reason: %s", exclusion.Reason)
	}
}

// recoverStaleAssignee clears a stale assignee after verifying worker is inactive.
func (d *ExclusionRecoveryDaemon) recoverStaleAssignee(ctx context.Context, workspacePath string, exclusion pluckfallback.BeadExclusion, recovery *ExclusionRecovery) (*ExclusionRecovery, error) {
	recovery.RecoveryAction = "clear-stale-assignee"

	log.Printf("[ExclusionRecovery] Recovering stale assignee for %s (assignee: %s)", exclusion.BeadID, exclusion.Assignee)

	// Verify worker is stale before clearing
	isStale, inactiveDuration, err := d.isWorkerStale(exclusion.Assignee)
	if err != nil {
		recovery.Success = false
		recovery.Error = fmt.Sprintf("cannot verify worker activity: %v", err)
		return recovery, nil
	}

	if !isStale {
		recovery.Success = false
		recovery.Error = fmt.Sprintf("worker %s is still active (last heartbeat %s ago)", exclusion.Assignee, inactiveDuration)
		log.Printf("[ExclusionRecovery] Skipping %s - worker is still active", exclusion.BeadID)
		return recovery, nil
	}

	// Clear the assignee
	cmd := exec.CommandContext(ctx, "bead", "update", exclusion.BeadID, "--clear-assignee")
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		recovery.Success = false
		recovery.Error = fmt.Sprintf("clear-assignee failed: %v: %s", err, string(output))
		return recovery, nil
	}

	recovery.Success = true
	recovery.ActionTaken = fmt.Sprintf("Cleared stale assignee: %s (inactive for %s)", exclusion.Assignee, inactiveDuration)

	// Create diagnostic bead
	diagnosticBeadID, err := d.createDiagnosticBead(ctx, workspacePath, exclusion, recovery)
	if err != nil {
		log.Printf("[ExclusionRecovery] Failed to create diagnostic bead: %v", err)
	} else {
		recovery.DiagnosticBeadID = diagnosticBeadID
	}

	log.Printf("[ExclusionRecovery] ✓ Recovered %s (cleared stale assignee %s)", exclusion.BeadID, exclusion.Assignee)

	return recovery, nil
}

// recoverBlockedByDeps checks if dependencies are actually closed and updates the dependency graph.
func (d *ExclusionRecoveryDaemon) recoverBlockedByDeps(ctx context.Context, workspacePath string, exclusion pluckfallback.BeadExclusion, recovery *ExclusionRecovery) (*ExclusionRecovery, error) {
	recovery.RecoveryAction = "sync-dependencies"

	log.Printf("[ExclusionRecovery] Recovering blocked dependencies for %s", exclusion.BeadID)

	// Check each dependency to see if it's actually closed
	unclosedDeps := []string{}
	closedDeps := []string{}

	for _, dep := range exclusion.Dependencies {
		cmd := exec.CommandContext(ctx, "bead", "show", dep, "--json")
		cmd.Dir = workspacePath

		output, err := cmd.Output()
		if err != nil {
			// Bead doesn't exist - consider it unclosed
			unclosedDeps = append(unclosedDeps, dep)
			continue
		}

		var beadData map[string]interface{}
		if err := json.Unmarshal(output, &beadData); err != nil {
			unclosedDeps = append(unclosedDeps, dep)
			continue
		}

		if status, ok := beadData["status"].(string); ok {
			if status == "closed" {
				closedDeps = append(closedDeps, dep)
			} else {
				unclosedDeps = append(unclosedDeps, dep)
			}
		}
	}

	// If some deps are actually closed, refresh the dependency graph
	if len(closedDeps) > 0 {
		cmd := exec.CommandContext(ctx, "bead", "dep", "sync", exclusion.BeadID)
		cmd.Dir = workspacePath

		output, err := cmd.CombinedOutput()
		if err != nil {
			recovery.Success = false
			recovery.Error = fmt.Sprintf("dep sync failed: %v: %s", err, string(output))
			return recovery, nil
		}

		recovery.Success = true
		recovery.ActionTaken = fmt.Sprintf("Synced dependencies - found %d closed deps that were still listed: %s", len(closedDeps), strings.Join(closedDeps, ", "))
		recovery.Details = fmt.Sprintf("Unclosed deps remaining: %s", strings.Join(unclosedDeps, ", "))

		log.Printf("[ExclusionRecovery] ✓ Recovered %s (synced %d closed dependencies)", exclusion.BeadID, len(closedDeps))
	} else {
		// No discrepancy found - dependencies are correctly listed
		recovery.Success = false
		recovery.Error = fmt.Sprintf("No dependency discrepancy found - all %d deps are genuinely unclosed", len(exclusion.Dependencies))
		return recovery, nil
	}

	// Create diagnostic bead
	diagnosticBeadID, err := d.createDiagnosticBead(ctx, workspacePath, exclusion, recovery)
	if err != nil {
		log.Printf("[ExclusionRecovery] Failed to create diagnostic bead: %v", err)
	} else {
		recovery.DiagnosticBeadID = diagnosticBeadID
	}

	return recovery, nil
}

// recoverDependencyLoop breaks circular dependencies by removing the newest dependency in the cycle.
func (d *ExclusionRecoveryDaemon) recoverDependencyLoop(ctx context.Context, workspacePath string, exclusion pluckfallback.BeadExclusion, recovery *ExclusionRecovery) (*ExclusionRecovery, error) {
	recovery.RecoveryAction = "break-dependency-loop"

	log.Printf("[ExclusionRecovery] Breaking dependency loop for %s", exclusion.BeadID)

	// Find the cycle - we need to identify which dependency creates the loop
	// Simplified approach: remove the last dependency in the list (most recently added)
	if len(exclusion.Dependencies) == 0 {
		recovery.Success = false
		recovery.Error = "No dependencies to remove"
		return recovery, nil
	}

	// Remove the newest dependency (last in list)
 newestDep := exclusion.Dependencies[len(exclusion.Dependencies)-1]

	cmd := exec.CommandContext(ctx, "bead", "dep", "rm", exclusion.BeadID, newestDep)
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		recovery.Success = false
		recovery.Error = fmt.Sprintf("dep rm failed: %v: %s", err, string(output))
		return recovery, nil
	}

	recovery.Success = true
	recovery.ActionTaken = fmt.Sprintf("Removed dependency %s to break loop", newestDep)

	// Create diagnostic bead
	diagnosticBeadID, err := d.createDiagnosticBead(ctx, workspacePath, exclusion, recovery)
	if err != nil {
		log.Printf("[ExclusionRecovery] Failed to create diagnostic bead: %v", err)
	} else {
		recovery.DiagnosticBeadID = diagnosticBeadID
	}

	log.Printf("[ExclusionRecovery] ✓ Recovered %s (removed dependency %s)", exclusion.BeadID, newestDep)

	return recovery, nil
}

// recoverExclusionaryLabels removes stale exclusionary labels if the bead is old enough.
func (d *ExclusionRecoveryDaemon) recoverExclusionaryLabels(ctx context.Context, workspacePath string, exclusion pluckfallback.BeadExclusion, recovery *ExclusionRecovery) (*ExclusionRecovery, error) {
	recovery.RecoveryAction = "remove-stale-labels"

	log.Printf("[ExclusionRecovery] Checking for stale exclusionary labels on %s", exclusion.BeadID)

	// Check bead age - only remove labels if bead is >24 hours old
	beadCreatedTime := exclusion.Timestamp
	if time.Since(beadCreatedTime) < 24*time.Hour {
		recovery.Success = false
		recovery.Error = fmt.Sprintf("Bead is only %s old (threshold: 24h)", time.Since(beadCreatedTime))
		return recovery, nil
	}

	// Identify which labels to remove
	labelsToRemove := []string{}
	exclusionaryLabels := []string{"no-auto-claim", "manual-only"}

	for _, label := range exclusion.Labels {
		for _, exclusionary := range exclusionaryLabels {
			if strings.EqualFold(label, exclusionary) {
				labelsToRemove = append(labelsToRemove, label)
			}
		}
	}

	if len(labelsToRemove) == 0 {
		recovery.Success = false
		recovery.Error = "No exclusionary labels found to remove"
		return recovery, nil
	}

	// Remove each label
	for _, label := range labelsToRemove {
		cmd := exec.CommandContext(ctx, "bead", "label", "remove", exclusion.BeadID, label)
		cmd.Dir = workspacePath

		output, err := cmd.CombinedOutput()
		if err != nil {
			recovery.Success = false
			recovery.Error = fmt.Sprintf("label remove failed for %s: %v: %s", label, err, string(output))
			return recovery, nil
		}
	}

	recovery.Success = true
	recovery.ActionTaken = fmt.Sprintf("Removed stale exclusionary labels: %s", strings.Join(labelsToRemove, ", "))

	// Create diagnostic bead
	diagnosticBeadID, err := d.createDiagnosticBead(ctx, workspacePath, exclusion, recovery)
	if err != nil {
		log.Printf("[ExclusionRecovery] Failed to create diagnostic bead: %v", err)
	} else {
		recovery.DiagnosticBeadID = diagnosticBeadID
	}

	log.Printf("[ExclusionRecovery] ✓ Recovered %s (removed labels: %s)", exclusion.BeadID, strings.Join(labelsToRemove, ", "))

	return recovery, nil
}

// recoverDatabaseCorruption triggers a checkpoint rebuild.
func (d *ExclusionRecoveryDaemon) recoverDatabaseCorruption(ctx context.Context, workspacePath string, exclusion pluckfallback.BeadExclusion, recovery *ExclusionRecovery) (*ExclusionRecovery, error) {
	recovery.RecoveryAction = "checkpoint-rebuild"

	log.Printf("[ExclusionRecovery] Triggering checkpoint rebuild for %s (database corruption detected)", exclusion.BeadID)

	// Run bead init to rebuild from checkpoint
	cmd := exec.CommandContext(ctx, "bead", "init")
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		recovery.Success = false
		recovery.Error = fmt.Sprintf("bead init failed: %v: %s", err, string(output))
		return recovery, nil
	}

	// Import from checkpoint
	cmd = exec.CommandContext(ctx, "bead", "sync", "import-only", "--input", ".beads/checkpoint/forensic.jsonl", "--restore-into-empty", "--actor", "exclusion-recovery-daemon")
	cmd.Dir = workspacePath

	output, err = cmd.CombinedOutput()
	if err != nil {
		recovery.Success = false
		recovery.Error = fmt.Sprintf("checkpoint import failed: %v: %s", err, string(output))
		return recovery, nil
	}

	recovery.Success = true
	recovery.ActionTaken = "Rebuilt database from checkpoint"

	// Create diagnostic bead
	diagnosticBeadID, err := d.createDiagnosticBead(ctx, workspacePath, exclusion, recovery)
	if err != nil {
		log.Printf("[ExclusionRecovery] Failed to create diagnostic bead: %v", err)
	} else {
		recovery.DiagnosticBeadID = diagnosticBeadID
	}

	log.Printf("[ExclusionRecovery] ✓ Recovered %s (database rebuilt from checkpoint)", exclusion.BeadID)

	return recovery, nil
}

// recoverStaleRevision runs bead doctor --repair.
func (d *ExclusionRecoveryDaemon) recoverStaleRevision(ctx context.Context, workspacePath string, exclusion pluckfallback.BeadExclusion, recovery *ExclusionRecovery) (*ExclusionRecovery, error) {
	recovery.RecoveryAction = "doctor-repair"

	log.Printf("[ExclusionRecovery] Running bead doctor --repair for %s", exclusion.BeadID)

	// Run bead doctor --repair
	cmd := exec.CommandContext(ctx, "bead", "doctor", "--repair")
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		recovery.Success = false
		recovery.Error = fmt.Sprintf("bead doctor --repair failed: %v: %s", err, string(output))
		return recovery, nil
	}

	recovery.Success = true
	recovery.ActionTaken = "Ran bead doctor --repair to fix revision conflicts"

	// Create diagnostic bead
	diagnosticBeadID, err := d.createDiagnosticBead(ctx, workspacePath, exclusion, recovery)
	if err != nil {
		log.Printf("[ExclusionRecovery] Failed to create diagnostic bead: %v", err)
	} else {
		recovery.DiagnosticBeadID = diagnosticBeadID
	}

	log.Printf("[ExclusionRecovery] ✓ Recovered %s (ran bead doctor --repair)", exclusion.BeadID)

	return recovery, nil
}

// createDiagnosticBead creates a diagnostic bead for the recovery operation.
func (d *ExclusionRecoveryDaemon) createDiagnosticBead(ctx context.Context, workspacePath string, exclusion pluckfallback.BeadExclusion, recovery *ExclusionRecovery) (string, error) {
	title := fmt.Sprintf("Auto-recovery: %s for %s", recovery.RecoveryAction, exclusion.BeadID)

	description := fmt.Sprintf(
		"**Auto-Recovery Operation**\n\n"+
			"**Bead:** %s\n"+
			"**Workspace:** %s\n"+
			"**Exclusion Reason:** %s\n"+
			"**Recovery Action:** %s\n"+
			"**Timestamp:** %s\n\n"+
			"**Action Taken:** %s\n\n"+
			"**Success:** %v\n"+
			"**Error:** %s\n\n"+
			"This diagnostic bead was automatically created by the exclusion recovery daemon.",
		exclusion.BeadID, filepath.Base(workspacePath), exclusion.Reason, recovery.RecoveryAction,
		recovery.Timestamp.Format(time.RFC3339), recovery.ActionTaken, recovery.Success, recovery.Error,
	)

	cmd := exec.CommandContext(ctx, "bead", "create",
		"--title", title,
		"--priority", "2",
		"--issue-type", "task",
		"--label", "auto-recovery",
		"--label", fmt.Sprintf("recovery:%s", recovery.RecoveryAction),
		"--label", "diagnostic",
	)
	cmd.Dir = workspacePath
	cmd.Stdin = strings.NewReader(description)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("create diagnostic bead failed: %v: %s", err, string(output))
	}

	// Extract bead ID from output
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	beadID := ""
	if len(lines) > 0 {
		beadID = strings.TrimSpace(lines[len(lines)-1])
	}

	return beadID, nil
}

// isWorkerStale checks if a worker's last heartbeat exceeds the staleness threshold.
func (d *ExclusionRecoveryDaemon) isWorkerStale(worker string) (bool, time.Duration, error) {
	heartbeatPath := filepath.Join(d.workspaceRoot, ".beads", "heartbeats.jsonl")

	f, err := os.Open(heartbeatPath)
	if err != nil {
		return false, 0, fmt.Errorf("open heartbeats file: %w", err)
	}
	defer f.Close()

	var lastHeartbeat *pluckfallback.WorkerHeartbeat
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := scanner.Text()
		var hb pluckfallback.WorkerHeartbeat
		if err := json.Unmarshal([]byte(line), &hb); err != nil {
			continue
		}
		if hb.Worker == worker {
			lastHeartbeat = &hb
		}
	}

	if lastHeartbeat == nil {
		return false, 0, fmt.Errorf("no heartbeat found for worker %s", worker)
	}

	now := time.Now()
	inactiveDuration := now.Sub(lastHeartbeat.Timestamp)

	return inactiveDuration > d.staleWorkerThreshold, inactiveDuration, nil
}

// logRecovery writes a recovery entry to the diagnostic log.
func (d *ExclusionRecoveryDaemon) logRecovery(recovery *ExclusionRecovery) {
	f, err := os.OpenFile(d.recoveryLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[ExclusionRecovery] Failed to open recovery log: %v", err)
		return
	}
	defer f.Close()

	logEntry := fmt.Sprintf("[%s] %s: %s - %s - success=%v - action=%s\n",
		recovery.Timestamp.Format(time.RFC3339),
		recovery.Workspace,
		recovery.BeadID,
		recovery.ExclusionReason,
		recovery.Success,
		recovery.ActionTaken)

	if _, err := f.WriteString(logEntry); err != nil {
		log.Printf("[ExclusionRecovery] Failed to write to recovery log: %v", err)
	}
}

// saveState persists the daemon state to disk.
func (d *ExclusionRecoveryDaemon) saveState() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	state := ExclusionRecoveryState{
		LastCheckTime:     d.lastCheckTime,
		TotalRecoveries:   d.recoveryCount,
		RecoveriesByType:  make(map[pluckfallback.ExclusionReason]int),
		LastRecoveries:    make([]ExclusionRecoverySummary, 0, 100),
	}

	// Collect recent recoveries from log (last 100)
	if f, err := os.Open(d.recoveryLogPath); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() && len(state.LastRecoveries) < 100 {
			line := scanner.Text()
			// Parse log line and extract summary
			if strings.Contains(line, "success=true") {
				// Simplified parsing - in production, use full JSON structure
				state.LastRecoveries = append(state.LastRecoveries, ExclusionRecoverySummary{
					Timestamp: time.Now(),
					Success:   true,
				})
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

// Stop stops the exclusion recovery daemon.
func (d *ExclusionRecoveryDaemon) Stop() {
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

	// Close exclusion tracker
	if d.exclusionTracker != nil {
		d.exclusionTracker.Close()
	}

	// Save final state
	if err := d.saveState(); err != nil {
		log.Printf("[ExclusionRecovery] Failed to save final state: %v", err)
	}

	log.Printf("[ExclusionRecovery] Exclusion recovery daemon stopped")
}

// IsRunning reports whether the daemon is currently running.
func (d *ExclusionRecoveryDaemon) IsRunning() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return !d.stopped && (d.leaseLeader == nil || d.leaseLeader.IsLeader())
}

// GetRecoveryCount returns the total number of recoveries performed.
func (d *ExclusionRecoveryDaemon) GetRecoveryCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.recoveryCount
}
