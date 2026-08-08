package server

import (
	"fmt"
	"net/url"
	"testing"
	"time"
)

// TestGenerateCacheKey_Basic tests basic cache key generation
func TestGenerateCacheKey_Basic(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		path           string
		query          url.Values
		wantConsistent bool // If multiple calls with same params should produce same key
	}{
		{
			name:           "simple GET request",
			method:         "GET",
			path:           "/api/users",
			query:          url.Values{},
			wantConsistent: true,
		},
		{
			name:           "GET with single query param",
			method:         "GET",
			path:           "/api/users",
			query:          url.Values{"page": []string{"1"}},
			wantConsistent: true,
		},
		{
			name:           "POST request",
			method:         "POST",
			path:           "/api/users",
			query:          url.Values{},
			wantConsistent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key1 := GenerateCacheKey(tt.method, tt.path, tt.query)
			key2 := GenerateCacheKey(tt.method, tt.path, tt.query)

			if tt.wantConsistent && key1 != key2 {
				t.Errorf("GenerateCacheKey() not deterministic: got %v and %v", key1, key2)
			}

			if key1 == "" {
				t.Errorf("GenerateCacheKey() returned empty key")
			}
		})
	}
}

// TestGenerateCacheKey_QueryParamOrder tests that query param order doesn't affect key
func TestGenerateCacheKey_QueryParamOrder(t *testing.T) {
	query1 := url.Values{
		"a": []string{"1"},
		"b": []string{"2"},
		"c": []string{"3"},
	}

	query2 := url.Values{
		"c": []string{"3"},
		"a": []string{"1"},
		"b": []string{"2"},
	}

	query3 := url.Values{
		"b": []string{"2"},
		"a": []string{"1"},
		"c": []string{"3"},
	}

	key1 := GenerateCacheKey("GET", "/api/test", query1)
	key2 := GenerateCacheKey("GET", "/api/test", query2)
	key3 := GenerateCacheKey("GET", "/api/test", query3)

	if key1 != key2 || key1 != key3 {
		t.Errorf("Query param order should not affect cache key: got %v, %v, %v", key1, key2, key3)
	}
}

// TestGenerateCacheKey_QueryParamValues tests that different param values produce different keys
func TestGenerateCacheKey_QueryParamValues(t *testing.T) {
	query1 := url.Values{"page": []string{"1"}}
	query2 := url.Values{"page": []string{"2"}}
	query3 := url.Values{"page": []string{"10"}}

	key1 := GenerateCacheKey("GET", "/api/users", query1)
	key2 := GenerateCacheKey("GET", "/api/users", query2)
	key3 := GenerateCacheKey("GET", "/api/users", query3)

	if key1 == key2 {
		t.Errorf("Different query param values should produce different keys: %v == %v", key1, key2)
	}
	if key1 == key3 {
		t.Errorf("Different query param values should produce different keys: %v == %v", key1, key3)
	}
	if key2 == key3 {
		t.Errorf("Different query param values should produce different keys: %v == %v", key2, key3)
	}
}

// TestGenerateCacheKey_EmptyQueryParams tests with no query parameters
func TestGenerateCacheKey_EmptyQueryParams(t *testing.T) {
	query1 := url.Values{}
	query2 := url.Values{}

	key1 := GenerateCacheKey("GET", "/api/users", query1)
	key2 := GenerateCacheKey("GET", "/api/users", query2)

	if key1 != key2 {
		t.Errorf("Empty query params should be deterministic: got %v and %v", key1, key2)
	}
}

