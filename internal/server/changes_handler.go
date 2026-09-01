package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/ardenone/seam/internal/spec"
)

// changesHandler returns the API changes between spec versions.
//
// Phase 8.4: This endpoint provides two levels of diff:
//   - Level 1 (?level=1 or default): Route list with change indicators
//   - Level 2 (?level=2): Field-level diffs per route
//
// Query parameters:
//   - level: 1 or 2 (default: 1)
//   - since: Spec hash to compare against (returns 200 with sinceKnown: false if unknown/evicted)
//   - scope-since: Optional scope version hash for scope change detection
//
// Response structure (Level 1):
//   - since_spec: The spec hash being compared against
//   - since_known: Whether the since spec is known (false for unknown/evicted)
//   - current_spec: The current spec hash
//   - routes: Array of route change entries
//
// Response structure (Level 2):
//   - Same as Level 1, but routes include field_diff entries
//
// This is a reserved path (/changes) and bypasses route-table lookup.
func (s *Server) changesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}

	// Parse query parameters
	query := r.URL.Query()
	level := query.Get("level")
	since := query.Get("since")
	scopeSince := query.Get("scope-since")

	// Default to level 1
	if level == "" {
		level = "1"
	}

	// Validate level parameter
	if level != "1" && level != "2" {
		NewErrorResponse(ErrCodeBadRequest, "Invalid level parameter").
			WithDetail("valid_levels", "1, 2").
			Write(w, r)
		return
	}

	// Get caller's identity for scope filtering
	identity := identityFromContext(r.Context())
	effectiveScopes := []string{}
	if identity != nil {
		effectiveScopes = identity.Capabilities
	}

	// Get current spec from ring buffer
	currentHash, currentVersion, currentSpec, hasCurrent := s.specRingBuffer.GetCurrentVersion()
	if !hasCurrent {
		NewErrorResponse(ErrCodeServiceUnavailable, "No current spec available").
			Write(w, r)
		return
	}

	// If since is not provided, compare against the oldest known version
	if since == "" {
		// Get all versions and pick the oldest
		versions := s.specRingBuffer.GetAllVersions()
		if len(versions) > 0 {
			since = versions[0].SpecHash
		} else {
			// No history, compare against current (no changes)
			since = currentHash
		}
	}

	// Retrieve the since spec from ring buffer
	sinceSpec, sinceKnown, _ := s.specRingBuffer.Get(since)

	// Build response
	response := map[string]interface{}{
		"since_spec":    since,
		"since_known":   sinceKnown,
		"current_spec":  currentHash,
		"current_version": currentVersion,
		"query": map[string]interface{}{
			"level":        level,
			"since":        since,
			"scope_since":  scopeSince,
		},
	}

	// Calculate changes based on level
	var routeChanges []RouteChange
	if level == "1" {
		routeChanges = s.calculateLevel1Changes(sinceSpec, currentSpec, sinceKnown, effectiveScopes)
	} else {
		routeChanges = s.calculateLevel2Changes(sinceSpec, currentSpec, sinceKnown, effectiveScopes)
	}

	response["routes"] = routeChanges
	response["route_count"] = len(routeChanges)

	// Add scope change information if scope-since is provided
	if scopeSince != "" {
		response["scope_changes"] = s.calculateScopeChanges(scopeSince, identity)
	}

	// Set headers and return response
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-SEAM-Scope-Version", s.getCurrentScopeVersion(identity))
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

// RouteChange represents a route change entry in the /changes response
type RouteChange struct {
	Path            string   `json:"path"`                       // OpenAPI path template
	Verb            string   `json:"verb"`                       // HTTP method
	ContractKinds   []string `json:"contract_kinds,omitempty"`   // added, removed, params-changed, response-changed, deprecated
	VisibilityKinds []string `json:"visibility_kinds,omitempty"` // granted, revoked
	DiffURL         string   `json:"diff_url"`                   // Link to diff for this route
	DocsURL         string   `json:"docs_url"`                   // Link to docs for this route

	// Level 2 only
	FieldDiff      []FieldDiffEntry `json:"field_diff,omitempty"` // Field-level changes
}

// FieldDiffEntry represents a single field change (Level 2)
type FieldDiffEntry struct {
	Field    string `json:"field"`              // Field path (e.g., "parameters[0].description")
	OldValue string `json:"old_value,omitempty"` // Old value
	NewValue string `json:"new_value,omitempty"` // New value
	Change   string `json:"change"`             // added, removed, changed
}

