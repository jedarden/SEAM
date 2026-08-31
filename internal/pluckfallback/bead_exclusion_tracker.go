package pluckfallback

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
)

// ExclusionReason represents why a bead was excluded from the ready frontier.
type ExclusionReason string

const (
	ExclusionActiveAssignee      ExclusionReason = "has_active_assignee"
	ExclusionStaleAssignee       ExclusionReason = "has_stale_assignee"
	ExclusionBlockedByDeps       ExclusionReason = "blocked_by_unclosed_dependencies"
	ExclusionStatusNotOpen       ExclusionReason = "status_not_open"
	ExclusionLabelsExclude       ExclusionReason = "labels_exclude_from_worker_type"
	ExclusionDependencyLoop      ExclusionReason = "dependency_loop_detected"
	ExclusionStaleRevision       ExclusionReason = "stale_revision_conflict"
	ExclusionDatabaseCorruption ExclusionReason = "database_corruption"
	ExclusionOther              ExclusionReason = "other_reason"
)

// BeadExclusion represents a single bead's exclusion from the ready set.
type BeadExclusion struct {
	BeadID        string            `json:"bead_id"`
	Title         string            `json:"title,omitempty"`
	Status        string            `json:"status"`
	Assignee      string            `json:"assignee,omitempty"`
	Priority      int               `json:"priority,omitempty"`
	Reason        ExclusionReason   `json:"exclusion_reason"`
	Details       string            `json:"details,omitempty"`
	Dependencies  []string          `json:"dependencies,omitempty"`
	Labels        []string          `json:"labels,omitempty"`
	Timestamp     time.Time         `json:"timestamp"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// ExclusionReport holds a complete exclusion analysis for a workspace.
type ExclusionReport struct {
	WorkspacePath    string         `json:"workspace_path"`
	Timestamp        time.Time      `json:"timestamp"`
	ReadyCount       int            `json:"ready_count"`
	OpenCount        int            `json:"open_count"`
	ExcludedBeads    []BeadExclusion `json:"excluded_beads"`
	Summary          ExclusionSummary `json:"summary"`
	QuerySuccessful  bool           `json:"query_successful"`
	QueryError       string         `json:"query_error,omitempty"`
}

// ExclusionSummary aggregates exclusion reasons.
type ExclusionSummary struct {
	TotalExcluded       int                       `json:"total_excluded"`
	ByReason            map[ExclusionReason]int   `json:"by_reason"`
	MostCommonReason    ExclusionReason           `json:"most_common_reason"`
	MostCommonCount     int                       `json:"most_common_count"`
}

// RollingWindow maintains a time-bounded window of exclusion statistics.
type RollingWindow struct {
	mu               sync.RWMutex
	window           time.Duration // How far back to keep data
	dataPoints       []WindowDataPoint
	maxDataPoints    int // Maximum data points to keep (prevents unbounded growth)
}

// WindowDataPoint represents a single exclusion observation in the rolling window.
type WindowDataPoint struct {
	Timestamp     time.Time       `json:"timestamp"`
	Workspace     string          `json:"workspace"`
	Reason        ExclusionReason `json:"reason"`
	BeadID        string          `json:"bead_id"`
	Count         int             `json:"count"` // Number of beads with this reason
}

// SystemicIssueAlert represents a detected systemic issue.
type SystemicIssueAlert struct {
	ID            string        `json:"id"`
	Timestamp     time.Time     `json:"timestamp"`
	Workspace     string        `json:"workspace"`
	AlertType     string        `json:"alert_type"`
	Reason        ExclusionReason `json:"reason"`
	Severity      string        `json:"severity"`
	Description   string        `json:"description"`
	Metrics       SystemicIssueMetrics `json:"metrics"`
	Resolved      bool          `json:"resolved"`
	ResolvedAt    *time.Time    `json:"resolved_at,omitempty"`
}

// SystemicIssueMetrics captures the metrics that triggered the alert.
type SystemicIssueMetrics struct {
	TotalOpen         int            `json:"total_open"`
	TotalExcluded     int            `json:"total_excluded"`
	ExclusionRate     float64        `json:"exclusion_rate"`
	ReasonCount       int            `json:"reason_count"`
	ReasonPercentage  float64        `json:"reason_percentage"`
	Threshold         float64        `json:"threshold"`
}

// ExclusionTracker tracks why beads are excluded from the ready frontier.
type ExclusionTracker struct {
	mu                   sync.RWMutex
	reportsByWorkspace   map[string]*ExclusionReport
	logFile              *os.File
	logPath              string
	verbose              bool
	workspaceRoot        string
	staleWorkerThreshold time.Duration
	rollingWindow        *RollingWindow
	alerts               []SystemicIssueAlert
	alertThreshold       float64 // Percentage threshold for systemic issues (e.g., 0.8 = 80%)
}

// NewExclusionTracker creates a new exclusion tracker.
func NewExclusionTracker(logPath string, verbose bool, workspaceRoot string, staleThreshold time.Duration) (*ExclusionTracker, error) {
	et := &ExclusionTracker{
		reportsByWorkspace:   make(map[string]*ExclusionReport),
		logPath:              logPath,
		verbose:              verbose,
		workspaceRoot:        workspaceRoot,
		staleWorkerThreshold: staleThreshold,
		rollingWindow: &RollingWindow{
			window:        24 * time.Hour, // Keep 24 hours of data
			maxDataPoints: 10000,           // Prevent unbounded growth
			dataPoints:    make([]WindowDataPoint, 0),
		},
		alerts:         make([]SystemicIssueAlert, 0),
		alertThreshold: 0.8, // 80% threshold for systemic issues
	}

	if logPath != "" {
		// Ensure directory exists
		if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
			return nil, fmt.Errorf("create log directory: %w", err)
		}

		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("open exclusion log: %w", err)
		}
		et.logFile = f
	}

	return et, nil
}

// TrackExclusions analyzes why beads are excluded from the ready set.
// Returns an exclusion report and optionally logs it.
func (et *ExclusionTracker) TrackExclusions(ctx context.Context, workspacePath string) (*ExclusionReport, error) {
	workspaceName := filepath.Base(workspacePath)

	if et.verbose {
		log.Printf("[ExclusionTracker] Analyzing exclusions for workspace: %s", workspaceName)
	}

	report := &ExclusionReport{
		WorkspacePath: workspacePath,
		Timestamp:     time.Now(),
		ExcludedBeads: make([]BeadExclusion, 0),
		Summary: ExclusionSummary{
			ByReason: make(map[ExclusionReason]int),
		},
	}

	// Step 1: Get ready bead count
	readyCount, err := et.countReadyBeads(ctx, workspacePath)
	if err != nil {
		report.QuerySuccessful = false
		report.QueryError = fmt.Sprintf("Failed to count ready beads: %v", err)
		log.Printf("[ExclusionTracker] %s: %v", workspaceName, err)
		return report, err
	}
	report.ReadyCount = readyCount

	// Step 2: Get all open beads
	openBeads, err := et.listOpenBeads(ctx, workspacePath)
	if err != nil {
		report.QuerySuccessful = false
		report.QueryError = fmt.Sprintf("Failed to list open beads: %v", err)
		log.Printf("[ExclusionTracker] %s: %v", workspaceName, err)
		return report, err
	}
	report.OpenCount = len(openBeads)
	report.QuerySuccessful = true

	// If ready count > 0, no need to analyze exclusions (no starvation)
	if readyCount > 0 {
		if et.verbose {
			log.Printf("[ExclusionTracker] %s: %d ready beads available, no exclusion analysis needed", workspaceName, readyCount)
		}
		return report, nil
	}

	// Step 3: Analyze why each open bead is excluded
	if et.verbose {
		log.Printf("[ExclusionTracker] %s: 0 ready beads but %d open beads - analyzing exclusions", workspaceName, len(openBeads))
	}

	for _, beadData := range openBeads {
		beadID, _ := beadData["id"].(string)
		if beadID == "" {
			continue
		}

		exclusion, err := et.analyzeBeadExclusion(ctx, workspacePath, beadID, beadData)
		if err != nil {
			log.Printf("[ExclusionTracker] Failed to analyze bead %s: %v", beadID, err)
			continue
		}

		report.ExcludedBeads = append(report.ExcludedBeads, *exclusion)
		report.Summary.ByReason[exclusion.Reason]++
	}

	// Calculate summary
	report.Summary.TotalExcluded = len(report.ExcludedBeads)
	for reason, count := range report.Summary.ByReason {
		if count > report.Summary.MostCommonCount {
			report.Summary.MostCommonReason = reason
			report.Summary.MostCommonCount = count
		}
	}

	// Store report
	et.mu.Lock()
	et.reportsByWorkspace[workspacePath] = report
	et.mu.Unlock()

	// Add to rolling window
	for _, exclusion := range report.ExcludedBeads {
		et.AddToRollingWindow(workspacePath, exclusion.Reason, exclusion.BeadID, 1)
	}

	// Detect systemic issues
	alert := et.DetectSystemicIssue(report)
	if alert != nil {
		et.RecordAlert(alert)
		if et.verbose {
			log.Printf("[ExclusionTracker] SYSTEMIC ISSUE DETECTED: %s", alert.Description)
		}
	}

	// Log the report
	if et.logFile != nil {
		et.logReport(report)
	}

	// If exclusions found, log a summary
	if report.Summary.TotalExcluded > 0 && et.verbose {
		et.logSummary(report)
	}

	return report, nil
}

// analyzeBeadExclusion determines why a specific bead is excluded from the ready set.
func (et *ExclusionTracker) analyzeBeadExclusion(ctx context.Context, workspacePath, beadID string, beadData map[string]interface{}) (*BeadExclusion, error) {
	exclusion := &BeadExclusion{
		BeadID:    beadID,
		Timestamp: time.Now(),
		Metadata:  make(map[string]string),
	}

	// Extract basic fields
	if title, ok := beadData["title"].(string); ok {
		exclusion.Title = title
	}
	if status, ok := beadData["status"].(string); ok {
		exclusion.Status = status
	}
	if assignee, ok := beadData["assignee"].(string); ok {
		exclusion.Assignee = assignee
	}
	if priority, ok := beadData["priority"].(float64); ok {
		exclusion.Priority = int(priority)
	}
	if labels, ok := beadData["labels"].([]interface{}); ok {
		for _, label := range labels {
			if labelStr, ok := label.(string); ok {
				exclusion.Labels = append(exclusion.Labels, labelStr)
			}
		}
	}

	// Check 1: Has assignee - determine if active or stale
	if exclusion.Assignee != "" && exclusion.Assignee != "null" {
		// Check if the assignee worker is stale
		isStale, inactiveDuration, err := et.isWorkerStale(exclusion.Assignee)
		if err != nil {
			// Cannot verify worker status - assume active (conservative)
			exclusion.Reason = ExclusionActiveAssignee
			exclusion.Details = fmt.Sprintf("Bead is assigned to %s (worker activity unknown: %v)", exclusion.Assignee, err)
			exclusion.Metadata["worker_active_status"] = "unknown"
			return exclusion, nil
		}

		if isStale {
			// Worker is stale - this is a known repairable condition
			exclusion.Reason = ExclusionStaleAssignee
			exclusion.Details = fmt.Sprintf("Bead is assigned to %s (worker inactive for %s)", exclusion.Assignee, inactiveDuration)
			exclusion.Metadata["worker_active_status"] = "stale"
			exclusion.Metadata["inactive_duration"] = inactiveDuration.String()
		} else {
			// Worker is still active
			exclusion.Reason = ExclusionActiveAssignee
			exclusion.Details = fmt.Sprintf("Bead is assigned to %s (worker active, last heartbeat %s ago)", exclusion.Assignee, inactiveDuration)
			exclusion.Metadata["worker_active_status"] = "active"
			exclusion.Metadata["inactive_duration"] = inactiveDuration.String()
		}
		return exclusion, nil
	}

	// Check 2: Get dependencies and check if any are unclosed
	deps, err := et.getBeadDependencies(ctx, workspacePath, beadID)
	if err == nil && len(deps) > 0 {
		exclusion.Dependencies = deps
		unclosedDeps := et.checkUnclosedDependencies(ctx, workspacePath, deps)
		if len(unclosedDeps) > 0 {
			exclusion.Reason = ExclusionBlockedByDeps
			exclusion.Details = fmt.Sprintf("Blocked by %d unclosed dependencies: %s", len(unclosedDeps), strings.Join(unclosedDeps, ", "))
			exclusion.Metadata["unclosed_deps"] = strings.Join(unclosedDeps, ",")
			return exclusion, nil
		}
	}

	// Check 3: Status not open (shouldn't happen since we query open beads, but check anyway)
	if exclusion.Status != "open" {
		exclusion.Reason = ExclusionStatusNotOpen
		exclusion.Details = fmt.Sprintf("Status is '%s' instead of 'open'", exclusion.Status)
		return exclusion, nil
	}

	// Check 4: Check for dependency loops
	if len(deps) > 0 {
		hasLoop := et.checkDependencyLoop(ctx, workspacePath, beadID, deps)
		if hasLoop {
			exclusion.Reason = ExclusionDependencyLoop
			exclusion.Details = "Dependency loop detected"
			return exclusion, nil
		}
	}

	// Check 5: Label-based exclusion (check for worker-type labels)
	if et.hasExclusionaryLabels(exclusion.Labels) {
		exclusion.Reason = ExclusionLabelsExclude
		exclusion.Details = fmt.Sprintf("Excluded by labels: %s", strings.Join(exclusion.Labels, ", "))
		return exclusion, nil
	}

	// Default: Other reason
	exclusion.Reason = ExclusionOther
	exclusion.Details = "No specific exclusion reason identified"
	return exclusion, nil
}

// countReadyBeads counts beads ready for workers to claim.
func (et *ExclusionTracker) countReadyBeads(ctx context.Context, workspacePath string) (int, error) {
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

// listOpenBeads lists all open beads in a workspace.
func (et *ExclusionTracker) listOpenBeads(ctx context.Context, workspacePath string) ([]map[string]interface{}, error) {
	cmd := exec.CommandContext(ctx, "bead", "list", "--status", "open", "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bead list --status open: %w", err)
	}

	var beads []map[string]interface{}
	if err := json.Unmarshal(output, &beads); err != nil {
		return nil, fmt.Errorf("parse open bead list: %w", err)
	}

	return beads, nil
}

// getBeadDependencies gets a bead's dependencies.
func (et *ExclusionTracker) getBeadDependencies(ctx context.Context, workspacePath, beadID string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "bead", "dep", "ls", beadID)
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bead dep ls: %w", err)
	}

	var deps []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		dep := strings.TrimSpace(scanner.Text())
		if dep != "" {
			deps = append(deps, dep)
		}
	}

	return deps, nil
}

// checkUnclosedDependencies checks which dependencies are not closed.
func (et *ExclusionTracker) checkUnclosedDependencies(ctx context.Context, workspacePath string, deps []string) []string {
	var unclosed []string

	for _, dep := range deps {
		// Check if dependency bead exists and is not closed
		cmd := exec.CommandContext(ctx, "bead", "show", dep, "--json")
		cmd.Dir = workspacePath

		output, err := cmd.Output()
		if err != nil {
			// Bead doesn't exist or error - consider it unclosed
			unclosed = append(unclosed, dep)
			continue
		}

		var beadData map[string]interface{}
		if err := json.Unmarshal(output, &beadData); err != nil {
			continue
		}

		if status, ok := beadData["status"].(string); ok {
			if status != "closed" {
				unclosed = append(unclosed, dep)
			}
		}
	}

	return unclosed
}

// checkDependencyLoop checks if a bead has circular dependencies.
func (et *ExclusionTracker) checkDependencyLoop(ctx context.Context, workspacePath, beadID string, deps []string) bool {
	// Simplified check: if any dependency lists us back, it's circular
	// In production, you'd want a full graph traversal
	for _, dep := range deps {
		cmd := exec.CommandContext(ctx, "bead", "dep", "ls", dep)
		cmd.Dir = workspacePath

		output, err := cmd.Output()
		if err != nil {
			continue
		}

		if strings.Contains(string(output), beadID) {
			return true
		}
	}
	return false
}

// hasExclusionaryLabels checks if labels exclude the bead from worker type.
func (et *ExclusionTracker) hasExclusionaryLabels(labels []string) bool {
	// Check for labels that might exclude from certain worker types
	exclusionaryLabels := []string{
		"no-auto-claim",
		"manual-only",
		"blocked",
		"deprecated",
	}

	for _, label := range labels {
		for _, exclusionary := range exclusionaryLabels {
			if strings.EqualFold(label, exclusionary) {
				return true
			}
		}
	}

	return false
}

// logReport writes an exclusion report to the log file.
func (et *ExclusionTracker) logReport(report *ExclusionReport) {
	if et.logFile == nil {
		return
	}

	data, err := json.Marshal(report)
	if err != nil {
		log.Printf("[ExclusionTracker] Failed to marshal report: %v", err)
		return
	}

	fmt.Fprintf(et.logFile, "%s\n", string(data))
	et.logFile.Sync()
}

// logSummary logs a human-readable summary of the exclusion report.
func (et *ExclusionTracker) logSummary(report *ExclusionReport) {
	workspaceName := filepath.Base(report.WorkspacePath)

	log.Printf("[ExclusionTracker] ===== EXCLUSION SUMMARY: %s =====", workspaceName)
	log.Printf("[ExclusionTracker] Open beads: %d | Ready beads: %d | Excluded: %d",
		report.OpenCount, report.ReadyCount, report.Summary.TotalExcluded)

	if report.Summary.TotalExcluded > 0 {
		log.Printf("[ExclusionTracker] Most common reason: %s (%d beads)",
			report.Summary.MostCommonReason, report.Summary.MostCommonCount)

		log.Printf("[ExclusionTracker] Breakdown by reason:")
		for reason, count := range report.Summary.ByReason {
			log.Printf("[ExclusionTracker]   - %s: %d beads", reason, count)
		}

		log.Printf("[ExclusionTracker] Top 10 excluded beads:")
		for i, exclusion := range report.ExcludedBeads {
			if i >= 10 {
				break
			}
			log.Printf("[ExclusionTracker]   %d. %s [%s] - %s: %s",
				i+1, exclusion.BeadID, exclusion.Status, exclusion.Reason, exclusion.Details)
		}
	}

	log.Printf("[ExclusionTracker] ================================================")
}

// GetLatestReport returns the most recent exclusion report for a workspace.
func (et *ExclusionTracker) GetLatestReport(workspacePath string) (*ExclusionReport, bool) {
	et.mu.RLock()
	defer et.mu.RUnlock()

	report, exists := et.reportsByWorkspace[workspacePath]
	return report, exists
}

// GetAllReports returns all tracked exclusion reports.
func (et *ExclusionTracker) GetAllReports() map[string]*ExclusionReport {
	et.mu.RLock()
	defer et.mu.RUnlock()

	// Return a copy to avoid race conditions
	reports := make(map[string]*ExclusionReport, len(et.reportsByWorkspace))
	for k, v := range et.reportsByWorkspace {
		reports[k] = v
	}
	return reports
}

// Close closes the log file.
func (et *ExclusionTracker) Close() error {
	if et.logFile != nil {
		return et.logFile.Close()
	}
	return nil
}

// AddToRollingWindow adds an exclusion observation to the rolling window.
func (et *ExclusionTracker) AddToRollingWindow(workspace string, reason ExclusionReason, beadID string, count int) {
	et.rollingWindow.mu.Lock()
	defer et.rollingWindow.mu.Unlock()

	now := time.Now()

	// Add new data point
	point := WindowDataPoint{
		Timestamp: now,
		Workspace: workspace,
		Reason:    reason,
		BeadID:    beadID,
		Count:     count,
	}
	et.rollingWindow.dataPoints = append(et.rollingWindow.dataPoints, point)

	// Trim old data points outside the window
	cutoff := now.Add(-et.rollingWindow.window)
	trimmed := et.rollingWindow.dataPoints[:0]
	for _, dp := range et.rollingWindow.dataPoints {
		if dp.Timestamp.After(cutoff) {
			trimmed = append(trimmed, dp)
		}
	}
	et.rollingWindow.dataPoints = trimmed

	// Enforce maximum data points limit
	if len(et.rollingWindow.dataPoints) > et.rollingWindow.maxDataPoints {
		// Keep the most recent data points
		et.rollingWindow.dataPoints = et.rollingWindow.dataPoints[len(et.rollingWindow.dataPoints)-et.rollingWindow.maxDataPoints:]
	}
}

// GetRollingWindowStats returns aggregated statistics from the rolling window.
func (et *ExclusionTracker) GetRollingWindowStats() map[string]interface{} {
	et.rollingWindow.mu.RLock()
	defer et.rollingWindow.mu.RUnlock()

	stats := map[string]interface{}{
		"window_duration":    et.rollingWindow.window.String(),
		"data_points_count":  len(et.rollingWindow.dataPoints),
		"oldest_timestamp":   nil,
		"newest_timestamp":   nil,
		"by_workspace":       make(map[string]int),
		"by_reason":          make(map[string]int),
	}

	if len(et.rollingWindow.dataPoints) == 0 {
		return stats
	}

	// Find oldest and newest timestamps
	oldest := et.rollingWindow.dataPoints[0].Timestamp
	newest := et.rollingWindow.dataPoints[0].Timestamp
	for _, dp := range et.rollingWindow.dataPoints {
		if dp.Timestamp.Before(oldest) {
			oldest = dp.Timestamp
		}
		if dp.Timestamp.After(newest) {
			newest = dp.Timestamp
		}
	}
	stats["oldest_timestamp"] = oldest
	stats["newest_timestamp"] = newest

	// Aggregate by workspace and reason
	byWorkspace := make(map[string]int)
	byReason := make(map[string]int)
	for _, dp := range et.rollingWindow.dataPoints {
		byWorkspace[dp.Workspace] += dp.Count
		byReason[string(dp.Reason)] += dp.Count
	}
	stats["by_workspace"] = byWorkspace
	stats["by_reason"] = byReason

	return stats
}

// DetectSystemicIssue checks if the exclusion report indicates a systemic issue.
func (et *ExclusionTracker) DetectSystemicIssue(report *ExclusionReport) *SystemicIssueAlert {
	if report == nil || report.OpenCount == 0 {
		return nil
	}

	exclusionRate := float64(report.Summary.TotalExcluded) / float64(report.OpenCount)

	// Check if overall exclusion rate exceeds threshold
	if exclusionRate < et.alertThreshold {
		return nil
	}

	// Check if the most common reason dominates
	mostCommonRate := float64(report.Summary.MostCommonCount) / float64(report.Summary.TotalExcluded)
	if mostCommonRate < et.alertThreshold {
		return nil
	}

	// Generate alert
	alert := &SystemicIssueAlert{
		ID:          fmt.Sprintf("sysalert-%s-%d", filepath.Base(report.WorkspacePath), time.Now().Unix()),
		Timestamp:   time.Now(),
		Workspace:   report.WorkspacePath,
		AlertType:   "high_exclusion_rate",
		Reason:      report.Summary.MostCommonReason,
		Severity:    et.calculateSeverity(exclusionRate, mostCommonRate),
		Description: fmt.Sprintf("%s: %.1f%% of open beads excluded, %.1f%% of exclusions are due to %s",
			filepath.Base(report.WorkspacePath),
			exclusionRate*100,
			mostCommonRate*100,
			report.Summary.MostCommonReason),
		Metrics: SystemicIssueMetrics{
			TotalOpen:         report.OpenCount,
			TotalExcluded:     report.Summary.TotalExcluded,
			ExclusionRate:     exclusionRate,
			ReasonCount:       report.Summary.MostCommonCount,
			ReasonPercentage:  mostCommonRate,
			Threshold:         et.alertThreshold,
		},
		Resolved: false,
	}

	return alert
}

// calculateSeverity determines the severity level based on rates.
func (et *ExclusionTracker) calculateSeverity(exclusionRate, mostCommonRate float64) string {
	if exclusionRate >= 0.95 || mostCommonRate >= 0.95 {
		return "critical"
	} else if exclusionRate >= 0.9 || mostCommonRate >= 0.9 {
		return "high"
	} else if exclusionRate >= 0.8 || mostCommonRate >= 0.8 {
		return "medium"
	}
	return "low"
}

// RecordAlert records a systemic issue alert.
func (et *ExclusionTracker) RecordAlert(alert *SystemicIssueAlert) {
	et.mu.Lock()
	defer et.mu.Unlock()

	// Check if this is a duplicate of an existing unresolved alert
	for _, existing := range et.alerts {
		if !existing.Resolved &&
			existing.Workspace == alert.Workspace &&
			existing.Reason == alert.Reason &&
			existing.AlertType == alert.AlertType {
			// Update the existing alert with new metrics
			existing.Metrics = alert.Metrics
			existing.Timestamp = alert.Timestamp
			existing.Description = alert.Description
			existing.Severity = alert.Severity
			if et.verbose {
				log.Printf("[ExclusionTracker] Updated existing alert: %s", existing.ID)
			}
			return
		}
	}

	// Add new alert
	et.alerts = append(et.alerts, *alert)
	if et.verbose {
		log.Printf("[ExclusionTracker] New alert recorded: %s - %s", alert.ID, alert.Description)
	}
}

// GetActiveAlerts returns all unresolved alerts.
func (et *ExclusionTracker) GetActiveAlerts() []SystemicIssueAlert {
	et.mu.RLock()
	defer et.mu.RUnlock()

	active := make([]SystemicIssueAlert, 0)
	for _, alert := range et.alerts {
		if !alert.Resolved {
			active = append(active, alert)
		}
	}
	return active
}

// GetAllAlerts returns all alerts (resolved and unresolved).
func (et *ExclusionTracker) GetAllAlerts() []SystemicIssueAlert {
	et.mu.RLock()
	defer et.mu.RUnlock()

	alerts := make([]SystemicIssueAlert, len(et.alerts))
	copy(alerts, et.alerts)
	return alerts
}

// ResolveAlert marks an alert as resolved.
func (et *ExclusionTracker) ResolveAlert(alertID string) error {
	et.mu.Lock()
	defer et.mu.Unlock()

	for i, alert := range et.alerts {
		if alert.ID == alertID && !alert.Resolved {
			now := time.Now()
			et.alerts[i].Resolved = true
			et.alerts[i].ResolvedAt = &now
			if et.verbose {
				log.Printf("[ExclusionTracker] Alert resolved: %s", alertID)
			}
			return nil
		}
	}
	return fmt.Errorf("alert not found or already resolved: %s", alertID)
}

// WorkerHeartbeat represents a worker's heartbeat record.
type WorkerHeartbeat struct {
	Worker     string    `json:"worker"`
	State      string    `json:"state"`
	Timestamp  time.Time `json:"ts"`
	LastStrand string    `json:"last_strand"`
}

// getLastHeartbeat retrieves the last heartbeat for a worker.
func (et *ExclusionTracker) getLastHeartbeat(worker string) (*WorkerHeartbeat, error) {
	if et.workspaceRoot == "" {
		return nil, fmt.Errorf("workspace root not configured")
	}

	heartbeatPath := filepath.Join(et.workspaceRoot, ".beads", "heartbeats.jsonl")

	f, err := os.Open(heartbeatPath)
	if err != nil {
		return nil, fmt.Errorf("open heartbeats file: %w", err)
	}
	defer f.Close()

	var lastHeartbeat *WorkerHeartbeat
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := scanner.Text()
		var hb WorkerHeartbeat
		if err := json.Unmarshal([]byte(line), &hb); err != nil {
			continue
		}
		if hb.Worker == worker {
			lastHeartbeat = &hb
		}
	}

	if lastHeartbeat == nil {
		return nil, fmt.Errorf("no heartbeat found for worker %s", worker)
	}

	return lastHeartbeat, nil
}

// isWorkerStale checks if a worker's last heartbeat exceeds the staleness threshold.
func (et *ExclusionTracker) isWorkerStale(worker string) (bool, time.Duration, error) {
	lastHB, err := et.getLastHeartbeat(worker)
	if err != nil {
		return false, 0, err
	}

	now := time.Now()
	inactiveDuration := now.Sub(lastHB.Timestamp)

	return inactiveDuration > et.staleWorkerThreshold, inactiveDuration, nil
}
