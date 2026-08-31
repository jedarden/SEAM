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

// StarvationDiagnosticDaemon scans for starvation-alert beads labeled 'human' or
// 'alert:starvation:unknown' and runs enhanced RootCauseAnalyzer diagnostics.
// It categorizes root causes, determines auto-recoverability, updates bead labels,
// and queues auto-repairable issues for the repair daemon.
type StarvationDiagnosticDaemon struct {
	mu               sync.RWMutex
	leaseLeader      *LeaseLeader
	stopCh           chan struct{}
	stopped          bool
	checkInterval    time.Duration
	workspaceRoot    string
	repairQueue      *RepairQueue
	analyzer         *RootCauseAnalyzer
	metrics          *Metrics
	onDiagnosticComplete func(result *DiagnosticResult)
	lastScanTime    time.Time
	lastScanResults []string // Bead IDs that were processed
}

// DiagnosticConfig holds configuration for the starvation diagnostic daemon.
type DiagnosticConfig struct {
	// WorkspaceRoot is the root directory containing all workspaces (e.g., /home/coding)
	WorkspaceRoot string

	// LeaseLeader is the Kubernetes Lease leader elector (optional)
	LeaseLeader *LeaseLeader

	// CheckInterval is how often to scan for starvation-alert beads (default: 2 minutes)
	CheckInterval time.Duration

	// RepairQueue is the repair queue for auto-repairable issues
	RepairQueue *RepairQueue

	// OnDiagnosticComplete is called when a diagnostic completes
	OnDiagnosticComplete func(result *DiagnosticResult)

	// Metrics is the Prometheus metrics publisher
	Metrics *Metrics
}

// DiagnosticResult holds the result of running diagnostics on a starvation alert.
type DiagnosticResult struct {
	BeadID         string    `json:"bead_id"`          // The starvation alert bead ID
	Workspace      string    `json:"workspace"`         // Workspace path
	RootCause      string    `json:"root_cause"`        // Categorized root cause
	AutoRecovered  bool      `json:"auto_recovered"`    // Whether auto-recovered
	Repairable     bool      `json:"repairable"`        // Whether auto-repairable
	HumanBlocked   bool      `json:"human_blocked"`     // Whether human intervention required
	LabelsAdded    []string  `json:"labels_added"`      // Labels added to the bead
	LabelsRemoved  []string  `json:"labels_removed"`    // Labels removed from the bead
	Queued         bool      `json:"queued"`            // Whether queued for repair
	Error          string    `json:"error,omitempty"`   // Error if diagnostic failed
	Timestamp      time.Time `json:"timestamp"`          // When diagnostic ran
	Diagnostics    string    `json:"diagnostics"`       // Full diagnostic output
}

// NewStarvationDiagnosticDaemon creates a new starvation diagnostic daemon.
func NewStarvationDiagnosticDaemon(cfg DiagnosticConfig) (*StarvationDiagnosticDaemon, error) {
	if cfg.WorkspaceRoot == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 2 * time.Minute
	}

	daemon := &StarvationDiagnosticDaemon{
		leaseLeader:           cfg.LeaseLeader,
		checkInterval:         cfg.CheckInterval,
		workspaceRoot:         cfg.WorkspaceRoot,
		repairQueue:          cfg.RepairQueue,
		analyzer:             NewRootCauseAnalyzer(),
		metrics:              cfg.Metrics,
		stopCh:               make(chan struct{}),
		onDiagnosticComplete: cfg.OnDiagnosticComplete,
		lastScanResults:      make([]string, 0),
	}

	log.Printf("[DiagnosticDaemon] Starvation diagnostic daemon initialized (check_interval=%v)", cfg.CheckInterval)

	return daemon, nil
}

