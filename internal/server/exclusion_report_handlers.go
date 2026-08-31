package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/ardenone/seam/internal/pluckfallback"
)

// exclusionReportData holds the response structure for exclusion reports.
type exclusionReportData struct {
	WorkspacePath    string                              `json:"workspace_path"`
	WorkspaceName    string                              `json:"workspace_name"`
	Timestamp        time.Time                           `json:"timestamp"`
	ReadyCount       int                                 `json:"ready_count"`
	OpenCount        int                                 `json:"open_count"`
	ExcludedBeads    []pluckfallback.BeadExclusion      `json:"excluded_beads"`
	Summary          pluckfallback.ExclusionSummary     `json:"summary"`
	QuerySuccessful  bool                                `json:"query_successful"`
	QueryError       string                              `json:"query_error,omitempty"`
	StarvationDetected bool                              `json:"starvation_detected"`
}

// exclusionListResponse holds the response for listing all exclusion reports.
type exclusionListResponse struct {
	Timestamp    time.Time              `json:"timestamp"`
	TotalReports int                    `json:"total_reports"`
	Reports      []exclusionReportData  `json:"reports"`
}

// exclusionAnalysisRequest holds the request body for on-demand exclusion analysis.
type exclusionAnalysisRequest struct {
	WorkspacePath string `json:"workspace_path"`
}

// exclusionAnalysisResponse holds the response for on-demand exclusion analysis.
type exclusionAnalysisResponse struct {
	Report   *exclusionReportData `json:"report,omitempty"`
	Success  bool                 `json:"success"`
	Error    string               `json:"error,omitempty"`
	Analyzed bool                 `json:"analyzed"`
}

