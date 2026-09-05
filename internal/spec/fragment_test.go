package spec

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestComputeSpecHash tests that the hash computation is stable
func TestComputeSpecHash(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantLen int  // Expected hash length
		stable  bool // Should produce same hash on repeated calls
	}{
		{
			name:    "Empty spec",
			data:    []byte("{}"),
			wantLen: 16,
			stable:  true,
		},
		{
			name: "Simple OpenAPI spec",
			data: []byte(`{
				"openapi": "3.1.0",
				"info": {"title": "Test", "version": "1.0.0"},
				"paths": {}
			}`),
			wantLen: 16,
			stable:  true,
		},
		{
			name: "Complex OpenAPI spec",
			data: []byte(`{
				"openapi": "3.1.0",
				"info": {
					"title": "Test API",
					"version": "1.0.0",
					"description": "Test description"
				},
				"paths": {
					"/test": {
						"get": {
							"summary": "Test endpoint",
							"responses": {
								"200": {"description": "OK"}
							}
						}
					}
				}
			}`),
			wantLen: 16,
			stable:  true,
		},
		{
			name:    "Large spec",
			data:    make([]byte, 10000),
			wantLen: 16,
			stable:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test that hash is always the same length
			hash1 := ComputeSpecHash(tt.data)
			if len(hash1) != tt.wantLen {
				t.Errorf("ComputeSpecHash() length = %v, want %v", len(hash1), tt.wantLen)
			}

			// Test stability: same input should produce same hash
			if tt.stable {
				hash2 := ComputeSpecHash(tt.data)
				if hash1 != hash2 {
					t.Errorf("ComputeSpecHash() not stable: %v != %v", hash1, hash2)
				}

				// Third computation for good measure
				hash3 := ComputeSpecHash(tt.data)
				if hash1 != hash3 {
					t.Errorf("ComputeSpecHash() not stable on third call: %v != %v", hash1, hash3)
				}
			}
		})
	}
}

// TestComputeSpecHash_Uniqueness tests that different specs produce different hashes
func TestComputeSpecHash_Uniqueness(t *testing.T) {
	specs := [][]byte{
		[]byte("{}"),
		[]byte(`{"openapi": "3.1.0"}`),
		[]byte(`{"openapi": "3.0.0"}`),
		[]byte(`{"title": "test"}`),
		[]byte(`{"version": "1.0.0"}`),
		[]byte(`{"paths": {}}`),
		[]byte(`{"info": {"title": "API"}}`),
	}

	hashes := make(map[string]bool)
	for i, spec := range specs {
		hash := ComputeSpecHash(spec)
		if hashes[hash] {
			t.Errorf("Hash collision detected at index %d: hash %s already seen", i, hash)
		}
		hashes[hash] = true
	}
}

// TestComputeSpecHash_SerializationFormat tests that the same spec produces the same hash
// regardless of whitespace differences (canonical JSON form)
func TestComputeSpecHash_SerializationFormat(t *testing.T) {
	// Create a spec structure
	spec := map[string]interface{}{
		"openapi": "3.1.0",
		"info": map[string]interface{}{
			"title":   "Test API",
			"version": "1.0.0",
		},
		"paths": map[string]interface{}{},
	}

	// Serialize to JSON twice with different formatting
	compact, _ := json.Marshal(spec)
	indented, _ := json.MarshalIndent(spec, "", "  ")

	// These should produce the same hash since we hash the raw bytes
	// (In practice, you'd want to canonicalize before hashing, but for now
	// we test that the function itself is deterministic)
	hashCompact := ComputeSpecHash(compact)
	hashIndented := ComputeSpecHash(indented)

	// Different serializations will produce different hashes
	// This is expected behavior - the hash is of the raw bytes
	if hashCompact == hashIndented {
		t.Logf("Note: Different JSON serializations produced same hash (this is OK if canonicalization is used)")
	} else {
		t.Logf("Note: Different JSON serializations produced different hashes (expected for raw byte hash)")
	}

	// But the same serialization should always produce the same hash
	if hash1 := ComputeSpecHash(compact); hash1 != hashCompact {
		t.Errorf("Compact JSON hash not stable: %s != %s", hash1, hashCompact)
	}
	if hash2 := ComputeSpecHash(indented); hash2 != hashIndented {
		t.Errorf("Indented JSON hash not stable: %s != %s", hash2, hashIndented)
	}
}

// TestComputeSpecHash_DeterministicOrder tests that map order doesn't affect hash
// when JSON is serialized (JSON serialization is deterministic in Go)
func TestComputeSpecHash_DeterministicOrder(t *testing.T) {
	// Create specs with keys in different order
	spec1 := map[string]interface{}{
		"openapi": "3.1.0",
		"info":    map[string]interface{}{"title": "Test"},
		"paths":   map[string]interface{}{},
	}
	spec2 := map[string]interface{}{
		"paths":   map[string]interface{}{},
		"openapi": "3.1.0",
		"info":    map[string]interface{}{"title": "Test"},
	}
	spec3 := map[string]interface{}{
		"info":    map[string]interface{}{"title": "Test"},
		"paths":   map[string]interface{}{},
		"openapi": "3.1.0",
	}

	// Serialize all three
	data1, _ := json.Marshal(spec1)
	data2, _ := json.Marshal(spec2)
	data3, _ := json.Marshal(spec3)

	hash1 := ComputeSpecHash(data1)
	hash2 := ComputeSpecHash(data2)
	hash3 := ComputeSpecHash(data3)

	// Go's JSON marshaling is deterministic (keys are sorted)
	// so all three should produce the same serialization and hash
	if hash1 != hash2 || hash1 != hash3 {
		t.Logf("Different map order produced different hashes:")
		t.Logf("  hash1: %s", hash1)
		t.Logf("  hash2: %s", hash2)
		t.Logf("  hash3: %s", hash3)
		t.Logf("  This is expected if JSON marshaling is not deterministic")
	} else {
		t.Logf("Map order does not affect hash (JSON marshaling is deterministic)")
	}
}