// ScopeChange represents scope changes between versions
type ScopeChange struct {
	Scopes     []string `json:"scopes"`     // Scopes that changed
	ChangeType string   `json:"change_type"` // granted, revoked
}

// calculateLevel1Changes computes Level 1 route changes (contract kinds + visibility kinds)
func (s *Server) calculateLevel1Changes(sinceSpec, currentSpec []byte, sinceKnown bool, effectiveScopes []string) []RouteChange {
	changes := []RouteChange{}

	if !sinceKnown || sinceSpec == nil {
		// Since spec is unknown/evicted - return empty changes
		return changes
	}

	// Parse both specs
	var sinceMap, currentMap map[string]interface{}
	if err := json.Unmarshal(sinceSpec, &sinceMap); err != nil {
		return changes
	}
	if err := json.Unmarshal(currentSpec, &currentMap); err != nil {
		return changes
	}

	// Extract paths from both specs
	sincePaths, _ := sinceMap["paths"].(map[string]interface{})
	currentPaths, _ := currentMap["paths"].(map[string]interface{})

	// Track all routes we've seen
	seenRoutes := make(map[string]bool)

	// Check for added/modified routes
	for path, pathItem := range currentPaths {
		pathItemMap, ok := pathItem.(map[string]interface{})
		if !ok {
			continue
		}

		// Check each HTTP method
		for method, operation := range pathItemMap {
			if !spec.IsHTTPMethod(method) {
				continue
			}

			routeKey := fmt.Sprintf("%s %s", method, path)
			seenRoutes[routeKey] = true

			operationMap, ok := operation.(map[string]interface{})
			if !ok {
				continue
			}

			// Check if this route is visible to the caller
			if !s.isRouteVisible(operationMap, effectiveScopes) {
				continue
			}

			// Check if route existed in since spec
			sincePathItem, pathExists := sincePaths[path]
			if !pathExists {
				// Route was added
				changes = append(changes, RouteChange{
					Path:          path,
					Verb:          method,
					ContractKinds: []string{"added"},
					DiffURL:       s.buildDiffURL(path, method),
					DocsURL:       s.buildDocsURL(path, method),
				})
				continue
			}

			sincePathItemMap, ok := sincePathItem.(map[string]interface{})
			if !ok {
				continue
			}

			sinceOperation, methodExists := sincePathItemMap[method]
			if !methodExists {
				// Method was added to existing path
				changes = append(changes, RouteChange{
					Path:          path,
					Verb:          method,
					ContractKinds: []string{"added"},
					DiffURL:       s.buildDiffURL(path, method),
					DocsURL:       s.buildDocsURL(path, method),
				})
				continue
			}

			// Compare operations for changes
			sinceOperationMap, ok := sinceOperation.(map[string]interface{})
			if !ok {
				continue
			}

			contractKinds := s.calculateContractKinds(sinceOperationMap, operationMap)
			visibilityKinds := s.calculateVisibilityKinds(sinceOperationMap, operationMap, effectiveScopes)

			if len(contractKinds) > 0 || len(visibilityKinds) > 0 {
				changes = append(changes, RouteChange{
					Path:           path,
					Verb:           method,
					ContractKinds:  contractKinds,
					VisibilityKinds: visibilityKinds,
					DiffURL:        s.buildDiffURL(path, method),
					DocsURL:        s.buildDocsURL(path, method),
				})
			}
		}
	}

	// Check for removed routes
	for path, pathItem := range sincePaths {
		pathItemMap, ok := pathItem.(map[string]interface{})
		if !ok {
			continue
		}

		for method, operation := range pathItemMap {
			if !spec.IsHTTPMethod(method) {
				continue
			}

			routeKey := fmt.Sprintf("%s %s", method, path)
			if seenRoutes[routeKey] {
				continue
			}

			operationMap, ok := operation.(map[string]interface{})
			if !ok {
				continue
			}

			// Check if route was visible in the since spec
			if !s.isRouteVisible(operationMap, effectiveScopes) {
				continue
			}

			// Route was removed
			changes = append(changes, RouteChange{
				Path:          path,
				Verb:          method,
				ContractKinds: []string{"removed"},
				DiffURL:       s.buildDiffURL(path, method),
				DocsURL:       s.buildDocsURL(path, method),
			})
		}
	}

	// Sort changes by path and method for consistent output
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path != changes[j].Path {
			return changes[i].Path < changes[j].Path
		}
		return changes[i].Verb < changes[j].Verb
	})

	return changes
}

