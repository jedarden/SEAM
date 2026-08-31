package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RecoveryStrategy defines a single recovery attempt.
type RecoveryStrategy struct {
	Name        string // Strategy name for logging
	Description string // Human-readable description
	Command     string // Command to execute
	Args        []string // Arguments for the command
	ExpectedRootCause string // If this succeeds, the bead will be tagged with this root cause
}

// CascadingRecoveryResult holds the result of attempting all recovery strategies.
type CascadingRecoveryResult struct {
	Success           bool     // Whether any strategy succeeded
	SuccessfulStrategy string   // Which strategy worked
	InferredRootCause string   // Root cause inferred from the successful strategy
	AttemptedStrategies []string // List of strategies attempted
	Diagnostics       string   // Full diagnostic output
	ReadyBefore       int      // Ready bead count before recovery
	ReadyAfter        int      // Ready bead count after successful recovery
}

// RootCauseAnalyzer performs automated root cause analysis for starvation alerts.
// It analyzes which recovery strategy succeeded, database integrity, checkpoint
// freshness, index integrity, filter configurations, and worker status to categorize
// incidents and determine auto-recoverability.
type RootCauseAnalyzer struct {
	checkpointFreshnessThreshold time.Duration // How stale before checkpoint is considered stale (default: 5 minutes)
	workerStaleThreshold          time.Duration // How long before worker is considered stuck (default: 10 minutes)
	confidenceThreshold           float64       // Minimum confidence for auto-recovery (default: 0.5)
}

// NewRootCauseAnalyzer creates a new root cause analyzer.
func NewRootCauseAnalyzer() *RootCauseAnalyzer {
	return &RootCauseAnalyzer{
		checkpointFreshnessThreshold: 5 * time.Minute,
		workerStaleThreshold:        10 * time.Minute,
		confidenceThreshold:         0.5,
	}
}

// getCascadingRecoveryStrategies returns the ordered list of recovery strategies
// to try for unknown-cause failures, ordered by severity (least invasive first).
func (rca *RootCauseAnalyzer) getCascadingRecoveryStrategies() []RecoveryStrategy {
	return []RecoveryStrategy{
		{
			Name:        "database-repair",
			Description: "Run bead doctor --repair to fix database corruption",
			Command:     "bead",
			Args:        []string{"doctor", "--repair"},
			ExpectedRootCause: "database-corrupt",
		},
		{
			Name:        "checkpoint-sync",
			Description: "Flush checkpoint to resync database state",
			Command:     "bead",
			Args:        []string{"sync", "flush-only"},
			ExpectedRootCause: "checkpoint-out-of-sync",
		},
		{
			Name:        "bead-release",
			Description: "Release stale bead assignments",
			Command:     "bead",
			Args:        []string{"list", "--status", "open", "--json"},
			ExpectedRootCause: "stale-assignment",
		},
		{
			Name:        "index-rebuild",
			Description: "Rebuild database from checkpoint (most invasive)",
			Command:     "bead",
			Args:        []string{"init"},
			ExpectedRootCause: "index-corrupt",
		},
	}
}

// AnalysisResult contains the root cause analysis results.
type AnalysisResult struct {
	RootCause        string   // Categorized root cause (e.g., "database-corrupt", "filter-mismatch")
	AutoRecovered     bool     // Whether the issue was automatically recoverable
	Strategy          string   // Which PluckFallback strategy succeeded
	DBHealthy         bool     // Database integrity check result
	CheckpointFresh   bool     // Checkpoint freshness check result
	IndexHealthy      bool     // Index integrity check result
	FilterConfigValid bool     // Filter configuration validation result
	WorkersAlive      bool     // Worker status check result
	Details           []string // Detailed analysis notes
	RecommendedLabel  string   // Recommended label for tagging
	HumanBlocked      bool     // Whether human intervention is required
	Repairable        bool     // Whether the issue can be auto-repaired
}

