package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ardenone/seam/internal/tailscale"
)

// BuiltinControlPlaneScopes is the compiled-in set of control-plane scopes.
// These are always available for control-plane access.
var BuiltinControlPlaneScopes = []string{
	"seam:read",
	"seam:config:read",
	"seam:config:write",
	"seam:scopes:read",
	"seam:scopes:read-all",
	"seam:metrics:read",
	"seam:health:read",
	"seam:capture:read",
	"seam:capture:write",
	"seam:cache:read",
	"seam:cache:write",
	"seam:openapi:read",
	"seam:docs:read",
	"seam:tailscale:key-create", // Phase 7: Tailscale ephemeral key creation
}

// whoamiHandler returns the resolved caller identity, tags, effective scopes,
// and current X-SEAM-Scope-Version.
//
// Phase 7 Stage 5-6: This endpoint exposes the identity resolution results
// from Stage 3 of the request pipeline.
//
// Response structure:
//   - identity: The resolved identity (node, user, tags)
//   - effective_scopes: The scope claims from the caller's identity
//   - scope_version: The current X-SEAM-Scope-Version hash
//   - resolved: Whether identity resolution succeeded
//
// This is a reserved path (/whoami) and bypasses route-table lookup.
func (s *Server) whoamiHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}

	// Get resolved identity from context (set by Stage 3 middleware)
	identity := identityFromContext(r.Context())
	if identity == nil {
		// This should not happen if Stage 3 ran, but handle gracefully
		identity = &Identity{
			NodeName:     "unknown",
			Resolved:     false,
			NodeKey:      "anonymous",
			Tags:         []string{},
			Capabilities: []string{},
		}
	}

	// Extract effective scopes from identity capabilities
	effectiveScopes := identity.Capabilities
	if len(effectiveScopes) == 0 {
		effectiveScopes = []string{}
	}

	// Record scope version in cache and get version hash
	var scopeVersion string
	if s.scopeVersionCache != nil {
		scopeVersion = s.scopeVersionCache.RecordScopeVersion(identity, effectiveScopes)
	} else {
		scopeVersion = ComputeScopeVersionHash(effectiveScopes)
	}

	// Build response
	response := map[string]interface{}{
		"identity": map[string]interface{}{
			"node_key":  identity.NodeKey,
			"node_name": identity.NodeName,
			"user":      identity.User,
			"tags":      identity.Tags,
		},
		"effective_scopes": effectiveScopes,
		"scope_version":    scopeVersion,
		"resolved":         identity.Resolved,
	}

	// Set headers and return response
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-SEAM-Scope-Version", scopeVersion)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

// scopesHandler returns the derived scope map from TWO merged sources:
//
// 1. The merged spec's x-required-scope declarations (inverted to scope->routes)
// 2. The builtin control-plane set compiled into the binary (labeled "source: builtin")
//
// Phase 7 Stage 5-6:
//   - Default: scope-filtered by the caller's effective scopes
//   - ?all=1: requires seam:scopes:read-all scope, returns all scopes
//
// Response structure:
//   - scopes: Map of scope ID -> { routes: [...], source: "spec"|"builtin" }
//   - filtered: Whether the response is scope-filtered
//   - total_scopes: Total number of scopes in the system
//
// This is a reserved path (/scopes) and bypasses route-table lookup.
func (s *Server) scopesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}

	// Parse query parameters
	query := r.URL.Query()
	showAll := query.Get("all") == "1"

	// Get caller's identity and effective scopes
	identity := identityFromContext(r.Context())
	effectiveScopes := []string{}
	if identity != nil {
		effectiveScopes = identity.Capabilities
	}

	// Check seam:scopes:read-all permission for ?all=1
	if showAll && !hasScope(effectiveScopes, "seam:scopes:read-all") {
		NewErrorResponse(ErrCodeForbidden, "The seam:scopes:read-all scope is required to list all scopes").
			WithDetail("required_scope", "seam:scopes:read-all").
			Write(w, r)
		return
	}

	// Build scope map from TWO sources
	scopeMap := s.buildScopeMap()

	// Filter scopes if not showing all
	filteredScopes := scopeMap
	if !showAll {
		filteredScopes = filterScopesByEffective(scopeMap, effectiveScopes)
	}

	// Build response
	response := map[string]interface{}{
		"scopes":          filteredScopes,
		"filtered":        !showAll,
		"total_scopes":    len(scopeMap),
		"returned":        len(filteredScopes),
		"effective_count": len(effectiveScopes),
	}

	// Set headers and return response
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-SEAM-Scope-Version", s.getCurrentScopeVersion(identity))
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

