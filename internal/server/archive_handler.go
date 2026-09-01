package server

import (
	"net/http"
	"strings"

	"github.com/ardenone/seam/internal/spec"
)

// archiveHandler serves archived OpenAPI specs by version hash.
//
// Phase 8.4: This endpoint supports:
//   - ?version=<spec-hash>: Serve a specific archived spec version
//   - ?version=<spec-hash>&scope-since=<version>: Scope filtering with scope change detection
//
// Behavior:
//   - Unknown/evicted versions: Return current spec with 200, not 404
//   - Scope filtering: Applied at serve-time (spec-version alphabet only)
//   - Level-2 out-of-grant: Returns byte-identical 404 (same as current spec)
//   - scopeChanged: unknown for archived specs
//   - Archived specs NEVER served verbatim (always scope-filtered)
//
// This is a reserved path (/openapi.json) and bypasses route-table lookup.
func (s *Server) archiveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}

	// Parse query parameters
	query := r.URL.Query()
	requestedVersion := query.Get("version")
	scopeSince := query.Get("scope-since")

	// Get caller's identity for scope filtering
	identity := identityFromContext(r.Context())
	effectiveScopes := []string{}
	if identity != nil {
		effectiveScopes = identity.Capabilities
	}

	// Determine which spec to serve
	var specJSON []byte
	var specHash string
	var specVersion string
	var sinceKnown bool
	var isCurrent bool

	if requestedVersion == "" {
		// No version specified - serve current spec
		specHash, specVersion, specJSON, isCurrent = s.specRingBuffer.GetCurrentVersion()
		sinceKnown = true
	} else {
		// Version specified - try to retrieve from ring buffer
		specJSON, sinceKnown, _ = s.specRingBuffer.Get(requestedVersion)
		specHash = requestedVersion
		specVersion = specHash[:16] // Truncate to 16 chars

		if !sinceKnown {
			// Unknown/evicted version - fall back to current spec
			specHash, specVersion, specJSON, isCurrent = s.specRingBuffer.GetCurrentVersion()
			sinceKnown = false
		} else {
			isCurrent = false
		}
	}

	// If no spec is available at all, return error
	if len(specJSON) == 0 {
		NewErrorResponse(ErrCodeServiceUnavailable, "No spec available").
			Write(w, r)
		return
	}

	// Apply scope filtering at serve-time (Phase 8.4 requirement)
	// Archived specs are NEVER served verbatim
	filteredJSON, err := s.applyScopeFiltering(specJSON, effectiveScopes)
	if err != nil {
		NewErrorResponse(ErrCodeInternalServer, "Failed to filter spec by scope").
			WithDetail("error", err.Error()).
			Write(w, r)
		return
	}

	// Build response headers
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-SEAM-Scope-Version", s.getCurrentScopeVersion(identity))
	w.Header().Set("X-SEAM-Spec-Version", specVersion)
	w.Header().Set("X-SEAM-Spec-Hash", specHash)

	// Add metadata about whether the requested version was known
	if !sinceKnown {
		w.Header().Set("X-SEAM-Spec-Known", "false")
	} else {
		w.Header().Set("X-SEAM-Spec-Known", "true")
	}

	// Add scope-since information if provided
	if scopeSince != "" {
		w.Header().Set("X-SEAM-Scope-Since", scopeSince)
		w.Header().Set("X-SEAM-Scope-Changed", "unknown") // Phase 8.4: unknown for archived specs
	}

	// If serving current spec, add additional metadata
	if isCurrent {
		w.Header().Set("X-SEAM-Current-Spec", "true")
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(filteredJSON)
}