// TestGenerateCacheKey_MultipleParams tests with multiple query parameters
func TestGenerateCacheKey_MultipleParams(t *testing.T) {
	query1 := url.Values{
		"filter":  []string{"active"},
		"sort":    []string{"name"},
		"page":    []string{"2"},
		"limit":   []string{"50"},
		"search": []string{"test"},
	}

	query2 := url.Values{
		"search":  []string{"test"},
		"limit":   []string{"50"},
		"page":    []string{"2"},
		"sort":    []string{"name"},
		"filter":  []string{"active"},
	}

	key1 := GenerateCacheKey("GET", "/api/items", query1)
	key2 := GenerateCacheKey("GET", "/api/items", query2)

	if key1 != key2 {
		t.Errorf("Multiple params in different orders should produce same key: got %v and %v", key1, key2)
	}
}

// TestGenerateCacheKey_CaseSensitivity tests case sensitivity of paths and params
func TestGenerateCacheKey_CaseSensitivity(t *testing.T) {
	query1 := url.Values{"Name": []string{"John"}}
	query2 := url.Values{"name": []string{"John"}}

	key1 := GenerateCacheKey("GET", "/api/Users", query1)
	key2 := GenerateCacheKey("GET", "/api/users", query2)

	if key1 == key2 {
		t.Errorf("Paths and param keys should be case-sensitive: got %v for both", key1)
	}
}

// TestGenerateCacheKey_DifferentPaths tests that different paths produce different keys
func TestGenerateCacheKey_DifferentPaths(t *testing.T) {
	query := url.Values{"id": []string{"123"}}

	key1 := GenerateCacheKey("GET", "/api/users/123", query)
	key2 := GenerateCacheKey("GET", "/api/posts/123", query)
	key3 := GenerateCacheKey("GET", "/api/users/456", query)

	if key1 == key2 {
		t.Errorf("Different paths should produce different keys: %v == %v", key1, key2)
	}
	if key1 == key3 {
		t.Errorf("Different paths should produce different keys: %v == %v", key1, key3)
	}
}

// TestGenerateCacheKey_DifferentMethods tests that different HTTP methods produce different keys
func TestGenerateCacheKey_DifferentMethods(t *testing.T) {
	query := url.Values{}

	key1 := GenerateCacheKey("GET", "/api/users", query)
	key2 := GenerateCacheKey("POST", "/api/users", query)
	key3 := GenerateCacheKey("PUT", "/api/users", query)

	if key1 == key2 {
		t.Errorf("Different HTTP methods should produce different keys: GET %v == POST %v", key1, key2)
	}
	if key1 == key3 {
		t.Errorf("Different HTTP methods should produce different keys: GET %v == PUT %v", key1, key3)
	}
}

// TestGenerateCacheKey_SpecialCharacters tests special characters in params
func TestGenerateCacheKey_SpecialCharacters(t *testing.T) {
	tests := []struct {
		name   string
		query  url.Values
		expect string
	}{
		{
			name:   "URL encoded spaces",
			query:  url.Values{"search": []string{"hello world"}},
			expect: "consistent",
		},
		{
			name:   "special characters",
			query:  url.Values{"filter": []string{"price>=100&price<=500"}},
			expect: "consistent",
		},
		{
			name:   "unicode characters",
			query:  url.Values{"name": []string{"José"}},

			expect: "consistent",
		},
		{
			name:   "email addresses",
			query:  url.Values{"email": []string{"user@example.com"}},
			expect: "consistent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key1 := GenerateCacheKey("GET", "/api/search", tt.query)
			key2 := GenerateCacheKey("GET", "/api/search", tt.query)

			if key1 != key2 {
				t.Errorf("Special characters should be handled consistently: got %v and %v", key1, key2)
			}
		})
	}
}

// TestGenerateCacheKey_MultiValuedParams tests query params with multiple values
func TestGenerateCacheKey_MultiValuedParams(t *testing.T) {
	query1 := url.Values{"tags": []string{"rust", "go", "python"}}
	query2 := url.Values{"tags": []string{"go", "rust", "python"}}

	key1 := GenerateCacheKey("GET", "/api/posts", query1)
	key2 := GenerateCacheKey("GET", "/api/posts", query2)

	// Note: url.Values.Get() only returns the first value, so these will have same keys
	// This test documents the current behavior
	if key1 != key2 {
		t.Logf("Multi-valued params order may affect key (current behavior): %v vs %v", key1, key2)
	}
}

