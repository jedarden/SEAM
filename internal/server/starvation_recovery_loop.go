package server

import (
	"bufio"
	"context"
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

// StarvationRecoveryLoop runs the automated starvation diagnostic and recovery loop.
// It detects starvation conditions (no candidates but open/invisible beads exist)
// and runs automated recovery before escalating to human intervention.
type StarvationRecoveryLoop struct {
	mu                  sync.RWMutex
	leaseLeader         *LeaseLeader
	workspaceRoot       string
	validateScript      string
	stopCh              chan struct{}
	stopped             bool
	checkInterval       time.Duration
	recoveryAttempts    map[string]int // bead ID -> attempt count
	maxAttemptsPerBead  int
	onRecoveryComplete func(beadID string, success bool, details string)
	pluckFallback       *pluckfallback.PluckFallback // PluckFallback for resilient bead querying
	diagnosticLogPath   string                        // Path to diagnostic log for pluck discrepancies
}

// RecoveryConfig holds the configuration for the starvation recovery loop.
type RecoveryConfig struct {
	// WorkspaceRoot is the root directory containing all workspaces (e.g., /home/coding)
	WorkspaceRoot string

	// ValidateScript is the path to the cross-repo precondition validation script
	ValidateScript string

	// LeaseLeader is the Kubernetes Lease leader elector (optional, for distributed deployment)
	LeaseLeader *LeaseLeader

	// CheckInterval is how often to check for starvation conditions (default: 5 minutes)
	CheckInterval time.Duration

	// MaxAttemptsPerBead is the maximum number of recovery attempts per bead before escalation
	MaxAttemptsPerBead int

	// OnRecoveryComplete is called when a recovery attempt completes
	OnRecoveryComplete func(beadID string, success bool, details string)

	// EnablePluckFallback enables the use of PluckFallback for resilient bead querying
	EnablePluckFallback bool

	// PluckFallbackDiagnosticLog is the path to the diagnostic log for pluck discrepancies
	PluckFallbackDiagnosticLog string
}

// NewStarvationRecoveryLoop creates a new starvation recovery loop.
func NewStarvationRecoveryLoop(cfg RecoveryConfig) (*StarvationRecoveryLoop, error) {
	if cfg.WorkspaceRoot == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	if cfg.ValidateScript == "" {
		cfg.ValidateScript = filepath.Join(cfg.WorkspaceRoot, "SEAM", "tools", "validate_cross_repo_preconditions.sh")
	}
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 5 * time.Minute
	}
	if cfg.MaxAttemptsPerBead == 0 {
		cfg.MaxAttemptsPerBead = 3
	}

	// Validate that scripts exist
	if _, err := os.Stat(cfg.ValidateScript); err != nil {
		return nil, fmt.Errorf("validate script not found: %w", err)
	}

	loop := &StarvationRecoveryLoop{
		leaseLeader:        cfg.LeaseLeader,
		workspaceRoot:      cfg.WorkspaceRoot,
		validateScript:     cfg.ValidateScript,
		checkInterval:      cfg.CheckInterval,
		maxAttemptsPerBead: cfg.MaxAttemptsPerBead,
		recoveryAttempts:   make(map[string]int),
		stopCh:            make(chan struct{}),
		onRecoveryComplete: cfg.OnRecoveryComplete,
		diagnosticLogPath:  cfg.PluckFallbackDiagnosticLog,
	}

	// Initialize PluckFallback if enabled
	if cfg.EnablePluckFallback {
		if cfg.PluckFallbackDiagnosticLog == "" {
			cfg.PluckFallbackDiagnosticLog = filepath.Join(cfg.WorkspaceRoot, ".beads", "diagnostics", "pluck-fallback.log")
		}
		pf, err := pluckfallback.NewPluckFallback(true, cfg.PluckFallbackDiagnosticLog)
		if err != nil {
			return nil, fmt.Errorf("initialize pluck fallback: %w", err)
		}
		loop.pluckFallback = pf
		log.Printf("[RecoveryLoop] PluckFallback enabled with diagnostic log: %s", cfg.PluckFallbackDiagnosticLog)
	}

	return loop, nil
}