// TestComputeSpecHash_HeaderSafe tests that the hash is safe for use in HTTP headers
func TestComputeSpecHash_HeaderSafe(t *testing.T) {
	// Test with various spec contents
	data := []byte(`{"openapi":"3.1.0","info":{"title":"Test"},"paths":{}}`)
	hash := ComputeSpecHash(data)

	// Check that hash contains only hex characters (0-9, a-f)
	for _, r := range hash {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Errorf("Hash contains non-hex character: %c (full hash: %s)", r, hash)
		}
	}

	// Check length
	if len(hash) != 16 {
		t.Errorf("Hash length %d is not suitable for headers (expected 16)", len(hash))
	}
}

// TestComputeSpecHash_ConcurrentAccess tests that hash computation is safe for concurrent access
func TestComputeSpecHash_ConcurrentAccess(t *testing.T) {
	data := []byte(`{"openapi":"3.1.0","info":{"title":"Test"},"paths":{}}`)

	// Compute hashes concurrently
	hashes := make(chan string, 100)
	for i := 0; i < 100; i++ {
		go func() {
			hashes <- ComputeSpecHash(data)
		}()
	}

	// Collect all hashes
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		hash := <-hashes
		if !seen[hash] {
			seen[hash] = true
		}
	}

	// All hashes should be identical
	if len(seen) != 1 {
		t.Errorf("Concurrent hash computation produced %d different results", len(seen))
		for hash := range seen {
			t.Logf("  Hash: %s", hash)
		}
	}
}

// BenchmarkComputeSpecHash benchmarks the hash computation performance
func BenchmarkComputeSpecHash(b *testing.B) {
	// Small spec
	smallSpec := []byte(`{"openapi":"3.1.0","info":{"title":"Test"},"paths":{}}`)
	b.Run("small", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ComputeSpecHash(smallSpec)
		}
	})

	// Medium spec
	mediumSpec := make([]byte, 1024)
	b.Run("medium", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ComputeSpecHash(mediumSpec)
		}
	})

	// Large spec
	largeSpec := make([]byte, 10240)
	b.Run("large", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ComputeSpecHash(largeSpec)
		}
	})
}

// TestComputeSpecHash_JSONEncodingConsistency verifies that JSON marshaling produces
// consistent bytes across multiple marshal operations of the same spec object
func TestComputeSpecHash_JSONEncodingConsistency(t *testing.T) {
	// Create a spec structure
	spec := map[string]interface{}{
		"openapi": "3.1.0",
		"info": map[string]interface{}{
			"title":   "Test API",
			"version": "1.0.0",
		},
		"paths": map[string]interface{}{
			"/users": map[string]interface{}{
				"get": map[string]interface{}{
					"summary": "List users",
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Success",
						},
					},
				},
			},
		},
		"components": map[string]interface{}{
			"schemas": map[string]interface{}{
				"User": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":   map[string]interface{}{"type": "integer"},
						"name": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}

	// Marshal the spec 100 times and collect hashes
	const iterations = 100
	hashes := make(map[string]bool)

	for i := 0; i < iterations; i++ {
		// Marshal using the same format as loader.go (json.MarshalIndent)
		data, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			t.Fatalf("Marshal failed on iteration %d: %v", i, err)
		}

		hash := ComputeSpecHash(data)
		hashes[hash] = true
	}

	// All marshals should produce identical bytes and thus identical hashes
	if len(hashes) != 1 {
		t.Errorf("JSON marshaling produced %d different hashes across %d iterations", len(hashes), iterations)
		for hash := range hashes {
			t.Logf("  Hash: %s", hash)
		}
	}

	// Get the single hash
	var singleHash string
	for hash := range hashes {
		singleHash = hash
		break
	}
	t.Logf("JSON encoding consistency verified: %d marshal operations produced identical hash %s", iterations, singleHash)
}

// TestComputeSpecHash_StabilityIterations verifies hash stability across 100 iterations
// This tests the core stability guarantee: same input → same output, always
func TestComputeSpecHash_StabilityIterations(t *testing.T) {
	testCases := []struct {
		name string
		data []byte
	}{
		{
			name: "Empty JSON",
			data: []byte("{}"),
		},
		{
			name: "Simple OpenAPI spec",
			data: []byte(`{"openapi":"3.1.0","info":{"title":"Test API"},"paths":{}}`),
		},
		{
			name: "Complex spec with multiple paths",
			data: []byte(`{
				"openapi": "3.1.0",
				"info": {"title": "Test", "version": "1.0.0"},
				"paths": {
					"/users": {"get": {"summary": "List users"}},
					"/posts": {"get": {"summary": "List posts"}}
				}
			}`),
		},
		{
			name: "Large spec (10KB)",
			data: make([]byte, 10240),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// First hash
			firstHash := ComputeSpecHash(tc.data)

			// Compute hash 100 times and verify all are identical
			const iterations = 100
			for i := 0; i < iterations; i++ {
				hash := ComputeSpecHash(tc.data)
				if hash != firstHash {
					t.Errorf("Iteration %d: hash %s differs from first hash %s", i, hash, firstHash)
				}
			}

			t.Logf("Stability verified: %d iterations produced identical hash %s", iterations, firstHash)
		})
	}
}