// TestCacheEntry_Structure tests the CacheEntry struct has required fields
func TestCacheEntry_Structure(t *testing.T) {
	// Verify CacheEntry can be created with all required fields
	now := testTime()
	entry := &CacheEntry{
		Response: &cachedResponse{
			StatusCode: 200,
			Header:     make(map[string][]string),
			Body:       []byte("test response"),
		},
		ExpiresAt:  now.Add(5 * time.Minute),
		TTLSeconds: 300,
		CreatedAt:  now,
	}

	if entry.Response == nil {
		t.Errorf("CacheEntry.Response field is required")
	}
	if entry.TTLSeconds == 0 {
		t.Errorf("CacheEntry.TTLSeconds field is required")
	}
	if entry.ExpiresAt.IsZero() {
		t.Errorf("CacheEntry.ExpiresAt field is required")
	}
}

// TestCacheKey_Type tests CacheKey type is a string alias
func TestCacheKey_Type(t *testing.T) {
	var key CacheKey = "test-key"

	if key != "test-key" {
		t.Errorf("CacheKey should be a string type")
	}

	// Verify it can be used as map key
	m := make(map[CacheKey]string)
	m[key] = "value"

	if m[key] != "value" {
		t.Errorf("CacheKey should be usable as map key")
	}
}

// TestCacheEntry_CreatedAt tests that CreatedAt is set when entries are added
func TestCacheEntry_CreatedAt(t *testing.T) {
	cache := NewResponseCache()

	response := &cachedResponse{
		StatusCode: 200,
		Header:     make(map[string][]string),
		Body:       []byte("test response"),
	}

	key := CacheKey("test-key")

	// Set the cache entry
	beforeSet := time.Now()
	cache.Set(key, response, 300)
	afterSet := time.Now()

	// Retrieve the entry to verify CreatedAt was set
	// Note: We need to access the internal store for this test
	cache.mu.RLock()
	entry, exists := cache.store[key]
	cache.mu.RUnlock()

	if !exists {
		t.Errorf("Cache entry was not created")
		return
	}

	if entry.CreatedAt.IsZero() {
		t.Errorf("CreatedAt field was not set")
	}

	if entry.CreatedAt.Before(beforeSet) || entry.CreatedAt.After(afterSet) {
		t.Errorf("CreatedAt should be between beforeSet and afterSet: got %v, expected between %v and %v",
			entry.CreatedAt, beforeSet, afterSet)
	}

	// Verify ExpiresAt is CreatedAt + TTL
	expectedExpiresAt := entry.CreatedAt.Add(time.Duration(entry.TTLSeconds) * time.Second)
	if entry.ExpiresAt.Unix() != expectedExpiresAt.Unix() {
		t.Errorf("ExpiresAt should be CreatedAt + TTL: got %v, expected %v",
			entry.ExpiresAt, expectedExpiresAt)
	}
}

// testTime returns a fixed time for testing
func testTime() time.Time {
	return time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
}

