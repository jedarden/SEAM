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
	"time"
)

// PluckResult represents a bead that was found by a query strategy.
type PluckResult struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Assignee    string   `json:"assignee,omitempty"`
	Priority    int      `json:"priority"`
	Labels      []string `json:"labels,omitempty"`
	QuerySource string   `json:"query_source"` // Which strategy found this bead
}

// QueryStrategy represents a single fallback query strategy.
type QueryStrategy interface {
	Name() string
	Execute(ctx context.Context, workspace string) ([]PluckResult, error)
}

// PrimaryQueryStrategy uses `bead list --ready` (the standard query).
type PrimaryQueryStrategy struct{}

func (s *PrimaryQueryStrategy) Name() string {
	return "primary"
}

func (s *PrimaryQueryStrategy) Execute(ctx context.Context, workspace string) ([]PluckResult, error) {
	cmd := exec.CommandContext(ctx, "bead", "list", "--ready", "--json")
	cmd.Dir = workspace

	output, err := cmd.Output()
	if err != nil {
		// No ready beads is not an error - return empty slice
		return []PluckResult{}, nil
	}

	return parseBeadListJSON(output, s.Name()), nil
}

// OpenStatusQueryStrategy uses `bead list --status open --json`.
type OpenStatusQueryStrategy struct{}

func (s *OpenStatusQueryStrategy) Name() string {
	return "open_status"
}

func (s *OpenStatusQueryStrategy) Execute(ctx context.Context, workspace string) ([]PluckResult, error) {
	cmd := exec.CommandContext(ctx, "bead", "list", "--status", "open", "--json")
	cmd.Dir = workspace

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bead list --status open: %w", err)
	}

	return parseBeadListJSON(output, s.Name()), nil
}

// DirectDBQueryStrategy queries SQLite directly.
type DirectDBQueryStrategy struct{}

func (s *DirectDBQueryStrategy) Name() string {
	return "direct_db"
}

func (s *DirectDBQueryStrategy) Execute(ctx context.Context, workspace string) ([]PluckResult, error) {
	dbPath := filepath.Join(workspace, ".beads", "beads.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("database not found: %s", dbPath)
	}

	// Query for open (status=0) or in_progress (status=1) beads
	// Status enum: 0=open, 1=in_progress, 2=deferred, 3=closed
	query := `SELECT id, title, status, assignee, priority FROM issues WHERE status IN (0, 1) LIMIT 50`
	cmd := exec.CommandContext(ctx, "sqlite3", dbPath, query)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sqlite3 query failed: %w", err)
	}

	return parseSQLiteOutput(output, s.Name()), nil
}

// CheckpointQueryStrategy reads from the checkpoint JSON file.
type CheckpointQueryStrategy struct{}

func (s *CheckpointQueryStrategy) Name() string {
	return "checkpoint"
}

func (s *CheckpointQueryStrategy) Execute(ctx context.Context, workspace string) ([]PluckResult, error) {
	checkpointPath := filepath.Join(workspace, ".beads", "checkpoint", "current.json")
	if _, err := os.Stat(checkpointPath); err != nil {
		return nil, fmt.Errorf("checkpoint not found: %s", checkpointPath)
	}

	data, err := os.ReadFile(checkpointPath)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}

	var checkpoint struct {
		Issues []struct {
			ID       string   `json:"id"`
			Title    string   `json:"title"`
			Status   int      `json:"status"`
			Assignee *string  `json:"assignee,omitempty"`
			Priority int      `json:"priority"`
			Labels   []string `json:"labels,omitempty"`
		} `json:"issues"`
	}

	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, fmt.Errorf("parse checkpoint JSON: %w", err)
	}

	results := make([]PluckResult, 0)
	for _, issue := range checkpoint.Issues {
		// Only include open (0) or in_progress (1)
		if issue.Status != 0 && issue.Status != 1 {
			continue
		}

		result := PluckResult{
			ID:          issue.ID,
			Title:       issue.Title,
			Status:       statusToString(issue.Status),
			Priority:    issue.Priority,
			Labels:      issue.Labels,
			QuerySource: s.Name(),
		}

		if issue.Assignee != nil && *issue.Assignee != "" {
			result.Assignee = *issue.Assignee
		}

		results = append(results, result)
	}

	return results, nil
}

// PluckFallback coordinates multiple query strategies with fallback logic.
type PluckFallback struct {
	strategies   []QueryStrategy
	verbose      bool
	diagnosticLog *os.File
}