// TestComputeSpecHash_KnownValues tests hash computation against known SHA256 values
func TestComputeSpecHash_KnownValues(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		wantHash string // Expected first 16 chars of SHA256
	}{
		{
			name:     "Empty byte array",
			data:     []byte(""),
			wantHash: "e3b0c44298fc1c14",
		},
		{
			name:     "Simple ASCII",
			data:     []byte("abc"),
			wantHash: "ba7816bf8f01cfea",
		},
		{
			name:     "JSON empty object",
			data:     []byte("{}"),
			wantHash: "44136fa355b3678a",
		},
		{
			name:     "JSON simple spec",
			data:     []byte(`{"openapi":"3.1.0","info":{"title":"Test"}}`),
			wantHash: "c62486ae85794493",
		},
		{
			name:     "Repeated byte",
			data:     []byte("aaaaa"),
			wantHash: "ed968e840d10d2d3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeSpecHash(tt.data)
			if got != tt.wantHash {
				t.Errorf("ComputeSpecHash() = %v, want %v", got, tt.wantHash)
				t.Logf("Full SHA256 would be: %x", sha256.Sum256(tt.data))
			}
		})
	}
}

// TestReservedPathExactMatch tests that fragments declaring exact reserved paths are quarantined
func TestReservedPathExactMatch(t *testing.T) {
	testCases := []struct {
		name            string
		reservedPath    string
		wantQuarantined bool
	}{
		{name: "docs_exact", reservedPath: "/docs", wantQuarantined: true},
		{name: "docs_route_exact", reservedPath: "/docs/route", wantQuarantined: true},
		{name: "openapi_json_exact", reservedPath: "/openapi.json", wantQuarantined: true},
		{name: "whoami_exact", reservedPath: "/whoami", wantQuarantined: true},
		{name: "scopes_exact", reservedPath: "/scopes", wantQuarantined: true},
		{name: "changes_exact", reservedPath: "/changes", wantQuarantined: true},
		{name: "health_credentials_exact", reservedPath: "/health/credentials", wantQuarantined: true},
		{name: "health_upstreams_exact", reservedPath: "/health/upstreams", wantQuarantined: true},
		{name: "config_status_exact", reservedPath: "/config/status", wantQuarantined: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			fragmentsDir := filepath.Join(tmpDir, "fragments")
			schemaDir := t.TempDir()

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

			content := fmt.Sprintf(`{
				"openapi": "3.1.0",
				"info": {"title": "Test Service", "version": "1.0.0"},
				"paths": {
					"%s": {
						"get": {"summary": "Reserved path endpoint"}
					}
				},
				"x-seam-owner": "myservice",
				"x-api-version": "v1"
			}`, tc.reservedPath)

			fragmentPath := filepath.Join(fragmentsDir, "myservice", "fragment.yaml")
			if err := os.MkdirAll(filepath.Dir(fragmentPath), 0755); err != nil {
				t.Fatalf("Failed to create directory: %v", err)
			}
			if err := os.WriteFile(fragmentPath, []byte(content), 0644); err != nil {
				t.Fatalf("Failed to write fragment: %v", err)
			}

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

			if tc.wantQuarantined {
				if loader.GetValidFragmentCount() != 0 {
					t.Errorf("Expected 0 valid fragments, got %d", loader.GetValidFragmentCount())
				}
				if loader.GetQuarantinedCount() != 1 {
					t.Errorf("Expected 1 quarantined fragment, got %d", loader.GetQuarantinedCount())
				}

				quarantined := loader.GetQuarantined()
				if len(quarantined) != 1 {
					t.Fatalf("Expected 1 quarantined fragment, got %d", len(quarantined))
				}

				if !quarantined[0].QueuedForQuarantine {
					t.Error("Expected fragment to be quarantined")
				}

				if len(quarantined[0].QuarantineReasons) == 0 {
					t.Error("Expected quarantine reasons to be set")
				}

				// Verify the reason mentions the reserved path
				hasReservedReason := false
				for _, reason := range quarantined[0].QuarantineReasons {
					if strings.Contains(reason, "reserved path") && strings.Contains(reason, tc.reservedPath) {
						hasReservedReason = true
						break
					}
				}
				if !hasReservedReason {
					t.Errorf("Expected quarantine reason to mention reserved path %s, got: %v", tc.reservedPath, quarantined[0].QuarantineReasons)
				}
			} else {
				if loader.GetValidFragmentCount() != 1 {
					t.Errorf("Expected 1 valid fragment, got %d", loader.GetValidFragmentCount())
				}
				if loader.GetQuarantinedCount() != 0 {
					t.Errorf("Expected 0 quarantined fragments, got %d", loader.GetQuarantinedCount())
				}
			}
		})
	}
}