// TestCache_ConcurrentReads tests that multiple goroutines can read simultaneously without blocking
func TestCache_ConcurrentReads(t *testing.T) {
	cache := NewResponseCache()

	// Pre-populate cache with entries
	for i := 0; i < 100; i++ {
		key := CacheKey(fmt.Sprintf("key-%d", i))
		response := &cachedResponse{
			StatusCode: 200,
			Header:     make(map[string][]string),
			Body:       []byte(fmt.Sprintf("response-%d", i)),
		}
		cache.Set(key, response, 300)
	}

	// Launch many concurrent readers
	numGoroutines := 100
	readsPerGoroutine := 100
	done := make(chan bool, numGoroutines)
	errors := make(chan error, numGoroutines*readsPerGoroutine)

	start := make(chan struct{})

	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			<-start // Wait for signal to start all goroutines at once

			for i := 0; i < readsPerGoroutine; i++ {
				key := CacheKey(fmt.Sprintf("key-%d", i%100))
				response, found := cache.Get(key)

				if !found {
					errors <- fmt.Errorf("goroutine %d: key %s not found", goroutineID, key)
					return
				}

				if response == nil {
					errors <- fmt.Errorf("goroutine %d: got nil response for key %s", goroutineID, key)
					return
				}

				expectedBody := fmt.Sprintf("response-%d", i%100)
				if string(response.Body) != expectedBody {
					errors <- fmt.Errorf("goroutine %d: body mismatch for key %s: got %s, want %s",
						goroutineID, key, response.Body, expectedBody)
					return
				}
			}

			done <- true
		}(g)
	}

	// Start all goroutines simultaneously
	close(start)

	// Wait for all goroutines to complete with timeout
	timeout := time.After(10 * time.Second)
	completed := 0
	for completed < numGoroutines {
		select {
		case <-done:
			completed++
		case err := <-errors:
			t.Fatalf("Concurrent read error: %v", err)
		case <-timeout:
			t.Fatalf("Timeout waiting for concurrent reads to complete (%d/%d done)", completed, numGoroutines)
		}
	}

	t.Logf("Successfully completed %d concurrent read operations (%d goroutines × %d reads each)",
		numGoroutines*readsPerGoroutine, numGoroutines, readsPerGoroutine)
}

// TestCache_ConcurrentWrites tests that multiple goroutines can write safely without data races
func TestCache_ConcurrentWrites(t *testing.T) {
	cache := NewResponseCache()

	numGoroutines := 50
	writesPerGoroutine := 50
	done := make(chan bool, numGoroutines)
	start := make(chan struct{})

	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			<-start // Wait for signal to start all goroutines at once

			for i := 0; i < writesPerGoroutine; i++ {
				key := CacheKey(fmt.Sprintf("writer-%d-key-%d", goroutineID, i))
				response := &cachedResponse{
					StatusCode: 200,
					Header:     make(map[string][]string),
					Body:       []byte(fmt.Sprintf("writer-%d-response-%d", goroutineID, i)),
				}
				cache.Set(key, response, 300)
			}

			done <- true
		}(g)
	}

	close(start)

	// Wait for completion with timeout
	timeout := time.After(10 * time.Second)
	completed := 0
	for completed < numGoroutines {
		select {
		case <-done:
			completed++
		case <-timeout:
			t.Fatalf("Timeout waiting for concurrent writes to complete (%d/%d done)", completed, numGoroutines)
		}
	}

	// Verify all entries were written correctly
	expectedCount := numGoroutines * writesPerGoroutine
	stats := cache.Stats()
	if stats.Size != expectedCount {
		t.Errorf("Expected %d entries in cache, got %d", expectedCount, stats.Size)
	}

	// Verify a sample of entries
	for g := 0; g < numGoroutines; g++ {
		for i := 0; i < writesPerGoroutine; i += 10 { // Check every 10th entry
			key := CacheKey(fmt.Sprintf("writer-%d-key-%d", g, i))
			response, found := cache.Get(key)

			if !found {
				t.Errorf("Entry not found: %s", key)
				continue
			}

			expectedBody := fmt.Sprintf("writer-%d-response-%d", g, i)
			if string(response.Body) != expectedBody {
				t.Errorf("Body mismatch for key %s: got %s, want %s", key, response.Body, expectedBody)
			}
		}
	}

	t.Logf("Successfully completed %d concurrent write operations (%d goroutines × %d writes each)",
		numGoroutines*writesPerGoroutine, numGoroutines, writesPerGoroutine)
}

