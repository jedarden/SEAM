package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RecoveryResult holds the outcome of a recovery attempt.
type RecoveryResult struct {
	Workspace         string    `json:"workspace"`
	StartTime         time.Time `json:"start_time"`
	EndTime           time.Time `json:"end_time"`
	DurationMs        int64     `json:"duration_ms"`
	Success           bool      `json:"success"`
	OpenBeadsBefore   int       `json:"open_beads_before"`
	InvisibleBefore   int       `json:"invisible_before"`
	ReadyBefore       int       `json:"ready_before"`
	ReadyAfter        int       `json:"ready_after"`
	BeadDoctorRun     bool      `json:"bead_doctor_run"`
	BeadDoctorSuccess bool      `json:"bead_doctor_success"`
	PrecondsRun       bool      `json:"preconds_run"`
	PrecondsSuccess   bool      `json:"preconds_success"`
	WorkersAlive      bool      `json:"workers_alive"`
	Errors            []string  `json:"errors,omitempty"`
	Steps             []string  `json:"steps"`
}

func main() {
	var (
		workspaceRoot  = flag.String("workspace-root", "/home/coding", "Root directory containing all workspaces")
		validateScript = flag.String("validate-script", "", "Path to validate_cross_repo_preconditions.sh")
		once           = flag.Bool("once", false, "Run recovery once and exit (default: loop)")
		interval       = flag.Duration("interval", 5*time.Minute, "Check interval for loop mode")
		verbose        = flag.Bool("verbose", false, "Enable verbose logging")
		jsonOutput     = flag.Bool("json", false, "Output results in JSON format")
		dryRun         = flag.Bool("dry-run", false, "Show what would be done without making changes")
	)
	flag.Parse()

	log.SetFlags(0)
	if *verbose {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	}

	if *validateScript == "" {
		*validateScript = filepath.Join(*workspaceRoot, "SEAM", "tools", "validate_cross_repo_preconditions.sh")
	}

	// Validate that scripts exist
	if _, err := os.Stat(*validateScript); err != nil {
		log.Fatalf("Validate script not found: %s", *validateScript)
	}

	recovery := &RecoveryTool{
		workspaceRoot:  *workspaceRoot,
		validateScript: *validateScript,
		verbose:        *verbose,
		dryRun:         *dryRun,
		jsonOutput:     *jsonOutput,
	}

	if *once {
		// One-shot mode: run recovery once and exit
		log.Printf("Running one-shot starvation recovery...")
		results := recovery.runAllWorkspaces(context.Background())
		recovery.reportResults(results)

		// Exit with error if any recovery failed
		for _, result := range results {
			if !result.Success {
				os.Exit(1)
			}
		}
		os.Exit(0)
	}

	// Loop mode: run continuously
	log.Printf("Starting starvation recovery loop (interval: %v)", *interval)
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		results := recovery.runAllWorkspaces(context.Background())
		recovery.reportResults(results)

		select {
		case <-ticker.C:
			// Continue loop
		case <-time.After(1 * time.Second):
			// Continue loop
		}
	}
}

// RecoveryTool implements the starvation recovery logic.
type RecoveryTool struct {
	workspaceRoot  string
	validateScript string
	verbose        bool
	dryRun         bool
	jsonOutput     bool
}

// runAllWorkspaces runs recovery on all workspaces with bead databases.
func (r *RecoveryTool) runAllWorkspaces(ctx context.Context) []*RecoveryResult {
	workspaces, err := r.findWorkspacesWithBeads()
	if err != nil {
		log.Printf("Failed to find workspaces: %v", err)
		return nil
	}

	var results []*RecoveryResult
	for _, workspace := range workspaces {
		result := r.runRecovery(ctx, workspace)
		results = append(results, result)
	}

	return results
}