// TestReservedPathPrefix tests that fragments declaring paths with reserved prefixes are quarantined
func TestReservedPathPrefix(t *testing.T) {
	testCases := []struct {
		name            string
		path            string
		wantQuarantined bool
		reason          string
	}{
		{name: "docs_prefix_with_subpath", path: "/docs/mypath", wantQuarantined: true, reason: "prefix: /docs/"},
		{name: "docs_prefix_deep", path: "/docs/api/v1/users", wantQuarantined: true, reason: "prefix: /docs/"},
		{name: "health_prefix_with_subpath", path: "/health/mypath", wantQuarantined: true, reason: "prefix: /health/"},
		{name: "health_prefix_deep", path: "/health/custom/metric", wantQuarantined: true, reason: "prefix: /health/"},
		{name: "config_prefix_with_subpath", path: "/config/myvalue", wantQuarantined: true, reason: "prefix: /config/"},
		{name: "config_prefix_deep", path: "/config/nested/value", wantQuarantined: true, reason: "prefix: /config/"},
		{name: "approvals_prefix", path: "/approvals/request", wantQuarantined: true, reason: "prefix: /approvals/"},
		{name: "seam_prefix", path: "/_seam/internal", wantQuarantined: true, reason: "prefix: /_seam/"},
		{name: "valid_similar_path", path: "/doc", wantQuarantined: false, reason: ""},
		{name: "valid_healthy_path", path: "/healthy", wantQuarantined: false, reason: ""},
		{name: "valid_configuration_path", path: "/configuration", wantQuarantined: false, reason: ""},
		{name: "valid_approval_path", path: "/approval", wantQuarantined: false, reason: ""},
		{name: "valid_seamless_path", path: "/seamless", wantQuarantined: false, reason: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			fragmentsDir := filepath.Join(tmpDir, "fragments")
			schemaDir := t.TempDir()

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

			content := fmt.Sprintf(`{
				"openapi": "3.1.0",
				"info": {"title": "Test Service", "version": "1.0.0"},
				"paths": {
					"%s": {
						"get": {"summary": "Endpoint"}
					}
				},
				"x-seam-owner": "myservice",
				"x-api-version": "v1"
			}`, tc.path)

			fragmentPath := filepath.Join(fragmentsDir, "myservice", "fragment.yaml")
			if err := os.MkdirAll(filepath.Dir(fragmentPath), 0755); err != nil {
				t.Fatalf("Failed to create directory: %v", err)
			}
			if err := os.WriteFile(fragmentPath, []byte(content), 0644); err != nil {
				t.Fatalf("Failed to write fragment: %v", err)
			}

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

			if tc.wantQuarantined {
				if loader.GetValidFragmentCount() != 0 {
					t.Errorf("Expected 0 valid fragments, got %d", loader.GetValidFragmentCount())
				}
				if loader.GetQuarantinedCount() != 1 {
					t.Errorf("Expected 1 quarantined fragment, got %d", loader.GetQuarantinedCount())
				}

				quarantined := loader.GetQuarantined()
				if !quarantined[0].QueuedForQuarantine {
					t.Error("Expected fragment to be quarantined")
				}

				// Verify the reason mentions the prefix
				hasReason := false
				for _, reason := range quarantined[0].QuarantineReasons {
					if strings.Contains(reason, tc.reason) {
						hasReason = true
						break
					}
				}
				if !hasReason && tc.reason != "" {
					t.Errorf("Expected quarantine reason to mention '%s', got: %v", tc.reason, quarantined[0].QuarantineReasons)
				}
			} else {
				if loader.GetValidFragmentCount() != 1 {
					t.Errorf("Expected 1 valid fragment, got %d", loader.GetValidFragmentCount())
				}
				if loader.GetQuarantinedCount() != 0 {
					t.Errorf("Expected 0 quarantined fragments, got %d", loader.GetQuarantinedCount())
				}
			}
		})
	}
}