// calculateLevel2Changes computes Level 2 changes with field diffs
func (s *Server) calculateLevel2Changes(sinceSpec, currentSpec []byte, sinceKnown bool, effectiveScopes []string) []RouteChange {
	// Start with Level 1 changes
	changes := s.calculateLevel1Changes(sinceSpec, currentSpec, sinceKnown, effectiveScopes)

	if !sinceKnown || sinceSpec == nil {
		return changes
	}

	// Add field diffs to each change
	var sinceMap, currentMap map[string]interface{}
	if err := json.Unmarshal(sinceSpec, &sinceMap); err != nil {
		return changes
	}
	if err := json.Unmarshal(currentSpec, &currentMap); err != nil {
		return changes
	}

	sincePaths, _ := sinceMap["paths"].(map[string]interface{})
	currentPaths, _ := currentMap["paths"].(map[string]interface{})

	// Augment each change with field diffs
	for i := range changes {
		change := &changes[i]
		change.FieldDiff = s.calculateFieldDiff(
			sincePaths,
			currentPaths,
			change.Path,
			change.Verb,
		)
	}

	return changes
}

// calculateContractKinds determines contract change kinds
func (s *Server) calculateContractKinds(sinceOp, currentOp map[string]interface{}) []string {
	kinds := []string{}

	// Check for deprecation
	if wasDeprecated(sinceOp) && !isDeprecated(currentOp) {
		kinds = append(kinds, "undeprecated")
	} else if !wasDeprecated(sinceOp) && isDeprecated(currentOp) {
		kinds = append(kinds, "deprecated")
	}

	// Check for parameter changes
	if parametersChanged(sinceOp, currentOp) {
		kinds = append(kinds, "params-changed")
	}

	// Check for response changes
	if responseChanged(sinceOp, currentOp) {
		kinds = append(kinds, "response-changed")
	}

	return kinds
}

// calculateVisibilityKinds determines visibility change kinds
func (s *Server) calculateVisibilityKinds(sinceOp, currentOp map[string]interface{}, effectiveScopes []string) []string {
	kinds := []string{}

	sinceVisible := s.isRouteVisible(sinceOp, effectiveScopes)
	currentVisible := s.isRouteVisible(currentOp, effectiveScopes)

	if !sinceVisible && currentVisible {
		kinds = append(kinds, "granted")
	} else if sinceVisible && !currentVisible {
		kinds = append(kinds, "revoked")
	}

	return kinds
}

// isRouteVisible checks if a route is visible to the caller
func (s *Server) isRouteVisible(operation map[string]interface{}, effectiveScopes []string) bool {
	requiredScopes := spec.ExtractRequiredScopesFromMap(operation)

	// No scopes required = public route = visible
	if len(requiredScopes) == 0 {
		return true
	}

	// Check if caller has at least one required scope
	effectiveSet := make(map[string]bool)
	for _, scope := range effectiveScopes {
		effectiveSet[normalizeScopeKey(scope)] = true
	}

	for _, requiredScope := range requiredScopes {
		if effectiveSet[normalizeScopeKey(requiredScope)] {
			return true
		}
	}

	return false
}

// calculateFieldDiff computes field-level diffs for a route
func (s *Server) calculateFieldDiff(sincePaths, currentPaths map[string]interface{}, path, method string) []FieldDiffEntry {
	diffs := []FieldDiffEntry{}

	// Get both operations
	sincePathItem, sinceExists := sincePaths[path]
	currentPathItem, currentExists := currentPaths[path]

	if !sinceExists || !currentExists {
		return diffs
	}

	sincePathItemMap, _ := sincePathItem.(map[string]interface{})
	currentPathItemMap, _ := currentPathItem.(map[string]interface{})

	sinceOp, _ := sincePathItemMap[method]
	currentOp, _ := currentPathItemMap[method]

	sinceOpMap, _ := sinceOp.(map[string]interface{})
	currentOpMap, _ := currentOp.(map[string]interface{})

	// Compare specific fields
	diffs = append(diffs, s.compareField(sinceOpMap, currentOpMap, "summary")...)
	diffs = append(diffs, s.compareField(sinceOpMap, currentOpMap, "description")...)
	diffs = append(diffs, s.compareParameters(sinceOpMap, currentOpMap)...)
	diffs = append(diffs, s.compareResponses(sinceOpMap, currentOpMap)...)

	return diffs
}