// Analyze performs root cause analysis based on the recovery strategy and workspace state.
func (rca *RootCauseAnalyzer) Analyze(ctx context.Context, workspacePath string, strategy string, readyCount int, history interface{}) (rootCause string, autoRecovered bool, details string) {
	workspaceName := filepath.Base(workspacePath)
	result := AnalysisResult{
		Strategy:          strategy,
		ReadyCount:        readyCount,
		Details:           []string{},
		DBHealthy:         true,  // Assume healthy until proven otherwise
		CheckpointFresh:    true,  // Assume fresh until proven otherwise
		IndexHealthy:      true,  // Assume healthy until proven otherwise
		FilterConfigValid: true,  // Assume valid until proven otherwise
		WorkersAlive:      true,  // Assume alive until proven otherwise
	}

	log.Printf("[RootCauseAnalyzer] Analyzing starvation resolution in %s (strategy=%s, ready=%d)",
		workspaceName, strategy, readyCount)

	// Step 1: Check database integrity using bead doctor --json
	dbHealthy, dbDetails := rca.checkDatabaseIntegrity(ctx, workspacePath)
	result.DBHealthy = dbHealthy
	result.Details = append(result.Details, dbDetails...)

	// Step 2: Verify checkpoint freshness
	checkpointFresh, checkpointDetails := rca.checkCheckpointFreshness(ctx, workspacePath)
	result.CheckpointFresh = checkpointFresh
	result.Details = append(result.Details, checkpointDetails...)

	// Step 3: Check index integrity
	indexHealthy, indexDetails := rca.checkIndexIntegrity(ctx, workspacePath)
	result.IndexHealthy = indexHealthy
	result.Details = append(result.Details, indexDetails...)

	// Step 4: Verify filter configurations
	filterValid, filterDetails := rca.checkFilterConfiguration(ctx, workspacePath)
	result.FilterConfigValid = filterValid
	result.Details = append(result.Details, filterDetails...)

	// Step 5: Check worker status
	workersAlive, workerDetails := rca.checkWorkerStatus(ctx, workspacePath)
	result.WorkersAlive = workersAlive
	result.Details = append(result.Details, workerDetails...)

	// Step 6: Determine root cause based on strategy and diagnostics
	rootCause, autoRecovered = rca.categorizeRootCause(&result)
	result.RootCause = rootCause
	result.AutoRecovered = autoRecovered
	result.RecommendedLabel = fmt.Sprintf("starvation:%s", rootCause)

	// Step 7: Determine if human intervention or auto-repair is needed
	rca.determineActionability(&result)

	// Build detailed output string
	detailsStr := fmt.Sprintf("**Root Cause:** %s\n", rootCause)
	detailsStr += fmt.Sprintf("**Auto-Recovered:** %v\n", autoRecovered)
	detailsStr += fmt.Sprintf("**Human Intervention Required:** %v\n", result.HumanBlocked)
	detailsStr += fmt.Sprintf("**Auto-Repairable:** %v\n\n", result.Repairable)
	detailsStr += "**Analysis:**\n"
	for i, detail := range result.Details {
		detailsStr += fmt.Sprintf("%d. %s\n", i+1, detail)
	}

	detailsStr += fmt.Sprintf("\n**Diagnostic Results:**\n")
	detailsStr += fmt.Sprintf("- Database Integrity: %v\n", result.DBHealthy)
	detailsStr += fmt.Sprintf("- Index Integrity: %v\n", result.IndexHealthy)
	detailsStr += fmt.Sprintf("- Checkpoint Freshness: %v\n", result.CheckpointFresh)
	detailsStr += fmt.Sprintf("- Filter Configuration: %v\n", result.FilterConfigValid)
	detailsStr += fmt.Sprintf("- Worker Status: %v\n", result.WorkersAlive)
	detailsStr += fmt.Sprintf("- Recovery Strategy: %s\n", result.Strategy)
	detailsStr += fmt.Sprintf("- Recommended Label: %s\n", result.RecommendedLabel)

	log.Printf("[RootCauseAnalyzer] Analysis complete for %s: root_cause=%s, auto_recovered=%v, human_blocked=%v, repairable=%v",
		workspaceName, rootCause, autoRecovered, result.HumanBlocked, result.Repairable)

	return rootCause, autoRecovered, detailsStr
}