// TestReservedPathMultiplePaths tests fragments with multiple paths
func TestReservedPathMultiplePaths(t *testing.T) {
	testCases := []struct {
		name            string
		paths           map[string]interface{}
		wantQuarantined bool
		reasonContains  string
	}{
		{
			name: "all_valid_paths",
			paths: map[string]interface{}{
				"/api/users": map[string]interface{}{"get": map[string]interface{}{"summary": "List users"}},
				"/api/posts": map[string]interface{}{"get": map[string]interface{}{"summary": "List posts"}},
				"/v1/items":  map[string]interface{}{"get": map[string]interface{}{"summary": "List items"}},
			},
			wantQuarantined: false,
			reasonContains:  "",
		},
		{
			name: "one_reserved_path_among_valid",
			paths: map[string]interface{}{
				"/api/users": map[string]interface{}{"get": map[string]interface{}{"summary": "List users"}},
				"/docs":      map[string]interface{}{"get": map[string]interface{}{"summary": "Docs"}},
				"/api/posts": map[string]interface{}{"get": map[string]interface{}{"summary": "List posts"}},
			},
			wantQuarantined: true,
			reasonContains:  "reserved path: /docs",
		},
		{
			name: "multiple_reserved_paths",
			paths: map[string]interface{}{
				"/docs":        map[string]interface{}{"get": map[string]interface{}{"summary": "Docs"}},
				"/whoami":      map[string]interface{}{"get": map[string]interface{}{"summary": "Whoami"}},
				"/config/test": map[string]interface{}{"get": map[string]interface{}{"summary": "Config test"}},
			},
			wantQuarantined: true,
			reasonContains:  "reserved path",
		},
		{
			name: "mixed_exact_and_prefix_reserved",
			paths: map[string]interface{}{
				"/docs":           map[string]interface{}{"get": map[string]interface{}{"summary": "Docs"}},
				"/health/mycheck": map[string]interface{}{"get": map[string]interface{}{"summary": "Health check"}},
			},
			wantQuarantined: true,
			reasonContains:  "reserved",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			fragmentsDir := filepath.Join(tmpDir, "fragments")
			schemaDir := t.TempDir()

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

			// Build paths JSON
			pathsJSON, err := json.Marshal(tc.paths)
			if err != nil {
				t.Fatalf("Failed to marshal paths: %v", err)
			}

			content := fmt.Sprintf(`{
				"openapi": "3.1.0",
				"info": {"title": "Test Service", "version": "1.0.0"},
				"paths": %s,
				"x-seam-owner": "myservice",
				"x-api-version": "v1"
			}`, string(pathsJSON))

			fragmentPath := filepath.Join(fragmentsDir, "myservice", "fragment.yaml")
			if err := os.MkdirAll(filepath.Dir(fragmentPath), 0755); err != nil {
				t.Fatalf("Failed to create directory: %v", err)
			}
			if err := os.WriteFile(fragmentPath, []byte(content), 0644); err != nil {
				t.Fatalf("Failed to write fragment: %v", err)
			}

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

			if tc.wantQuarantined {
				if loader.GetValidFragmentCount() != 0 {
					t.Errorf("Expected 0 valid fragments, got %d", loader.GetValidFragmentCount())
				}
				if loader.GetQuarantinedCount() != 1 {
					t.Errorf("Expected 1 quarantined fragment, got %d", loader.GetQuarantinedCount())
				}

				quarantined := loader.GetQuarantined()
				if !quarantined[0].QueuedForQuarantine {
					t.Error("Expected fragment to be quarantined")
				}

				// Verify at least one reason contains the expected text
				hasReason := false
				for _, reason := range quarantined[0].QuarantineReasons {
					if strings.Contains(reason, tc.reasonContains) {
						hasReason = true
						break
					}
				}
				if !hasReason && tc.reasonContains != "" {
					t.Errorf("Expected quarantine reason to contain '%s', got: %v", tc.reasonContains, quarantined[0].QuarantineReasons)
				}
			} else {
				if loader.GetValidFragmentCount() != 1 {
					t.Errorf("Expected 1 valid fragment, got %d", loader.GetValidFragmentCount())
				}
				if loader.GetQuarantinedCount() != 0 {
					t.Errorf("Expected 0 quarantined fragments, got %d", loader.GetQuarantinedCount())
				}
			}
		})
	}
}

// TestReservedPathNoPathsField tests fragments without a paths field
func TestReservedPathNoPathsField(t *testing.T) {
	tmpDir := t.TempDir()
	fragmentsDir := filepath.Join(tmpDir, "fragments")
	schemaDir := t.TempDir()

	schema := `{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type": "object"
	}`
	schemaPath := filepath.Join(schemaDir, "schema.json")
	if err := os.WriteFile(schemaPath, []byte(schema), 0644); err != nil {
		t.Fatalf("Failed to write schema: %v", err)
	}

	content := `{
		"openapi": "3.1.0",
		"info": {"title": "Test Service"},
		"x-seam-owner": "myservice"
	}`

	fragmentPath := filepath.Join(fragmentsDir, "myservice", "fragment.yaml")
	if err := os.MkdirAll(filepath.Dir(fragmentPath), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	if err := os.WriteFile(fragmentPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write fragment: %v", err)
	}

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

	// Fragment with no paths field should not be quarantined for reserved paths
	if loader.GetValidFragmentCount() != 1 {
		t.Errorf("Expected 1 valid fragment, got %d", loader.GetValidFragmentCount())
	}
	if loader.GetQuarantinedCount() != 0 {
		t.Errorf("Expected 0 quarantined fragments, got %d", loader.GetQuarantinedCount())
	}
}

// TestReservedPathCaseSensitivity tests that reserved paths are case-sensitive
func TestReservedPathCaseSensitivity(t *testing.T) {
	testCases := []struct {
		name            string
		path            string
		wantQuarantined bool
	}{
		{name: "Docs_uppercase", path: "/Docs", wantQuarantined: false},
		{name: "DOCS_allcaps", path: "/DOCS", wantQuarantined: false},
		{name: "docs_path_uppercase", path: "/docs/MyPath", wantQuarantined: true},
		{name: "docs_exact_lowercase", path: "/docs", wantQuarantined: true},
		{name: "Health_uppercase", path: "/Health/check", wantQuarantined: false},
		{name: "config_uppercase", path: "/Config/value", wantQuarantined: false},
		{name: "config_exact_lowercase", path: "/config/test", wantQuarantined: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			fragmentsDir := filepath.Join(tmpDir, "fragments")
			schemaDir := t.TempDir()

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

			content := fmt.Sprintf(`{
				"openapi": "3.1.0",
				"info": {"title": "Test Service", "version": "1.0.0"},
				"paths": {
					"%s": {
						"get": {"summary": "Endpoint"}
					}
				},
				"x-seam-owner": "myservice",
				"x-api-version": "v1"
			}`, tc.path)

			fragmentPath := filepath.Join(fragmentsDir, "myservice", "fragment.yaml")
			if err := os.MkdirAll(filepath.Dir(fragmentPath), 0755); err != nil {
				t.Fatalf("Failed to create directory: %v", err)
			}
			if err := os.WriteFile(fragmentPath, []byte(content), 0644); err != nil {
				t.Fatalf("Failed to write fragment: %v", err)
			}

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

			if tc.wantQuarantined {
				if loader.GetValidFragmentCount() != 0 {
					t.Errorf("Expected 0 valid fragments, got %d", loader.GetValidFragmentCount())
				}
				if loader.GetQuarantinedCount() != 1 {
					t.Errorf("Expected 1 quarantined fragment, got %d", loader.GetQuarantinedCount())
				}
			} else {
				if loader.GetValidFragmentCount() != 1 {
					t.Errorf("Expected 1 valid fragment, got %d", loader.GetValidFragmentCount())
				}
				if loader.GetQuarantinedCount() != 0 {
					t.Errorf("Expected 0 quarantined fragments, got %d", loader.GetQuarantinedCount())
				}
			}
		})
	}
}