// exclusionReportHandler returns the latest exclusion report for a specific workspace.
// Handler: GET /api/v1/exclusions/report?workspace=<path>
func (s *Server) exclusionReportHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	workspacePath := r.URL.Query().Get("workspace")
	if workspacePath == "" {
		http.Error(w, "Missing workspace parameter", http.StatusBadRequest)
		return
	}

	// Get the exclusion tracker from the starvation alert self-resolution daemon
	// (This assumes the daemon is accessible via the server - we'll need to wire this up)
	// For now, we'll read from the log file if it exists

	logPath := filepath.Join(workspacePath, ".beads", "diagnostics", "starvation-alert-resolution-exclusions.jsonl")
	report, err := readLatestExclusionReportFromLog(logPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read exclusion report: %v", err), http.StatusInternalServerError)
		log.Printf("[exclusionReportHandler] Failed to read report for %s: %v", workspacePath, err)
		return
	}

	if report == nil {
		http.Error(w, "No exclusion report found for workspace", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// exclusionAllReportsHandler returns all exclusion reports across all workspaces.
// Handler: GET /api/v1/exclusions/reports
func (s *Server) exclusionAllReportsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get all reports from the tracker
	response := exclusionListResponse{
		Timestamp:    time.Now(),
		TotalReports: 0,
		Reports:      []exclusionReportData{},
	}

	if s.exclusionTracker != nil {
		allReports := s.exclusionTracker.GetAllReports()
		response.TotalReports = len(allReports)

		for workspace, report := range allReports {
			reportData := convertExclusionReport(report)
			response.Reports = append(response.Reports, *reportData)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// exclusionAnalyzeHandler performs on-demand exclusion analysis for a workspace.
// Handler: POST /api/v1/exclusions/analyze
// Request body: {"workspace_path": "/path/to/workspace"}
func (s *Server) exclusionAnalyzeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req exclusionAnalysisRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.WorkspacePath == "" {
		http.Error(w, "Missing workspace_path in request body", http.StatusBadRequest)
		return
	}

	// Create a temporary exclusion tracker for this analysis
	logPath := filepath.Join(req.WorkspacePath, ".beads", "diagnostics", "on-demand-exclusions.jsonl")
	// Use parent of workspace path as root (for heartbeats.jsonl access)
	workspaceRoot := filepath.Dir(req.WorkspacePath)
	tracker, err := pluckfallback.NewExclusionTracker(logPath, true, workspaceRoot, 30*time.Minute)
	if err != nil {
		response := exclusionAnalysisResponse{
			Success: false,
			Error:   fmt.Sprintf("Failed to create exclusion tracker: %v", err),
			Analyzed: false,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(response)
		return
	}
	defer tracker.Close()

	// Run the analysis
	ctx := context.Background()
	report, err := tracker.TrackExclusions(ctx, req.WorkspacePath)
	if err != nil {
		response := exclusionAnalysisResponse{
			Success: false,
			Error:   fmt.Sprintf("Analysis failed: %v", err),
			Analyzed: false,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(response)
		return
	}

	// Convert the report
	reportData := convertExclusionReport(report)
	reportData.StarvationDetected = (report.ReadyCount == 0 && report.OpenCount > 0)

	response := exclusionAnalysisResponse{
		Report:   reportData,
		Success:  true,
		Analyzed: true,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)

	log.Printf("[exclusionAnalyzeHandler] Completed analysis for %s: %d excluded beads",
		req.WorkspacePath, report.Summary.TotalExcluded)
}

// exclusionSummaryHandler returns a summary of recent exclusion activity.
// Handler: GET /api/v1/exclusions/summary
func (s *Server) exclusionSummaryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	summary := map[string]interface{}{
		"timestamp":             time.Now(),
		"workspaces_analyzed":   0,
		"total_exclusions":       0,
		"starvation_events":     0,
		"most_common_reason":     nil,
		"active_alerts":         0,
		"rolling_window_stats":  nil,
		"message":              "Exclusion tracking active",
	}

	if s.exclusionTracker != nil {
		// Get rolling window statistics
		rollingStats := s.exclusionTracker.GetRollingWindowStats()
		summary["rolling_window_stats"] = rollingStats

		// Get all reports
		allReports := s.exclusionTracker.GetAllReports()
		summary["workspaces_analyzed"] = len(allReports)

		// Calculate totals
		totalExclusions := 0
		starvationEvents := 0
		mostCommonReason := ""
		mostCommonCount := 0
		reasonCounts := make(map[string]int)

		for _, report := range allReports {
			totalExclusions += report.Summary.TotalExcluded
			if report.ReadyCount == 0 && report.OpenCount > 0 {
				starvationEvents++
			}

			// Aggregate reason counts
			for reason, count := range report.Summary.ByReason {
				reasonCounts[string(reason)] += count
			}
		}

		summary["total_exclusions"] = totalExclusions
		summary["starvation_events"] = starvationEvents

		// Find most common reason overall
		for reason, count := range reasonCounts {
			if count > mostCommonCount {
				mostCommonReason = reason
				mostCommonCount = count
			}
		}
		summary["most_common_reason"] = mostCommonReason

		// Get active alerts
		activeAlerts := s.exclusionTracker.GetActiveAlerts()
		summary["active_alerts"] = len(activeAlerts)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

// exclusionAlertsHandler returns all alerts (active and resolved).
// Handler: GET /api/v1/exclusions/alerts
func (s *Server) exclusionAlertsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.exclusionTracker == nil {
		http.Error(w, "Exclusion tracker not initialized", http.StatusServiceUnavailable)
		return
	}

	alerts := s.exclusionTracker.GetAllAlerts()

	response := map[string]interface{}{
		"timestamp":    time.Now(),
		"total_alerts": len(alerts),
		"alerts":       alerts,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// exclusionActiveAlertsHandler returns only active (unresolved) alerts.
// Handler: GET /api/v1/exclusions/alerts/active
func (s *Server) exclusionActiveAlertsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.exclusionTracker == nil {
		http.Error(w, "Exclusion tracker not initialized", http.StatusServiceUnavailable)
		return
	}

	alerts := s.exclusionTracker.GetActiveAlerts()

	response := map[string]interface{}{
		"timestamp":      time.Now(),
		"active_alerts": len(alerts),
		"alerts":         alerts,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// exclusionResolveAlertHandler marks an alert as resolved.
// Handler: POST /api/v1/exclusions/alerts/resolve
// Request body: {"alert_id": "sysalert-..."}
func (s *Server) exclusionResolveAlertHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.exclusionTracker == nil {
		http.Error(w, "Exclusion tracker not initialized", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		AlertID string `json:"alert_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.AlertID == "" {
		http.Error(w, "Missing alert_id in request body", http.StatusBadRequest)
		return
	}

	if err := s.exclusionTracker.ResolveAlert(req.AlertID); err != nil {
		response := map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(response)
		return
	}

	response := map[string]interface{}{
		"success":  true,
		"alert_id": req.AlertID,
		"message":  "Alert resolved successfully",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)

	log.Printf("[exclusionResolveAlertHandler] Alert resolved: %s", req.AlertID)
}

// convertExclusionReport converts a pluckfallback.ExclusionReport to the HTTP response format.
func convertExclusionReport(report *pluckfallback.ExclusionReport) *exclusionReportData {
	if report == nil {
		return nil
	}

	return &exclusionReportData{
		WorkspacePath:     report.WorkspacePath,
		WorkspaceName:     filepath.Base(report.WorkspacePath),
		Timestamp:         report.Timestamp,
		ReadyCount:        report.ReadyCount,
		OpenCount:         report.OpenCount,
		ExcludedBeads:     report.ExcludedBeads,
		Summary:           report.Summary,
		QuerySuccessful:   report.QuerySuccessful,
		QueryError:        report.QueryError,
		StarvationDetected: (report.ReadyCount == 0 && report.OpenCount > 0),
	}
}

// readLatestExclusionReportFromLog reads the most recent exclusion report from a JSONL log file.
func readLatestExclusionReportFromLog(logPath string) (*exclusionReportData, error) {
	// This is a placeholder - the actual implementation would read the JSONL file
	// and parse the most recent entry. For now, we return nil to indicate no report found.
	//
	// Implementation would:
	// 1. Open the log file
	// 2. Read lines from the end backwards
	// 3. Parse each line as JSON
	// 4. Convert and return the most recent report

	return nil, fmt.Errorf("reading from log not yet implemented")
}

// logExclusionReportToDiagnostic logs an exclusion report to the diagnostic log.
func logExclusionReportToDiagnostic(report *pluckfallback.ExclusionReport, diagnosticLog string) error {
	if report == nil || diagnosticLog == "" {
		return nil
	}

	// Log a summary of the exclusion report
	workspaceName := filepath.Base(report.WorkspacePath)
	logMsg := fmt.Sprintf("[%s] Exclusion Analysis: %d open beads, %d ready, %d excluded",
		workspaceName, report.OpenCount, report.ReadyCount, report.Summary.TotalExcluded)

	if report.Summary.TotalExcluded > 0 {
		logMsg += fmt.Sprintf(" (Most common: %s with %d beads)",
			report.Summary.MostCommonReason, report.Summary.MostCommonCount)
	}

	log.Printf("[ExclusionReport] %s", logMsg)

	// Log detailed breakdown for top excluded beads
	if report.Summary.TotalExcluded > 0 {
		log.Printf("[ExclusionReport] Top excluded beads in %s:", workspaceName)
		for i, exclusion := range report.ExcludedBeads {
			if i >= 10 {
				break // Log top 10
			}
			log.Printf("[ExclusionReport]   %d. %s [%s] - %s: %s",
				i+1, exclusion.BeadID, exclusion.Status, exclusion.Reason, exclusion.Details)
		}
	}

	return nil
}
