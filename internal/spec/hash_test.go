package spec

import (
	"sync"
	"testing"
)

// TestComputeSpecHashBasic verifies the SHA256 hash computation function
func TestComputeSpecHashBasic(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantLen int
		wantHex bool
	}{
		{
			name:    "Empty input",
			data:    []byte(""),
			wantLen: 16,
			wantHex: true,
		},
		{
			name:    "Simple JSON",
			data:    []byte(`{"openapi":"3.1.0","info":{"title":"Test API","version":"1.0.0"},"paths":{}}`),
			wantLen: 16,
			wantHex: true,
		},
		{
			name:    "Larger spec",
			data:    []byte(`{"openapi":"3.1.0","info":{"title":"Test API","version":"1.0.0","description":"A test API"},"paths":{"/test":{"get":{"summary":"Test endpoint","responses":{"200":{"description":"OK"}}}}}}`),
			wantLen: 16,
			wantHex: true,
		},
		{
			name:    "Binary data",
			data:    []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05},
			wantLen: 16,
			wantHex: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeSpecHash(tt.data)

			// Check length is exactly 16 characters (64 bits)
			if len(got) != tt.wantLen {
				t.Errorf("ComputeSpecHash() length = %v, want %v", len(got), tt.wantLen)
			}

			// Check result contains only hexadecimal characters
			for i := 0; i < len(got); i++ {
				if !isHexChar(got[i]) {
					t.Errorf("ComputeSpecHash() contains non-hex character at index %d: %c", i, got[i])
				}
			}
		})
	}
}

// TestHashStability verifies that the same input produces the same hash across multiple calls
func TestHashStability(t *testing.T) {
	testData := []byte(`{"openapi":"3.1.0","info":{"title":"Test","version":"1.0.0"},"paths":{}}`)

	// Compute hash multiple times
	var results [100]string
	for i := 0; i < 100; i++ {
		results[i] = ComputeSpecHash(testData)
	}

	// All results should be identical
	first := results[0]
	for i, result := range results {
		if result != first {
			t.Errorf("ComputeSpecHash() call %d = %v, want %v (unstable)", i, result, first)
		}
	}
}

// TestHashStabilityConcurrent verifies hash stability under concurrent access
func TestHashStabilityConcurrent(t *testing.T) {
	testData := []byte(`{"openapi":"3.1.0","info":{"title":"Test","version":"1.0.0"},"paths":{}}`)
	iterations := 100

	// Compute expected hash
	expected := ComputeSpecHash(testData)

	// Run concurrent computations
	var wg sync.WaitGroup
	results := make([]string, iterations)
	errors := make(chan error, iterations)

	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = ComputeSpecHash(testData)
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Fatal(err)
	}

	// Verify all results match expected
	for i, result := range results {
		if result != expected {
			t.Errorf("Concurrent call %d = %v, want %v", i, result, expected)
		}
	}
}

// TestHashUniqueness verifies that different inputs produce different hashes
func TestHashUniqueness(t *testing.T) {
	testCases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte("")},
		{"one byte", []byte("a")},
		{"short", []byte("short")},
		{"json1", []byte(`{"a":1}`)},
		{"json2", []byte(`{"a":2}`)},
		{"json3", []byte(`{"b":1}`)},
		{"spec1", []byte(`{"openapi":"3.1.0","info":{"title":"API","version":"1.0.0"}}`)},
		{"spec2", []byte(`{"openapi":"3.1.0","info":{"title":"API","version":"1.0.1"}}`)},
	}

	hashes := make(map[string]bool)
	for _, tc := range testCases {
		hash := ComputeSpecHash(tc.data)
		if hashes[hash] {
			t.Errorf("Hash collision detected: %s produced same hash as another input", tc.name)
		}
		hashes[hash] = true
	}
}

// TestHashDeterminism verifies that byte-level differences produce different hashes
func TestHashDeterminism(t *testing.T) {
	testCases := []struct {
		name     string
		data1    []byte
		data2    []byte
		sameHash bool
	}{
		{
			name:     "identical JSON",
			data1:    []byte(`{"openapi":"3.1.0"}`),
			data2:    []byte(`{"openapi":"3.1.0"}`),
			sameHash: true,
		},
		{
			name:     "different spacing",
			data1:    []byte(`{"a":1}`),
			data2:    []byte(`{"a": 1}`),
			sameHash: false, // Different bytes = different hash (caller must normalize)
		},
		{
			name:     "different key order",
			data1:    []byte(`{"a":1,"b":2}`),
			data2:    []byte(`{"b":2,"a":1}`),
			sameHash: false, // Different bytes = different hash
		},
		{
			name:     "case sensitivity",
			data1:    []byte(`{"openapi":"3.1.0"}`),
			data2:    []byte(`{"OPENAPI":"3.1.0"}`),
			sameHash: false,
		},
		{
			name:  "trailing newline",
			data1: []byte(`{"test":true}`),
			data2: []byte(`{"test":true}
`),
			sameHash: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			hash1 := ComputeSpecHash(tc.data1)
			hash2 := ComputeSpecHash(tc.data2)

			if tc.sameHash {
				if hash1 != hash2 {
					t.Errorf("Expected same hash, got %v vs %v", hash1, hash2)
				}
			} else {
				if hash1 == hash2 {
					t.Errorf("Expected different hashes, got same: %v", hash1)
				}
			}
		})
	}
}

// TestHashFromRealSpec verifies hash works with realistic OpenAPI spec content
func TestHashFromRealSpec(t *testing.T) {
	// Minimal but realistic OpenAPI 3.1 spec
	spec := `{
  "openapi": "3.1.0",
  "info": {
    "title": "SEAM Test API",
    "version": "1.0.0",
    "description": "Test API for hash verification"
  },
  "paths": {
    "/test": {
      "get": {
        "summary": "Test endpoint",
        "operationId": "testEndpoint",
        "responses": {
          "200": {
            "description": "Success"
          }
        }
      }
    }
  }
}`

	hash1 := ComputeSpecHash([]byte(spec))
	hash2 := ComputeSpecHash([]byte(spec))

	if hash1 != hash2 {
		t.Errorf("Real spec hash unstable: %v vs %v", hash1, hash2)
	}

	// Verify hash properties
	if len(hash1) != 16 {
		t.Errorf("Hash length incorrect: got %d, want 16", len(hash1))
	}

	for i := 0; i < len(hash1); i++ {
		if !isHexChar(hash1[i]) {
			t.Errorf("Hash contains non-hex character at index %d: %c", i, hash1[i])
		}
	}
}

// isHexChar checks if a character is a valid hexadecimal digit
func isHexChar(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