// checkDatabaseIntegrity runs bead doctor --json to check database schema integrity.
func (rca *RootCauseAnalyzer) checkDatabaseIntegrity(ctx context.Context, workspacePath string) (healthy bool, details []string) {
	workspaceName := filepath.Base(workspacePath)

	log.Printf("[RootCauseAnalyzer] Checking database integrity in %s", workspaceName)

	cmd := exec.CommandContext(ctx, "bead", "doctor", "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		// bead doctor --json might not be supported, fall back to --repair check
		return rca.checkDatabaseIntegrityFallback(ctx, workspacePath)
	}

	// Parse JSON output
	var doctorResult map[string]interface{}
	if err := json.Unmarshal(output, &doctorResult); err != nil {
		details = append(details, fmt.Sprintf("⚠️  Could not parse bead doctor JSON output: %v", err))
		return true, details // Assume healthy if we can't parse
	}

	// Check for errors in the output
	if errors, ok := doctorResult["errors"].([]interface{}); ok && len(errors) > 0 {
		details = append(details, fmt.Sprintf("❌ Database integrity issues found: %d errors", len(errors)))
		for i, errItem := range errors {
			if i >= 5 { // Limit to 5 errors to avoid spam
				details = append(details, fmt.Sprintf("   ... and %d more", len(errors)-5))
				break
			}
			details = append(details, fmt.Sprintf("   - %v", errItem))
		}
		return false, details
	}

	// Check for warnings
	if warnings, ok := doctorResult["warnings"].([]interface{}); ok && len(warnings) > 0 {
		details = append(details, fmt.Sprintf("⚠️  Database warnings: %d", len(warnings)))
		for i, warnItem := range warnings {
			if i >= 3 {
				details = append(details, fmt.Sprintf("   ... and %d more", len(warnings)-3))
				break
			}
			details = append(details, fmt.Sprintf("   - %v", warnItem))
		}
	}

	// Check for healthy status
	if healthy, ok := doctorResult["healthy"].(bool); ok {
		if healthy {
			details = append(details, "✓ Database integrity check passed")
		} else {
			details = append(details, "❌ Database marked as unhealthy by bead doctor")
			return false, details
		}
	}

	details = append(details, "✓ Database integrity verified")
	return true, details
}

// checkDatabaseIntegrityFallback runs bead doctor without --json flag.
func (rca *RootCauseAnalyzer) checkDatabaseIntegrityFallback(ctx context.Context, workspacePath string) (healthy bool, details []string) {
	cmd := exec.CommandContext(ctx, "bead", "doctor")
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		details = append(details, fmt.Sprintf("⚠️  bead doctor check failed: %v", err))
		return true, details // Assume healthy if check itself fails
	}

	outputStr := strings.ToLower(string(output))

	// Look for common error indicators
	if strings.Contains(outputStr, "integrity check failed") ||
	   strings.Contains(outputStr, "database corruption") ||
	   strings.Contains(outputStr, "schema mismatch") {
		details = append(details, "❌ Database integrity issues detected")
		details = append(details, "   "+string(output))
		return false, details
	}

	details = append(details, "✓ Database integrity check passed (fallback method)")
	return true, details
}

