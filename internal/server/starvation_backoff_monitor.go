package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// TransientStarvationBackoff monitors workspaces for starvation conditions
// and implements exponential backoff before creating alert beads.
// This reduces false-positive alert noise by giving transient issues time to resolve.
//
// Backoff intervals: 30s, 2m, 5m, 15m
// Only creates an alert bead if starvation persists through ALL intervals.
type TransientStarvationBackoff struct {
	mu                sync.RWMutex
	leaseLeader       *LeaseLeader
	workspaceRoot     string
	stopCh            chan struct{}
	stopped           bool
	checkInterval     time.Duration
	backoffIntervals  []time.Duration // 30s, 2m, 5m, 15m
	pendingEvents     map[string]*BackoffState
	rootCauseAnalyzer *RootCauseAnalyzer
	onCreateAlert     func(workspace string, state *BackoffState)
	diagnosticLogPath string
}

// BackoffState tracks a pending starvation event through the backoff sequence.
type BackoffState struct {
	Workspace         string
	FirstDetected     time.Time
	LastChecked       time.Time
	CurrentRetryIndex int // 0-3 for 4 backoff intervals
	OpenBeadsCount    int
	ReadyBeadsCount   int
	InfrastructureOK  bool
	CheckHistory      []BackoffCheck
	Resolved          bool
	Escalated         bool
	AlertBeadID       string // Set when alert is finally created
}

// BackoffCheck records a single backoff retry check.
type BackoffCheck struct {
	Timestamp     time.Time
	ReadyCount    int
	OpenCount     int
	InfrastructureOK bool
	Strategy      string
}

// BackoffConfig holds configuration for the backoff monitor.
type BackoffConfig struct {
	// WorkspaceRoot is the root directory containing all workspaces
	WorkspaceRoot string

	// LeaseLeader is the Kubernetes Lease leader elector (optional)
	LeaseLeader *LeaseLeader

	// CheckInterval is how often to scan workspaces (default: 30 seconds)
	CheckInterval time.Duration

	// BackoffIntervals are the retry intervals (default: 30s, 2m, 5m, 15m)
	BackoffIntervals []time.Duration

	// OnCreateAlert is called when an alert bead is finally created
	OnCreateAlert func(workspace string, state *BackoffState)
}

// Default backoff intervals: 30s, 2m, 5m, 15m
var defaultBackoffIntervals = []time.Duration{
	30 * time.Second,
	2 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
}

// NewTransientStarvationBackoff creates a new backoff monitor daemon.
func NewTransientStarvationBackoff(cfg BackoffConfig) (*TransientStarvationBackoff, error) {
	if cfg.WorkspaceRoot == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 30 * time.Second
	}
	if len(cfg.BackoffIntervals) == 0 {
		cfg.BackoffIntervals = defaultBackoffIntervals
	}

	diagnosticLog := filepath.Join(cfg.WorkspaceRoot, ".beads", "diagnostics", "starvation-backoff.log")

	daemon := &TransientStarvationBackoff{
		leaseLeader:       cfg.LeaseLeader,
		workspaceRoot:     cfg.WorkspaceRoot,
		checkInterval:     cfg.CheckInterval,
		backoffIntervals:  cfg.BackoffIntervals,
		pendingEvents:     make(map[string]*BackoffState),
		onCreateAlert:     cfg.OnCreateAlert,
		diagnosticLogPath: diagnosticLog,
	}

	// Initialize root cause analyzer
	daemon.rootCauseAnalyzer = NewRootCauseAnalyzer()

	return daemon, nil
}

