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

// StarvationAlertSelfResolution monitors open starvation-alert beads and
// automatically resolves them when the underlying condition is fixed.
// It uses PluckFallback to verify work availability and tracks consecutive
// checks before escalating to human review.
type StarvationAlertSelfResolution struct {
	mu                  sync.RWMutex
	leaseLeader         *LeaseLeader
	workspaceRoot       string
	stopCh              chan struct{}
	stopped             bool
	checkInterval       time.Duration
	alertLabel          string
	pluckFallback       *pluckfallback.PluckFallback
	diagnosticLogPath   string
	maxConsecutiveChecks int // Number of checks before escalation (3)
	checkHistory        map[string]*CheckHistory // bead ID -> check history
	onResolution        func(resolution *AlertResolution)
	rootCauseAnalyzer   *RootCauseAnalyzer // New: root cause analysis
}

// CheckHistory tracks consecutive checks for a starvation alert.
type CheckHistory struct {
	AlertID        string
	Workspace      string
	FirstCheck     time.Time
	LastCheck      time.Time
	CheckCount     int
	LastReadyCount int
	LastStrategy   string
	Resolved       bool
	Escalated      bool
}

// AlertResolution records the outcome of an alert self-resolution attempt.
type AlertResolution struct {
	AlertID           string    `json:"alert_id"`
	Workspace         string    `json:"workspace"`
	Timestamp         time.Time `json:"timestamp"`
	Resolved          bool      `json:"resolved"`
	Escalated         bool      `json:"escalated,omitempty"`
	ClosedWithReason  string    `json:"closed_with_reason,omitempty"`
	EscalationBeadID  string    `json:"escalation_bead_id,omitempty"`
	ReadyCount        int       `json:"ready_count"`
	StrategyUsed      string    `json:"strategy_used,omitempty"`
	ConsecutiveChecks int       `json:"consecutive_checks"`
	Error             string    `json:"error,omitempty"`
	RootCause         string    `json:"root_cause,omitempty"` // New: root cause
	AutoRecovered     bool      `json:"auto_recovered"`       // New: auto-recovery flag
}

// SelfResolutionConfig holds configuration for the self-resolution daemon.
type SelfResolutionConfig struct {
	// WorkspaceRoot is the root directory containing all workspaces
	WorkspaceRoot string

	// LeaseLeader is the Kubernetes Lease leader elector (optional)
	LeaseLeader *LeaseLeader

	// CheckInterval is how often to check alerts (default: 5 minutes)
	CheckInterval time.Duration

	// AlertLabel identifies starvation alert beads (default: "starvation-alert")
	AlertLabel string

	// EnablePluckFallback enables the use of PluckFallback for resilient verification
	EnablePluckFallback bool

	// PluckFallbackDiagnosticLog is the path to the diagnostic log
	PluckFallbackDiagnosticLog string

	// MaxConsecutiveChecks is the number of checks before escalation (default: 3)
	MaxConsecutiveChecks int

	// OnResolution is called when an alert is resolved or escalated
	OnResolution func(resolution *AlertResolution)
}

// NewStarvationAlertSelfResolution creates a new self-resolution daemon.
func NewStarvationAlertSelfResolution(cfg SelfResolutionConfig) (*StarvationAlertSelfResolution, error) {
	if cfg.WorkspaceRoot == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 5 * time.Minute
	}
	if cfg.AlertLabel == "" {
		cfg.AlertLabel = "starvation-alert"
	}
	if cfg.MaxConsecutiveChecks == 0 {
		cfg.MaxConsecutiveChecks = 3
	}

	daemon := &StarvationAlertSelfResolution{
		leaseLeader:         cfg.LeaseLeader,
		workspaceRoot:       cfg.WorkspaceRoot,
		checkInterval:       cfg.CheckInterval,
		alertLabel:          cfg.AlertLabel,
		stopCh:             make(chan struct{}),
		checkHistory:       make(map[string]*CheckHistory),
		maxConsecutiveChecks: cfg.MaxConsecutiveChecks,
		onResolution:       cfg.OnResolution,
		diagnosticLogPath:  cfg.PluckFallbackDiagnosticLog,
	}

	// Initialize PluckFallback if enabled
	if cfg.EnablePluckFallback {
		if cfg.PluckFallbackDiagnosticLog == "" {
			cfg.PluckFallbackDiagnosticLog = filepath.Join(cfg.WorkspaceRoot, ".beads", "diagnostics", "starvation-alert-resolution.log")
		}
		pf, err := pluckfallback.NewPluckFallback(true, cfg.PluckFallbackDiagnosticLog, cfg.WorkspaceRoot)
		if err != nil {
			return nil, fmt.Errorf("initialize pluck fallback: %w", err)
		}
		daemon.pluckFallback = pf
		log.Printf("[AlertSelfResolution] PluckFallback enabled with diagnostic log: %s", cfg.PluckFallbackDiagnosticLog)
	}

	// Initialize root cause analyzer
	daemon.rootCauseAnalyzer = NewRootCauseAnalyzer()

	return daemon, nil
}

