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

	return &StarvationRecoveryLoop{
		leaseLeader:        cfg.LeaseLeader,
		workspaceRoot:      cfg.WorkspaceRoot,
		validateScript:     cfg.ValidateScript,
		checkInterval:      cfg.CheckInterval,
		maxAttemptsPerBead: cfg.MaxAttemptsPerBead,
		recoveryAttempts:   make(map[string]int),
		stopCh:            make(chan struct{}),
		onRecoveryComplete: cfg.OnRecoveryComplete,
	}, nil
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
func (l *StarvationRecoveryLoop) detectStarvation(ctx context.Context, workspacePath string) (hasStarvation bool, openBeadsCount int, invisibleCount int, err error) {
	// Count open beads
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

	// Starvation condition: no ready beads but open/invisible beads exist
	hasStarvation = (readyCount == 0) && (openCount > 0 || invisible > 0)

	return hasStarvation, openCount, invisible, nil
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
func (l *StarvationRecoveryLoop) countReadyBeads(ctx context.Context, workspacePath string) (int, error) {
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
