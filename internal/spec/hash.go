package spec

import (
	"crypto/sha256"
	"encoding/hex"
)

// ComputeSpecHash computes a stable SHA256 hash of spec data for use as a version identifier.
//
// # Stability Guarantees
//
// This function provides deterministic hashing: the same input bytes will always produce
// the same hash output, regardless of:
//   - Number of calls (tested with 100+ iterations)
//   - Concurrent access (thread-safe)
//   - System architecture or platform
//
// # JSON Normalization Behavior
//
// The hash is computed from the RAW bytes of the input, NOT from normalized JSON.
// This means:
//   - Different JSON serializations of the same semantic content WILL produce different hashes
//   - Example: {"a":1} (compact) vs {"a": 1} (with space) produce different hashes
//   - For stability across serialization formats, ensure consistent JSON encoding before calling
//
// The caller is responsible for normalizing JSON if byte-level stability is required
// across different serialization sources (e.g., compact vs indented, different whitespace).
//
// # Hash Properties
//
// Returns the first 16 hex characters (64 bits) of the SHA256 hash:
//   - Sufficient for version identification (collision probability: ~1 in 2^64)
//   - Safe for use in HTTP headers (contains only hex characters)
//   - Fixed length: always 16 characters
//
// # Usage
//
// For fragment mode: Call with the result of json.MarshalIndent() on the merged spec.
// For static mode: Call with the raw YAML/JSON file bytes.
func ComputeSpecHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])[:16]
}
