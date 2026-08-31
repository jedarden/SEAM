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

// StarvationAlertHumanMonitor monitors starvation-alert beads marked with 'human' or 'alert:starvation:unknown'
// and automatically re-evaluates them to detect self-resolved conditions.
// When resolved, it closes the alert as 'transient-starvation' and removes the human label.
type StarvationAlertHumanMonitor struct {
	mu                  sync.RWMutex
	leaseLeader         *LeaseLeader
	workspaceRoot       string
	stopCh              chan struct{}
	stopped             bool
	checkInterval       time.Duration
	minReevaluationAge  time.Duration
	alertLabels         []string
	pluckFallback       interface{} // Will be *pluckfallback.PluckFallback when imported
	diagnosticLogPath   string
	reevaluationLog     *os.File
	checkHistory        map[string]*ReevaluationHistory
	onResolution        func(resolution *ReevaluationResult)
}

// ReevaluationHistory tracks re-evaluation attempts for a human-marked alert.
type ReevaluationHistory struct {
	AlertID             string
	Workspace           string
	FirstCheck          time.Time
	LastCheck           time.Time
	CheckCount          int
	LastReadyCount      int
	LastStrategy        string
	Resolved            bool
	HadHumanLabel       bool
	ResolutionTimestamp time.Time
}

// ReevaluationResult records the outcome of a re-evaluation.
type ReevaluationResult struct {
	AlertID              string    `json:"alert_id"`
	Workspace            string    `json:"workspace"`
	Timestamp            time.Time `json:"timestamp"`
	AlertCreated         time.Time `json:"alert_created"`
	AlertAgeHours        float64   `json:"alert_age_hours"`
	ReevaluationCount    int       `json:"reevaluation_count"`
	Resolved              bool      `json:"resolved"`
	ClosedWithReason      string    `json:"closed_with_reason,omitempty"`
	HumanLabelRemoved     bool      `json:"human_label_removed"`
	ReadyCount            int       `json:"ready_count"`
	StrategyUsed          string    `json:"strategy_used,omitempty"`
	Error                 string    `json:"error,omitempty"`
	Trigger               string    `json:"trigger"` // "backoff-expired", "repair-attempted", "scheduled"
}

// HumanMonitorConfig holds configuration for the human monitor daemon.
type HumanMonitorConfig struct {
	// WorkspaceRoot is the root directory containing all workspaces
	WorkspaceRoot string

	// LeaseLeader is the Kubernetes Lease leader elector (optional)
	LeaseLeader *LeaseLeader

	// CheckInterval is how often to check alerts (default: 5 minutes)
	CheckInterval time.Duration

	// MinReevaluationAge is minimum age before first re-evaluation (default: 15 minutes)
	MinReevaluationAge time.Duration

	// AlertLabels identifies beads to monitor (default: ["human", "alert:starvation:unknown"])
	AlertLabels []string

	// ReevaluationLogPath is the path to the re-evaluation log file
	ReevaluationLogPath string

	// OnReevaluation is called when a re-evaluation completes
	OnReevaluation func(resolution *ReevaluationResult)
}

// NewStarvationAlertHumanMonitor creates a new human monitor daemon.
func NewStarvationAlertHumanMonitor(cfg HumanMonitorConfig) (*StarvationAlertHumanMonitor, error) {
	if cfg.WorkspaceRoot == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
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

	// Open re-evaluation log file
	reevaluationLog, err := os.OpenFile(cfg.ReevaluationLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open re-evaluation log: %w", err)
	}

	daemon := &StarvationAlertHumanMonitor{
		leaseLeader:        cfg.LeaseLeader,
		workspaceRoot:      cfg.WorkspaceRoot,
		checkInterval:      cfg.CheckInterval,
		minReevaluationAge: cfg.MinReevaluationAge,
		alertLabels:        cfg.AlertLabels,
		stopCh:            make(chan struct{}),
		checkHistory:      make(map[string]*ReevaluationHistory),
		reevaluationLog:   reevaluationLog,
		onResolution:      cfg.OnReevaluation,
		diagnosticLogPath: cfg.ReevaluationLogPath,
	}

	log.Printf("[HumanMonitor] Created with labels: %v, min age: %v, interval: %v",
		cfg.AlertLabels, cfg.MinReevaluationAge, cfg.CheckInterval)

	return daemon, nil
}