// TestCache_ConcurrentReadsAndWrites tests mixed concurrent access pattern
func TestCache_ConcurrentReadsAndWrites(t *testing.T) {
	cache := NewResponseCache()

	// Pre-populate some initial data
	for i := 0; i < 50; i++ {
		key := CacheKey(fmt.Sprintf("initial-key-%d", i))
		response := &cachedResponse{
			StatusCode: 200,
			Header:     make(map[string][]string),
			Body:       []byte(fmt.Sprintf("initial-response-%d", i)),
		}
		cache.Set(key, response, 600)
	}

	done := make(chan bool, 3) // 3 types of operations: readers, writers, deleters
	start := make(chan struct{})
	errors := make(chan error, 100)

	// Launch readers
	numReaders := 20
	readsPerReader := 100
	for r := 0; r < numReaders; r++ {
		go func(readerID int) {
			<-start

			for i := 0; i < readsPerReader; i++ {
				// Read from different key ranges
				keyNum := (readerID + i) % 100
				key := CacheKey(fmt.Sprintf("key-%d", keyNum))
				_, found := cache.Get(key)

				// We don't care if found or not - just that reads don't crash
				_ = found
			}

			done <- true
		}(r)
	}

	// Launch writers
	numWriters := 10
	writesPerWriter := 50
	for w := 0; w < numWriters; w++ {
		go func(writerID int) {
			<-start

			for i := 0; i < writesPerWriter; i++ {
				key := CacheKey(fmt.Sprintf("writer-%d-key-%d", writerID, i))
				response := &cachedResponse{
					StatusCode: 200,
					Header:     make(map[string][]string),
					Body:       []byte(fmt.Sprintf("data-%d-%d", writerID, i)),
				}
				cache.Set(key, response, 300)
			}

			done <- true
		}(w)
	}

	// Launch deleters
	numDeleters := 5
	deletesPerDeleter := 20
	for d := 0; d < numDeleters; d++ {
		go func(deleterID int) {
			<-start

			for i := 0; i < deletesPerDeleter; i++ {
				// Try to delete some keys (may or may not exist)
				key := CacheKey(fmt.Sprintf("writer-%d-key-%d", i%numWriters, i))
				cache.Delete(key)
			}

			done <- true
		}(d)
	}

	close(start)

	totalOperations := numReaders + numWriters + numDeleters
	completed := 0
	timeout := time.After(15 * time.Second)

	for completed < totalOperations {
		select {
		case <-done:
			completed++
		case err := <-errors:
			t.Fatalf("Concurrent operation error: %v", err)
		case <-timeout:
			t.Fatalf("Timeout waiting for mixed concurrent operations to complete (%d/%d done)", completed, totalOperations)
		}
	}

	// Verify cache is still in consistent state
	stats := cache.Stats()
	t.Logf("Cache stats after mixed operations: %d entries, %d hits, %d misses",
		stats.Size, stats.Hits, stats.Misses)

	// Verify we can still read from the cache without issues
	for i := 0; i < 10; i++ {
		key := CacheKey(fmt.Sprintf("initial-key-%d", i))
		_, found := cache.Get(key)
		if !found {
			t.Logf("Initial key %s no longer in cache (may have been evicted or overwritten)", key)
		}
	}

	t.Logf("Successfully completed mixed concurrent operations: %d readers, %d writers, %d deleters",
		numReaders, numWriters, numDeleters)
}

