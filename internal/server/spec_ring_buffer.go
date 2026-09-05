package server

import (
	"sync"
	"time"
)

// SpecRingBuffer is an in-memory ring buffer that stores the last N merged specs.
// It is restart-scoped (evicted on process restart) and provides:
//   - Retrieval by spec hash (returns nil with sinceKnown: false for unknown/evicted)
//   - Automatic eviction of oldest entries when capacity is reached
//   - Metadata about when each spec was active
//
// Phase 8.4: This ring buffer supports the /changes endpoint and the archive endpoint
// by maintaining a short-lived history of spec versions for diffing and serving.
type SpecRingBuffer struct {
	mu           sync.RWMutex
	entries      []*specRingEntry // Ring buffer storage
	capacity     int              // Maximum number of specs to store
	head         int              // Index of the most recent entry
	size         int              // Current number of entries in the buffer
	versionIndex map[string]int   // Map from spec hash to index in entries (for O(1) lookup)
}

// specRingEntry represents a single spec version in the ring buffer
type specRingEntry struct {
	SpecHash    string          // Full SHA256 hash of the spec
	SpecVersion string          // Truncated version (first 16 chars)
	SpecJSON    []byte          // The full OpenAPI spec JSON
	FirstSeen   time.Time       // When this spec version was first observed
	LastSeen    time.Time       // When this spec version was most recently active
	Routes      []RouteSnapshot // Snapshot of routes at this version
}

// RouteSnapshot captures the essential route metadata at a point in time
type RouteSnapshot struct {
	Path            string   // OpenAPI path template
	Method          string   // HTTP method
	RequiredScopes  []string // Scopes required for this route
	Deprecated      bool     // Whether the route was deprecated
	VisibilityKinds []string // Visibility kinds granted for this route
}

// NewSpecRingBuffer creates a new ring buffer with the specified capacity.
// A capacity of 0 means unlimited (not recommended for production).
func NewSpecRingBuffer(capacity int) *SpecRingBuffer {
	if capacity <= 0 {
		capacity = 10 // Default capacity
	}

	return &SpecRingBuffer{
		entries:      make([]*specRingEntry, capacity),
		capacity:     capacity,
		head:         -1,
		size:         0,
		versionIndex: make(map[string]int),
	}
}

// Add stores a new spec version in the ring buffer.
// If the buffer is full, the oldest entry is evicted.
// If the spec hash already exists, its LastSeen timestamp is updated.
//
// Returns the spec version (truncated hash).
func (rb *SpecRingBuffer) Add(specHash, specVersion string, specJSON []byte, routes []RouteSnapshot) string {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	now := time.Now()

	// Check if this version already exists
	if idx, exists := rb.versionIndex[specHash]; exists {
		// Update the existing entry's LastSeen timestamp and data
		entry := rb.entries[idx]
		entry.LastSeen = now
		entry.SpecJSON = specJSON // Update in case spec was reloaded
		entry.Routes = routes
		rb.entries[idx] = entry
		return specVersion
	}

	// Add new entry
	rb.head = (rb.head + 1) % rb.capacity

	// If we're evicting an entry, remove it from the index
	if rb.size >= rb.capacity {
		evictedEntry := rb.entries[rb.head]
		if evictedEntry != nil {
			delete(rb.versionIndex, evictedEntry.SpecHash)
		}
	} else {
		rb.size++
	}

	entry := &specRingEntry{
		SpecHash:    specHash,
		SpecVersion: specVersion,
		SpecJSON:    specJSON,
		FirstSeen:   now,
		LastSeen:    now,
		Routes:      routes,
	}

	rb.entries[rb.head] = entry
	rb.versionIndex[specHash] = rb.head

	return specVersion
}

// Get retrieves a spec version by its hash.
//
// Returns:
//   - specJSON: The spec JSON (or nil if not found)
//   - sinceKnown: true if the spec version is in the buffer, false otherwise
//   - firstSeen: When the spec was first added (zero time if not found)
//
// Per Phase 8.4 requirements: unknown/evicted specs return (nil, false, time.Time{})
// which translates to HTTP 200 with sinceKnown: false in the /changes endpoint.
func (rb *SpecRingBuffer) Get(specHash string) (specJSON []byte, sinceKnown bool, firstSeen time.Time) {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	idx, exists := rb.versionIndex[specHash]
	if !exists || idx < 0 || idx >= len(rb.entries) {
		return nil, false, time.Time{}
	}

	entry := rb.entries[idx]
	if entry == nil || entry.SpecHash != specHash {
		return nil, false, time.Time{}
	}

	return entry.SpecJSON, true, entry.FirstSeen
}

// GetCurrentVersion returns the most recent spec version
func (rb *SpecRingBuffer) GetCurrentVersion() (specHash, specVersion string, specJSON []byte, exists bool) {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if rb.size == 0 || rb.head < 0 {
		return "", "", nil, false
	}

	entry := rb.entries[rb.head]
	if entry == nil {
		return "", "", nil, false
	}

	return entry.SpecHash, entry.SpecVersion, entry.SpecJSON, true
}

// GetAllVersions returns all spec versions in the buffer, ordered from oldest to newest
func (rb *SpecRingBuffer) GetAllVersions() []SpecVersionInfo {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if rb.size == 0 {
		return []SpecVersionInfo{}
	}

	versions := make([]SpecVersionInfo, 0, rb.size)

	// Start from the oldest entry and iterate forward
	// The oldest entry is at (head - size + 1 + capacity) % capacity
	start := (rb.head - rb.size + 1 + rb.capacity) % rb.capacity

	for i := 0; i < rb.size; i++ {
		idx := (start + i) % rb.capacity
		entry := rb.entries[idx]
		if entry != nil {
			versions = append(versions, SpecVersionInfo{
				SpecHash:    entry.SpecHash,
				SpecVersion: entry.SpecVersion,
				FirstSeen:   entry.FirstSeen,
				LastSeen:    entry.LastSeen,
			})
		}
	}

	return versions
}

// GetRoutesForVersion returns the route snapshot for a given spec version
func (rb *SpecRingBuffer) GetRoutesForVersion(specHash string) []RouteSnapshot {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	idx, exists := rb.versionIndex[specHash]
	if !exists || idx < 0 || idx >= len(rb.entries) {
		return nil
	}

	entry := rb.entries[idx]
	if entry == nil {
		return nil
	}

	return entry.Routes
}

// SpecVersionInfo contains metadata about a spec version
type SpecVersionInfo struct {
	SpecHash    string    `json:"spec_hash"`
	SpecVersion string    `json:"spec_version"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
}

// Stats returns statistics about the ring buffer
func (rb *SpecRingBuffer) Stats() map[string]interface{} {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	return map[string]interface{}{
		"capacity":            rb.capacity,
		"size":                rb.size,
		"utilization_percent": float64(rb.size) / float64(rb.capacity) * 100,
	}
}