// Start begins the starvation recovery loop.
// It periodically checks for starvation conditions and runs automated recovery.
func (l *StarvationRecoveryLoop) Start(ctx context.Context) error {
	if l == nil {
		return fmt.Errorf("recovery loop is nil")
	}

	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return fmt.Errorf("recovery loop already stopped")
	}
	l.mu.Unlock()

	// Acquire leadership if configured
	if l.leaseLeader != nil {
		log.Printf("[RecoveryLoop] Attempting to acquire leadership for starvation recovery")
		if !l.leaseLeader.Acquire(ctx) {
			return fmt.Errorf("failed to acquire leadership")
		}
		log.Printf("[RecoveryLoop] Leadership acquired")

		// Start the lease renewal goroutine
		renewCtx, cancelRenew := context.WithCancel(ctx)
		defer cancelRenew()

		go func() {
			l.leaseLeader.Renew(renewCtx)
			log.Printf("[RecoveryLoop] Leadership lost, stopping recovery loop")
			l.Stop()
		}()
	} else {
		log.Printf("[RecoveryLoop] Running without leadership (local mode)")
	}

	// Main recovery loop
	ticker := time.NewTicker(l.checkInterval)
	defer ticker.Stop()

	log.Printf("[RecoveryLoop] Starting starvation recovery loop (check interval: %v)", l.checkInterval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[RecoveryLoop] Context cancelled, stopping recovery loop")
			return ctx.Err()
		case <-l.stopCh:
			log.Printf("[RecoveryLoop] Stop signal received, stopping recovery loop")
			return nil
		case <-ticker.C:
			if l.leaseLeader != nil && !l.leaseLeader.IsLeader() {
				log.Printf("[RecoveryLoop] No longer leader, skipping recovery check")
				continue
			}

			// Check for starvation conditions and run recovery
			l.checkAndRecover(ctx)
		}
	}
}

// checkAndRecover checks all workspaces for starvation conditions and runs automated recovery.
func (l *StarvationRecoveryLoop) checkAndRecover(ctx context.Context) {
	// Find all workspaces with bead databases
	workspaces, err := l.findWorkspacesWithBeads()
	if err != nil {
		log.Printf("[RecoveryLoop] Failed to find workspaces: %v", err)
		return
	}

	for _, workspace := range workspaces {
		l.checkWorkspace(ctx, workspace)
	}
}

