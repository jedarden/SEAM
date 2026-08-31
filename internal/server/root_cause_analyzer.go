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

// RootCauseAnalyzer performs automated root cause analysis for starvation alerts.
// It analyzes which recovery strategy succeeded, database integrity, and checkpoint
// freshness to categorize incidents and determine auto-recoverability.
type RootCauseAnalyzer struct {
	checkpointFreshnessThreshold time.Duration // How stale before checkpoint is considered stale (default: 5 minutes)
}

// NewRootCauseAnalyzer creates a new root cause analyzer.
func NewRootCauseAnalyzer() *RootCauseAnalyzer {
	return &RootCauseAnalyzer{
		checkpointFreshnessThreshold: 5 * time.Minute,
	}
}

// AnalysisResult contains the root cause analysis results.
type AnalysisResult struct {
	RootCause       string   // Categorized root cause (e.g., "database-corruption", "query-bug")
	AutoRecovered    bool     // Whether the issue was automatically recoverable
	Strategy         string   // Which PluckFallback strategy succeeded
	DBHealthy        bool     // Database integrity check result
	CheckpointFresh  bool     // Checkpoint freshness check result
	Details          []string // Detailed analysis notes
	RecommendedLabel string   // Recommended label for tagging
}

// Analyze performs root cause analysis based on the recovery strategy and workspace state.
func (rca *RootCauseAnalyzer) Analyze(ctx context.Context, workspacePath string, strategy string, readyCount int, history interface{}) (rootCause string, autoRecovered bool, details string) {
	workspaceName := filepath.Base(workspacePath)
	result := AnalysisResult{
		Strategy:      strategy,
		ReadyCount:    readyCount,
		Details:       []string{},
		DBHealthy:      true,  // Assume healthy until proven otherwise
		CheckpointFresh: true, // Assume fresh until proven otherwise
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

	// Step 3: Determine root cause based on strategy and diagnostics
	rootCause, autoRecovered = rca.categorizeRootCause(strategy, dbHealthy, checkpointFresh, readyCount)
	result.RootCause = rootCause
	result.AutoRecovered = autoRecovered
	result.RecommendedLabel = fmt.Sprintf("starvation:%s", rootCause)

	// Build detailed output string
	detailsStr := fmt.Sprintf("**Root Cause:** %s\n", rootCause)
	detailsStr += fmt.Sprintf("**Auto-Recovered:** %v\n\n", autoRecovered)
	detailsStr += "**Analysis:**\n"
	for i, detail := range result.Details {
		detailsStr += fmt.Sprintf("%d. %s\n", i+1, detail)
	}

	detailsStr += fmt.Sprintf("\n**Diagnostic Results:**\n")
	detailsStr += fmt.Sprintf("- Database Integrity: %v\n", result.DBHealthy)
	detailsStr += fmt.Sprintf("- Checkpoint Freshness: %v\n", result.CheckpointFresh)
	detailsStr += fmt.Sprintf("- Recovery Strategy: %s\n", result.Strategy)
	detailsStr += fmt.Sprintf("- Recommended Label: %s\n", result.RecommendedLabel)

	log.Printf("[RootCauseAnalyzer] Analysis complete for %s: root_cause=%s, auto_recovered=%v",
		workspaceName, rootCause, autoRecovered)

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

// categorizeRootCause determines the root cause category based on strategy and diagnostics.
func (rca *RootCauseAnalyzer) categorizeRootCause(strategy string, dbHealthy bool, checkpointFresh bool, readyCount int) (rootCause string, autoRecovered bool) {
	// Map strategies to root causes
	switch strategy {
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
		// Checkpoint query succeeded - database corruption or desync
		if !dbHealthy {
			return "database-corruption", true
		}
		if !checkpointFresh {
			return "checkpoint-desync", true
		}
		return "database-corruption", true

	default:
		// Unknown strategy
		return "unknown", false
	}
}