// applyScopeFiltering applies scope-based filtering to a spec JSON
// This implements Phase 8.4's requirement: "Archived specs NEVER served verbatim"
func (s *Server) applyScopeFiltering(specJSON []byte, effectiveScopes []string) ([]byte, error) {
	// If no identity scopes provided, return empty spec
	if len(effectiveScopes) == 0 {
		return s.buildEmptySpec(), nil
	}

	// Parse the spec into a map
	var specMap map[string]interface{}
	if err := json.Unmarshal(specJSON, &specMap); err != nil {
		return nil, err
	}

	// Get paths from the spec
	paths, ok := specMap["paths"].(map[string]interface{})
	if !ok {
		// No paths, return as-is
		return specJSON, nil
	}

	// Normalize identity scopes for comparison
	normalizedIdentityScopes := make(map[string]bool)
	for _, scope := range effectiveScopes {
		normalized := strings.ToLower(strings.TrimSpace(scope))
		if normalized != "" {
			normalizedIdentityScopes[normalized] = true
		}
	}

	// Filter paths: keep only routes where identity has at least one required scope
	filteredPaths := make(map[string]interface{})
	for path, pathItem := range paths {
		pathItemMap, ok := pathItem.(map[string]interface{})
		if !ok {
			// Keep path items that aren't maps (shouldn't happen, but be safe)
			filteredPaths[path] = pathItem
			continue
		}

		// Check each method in the path item
		filteredPathItem := make(map[string]interface{})
		methodVisible := false

		for httpMethod, methodOp := range pathItemMap {
			// Skip non-method fields (like $ref, summary, etc.)
			if !spec.IsHTTPMethod(httpMethod) {
				filteredPathItem[httpMethod] = methodOp
				continue
			}

			// Get the operation
			methodOpMap, ok := methodOp.(map[string]interface{})
			if !ok {
				filteredPathItem[httpMethod] = methodOp
				continue
			}

			// Check if this operation has x-required-scope
			requiredScopes := spec.ExtractRequiredScopesFromMap(methodOpMap)

			// Determine visibility:
			// - No required scopes: visible (public route)
			// - Has required scopes: visible if identity has at least one matching scope
			visible := false
			if len(requiredScopes) == 0 {
				visible = true // No scope requirement = public route
			} else {
				// Check if identity has any of the required scopes
				for _, requiredScope := range requiredScopes {
					normalizedRequired := strings.ToLower(strings.TrimSpace(requiredScope))
					if normalizedIdentityScopes[normalizedRequired] {
						visible = true
						break
					}
				}
			}

			if visible {
				filteredPathItem[httpMethod] = methodOp
				methodVisible = true
			}
		}

		// Keep the path if at least one method is visible
		if methodVisible {
			filteredPaths[path] = filteredPathItem
		}
	}

	// Build the filtered spec
	specMap["paths"] = filteredPaths
	specMap["x-seam-filtered"] = true
	specMap["x-seam-filter-reason"] = "Scope-based filtering: only routes visible to caller's identity are included"

	// Marshal back to JSON
	filteredJSON, err := json.MarshalIndent(specMap, "", "  ")
	if err != nil {
		return nil, err
	}

	return filteredJSON, nil
}

// buildEmptySpec returns a minimal valid OpenAPI spec with no paths
func (s *Server) buildEmptySpec() []byte {
	baseURL := s.getBaseURL()
	emptySpec := map[string]interface{}{
		"openapi": "3.1.0",
		"info": map[string]interface{}{
			"title":       "SEAM API",
			"version":     "_unversioned",
			"description": "No routes are visible with your current scope",
		},
		"servers": []map[string]interface{}{
			{
				"url":         baseURL,
				"description": "SEAM caller-facing endpoint",
			},
		},
		"paths": map[string]interface{}{},
		"x-seam-filtered": true,
		"x-seam-filter-reason": "Scope-based filtering: no routes visible (no scopes provided)",
	}

	specJSON, _ := json.MarshalIndent(emptySpec, "", "  ")
	return specJSON
}

// getBaseURL returns the configured base URL for the server
func (s *Server) getBaseURL() string {
	if s.specLoader != nil {
		return s.specLoader.GetBaseURL()
	}
	return "https://seam.example.com"
}