// Start begins the backoff monitor daemon.
func (d *TransientStarvationBackoff) Start(ctx context.Context) error {
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
		log.Printf("[BackoffMonitor] Attempting to acquire leadership")
		if !d.leaseLeader.Acquire(ctx) {
			return fmt.Errorf("failed to acquire leadership")
		}
		log.Printf("[BackoffMonitor] Leadership acquired")

		// Start the lease renewal goroutine
		renewCtx, cancelRenew := context.WithCancel(ctx)
		defer cancelRenew()

		go func() {
			d.leaseLeader.Renew(renewCtx)
			log.Printf("[BackoffMonitor] Leadership lost, stopping daemon")
			d.Stop()
		}()
	} else {
		log.Printf("[BackoffMonitor] Running without leadership (local mode)")
	}

	// Main loop
	ticker := time.NewTicker(d.checkInterval)
	defer ticker.Stop()

	log.Printf("[BackoffMonitor] Starting transient starvation backoff monitor (interval: %v, backoffs: %v)",
		d.checkInterval, d.backoffIntervals)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[BackoffMonitor] Context cancelled, stopping daemon")
			return ctx.Err()
		case <-d.stopCh:
			log.Printf("[BackoffMonitor] Stop signal received, stopping daemon")
			return nil
		case <-ticker.C:
			if d.leaseLeader != nil && !d.leaseLeader.IsLeader() {
				log.Printf("[BackoffMonitor] No longer leader, skipping check")
				continue
			}

			d.checkAllWorkspaces(ctx)
		}
	}
}

// checkAllWorkspaces scans all workspaces for starvation conditions.
func (d *TransientStarvationBackoff) checkAllWorkspaces(ctx context.Context) {
	workspaces, err := d.findWorkspacesWithBeads()
	if err != nil {
		log.Printf("[BackoffMonitor] Failed to find workspaces: %v", err)
		return
	}

	for _, workspace := range workspaces {
		d.checkWorkspace(ctx, workspace)
	}
}

// findWorkspacesWithBeads finds all directories with .beads/beads.db.
func (d *TransientStarvationBackoff) findWorkspacesWithBeads() ([]string, error) {
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

// checkWorkspace checks a single workspace for starvation conditions.
func (d *TransientStarvationBackoff) checkWorkspace(ctx context.Context, workspacePath string) {
	workspaceName := filepath.Base(workspacePath)
	now := time.Now()

	// Get current state
	openCount, readyCount, err := d.getBeadCounts(ctx, workspacePath)
	if err != nil {
		log.Printf("[BackoffMonitor] Failed to get bead counts for %s: %v", workspaceName, err)
		return
	}

	d.mu.Lock()
	state, exists := d.pendingEvents[workspacePath]
	d.mu.Unlock()

	// Check if infrastructure is healthy using root cause analyzer checks
	// We only care about infrastructure health, not about whether work is available
	infrastructureOK := d.checkInfrastructureHealth(ctx, workspacePath)

	log.Printf("[BackoffMonitor] %s: open=%d, ready=%d, infra_ok=%v, tracking=%v",
		workspaceName, openCount, readyCount, infrastructureOK, exists)

	// Condition 1: No starvation - clear any pending event
	if readyCount > 0 || openCount == 0 {
		if exists {
			log.Printf("[BackoffMonitor] %s: Starvation resolved (ready=%d, open=%d), clearing pending event",
				workspaceName, readyCount, openCount)
			d.clearPendingEvent(workspacePath)
		}
		return
	}

	// Condition 2: Starvation detected, but infrastructure is unhealthy
	// This is a real problem - don't use backoff, escalate immediately
	if !infrastructureOK {
		log.Printf("[BackoffMonitor] %s: Infrastructure unhealthy, escalating immediately", workspaceName)
		d.escalateImmediately(ctx, workspacePath, openCount, readyCount)
		d.clearPendingEvent(workspacePath)
		return
	}

	// Condition 3: Starvation with healthy infrastructure - use backoff
	if !exists {
		// First detection - start tracking
		d.startBackoffTracking(workspacePath, now, openCount, readyCount)
		log.Printf("[BackoffMonitor] %s: Starvation detected with healthy infrastructure, starting backoff (interval 0: %v)",
			workspaceName, d.backoffIntervals[0])
		return
	}

	// Existing tracking event - check if it's time for next retry
	if !d.isTimeForNextRetry(state, now, d.backoffIntervals) {
		return
	}

	// Time for next retry check - record the check
	checkRecord := BackoffCheck{
		Timestamp:        now,
		ReadyCount:       readyCount,
		OpenCount:        openCount,
		InfrastructureOK: infrastructureOK,
		Strategy:         "backoff-retry",
	}

	d.mu.Lock()
	state.CheckHistory = append(state.CheckHistory, checkRecord)
	state.LastChecked = now
	state.CurrentRetryIndex++
	d.mu.Unlock()

	// Still starving - check if we've exhausted all backoff intervals
	if state.CurrentRetryIndex >= len(d.backoffIntervals) {
		log.Printf("[BackoffMonitor] %s: Starvation persisted through %d backoff intervals, creating alert bead",
			workspaceName, len(d.backoffIntervals))
		d.createAlertBead(ctx, workspacePath, state)
		d.clearPendingEvent(workspacePath)
		return
	}

	// Continue to next backoff interval
	nextInterval := d.backoffIntervals[state.CurrentRetryIndex]
	log.Printf("[BackoffMonitor] %s: Starvation persists, advancing to backoff interval %d: %v",
		workspaceName, state.CurrentRetryIndex, nextInterval)
}

// getBeadCounts gets the current open and ready bead counts.
func (d *TransientStarvationBackoff) getBeadCounts(ctx context.Context, workspacePath string) (openCount, readyCount int, err error) {
	// Get open count
	cmd := exec.CommandContext(ctx, "bead", "list", "--status", "open", "--json")
	cmd.Dir = workspacePath
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("get open beads: %w", err)
	}

	// Count JSON array items
	openCount = countJSONItems(string(output))

	// Get ready count
	cmd = exec.CommandContext(ctx, "bead", "list", "--ready", "--json")
	cmd.Dir = workspacePath
	output, err = cmd.Output()
	if err != nil {
		// No ready beads is not an error
		readyCount = 0
	} else {
		readyCount = countJSONItems(string(output))
	}

	return openCount, readyCount, nil
}