// checkCheckpointFreshness verifies that the checkpoint is not stale relative to the database.
func (rca *RootCauseAnalyzer) checkCheckpointFreshness(ctx context.Context, workspacePath string) (fresh bool, details []string) {
	workspaceName := filepath.Base(workspacePath)

	log.Printf("[RootCauseAnalyzer] Checking checkpoint freshness in %s", workspaceName)

	dbPath := filepath.Join(workspacePath, ".beads", "beads.db")
	checkpointPath := filepath.Join(workspacePath, ".beads", "checkpoint", "current.json")

	// Get database modification time
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		details = append(details, fmt.Sprintf("⚠️  Could not stat database: %v", err))
		return true, details // Assume fresh if we can't check
	}
	dbMtime := dbInfo.ModTime()

	// Get checkpoint modification time
	checkpointInfo, err := os.Stat(checkpointPath)
	if err != nil {
		details = append(details, fmt.Sprintf("⚠️  Could not stat checkpoint: %v", err))
		return true, details // Assume fresh if we can't check
	}
	checkpointMtime := checkpointInfo.ModTime()

	// Calculate age difference
	ageDiff := dbMtime.Sub(checkpointMtime)

	// Check if checkpoint is significantly older than database (potential desync)
	if ageDiff > rca.checkpointFreshnessThreshold {
		details = append(details, fmt.Sprintf("⚠️  Checkpoint stale: checkpoint is %v older than database (threshold: %v)",
			ageDiff, rca.checkpointFreshnessThreshold))
		details = append(details, fmt.Sprintf("   Checkpoint mtime: %s", checkpointMtime.Format(time.RFC3339)))
		details = append(details, fmt.Sprintf("   Database mtime: %s", dbMtime.Format(time.RFC3339)))
		return false, details
	}

	// Check if database is significantly older than checkpoint (potential recovery in progress)
	if ageDiff < -rca.checkpointFreshnessThreshold {
		details = append(details, fmt.Sprintf("⚠️  Database stale: database is %v older than checkpoint (threshold: %v)",
			-ageDiff, rca.checkpointFreshnessThreshold))
		details = append(details, fmt.Sprintf("   Database mtime: %s", dbMtime.Format(time.RFC3339)))
		details = append(details, fmt.Sprintf("   Checkpoint mtime: %s", checkpointMtime.Format(time.RFC3339)))
		return false, details
	}

	details = append(details, fmt.Sprintf("✓ Checkpoint fresh: age difference %v within threshold %v",
		ageDiff, rca.checkpointFreshnessThreshold))
	return true, details
}

// checkIndexIntegrity performs SQLite index integrity checks to detect corruption.
func (rca *RootCauseAnalyzer) checkIndexIntegrity(ctx context.Context, workspacePath string) (healthy bool, details []string) {
	workspaceName := filepath.Base(workspacePath)
	dbPath := filepath.Join(workspacePath, ".beads", "beads.db")

	log.Printf("[RootCauseAnalyzer] Checking index integrity in %s", workspaceName)

	// Run SQLite integrity check
	cmd := exec.CommandContext(ctx, "sqlite3", dbPath, "PRAGMA integrity_check;")
	output, err := cmd.Output()
	if err != nil {
		details = append(details, fmt.Sprintf("⚠️  Could not run integrity check: %v", err))
		return true, details // Assume healthy if we can't check
	}

	outputStr := strings.TrimSpace(string(output))

	// SQLite returns "ok" if integrity check passed
	if outputStr == "ok" {
		details = append(details, "✓ Index integrity check passed")
		return true, details
	}

	// Any other output indicates corruption
	details = append(details, fmt.Sprintf("❌ Index integrity check failed: %s", outputStr))
	return false, details
}

// checkFilterConfiguration verifies that CLI queries match database state to detect filter mismatches.
func (rca *RootCauseAnalyzer) checkFilterConfiguration(ctx context.Context, workspacePath string) (valid bool, details []string) {
	workspaceName := filepath.Base(workspacePath)

	log.Printf("[RootCauseAnalyzer] Checking filter configuration in %s", workspaceName)

	// Get open bead count via CLI
	cliCmd := exec.CommandContext(ctx, "bead", "list", "--status", "open", "--json")
	cliCmd.Dir = workspacePath
	cliOutput, err := cliCmd.Output()
	if err != nil {
		details = append(details, fmt.Sprintf("⚠️  Could not query CLI: %v", err))
		return true, details // Assume valid if we can't check
	}

	var cliBeads []map[string]interface{}
	if err := json.Unmarshal(cliOutput, &cliBeads); err != nil {
		details = append(details, fmt.Sprintf("⚠️  Could not parse CLI output: %v", err))
		return true, details
	}

	cliCount := len(cliBeads)

	// Get open bead count via direct database query
	dbPath := filepath.Join(workspacePath, ".beads", "beads.db")
	dbCmd := exec.CommandContext(ctx, "sqlite3", dbPath, "SELECT COUNT(*) FROM issues WHERE base_status = 'open';")
	dbOutput, err := dbCmd.Output()
	if err != nil {
		details = append(details, fmt.Sprintf("⚠️  Could not query database: %v", err))
		return true, details // Assume valid if we can't check
	}

	var dbCount int
	fmt.Sscanf(strings.TrimSpace(string(dbOutput)), "%d", &dbCount)

	// Compare counts
	if cliCount == dbCount {
		details = append(details, fmt.Sprintf("✓ Filter configuration valid: CLI and DB agree on %d open beads", cliCount))
		return true, details
	}

	// Mismatch detected
	details = append(details, fmt.Sprintf("❌ Filter configuration mismatch: CLI reports %d open beads, DB has %d", cliCount, dbCount))
	details = append(details, fmt.Sprintf("   This indicates a CLI query filter bug causing bead visibility issues"))

	return false, details
}