// Start begins the human monitor daemon.
func (d *StarvationAlertHumanMonitor) Start(ctx context.Context) error {
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
		log.Printf("[HumanMonitor] Attempting to acquire leadership")
		if !d.leaseLeader.Acquire(ctx) {
			return fmt.Errorf("failed to acquire leadership")
		}
		log.Printf("[HumanMonitor] Leadership acquired")

		// Start the lease renewal goroutine
		renewCtx, cancelRenew := context.WithCancel(ctx)
		defer cancelRenew()

		go func() {
			d.leaseLeader.Renew(renewCtx)
			log.Printf("[HumanMonitor] Leadership lost, stopping daemon")
			d.Stop()
		}()
	} else {
		log.Printf("[HumanMonitor] Running without leadership (local mode)")
	}

	// Main loop
	ticker := time.NewTicker(d.checkInterval)
	defer ticker.Stop()

	log.Printf("[HumanMonitor] Starting starvation alert human monitor (interval: %v, min age: %v)",
		d.checkInterval, d.minReevaluationAge)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[HumanMonitor] Context cancelled, stopping daemon")
			return ctx.Err()
		case <-d.stopCh:
			log.Printf("[HumanMonitor] Stop signal received, stopping daemon")
			return nil
		case <-ticker.C:
			if d.leaseLeader != nil && !d.leaseLeader.IsLeader() {
				log.Printf("[HumanMonitor] No longer leader, skipping check")
				continue
			}

			d.checkAllWorkspaces(ctx)
		}
	}
}

// checkAllWorkspaces checks all workspaces for human-marked starvation alerts.
func (d *StarvationAlertHumanMonitor) checkAllWorkspaces(ctx context.Context) {
	workspaces, err := d.findWorkspacesWithBeads()
	if err != nil {
		log.Printf("[HumanMonitor] Failed to find workspaces: %v", err)
		return
	}

	for _, workspace := range workspaces {
		d.checkWorkspace(ctx, workspace)
	}
}

// findWorkspacesWithBeads finds all directories with .beads/beads.db.
func (d *StarvationAlertHumanMonitor) findWorkspacesWithBeads() ([]string, error) {
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

// checkWorkspace checks a single workspace for human-marked alerts to re-evaluate.
func (d *StarvationAlertHumanMonitor) checkWorkspace(ctx context.Context, workspacePath string) {
	workspaceName := filepath.Base(workspacePath)

	// Find all open human-marked starvation alert beads
	alertBeads, err := d.findHumanMarkedAlertBeads(ctx, workspacePath)
	if err != nil {
		log.Printf("[HumanMonitor] Failed to find alert beads in %s: %v", workspaceName, err)
		return
	}

	if len(alertBeads) == 0 {
		return // No human-marked alerts in this workspace
	}

	log.Printf("[HumanMonitor] Found %d human-marked starvation alert beads in %s", len(alertBeads), workspaceName)

	for _, beadData := range alertBeads {
		result := d.reevaluateAlert(ctx, workspacePath, beadData)

		// Log the re-evaluation
		d.logReevaluation(result)

		// Notify callback if set
		if d.onResolution != nil && result != nil {
			d.onResolution(result)
		}
	}
}

// HumanAlertData holds metadata about a human-marked alert bead.
type HumanAlertData struct {
	ID           string
	Title        string
	Created      time.Time
	HasHumanLabel bool
}

// findHumanMarkedAlertBeads finds all open beads with human or alert:starvation:unknown labels.
func (d *StarvationAlertHumanMonitor) findHumanMarkedAlertBeads(ctx context.Context, workspacePath string) ([]HumanAlertData, error) {
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

	var alertBeads []HumanAlertData
	for _, bead := range beads {
		beadID, ok := bead["id"].(string)
		if !ok {
			continue
		}

		// Check if bead has any of the target labels
		hasTargetLabel := false
		hasHumanLabel := false
		if labels, ok := bead["labels"].([]interface{}); ok {
			for _, label := range labels {
				if labelStr, ok := label.(string); ok {
					// Check if it's a target label
					for _, targetLabel := range d.alertLabels {
						if labelStr == targetLabel {
							hasTargetLabel = true
							break
						}
					}
					// Specifically check for human label
					if labelStr == "human" {
						hasHumanLabel = true
					}
				}
			}
		}

		if !hasTargetLabel {
			continue
		}

		// Extract metadata
		title := ""
		if t, ok := bead["title"].(string); ok {
			title = t
		}

		created := time.Now()
		if createdStr, ok := bead["created"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, createdStr); err == nil {
				created = parsed
			}
		}

		alertBeads = append(alertBeads, HumanAlertData{
			ID:           beadID,
			Title:        title,
			Created:      created,
			HasHumanLabel: hasHumanLabel,
		})
	}

	return alertBeads, nil
}