// findWorkspacesWithBeads finds all directories with .beads/beads.db.
func (r *RecoveryTool) findWorkspacesWithBeads() ([]string, error) {
	var workspaces []string

	entries, err := os.ReadDir(r.workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("read workspace root: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		workspacePath := filepath.Join(r.workspaceRoot, entry.Name())
		beadDB := filepath.Join(workspacePath, ".beads", "beads.db")

		if _, err := os.Stat(beadDB); err == nil {
			workspaces = append(workspaces, workspacePath)
		}
	}

	return workspaces, nil
}

// runRecovery runs the full recovery workflow on a single workspace.
func (r *RecoveryTool) runRecovery(ctx context.Context, workspacePath string) *RecoveryResult {
	workspaceName := filepath.Base(workspacePath)
	startTime := time.Now()

	result := &RecoveryResult{
		Workspace:   workspaceName,
		StartTime:  startTime,
		Steps:      []string{},
		Errors:     []string{},
	}

	log.Printf("[%s] Checking workspace for starvation...", workspaceName)

	// Check initial state
	openBeads, invisible, ready, err := r.getBeadState(ctx, workspacePath)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("Failed to get bead state: %v", err))
		result.EndTime = time.Now()
		result.DurationMs = result.EndTime.Sub(startTime).Milliseconds()
		result.Success = false
		return result
	}

	result.OpenBeadsBefore = openBeads
	result.InvisibleBefore = invisible
	result.ReadyBefore = ready

	log.Printf("[%s] State: open=%d, invisible=%d, ready=%d", workspaceName, openBeads, invisible, ready)

	// Check if starvation condition exists
	// CRITICAL: openBeads > 0 is mandatory - never create starvation alert when open beads is 0
	// The "invisible > 0" alone is insufficient; must have at least one open bead
	// This prevents false-positive alerts claiming "open beads exist" when count is 0
	hasStarvation := (ready == 0) && (openBeads > 0)
	if !hasStarvation {
		log.Printf("[%s] No starvation detected (ready beads available)", workspaceName)
		result.Success = true
		result.EndTime = time.Now()
		result.DurationMs = result.EndTime.Sub(startTime).Milliseconds()
		return result
	}

	log.Printf("[%s] Starvation detected! Running automated recovery...", workspaceName)

	// Step 1: Run bead doctor --repair
	result.Steps = append(result.Steps, "Running bead doctor --repair")
	if !r.dryRun {
		if err := r.runBeadDoctor(ctx, workspacePath); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("bead doctor failed: %v", err))
			result.BeadDoctorSuccess = false
		} else {
			result.BeadDoctorSuccess = true
		}
		result.BeadDoctorRun = true
	} else {
		log.Printf("[%s] [DRY-RUN] Would run: bead doctor --repair", workspaceName)
		result.BeadDoctorRun = true
		result.BeadDoctorSuccess = true // Assume success in dry-run
	}

	// Step 2: Run validate_cross_repo_preconditions.sh
	result.Steps = append(result.Steps, "Validating cross-repo preconditions")
	if !r.dryRun {
		if err := r.runValidatePreconditions(ctx, workspacePath); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("precondition validation failed: %v", err))
			result.PrecondsSuccess = false
		} else {
			result.PrecondsSuccess = true
		}
		result.PrecondsRun = true
	} else {
		log.Printf("[%s] [DRY-RUN] Would run: %s", workspaceName, r.validateScript)
		result.PrecondsRun = true
		result.PrecondsSuccess = true
	}

	// Step 3: Check if workers are alive
	result.Steps = append(result.Steps, "Checking worker status")
	workersAlive, err := r.checkWorkersAlive(ctx)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("worker check failed: %v", err))
	}
	result.WorkersAlive = workersAlive

	// Step 4: Re-evaluate ready frontier
	result.Steps = append(result.Steps, "Re-evaluating ready frontier")
	readyAfter, err := r.countReadyBeads(ctx, workspacePath)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to count ready beads: %v", err))
	}
	result.ReadyAfter = readyAfter

	// Determine success
	result.Success = readyAfter > 0
	result.EndTime = time.Now()
	result.DurationMs = result.EndTime.Sub(startTime).Milliseconds()

	// Log outcome
	if result.Success {
		log.Printf("[%s] Recovery succeeded! Ready beads: %d (was %d)", workspaceName, readyAfter, ready)
	} else {
		log.Printf("[%s] Recovery incomplete. Ready beads: %d. Manual intervention may be needed.", workspaceName, readyAfter)
		if len(result.Errors) > 0 {
			log.Printf("[%s] Errors: %s", workspaceName, strings.Join(result.Errors, "; "))
		}
	}

	return result
}

// getBeadState gets the current bead state counts.
func (r *RecoveryTool) getBeadState(ctx context.Context, workspacePath string) (open, invisible, ready int, err error) {
	// Count open beads
	open, err = r.countBeadsByStatus(ctx, workspacePath, "open")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count open beads: %w", err)
	}

	// Count invisible beads
	invisible, err = r.countInvisibleBeads(ctx, workspacePath)
	if err != nil {
		return open, 0, 0, fmt.Errorf("count invisible beads: %w", err)
	}

	// Count ready beads
	ready, err = r.countReadyBeads(ctx, workspacePath)
	if err != nil {
		return open, invisible, 0, fmt.Errorf("count ready beads: %w", err)
	}

	return open, invisible, ready, nil
}