// checkWorkerStatus determines if NEEDLE workers are alive and responsive.
func (rca *RootCauseAnalyzer) checkWorkerStatus(ctx context.Context, workspacePath string) (alive bool, details []string) {
	log.Printf("[RootCauseAnalyzer] Checking worker status")

	// Check for NEEDLE worker processes
	cmd := exec.CommandContext(ctx, "pgrep", "-f", "needle.*worker")
	output, err := cmd.Output()
	if err != nil {
		details = append(details, fmt.Sprintf("❌ No NEEDLE worker processes detected"))
		details = append(details, fmt.Sprintf("   Worker starvation: no workers available to process beads"))
		return false, details
	}

	// Workers are alive
	pids := strings.TrimSpace(string(output))
	workerCount := len(strings.Split(pids, "\n"))

	details = append(details, fmt.Sprintf("✓ Workers alive: %d worker process(es) detected", workerCount))
	return true, details
}

// determineActionability determines if the issue requires human intervention or can be auto-repaired.
func (rca *RootCauseAnalyzer) determineActionability(result *AnalysisResult) {
	// Default: not human blocked, repairable
	result.HumanBlocked = false
	result.Repairable = true

	// Determine if human intervention is required
	// Only mark as human-blocked if the issue truly requires business logic decisions
	// or external dependency resolution that automation cannot handle

	// Index corruption is auto-repairable via rebuild
	if !result.IndexHealthy {
		result.Repairable = true
		result.HumanBlocked = false
		return
	}

	// Checkpoint desync is auto-repairable via flush/rebuild
	if !result.CheckpointFresh {
		result.Repairable = true
		result.HumanBlocked = false
		return
	}

	// Database corruption is auto-repairable via doctor --repair
	if !result.DBHealthy {
		result.Repairable = true
		result.HumanBlocked = false
		return
	}

	// Filter mismatch may require investigation but is often auto-repairable
	if !result.FilterConfigValid {
		result.Repairable = true
		result.HumanBlocked = false
		return
	}

	// Worker stuck is auto-recoverable (workers restart automatically)
	if !result.WorkersAlive {
		result.Repairable = false // Workers restart on their own
		result.HumanBlocked = false
		return
	}

	// If all checks pass but we still have starvation, it might be a transient issue
	// Transient issues are auto-recoverable by definition
	result.HumanBlocked = false
	result.Repairable = false
}