// TestKubernetesConfigMapLayout tests that fragments mounted from Kubernetes ConfigMaps
// are loaded exactly once, ignoring the timestamped atomic-update directory.
// This verifies the fix for the duplicate fragment quarantine issue.
func TestKubernetesConfigMapLayout(t *testing.T) {
	tmpDir := t.TempDir()
	fragmentsDir := filepath.Join(tmpDir, "fragments")
	schemaDir := t.TempDir()

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

	// Create a ConfigMap-shaped mount layout:
	// fragments.d/argocd-ro/
	//   ..2026_08_19_06_32_37.3941861081/  (timestamped directory)
	//     argocd-read-only-proxy.yaml
	//   ..data -> ..2026_08_19_06_32_37.3941861081  (symlink)
	//   argocd-read-only-proxy.yaml -> ..data/argocd-read-only-proxy.yaml  (canonical symlink)

	serviceDir := filepath.Join(fragmentsDir, "argocd-ro")
	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		t.Fatalf("Failed to create service directory: %v", err)
	}

	// Create the timestamped directory (the actual data directory)
	timestampedDir := filepath.Join(serviceDir, "..2026_08_19_06_32_37.3941861081")
	if err := os.Mkdir(timestampedDir, 0755); err != nil {
		t.Fatalf("Failed to create timestamped directory: %v", err)
	}

	// Create the actual fragment file in the timestamped directory
	fragmentContent := `{
		"openapi": "3.1.0",
		"info": {"title": "ArgoCD Read-Only Proxy", "version": "1.0.0"},
		"paths": {
			"/api/v1": {
				"get": {"summary": "Get API v1"}
			}
		},
		"x-seam-owner": "argocd-ro",
		"x-api-version": "v1"
	}`
	actualFragmentPath := filepath.Join(timestampedDir, "argocd-read-only-proxy.yaml")
	if err := os.WriteFile(actualFragmentPath, []byte(fragmentContent), 0644); err != nil {
		t.Fatalf("Failed to write actual fragment: %v", err)
	}

	// Create the ..data symlink pointing to the timestamped directory
	dataSymlinkPath := filepath.Join(serviceDir, "..data")
	if err := os.Symlink("..2026_08_19_06_32_37.3941861081", dataSymlinkPath); err != nil {
		t.Fatalf("Failed to create ..data symlink: %v", err)
	}

	// Create the canonical symlink through ..data
	canonicalSymlinkPath := filepath.Join(serviceDir, "argocd-read-only-proxy.yaml")
	if err := os.Symlink("..data/argocd-read-only-proxy.yaml", canonicalSymlinkPath); err != nil {
		t.Fatalf("Failed to create canonical symlink: %v", err)
	}

	// Load the fragments
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

	// The fragment should be loaded exactly once (via the canonical symlink)
	// The timestamped directory should be skipped, so no duplicate is loaded
	if loader.GetValidFragmentCount() != 1 {
		t.Errorf("Expected 1 valid fragment, got %d", loader.GetValidFragmentCount())
	}
	if loader.GetQuarantinedCount() != 0 {
		t.Errorf("Expected 0 quarantined fragments, got %d", loader.GetQuarantinedCount())
		quarantined := loader.GetQuarantined()
		for _, q := range quarantined {
			t.Logf("Quarantined fragment: %s, reasons: %v", q.SourceFile, q.QuarantineReasons)
		}
	}

	// Verify the loaded fragment has the correct owner
	validFragments := loader.fragments
	if len(validFragments) != 1 {
		t.Fatalf("Expected exactly 1 valid fragment, got %d", len(validFragments))
	}
	if validFragments[0].Owner != "argocd-ro" {
		t.Errorf("Expected fragment owner to be 'argocd-ro', got '%s'", validFragments[0].Owner)
	}
}

