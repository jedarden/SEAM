package fanout

import (
	"net/http/httptest"
	"testing"
)

func TestNewScopeFilter(t *testing.T) {
	scopeConfig := map[string][]string{
		"instance-1": {"scope-a", "scope-b"},
		"instance-2": {"scope-a"},
		"_default":   {"scope-c"},
	}

	filter := NewScopeFilter(scopeConfig)

	if filter == nil {
		t.Fatal("NewScopeFilter should return non-nil filter")
	}
	if filter.scopeConfig == nil {
		t.Error("scopeConfig should be initialized")
	}
}

func TestSetEffectiveScope(t *testing.T) {
	filter := NewScopeFilter(map[string][]string{})
	scopes := []string{"scope-a", "scope-b"}

	filter.SetEffectiveScope(scopes)

	if len(filter.effectiveScope) != len(scopes) {
		t.Errorf("Expected %d effective scopes, got %d", len(scopes), len(filter.effectiveScope))
	}
}

func TestIsInScope_NoConstraints(t *testing.T) {
	filter := NewScopeFilter(map[string][]string{})
	filter.SetEffectiveScope([]string{"scope-a"})

	// Instance with no constraints should always be in scope
	if !filter.IsInScope("any-instance") {
		t.Error("Instance with no constraints should be in scope")
	}
}

func TestIsInScope_NoEffectiveScope(t *testing.T) {
	scopeConfig := map[string][]string{
		"instance-1": {"scope-a"},
	}
	filter := NewScopeFilter(scopeConfig)
	// Don't set effective scope

	// Should fail closed when no effective scope is set
	if filter.IsInScope("instance-1") {
		t.Error("Instance should not be in scope when no effective scope is set")
	}
}

func TestIsInScope_MatchingScope(t *testing.T) {
	scopeConfig := map[string][]string{
		"instance-1": {"scope-a", "scope-b"},
	}
	filter := NewScopeFilter(scopeConfig)
	filter.SetEffectiveScope([]string{"scope-a"})

	if !filter.IsInScope("instance-1") {
		t.Error("instance-1 should be in scope with scope-a")
	}
}

func TestIsInScope_NonMatchingScope(t *testing.T) {
	scopeConfig := map[string][]string{
		"instance-1": {"scope-a", "scope-b"},
	}
	filter := NewScopeFilter(scopeConfig)
	filter.SetEffectiveScope([]string{"scope-c"})

	if filter.IsInScope("instance-1") {
		t.Error("instance-1 should not be in scope with scope-c only")
	}
}

func TestIsInScope_DefaultFallback(t *testing.T) {
	scopeConfig := map[string][]string{
		"_default": {"scope-a"},
	}
	filter := NewScopeFilter(scopeConfig)
	filter.SetEffectiveScope([]string{"scope-a"})

	// Unknown instance should fall back to _default
	if !filter.IsInScope("unknown-instance") {
		t.Error("Unknown instance should use _default and be in scope")
	}

	// But fail if effective scope doesn't match _default
	filter.SetEffectiveScope([]string{"scope-b"})
	if filter.IsInScope("unknown-instance") {
		t.Error("Unknown instance should not be in scope with mismatched scope")
	}
}

func TestIsInScope_PartialMatch(t *testing.T) {
	scopeConfig := map[string][]string{
		"instance-1": {"scope-a", "scope-b", "scope-c"},
	}
	filter := NewScopeFilter(scopeConfig)
	filter.SetEffectiveScope([]string{"scope-b"})

	// Should match if any allowed scope intersects
	if !filter.IsInScope("instance-1") {
		t.Error("Should be in scope with partial match (scope-b)")
	}
}

