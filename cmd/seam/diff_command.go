package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	seamspec "github.com/ardenone/seam/internal/spec"
)

// diffCommand is the CLI wrapper for the diff command
func diffCommand(args []string) {
	os.Exit(runDiffCommand(args, os.Stdout, os.Stderr))
}

// runDiffCommand implements the diff command
// Usage: seam diff [--fragments-dir <dir>] [--base <dir>] [--json]
//
// Flags:
//
//	--fragments-dir, -f: Directory containing route fragments (default: ./fragments)
//	--base, -b: Base directory to compare against (default: compare with git HEAD)
//	--json: Emit JSON output instead of human-readable diff
//	--output, -o: Write merged spec to file instead of stdout
//
// Positional args are not supported: the command always diffs the whole
// fragments directory, and path arguments are rejected rather than ignored.
func runDiffCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	fs.SetOutput(stderr)

	fragmentsDir := "./fragments"
	baseDir := ""
	jsonOutput := false
	outputFile := ""
	formatUnified := true
	formatSideBySide := false

	fs.StringVar(&fragmentsDir, "fragments-dir", fragmentsDir, "Directory containing route fragments")
	fs.StringVar(&fragmentsDir, "f", fragmentsDir, "Alias for --fragments-dir")
	fs.StringVar(&baseDir, "base", baseDir, "Base directory to compare against")
	fs.StringVar(&baseDir, "b", baseDir, "Alias for --base")
	fs.BoolVar(&jsonOutput, "json", jsonOutput, "Emit JSON output instead of human-readable diff")
	fs.BoolVar(&jsonOutput, "j", jsonOutput, "Alias for --json")
	fs.StringVar(&outputFile, "output", outputFile, "Write merged spec to file")
	fs.StringVar(&outputFile, "o", outputFile, "Alias for --output")
	fs.BoolVar(&formatUnified, "unified", formatUnified, "Use unified diff format (default)")
	fs.BoolVar(&formatSideBySide, "side-by-side", formatSideBySide, "Use side-by-side diff format")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(fs.Args()) > 0 {
		fmt.Fprintf(stderr, "seam diff: positional path arguments are not supported; pass --fragments-dir instead\n")
		return 2
	}

	// Override with environment variable
	if val := os.Getenv("SEAM_FRAGMENTS_DIR"); val != "" && fragmentsDir == "./fragments" {
		fragmentsDir = val
	}

	// If no base directory specified, try to use git to get the base version
	if baseDir == "" {
		baseDir = detectGitBase()
		if baseDir != "" {
			fmt.Fprintf(stderr, "seam diff: comparing against git HEAD:%s\n", baseDir)
		} else {
			// No git base, so we can't diff
			fmt.Fprintf(stderr, "seam diff: no base directory specified and not in a git repository\n")
			fmt.Fprintf(stderr, "Usage: seam diff [--base <dir>] [--fragments-dir <dir>]\n")
			return 2
		}
	}

	// Load current fragments
	currentLoader, err := seamspec.NewFragmentLoader()
	if err != nil {
		fmt.Fprintf(stderr, "seam diff: failed to create fragment loader: %v\n", err)
		return 2
	}

	if err := currentLoader.LoadDirectory(fragmentsDir); err != nil {
		fmt.Fprintf(stderr, "seam diff: failed to load current fragments: %v\n", err)
		return 2
	}

	// Merge current fragments
	currentJSON, err := currentLoader.MergeFragments("http://localhost:8080")
	if err != nil {
		fmt.Fprintf(stderr, "seam diff: failed to merge current fragments: %v\n", err)
		return 2
	}

	// Load base fragments
	baseLoader, err := seamspec.NewFragmentLoader()
	if err != nil {
		fmt.Fprintf(stderr, "seam diff: failed to create base fragment loader: %v\n", err)
		return 2
	}

	if err := baseLoader.LoadDirectory(baseDir); err != nil {
		fmt.Fprintf(stderr, "seam diff: failed to load base fragments: %v\n", err)
		return 2
	}

	// Merge base fragments
	baseJSON, err := baseLoader.MergeFragments("http://localhost:8080")
	if err != nil {
		fmt.Fprintf(stderr, "seam diff: failed to merge base fragments: %v\n", err)
		return 2
	}

	// Compare the two merged specs
	result, err := compareSpecs(baseJSON, currentJSON)
	if err != nil {
		fmt.Fprintf(stderr, "seam diff: failed to compare specs: %v\n", err)
		return 2
	}

	// Write output
	if jsonOutput {
		if err := writeDiffJSON(stdout, result); err != nil {
			fmt.Fprintf(stderr, "seam diff: failed to write JSON: %v\n", err)
			return 2
		}
	} else {
		if err := writeDiffText(stdout, result, formatUnified, formatSideBySide); err != nil {
			fmt.Fprintf(stderr, "seam diff: failed to write diff: %v\n", err)
			return 2
		}
	}

	// Write to output file if specified
	if outputFile != "" {
		if err := os.WriteFile(outputFile, currentJSON, 0644); err != nil {
			fmt.Fprintf(stderr, "seam diff: failed to write output file: %v\n", err)
			return 2
		}
		fmt.Fprintf(stderr, "seam diff: wrote merged spec to %s\n", outputFile)
	}

	// Return non-zero if there were changes
	if result.HasChanges() {
		return 1
	}
	return 0
}