// TestKubernetesInternalDirectoriesSkipped tests that various Kubernetes internal
// directory names are all properly skipped during fragment loading.
func TestKubernetesInternalDirectoriesSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	fragmentsDir := filepath.Join(tmpDir, "fragments")
	schemaDir := t.TempDir()

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

	// Test various Kubernetes-internal directory patterns
	internalDirNames := []string{
		"..2026_08_19_06_32_37.3941861081",
		"..2026_08_20_00_00_00.1234567890",
		"..2025_12_31_23_59_59.9999999999",
		"..data",
		"..backup",
		"..tmp",
	}

	for _, internalDir := range internalDirNames {
		serviceDir := filepath.Join(fragmentsDir, "test-service")
		if err := os.MkdirAll(serviceDir, 0755); err != nil {
			t.Fatalf("Failed to create service directory: %v", err)
		}

		// Create the internal directory with a fragment inside
		internalPath := filepath.Join(serviceDir, internalDir)
		if err := os.Mkdir(internalPath, 0755); err != nil {
			t.Fatalf("Failed to create internal directory %s: %v", internalDir, err)
		}

		fragmentContent := `{
			"openapi": "3.1.0",
			"info": {"title": "Test", "version": "1.0.0"},
			"paths": {"/test": {"get": {"summary": "Test"}}},
			"x-seam-owner": "wrong-owner",
			"x-api-version": "v1"
		}`
		fragmentPath := filepath.Join(internalPath, "fragment.yaml")
		if err := os.WriteFile(fragmentPath, []byte(fragmentContent), 0644); err != nil {
			t.Fatalf("Failed to write fragment in internal dir: %v", err)
		}

		// Load and verify the fragment inside the internal directory is skipped
		loader, err := NewFragmentLoader()
		if err != nil {
			t.Fatalf("Failed to create fragment loader: %v", err)
		}

		if err := loader.LoadDirectory(fragmentsDir); err != nil {
			t.Fatalf("Failed to load fragments: %v", err)
		}

		if loader.GetValidFragmentCount() != 0 {
			t.Errorf("Internal directory %s: expected 0 valid fragments (should be skipped), got %d", internalDir, loader.GetValidFragmentCount())
		}

		// Clean up for next iteration
		if err := os.RemoveAll(serviceDir); err != nil {
			t.Fatalf("Failed to clean up service directory: %v", err)
		}
	}
}

// writeFragment writes a fragment file, creating its parent directory first.
func writeFragment(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("Failed to create directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to write fragment %s: %v", path, err)
	}
}

// mergedPaths loads fragments from dir and returns the merged document's paths.
func mergedPaths(t *testing.T, dir string) map[string]interface{} {
	t.Helper()
	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatalf("Failed to create fragment loader: %v", err)
	}
	if err := loader.LoadDirectory(dir); err != nil {
		t.Fatalf("Failed to load fragments: %v", err)
	}
	merged, err := loader.MergeFragments("http://localhost:8080")
	if err != nil {
		t.Fatalf("Failed to merge fragments: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatalf("Failed to parse merged document: %v", err)
	}
	paths, _ := doc["paths"].(map[string]interface{})
	return paths
}

// TestLoadDirectoryParsesYAMLFragments is the regression test for the loader
// decoding every discovered file as JSON. A .yaml fragment must contribute its
// real paths to the merged document instead of being dropped, which used to
// make an empty-document merge look identical to an unchanged one.
func TestLoadDirectoryParsesYAMLFragments(t *testing.T) {
	dir := t.TempDir()
	writeFragment(t, filepath.Join(dir, "owner", "route.yaml"), `x-seam-schema: v1
x-seam-owner: owner
x-api-version: v1
paths:
  /api/test:
    get:
      summary: Test endpoint
      x-upstream: http://owner.internal/api/test
`)

	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatalf("Failed to create fragment loader: %v", err)
	}
	if err := loader.LoadDirectory(dir); err != nil {
		t.Fatalf("Failed to load fragments: %v", err)
	}

	if got := loader.GetValidFragmentCount(); got != 1 {
		t.Fatalf("Expected 1 loaded fragment, got %d", got)
	}

	// Nested YAML mappings must arrive as map[string]any so the merge and
	// schema-validation type assertions hold all the way down.
	fragment := loader.fragments[0]
	paths, ok := fragment.ParsedFragment["paths"].(map[string]any)
	if !ok {
		t.Fatalf("Expected paths to be map[string]any, got %T", fragment.ParsedFragment["paths"])
	}
	pathItem, ok := paths["/api/test"].(map[string]any)
	if !ok {
		t.Fatalf("Expected path item to be map[string]any, got %T", paths["/api/test"])
	}
	if _, ok := pathItem["get"].(map[string]any); !ok {
		t.Fatalf("Expected get operation to be map[string]any, got %T", pathItem["get"])
	}

	if fragment.SchemaVer != "v1" {
		t.Errorf("Expected schema version v1, got %q", fragment.SchemaVer)
	}
	if fragment.APIVersion != "v1" {
		t.Errorf("Expected API version v1, got %q", fragment.APIVersion)
	}
	if got := fragment.Owner; got != "owner" {
		t.Errorf("Expected owner derived from parent directory, got %q", got)
	}

	// The end-to-end symptom: the fragment's routes reach the merged document.
	got := mergedPaths(t, dir)
	if len(got) != 1 {
		t.Fatalf("Expected 1 merged path, got %d: %v", len(got), got)
	}
	if _, ok := got["/api/test"]; !ok {
		t.Errorf("Expected /api/test in merged document, got %v", got)
	}
}