// buildScopeMap builds the complete scope map from TWO sources:
// 1. Spec-derived scopes (from x-required-scope declarations)
// 2. Builtin control-plane scopes
func (s *Server) buildScopeMap() map[string]scopeInfo {
	scopeMap := make(map[string]scopeInfo)

	// Source 1: Extract scopes from route table (x-required-scope)
	if s.routeTableHolder != nil {
		routes := s.routeTableHolder.Snapshot()
		for _, route := range routes {
			if len(route.RequiredScopes) > 0 {
				for _, scope := range route.RequiredScopes {
					normalizedScope := normalizeScopeKey(scope)
					info, exists := scopeMap[normalizedScope]
					if !exists {
						info = scopeInfo{
							Routes: []string{},
							Source: "spec",
						}
					}
					// Add route if not already present
					routeKey := fmt.Sprintf("%s %s", route.Method, route.PathTemplate)
					routeExists := false
					for _, r := range info.Routes {
						if r == routeKey {
							routeExists = true
							break
						}
					}
					if !routeExists {
						info.Routes = append(info.Routes, routeKey)
					}
					scopeMap[normalizedScope] = info
				}
			}
		}
	}

	// Source 2: Add builtin control-plane scopes
	for _, builtinScope := range BuiltinControlPlaneScopes {
		normalizedScope := normalizeScopeKey(builtinScope)
		if _, exists := scopeMap[normalizedScope]; !exists {
			scopeMap[normalizedScope] = scopeInfo{
				Routes: []string{"<control-plane>"},
				Source: "builtin",
			}
		}
	}

	return scopeMap
}

// scopeInfo represents information about a scope
type scopeInfo struct {
	Routes []string `json:"routes"` // Routes that require this scope
	Source string   `json:"source"` // "spec" or "builtin"
}

// filterScopesByEffective filters the scope map to only include scopes
// that intersect with the caller's effective scopes
func filterScopesByEffective(scopeMap map[string]scopeInfo, effectiveScopes []string) map[string]scopeInfo {
	if len(effectiveScopes) == 0 {
		// No effective scopes - return empty map
		return make(map[string]scopeInfo)
	}

	// Build set of effective scopes for fast lookup
	effectiveSet := make(map[string]bool)
	for _, scope := range effectiveScopes {
		effectiveSet[normalizeScopeKey(scope)] = true
	}

	// Filter scope map
	filtered := make(map[string]scopeInfo)
	for scope, info := range scopeMap {
		if effectiveSet[scope] {
			filtered[scope] = info
		}
	}

	return filtered
}

// hasScope checks if the effective scopes include a specific scope
func hasScope(effectiveScopes []string, scope string) bool {
	normalizedTarget := normalizeScopeKey(scope)
	for _, s := range effectiveScopes {
		if normalizeScopeKey(s) == normalizedTarget {
			return true
		}
	}
	return false
}

// normalizeScopeKey normalizes a scope string for map keys
func normalizeScopeKey(scope string) string {
	return strings.ToLower(strings.TrimSpace(scope))
}

// getCurrentScopeVersion returns the current scope version for an identity
func (s *Server) getCurrentScopeVersion(identity *Identity) string {
	if identity == nil {
		return ComputeScopeVersionHash([]string{})
	}

	effectiveScopes := identity.Capabilities
	if len(effectiveScopes) == 0 {
		effectiveScopes = []string{}
	}

	if s.scopeVersionCache != nil {
		return s.scopeVersionCache.GetCurrentScopeVersion(identity)
	}

	return ComputeScopeVersionHash(effectiveScopes)
}