// Start begins the self-resolution daemon.
func (d *StarvationAlertSelfResolution) Start(ctx context.Context) error {
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
		log.Printf("[AlertSelfResolution] Attempting to acquire leadership")
		if !d.leaseLeader.Acquire(ctx) {
			return fmt.Errorf("failed to acquire leadership")
		}
		log.Printf("[AlertSelfResolution] Leadership acquired")

		// Start the lease renewal goroutine
		renewCtx, cancelRenew := context.WithCancel(ctx)
		defer cancelRenew()

		go func() {
			d.leaseLeader.Renew(renewCtx)
			log.Printf("[AlertSelfResolution] Leadership lost, stopping daemon")
			d.Stop()
		}()
	} else {
		log.Printf("[AlertSelfResolution] Running without leadership (local mode)")
	}

	// Main loop
	ticker := time.NewTicker(d.checkInterval)
	defer ticker.Stop()

	log.Printf("[AlertSelfResolution] Starting starvation alert self-resolution daemon (interval: %v)", d.checkInterval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[AlertSelfResolution] Context cancelled, stopping daemon")
			return ctx.Err()
		case <-d.stopCh:
			log.Printf("[AlertSelfResolution] Stop signal received, stopping daemon")
			return nil
		case <-ticker.C:
			if d.leaseLeader != nil && !d.leaseLeader.IsLeader() {
				log.Printf("[AlertSelfResolution] No longer leader, skipping check")
				continue
			}

			d.checkAllWorkspaces(ctx)
		}
	}
}

// checkAllWorkspaces checks all workspaces for starvation alerts.
func (d *StarvationAlertSelfResolution) checkAllWorkspaces(ctx context.Context) {
	workspaces, err := d.findWorkspacesWithBeads()
	if err != nil {
		log.Printf("[AlertSelfResolution] Failed to find workspaces: %v", err)
		return
	}

	for _, workspace := range workspaces {
		d.checkWorkspace(ctx, workspace)
	}
}

