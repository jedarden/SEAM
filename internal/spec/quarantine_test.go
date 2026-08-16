package spec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFragmentQuarantineSchemaValidation tests that fragments failing schema validation are quarantined
func TestFragmentQuarantineSchemaValidation(t *testing.T) {
	// Create a temporary directory for test fragments
	tmpDir := t.TempDir()
	fragmentsDir := filepath.Join(tmpDir, "fragments")
	schemaDir := t.TempDir()

	// Create a schema that requires certain fields
	schema := `{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type": "object",
		"required": ["openapi", "info", "paths"],
		"properties": {
			"openapi": {"type": "string"},
			"info": {"type": "object"},
			"paths": {"type": "object"}
		}
	}`
	schemaPath := filepath.Join(schemaDir, "schema.json")
	if err := os.WriteFile(schemaPath, []byte(schema), 0644); err != nil {
		t.Fatalf("Failed to write schema: %v", err)
	}

	// Create a valid fragment
	validFragment := `{
		"openapi": "3.1.0",
		"info": {"title": "Valid", "version": "1.0.0"},
		"paths": {"/test": {}}
	}`
	validPath := filepath.Join(fragmentsDir, "valid.yaml")
	if err := os.MkdirAll(filepath.Dir(validPath), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	if err := os.WriteFile(validPath, []byte(validFragment), 0644); err != nil {
		t.Fatalf("Failed to write valid fragment: %v", err)
	}

	// Create an invalid fragment (missing required "paths" field)
	invalidFragment := `{
		"openapi": "3.1.0",
		"info": {"title": "Invalid"}
	}`
	invalidPath := filepath.Join(fragmentsDir, "invalid.yaml")
	if err := os.WriteFile(invalidPath, []byte(invalidFragment), 0644); err != nil {
		t.Fatalf("Failed to write invalid fragment: %v", err)
	}

	// Load and validate fragments
	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatalf("Failed to create fragment loader: %v", err)
	}

	if err := loader.LoadDirectory(fragmentsDir); err != nil {
		t.Fatalf("Failed to load fragments: %v", err)
	}

	if err := loader.ValidateFragments(schemaPath); err != nil {
		t.Fatalf("ValidateFragments failed: %v", err)
	}

	// Verify results
	validCount := loader.GetValidFragmentCount()
	quarantinedCount := loader.GetQuarantinedCount()

	if validCount != 1 {
		t.Errorf("Expected 1 valid fragment, got %d", validCount)
	}
	if quarantinedCount != 1 {
		t.Errorf("Expected 1 quarantined fragment, got %d", quarantinedCount)
	}

	// Verify the invalid fragment was quarantined
	quarantined := loader.GetQuarantined()
	if len(quarantined) != 1 {
		t.Fatalf("Expected 1 quarantined fragment, got %d", len(quarantined))
	}

	if quarantined[0].SourceFile != invalidPath {
		t.Errorf("Expected quarantined fragment to be %s, got %s", invalidPath, quarantined[0].SourceFile)
	}

	if !quarantined[0].QueuedForQuarantine {
		t.Error("Expected QueuedForQuarantine to be true")
	}

	if len(quarantined[0].QuarantineReasons) == 0 {
		t.Error("Expected quarantine reasons to be set")
	}
}

