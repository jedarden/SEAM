package server

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
)

// ScopeVersionCache implements a bounded retention map for scope versions.
// Per the specification:
//   - Most recent 4 distinct scope versions per identity
//   - 24 hour idle eviction
//   - Global LRU cap (100 entries total)
//
// This cache is NEVER used for current-scope filtering - it only stores
// historical scope versions for the X-SEAM-Scope-Version header correlation.
type ScopeVersionCache struct {
	mu       sync.RWMutex
	entries  map[string]*identityScopeVersions // identity -> scope versions
	lruList  []string                          // LRU tracking by identity
	maxLRU   int                                // Global LRU cap
	maxPerID int                                // Max versions per identity
	idleTTL  time.Duration                      // Idle eviction TTL
}

// identityScopeVersions holds the scope versions for a single identity
type identityScopeVersions struct {
	identity     string
	versions     []*scopeVersionEntry
	lastAccessed time.Time
}

// scopeVersionEntry represents a single scope version for an identity
type scopeVersionEntry struct {
	versionHash string
	scopes      []string // Sorted for canonical representation
	timestamp   time.Time
}

// NewScopeVersionCache creates a new bounded scope version cache
func NewScopeVersionCache() *ScopeVersionCache {
	return &ScopeVersionCache{
		entries:  make(map[string]*identityScopeVersions),
		lruList:  make([]string, 0, 100),
		maxLRU:   100,                        // Global LRU cap
		maxPerID: 4,                          // Max 4 versions per identity
		idleTTL:  24 * time.Hour,            // 24 hour idle eviction
	}
}

// identityKey returns the cache key for an identity
// Uses the stable node key from the resolved identity
func identityKey(identity *Identity) string {
	if identity == nil {
		return "anonymous"
	}
	if identity.NodeKey != "" {
		return identity.NodeKey
	}
	return identity.NodeName // Fallback to node name
}

// ComputeScopeVersionHash computes a stable hash for a scope set
// The hash is computed from the sorted, normalized scope list
func ComputeScopeVersionHash(scopes []string) string {
	if len(scopes) == 0 {
		// Empty scope set has a canonical hash
		return "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // SHA-256 of empty string
	}

	// Normalize and sort scopes for canonical representation
	normalized := make([]string, 0, len(scopes))
	seen := make(map[string]bool)

	for _, scope := range scopes {
		normalizedScope := strings.ToLower(strings.TrimSpace(scope))
		if normalizedScope != "" && !seen[normalizedScope] {
			seen[normalizedScope] = true
			normalized = append(normalized, normalizedScope)
		}
	}

	sort.Strings(normalized)

	// Compute SHA-256 hash
	h := sha256.New()
	for _, scope := range normalized {
		h.Write([]byte(scope))
		h.Write([]byte("\n")) // Separator for canonical representation
	}

	return hex.EncodeToString(h.Sum(nil))
}

// RecordScopeVersion records a scope version for an identity
// Returns the version hash (computed from the scope set)
func (c *ScopeVersionCache) RecordScopeVersion(identity *Identity, scopes []string) string {
	versionHash := ComputeScopeVersionHash(scopes)
	idKey := identityKey(identity)

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()

	// Get or create identity entry
	idEntry, exists := c.entries[idKey]
	if !exists {
		idEntry = &identityScopeVersions{
			identity:     idKey,
			versions:     make([]*scopeVersionEntry, 0, 1),
			lastAccessed: now,
		}
		c.entries[idKey] = idEntry
		c.updateLRU(idKey)
	}

	idEntry.lastAccessed = now

	// Check if this version already exists
	for _, entry := range idEntry.versions {
		if entry.versionHash == versionHash {
			entry.timestamp = now // Update timestamp
			c.updateLRU(idKey)
			return versionHash
		}
	}

	// Add new version entry
	newEntry := &scopeVersionEntry{
		versionHash: versionHash,
		scopes:      scopes,
		timestamp:   now,
	}

	idEntry.versions = append(idEntry.versions, newEntry)

	// Enforce per-identity cap (keep most recent)
	if len(idEntry.versions) > c.maxPerID {
		// Sort by timestamp descending and keep maxPerID most recent
		sortEntries(idEntry.versions)
		idEntry.versions = idEntry.versions[:c.maxPerID]
	}

	// Enforce global LRU cap
	c.evictIfNecessary()

	c.updateLRU(idKey)
	return versionHash
}

// GetCurrentScopeVersion returns the current (most recent) scope version for an identity
// Returns empty string if identity has no recorded scopes
func (c *ScopeVersionCache) GetCurrentScopeVersion(identity *Identity) string {
	idKey := identityKey(identity)

	c.mu.RLock()
	defer c.mu.RUnlock()

	idEntry, exists := c.entries[idKey]
	if !exists || len(idEntry.versions) == 0 {
		return ""
	}

	// Return most recent version
	return idEntry.versions[len(idEntry.versions)-1].versionHash
}

// GetScopesForVersion returns the scope set for a given version hash
// Returns nil if version not found for this identity
func (c *ScopeVersionCache) GetScopesForVersion(identity *Identity, versionHash string) []string {
	idKey := identityKey(identity)

	c.mu.RLock()
	defer c.mu.RUnlock()

	idEntry, exists := c.entries[idKey]
	if !exists {
		return nil
	}

	for _, entry := range idEntry.versions {
		if entry.versionHash == versionHash {
			return entry.scopes
		}
	}

	return nil
}

// CleanupIdleEntries removes entries that haven't been accessed in 24 hours
// Should be called periodically (e.g., every hour)
func (c *ScopeVersionCache) CleanupIdleEntries() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	removed := 0

	for idKey, idEntry := range c.entries {
		if now.Sub(idEntry.lastAccessed) > c.idleTTL {
			delete(c.entries, idKey)
			c.removeLRU(idKey)
			removed++
		}
	}

	return removed
}

// updateLRU moves an identity to the end of the LRU list (most recently used)
func (c *ScopeVersionCache) updateLRU(idKey string) {
	// Remove from existing position
	for i, key := range c.lruList {
		if key == idKey {
			c.lruList = append(c.lruList[:i], c.lruList[i+1:]...)
			break
		}
	}
	// Add to end (most recently used)
	c.lruList = append(c.lruList, idKey)
}

// removeLRU removes an identity from the LRU list
func (c *ScopeVersionCache) removeLRU(idKey string) {
	for i, key := range c.lruList {
		if key == idKey {
			c.lruList = append(c.lruList[:i], c.lruList[i+1:]...)
			return
		}
	}
}

// evictIfNecessary enforces the global LRU cap by evicting the least recently used entry
func (c *ScopeVersionCache) evictIfNecessary() {
	for len(c.entries) > c.maxLRU && len(c.lruList) > 0 {
		// Evict LRU entry (first in list)
		lruKey := c.lruList[0]
		delete(c.entries, lruKey)
		c.lruList = c.lruList[1:]
	}
}

// sortEntries sorts version entries by timestamp (oldest first)
func sortEntries(entries []*scopeVersionEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].timestamp.Before(entries[j].timestamp)
	})
}

// Stats returns cache statistics
func (c *ScopeVersionCache) Stats() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	totalVersions := 0
	for _, idEntry := range c.entries {
		totalVersions += len(idEntry.versions)
	}

	return map[string]interface{}{
		"identities":     len(c.entries),
		"total_versions": totalVersions,
		"lru_size":       len(c.lruList),
		"max_lru":        c.maxLRU,
		"max_per_id":     c.maxPerID,
		"idle_ttl_hours": c.idleTTL.Hours(),
	}
}
