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

// AlertRevaluator implements the periodic re-evaluation of starvation alert beads.
type AlertRevaluator struct {
	workspaceRoot       string
	checkInterval       time.Duration
	dryRun              bool
	verbose             bool
	alertLabel          string
	logFile             *os.File
	minReevaluationAge  time.Duration // Minimum age before re-evaluation (24 hours)
	maxReevaluationAge  time.Duration // Maximum age for escalation (48 hours)
}

// AlertMetadata holds metadata about a starvation alert bead.
type AlertMetadata struct {
	ID        string
	Title     string
	Created   time.Time
	Workspace string
}
type AlertResolution struct {
	AlertID          string    `json:"alert_id"`
	Workspace        string    `json:"workspace"`
	Timestamp        time.Time `json:"timestamp"`
	AlertCreated     time.Time `json:"alert_created"`
	AlertAge         float64   `json:"alert_age_hours"`
	Resolved         bool      `json:"resolved"`
	ReadyCount       int       `json:"ready_count"`
	ClosedWithReason string    `json:"closed_with_reason,omitempty"`
	Escalated        bool      `json:"escalated,omitempty"`
	EscalationBeadID string    `json:"escalation_bead_id,omitempty"`
	Error            string    `json:"error,omitempty"`
}

func main() {
	var (
		workspaceRoot = flag.String("workspace-root", "/home/coding", "Root directory containing all workspaces")
		interval      = flag.Duration("interval", 7*time.Minute, "Check interval (default: 7 minutes)")
		dryRun       = flag.Bool("dry-run", false, "Show what would be done without making changes")
		verbose      = flag.Bool("verbose", false, "Enable verbose logging")
		alertLabel   = flag.String("alert-label", "alert:starvation:unknown", "Label identifying starvation alert beads")
		logPath      = flag.String("log-file", "", "Path to log file for audit trail (default: stdout only)")
		once         = flag.Bool("once", false, "Run once and exit")
		minAge       = flag.Duration("min-age", 24*time.Hour, "Minimum alert age before re-evaluation (default: 24 hours)")
		maxAge       = flag.Duration("max-age", 48*time.Hour, "Maximum alert age before escalation (default: 48 hours)")
	)
	flag.Parse()

	log.SetFlags(0)
	if *verbose {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	}

	// Open log file if specified
	var logFile *os.File
	if *logPath != "" {
		var err error
		logFile, err = os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatalf("Failed to open log file: %v", err)
		}
		defer logFile.Close()
		log.Printf("Logging audit trail to: %s", *logPath)
	}

	revaluator := &AlertRevaluator{
		workspaceRoot:       *workspaceRoot,
		checkInterval:       *interval,
		dryRun:              *dryRun,
		verbose:             *verbose,
		alertLabel:          *alertLabel,
		logFile:             logFile,
		minReevaluationAge:  *minAge,
		maxReevaluationAge:  *maxAge,
	}

	log.Printf("Starting starvation alert re-evaluation (interval: %v)", *interval)

	if *once {
		// One-shot mode
		resolutions := revaluator.runAllWorkspaces(context.Background())
		revaluator.reportResolutions(resolutions)
		return
	}

	// Loop mode
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			resolutions := revaluator.runAllWorkspaces(context.Background())
			revaluator.reportResolutions(resolutions)
		}
	}
}

// runAllWorkspaces checks all workspaces for starvation alerts to re-evaluate.
func (r *AlertRevaluator) runAllWorkspaces(ctx context.Context) []*AlertResolution {
	workspaces, err := r.findWorkspacesWithBeads()
	if err != nil {
		log.Printf("[Revaluator] Failed to find workspaces: %v", err)
		return nil
	}

	var resolutions []*AlertResolution
	for _, workspace := range workspaces {
		workspaceResolutions := r.revaluateAlerts(ctx, workspace)
		resolutions = append(resolutions, workspaceResolutions...)
	}

	return resolutions
}