// compareField compares a single field
func (s *Server) compareField(sinceOp, currentOp map[string]interface{}, field string) []FieldDiffEntry {
	diffs := []FieldDiffEntry{}

	sinceVal, sinceHas := sinceOp[field]
	currentVal, currentHas := currentOp[field]

	if !sinceHas && currentHas {
		diffs = append(diffs, FieldDiffEntry{
			Field:    field,
			NewValue: fmt.Sprintf("%v", currentVal),
			Change:   "added",
		})
	} else if sinceHas && !currentHas {
		diffs = append(diffs, FieldDiffEntry{
			Field:    field,
			OldValue: fmt.Sprintf("%v", sinceVal),
			Change:   "removed",
		})
	} else if sinceHas && currentHas && fmt.Sprintf("%v", sinceVal) != fmt.Sprintf("%v", currentVal) {
		diffs = append(diffs, FieldDiffEntry{
			Field:    field,
			OldValue: fmt.Sprintf("%v", sinceVal),
			NewValue: fmt.Sprintf("%v", currentVal),
			Change:   "changed",
		})
	}

	return diffs
}

// compareParameters compares parameter arrays
func (s *Server) compareParameters(sinceOp, currentOp map[string]interface{}) []FieldDiffEntry {
	diffs := []FieldDiffEntry{}

	sinceParams, sinceHas := sinceOp["parameters"]
	currentParams, currentHas := currentOp["parameters"]

	if !sinceHas && !currentHas {
		return diffs
	}

	sinceParamArray, _ := sinceParams.([]interface{})
	currentParamArray, _ := currentParams.([]interface{})

	// Simplified comparison: count and presence
	if len(sinceParamArray) != len(currentParamArray) {
		diffs = append(diffs, FieldDiffEntry{
			Field:  "parameters",
			Change: "changed",
		})
	}

	return diffs
}

// compareResponses compares response objects
func (s *Server) compareResponses(sinceOp, currentOp map[string]interface{}) []FieldDiffEntry {
	diffs := []FieldDiffEntry{}

	_, sinceHas := sinceOp["responses"]
	_, currentHas := currentOp["responses"]

	if !sinceHas && !currentHas {
		return diffs
	}

	// Basic presence check
	if !sinceHas && currentHas {
		diffs = append(diffs, FieldDiffEntry{
			Field:  "responses",
			Change: "added",
		})
	} else if sinceHas && !currentHas {
		diffs = append(diffs, FieldDiffEntry{
			Field:  "responses",
			Change: "removed",
		})
	}

	return diffs
}

// calculateScopeChanges computes scope changes
func (s *Server) calculateScopeChanges(scopeSince string, identity *Identity) ScopeChange {
	// Placeholder for scope change detection
	// Phase 8.4: scopeChanged: unknown for archived specs
	return ScopeChange{
		Scopes:     []string{},
		ChangeType: "unknown",
	}
}

// buildDiffURL builds the diff URL for a route
func (s *Server) buildDiffURL(path, method string) string {
	return fmt.Sprintf("/changes?level=2&path=%s&method=%s", path, method)
}

// buildDocsURL builds the docs URL for a route
func (s *Server) buildDocsURL(path, method string) string {
	return fmt.Sprintf("/docs/route?path=%s&method=%s&version=_unversioned", path, method)
}

// wasDeprecated checks if an operation was deprecated
func wasDeprecated(operation map[string]interface{}) bool {
	if deprecated, ok := operation["deprecated"].(bool); ok {
		return deprecated
	}
	return false
}

// isDeprecated checks if an operation is currently deprecated
func isDeprecated(operation map[string]interface{}) bool {
	if deprecated, ok := operation["deprecated"].(bool); ok {
		return deprecated
	}
	return false
}

// parametersChanged checks if parameters changed
func parametersChanged(sinceOp, currentOp map[string]interface{}) bool {
	sinceParams, sinceHas := sinceOp["parameters"]
	currentParams, currentHas := currentOp["parameters"]

	// One has params, the other doesn't
	if sinceHas != currentHas {
		return true
	}

	if !sinceHas {
		return false
	}

	sinceParamArray, _ := sinceParams.([]interface{})
	currentParamArray, _ := currentParams.([]interface{})

	return len(sinceParamArray) != len(currentParamArray)
}

// responseChanged checks if responses changed
func responseChanged(sinceOp, currentOp map[string]interface{}) bool {
	sinceResp, sinceHas := sinceOp["responses"]
	currentResp, currentHas := currentOp["responses"]

	// One has responses, the other doesn't
	if sinceHas != currentHas {
		return true
	}

	if !sinceHas {
		return false
	}

	// Simplified check - in production would do deep comparison
	return fmt.Sprintf("%v", sinceResp) != fmt.Sprintf("%v", currentResp)
}