// Start begins the diagnostic daemon loop.
func (d *StarvationDiagnosticDaemon) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return fmt.Errorf("daemon already stopped")
	}
	d.mu.Unlock()

	// Acquire leadership if configured
	if d.leaseLeader != nil {
		log.Printf("[DiagnosticDaemon] Attempting to acquire leadership")
		if !d.leaseLeader.Acquire(ctx) {
			return fmt.Errorf("failed to acquire leadership")
		}
		log.Printf("[DiagnosticDaemon] Leadership acquired")

		// Start the lease renewal goroutine
		renewCtx, cancelRenew := context.WithCancel(ctx)
		defer cancelRenew()

		go func() {
			d.leaseLeader.Renew(renewCtx)
			log.Printf("[DiagnosticDaemon] Leadership lost, stopping daemon")
			d.Stop()
		}()
	} else {
		log.Printf("[DiagnosticDaemon] Running without leadership (local mode)")
	}

	// Main daemon loop
	ticker := time.NewTicker(d.checkInterval)
	defer ticker.Stop()

	log.Printf("[DiagnosticDaemon] Starting starvation diagnostic daemon (check interval: %v)", d.checkInterval)

	// Run initial scan immediately
	d.scanAndDiagnose(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[DiagnosticDaemon] Context cancelled, stopping daemon")
			return ctx.Err()
		case <-d.stopCh:
			log.Printf("[DiagnosticDaemon] Stop signal received, stopping daemon")
			return nil
		case <-ticker.C:
			if d.leaseLeader != nil && !d.leaseLeader.IsLeader() {
				log.Printf("[DiagnosticDaemon] No longer leader, skipping check")
				continue
			}

			// Run diagnostic scan
			d.scanAndDiagnose(ctx)
		}
	}
}

// scanAndDiagnose scans for starvation-alert beads and runs diagnostics.
func (d *StarvationDiagnosticDaemon) scanAndDiagnose(ctx context.Context) {
	d.mu.Lock()
	d.lastScanTime = time.Now()
	d.lastScanResults = make([]string, 0)
	d.mu.Unlock()

	log.Printf("[DiagnosticDaemon] Starting diagnostic scan")

	// Find all workspaces
	workspaces, err := d.findWorkspaces(ctx)
	if err != nil {
		log.Printf("[DiagnosticDaemon] Failed to find workspaces: %v", err)
		return
	}

	log.Printf("[DiagnosticDaemon] Found %d workspaces to scan", len(workspaces))

	processedCount := 0
	for _, workspace := range workspaces {
		// Find starvation-alert beads in this workspace
		alertBeads, err := d.findStarvationAlertBeads(ctx, workspace)
		if err != nil {
			log.Printf("[DiagnosticDaemon] Failed to find alert beads in %s: %v", filepath.Base(workspace), err)
			continue
		}

		if len(alertBeads) == 0 {
			continue
		}

		log.Printf("[DiagnosticDaemon] Found %d starvation-alert beads in %s", len(alertBeads), filepath.Base(workspace))

		// Run diagnostics on each alert bead
		for _, bead := range alertBeads {
			result := d.diagnoseBead(ctx, workspace, bead)
			processedCount++

			d.mu.Lock()
			d.lastScanResults = append(d.lastScanResults, bead.BeadID)
			d.mu.Unlock()

			// Record metrics
			if d.metrics != nil {
				d.metrics.RecordDiagnosticRun(workspace, result.RootCause, result.Repairable)
			}

			// Notify callback
			if d.onDiagnosticComplete != nil {
				go d.onDiagnosticComplete(result)
			}
		}
	}

	log.Printf("[DiagnosticDaemon] Diagnostic scan complete: %d beads processed", processedCount)
}