// findWorkspacesWithBeads finds all directories in workspaceRoot that have a .beads/beads.db file.
func (l *StarvationRecoveryLoop) findWorkspacesWithBeads() ([]string, error) {
	var workspaces []string

	entries, err := os.ReadDir(l.workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("read workspace root: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		workspacePath := filepath.Join(l.workspaceRoot, entry.Name())
		beadDB := filepath.Join(workspacePath, ".beads", "beads.db")

		if _, err := os.Stat(beadDB); err == nil {
			workspaces = append(workspaces, workspacePath)
		}
	}

	return workspaces, nil
}

// checkWorkspace checks a single workspace for starvation conditions and runs recovery.
func (l *StarvationRecoveryLoop) checkWorkspace(ctx context.Context, workspacePath string) {
	workspaceName := filepath.Base(workspacePath)
	log.Printf("[RecoveryLoop] Checking workspace: %s", workspaceName)

	// Check for starvation: no ready beads but open/invisible beads exist
	hasStarvation, openBeadsCount, invisibleCount, err := l.detectStarvation(ctx, workspacePath)
	if err != nil {
		log.Printf("[RecoveryLoop] Failed to detect starvation in %s: %v", workspaceName, err)
		return
	}

	if !hasStarvation {
		log.Printf("[RecoveryLoop] No starvation detected in %s (open=%d, invisible=%d)",
			workspaceName, openBeadsCount, invisibleCount)
		return
	}

	log.Printf("[RecoveryLoop] Starvation detected in %s (open=%d, invisible=%d), starting recovery",
		workspaceName, openBeadsCount, invisibleCount)

	// Run automated recovery
	success, details := l.runAutomatedRecovery(ctx, workspacePath)

	if success {
		log.Printf("[RecoveryLoop] Recovery succeeded in %s: %s", workspaceName, details)
	} else {
		log.Printf("[RecoveryLoop] Recovery incomplete in %s: %s (may require manual intervention)", workspaceName, details)
	}

	// Notify callback if set
	if l.onRecoveryComplete != nil {
		l.onRecoveryComplete(workspaceName, success, details)
	}
}

// detectStarvation checks if a workspace is in starvation state.
// Starvation = no ready beads available but open/invisible beads exist.
// IMPORTANT: Self-verifies open bead count against direct database query before declaring starvation.
func (l *StarvationRecoveryLoop) detectStarvation(ctx context.Context, workspacePath string) (hasStarvation bool, openBeadsCount int, invisibleCount int, err error) {
	// Count open beads via CLI
	openCount, err := l.countBeadsByStatus(ctx, workspacePath, "open")
	if err != nil {
		return false, 0, 0, fmt.Errorf("count open beads: %w", err)
	}

	// Count beads that might be invisible (assigned but not in_progress, or blocked)
	invisible, err := l.countInvisibleBeads(ctx, workspacePath)
	if err != nil {
		return false, openCount, 0, fmt.Errorf("count invisible beads: %w", err)
	}

	// Check if there are any ready beads
	readyCount, err := l.countReadyBeads(ctx, workspacePath)
	if err != nil {
		return false, openCount, invisible, fmt.Errorf("count ready beads: %w", err)
	}

	// Self-verification: Compare CLI-based open count against direct database query
	// This prevents false-positive starvation alerts when the CLI returns stale/incorrect data
	dbOpenCount, err := l.verifyOpenBeadCountDirectDB(ctx, workspacePath)
	if err != nil {
		// If direct DB query fails, log warning but continue with CLI count
		log.Printf("[RecoveryLoop] WARNING: Direct DB verification failed for %s: %v (using CLI count)", filepath.Base(workspacePath), err)
		dbOpenCount = openCount // fallback to CLI count
	}

	// Detection anomaly check: If CLI claims open beads exist but DB shows none, suppress alert
	if openCount > 0 && dbOpenCount == 0 {
		log.Printf("[RecoveryLoop] DETECTION ANOMALY in %s: CLI reports %d open beads but direct DB query shows 0. Suppressing starvation alert to prevent false positive.",
			filepath.Base(workspacePath), openCount)
		// Log to diagnostic file if available
		if l.diagnosticLogPath != "" {
			l.logDetectionAnomaly(ctx, workspacePath, openCount, dbOpenCount, readyCount, invisible)
		}
		// Suppress the alert - return no starvation despite CLI claim
		return false, openCount, invisible, nil
	}

	// Log verification result if counts differ (but don't suppress unless DB shows zero)
	if openCount != dbOpenCount {
		log.Printf("[RecoveryLoop] VERIFICATION DISCREPANCY in %s: CLI=%d open beads, DB=%d open beads. Proceeding with DB count as authoritative.",
			filepath.Base(workspacePath), openCount, dbOpenCount)
	}

	// Starvation condition: no ready beads but open/invisible beads exist
	// CRITICAL: dbOpenCount > 0 is mandatory - never create starvation alert when open beads is 0
	// The "invisible > 0" alone is insufficient; must have at least one open bead
	// This prevents false-positive alerts claiming "open beads exist" when count is 0
	hasStarvation = (readyCount == 0) && (dbOpenCount > 0)

	return hasStarvation, dbOpenCount, invisible, nil
}

// countBeadsByStatus counts beads with a given status.
func (l *StarvationRecoveryLoop) countBeadsByStatus(ctx context.Context, workspacePath string, status string) (int, error) {
	cmd := exec.CommandContext(ctx, "bead", "list", "--status", status, "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("bead list --status %s: %w", status, err)
	}

	// Parse JSON output and count beads
	count := strings.Count(string(output), `"id":`)
	return count, nil
}

// countInvisibleBeads counts beads that are effectively invisible to workers.
// These are beads that are open but assigned (stale assignment), or blocked.
func (l *StarvationRecoveryLoop) countInvisibleBeads(ctx context.Context, workspacePath string) (int, error) {
	cmd := exec.CommandContext(ctx, "bead", "list", "--status", "open", "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("bead list: %w", err)
	}

	// Parse output to find beads with assignees or manual_blocked
	invisible := 0
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, `"assignee":`) && !strings.Contains(line, `"assignee": ""`) &&
			!strings.Contains(line, `"assignee": null`) {
			invisible++
		}
		if strings.Contains(line, `"manual_blocked": true`) {
			invisible++
		}
	}

	return invisible, nil
}