// detectGitBase attempts to detect the base directory by checking git
// Returns the path to a temporary directory containing the git HEAD version
func detectGitBase() string {
	// Check if we're in a git repository
	if _, err := os.Stat(".git"); os.IsNotExist(err) {
		return ""
	}

	// Create a temporary directory for the base version
	tempDir, err := os.MkdirTemp("", "seam-diff-base-*")
	if err != nil {
		return ""
	}

	// Use git commands to extract the fragments directory at HEAD
	// git archive HEAD -- fragments.tar | tar -x -C {tempDir}
	cmd := exec.Command("git", "archive", "HEAD", "--", "./fragments")
	cmd.Dir = "."
	var archive bytes.Buffer
	cmd.Stdout = &archive
	if err := cmd.Run(); err != nil {
		os.RemoveAll(tempDir)
		return ""
	}

	// Extract the archive into the temp directory
	// Use tar command to extract
	cmd = exec.Command("tar", "-x", "-C", tempDir)
	cmd.Stdin = &archive
	if err := cmd.Run(); err != nil {
		os.RemoveAll(tempDir)
		return ""
	}

	return filepath.Join(tempDir, "fragments")
}

// DiffResult represents the result of comparing two specs
type DiffResult struct {
	PathsAdded      []string           `json:"paths_added"`
	PathsRemoved    []string           `json:"paths_removed"`
	PathsModified   []PathModification `json:"paths_modified"`
	FragmentChanges []FragmentChange   `json:"fragment_changes"`
	BaseHash        string             `json:"base_hash"`
	CurrentHash     string             `json:"current_hash"`
	Summary         DiffSummary        `json:"summary"`
}

// PathModification represents a change to a specific path
type PathModification struct {
	Path       string            `json:"path"`
	Operations []OperationChange `json:"operations"`
}

// OperationChange represents a change to an operation within a path
type OperationChange struct {
	Method       string                 `json:"method"`
	ChangeType   string                 `json:"change_type"` // "added", "removed", "modified"
	OldValue     map[string]interface{} `json:"old_value,omitempty"`
	NewValue     map[string]interface{} `json:"new_value,omitempty"`
	FieldChanges []FieldChange          `json:"field_changes,omitempty"`
}

// FieldChange represents a change to a specific field
type FieldChange struct {
	Field    string      `json:"field"`
	OldValue interface{} `json:"old_value,omitempty"`
	NewValue interface{} `json:"new_value,omitempty"`
}

// FragmentChange represents a change to a fragment file
type FragmentChange struct {
	File          string   `json:"file"`
	ChangeType    string   `json:"change_type"` // "added", "removed", "modified"
	PathsAffected []string `json:"paths_affected,omitempty"`
}