// TestFragmentQuarantinePathCollisions tests that exact route/version collisions
// quarantine only the later filename and retain the incumbent.
func TestFragmentQuarantinePathCollisions(t *testing.T) {
	tmpDir := t.TempDir()
	fragmentsDir := filepath.Join(tmpDir, "fragments")

	// Create two fragments with the same (path, method, x-api-version) key.
	fragment1 := `{
		"openapi": "3.1.0",
		"info": {"title": "Fragment 1"},
		"paths": {"/users": {"get": {"summary": "Get users"}}}
	}`
	path1 := filepath.Join(fragmentsDir, "service1", "fragment1.yaml")
	if err := os.MkdirAll(filepath.Dir(path1), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	if err := os.WriteFile(path1, []byte(fragment1), 0644); err != nil {
		t.Fatalf("Failed to write fragment1: %v", err)
	}

	fragment2 := `{
		"openapi": "3.1.0",
		"info": {"title": "Fragment 2"},
		"paths": {"/users": {"get": {"summary": "Replacement users"}}}
	}`
	path2 := filepath.Join(fragmentsDir, "service2", "fragment2.yaml")
	if err := os.MkdirAll(filepath.Dir(path2), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	if err := os.WriteFile(path2, []byte(fragment2), 0644); err != nil {
		t.Fatalf("Failed to write fragment2: %v", err)
	}

	// Load fragments
	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatalf("Failed to create fragment loader: %v", err)
	}

	if err := loader.LoadDirectory(fragmentsDir); err != nil {
		t.Fatalf("Failed to load fragments: %v", err)
	}

	// Detect path collisions (should happen before merge)
	loader.DetectPathCollisions()

	// Verify only the later filename was quarantined; the incumbent stays live.
	validCount := loader.GetValidFragmentCount()
	quarantinedCount := loader.GetQuarantinedCount()

	if validCount != 1 {
		t.Errorf("Expected 1 valid incumbent fragment, got %d", validCount)
	}
	if quarantinedCount != 1 {
		t.Errorf("Expected 1 quarantined later fragment, got %d", quarantinedCount)
	}

	quarantined := loader.GetQuarantined()
	if len(quarantined) != 1 || quarantined[0].SourceFile != path2 {
		t.Fatalf("Expected later fragment %s to be quarantined, got %v", path2, quarantined)
	}
	for _, frag := range quarantined {
		if !frag.QueuedForQuarantine {
			t.Errorf("Fragment %s should be quarantined", frag.SourceFile)
		}
		hasCollisionReason := false
		for _, reason := range frag.QuarantineReasons {
			if contains(reason, "path_collision") {
				hasCollisionReason = true
				break
			}
		}
		if !hasCollisionReason {
			t.Errorf("Fragment %s should have path_collision reason, got %v", frag.SourceFile, frag.QuarantineReasons)
		}
	}

	status := loader.GetFragmentStatus()
	fragmentStatuses, ok := status["fragments"].([]FragmentStatus)
	if !ok {
		t.Fatalf("Expected status fragments to be []FragmentStatus, got %T", status["fragments"])
	}
	var incumbentStatus, quarantinedStatus *FragmentStatus
	for i := range fragmentStatuses {
		switch fragmentStatuses[i].SourceFile {
		case path1:
			incumbentStatus = &fragmentStatuses[i]
		case path2:
			quarantinedStatus = &fragmentStatuses[i]
		}
	}
	if incumbentStatus == nil || incumbentStatus.Status != "valid" {
		t.Errorf("Expected incumbent status to be valid, got %+v", incumbentStatus)
	}
	if quarantinedStatus == nil || quarantinedStatus.Status != "quarantined" || len(quarantinedStatus.QuarantineReasons) == 0 {
		t.Errorf("Expected collision status for later fragment, got %+v", quarantinedStatus)
	}
}

// TestFragmentCollisionKeyAllowsMethodAndVersionCoexistence verifies that
// different methods and API versions do not collide on the same path.
func TestFragmentCollisionKeyAllowsMethodAndVersionCoexistence(t *testing.T) {
	fragmentsDir := filepath.Join(t.TempDir(), "fragments")
	fragments := []struct {
		name    string
		version string
		method  string
	}{
		{name: "a/unversioned.json", method: "get"},
		{name: "b/v1-get.json", version: "v1", method: "get"},
		{name: "c/v1-post.json", version: "v1", method: "post"},
		{name: "d/v1-get-duplicate.json", version: "v1", method: "get"},
	}

	for _, fragment := range fragments {
		path := filepath.Join(fragmentsDir, fragment.name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("Failed to create directory for %s: %v", path, err)
		}
		version := ""
		if fragment.version != "" {
			version = fmt.Sprintf(",\n\t\t\"x-api-version\": %q", fragment.version)
		}
		content := fmt.Sprintf(`{
			"openapi": "3.1.0",
			"info": {"title": %q},
			"paths": {"/users": {%q: {"summary": %q}}}%s
		}`, fragment.name, fragment.method, fragment.name, version)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write %s: %v", path, err)
		}
	}

	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatalf("Failed to create fragment loader: %v", err)
	}
	if err := loader.LoadDirectory(fragmentsDir); err != nil {
		t.Fatalf("Failed to load fragments: %v", err)
	}
	loader.DetectPathCollisions()

	if got := loader.GetValidFragmentCount(); got != 3 {
		t.Errorf("Expected unversioned, v1 GET, and v1 POST fragments to remain valid; got %d", got)
	}
	if got := loader.GetQuarantinedCount(); got != 1 {
		t.Errorf("Expected only the later v1 GET fragment to be quarantined; got %d", got)
	}
	if got := loader.GetQuarantined()[0].SourceFile; !strings.HasSuffix(got, "d/v1-get-duplicate.json") {
		t.Errorf("Expected deterministic later filename to lose, got %s", got)
	}
}