// countReadyBeads counts beads that are actually ready for workers to claim.
// Uses PluckFallback if enabled, otherwise falls back to direct bead list --ready query.
func (l *StarvationRecoveryLoop) countReadyBeads(ctx context.Context, workspacePath string) (int, error) {
	// Use PluckFallback if enabled
	if l.pluckFallback != nil {
		candidates, metrics, discrepancies, err := l.pluckFallback.Pluck(ctx, workspacePath)
		if err != nil {
			log.Printf("[RecoveryLoop] PluckFallback failed for %s: %v", workspacePath, err)
			// Fall back to direct query as last resort
			return l.countReadyBeadsDirect(ctx, workspacePath)
		}

		// Log any discrepancies detected
		for _, d := range discrepancies {
			log.Printf("[RecoveryLoop] %s", d)
		}

		// Log metrics if available
		if metrics != nil {
			log.Printf("[RecoveryLoop] PluckFallback strategy: %s (last used: %s)",
				metrics.StrategyName, metrics.LastUsed.Format(time.RFC3339))
		}

		// If fallback was triggered, log it
		if metrics != nil && metrics.StrategyName != "primary" && len(candidates) > 0 {
			log.Printf("[RecoveryLoop] PluckFallback used %s strategy and recovered %d beads in %s",
				metrics.StrategyName, len(candidates), workspacePath)
		}

		return len(candidates), nil
	}

	// Fall back to direct query if PluckFallback is not enabled
	return l.countReadyBeadsDirect(ctx, workspacePath)
}