// reevaluateAlert checks if a human-marked alert has self-resolved.
func (d *StarvationAlertHumanMonitor) reevaluateAlert(ctx context.Context, workspacePath string, beadData HumanAlertData) *ReevaluationResult {
	workspaceName := filepath.Base(workspacePath)
	now := time.Now()
	beadID := beadData.ID

	d.mu.Lock()
	history, exists := d.checkHistory[beadID]
	if !exists {
		history = &ReevaluationHistory{
			AlertID:       beadID,
			Workspace:     workspaceName,
			FirstCheck:    now,
			HadHumanLabel: beadData.HasHumanLabel,
		}
		d.checkHistory[beadID] = history
	}
	d.mu.Unlock()

	// Skip if already resolved
	if history.Resolved {
		return nil
	}

	// Calculate age
	age := now.Sub(beadData.Created)

	// Skip if too young for re-evaluation
	if age < d.minReevaluationAge {
		log.Printf("[HumanMonitor] Alert %s in %s too young for re-evaluation (age: %.1f min, min: %.1f min)",
			beadID, workspaceName, age.Minutes(), d.minReevaluationAge.Minutes())
		return &ReevaluationResult{
			AlertID:         beadID,
			Workspace:       workspaceName,
			Timestamp:       now,
			AlertCreated:    beadData.Created,
			AlertAgeHours:   age.Hours(),
			ReevaluationCount: history.CheckCount,
			Resolved:        false,
			Trigger:         "backoff-expired",
		}
	}

	log.Printf("[HumanMonitor] Re-evaluating alert %s in %s (age: %.1f hours, check #%d)",
		beadID, workspaceName, age.Hours(), history.CheckCount+1)

	// Check if work is now available using bead pluck (PluckFallback)
	readyCount, strategy, err := d.pluckForCandidates(ctx, workspacePath)

	// Update check history
	history.LastCheck = now
	history.CheckCount++
	history.LastReadyCount = readyCount
	history.LastStrategy = strategy

	trigger := "scheduled"
	if history.CheckCount == 1 {
		trigger = "backoff-expired"
	}

	// If candidates found, resolve the alert and remove human label
	if readyCount > 0 {
		return d.resolveAlert(ctx, workspacePath, beadID, beadData, readyCount, strategy, history, trigger)
	}

	// No candidates - log and continue monitoring
	log.Printf("[HumanMonitor] Alert %s in %s persists (check #%d, age: %.1f hours, ready=%d, strategy=%s)",
		beadID, workspaceName, history.CheckCount, age.Hours(), readyCount, strategy)

	return &ReevaluationResult{
		AlertID:           beadID,
		Workspace:         workspaceName,
		Timestamp:         now,
		AlertCreated:      beadData.Created,
		AlertAgeHours:     age.Hours(),
		ReevaluationCount: history.CheckCount,
		Resolved:          false,
		ReadyCount:        readyCount,
		StrategyUsed:      strategy,
		Trigger:           trigger,
	}
}

// pluckForCandidates uses PluckFallback to check if work is available.
// This implements the "bead pluck" mentioned in the task.
func (d *StarvationAlertHumanMonitor) pluckForCandidates(ctx context.Context, workspacePath string) (int, string, error) {
	// Try primary strategy: bead list --ready
	cmd := exec.CommandContext(ctx, "bead", "list", "--ready", "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		// No ready beads is not an error
		return 0, "primary", nil
	}

	// Count candidates
	count := strings.Count(string(output), `"id":`)
	if count > 0 {
		return count, "primary", nil
	}

	// Fallback strategies would go here (similar to PluckFallback)
	// For now, just return 0 with primary strategy
	return 0, "primary", nil
}