// categorizeRootCause determines the root cause category based on strategy and diagnostics.
func (rca *RootCauseAnalyzer) categorizeRootCause(result *AnalysisResult) (rootCause string, autoRecovered bool) {
	// Priority 1: Check for critical infrastructure failures first

	// Index corruption is the most serious - database structure is broken
	if !result.IndexHealthy {
		return "index-corrupt", true
	}

	// Database corruption prevents all operations
	if !result.DBHealthy {
		return "database-corrupt", true
	}

	// Checkpoint desync causes stale/incorrect data
	if !result.CheckpointFresh {
		return "checkpoint-out-of-sync", true
	}

	// Filter mismatch causes beads to be invisible
	if !result.FilterConfigValid {
		return "filter-mismatch", true
	}

	// Worker issues prevent processing
	if !result.WorkersAlive {
		return "worker-stuck", false
	}

	// Priority 2: Map strategies to root causes when all infrastructure checks pass
	switch result.Strategy {
	case "primary":
		// Primary query succeeded - starvation was transient or already resolved
		return "transient-starvation", true

	case "open_unassigned":
		// Found unassigned beads - suggests stale assignment bug
		return "stale-assignment", true

	case "open_status":
		// Found open beads - suggests primary query bug or visibility issue
		return "query-bug", true

	case "direct_db":
		// Direct DB query succeeded - primary CLI is broken
		return "cli-failure", true

	case "checkpoint":
		// Checkpoint query succeeded - fallback strategy
		// If we reach here with checkpoint strategy but all checks passed,
		// it means the primary queries are failing for unknown reasons
		return "primary-query-failure", true

	default:
		// Unknown strategy - should not happen
		return "unknown-cause", false
	}
}

// AttemptCascadingRecovery tries all documented auto-recovery strategies in order
// of severity when root cause analysis returns 'unknown-cause' or low confidence.
// It verifies recovery after each attempt by checking if ready beads are available.
func (rca *RootCauseAnalyzer) AttemptCascadingRecovery(ctx context.Context, workspacePath string) (*CascadingRecoveryResult, error) {
	workspaceName := filepath.Base(workspacePath)
	log.Printf("[RootCauseAnalyzer] Starting cascading recovery in %s", workspaceName)

	// Get initial ready bead count
	readyBefore, err := rca.countReadyBeads(ctx, workspacePath)
	if err != nil {
		return nil, fmt.Errorf("count ready beads before recovery: %w", err)
	}

	result := &CascadingRecoveryResult{
		AttemptedStrategies: make([]string, 0),
		ReadyBefore:         readyBefore,
		Diagnostics:         fmt.Sprintf("**Cascading Recovery for %s**\n\nInitial ready count: %d\n\n", workspaceName, readyBefore),
	}

	// Try each recovery strategy in order
	strategies := rca.getCascadingRecoveryStrategies()
	for i, strategy := range strategies {
		strategyNum := i + 1
		log.Printf("[RootCauseAnalyzer] Attempting strategy %d/%d: %s in %s",
			strategyNum, len(strategies), strategy.Name, workspaceName)

		result.AttemptedStrategies = append(result.AttemptedStrategies, strategy.Name)
		result.Diagnostics += fmt.Sprintf("### Strategy %d: %s\n", strategyNum, strategy.Name)
		result.Diagnostics += fmt.Sprintf("Description: %s\n", strategy.Description)
		result.Diagnostics += fmt.Sprintf("Command: %s %s\n", strategy.Command, strings.Join(strategy.Args, " "))

		// Execute the recovery strategy
		success, output, err := rca.executeRecoveryStrategy(ctx, workspacePath, strategy)
		result.Diagnostics += fmt.Sprintf("Success: %v\n", success)

		if err != nil {
			result.Diagnostics += fmt.Sprintf("Error: %v\n", err)
			result.Diagnostics += fmt.Sprintf("Output: %s\n\n", output)
		} else {
			result.Diagnostics += fmt.Sprintf("Output: %s\n\n", output)
		}

		// Special handling for bead-release strategy
		if strategy.Name == "bead-release" {
			releaseSuccess, releaseOutput := rca.releaseStaleAssignments(ctx, workspacePath, output)
			result.Diagnostics += fmt.Sprintf("Released %d stale assignments\n", releaseSuccess)
			result.Diagnostics += fmt.Sprintf("Release output: %s\n\n", releaseOutput)
			success = releaseSuccess > 0
		}

		// Verify if recovery succeeded by checking ready bead count
		if success || err == nil {
			readyAfter, err := rca.countReadyBeads(ctx, workspacePath)
			if err != nil {
				log.Printf("[RootCauseAnalyzer] Failed to verify recovery: %v", err)
				result.Diagnostics += fmt.Sprintf("Verification failed: %v\n\n", err)
				continue
			}

			result.Diagnostics += fmt.Sprintf("Ready count after: %d\n", readyAfter)

			// Check if recovery was successful (ready beads now available)
			if readyAfter > 0 {
				log.Printf("[RootCauseAnalyzer] ✓ Strategy %s succeeded: %d ready beads available in %s",
					strategy.Name, readyAfter, workspaceName)

				result.Success = true
				result.SuccessfulStrategy = strategy.Name
				result.InferredRootCause = strategy.ExpectedRootCause
				result.ReadyAfter = readyAfter
				result.Diagnostics += fmt.Sprintf("**RECOVERY SUCCESSFUL**\n")
				result.Diagnostics += fmt.Sprintf("Inferred root cause: %s\n", strategy.ExpectedRootCause)
				result.Diagnostics += fmt.Sprintf("Ready beads: %d → %d\n", readyBefore, readyAfter)

				return result, nil
			}

			result.Diagnostics += fmt.Sprintf("No improvement (ready count still %d)\n\n", readyAfter)
		} else {
			result.Diagnostics += fmt.Sprintf("Strategy failed, moving to next\n\n")
		}

		log.Printf("[RootCauseAnalyzer] Strategy %s did not resolve starvation in %s",
			strategy.Name, workspaceName)
	}

	// All strategies failed
	log.Printf("[RootCauseAnalyzer] All cascading recovery strategies failed for %s", workspaceName)
	result.Diagnostics += fmt.Sprintf("**ALL STRATEGIES FAILED**\n")
	result.Diagnostics += fmt.Sprintf("Requires manual investigation and escalation\n")

	return result, nil
}