// countReadyBeadsDirect counts beads using the direct `bead list --ready` query.
// This is the fallback method when PluckFallback is not enabled.
func (l *StarvationRecoveryLoop) countReadyBeadsDirect(ctx context.Context, workspacePath string) (int, error) {
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

// verifyOpenBeadCountDirectDB performs self-verification by querying the database directly.
// Returns the count of open beads from the authoritative database source.
// This prevents false-positive starvation alerts based on stale CLI output.
func (l *StarvationRecoveryLoop) verifyOpenBeadCountDirectDB(ctx context.Context, workspacePath string) (int, error) {
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

	// Parse the count from output (sqlite3 returns just the number)
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

// logDetectionAnomaly logs a detection anomaly to the diagnostic log.
// Called when CLI reports open beads but direct DB query shows none.
func (l *StarvationRecoveryLoop) logDetectionAnomaly(ctx context.Context, workspacePath string, cliCount, dbCount, readyCount, invisibleCount int) {
	timestamp := time.Now().Format(time.RFC3339)
	anomalyLog := fmt.Sprintf(
		"[%s] DETECTION_ANOMALY workspace=%s cli_open=%d db_open=%d ready=%d invisible=%d message=CLI reported open beads but direct DB query showed zero. Starvation alert suppressed to prevent false positive.",
		timestamp, filepath.Base(workspacePath), cliCount, dbCount, readyCount, invisibleCount,
	)

	// Append to diagnostic log file
	if l.diagnosticLogPath != "" {
		f, err := os.OpenFile(l.diagnosticLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("[RecoveryLoop] Failed to open diagnostic log: %v", err)
			return
		}
		defer f.Close()

		if _, err := f.WriteString(anomalyLog + "\n"); err != nil {
			log.Printf("[RecoveryLoop] Failed to write to diagnostic log: %v", err)
		}
	}

	// Always log to stderr as well
	log.Printf("[RecoveryLoop] %s", anomalyLog)
}

// runAutomatedRecovery executes the automated recovery steps.
// Returns (success, details) where success indicates if recovery was complete.
func (l *StarvationRecoveryLoop) runAutomatedRecovery(ctx context.Context, workspacePath string) (bool, string) {
	var steps []string
	var errors []string

	// Step 1: Run bead doctor --repair to fix database corruption
	steps = append(steps, "Running bead doctor --repair")
	if err := l.runBeadDoctor(ctx, workspacePath); err != nil {
		errors = append(errors, fmt.Sprintf("bead doctor failed: %v", err))
	}

	// Step 2: Run validate_cross_repo_preconditions.sh to mark beads with unmet dependencies
	steps = append(steps, "Validating cross-repo preconditions")
	if err := l.runValidatePreconditions(ctx, workspacePath); err != nil {
		errors = append(errors, fmt.Sprintf("precondition validation failed: %v", err))
	}

	// Step 3: Check if NEEDLE workers are alive
	steps = append(steps, "Checking worker status")
	workersAlive, err := l.checkWorkersAlive(ctx)
	if err != nil {
		errors = append(errors, fmt.Sprintf("worker check failed: %v", err))
	}

	// Step 4: Re-evaluate ready frontier
	steps = append(steps, "Re-evaluating ready frontier")
	readyCount, err := l.countReadyBeads(ctx, workspacePath)
	if err != nil {
		errors = append(errors, fmt.Sprintf("failed to re-evaluate ready frontier: %v", err))
	}

	// Build details message
	details := fmt.Sprintf("Steps executed: %s. Ready beads after recovery: %d.",
		strings.Join(steps, ", "), readyCount)

	if len(errors) > 0 {
		details += fmt.Sprintf(" Errors: %s", strings.Join(errors, "; "))
	}

	// Recovery is successful if work became available
	success := readyCount > 0

	return success, details
}

// runBeadDoctor executes `bead doctor --repair` in the workspace.
func (l *StarvationRecoveryLoop) runBeadDoctor(ctx context.Context, workspacePath string) error {
	cmd := exec.CommandContext(ctx, "bead", "doctor", "--repair")
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}

	log.Printf("[RecoveryLoop] bead doctor --repair completed in %s", workspacePath)
	return nil
}

// runValidatePreconditions executes the cross-repo precondition validation script.
func (l *StarvationRecoveryLoop) runValidatePreconditions(ctx context.Context, workspacePath string) error {
	cmd := exec.CommandContext(ctx, l.validateScript, "--verbose")
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}

	log.Printf("[RecoveryLoop] precondition validation completed in %s", workspacePath)
	return nil
}

// checkWorkersAlive checks if NEEDLE workers are running and responsive.
func (l *StarvationRecoveryLoop) checkWorkersAlive(ctx context.Context) (bool, error) {
	// Check for NEEDLE worker processes
	cmd := exec.CommandContext(ctx, "pgrep", "-f", "needle.*worker")

	if err := cmd.Run(); err != nil {
		// No worker processes found
		return false, fmt.Errorf("no needle worker processes running")
	}

	// Workers are alive
	return true, nil
}

// Stop stops the starvation recovery loop.
func (l *StarvationRecoveryLoop) Stop() {
	if l == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.stopped {
		return
	}

	l.stopped = true
	close(l.stopCh)

	// Close PluckFallback if enabled
	if l.pluckFallback != nil {
		if err := l.pluckFallback.Close(); err != nil {
			log.Printf("[RecoveryLoop] Failed to close PluckFallback: %v", err)
		}
	}

	// Release lease leadership if configured
	if l.leaseLeader != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		l.leaseLeader.Release(ctx)
	}

	log.Printf("[RecoveryLoop] Starvation recovery loop stopped")
}

// IsRunning reports whether the recovery loop is currently running.
func (l *StarvationRecoveryLoop) IsRunning() bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return !l.stopped && (l.leaseLeader == nil || l.leaseLeader.IsLeader())
}