// findWorkspaces finds all directories with .beads/beads.db.
func (d *StarvationDiagnosticDaemon) findWorkspaces(ctx context.Context) ([]string, error) {
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

// StarvationAlertBead represents a starvation alert bead.
type StarvationAlertBead struct {
	BeadID     string   // Bead ID
	Workspace  string   // Workspace path
	Title      string   // Bead title
	Status     string   // Bead status
	Labels     []string // Bead labels
	Priority   int      // Bead priority
}

// findStarvationAlertBeads finds beads labeled with 'human' or 'alert:starvation:unknown'.
func (d *StarvationDiagnosticDaemon) findStarvationAlertBeads(ctx context.Context, workspacePath string) ([]*StarvationAlertBead, error) {
	var alertBeads []*StarvationAlertBead

	// Get all open beads
	cmd := exec.CommandContext(ctx, "bead", "list", "--status", "open", "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bead list --status open: %w", err)
	}

	// Parse JSON output
	var beads []map[string]interface{}
	if err := json.Unmarshal(output, &beads); err != nil {
		return nil, fmt.Errorf("parse bead list JSON: %w", err)
	}

	// Filter for starvation-alert beads with 'human' or 'alert:starvation:unknown' labels
	for _, bead := range beads {
		beadID, ok := bead["id"].(string)
		if !ok {
			continue
		}

		title, _ := bead["title"].(string)
		status, _ := bead["base_status"].(string)

		// Extract labels
		labels := extractBeadLabels(bead)

		// Check if this is a starvation alert bead
		isStarvationAlert := false
		hasHumanLabel := false
		hasUnknownLabel := false

		for _, label := range labels {
			if label == "starvation-alert" {
				isStarvationAlert = true
			}
			if label == "human" {
				hasHumanLabel = true
			}
			if label == "alert:starvation:unknown" {
				hasUnknownLabel = true
			}
		}

		// Only include if it's a starvation alert AND has human or unknown label
		if isStarvationAlert && (hasHumanLabel || hasUnknownLabel) {
			priority := 2 // Default priority
			if p, ok := bead["priority"].(float64); ok {
				priority = int(p)
			}

			alertBeads = append(alertBeads, &StarvationAlertBead{
				BeadID:    beadID,
				Workspace: workspacePath,
				Title:     title,
				Status:    status,
				Labels:    labels,
				Priority:  priority,
			})
		}
	}

	return alertBeads, nil
}

// extractBeadLabels extracts labels from a bead map.
func extractBeadLabels(bead map[string]interface{}) []string {
	var labels []string

	if labelsField, ok := bead["labels"].([]interface{}); ok {
		for _, label := range labelsField {
			if labelStr, ok := label.(string); ok {
				labels = append(labels, labelStr)
			}
		}
	}

	return labels
}

// diagnoseBead runs enhanced diagnostics on a single starvation alert bead.
func (d *StarvationDiagnosticDaemon) diagnoseBead(ctx context.Context, workspacePath string, bead *StarvationAlertBead) *DiagnosticResult {
	startTime := time.Now()

	result := &DiagnosticResult{
		BeadID:    bead.BeadID,
		Workspace: workspacePath,
		Timestamp: startTime,
	}

	log.Printf("[DiagnosticDaemon] Diagnosing bead %s in %s", bead.BeadID, filepath.Base(workspacePath))

	// Get ready bead count
	readyCount, err := d.countReadyBeads(ctx, workspacePath)
	if err != nil {
		result.Error = fmt.Sprintf("Failed to count ready beads: %v", err)
		log.Printf("[DiagnosticDaemon] %s: %v", bead.BeadID, result.Error)
		return result
	}

	// Run enhanced root cause analysis
	// Use "unknown" strategy for initial diagnostic
	rootCause, autoRecovered, diagnostics := d.analyzer.Analyze(ctx, workspacePath, "unknown", readyCount, nil)

	result.RootCause = rootCause
	result.AutoRecovered = autoRecovered
	result.Diagnostics = diagnostics

	// If root cause is unknown or confidence is low, attempt cascading recovery
	if rootCause == "unknown-cause" || !autoRecovered {
		log.Printf("[DiagnosticDaemon] Unknown root cause detected, attempting cascading recovery for %s", bead.BeadID)

		cascadingResult, err := d.analyzer.AttemptCascadingRecovery(ctx, workspacePath)
		if err != nil {
			result.Error = fmt.Sprintf("Cascading recovery failed: %v", err)
			log.Printf("[DiagnosticDaemon] %s: %v", bead.BeadID, result.Error)
		} else if cascadingResult.Success {
			// Cascading recovery succeeded - update root cause and auto-recovery status
			log.Printf("[DiagnosticDaemon] ✓ Cascading recovery succeeded for %s (strategy=%s, root_cause=%s)",
				bead.BeadID, cascadingResult.SuccessfulStrategy, cascadingResult.InferredRootCause)

			result.RootCause = cascadingResult.InferredRootCause
			result.AutoRecovered = true
			result.Diagnostics += fmt.Sprintf("\n\n**Cascading Recovery Results:**\n%s", cascadingResult.Diagnostics)

			// Update ready count for verification
			if cascadingResult.ReadyAfter > 0 {
				readyCount = cascadingResult.ReadyAfter
			}
		} else {
			// All strategies failed
			log.Printf("[DiagnosticDaemon] All cascading recovery strategies failed for %s", bead.BeadID)
			result.Diagnostics += fmt.Sprintf("\n\n**Cascading Recovery Results:**\n%s", cascadingResult.Diagnostics)

			// Keep unknown-cause root cause, but mark as requiring human intervention
			result.RootCause = "unknown-cause"
			result.AutoRecovered = false
			result.HumanBlocked = true
		}
	}

	// Determine repairability and human-blocking based on (possibly updated) root cause
	result.Repairable = d.isRepairable(result.RootCause)
	result.HumanBlocked = d.isHumanBlocked(result.RootCause)

	// Update bead labels
	if err := d.updateBeadLabels(ctx, workspacePath, bead.BeadID, result, bead.Labels); err != nil {
		result.Error = fmt.Sprintf("Failed to update labels: %v", err)
		log.Printf("[DiagnosticDaemon] %s: %v", bead.BeadID, result.Error)
		return result
	}

	// Queue for repair if auto-repairable
	if result.Repairable && d.repairQueue != nil {
		if err := d.queueForRepair(ctx, workspacePath, bead.BeadID, result); err != nil {
			result.Error = fmt.Sprintf("Failed to queue for repair: %v", err)
			log.Printf("[DiagnosticDaemon] %s: %v", bead.BeadID, result.Error)
			// Don't return - label update succeeded
		} else {
			result.Queued = true
		}
	}

	log.Printf("[DiagnosticDaemon] ✓ Completed diagnostic for %s (root_cause=%s, repairable=%v, human_blocked=%v, queued=%v)",
		bead.BeadID, result.RootCause, result.Repairable, result.HumanBlocked, result.Queued)

	return result
}

