package fanout

import (
	"fmt"
	"net/http"
	"strings"
)

// ScopeFilter checks whether an instance is within the allowed scope for a fan-out request.
type ScopeFilter struct {
	// scopeConfig holds the per-instance scope constraints from x-fanout-scope.
	// Map structure: instanceID -> []allowedScopeIDs
	scopeConfig map[string][]string

	// effectiveScope holds the current request's effective scope IDs.
	effectiveScope []string
}

// ScopeCheck is a function type for checking if an instance is in scope.
// Used by the dispatcher to filter instances before dispatch.
type ScopeCheck func(instanceID string) bool

// NewScopeFilter creates a new scope filter from the x-fanout-scope configuration.
// The config format matches the fragment-root field:
//
//	{
//	  "instance1": ["scope-a", "scope-b"],
//	  "instance2": ["scope-a"],
//	  "_default": ["scope-a"]
//	}
func NewScopeFilter(scopeConfig map[string][]string) *ScopeFilter {
	return &ScopeFilter{
		scopeConfig: scopeConfig,
	}
}

// SetEffectiveScope sets the effective scope IDs for the current request.
// These are typically extracted from the request's scope token or headers.
func (sf *ScopeFilter) SetEffectiveScope(scopeIDs []string) {
	sf.effectiveScope = scopeIDs
}

// IsInScope checks if the given instance ID is within the effective scope.
// Returns true if:
// - The instance has no scope constraints (not in scopeConfig), OR
// - The instance's scope constraints intersect with the effective scope
func (sf *ScopeFilter) IsInScope(instanceID string) bool {
	// No scope constraints for this instance - always in scope
	allowedScopes, exists := sf.scopeConfig[instanceID]
	if !exists {
		// Check for _default fallback
		allowedScopes, exists = sf.scopeConfig["_default"]
		if !exists {
			return true // No constraints
		}
	}

	// No effective scope set - deny (fail closed)
	if len(sf.effectiveScope) == 0 {
		return false
	}

	// Check if any allowed scope intersects with effective scope
	for _, allowed := range allowedScopes {
		for _, effective := range sf.effectiveScope {
			if allowed == effective {
				return true
			}
		}
	}

	return false
}

// Check creates a ScopeCheck function for use with the dispatcher.
func (sf *ScopeFilter) Check() ScopeCheck {
	return func(instanceID string) bool {
		return sf.IsInScope(instanceID)
	}
}

// ParseFanoutScope parses the x-fanout-scope fragment-root field.
// The input is a map[string][]any where each key is an instance ID
// and each value is an array of scope ID strings.
func ParseFanoutScope(raw interface{}) (map[string][]string, error) {
	if raw == nil {
		return nil, nil
	}

	rawMap, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("x-fanout-scope must be an object")
	}

	result := make(map[string][]string)

	for instanceID, scopesAny := range rawMap {
		// Parse scopes array
		scopesArray, ok := scopesAny.([]interface{})
		if !ok {
			return nil, fmt.Errorf("x-fanout-scope entry for instance %q: value must be an array", instanceID)
		}

		scopes := make([]string, 0, len(scopesArray))
		for i, scopeAny := range scopesArray {
			scope, ok := scopeAny.(string)
			if !ok {
				return nil, fmt.Errorf("x-fanout-scope entry for instance %q: scope at index %d must be a string", instanceID, i)
			}
			scope = strings.TrimSpace(scope)
			if scope == "" {
				return nil, fmt.Errorf("x-fanout-scope entry for instance %q: scope at index %d is empty", instanceID, i)
			}
			scopes = append(scopes, scope)
		}

		if len(scopes) == 0 {
			return nil, fmt.Errorf("x-fanout-scope entry for instance %q: scope array cannot be empty", instanceID)
		}

		result[instanceID] = scopes
	}

	return result, nil
}

// ExtractScopeFromRequest extracts effective scope IDs from an HTTP request.
// This looks for scope information in headers or other request context.
//
// Expected scope sources (in order of precedence):
// 1. X-SEAM-Scope header: comma-separated scope IDs
// 2. Authorization header with scope claim (if bearer token)
// 3. Request context variables (if set by middleware)
func ExtractScopeFromRequest(r *http.Request) []string {
	if r == nil {
		return nil
	}

	// Check X-SEAM-Scope header first
	if scopeHeader := r.Header.Get("X-SEAM-Scope"); scopeHeader != "" {
		parts := strings.Split(scopeHeader, ",")
		scopes := make([]string, 0, len(parts))
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				scopes = append(scopes, trimmed)
			}
		}
		if len(scopes) > 0 {
			return scopes
		}
	}

	// TODO: Extract from bearer token scope claim if present
	// This would require JWT parsing and is implementation-specific

	// TODO: Extract from request context if set by auth middleware
	// This would be: ctx.Value("effective_scopes")

	return nil
}

// ScopeWithheldReason returns the error message for a scope-withheld instance.
func ScopeWithheldReason(instanceID string, requiredScopes []string) string {
	if len(requiredScopes) == 0 {
		return fmt.Sprintf("Instance %q is not in the effective scope", instanceID)
	}
	return fmt.Sprintf("Instance %q requires one of scopes: %v (not in effective scope)", instanceID, requiredScopes)
}

// ValidateScopeConfig validates the x-fanout-scope configuration at fragment load time.
// This ensures the scope constraints are well-formed before runtime use.
func ValidateScopeConfig(scopeConfig map[string][]string) error {
	for instanceID, scopes := range scopeConfig {
		if instanceID == "" {
			return fmt.Errorf("x-fanout-scope: instance ID cannot be empty")
		}

		if len(scopes) == 0 {
			return fmt.Errorf("x-fanout-scope: instance %q has empty scope array", instanceID)
		}

		seen := make(map[string]bool)
		for _, scope := range scopes {
			if scope == "" {
				return fmt.Errorf("x-fanout-scope: instance %q has empty scope ID", instanceID)
			}
			if seen[scope] {
				return fmt.Errorf("x-fanout-scope: instance %q has duplicate scope %q", instanceID, scope)
			}
			seen[scope] = true
		}
	}

	return nil
}