func TestCheck(t *testing.T) {
	scopeConfig := map[string][]string{
		"instance-1": {"scope-a"},
		"_default":   {"scope-b"}, // Add default scope
	}
	filter := NewScopeFilter(scopeConfig)
	filter.SetEffectiveScope([]string{"scope-a"})

	checkFunc := filter.Check()

	if checkFunc == nil {
		t.Fatal("Check() should return non-nil function")
	}

	if !checkFunc("instance-1") {
		t.Error("Check function should return true for instance-1")
	}

	// Unknown instance falls back to _default, which requires scope-b
	// Since we only have scope-a, this should return false
	if checkFunc("unknown-instance") {
		t.Error("Check function should return false for unknown instance (no matching default scope)")
	}
}

func TestParseFanoutScope_Nil(t *testing.T) {
	result, err := ParseFanoutScope(nil)

	if err != nil {
		t.Errorf("ParseFanoutScope(nil) should return nil, nil, got error: %v", err)
	}
	if result != nil {
		t.Error("ParseFanoutScope(nil) should return nil result")
	}
}

func TestParseFanoutScope_Valid(t *testing.T) {
	raw := map[string]interface{}{
		"instance-1": []interface{}{"scope-a", "scope-b"},
		"instance-2": []interface{}{"scope-c"},
	}

	result, err := ParseFanoutScope(raw)

	if err != nil {
		t.Fatalf("ParseFanoutScope returned error: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("Expected 2 instances, got %d", len(result))
	}

	scopes1, ok := result["instance-1"]
	if !ok || len(scopes1) != 2 {
		t.Error("instance-1 should have 2 scopes")
	}

	scopes2, ok := result["instance-2"]
	if !ok || len(scopes2) != 1 {
		t.Error("instance-2 should have 1 scope")
	}
}

func TestParseFanoutScope_InvalidType(t *testing.T) {
	raw := "not an object"

	_, err := ParseFanoutScope(raw)

	if err == nil {
		t.Error("ParseFanoutScope should return error for non-object type")
	}
}

func TestParseFanoutScope_ScopeArrayInvalidType(t *testing.T) {
	raw := map[string]interface{}{
		"instance-1": "not an array",
	}

	_, err := ParseFanoutScope(raw)

	if err == nil {
		t.Error("ParseFanoutScope should return error when scope value is not an array")
	}
}

func TestParseFanoutScope_ScopeNotString(t *testing.T) {
	raw := map[string]interface{}{
		"instance-1": []interface{}{"scope-a", 123},
	}

	_, err := ParseFanoutScope(raw)

	if err == nil {
		t.Error("ParseFanoutScope should return error when scope array contains non-string")
	}
}

func TestParseFanoutScope_EmptyScope(t *testing.T) {
	raw := map[string]interface{}{
		"instance-1": []interface{}{"scope-a", ""},
	}

	_, err := ParseFanoutScope(raw)

	if err == nil {
		t.Error("ParseFanoutScope should return error for empty scope string")
	}
}

func TestParseFanoutScope_EmptyScopeArray(t *testing.T) {
	raw := map[string]interface{}{
		"instance-1": []interface{}{},
	}

	_, err := ParseFanoutScope(raw)

	if err == nil {
		t.Error("ParseFanoutScope should return error for empty scope array")
	}
}

func TestParseFanoutScope_TrimWhitespace(t *testing.T) {
	raw := map[string]interface{}{
		"instance-1": []interface{}{" scope-a ", " scope-b "},
	}

	result, err := ParseFanoutScope(raw)

	if err != nil {
		t.Fatalf("ParseFanoutScope returned error: %v", err)
	}

	scopes := result["instance-1"]
	if len(scopes) != 2 {
		t.Fatalf("Expected 2 scopes, got %d", len(scopes))
	}

	if scopes[0] != "scope-a" || scopes[1] != "scope-b" {
		t.Errorf("Scopes should be trimmed, got %q and %q", scopes[0], scopes[1])
	}
}

func TestScopeWithheldReason(t *testing.T) {
	reason := ScopeWithheldReason("instance-1", []string{"scope-a", "scope-b"})

	// The actual implementation returns "Instance \"instance-1\"..."
	expected := "Instance \"instance-1\" requires one of scopes: [scope-a scope-b] (not in effective scope)"
	if reason != expected {
		t.Errorf("ScopeWithheldReason = %q, want %q", reason, expected)
	}
}

func TestScopeWithheldReason_NoScopes(t *testing.T) {
	reason := ScopeWithheldReason("instance-1", []string{})

	// The actual implementation returns "Instance \"instance-1\"..."
	expected := "Instance \"instance-1\" is not in the effective scope"
	if reason != expected {
		t.Errorf("ScopeWithheldReason = %q, want %q", reason, expected)
	}
}

func TestValidateScopeConfig_Valid(t *testing.T) {
	scopeConfig := map[string][]string{
		"instance-1": {"scope-a", "scope-b"},
		"instance-2": {"scope-c"},
	}

	err := ValidateScopeConfig(scopeConfig)

	if err != nil {
		t.Errorf("ValidateScopeConfig should return nil for valid config, got: %v", err)
	}
}

func TestValidateScopeConfig_EmptyInstanceID(t *testing.T) {
	scopeConfig := map[string][]string{
		"": {"scope-a"},
	}

	err := ValidateScopeConfig(scopeConfig)

	if err == nil {
		t.Error("ValidateScopeConfig should return error for empty instance ID")
	}
}

func TestValidateScopeConfig_EmptyScopeArray(t *testing.T) {
	scopeConfig := map[string][]string{
		"instance-1": {},
	}

	err := ValidateScopeConfig(scopeConfig)

	if err == nil {
		t.Error("ValidateScopeConfig should return error for empty scope array")
	}
}

func TestValidateScopeConfig_EmptyScopeID(t *testing.T) {
	scopeConfig := map[string][]string{
		"instance-1": {"scope-a", ""},
	}

	err := ValidateScopeConfig(scopeConfig)

	if err == nil {
		t.Error("ValidateScopeConfig should return error for empty scope ID")
	}
}

func TestValidateScopeConfig_DuplicateScope(t *testing.T) {
	scopeConfig := map[string][]string{
		"instance-1": {"scope-a", "scope-a"},
	}

	err := ValidateScopeConfig(scopeConfig)

	if err == nil {
		t.Error("ValidateScopeConfig should return error for duplicate scope IDs")
	}
}

func TestExtractScopeFromRequest_NilRequest(t *testing.T) {
	scopes := ExtractScopeFromRequest(nil)

	if scopes != nil {
		t.Error("ExtractScopeFromRequest(nil) should return nil")
	}
}

func TestExtractScopeFromRequest_XSEAMScopeHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-SEAM-Scope", "scope-a,scope-b,scope-c")

	scopes := ExtractScopeFromRequest(req)

	if scopes == nil {
		t.Fatal("ExtractScopeFromRequest should return non-nil scopes")
	}

	if len(scopes) != 3 {
		t.Errorf("Expected 3 scopes, got %d", len(scopes))
	}

	expectedScopes := []string{"scope-a", "scope-b", "scope-c"}
	for i, expected := range expectedScopes {
		if scopes[i] != expected {
			t.Errorf("Scope %d: expected %q, got %q", i, expected, scopes[i])
		}
	}
}

func TestExtractScopeFromRequest_XSEAMScopeHeaderWithSpaces(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-SEAM-Scope", " scope-a , scope-b , scope-c ")

	scopes := ExtractScopeFromRequest(req)

	if len(scopes) != 3 {
		t.Errorf("Expected 3 scopes (whitespace trimmed), got %d", len(scopes))
	}
}

func TestExtractScopeFromRequest_EmptyScopeHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-SEAM-Scope", "")

	scopes := ExtractScopeFromRequest(req)

	if scopes != nil {
		t.Error("Empty X-SEAM-Scope header should return nil")
	}
}

func TestExtractScopeFromRequest_NoScopeHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)

	scopes := ExtractScopeFromRequest(req)

	if scopes != nil {
		t.Error("Missing X-SEAM-Scope header should return nil")
	}
}
