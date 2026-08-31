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
	workspaceRoot  string
	checkInterval  time.Duration
	dryRun         bool
	verbose        bool
	alertLabel     string
	logFile        *os.File
}

// AlertResolution holds the outcome of an alert re-evaluation.
type AlertResolution struct {
	AlertID        string    `json:"alert_id"`
	Workspace      string    `json:"workspace"`
	Timestamp      time.Time `json:"timestamp"`
	Resolved       bool      `json:"resolved"`
	ReadyCount     int       `json:"ready_count"`
	ClosedWithReason string  `json:"closed_with_reason,omitempty"`
	Error          string    `json:"error,omitempty"`
}

func main() {
	var (
		workspaceRoot = flag.String("workspace-root", "/home/coding", "Root directory containing all workspaces")
		interval      = flag.Duration("interval", 7*time.Minute, "Check interval (default: 7 minutes)")
		dryRun       = flag.Bool("dry-run", false, "Show what would be done without making changes")
		verbose      = flag.Bool("verbose", false, "Enable verbose logging")
		alertLabel   = flag.String("alert-label", "starvation-alert", "Label identifying starvation alert beads")
		logPath      = flag.String("log-file", "", "Path to log file for audit trail (default: stdout only)")
		once         = flag.Bool("once", false, "Run once and exit")
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
		workspaceRoot: *workspaceRoot,
		checkInterval: *interval,
		dryRun:        *dryRun,
		verbose:       *verbose,
		alertLabel:    *alertLabel,
		logFile:       logFile,
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

	// Find all open starvation alert beads
	alertBeads, err := r.findStarvationAlertBeads(ctx, workspacePath)
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

	// If work is available, close all starvation alert beads
	if readyCount > 0 {
		log.Printf("[Revaluator] Work available (ready=%d), closing %d starvation alerts in %s",
			readyCount, len(alertBeads), workspaceName)

		for _, beadID := range alertBeads {
			resolution := r.closeAlert(ctx, workspacePath, beadID, readyCount)
			resolutions = append(resolutions, resolution)
		}
	} else {
		if r.verbose {
			log.Printf("[Revaluator] No work available yet (ready=0), keeping %d alerts open in %s",
				len(alertBeads), workspaceName)
		}

		// Record that we checked but didn't resolve
		for _, beadID := range alertBeads {
			resolutions = append(resolutions, &AlertResolution{
				AlertID:    beadID,
				Workspace:  workspaceName,
				Timestamp:  time.Now(),
				Resolved:   false,
				ReadyCount: readyCount,
			})
		}
	}

	return resolutions
}

// findStarvationAlertBeads finds all open beads with the starvation alert label.
func (r *AlertRevaluator) findStarvationAlertBeads(ctx context.Context, workspacePath string) ([]string, error) {
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

		// Check if bead has the starvation alert label
		if labels, ok := bead["labels"].([]interface{}); ok {
			for _, label := range labels {
				if labelStr, ok := label.(string); ok && labelStr == r.alertLabel {
					alertBeads = append(alertBeads, beadID)
					break
				}
			}
		}

		// Also check title for "starvation" keyword (for backwards compatibility)
		if title, ok := bead["title"].(string); ok {
			if strings.Contains(strings.ToLower(title), "starvation") {
				// Avoid duplicates
				found := false
				for _, id := range alertBeads {
					if id == beadID {
						found = true
						break
					}
				}
				if !found {
					alertBeads = append(alertBeads, beadID)
				}
			}
		}
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
func (r *AlertRevaluator) closeAlert(ctx context.Context, workspacePath, beadID string, readyCount int) *AlertResolution {
	workspaceName := filepath.Base(workspacePath)
	reason := "Starvation condition resolved - work now available"

	resolution := &AlertResolution{
		AlertID:          beadID,
		Workspace:        workspaceName,
		Timestamp:        time.Now(),
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

// reportResolutions logs the resolution results.
func (r *AlertRevaluator) reportResolutions(resolutions []*AlertResolution) {
	if len(resolutions) == 0 {
		return
	}

	resolvedCount := 0
	for _, r := range resolutions {
		if r.Resolved {
			resolvedCount++
		}
	}

	log.Printf("[Revaluator] Evaluated %d alerts, resolved %d", len(resolutions), resolvedCount)

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
				log.Printf("[Revaluator] ✓ Resolved %s in %s", resolution.AlertID, resolution.Workspace)
			} else if resolution.Error != "" {
				log.Printf("[Revaluator] ✗ Error %s in %s: %s", resolution.AlertID, resolution.Workspace, resolution.Error)
			}
		}
	}
}