// effectiveScopesFromIdentity extracts the effective scope set from an identity
// This combines the identity's capabilities with any system-default scopes
func effectiveScopesFromIdentity(identity *Identity) []string {
	if identity == nil || !identity.Resolved {
		// Unresolved identity has no scopes
		return []string{}
	}

	// Return capabilities as effective scopes
	scopes := make([]string, 0, len(identity.Capabilities))
	for _, scope := range identity.Capabilities {
		normalized := strings.TrimSpace(scope)
		if normalized != "" {
			scopes = append(scopes, normalized)
		}
	}

	// Sort for canonical representation
	sort.Strings(scopes)
	return scopes
}

// tailscaleEphemeralKeyHandler creates a Tailscale ephemeral key for a NEEDLE worker.
//
// POST /api/v1/tailscale/ephemeral-key
//
// This endpoint allows NEEDLE workers to obtain ephemeral Tailscale auth keys.
// The request must include a worker_id in the JSON body.
//
// Phase 7 Integration:
// - This endpoint is a reserved path (bypasses route-table lookup)
// - Requires seam:tailscale:key-create scope
// - Uses the Tailscale API client to create keys with caching
//
// Request JSON:
//
//	{
//	  "worker_id": "needle-worker-name"
//	}
//
// Response JSON:
//
//	{
//	  "key": "tskey-...",
//	  "id": "key-id",
//	  "expires": "2026-11-26T12:34:56Z",
//	  "description": "NEEDLE worker: needle-worker-name"
//	}
func (s *Server) tailscaleEphemeralKeyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed("Only POST method is allowed").Write(w, r)
		return
	}

	// Get caller's identity and effective scopes
	identity := identityFromContext(r.Context())
	effectiveScopes := []string{}
	if identity != nil {
		effectiveScopes = identity.Capabilities
	}

	// Check seam:tailscale:key-create scope
	if !hasScope(effectiveScopes, "seam:tailscale:key-create") {
		NewErrorResponse(ErrCodeForbidden, "The seam:tailscale:key-create scope is required to create ephemeral keys").
			WithDetail("required_scope", "seam:tailscale:key-create").
			Write(w, r)
		return
	}

	// Parse request body
	var req struct {
		WorkerID string `json:"worker_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		NewErrorResponse(ErrCodeBadRequest, "Invalid request body").
			WithDetail("error", err.Error()).
			Write(w, r)
		return
	}

	// Validate worker_id
	if req.WorkerID == "" {
		NewErrorResponse(ErrCodeBadRequest, "worker_id is required").
			WithDetail("field", "worker_id").
			Write(w, r)
		return
	}

	// Check if Tailscale client is initialized
	if s.tailscaleClient == nil {
		NewErrorResponse(ErrCodeServiceUnavailable, "Tailscale client not initialized").
			WithDetail("reason", "Tailscale API client is not configured").
			Write(w, r)
		return
	}

	// Create ephemeral key
	key, err := s.tailscaleClient.CreateEphemeralKey(r.Context(), req.WorkerID)
	if err != nil {
		// Handle specific error types
		if err == tailscale.ErrCacheHoldDown {
			NewErrorResponse(ErrCodeServiceUnavailable, "Tailscale API is in hold-down period after previous failure").
				WithDetail("retry_after", "30s").
				Write(w, r)
			return
		}
		if err == tailscale.ErrNoAPIKey || err == tailscale.ErrNoTailnet {
			NewErrorResponse(ErrCodeInternalServer, "Tailscale client misconfigured").
				WithDetail("error", err.Error()).
				Write(w, r)
			return
		}
		NewErrorResponse(ErrCodeInternalServer, "Failed to create ephemeral key").
			WithDetail("error", err.Error()).
			Write(w, r)
		return
	}

	// Build response
	response := map[string]interface{}{
		"key":         key.Key,
		"id":          key.ID,
		"expires":     key.Expires.Format(time.RFC3339),
		"description": key.Description,
	}

	// Set headers and return response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}
