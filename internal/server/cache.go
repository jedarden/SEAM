package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// CacheEntry represents a cached response with expiration time
type CacheEntry struct {
	Response   *cachedResponse
	ExpiresAt  time.Time
	TTLSeconds int
	CreatedAt  time.Time
}

// cachedResponse stores the HTTP response data for caching
type cachedResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// CacheKey represents the normalized cache key for a request
type CacheKey string

// ResponseCache is an in-memory cache with TTL-based expiration
type ResponseCache struct {
	mu    sync.RWMutex
	store map[CacheKey]*CacheEntry

	// Metrics
	hits   int64
	misses int64
	evictions int64
}

// NewResponseCache creates a new response cache
func NewResponseCache() *ResponseCache {
	return &ResponseCache{
		store: make(map[CacheKey]*CacheEntry),
	}
}

// Get retrieves a cached response if it exists and hasn't expired
func (c *ResponseCache) Get(key CacheKey) (*cachedResponse, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.store[key]
	if !exists {
		c.misses++
		return nil, false
	}

	// Check if expired
	if time.Now().After(entry.ExpiresAt) {
		// Entry expired, will be cleaned up on next cleanup pass
		c.misses++
		return nil, false
	}

	c.hits++
	return entry.Response, true
}

// Set stores a response in the cache with the given TTL
func (c *ResponseCache) Set(key CacheKey, response *cachedResponse, ttlSeconds int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Never cache 5xx responses - they evict existing entries
	if response.StatusCode >= 500 {
		if _, exists := c.store[key]; exists {
			delete(c.store, key)
			c.evictions++
		}
		return
	}

	// 4xx responses are cached normally
	now := time.Now()
	expiresAt := now.Add(time.Duration(ttlSeconds) * time.Second)

	c.store[key] = &CacheEntry{
		Response:   response,
		ExpiresAt:  expiresAt,
		TTLSeconds: ttlSeconds,
		CreatedAt:  now,
	}
}

// Delete removes a cache entry
func (c *ResponseCache) Delete(key CacheKey) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.store[key]; exists {
		delete(c.store, key)
		c.evictions++
	}
}

// Cleanup removes expired entries from the cache
// Should be called periodically (e.g., every minute)
func (c *ResponseCache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.store {
		if now.After(entry.ExpiresAt) {
			delete(c.store, key)
			c.evictions++
		}
	}
}

// Stats returns cache statistics
func (c *ResponseCache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return CacheStats{
		Size:      len(c.store),
		Hits:      c.hits,
		Misses:    c.misses,
		Evictions: c.evictions,
	}
}

// CacheStats holds cache statistics
type CacheStats struct {
	Size      int
	Hits      int64
	Misses    int64
	Evictions int64
}

// GenerateCacheKey creates a normalized cache key from the request
// Key format: route + normalized query parameters (sorted alphabetically)
func GenerateCacheKey(method, path string, query url.Values) CacheKey {
	// Normalize query parameters - sort keys alphabetically
	// This ensures that ?a=1&b=2 and ?b=2&a=1 produce the same key
	var normalizedQuery strings.Builder

	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}

	// Simple bubble sort for keys (small number of query params typically)
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}

	if len(keys) > 0 {
		normalizedQuery.WriteString("?")
		for i, k := range keys {
			if i > 0 {
				normalizedQuery.WriteString("&")
			}
			normalizedQuery.WriteString(k)
			normalizedQuery.WriteString("=")
			normalizedQuery.WriteString(query.Get(k))
		}
	}

	// Create hash for the key components
	keyData := fmt.Sprintf("%s:%s%s", method, path, normalizedQuery.String())
	hash := sha256.Sum256([]byte(keyData))

	return CacheKey(hex.EncodeToString(hash[:]))
}

// ShouldUseCache determines if a request should be cached
// Only GET requests are cached; other methods always bypass
func ShouldUseCache(r *http.Request) bool {
	return r.Method == http.MethodGet
}

// GetCacheTTL extracts the x-cache-ttl from a fragment's parsed content
// Returns 0 if not set (meaning no caching)
func GetCacheTTL(fragmentData map[string]interface{}) int {
	if fragmentData == nil {
		return 0
	}

	ttlValue, exists := fragmentData["x-cache-ttl"]
	if !exists {
		return 0
	}

	// Convert to int based on actual type
	switch v := ttlValue.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}