// findWorkspacesWithBeads finds all directories with .beads/beads.db.
func (r *AlertRevaluator) findWorkspacesWithBeads() ([]string, error) {
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

// revaluateAlerts checks all open starvation alert beads in a workspace and closes them if work is available.
func (r *AlertRevaluator) revaluateAlerts(ctx context.Context, workspacePath string) []*AlertResolution {
	workspaceName := filepath.Base(workspacePath)
	log.Printf("[Revaluator] Checking workspace: %s", workspaceName)

	// Find all open starvation alert beads with their metadata
	alertBeads, err := r.findStarvationAlertBeadsWithMetadata(ctx, workspacePath)
	if err != nil {
		log.Printf("[Revaluator] Failed to find alert beads in %s: %v", workspaceName, err)
		return nil
	}

	if len(alertBeads) == 0 {
		if r.verbose {
			log.Printf("[Revaluator] No starvation alert beads in %s", workspaceName)
		}
		return nil
	}

	log.Printf("[Revaluator] Found %d starvation alert beads in %s", len(alertBeads), workspaceName)

	// Check if work is now available
	readyCount, err := r.countReadyBeads(ctx, workspacePath)
	if err != nil {
		log.Printf("[Revaluator] Failed to count ready beads in %s: %v", workspaceName, err)
		return nil
	}

	var resolutions []*AlertResolution
	now := time.Now()

	for _, alertBead := range alertBeads {
		beadID := alertBead.ID
		created := alertBead.Created
		age := now.Sub(created)

		// Skip alerts that are too young (less than min age)
		if age < r.minReevaluationAge {
			if r.verbose {
				log.Printf("[Revaluator] Alert %s too young for re-evaluation (age: %.1f hours, min: %.1f hours)",
					beadID, age.Hours(), r.minReevaluationAge.Hours())
			}
			resolutions = append(resolutions, &AlertResolution{
				AlertID:      beadID,
				Workspace:    workspaceName,
				Timestamp:    now,
				AlertCreated: created,
				AlertAge:     age.Hours(),
				Resolved:     false,
				ReadyCount:   readyCount,
			})
			continue
		}

		log.Printf("[Revaluator] Re-evaluating alert %s (age: %.1f hours)", beadID, age.Hours())

		// If work is available, close the alert
		if readyCount > 0 {
			log.Printf("[Revaluator] Work available (ready=%d), closing starvation alert %s in %s",
				readyCount, beadID, workspaceName)
			resolution := r.closeAlert(ctx, workspacePath, beadID, readyCount, created, age)
			resolutions = append(resolutions, resolution)
		} else {
			// No work available - check if we should escalate
			if age >= r.maxReevaluationAge {
				log.Printf("[Revaluator] Alert %s exceeds max age (%.1f hours), escalating to diagnostic bead",
					beadID, r.maxReevaluationAge.Hours())
				resolution := r.escalateAlert(ctx, workspacePath, alertBead, readyCount)
				resolutions = append(resolutions, resolution)
			} else {
				if r.verbose {
					log.Printf("[Revaluator] Alert %s persists but below escalation threshold (age: %.1f hours, max: %.1f hours)",
						beadID, age.Hours(), r.maxReevaluationAge.Hours())
				}
				resolutions = append(resolutions, &AlertResolution{
					AlertID:      beadID,
					Workspace:    workspaceName,
					Timestamp:    now,
					AlertCreated: created,
					AlertAge:     age.Hours(),
					Resolved:     false,
					ReadyCount:   readyCount,
				})
			}
		}
	}

	return resolutions
}

// findStarvationAlertBeadsWithMetadata finds all open beads with the starvation alert label and their metadata.
func (r *AlertRevaluator) findStarvationAlertBeadsWithMetadata(ctx context.Context, workspacePath string) ([]AlertMetadata, error) {
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

	var alertBeads []AlertMetadata
	for _, bead := range beads {
		beadID, ok := bead["id"].(string)
		if !ok {
			continue
		}

		// Check if bead has the starvation alert label
		hasAlertLabel := false
		if labels, ok := bead["labels"].([]interface{}); ok {
			for _, label := range labels {
				if labelStr, ok := label.(string); ok && labelStr == r.alertLabel {
					hasAlertLabel = true
					break
				}
			}
		}

		if !hasAlertLabel {
			// Also check title for "starvation" keyword (for backwards compatibility)
			if title, ok := bead["title"].(string); ok {
				if !strings.Contains(strings.ToLower(title), "starvation") {
					continue
				}
			} else {
				continue
			}
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

		alertBeads = append(alertBeads, AlertMetadata{
			ID:      beadID,
			Title:   title,
			Created: created,
		})
	}

	return alertBeads, nil
}

// countReadyBeads counts beads ready for workers to claim.
func (r *AlertRevaluator) countReadyBeads(ctx context.Context, workspacePath string) (int, error) {
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

// closeAlert closes a starvation alert bead with the standard reason.
func (r *AlertRevaluator) closeAlert(ctx context.Context, workspacePath, beadID string, readyCount int, created time.Time, age time.Duration) *AlertResolution {
	workspaceName := filepath.Base(workspacePath)
	reason := "Condition self-resolved - no action required"

	resolution := &AlertResolution{
		AlertID:          beadID,
		Workspace:        workspaceName,
		Timestamp:        time.Now(),
		AlertCreated:     created,
		AlertAge:         age.Hours(),
		Resolved:         true,
		ReadyCount:       readyCount,
		ClosedWithReason: reason,
	}

	if r.dryRun {
		log.Printf("[Revaluator] [DRY-RUN] Would close alert %s in %s", beadID, workspaceName)
		return resolution
	}

	// Close the bead
	cmd := exec.CommandContext(ctx, "bead", "close", beadID, "--reason", reason)
	cmd.Dir = workspacePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[Revaluator] Failed to close alert %s in %s: %v", beadID, workspaceName, err)
		resolution.Resolved = false
		resolution.Error = fmt.Sprintf("close failed: %v: %s", err, string(output))
		return resolution
	}

	log.Printf("[Revaluator] Closed starvation alert %s in %s (ready=%d)", beadID, workspaceName, readyCount)
	return resolution
}

// escalateAlert creates a diagnostic bead for automated recovery when a starvation alert persists beyond the threshold.
func (r *AlertRevaluator) escalateAlert(ctx context.Context, workspacePath string, alertBead AlertMetadata, readyCount int) *AlertResolution {
	workspaceName := filepath.Base(workspacePath)
	now := time.Now()

	resolution := &AlertResolution{
		AlertID:      alertBead.ID,
		Workspace:    workspaceName,
		Timestamp:    now,
		AlertCreated: alertBead.Created,
		AlertAge:     now.Sub(alertBead.Created).Hours(),
		Resolved:     false,
		ReadyCount:   readyCount,
		Escalated:    true,
	}

	if r.dryRun {
		log.Printf("[Revaluator] [DRY-RUN] Would create diagnostic bead for persistent alert %s in %s", alertBead.ID, workspaceName)
		return resolution
	}

	// Create diagnostic bead for automated recovery
	diagnosticTitle := fmt.Sprintf("Diagnostic: Starvation condition persists - %s", workspaceName)
	diagnosticDesc := fmt.Sprintf(
		"Starvation alert %s has persisted for %.1f hours without self-resolution.\n\n"+
			"**Alert Details:**\n"+
			"- Alert ID: %s\n"+
			"- Alert Title: %s\n"+
			"- Created: %s\n"+
			"- Age: %.1f hours\n"+
			"- Workspace: %s\n\n"+
			"**Current State:**\n"+
			"- Ready beads: %d\n"+
			"- Condition: Starvation persists (no work available)\n\n"+
			"**Action Required:**\n"+
			"Run automated recovery diagnostics and repair:\n"+
			"1. Execute `bead doctor --repair`\n"+
			"2. Validate cross-repo preconditions\n"+
			"3. Check worker health and responsiveness\n"+
			"4. Investigate database corruption or locking issues\n\n"+
			"This diagnostic bead was automatically created by the starvation-alert-revaluator.",
		alertBead.ID, now.Sub(alertBead.Created).Hours(),
		alertBead.ID, alertBead.Title, alertBead.Created.Format(time.RFC3339),
		now.Sub(alertBead.Created).Hours(), workspaceName,
		readyCount,
	)

	// Create the diagnostic bead
	cmd := exec.CommandContext(ctx, "bead", "create",
		"--title", diagnosticTitle,
		"--priority", "1", // P1 - high priority
		"--issue-type", "task",
		"--label", "automated:recovery",
		"--label", "diagnostic:starvation",
	)
	cmd.Dir = workspacePath

	// Set the description via stdin
	cmd.Stdin = strings.NewReader(diagnosticDesc)

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[Revaluator] Failed to create diagnostic bead for alert %s in %s: %v", alertBead.ID, workspaceName, err)
		resolution.Error = fmt.Sprintf("failed to create diagnostic bead: %v: %s", err, string(output))
		return resolution
	}

	// Extract the bead ID from the output
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) > 0 {
		// The last line should contain the bead ID
		resolution.EscalationBeadID = strings.TrimSpace(lines[len(lines)-1])
	}

	log.Printf("[Revaluator] Created diagnostic bead %s for persistent starvation alert %s in %s",
		resolution.EscalationBeadID, alertBead.ID, workspaceName)

	return resolution
}