// countReadyBeads counts beads ready for workers to claim.
func (d *StarvationDiagnosticDaemon) countReadyBeads(ctx context.Context, workspacePath string) (int, error) {
	cmd := exec.CommandContext(ctx, "bead", "list", "--ready", "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("bead list --ready: %w", err)
	}

	var beads []map[string]interface{}
	if err := json.Unmarshal(output, &beads); err != nil {
		return 0, fmt.Errorf("parse ready bead list: %w", err)
	}

	return len(beads), nil
}

// isRepairable determines if a root cause is auto-repairable.
func (d *StarvationDiagnosticDaemon) isRepairable(rootCause string) bool {
	repairableRootCauses := map[string]bool{
		"index-corrupt":          true,
		"database-corrupt":       true,
		"checkpoint-out-of-sync": true,
		"filter-mismatch":        true,
		"stale-assignment":       true,
		"transient-starvation":   false, // Already resolved
		"worker-stuck":           false, // Workers restart on their own
		"cli-failure":            false, // May need investigation
		"query-bug":               false, // Requires code fix
		"primary-query-failure":  false, // Requires investigation
		"unknown-cause":          false, // Needs diagnosis first
	}

	if repairable, ok := repairableRootCauses[rootCause]; ok {
		return repairable
	}

	// Default: not repairable for unknown causes
	return false
}

// isHumanBlocked determines if human intervention is required.
func (d *StarvationDiagnosticDaemon) isHumanBlocked(rootCause string) bool {
	humanBlockedRootCauses := map[string]bool{
		"index-corrupt":          false, // Auto-repairable
		"database-corrupt":       false, // Auto-repairable
		"checkpoint-out-of-sync": false, // Auto-repairable
		"filter-mismatch":        false, // Usually auto-repairable
		"stale-assignment":       false, // Auto-repairable
		"transient-starvation":   false, // Already resolved
		"worker-stuck":           false, // Workers restart automatically
		"cli-failure":            true,  // May require investigation
		"query-bug":               true,  // Requires code fix
		"primary-query-failure":  true,  // Requires investigation
		"unknown-cause":          true,  // Needs diagnosis first
	}

	if blocked, ok := humanBlockedRootCauses[rootCause]; ok {
		return blocked
	}

	// Default: human blocked for unknown causes
	return true
}