// checkInfrastructureHealth checks if the infrastructure is healthy.
// Returns true only if all infrastructure checks pass.
func (d *TransientStarvationBackoff) checkInfrastructureHealth(ctx context.Context, workspacePath string) bool {
	// Use root cause analyzer to check infrastructure components
	// We're only checking for health, not analyzing root causes
	workspaceName := filepath.Base(workspacePath)

	// Check database integrity
	dbHealthy, _ := d.rootCauseAnalyzer.checkDatabaseIntegrity(ctx, workspacePath)
	if !dbHealthy {
		log.Printf("[BackoffMonitor] %s: Database integrity check failed", workspaceName)
		return false
	}

	// Check checkpoint freshness
	checkpointFresh, _ := d.rootCauseAnalyzer.checkCheckpointFreshness(ctx, workspacePath)
	if !checkpointFresh {
		log.Printf("[BackoffMonitor] %s: Checkpoint freshness check failed", workspaceName)
		return false
	}

	// Check index integrity
	indexHealthy, _ := d.rootCauseAnalyzer.checkIndexIntegrity(ctx, workspacePath)
	if !indexHealthy {
		log.Printf("[BackoffMonitor] %s: Index integrity check failed", workspaceName)
		return false
	}

	// Check filter configuration
	filterValid, _ := d.rootCauseAnalyzer.checkFilterConfiguration(ctx, workspacePath)
	if !filterValid {
		log.Printf("[BackoffMonitor] %s: Filter configuration check failed", workspaceName)
		return false
	}

	// Check worker status
	workersAlive, _ := d.rootCauseAnalyzer.checkWorkerStatus(ctx, workspacePath)
	if !workersAlive {
		log.Printf("[BackoffMonitor] %s: Worker status check failed", workspaceName)
		return false
	}

	// All checks passed
	return true
}