// DiffSummary provides a summary of the diff
type DiffSummary struct {
	TotalChanges  int  `json:"total_changes"`
	PathsAdded    int  `json:"paths_added"`
	PathsRemoved  int  `json:"paths_removed"`
	PathsModified int  `json:"paths_modified"`
	HasChanges    bool `json:"has_changes"`
}

// HasChanges returns true if there are any changes in the diff result
func (d *DiffResult) HasChanges() bool {
	return d.Summary.HasChanges
}

// compareSpecs compares two merged OpenAPI specs and returns the diff
func compareSpecs(baseJSON, currentJSON []byte) (*DiffResult, error) {
	var base, current map[string]interface{}
	if err := json.Unmarshal(baseJSON, &base); err != nil {
		return nil, fmt.Errorf("failed to parse base spec: %w", err)
	}
	if err := json.Unmarshal(currentJSON, &current); err != nil {
		return nil, fmt.Errorf("failed to parse current spec: %w", err)
	}

	result := &DiffResult{
		PathsAdded:      []string{},
		PathsRemoved:    []string{},
		PathsModified:   []PathModification{},
		FragmentChanges: []FragmentChange{},
		Summary:         DiffSummary{},
	}

	// Compare paths
	basePaths, ok := base["paths"].(map[string]interface{})
	if !ok {
		basePaths = map[string]interface{}{}
	}

	currentPaths, ok := current["paths"].(map[string]interface{})
	if !ok {
		currentPaths = map[string]interface{}{}
	}

	// Find added and removed paths
	for path := range currentPaths {
		if _, exists := basePaths[path]; !exists {
			result.PathsAdded = append(result.PathsAdded, path)
		}
	}

	for path := range basePaths {
		if _, exists := currentPaths[path]; !exists {
			result.PathsRemoved = append(result.PathsRemoved, path)
		}
	}

	// Find modified paths
	for path := range currentPaths {
		if _, exists := basePaths[path]; exists {
			basePathItem := basePaths[path].(map[string]interface{})
			currentPathItem := currentPaths[path].(map[string]interface{})

			mod := PathModification{Path: path}
			for method := range currentPathItem {
				if isHTTPMethod(method) {
					currentOp := currentPathItem[method].(map[string]interface{})
					if baseOp, exists := basePathItem[method]; exists {
						// Operation exists in both, check for modifications
						baseOpMap := baseOp.(map[string]interface{})
						if !mapsEqual(currentOp, baseOpMap) {
							fieldChanges := compareMaps(currentOp, baseOpMap)
							mod.Operations = append(mod.Operations, OperationChange{
								Method:       strings.ToUpper(method),
								ChangeType:   "modified",
								OldValue:     baseOpMap,
								NewValue:     currentOp,
								FieldChanges: fieldChanges,
							})
						}
					} else {
						// Operation added
						mod.Operations = append(mod.Operations, OperationChange{
							Method:     strings.ToUpper(method),
							ChangeType: "added",
							NewValue:   currentOp,
						})
					}
				}
			}

			// Check for removed operations
			for method := range basePathItem {
				if isHTTPMethod(method) {
					if _, exists := currentPathItem[method]; !exists {
						baseOp := basePathItem[method].(map[string]interface{})
						mod.Operations = append(mod.Operations, OperationChange{
							Method:     strings.ToUpper(method),
							ChangeType: "removed",
							OldValue:   baseOp,
						})
					}
				}
			}

			if len(mod.Operations) > 0 {
				result.PathsModified = append(result.PathsModified, mod)
			}
		}
	}

	// Update summary
	result.Summary.PathsAdded = len(result.PathsAdded)
	result.Summary.PathsRemoved = len(result.PathsRemoved)
	result.Summary.PathsModified = len(result.PathsModified)
	result.Summary.TotalChanges = result.Summary.PathsAdded + result.Summary.PathsRemoved + result.Summary.PathsModified
	result.Summary.HasChanges = result.Summary.TotalChanges > 0

	return result, nil
}