// findWorkspacesWithBeads finds all directories with .beads/beads.db.
func (d *StarvationAlertSelfResolution) findWorkspacesWithBeads() ([]string, error) {
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

// checkWorkspace checks a single workspace for starvation alerts to resolve.
func (d *StarvationAlertSelfResolution) checkWorkspace(ctx context.Context, workspacePath string) {
	workspaceName := filepath.Base(workspacePath)

	// Find all open starvation alert beads
	alertBeads, err := d.findStarvationAlertBeads(ctx, workspacePath)
	if err != nil {
		log.Printf("[AlertSelfResolution] Failed to find alert beads in %s: %v", workspaceName, err)
		return
	}

	if len(alertBeads) == 0 {
		return // No alerts in this workspace
	}

	log.Printf("[AlertSelfResolution] Found %d starvation alert beads in %s", len(alertBeads), workspaceName)

	for _, beadID := range alertBeads {
		resolution := d.checkAndResolveAlert(ctx, workspacePath, beadID)

		// Notify callback if set
		if d.onResolution != nil && resolution != nil {
			d.onResolution(resolution)
		}
	}
}

// findStarvationAlertBeads finds all open beads with the starvation-alert label.
func (d *StarvationAlertSelfResolution) findStarvationAlertBeads(ctx context.Context, workspacePath string) ([]string, error) {
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

	var alertBeads []string
	for _, bead := range beads {
		beadID, ok := bead["id"].(string)
		if !ok {
			continue
		}

		// Check if bead has the starvation-alert label
		hasAlertLabel := false
		if labels, ok := bead["labels"].([]interface{}); ok {
			for _, label := range labels {
				if labelStr, ok := label.(string); ok && labelStr == d.alertLabel {
					hasAlertLabel = true
					break
				}
			}
		}

		if hasAlertLabel {
			alertBeads = append(alertBeads, beadID)
		}
	}

	return alertBeads, nil
}

// checkAndResolveAlert checks a single alert and resolves it if work is available.
func (d *StarvationAlertSelfResolution) checkAndResolveAlert(ctx context.Context, workspacePath, beadID string) *AlertResolution {
	workspaceName := filepath.Base(workspacePath)
	now := time.Now()

	d.mu.Lock()
	history, exists := d.checkHistory[beadID]
	if !exists {
		history = &CheckHistory{
			AlertID:    beadID,
			Workspace:  workspaceName,
			FirstCheck: now,
		}
		d.checkHistory[beadID] = history
	}
	d.mu.Unlock()

	// Skip if already resolved or escalated
	if history.Resolved || history.Escalated {
		return nil
	}

	// Check if candidates are available using PluckFallback
	readyCount, strategyUsed, err := d.countCandidatesWithFallback(ctx, workspacePath)
	if err != nil {
		log.Printf("[AlertSelfResolution] Failed to check candidates for %s in %s: %v", beadID, workspaceName, err)
		return &AlertResolution{
			AlertID:   beadID,
			Workspace: workspaceName,
			Timestamp: now,
			Resolved:  false,
			Error:     fmt.Sprintf("failed to check candidates: %v", err),
		}
	}

	// Update check history
	history.LastCheck = now
	history.CheckCount++
	history.LastReadyCount = readyCount
	history.LastStrategy = strategyUsed

	log.Printf("[AlertSelfResolution] Alert %s in %s: check #%d, ready=%d, strategy=%s",
		beadID, workspaceName, history.CheckCount, readyCount, strategyUsed)

	// If candidates found, analyze root cause and resolve the alert
	if readyCount > 0 {
		return d.resolveAlertWithRootCauseAnalysis(ctx, workspacePath, beadID, readyCount, strategyUsed, history)
	}

	// No candidates - check if we should escalate
	timeSinceFirst := now.Sub(history.FirstCheck)
	hoursSinceFirst := timeSinceFirst.Hours()

	// Escalate after 3 consecutive checks (approximately 1 hour with 5min intervals)
	if history.CheckCount >= d.maxConsecutiveChecks {
		return d.escalateAlert(ctx, workspacePath, beadID, history)
	}

	// Not ready to escalate yet - log and continue
	log.Printf("[AlertSelfResolution] Alert %s in %s persists (check #%d/%d, age: %.1f min)",
		beadID, workspaceName, history.CheckCount, d.maxConsecutiveChecks, hoursSinceFirst*60)

	return &AlertResolution{
		AlertID:           beadID,
		Workspace:         workspaceName,
		Timestamp:         now,
		Resolved:          false,
		ReadyCount:        readyCount,
		StrategyUsed:      strategyUsed,
		ConsecutiveChecks: history.CheckCount,
	}
}

// countCandidatesWithFallback uses PluckFallback to count available candidates.
func (d *StarvationAlertSelfResolution) countCandidatesWithFallback(ctx context.Context, workspacePath string) (int, string, error) {
	// Use PluckFallback if enabled
	if d.pluckFallback != nil {
		candidates, metrics, discrepancies, err := d.pluckFallback.Pluck(ctx, workspacePath)
		if err != nil {
			log.Printf("[AlertSelfResolution] PluckFallback failed for %s: %v", workspacePath, err)
			// Fall back to direct query
			return d.countReadyBeadsDirect(ctx, workspacePath)
		}

		// Log any discrepancies
		for _, d := range discrepancies {
			log.Printf("[AlertSelfResolution] %s", d)
		}

		strategy := "primary"
		if metrics != nil {
			strategy = metrics.StrategyName
		}

		return len(candidates), strategy, nil
	}

	// Fall back to direct query
	count, err := d.countReadyBeadsDirect(ctx, workspacePath)
	return count, "direct", err
}

// countReadyBeadsDirect counts beads using the direct `bead list --ready` query.
func (d *StarvationAlertSelfResolution) countReadyBeadsDirect(ctx context.Context, workspacePath string) (int, error) {
	cmd := exec.CommandContext(ctx, "bead", "list", "--ready", "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		// No ready beads is not an error
		return 0, nil
	}

	// Parse JSON output and count beads
	count := strings.Count(string(output), `"id":`)
	return count, nil
}

// resolveAlertWithRootCauseAnalysis closes an alert bead with detailed recovery information and root cause analysis.
func (d *StarvationAlertSelfResolution) resolveAlertWithRootCauseAnalysis(ctx context.Context, workspacePath, beadID string, readyCount int, strategy string, history *CheckHistory) *AlertResolution {
	workspaceName := filepath.Base(workspacePath)
	now := time.Now()

	// Run root cause analysis
	rootCause, autoRecovered, analysisDetails := d.rootCauseAnalyzer.Analyze(ctx, workspacePath, strategy, readyCount, history)

	// Tag the bead with root cause label
	tagLabel := fmt.Sprintf("starvation:%s", rootCause)
	tagCmd := exec.CommandContext(ctx, "bead", "label", "add", beadID, tagLabel)
	tagCmd.Dir = workspacePath
	if output, err := tagCmd.CombinedOutput(); err != nil {
		log.Printf("[AlertSelfResolution] Failed to add root cause label to %s: %v (output: %s)", beadID, err, string(output))
		// Continue anyway - tagging is optional
	} else {
		log.Printf("[AlertSelfResolution] ✓ Tagged %s with root cause: %s", beadID, tagLabel)
	}

	// Build the close reason and note
	reason := fmt.Sprintf("Root cause identified and resolved: %s", rootCause)
	note := fmt.Sprintf("Automated recovery at %s\n\n"+
		"**Root Cause Analysis:**\n"+
		"%s\n\n"+
		"**Recovery Details:**\n"+
		"- Candidates found: %d\n"+
		"- Strategy used: %s\n"+
		"- Consecutive checks: %d\n"+
		"- Time to resolution: %.1f minutes\n"+
		"- Auto-recovered: %v\n",
		now.Format(time.RFC3339), analysisDetails, readyCount, strategy, history.CheckCount, now.Sub(history.FirstCheck).Minutes(), autoRecovered)

	// Add note to bead
	updateCmd := exec.CommandContext(ctx, "bead", "update", beadID, "--notes", note)
	updateCmd.Dir = workspacePath
	if output, err := updateCmd.CombinedOutput(); err != nil {
		log.Printf("[AlertSelfResolution] Failed to add note to %s: %v (output: %s)", beadID, err, string(output))
		// Continue anyway - note is optional
	}

	// Close the bead
	closeCmd := exec.CommandContext(ctx, "bead", "close", beadID, "--reason", reason)
	closeCmd.Dir = workspacePath

	output, err := closeCmd.CombinedOutput()
	if err != nil {
		log.Printf("[AlertSelfResolution] Failed to close alert %s in %s: %v", beadID, workspaceName, err)
		return &AlertResolution{
			AlertID:   beadID,
			Workspace: workspaceName,
			Timestamp: now,
			Resolved:  false,
			Error:     fmt.Sprintf("close failed: %v: %s", err, string(output)),
		}
	}

	// Mark as resolved in history
	history.Resolved = true

	log.Printf("[AlertSelfResolution] ✓ Resolved alert %s in %s (root cause=%s, auto-recovered=%v, ready=%d, strategy=%s, checks=%d)",
		beadID, workspaceName, rootCause, autoRecovered, readyCount, strategy, history.CheckCount)

	return &AlertResolution{
		AlertID:           beadID,
		Workspace:         workspaceName,
		Timestamp:         now,
		Resolved:          true,
		ClosedWithReason:  reason,
		ReadyCount:        readyCount,
		StrategyUsed:      strategy,
		ConsecutiveChecks: history.CheckCount,
		RootCause:         rootCause,
		AutoRecovered:     autoRecovered,
	}
}

// escalateAlert creates a human-review bead when alert persists.
func (d *StarvationAlertSelfResolution) escalateAlert(ctx context.Context, workspacePath, beadID string, history *CheckHistory) *AlertResolution {
	workspaceName := filepath.Base(workspacePath)
	now := time.Now()

	// Mark as escalated
	history.Escalated = true

	// Create escalation bead
	escalationTitle := fmt.Sprintf("Starvation alert requires manual review - %s", workspaceName)
	escalationDesc := fmt.Sprintf(
		"Starvation alert %s has persisted through %d consecutive automated checks over %.1f minutes.\n\n"+
			"**Alert Details:**\n"+
			"- Alert ID: %s\n"+
			"- Workspace: %s\n"+
			"- First detected: %s\n"+
			"- Last check: %s\n"+
			"- Total checks: %d\n\n"+
			"**Current State:**\n"+
			"- Ready beads: %d\n"+
			"- Last strategy used: %s\n"+
			"- Condition: Starvation persists (no work available)\n\n"+
			"**Automated Recovery Attempts:**\n"+
			"- Automated verification ran %d times using PluckFallback\n"+
			"- All strategies attempted: primary, open_unassigned, open_status, direct_db, checkpoint\n"+
			"- No candidates found through any strategy\n\n"+
			"**Action Required:**\n"+
			"Manual investigation needed:\n"+
			"1. Check if NEEDLE workers are alive and responsive\n"+
			"2. Run `bead doctor --repair` to fix database corruption\n"+
			"3. Validate cross-repo preconditions\n"+
			"4. Investigate database locking or corruption issues\n"+
			"5. Verify bead visibility and assignment issues\n\n"+
			"This escalation bead was automatically created by the starvation-alert-self-resolution daemon.",
		beadID, history.CheckCount, now.Sub(history.FirstCheck).Minutes(),
		beadID, workspaceName, history.FirstCheck.Format(time.RFC3339),
		history.LastCheck.Format(time.RFC3339), history.CheckCount,
		history.LastReadyCount, history.LastStrategy, history.CheckCount,
	)

	cmd := exec.CommandContext(ctx, "bead", "create",
		"--title", escalationTitle,
		"--priority", "1", // P1 - high priority
		"--issue-type", "task",
		"--label", "human-review-required",
		"--label", "starvation-escalation",
	)
	cmd.Dir = workspacePath
	cmd.Stdin = strings.NewReader(escalationDesc)

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[AlertSelfResolution] Failed to create escalation bead for %s: %v", beadID, err)
		return &AlertResolution{
			AlertID:   beadID,
			Workspace: workspaceName,
			Timestamp: now,
			Resolved:  false,
			Error:     fmt.Sprintf("escalation failed: %v: %s", err, string(output)),
		}
	}

	// Extract bead ID from output
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	escalationBeadID := ""
	if len(lines) > 0 {
		escalationBeadID = strings.TrimSpace(lines[len(lines)-1])
	}

	log.Printf("[AlertSelfResolution] → Escalated alert %s in %s to bead %s after %d checks",
		beadID, workspaceName, escalationBeadID, history.CheckCount)

	return &AlertResolution{
		AlertID:           beadID,
		Workspace:         workspaceName,
		Timestamp:         now,
		Resolved:          false,
		Escalated:         true,
		EscalationBeadID:  escalationBeadID,
		ReadyCount:        history.LastReadyCount,
		StrategyUsed:      history.LastStrategy,
		ConsecutiveChecks: history.CheckCount,
	}
}

// Stop stops the self-resolution daemon.
func (d *StarvationAlertSelfResolution) Stop() {
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

	// Close PluckFallback if enabled
	if d.pluckFallback != nil {
		if err := d.pluckFallback.Close(); err != nil {
			log.Printf("[AlertSelfResolution] Failed to close PluckFallback: %v", err)
		}
	}

	// Release lease leadership if configured
	if d.leaseLeader != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.leaseLeader.Release(ctx)
	}

	log.Printf("[AlertSelfResolution] Starvation alert self-resolution daemon stopped")
}

// IsRunning reports whether the daemon is currently running.
func (d *StarvationAlertSelfResolution) IsRunning() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return !d.stopped && (d.leaseLeader == nil || d.leaseLeader.IsLeader())
}

// GetCheckHistory returns the check history for all tracked alerts.
func (d *StarvationAlertSelfResolution) GetCheckHistory() map[string]*CheckHistory {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Return a copy to avoid race conditions
	history := make(map[string]*CheckHistory, len(d.checkHistory))
	for k, v := range d.checkHistory {
		history[k] = v
	}
	return history
}