// TestCache_ConcurrentDeletes tests concurrent delete operations
func TestCache_ConcurrentDeletes(t *testing.T) {
	cache := NewResponseCache()

	// Pre-populate cache
	numEntries := 200
	for i := 0; i < numEntries; i++ {
		key := CacheKey(fmt.Sprintf("delete-test-%d", i))
		response := &cachedResponse{
			StatusCode: 200,
			Header:     make(map[string][]string),
			Body:       []byte(fmt.Sprintf("data-%d", i)),
		}
		cache.Set(key, response, 300)
	}

	initialStats := cache.Stats()
	if initialStats.Size != numEntries {
		t.Errorf("Expected %d entries initially, got %d", numEntries, initialStats.Size)
	}

	numGoroutines := 20
	deletesPerGoroutine := 10
	done := make(chan bool, numGoroutines)
	start := make(chan struct{})

	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			<-start

			for i := 0; i < deletesPerGoroutine; i++ {
				// Each goroutine deletes from its own range to avoid overlap
				keyNum := goroutineID*deletesPerGoroutine + i
				if keyNum < numEntries {
					key := CacheKey(fmt.Sprintf("delete-test-%d", keyNum))
					cache.Delete(key)
				}
			}

			done <- true
		}(g)
	}

	close(start)

	// Wait for completion
	timeout := time.After(10 * time.Second)
	completed := 0
	for completed < numGoroutines {
		select {
		case <-done:
			completed++
		case <-timeout:
			t.Fatalf("Timeout waiting for concurrent deletes to complete (%d/%d done)", completed, numGoroutines)
		}
	}

	expectedDeletes := numGoroutines * deletesPerGoroutine
	expectedRemaining := numEntries - expectedDeletes

	finalStats := cache.Stats()
	if finalStats.Size != expectedRemaining {
		t.Errorf("Expected %d remaining entries, got %d", expectedRemaining, finalStats.Size)
	}

	// Verify deleted entries are gone
	for i := 0; i < expectedDeletes; i++ {
		key := CacheKey(fmt.Sprintf("delete-test-%d", i))
		_, found := cache.Get(key)
		if found {
			t.Errorf("Entry %s should have been deleted but still exists", key)
		}
	}

	// Verify remaining entries still exist
	for i := expectedDeletes; i < numEntries; i++ {
		key := CacheKey(fmt.Sprintf("delete-test-%d", i))
		_, found := cache.Get(key)
		if !found {
			t.Errorf("Entry %s should still exist but was deleted", key)
		}
	}

	t.Logf("Successfully completed %d concurrent delete operations", expectedDeletes)
}

// TestCache_NoRaceOnSameKey tests that concurrent operations on the same key are handled correctly
func TestCache_NoRaceOnSameKey(t *testing.T) {
	cache := NewResponseCache()

	key := CacheKey("shared-key")
	initialResponse := &cachedResponse{
		StatusCode: 200,
		Header:     make(map[string][]string),
		Body:       []byte("initial"),
	}
	cache.Set(key, initialResponse, 300)

	done := make(chan bool, 3)
	start := make(chan struct{})
	errors := make(chan error, 10)

	// Goroutine that constantly writes
	go func() {
		<-start
		for i := 0; i < 100; i++ {
			response := &cachedResponse{
				StatusCode: 200,
				Header:     make(map[string][]string),
				Body:       []byte(fmt.Sprintf("write-%d", i)),
			}
			cache.Set(key, response, 300)
		}
		done <- true
	}()

	// Goroutine that constantly reads
	go func() {
		<-start
		for i := 0; i < 100; i++ {
			response, found := cache.Get(key)
			// When concurrently deleting, it's normal for the key to not be found
			// The important thing is that when found, the response is valid
			if found && response == nil {
				errors <- fmt.Errorf("got nil response when key was found")
				return
			}
		}
		done <- true
	}()

	// Goroutine that constantly deletes
	go func() {
		<-start
		for i := 0; i < 50; i++ {
			cache.Delete(key)
		}
		done <- true
	}()

	close(start)

	totalGoroutines := 3
	completed := 0
	timeout := time.After(10 * time.Second)

	for completed < totalGoroutines {
		select {
		case <-done:
			completed++
		case err := <-errors:
			t.Fatalf("Error during concurrent same-key operations: %v", err)
		case <-timeout:
			t.Fatalf("Timeout waiting for same-key concurrent operations to complete")
		}
	}

	t.Log("No race conditions detected with concurrent reads/writes/deletes on same key")
}