// countBeadsByStatus counts beads with a given status.
func (r *RecoveryTool) countBeadsByStatus(ctx context.Context, workspacePath string, status string) (int, error) {
	cmd := exec.CommandContext(ctx, "bead", "list", "--status", status, "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("bead list --status %s: %w", status, err)
	}

	// Parse JSON output and count beads
	var beads []map[string]interface{}
	if err := json.Unmarshal(output, &beads); err != nil {
		return 0, fmt.Errorf("parse JSON: %w", err)
	}

	return len(beads), nil
}

// countInvisibleBeads counts beads that are effectively invisible to workers.
func (r *RecoveryTool) countInvisibleBeads(ctx context.Context, workspacePath string) (int, error) {
	cmd := exec.CommandContext(ctx, "bead", "list", "--status", "open", "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("bead list: %w", err)
	}

	var beads []map[string]interface{}
	if err := json.Unmarshal(output, &beads); err != nil {
		return 0, fmt.Errorf("parse JSON: %w", err)
	}

	invisible := 0
	for _, bead := range beads {
		// Check if bead has an assignee (stale assignment)
		if assignee, ok := bead["assignee"].(string); ok && assignee != "" {
			invisible++
			continue
		}

		// Check if bead is manually blocked
		if blocked, ok := bead["manual_blocked"].(bool); ok && blocked {
			invisible++
		}
	}

	return invisible, nil
}

// countReadyBeads counts beads that are ready for workers to claim.
func (r *RecoveryTool) countReadyBeads(ctx context.Context, workspacePath string) (int, error) {
	cmd := exec.CommandContext(ctx, "bead", "list", "--ready", "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		// No ready beads is not an error
		return 0, nil
	}

	var beads []map[string]interface{}
	if err := json.Unmarshal(output, &beads); err != nil {
		return 0, fmt.Errorf("parse JSON: %w", err)
	}

	return len(beads), nil
}

// runBeadDoctor executes `bead doctor --repair`.
func (r *RecoveryTool) runBeadDoctor(ctx context.Context, workspacePath string) error {
	cmd := exec.CommandContext(ctx, "bead", "doctor", "--repair")
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}

	if r.verbose {
		log.Printf("bead doctor output:\n%s", string(output))
	}

	return nil
}

// runValidatePreconditions executes the cross-repo precondition validation script.
func (r *RecoveryTool) runValidatePreconditions(ctx context.Context, workspacePath string) error {
	cmd := exec.CommandContext(ctx, r.validateScript, "--verbose")
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}

	if r.verbose {
		log.Printf("Precondition validation output:\n%s", string(output))
	}

	return nil
}

// checkWorkersAlive checks if NEEDLE workers are running.
func (r *RecoveryTool) checkWorkersAlive(ctx context.Context) (bool, error) {
	// Check for NEEDLE worker processes
	cmd := exec.CommandContext(ctx, "pgrep", "-f", "needle.*worker")

	if err := cmd.Run(); err != nil {
		// No worker processes found
		return false, nil
	}

	return true, nil
}

// reportResults reports the recovery results to stdout.
func (r *RecoveryTool) reportResults(results []*RecoveryResult) {
	if r.jsonOutput {
		// JSON output
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		for _, result := range results {
			encoder.Encode(result)
		}
		return
	}

	// Human-readable output
	fmt.Printf("\n=== Starvation Recovery Report ===\n")
	fmt.Printf("Time: %s\n", time.Now().Format(time.RFC3339))
	fmt.Printf("Workspaces checked: %d\n\n", len(results))

	for _, result := range results {
		fmt.Printf("Workspace: %s\n", result.Workspace)
		fmt.Printf("  Duration: %dms\n", result.DurationMs)
		fmt.Printf("  Initial state: open=%d, invisible=%d, ready=%d\n",
			result.OpenBeadsBefore, result.InvisibleBefore, result.ReadyBefore)
		fmt.Printf("  Final state: ready=%d\n", result.ReadyAfter)
		fmt.Printf("  Steps: %s\n", strings.Join(result.Steps, ", "))

		if result.Success {
			fmt.Printf("  Result: ✓ SUCCESS\n")
		} else {
			fmt.Printf("  Result: ✗ INCOMPLETE\n")
			if len(result.Errors) > 0 {
				fmt.Printf("  Errors:\n")
				for _, err := range result.Errors {
					fmt.Printf("    - %s\n", err)
				}
			}
		}
		fmt.Printf("\n")
	}
}