// NewPluckFallback creates a new PluckFallback with default strategies.
func NewPluckFallback(verbose bool, diagnosticLogPath string) (*PluckFallback, error) {
	pf := &PluckFallback{
		strategies: []QueryStrategy{
			&PrimaryQueryStrategy{},
			&OpenStatusQueryStrategy{},
			&DirectDBQueryStrategy{},
			&CheckpointQueryStrategy{},
		},
		verbose: verbose,
	}

	if diagnosticLogPath != "" {
		f, err := os.OpenFile(diagnosticLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("open diagnostic log: %w", err)
		}
		pf.diagnosticLog = f
	}

	return pf, nil
}

// Pluck attempts to find bead candidates using fallback strategies.
// Returns candidates found, which strategy succeeded, and any visibility discrepancies.
func (pf *PluckFallback) Pluck(ctx context.Context, workspace string) ([]PluckResult, string, []string, error) {
	var discrepancies []string
	var lastError error

	for i, strategy := range pf.strategies {
		results, err := strategy.Execute(ctx, workspace)
		if err != nil {
			if pf.verbose {
				log.Printf("[%s] Strategy %d (%s) failed: %v", workspace, i, strategy.Name(), err)
			}
			lastError = err
			continue
		}

		// Strategy succeeded
		if pf.verbose {
			log.Printf("[%s] Strategy %d (%s) returned %d candidates", workspace, i, strategy.Name(), len(results))
		}

		// If this is a fallback strategy (not primary) and it found results,
		// log the visibility discrepancy
		if i > 0 && len(results) > 0 {
			discrepancy := fmt.Sprintf("[%s] Visibility bug detected: primary query returned 0, but %s returned %d candidates",
				time.Now().Format(time.RFC3339), strategy.Name(), len(results))
			discrepancies = append(discrepancies, discrepancy)

			if pf.diagnosticLog != nil {
				fmt.Fprintf(pf.diagnosticLog, "%s\n", discrepancy)
				for _, r := range results {
					fmt.Fprintf(pf.diagnosticLog, "  - Recovered bead: %s (%s)\n", r.ID, r.Title)
				}
				pf.diagnosticLog.Sync()
			}
		}

		return results, strategy.Name(), discrepancies, nil
	}

	// All strategies failed
	return nil, "", discrepancies, lastError
}

// Close closes the diagnostic log if open.
func (pf *PluckFallback) Close() error {
	if pf.diagnosticLog != nil {
		return pf.diagnosticLog.Close()
	}
	return nil
}

// parseBeadListJSON parses JSONL output from `bead list --json`.
// Each line is a separate JSON object representing one bead.
func parseBeadListJSON(data []byte, source string) []PluckResult {
	var results []PluckResult
	scanner := bufio.NewScanner(strings.NewReader(string(data)))

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var bead struct {
			ID       string   `json:"id"`
			Title    string   `json:"title"`
			Status   string   `json:"status"`
			Assignee string   `json:"assignee,omitempty"`
			Priority int      `json:"priority"`
			Labels   []string `json:"labels,omitempty"`
		}

		if err := json.Unmarshal([]byte(line), &bead); err != nil {
			log.Printf("Failed to parse bead JSON line: %s", line)
			continue
		}

		results = append(results, PluckResult{
			ID:          bead.ID,
			Title:       bead.Title,
			Status:      bead.Status,
			Assignee:    bead.Assignee,
			Priority:    bead.Priority,
			Labels:      bead.Labels,
			QuerySource: source,
		})
	}

	return results
}

// parseSQLiteOutput parses tab-separated output from sqlite3 command.
func parseSQLiteOutput(data []byte, source string) []PluckResult {
	var results []PluckResult
	scanner := bufio.NewScanner(strings.NewReader(string(data)))

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Output format: id|title|status|assignee|priority (tab-separated by default)
		fields := strings.Split(line, "|")
		if len(fields) < 5 {
			continue
		}

		priority := 0
		fmt.Sscanf(fields[4], "%d", &priority)

		results = append(results, PluckResult{
			ID:          strings.TrimSpace(fields[0]),
			Title:       strings.TrimSpace(fields[1]),
			Status:      statusToString(intOfString(fields[2])),
			Assignee:    strings.TrimSpace(fields[3]),
			Priority:    priority,
			QuerySource: source,
		})
	}

	return results
}

// statusToString converts numeric status to string.
func statusToString(status int) string {
	switch status {
	case 0:
		return "open"
	case 1:
		return "in_progress"
	case 2:
		return "deferred"
	case 3:
		return "closed"
	default:
		return "unknown"
	}
}

// intOfString converts string to int.
func intOfString(s string) int {
	var i int
	fmt.Sscanf(s, "%d", &i)
	return i
}