// startBackoffTracking begins tracking a new starvation event.
func (d *TransientStarvationBackoff) startBackoffTracking(workspacePath string, detected time.Time, openCount, readyCount int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.pendingEvents[workspacePath] = &BackoffState{
		Workspace:        workspacePath,
		FirstDetected:     detected,
		LastChecked:      detected,
		CurrentRetryIndex: 0,
		OpenBeadsCount:   openCount,
		ReadyBeadsCount:  readyCount,
		InfrastructureOK: true,
		CheckHistory: []BackoffCheck{
			{
				Timestamp:        detected,
				ReadyCount:       readyCount,
				OpenCount:        openCount,
				InfrastructureOK: true,
				Strategy:         "initial-detection",
			},
		},
	}
}

// isTimeForNextRetry checks if enough time has passed for the next retry.
func (d *TransientStarvationBackoff) isTimeForNextRetry(state *BackoffState, now time.Time, intervals []time.Duration) bool {
	if state.CurrentRetryIndex >= len(intervals) {
		return false
	}

	elapsed := now.Sub(state.FirstDetected)
	targetInterval := intervals[state.CurrentRetryIndex]

	return elapsed >= targetInterval
}

// escalateImmediately creates an alert bead when infrastructure is unhealthy.
func (d *TransientStarvationBackoff) escalateImmediately(ctx context.Context, workspacePath string, openCount, readyCount int) {
	workspaceName := filepath.Base(workspacePath)
	now := time.Now()

	title := fmt.Sprintf("CRITICAL: Infrastructure failure detected in %s", workspaceName)
	description := fmt.Sprintf(
		"**CRITICAL INFRASTRUCTURE FAILURE**\n\n"+
			"Workspace: %s\n"+
			"Detected: %s\n\n"+
			"**Current State:**\n"+
			"- Open beads: %d\n"+
			"- Ready beads: %d\n"+
			"- Infrastructure: UNHEALTHY\n\n"+
			"**Action Required:**\n"+
			"This starvation condition is caused by an infrastructure failure, not a transient issue.\n"+
			"Run `bead doctor --repair` to diagnose and fix the underlying infrastructure problem.\n\n"+
			"This alert was created immediately (without backoff) because infrastructure checks failed.\n\n"+
			"Created by: TransientStarvationBackoff daemon",
		workspaceName, now.Format(time.RFC3339),
		openCount, readyCount,
	)

	beadID, err := d.createBead(ctx, workspacePath, title, description, 1, []string{"starvation-alert", "infrastructure-failure", "starvation:infrastructure-failure"})
	if err != nil {
		log.Printf("[BackoffMonitor] Failed to create infrastructure failure alert for %s: %v", workspaceName, err)
		return
	}

	log.Printf("[BackoffMonitor] ✓ Created infrastructure failure alert %s for %s", beadID, workspaceName)

	// Notify callback
	if d.onCreateAlert != nil {
		d.onCreateAlert(workspacePath, &BackoffState{
			Workspace:   workspacePath,
			AlertBeadID: beadID,
			Escalated:   true,
		})
	}
}

