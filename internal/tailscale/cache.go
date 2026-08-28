package tailscale

import (
	"sync"
	"time"
)

// keyCache provides TTL-based caching for Tailscale keys
type keyCache struct {
	mu          sync.RWMutex
	entries     map[string]*cacheEntry
	ttl         time.Duration
	holdDown    time.Duration
	lastFailure time.Time
}

type cacheEntry struct {
	key       *Key
	expiresAt time.Time
}

// newKeyCache creates a new key cache with specified TTL and hold-down duration
func newKeyCache(ttl, holdDown time.Duration) *keyCache {
	return &keyCache{
		entries:  make(map[string]*cacheEntry),
		ttl:      ttl,
		holdDown: holdDown,
	}
}

// Get retrieves a cached key if valid
func (c *keyCache) Get(workerID string) (*Key, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[workerID]
	if !ok {
		return nil, false
	}

	if time.Now().After(entry.expiresAt) {
		return nil, false
	}

	return entry.key, true
}

// Set stores a key in the cache
func (c *keyCache) Set(workerID string, key *Key) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[workerID] = &cacheEntry{
		key:       key,
		expiresAt: time.Now().Add(c.ttl),
	}
}

// Delete removes a key from the cache
func (c *keyCache) Delete(workerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, workerID)
}

// Clear removes all entries from the cache
func (c *keyCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]*cacheEntry)
}

// IsInHoldDown checks if we're in hold-down period after a failure
func (c *keyCache) IsInHoldDown() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.lastFailure.IsZero() {
		return false
	}

	return time.Now().Before(c.lastFailure.Add(c.holdDown))
}

// MarkFailure records a failure and starts hold-down period
func (c *keyCache) MarkFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lastFailure = time.Now()
}

// Cleanup removes expired entries from the cache
func (c *keyCache) Cleanup() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	cleaned := 0

	for workerID, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, workerID)
			cleaned++
		}
	}

	return cleaned
}

// Size returns the number of entries in the cache
func (c *keyCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.entries)
}