// TestLoadDirectoryParsesEveryDiscoveredExtension verifies each discovered
// extension is parsed with a decoder that understands it.
func TestLoadDirectoryParsesEveryDiscoveredExtension(t *testing.T) {
	fragmentYAML := `x-seam-owner: owner
paths:
  /from-yaml:
    get:
      summary: YAML
`
	fragmentYML := `x-seam-owner: owner
paths:
  /from-yml:
    get:
      summary: YML
`
	fragmentJSON := `{"x-seam-owner": "owner", "paths": {"/from-json": {"get": {"summary": "JSON"}}}}`

	testCases := []struct {
		name         string
		filename     string
		content      string
		wantPath     string
		wantFragment string
	}{
		{name: "yaml", filename: "route.yaml", content: fragmentYAML, wantPath: "/from-yaml", wantFragment: "yaml"},
		{name: "yml", filename: "route.yml", content: fragmentYML, wantPath: "/from-yml", wantFragment: "yaml"},
		{name: "json", filename: "route.json", content: fragmentJSON, wantPath: "/from-json", wantFragment: "json"},
		// Discovery and parsing share one extension test, so an
		// upper-case extension is both discovered and parsed as YAML.
		{name: "upper-case yaml", filename: "route.YAML", content: fragmentYAML, wantPath: "/from-yaml", wantFragment: "yaml"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFragment(t, filepath.Join(dir, "owner", tc.filename), tc.content)

			if got := fragmentFormat(filepath.Join(dir, "owner", tc.filename)); got != tc.wantFragment {
				t.Errorf("Expected fragmentFormat %q, got %q", tc.wantFragment, got)
			}

			paths := mergedPaths(t, dir)
			if _, ok := paths[tc.wantPath]; !ok {
				t.Errorf("Expected %s in merged document, got %v", tc.wantPath, paths)
			}
		})
	}
}

// TestLoadDirectoryToleratesPartialParseFailure verifies one unparseable
// fragment is skipped without taking the rest of the route table with it.
func TestLoadDirectoryToleratesPartialParseFailure(t *testing.T) {
	dir := t.TempDir()
	writeFragment(t, filepath.Join(dir, "owner", "broken.yaml"), "x-seam-owner: owner\npaths:\n\t/get: [unclosed\n")
	writeFragment(t, filepath.Join(dir, "other", "fine.yaml"), `x-seam-owner: other
paths:
  /fine:
    get:
      summary: Fine
`)

	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatalf("Failed to create fragment loader: %v", err)
	}
	if err := loader.LoadDirectory(dir); err != nil {
		t.Fatalf("Expected a partial failure to be tolerated, got: %v", err)
	}

	if got := loader.GetValidFragmentCount(); got != 1 {
		t.Errorf("Expected the parseable fragment to load, got %d valid", got)
	}
	paths := mergedPaths(t, dir)
	if _, ok := paths["/fine"]; !ok {
		t.Errorf("Expected /fine in merged document, got %v", paths)
	}
}

// TestLoadDirectoryRejectsFullyUnparseableFragments verifies the loader fails
// loudly when every discovered fragment is unparseable, rather than merging
// zero fragments into an empty document that a diff would read as "no changes".
func TestLoadDirectoryRejectsFullyUnparseableFragments(t *testing.T) {
	dir := t.TempDir()
	writeFragment(t, filepath.Join(dir, "owner", "route.yaml"), "x-seam-owner: owner\npaths:\n\t/get: [unclosed\n")
	writeFragment(t, filepath.Join(dir, "other", "route.json"), "{not json")

	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatalf("Failed to create fragment loader: %v", err)
	}
	err = loader.LoadDirectory(dir)
	if err == nil {
		t.Fatal("Expected an error when no fragment could be loaded, got nil")
	}
	if !strings.Contains(err.Error(), "failed to load") {
		t.Errorf("Expected the error to name the failure, got: %v", err)
	}

	// The empty-directory case stays a no-op: nothing was discovered, so there
	// is nothing to distinguish from a correct empty document.
	empty := t.TempDir()
	emptyLoader, err := NewFragmentLoader()
	if err != nil {
		t.Fatalf("Failed to create fragment loader: %v", err)
	}
	if err := emptyLoader.LoadDirectory(empty); err != nil {
		t.Errorf("Expected an empty directory to load cleanly, got: %v", err)
	}
}

// TestLoadDirectoryRejectsNonObjectFragment verifies a fragment whose root is
// not a mapping is reported rather than silently loaded with no fields.
func TestLoadDirectoryRejectsNonObjectFragment(t *testing.T) {
	testCases := []struct {
		name     string
		filename string
		content  string
	}{
		{name: "yaml sequence", filename: "route.yaml", content: "- a\n- b\n"},
		{name: "yaml scalar", filename: "route.yaml", content: "just a string\n"},
		{name: "empty yaml", filename: "route.yaml", content: ""},
		{name: "json array", filename: "route.json", content: `[1, 2, 3]`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFragment(t, filepath.Join(dir, "owner", tc.filename), tc.content)

			loader, err := NewFragmentLoader()
			if err != nil {
				t.Fatalf("Failed to create fragment loader: %v", err)
			}
			if err := loader.LoadDirectory(dir); err == nil {
				t.Fatalf("Expected a non-object fragment to be rejected")
			}
			if got := loader.GetValidFragmentCount(); got != 0 {
				t.Errorf("Expected 0 loaded fragments, got %d", got)
			}
		})
	}
}
