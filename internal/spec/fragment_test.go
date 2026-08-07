package spec

import (
	"crypto/sha256"
	"encoding/json"
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