// updateBeadLabels updates bead labels with diagnostic tags.
func (d *StarvationDiagnosticDaemon) updateBeadLabels(ctx context.Context, workspacePath, beadID string, result *DiagnosticResult, currentLabels []string) error {
	// Build new label set
	newLabels := make([]string, 0)
	removedLabels := make([]string, 0)

	// Copy existing labels, removing the ones we need to remove
	for _, label := range currentLabels {
		// Remove 'human' label if auto-recoverable
		if label == "human" && !result.HumanBlocked {
			removedLabels = append(removedLabels, label)
			continue
		}

		// Remove 'alert:starvation:unknown' since we now know the cause
		if label == "alert:starvation:unknown" {
			removedLabels = append(removedLabels, label)
			continue
		}

		newLabels = append(newLabels, label)
	}

	// Add structured diagnostic tags
	diagnosticLabel := fmt.Sprintf("starvation:%s", result.RootCause)
	newLabels = append(newLabels, diagnosticLabel)
	result.LabelsAdded = []string{diagnosticLabel}

	// Add actionability labels
	if !result.HumanBlocked {
		newLabels = append(newLabels, "automated-recovery")
		result.LabelsAdded = append(result.LabelsAdded, "automated-recovery")
	} else {
		newLabels = append(newLabels, "human-intervention-required")
		result.LabelsAdded = append(result.LabelsAdded, "human-intervention-required")
	}

	if result.Repairable {
		newLabels = append(newLabels, "repair-daemon-queue")
		result.LabelsAdded = append(result.LabelsAdded, "repair-daemon-queue")
	} else {
		newLabels = append(newLabels, "monitor-only")
		result.LabelsAdded = append(result.LabelsAdded, "monitor-only")
	}

	result.LabelsRemoved = removedLabels

	// Build label update command
	args := []string{"update", beadID}
	for _, label := range newLabels {
		args = append(args, "--label", label)
	}

	cmd := exec.CommandContext(ctx, "bead", args...)
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bead update: %w (output: %s)", err, string(output))
	}

	log.Printf("[DiagnosticDaemon] Updated labels for bead %s: added=%v, removed=%v",
		beadID, result.LabelsAdded, result.LabelsRemoved)

	return nil
}

// queueForRepair queues an auto-repairable issue for the repair daemon.
func (d *StarvationDiagnosticDaemon) queueForRepair(ctx context.Context, workspacePath, beadID string, result *DiagnosticResult) error {
	// Create repair item
	item := &RepairItem{
		AlertID:   beadID,
		Workspace: workspacePath,
		RootCause: result.RootCause,
		Priority:  2, // Default to P2
		QueuedBy:  "starvation-diagnostic-daemon",
	}

	// Enqueue the item
	enqueued, existing, err := d.repairQueue.Enqueue(ctx, item)
	if err != nil {
		return fmt.Errorf("enqueue repair item: %w", err)
	}

	if !enqueued {
		log.Printf("[DiagnosticDaemon] Repair item already exists for bead %s (item_id=%s)", beadID, existing.ID)
		return nil
	}

	log.Printf("[DiagnosticDaemon] ✓ Queued bead %s for repair (item_id=%s, root_cause=%s)",
		beadID, item.ID, result.RootCause)

	return nil
}

// Stop stops the diagnostic daemon.
func (d *StarvationDiagnosticDaemon) Stop() {
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

	log.Printf("[DiagnosticDaemon] Starvation diagnostic daemon stopped")
}

// IsRunning reports whether the daemon is currently running.
func (d *StarvationDiagnosticDaemon) IsRunning() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return !d.stopped && (d.leaseLeader == nil || d.leaseLeader.IsLeader())
}

// GetLastScanInfo returns information about the last scan.
func (d *StarvationDiagnosticDaemon) GetLastScanInfo() (lastScan time.Time, processedBeads []string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastScanTime, d.lastScanResults
}

// TriggerManualScan triggers a manual diagnostic scan outside the normal schedule.
func (d *StarvationDiagnosticDaemon) TriggerManualScan(ctx context.Context) error {
	log.Printf("[DiagnosticDaemon] Manual scan triggered")

	if d.leaseLeader != nil && !d.leaseLeader.IsLeader() {
		return fmt.Errorf("not leader, cannot trigger scan")
	}

	go d.scanAndDiagnose(ctx)
	return nil
}