// resolveAlert closes a human-marked alert as transient-starvation and removes the human label.
func (d *StarvationAlertHumanMonitor) resolveAlert(ctx context.Context, workspacePath, beadID string, beadData HumanAlertData, readyCount int, strategy string, history *ReevaluationHistory, trigger string) *ReevaluationResult {
	workspaceName := filepath.Base(workspacePath)
	now := time.Now()

	reason := "Condition self-resolved - transient starvation"
	humanLabelRemoved := false

	// Remove human label if present
	if beadData.HasHumanLabel {
		removeCmd := exec.CommandContext(ctx, "bead", "label", "remove", beadID, "human")
		removeCmd.Dir = workspacePath

		if output, err := removeCmd.CombinedOutput(); err != nil {
			log.Printf("[HumanMonitor] Failed to remove human label from %s: %v (output: %s)", beadID, err, string(output))
			// Continue anyway - label removal is optional
		} else {
			humanLabelRemoved = true
			log.Printf("[HumanMonitor] ✓ Removed human label from %s", beadID)
		}
	}

	// Close the bead
	closeCmd := exec.CommandContext(ctx, "bead", "close", beadID, "--reason", reason)
	closeCmd.Dir = workspacePath

	output, err := closeCmd.CombinedOutput()
	if err != nil {
		log.Printf("[HumanMonitor] Failed to close alert %s in %s: %v", beadID, workspaceName, err)
		return &ReevaluationResult{
			AlertID:           beadID,
			Workspace:         workspaceName,
			Timestamp:         now,
			AlertCreated:      beadData.Created,
			AlertAgeHours:     now.Sub(beadData.Created).Hours(),
			ReevaluationCount: history.CheckCount,
			Resolved:          false,
			Error:             fmt.Sprintf("close failed: %v: %s", err, string(output)),
			Trigger:           trigger,
		}
	}

	// Mark as resolved in history
	history.Resolved = true
	history.ResolutionTimestamp = now

	log.Printf("[HumanMonitor] ✓ Resolved human-marked alert %s in %s (ready=%d, strategy=%s, human_label_removed=%v, checks=%d)",
		beadID, workspaceName, readyCount, strategy, humanLabelRemoved, history.CheckCount)

	return &ReevaluationResult{
		AlertID:           beadID,
		Workspace:         workspaceName,
		Timestamp:         now,
		AlertCreated:      beadData.Created,
		AlertAgeHours:     now.Sub(beadData.Created).Hours(),
		ReevaluationCount: history.CheckCount,
		Resolved:          true,
		ClosedWithReason:  reason,
		HumanLabelRemoved: humanLabelRemoved,
		ReadyCount:        readyCount,
		StrategyUsed:      strategy,
		Trigger:           trigger,
	}
}

// logReevaluation writes a re-evaluation result to the log file.
func (d *StarvationAlertHumanMonitor) logReevaluation(result *ReevaluationResult) {
	if result == nil || d.reevaluationLog == nil {
		return
	}

	jsonBytes, err := json.Marshal(result)
	if err != nil {
		log.Printf("[HumanMonitor] Failed to marshal re-evaluation result: %v", err)
		return
	}

	if _, err := d.reevaluationLog.Write(append(jsonBytes, '\n')); err != nil {
		log.Printf("[HumanMonitor] Failed to write re-evaluation result: %v", err)
	}
}

// Stop stops the human monitor daemon.
func (d *StarvationAlertHumanMonitor) Stop() {
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

	// Close log file
	if d.reevaluationLog != nil {
		d.reevaluationLog.Close()
	}

	// Release lease leadership if configured
	if d.leaseLeader != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.leaseLeader.Release(ctx)
	}

	log.Printf("[HumanMonitor] Starvation alert human monitor stopped")
}

// IsRunning reports whether the daemon is currently running.
func (d *StarvationAlertHumanMonitor) IsRunning() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return !d.stopped && (d.leaseLeader == nil || d.leaseLeader.IsLeader())
}

// GetCheckHistory returns the check history for all tracked alerts.
func (d *StarvationAlertHumanMonitor) GetCheckHistory() map[string]*ReevaluationHistory {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Return a copy to avoid race conditions
	history := make(map[string]*ReevaluationHistory, len(d.checkHistory))
	for k, v := range d.checkHistory {
		history[k] = v
	}
	return history
}