// reportResolutions logs the resolution results.
func (r *AlertRevaluator) reportResolutions(resolutions []*AlertResolution) {
	if len(resolutions) == 0 {
		return
	}

	resolvedCount := 0
	escalatedCount := 0
	for _, resolution := range resolutions {
		if resolution.Resolved {
			resolvedCount++
		}
		if resolution.Escalated {
			escalatedCount++
		}
	}

	log.Printf("[Revaluator] Evaluated %d alerts, resolved %d, escalated %d",
		len(resolutions), resolvedCount, escalatedCount)

	// Write to audit log if configured
	if r.logFile != nil {
		for _, resolution := range resolutions {
			jsonBytes, err := json.Marshal(resolution)
			if err != nil {
				log.Printf("[Revaluator] Failed to marshal resolution: %v", err)
				continue
			}
			if _, err := r.logFile.Write(append(jsonBytes, '\n')); err != nil {
				log.Printf("[Revaluator] Failed to write resolution: %v", err)
			}
		}
	}

	// Verbose output
	if r.verbose {
		for _, resolution := range resolutions {
			if resolution.Resolved {
				log.Printf("[Revaluator] ✓ Resolved %s in %s (age: %.1f hours, ready: %d)",
					resolution.AlertID, resolution.Workspace, resolution.AlertAge, resolution.ReadyCount)
			} else if resolution.Escalated {
				log.Printf("[Revaluator] → Escalated %s in %s to diagnostic bead %s (age: %.1f hours)",
					resolution.AlertID, resolution.Workspace, resolution.EscalationBeadID, resolution.AlertAge)
			} else if resolution.Error != "" {
				log.Printf("[Revaluator] ✗ Error %s in %s: %s", resolution.AlertID, resolution.Workspace, resolution.Error)
			} else if r.verbose {
				log.Printf("[Revaluator] • Waiting %s in %s (age: %.1f hours, below threshold)",
					resolution.AlertID, resolution.Workspace, resolution.AlertAge)
			}
		}
	}
}