// isHTTPMethod checks if a string is an HTTP method
func isHTTPMethod(s string) bool {
	switch strings.ToLower(s) {
	case "get", "post", "put", "delete", "patch", "head", "options", "trace":
		return true
	default:
		return false
	}
}

// mapsEqual compares two maps for equality
func mapsEqual(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, exists := b[k]; !exists {
			return false
		} else {
			// Simple comparison - for proper JSON equality we'd need more sophisticated handling
			if fmt.Sprintf("%v", v) != fmt.Sprintf("%v", bv) {
				return false
			}
		}
	}
	return true
}

// compareMaps compares two maps and returns field-level differences
func compareMaps(current, base map[string]interface{}) []FieldChange {
	changes := []FieldChange{}

	// Find added/modified fields
	for k, v := range current {
		if bv, exists := base[k]; exists {
			if fmt.Sprintf("%v", v) != fmt.Sprintf("%v", bv) {
				changes = append(changes, FieldChange{
					Field:    k,
					OldValue: bv,
					NewValue: v,
				})
			}
		} else {
			changes = append(changes, FieldChange{
				Field:    k,
				NewValue: v,
			})
		}
	}

	// Find removed fields
	for k, v := range base {
		if _, exists := current[k]; !exists {
			changes = append(changes, FieldChange{
				Field:    k,
				OldValue: v,
			})
		}
	}

	return changes
}

// writeDiffJSON writes the diff result as JSON
func writeDiffJSON(w io.Writer, result *DiffResult) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

// writeDiffText writes the diff result in human-readable format
func writeDiffText(w io.Writer, result *DiffResult, unified, sideBySide bool) error {
	fmt.Fprintf(w, "SEAM Fragment Diff\n")
	fmt.Fprintf(w, "==================\n\n")

	// Write summary
	fmt.Fprintf(w, "Summary: %d total changes\n", result.Summary.TotalChanges)
	fmt.Fprintf(w, "  Paths added: %d\n", result.Summary.PathsAdded)
	fmt.Fprintf(w, "  Paths removed: %d\n", result.Summary.PathsRemoved)
	fmt.Fprintf(w, "  Paths modified: %d\n", result.Summary.PathsModified)
	fmt.Fprintln(w)

	// Write added paths
	if len(result.PathsAdded) > 0 {
		fmt.Fprintf(w, "Paths Added (%d):\n", len(result.PathsAdded))
		for _, path := range result.PathsAdded {
			fmt.Fprintf(w, "  + %s\n", path)
		}
		fmt.Fprintln(w)
	}

	// Write removed paths
	if len(result.PathsRemoved) > 0 {
		fmt.Fprintf(w, "Paths Removed (%d):\n", len(result.PathsRemoved))
		for _, path := range result.PathsRemoved {
			fmt.Fprintf(w, "  - %s\n", path)
		}
		fmt.Fprintln(w)
	}

	// Write modified paths
	if len(result.PathsModified) > 0 {
		fmt.Fprintf(w, "Paths Modified (%d):\n", len(result.PathsModified))
		for _, mod := range result.PathsModified {
			fmt.Fprintf(w, "  * %s\n", mod.Path)
			for _, opChange := range mod.Operations {
				fmt.Fprintf(w, "      %s %s\n", opChange.Method, opChange.ChangeType)
				for _, fieldChange := range opChange.FieldChanges {
					if fieldChange.OldValue != nil && fieldChange.NewValue != nil {
						fmt.Fprintf(w, "        ~ %s: %v -> %v\n", fieldChange.Field, fieldChange.OldValue, fieldChange.NewValue)
					} else if fieldChange.OldValue != nil {
						fmt.Fprintf(w, "        - %s: %v\n", fieldChange.Field, fieldChange.OldValue)
					} else if fieldChange.NewValue != nil {
						fmt.Fprintf(w, "        + %s: %v\n", fieldChange.Field, fieldChange.NewValue)
					}
				}
			}
		}
		fmt.Fprintln(w)
	}

	return nil
}