// TestQuarantinedFragmentsNotInMergedSpec tests that quarantined fragments don't appear in merged spec
func TestQuarantinedFragmentsNotInMergedSpec(t *testing.T) {
	tmpDir := t.TempDir()
	fragmentsDir := filepath.Join(tmpDir, "fragments")
	schemaDir := t.TempDir()

	// Create schema
	schema := `{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type": "object",
		"required": ["openapi", "info", "paths"],
		"properties": {
			"openapi": {"type": "string"},
			"info": {"type": "object"},
			"paths": {"type": "object"}
		}
	}`
	schemaPath := filepath.Join(schemaDir, "schema.json")
	if err := os.WriteFile(schemaPath, []byte(schema), 0644); err != nil {
		t.Fatalf("Failed to write schema: %v", err)
	}

	// Create valid fragment
	validFragment := `{
		"openapi": "3.1.0",
		"info": {"title": "Valid"},
		"paths": {"/valid": {"get": {"summary": "Valid endpoint"}}}
	}`
	validPath := filepath.Join(fragmentsDir, "valid.yaml")
	if err := os.MkdirAll(filepath.Dir(validPath), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	if err := os.WriteFile(validPath, []byte(validFragment), 0644); err != nil {
		t.Fatalf("Failed to write valid fragment: %v", err)
	}

	// Create invalid fragment (missing paths)
	invalidFragment := `{
		"openapi": "3.1.0",
		"info": {"title": "Invalid"}
	}`
	invalidPath := filepath.Join(fragmentsDir, "invalid.yaml")
	if err := os.WriteFile(invalidPath, []byte(invalidFragment), 0644); err != nil {
		t.Fatalf("Failed to write invalid fragment: %v", err)
	}

	// Load and validate
	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatalf("Failed to create fragment loader: %v", err)
	}

	if err := loader.LoadDirectory(fragmentsDir); err != nil {
		t.Fatalf("Failed to load fragments: %v", err)
	}

	if err := loader.ValidateFragments(schemaPath); err != nil {
		t.Fatalf("ValidateFragments failed: %v", err)
	}

	// Merge fragments
	mergedJSON, err := loader.MergeFragments("http://localhost:8080")
	if err != nil {
		t.Fatalf("Failed to merge fragments: %v", err)
	}

	// Parse merged spec
	var mergedSpec map[string]interface{}
	if err := json.Unmarshal(mergedJSON, &mergedSpec); err != nil {
		t.Fatalf("Failed to parse merged spec: %v", err)
	}

	// Verify only valid fragment's paths are in the merged spec
	paths, ok := mergedSpec["paths"].(map[string]interface{})
	if !ok {
		t.Fatal("Merged spec missing paths")
	}

	// Should only have /valid path, not any paths from invalid fragment
	if len(paths) != 1 {
		t.Errorf("Expected 1 path in merged spec, got %d", len(paths))
	}

	if _, exists := paths["/valid"]; !exists {
		t.Error("Expected /valid path to be in merged spec")
	}

	// Verify invalid fragment was quarantined
	quarantined := loader.GetQuarantined()
	hasInvalid := false
	for _, frag := range quarantined {
		if frag.SourceFile == invalidPath {
			hasInvalid = true
			break
		}
	}
	if !hasInvalid {
		t.Error("Invalid fragment should be quarantined")
	}
}

