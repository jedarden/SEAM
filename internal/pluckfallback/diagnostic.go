package pluckfallback

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CreateDiagnosticBead creates a bead documenting the visibility issue.
func CreateDiagnosticBead(ctx context.Context, workspace string, strategy string, discrepancies []string, candidates []PluckResult) error {
	cmd := exec.CommandContext(ctx, "bead", "create",
		"--title", fmt.Sprintf("[Visibility Bug] Primary query failed, %s recovered %d beads", strategy, len(candidates)),
		"--priority", "1",
		"--issue-type", "task",
		"--label", "visibility-bug,auto-detected,pluck-fallback",
	)
	cmd.Dir = workspace

	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to create diagnostic bead: %w", err)
	}

	beadID := strings.TrimSpace(string(output))
	if beadID == "" {
		return fmt.Errorf("no bead ID returned from create command")
	}

	log.Printf("Created diagnostic bead: %s", beadID)

	// Add notes to the bead
	notes := fmt.Sprintf("Visibility bug detected at %s\n\n", time.Now().Format(time.RFC3339))
	notes += fmt.Sprintf("**Strategy Used:** %s\n\n", strategy)
	notes += "**Discrepancies:**\n"
	for _, d := range discrepancies {
		notes += fmt.Sprintf("- %s\n", d)
	}
	notes += "\n**Recovered Beads:**\n"
	for _, c := range candidates {
		notes += fmt.Sprintf("- %s [%s] - %s (priority %d)\n", c.ID, c.Status, c.Title, c.Priority)
	}

	updateCmd := exec.CommandContext(ctx, "bead", "update", beadID, "--notes", notes)
	updateCmd.Dir = workspace
	if err := updateCmd.Run(); err != nil {
		log.Printf("Failed to update diagnostic bead %s: %v", beadID, err)
	}

	return nil
}

// LogDiscrepancy writes a discrepancy to the diagnostic log file.
func LogDiscrepancy(logFile string, discrepancy string, recoveredBeads []PluckResult) error {
	if logFile == "" {
		return nil
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open diagnostic log: %w", err)
	}
	defer f.Close()

	fmt.Fprintf(f, "%s\n", discrepancy)
	for _, r := range recoveredBeads {
		fmt.Fprintf(f, "  - Recovered bead: %s (%s)\n", r.ID, r.Title)
	}

	return nil
}