// createAlertBead creates a starvation alert bead after backoff is exhausted.
func (d *TransientStarvationBackoff) createAlertBead(ctx context.Context, workspacePath string, state *BackoffState) {
	workspaceName := filepath.Base(workspacePath)
	now := time.Now()

	// Calculate total backoff duration
	totalDuration := now.Sub(state.FirstDetected)

	title := fmt.Sprintf("Starvation alert: beads invisible in %s", workspaceName)
	description := fmt.Sprintf(
		"**PERSISTENT STARVATION CONDITION**\n\n"+
			"Workspace: %s\n"+
			"First detected: %s\n"+
			"Alert created: %s\n\n"+
			"**Current State:**\n"+
			"- Open beads: %d\n"+
			"- Ready beads: %d\n"+
			"- Infrastructure: HEALTHY\n\n"+
			"**Backoff History:**\n"+
			"- Total backoff duration: %.1f minutes\n"+
			"- Retry intervals attempted: %d\n"+
			"- Backoff intervals: 30s, 2m, 5m, 15m\n\n"+
			"**Retry Checks:**\n",
		workspaceName,
		state.FirstDetected.Format(time.RFC3339),
		now.Format(time.RFC3339),
		state.OpenBeadsCount,
		state.ReadyBeadsCount,
		totalDuration.Minutes(),
		len(state.CheckHistory),
	)

	for i, check := range state.CheckHistory {
		description += fmt.Sprintf("  %d. %s: ready=%d, open=%d, infra_ok=%v\n",
			i+1, check.Timestamp.Format(time.RFC3339), check.ReadyCount, check.OpenCount, check.InfrastructureOK)
	}

	description += "\n**Analysis:**\n" +
		"This starvation condition persisted through all exponential backoff intervals.\n" +
		"All infrastructure checks passed (database, checkpoint, index, filter, workers).\n" +
		"This suggests a persistent issue rather than a transient problem.\n\n" +
		"**Action Required:**\n" +
		"Manual investigation may be needed. The starvation-alert-self-resolution daemon\n" +
		"will continue to monitor and auto-resolve if work becomes available.\n\n" +
		"Created by: TransientStarvationBackoff daemon after exhausting backoff sequence."

	beadID, err := d.createBead(ctx, workspacePath, title, description, 1, []string{"starvation-alert", "persistent-starvation", "starvation:transient-failed"})
	if err != nil {
		log.Printf("[BackoffMonitor] Failed to create alert bead for %s: %v", workspaceName, err)
		return
	}

	log.Printf("[BackoffMonitor] ✓ Created starvation alert %s for %s after %d retries (%.1f min)",
		beadID, workspaceName, len(state.CheckHistory), totalDuration.Minutes())

	// Update state with bead ID
	state.AlertBeadID = beadID
	state.Escalated = true

	// Notify callback
	if d.onCreateAlert != nil {
		d.onCreateAlert(workspacePath, state)
	}
}

// createBead creates a bead with the given parameters.
func (d *TransientStarvationBackoff) createBead(ctx context.Context, workspacePath, title, description string, priority int, labels []string) (string, error) {
	args := []string{
		"create",
		"--title", title,
		"--priority", fmt.Sprintf("%d", priority),
		"--issue-type", "task",
	}
	for _, label := range labels {
		args = append(args, "--label", label)
	}

	cmd := exec.CommandContext(ctx, "bead", args...)
	cmd.Dir = workspacePath
	// Note: description would need to be passed via stdin if we wanted full body support
	// For now, we use --title which captures the essence

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("bead create: %w: %s", err, string(output))
	}

	// Extract bead ID from output
	lines := splitLines(string(output))
	if len(lines) > 0 {
		beadID := trimSpace(lines[len(lines)-1])
		return beadID, nil
	}

	return "", fmt.Errorf("no bead ID in output")
}

// clearPendingEvent removes a pending event from tracking.
func (d *TransientStarvationBackoff) clearPendingEvent(workspacePath string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.pendingEvents, workspacePath)
}

// GetPendingEvents returns all currently tracked pending events.
func (d *TransientStarvationBackoff) GetPendingEvents() map[string]*BackoffState {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Return a copy to avoid race conditions
	events := make(map[string]*BackoffState, len(d.pendingEvents))
	for k, v := range d.pendingEvents {
		events[k] = v
	}
	return events
}

// Stop stops the backoff monitor daemon.
func (d *TransientStarvationBackoff) Stop() {
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

	log.Printf("[BackoffMonitor] Transient starvation backoff monitor stopped")
}

// IsRunning reports whether the daemon is currently running.
func (d *TransientStarvationBackoff) IsRunning() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return !d.stopped && (d.leaseLeader == nil || d.leaseLeader.IsLeader())
}

// Helper functions

func countJSONItems(jsonStr string) int {
	// Simple JSON item counter - counts occurrences of "id" field
	count := 0
	start := 0
	for {
		idx := index(jsonStr, `"id":`, start)
		if idx == -1 {
			break
		}
		count++
		start = idx + 5
	}
	return count
}

func index(s, substr string, start int) int {
	if start >= len(s) {
		return -1
	}
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func splitLines(s string) []string {
	lines := []string{}
	current := ""
	for _, ch := range s {
		if ch == '\n' {
			lines = append(lines, current)
			current = ""
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