// executeRecoveryStrategy executes a single recovery strategy.
func (rca *RootCauseAnalyzer) executeRecoveryStrategy(ctx context.Context, workspacePath string, strategy RecoveryStrategy) (success bool, output string, err error) {
	cmd := exec.CommandContext(ctx, strategy.Command, strategy.Args...)
	cmd.Dir = workspacePath

	rawOutput, err := cmd.CombinedOutput()
	output = string(rawOutput)

	// Strategy is considered successful if command executed without error
	success = (err == nil)

	return success, output, err
}

// releaseStaleAssignments releases beads that are assigned but open (a known starvation bug).
func (rca *RootCauseAnalyzer) releaseStaleAssignments(ctx context.Context, workspacePath string, beadListOutput string) (releasedCount int, output string) {
	// Parse the JSON output from bead list --status open --json
	var beads []map[string]interface{}
	if err := json.Unmarshal([]byte(beadListOutput), &beads); err != nil {
		log.Printf("[RootCauseAnalyzer] Failed to parse bead list: %v", err)
		return 0, fmt.Sprintf("Parse error: %v", err)
	}

	releasedCount = 0
	var releaseOutputs []string

	for _, bead := range beads {
		beadID, ok := bead["id"].(string)
		if !ok {
			continue
		}

		status, _ := bead["base_status"].(string)
		assignee, _ := bead["assignee"].(string)

		// Target: assigned but open beads (the starvation bug)
		if status == "open" && assignee != "" && assignee != "null" {
			log.Printf("[RootCauseAnalyzer] Releasing stale assignment for bead %s (assignee: %s)", beadID, assignee)

			// Release the bead using bead update --clear-assignee
			cmd := exec.CommandContext(ctx, "bead", "update", beadID, "--clear-assignee")
			cmd.Dir = workspacePath
			cmdOutput, err := cmd.CombinedOutput()

			if err != nil {
				releaseOutputs = append(releaseOutputs, fmt.Sprintf("Failed to release %s: %v", beadID, err))
				continue
			}

			releasedCount++
			releaseOutputs = append(releaseOutputs, fmt.Sprintf("Released %s (was assigned to %s)", beadID, assignee))
		}
	}

	output = strings.Join(releaseOutputs, "\n")
	return releasedCount, output
}

// countReadyBeads counts beads ready for workers to claim.
func (rca *RootCauseAnalyzer) countReadyBeads(ctx context.Context, workspacePath string) (int, error) {
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