// TestServerContinuesWithAllQuarantinedFragments tests server continues when all fragments are quarantined
func TestServerContinuesWithAllQuarantinedFragments(t *testing.T) {
	tmpDir := t.TempDir()
	fragmentsDir := filepath.Join(tmpDir, "fragments")
	schemaDir := t.TempDir()

	// Create schema
	schema := `{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type": "object",
		"required": ["openapi", "info", "paths"],
		"properties": {
			"openapi": {"type": "string"},
			"info": {"type": "object"},
			"paths": {"type": "object"}
		}
	}`
	schemaPath := filepath.Join(schemaDir, "schema.json")
	if err := os.WriteFile(schemaPath, []byte(schema), 0644); err != nil {
		t.Fatalf("Failed to write schema: %v", err)
	}

	// Create only invalid fragments
	invalidFragment1 := `{
		"openapi": "3.1.0",
		"info": {"title": "Invalid 1"}
	}`
	invalidPath1 := filepath.Join(fragmentsDir, "invalid1.yaml")
	if err := os.MkdirAll(filepath.Dir(invalidPath1), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	if err := os.WriteFile(invalidPath1, []byte(invalidFragment1), 0644); err != nil {
		t.Fatalf("Failed to write invalid fragment1: %v", err)
	}

	invalidFragment2 := `{
		"openapi": "3.1.0",
		"info": {"title": "Invalid 2"}
	}`
	invalidPath2 := filepath.Join(fragmentsDir, "invalid2.yaml")
	if err := os.WriteFile(invalidPath2, []byte(invalidFragment2), 0644); err != nil {
		t.Fatalf("Failed to write invalid fragment2: %v", err)
	}

	// Load and validate - this should NOT fail
	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatalf("Failed to create fragment loader: %v", err)
	}

	if err := loader.LoadDirectory(fragmentsDir); err != nil {
		t.Fatalf("Failed to load fragments: %v", err)
	}

	// Validate should not error even if all fragments are invalid
	if err := loader.ValidateFragments(schemaPath); err != nil {
		t.Fatalf("ValidateFragments should not fail when all fragments are invalid: %v", err)
	}

	// Merge should succeed with empty spec
	mergedJSON, err := loader.MergeFragments("http://localhost:8080")
	if err != nil {
		t.Fatalf("MergeFragments should succeed even with all quarantined fragments: %v", err)
	}

	// Verify we get a valid (empty) OpenAPI spec
	var mergedSpec map[string]interface{}
	if err := json.Unmarshal(mergedJSON, &mergedSpec); err != nil {
		t.Fatalf("Failed to parse merged spec: %v", err)
	}

	// Should have basic OpenAPI structure
	if mergedSpec["openapi"] != "3.1.0" {
		t.Errorf("Expected openapi 3.1.0, got %v", mergedSpec["openapi"])
	}

	paths, ok := mergedSpec["paths"].(map[string]interface{})
	if !ok {
		t.Fatal("Merged spec missing paths")
	}

	if len(paths) != 0 {
		t.Errorf("Expected 0 paths in merged spec when all fragments quarantined, got %d", len(paths))
	}

	// Verify server_continues flag is true in status
	status := loader.GetFragmentStatus()
	if serverContinues, ok := status["server_continues"].(bool); !ok || !serverContinues {
		t.Error("Expected server_continues to be true")
	}

	// Verify all fragments were quarantined
	if loader.GetValidFragmentCount() != 0 {
		t.Errorf("Expected 0 valid fragments, got %d", loader.GetValidFragmentCount())
	}
	if loader.GetQuarantinedCount() != 2 {
		t.Errorf("Expected 2 quarantined fragments, got %d", loader.GetQuarantinedCount())
	}
}

// TestFragmentStatusEndpoint tests the GetFragmentStatus response format
func TestFragmentStatusEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	fragmentsDir := filepath.Join(tmpDir, "fragments")
	schemaDir := t.TempDir()

	// Create schema
	schema := `{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type": "object",
		"required": ["openapi", "info", "paths"],
		"properties": {
			"openapi": {"type": "string"},
			"info": {"type": "object"},
			"paths": {"type": "object"},
			"x-seam-schema": {"type": "string"},
			"x-api-version": {"type": "string"}
		}
	}`
	schemaPath := filepath.Join(schemaDir, "schema.json")
	if err := os.WriteFile(schemaPath, []byte(schema), 0644); err != nil {
		t.Fatalf("Failed to write schema: %v", err)
	}

	// Create valid fragment
	validFragment := `{
		"openapi": "3.1.0",
		"info": {"title": "Valid Service", "version": "1.0.0"},
		"paths": {"/api/test": {"get": {"summary": "Test endpoint"}}},
		"x-seam-schema": "1.0.0",
		"x-api-version": "v1"
	}`
	validPath := filepath.Join(fragmentsDir, "myservice", "fragment.yaml")
	if err := os.MkdirAll(filepath.Dir(validPath), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	if err := os.WriteFile(validPath, []byte(validFragment), 0644); err != nil {
		t.Fatalf("Failed to write valid fragment: %v", err)
	}

	// Create invalid fragment
	invalidFragment := `{
		"openapi": "3.1.0",
		"info": {"title": "Invalid"}
	}`
	invalidPath := filepath.Join(fragmentsDir, "invalid", "fragment.yaml")
	if err := os.MkdirAll(filepath.Dir(invalidPath), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	if err := os.WriteFile(invalidPath, []byte(invalidFragment), 0644); err != nil {
		t.Fatalf("Failed to write invalid fragment: %v", err)
	}

	// Load and validate
	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatalf("Failed to create fragment loader: %v", err)
	}

	if err := loader.LoadDirectory(fragmentsDir); err != nil {
		t.Fatalf("Failed to load fragments: %v", err)
	}

	if err := loader.ValidateFragments(schemaPath); err != nil {
		t.Fatalf("ValidateFragments failed: %v", err)
	}

	// Get status
	status := loader.GetFragmentStatus()

	// Verify response structure
	fragments, ok := status["fragments"].([]FragmentStatus)
	if !ok {
		t.Fatalf("Expected fragments to be []FragmentStatus, got %T", status["fragments"])
	}

	if len(fragments) != 2 {
		t.Errorf("Expected 2 fragments in status, got %d", len(fragments))
	}

	validCount := status["valid_count"].(int)
	quarantinedCount := status["quarantined_count"].(int)
	totalCount := status["total_count"].(int)

	if validCount != 1 {
		t.Errorf("Expected valid_count=1, got %d", validCount)
	}
	if quarantinedCount != 1 {
		t.Errorf("Expected quarantined_count=1, got %d", quarantinedCount)
	}
	if totalCount != 2 {
		t.Errorf("Expected total_count=2, got %d", totalCount)
	}

	// Verify fragment details
	var validFrag, invalidFrag *FragmentStatus
	for i := range fragments {
		switch fragments[i].Status {
		case "valid":
			validFrag = &fragments[i]
		case "quarantined":
			invalidFrag = &fragments[i]
		}
	}

	if validFrag == nil {
		t.Fatal("Expected to find a valid fragment")
	}
	if invalidFrag == nil {
		t.Fatal("Expected to find a quarantined fragment")
	}

	// Check valid fragment details
	if validFrag.SourceFile != validPath {
		t.Errorf("Expected valid fragment source %s, got %s", validPath, validFrag.SourceFile)
	}
	if validFrag.Owner != "myservice" {
		t.Errorf("Expected owner=myservice, got %s", validFrag.Owner)
	}
	if validFrag.SchemaVersion != "1.0.0" {
		t.Errorf("Expected schema_version=1.0.0, got %s", validFrag.SchemaVersion)
	}
	if validFrag.APIVersion != "v1" {
		t.Errorf("Expected api_version=v1, got %s", validFrag.APIVersion)
	}
	if validFrag.Hash == "" {
		t.Error("Expected hash to be set")
	}

	// Check invalid fragment details
	if invalidFrag.SourceFile != invalidPath {
		t.Errorf("Expected invalid fragment source %s, got %s", invalidPath, invalidFrag.SourceFile)
	}
	if len(invalidFrag.QuarantineReasons) == 0 {
		t.Error("Expected quarantine reasons to be set")
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsInString(s, substr))
}

func containsInString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
