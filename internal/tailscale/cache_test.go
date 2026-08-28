package tailscale

import (
	"fmt"
	"testing"
	"time"
)

func TestNewKeyCache(t *testing.T) {
	ttl := 5 * time.Minute
	holdDown := 30 * time.Second

	cache := newKeyCache(ttl, holdDown)

	if cache == nil {
		t.Fatal("Expected non-nil cache")
	}
	if cache.ttl != ttl {
		t.Errorf("Expected TTL %v, got %v", ttl, cache.ttl)
	}
	if cache.holdDown != holdDown {
		t.Errorf("Expected hold-down %v, got %v", holdDown, cache.holdDown)
	}
	if cache.entries == nil {
		t.Error("Expected initialized entries map")
	}
}

func TestCacheGetSet(t *testing.T) {
	cache := newKeyCache(5*time.Minute, 30*time.Second)

	// Test get on empty cache
	key, ok := cache.Get("worker-1")
	if ok {
		t.Error("Expected false for non-existent key, got true")
	}
	if key != nil {
		t.Error("Expected nil key for non-existent entry")
	}

	// Test set and get
	testKey := &Key{
		ID:         "key-123",
		Key:        "tskey-auth-test",
		Expires:    time.Now().Add(90 * 24 * time.Hour),
		Revoked:    false,
	}

	cache.Set("worker-1", testKey)

	key, ok = cache.Get("worker-1")
	if !ok {
		t.Error("Expected true for existing key, got false")
	}
	if key == nil {
		t.Fatal("Expected non-nil key after set")
	}
	if key.ID != testKey.ID {
		t.Errorf("Expected key ID %s, got %s", testKey.ID, key.ID)
	}
	if key.Key != testKey.Key {
		t.Errorf("Expected key value %s, got %s", testKey.Key, key.Key)
	}
}

func TestCacheExpiry(t *testing.T) {
	shortTTL := 10 * time.Millisecond
	cache := newKeyCache(shortTTL, 30*time.Second)

	testKey := &Key{
		ID:      "key-123",
		Key:     "tskey-auth-test",
		Expires: time.Now().Add(90 * 24 * time.Hour),
	}

	cache.Set("worker-1", testKey)

	// Should be available immediately
	key, ok := cache.Get("worker-1")
	if !ok {
		t.Error("Expected key to be available immediately after set")
	}
	if key == nil {
		t.Fatal("Expected non-nil key")
	}

	// Wait for expiry
	time.Sleep(shortTTL + 10*time.Millisecond)

	// Should be expired
	key, ok = cache.Get("worker-1")
	if ok {
		t.Error("Expected false for expired key, got true")
	}
	if key != nil {
		t.Error("Expected nil key for expired entry")
	}
}

func TestCacheDelete(t *testing.T) {
	cache := newKeyCache(5*time.Minute, 30*time.Second)

	testKey := &Key{
		ID:   "key-123",
		Key:  "tskey-auth-test",
	}

	cache.Set("worker-1", testKey)

	// Verify it's there
	_, ok := cache.Get("worker-1")
	if !ok {
		t.Error("Expected key to exist before deletion")
	}

	// Delete it
	cache.Delete("worker-1")

	// Verify it's gone
	_, ok = cache.Get("worker-1")
	if ok {
		t.Error("Expected key to be deleted, but it still exists")
	}
}

func TestCacheClear(t *testing.T) {
	cache := newKeyCache(5*time.Minute, 30*time.Second)

	// Add multiple entries
	for i := 1; i <= 5; i++ {
		cache.Set(
			fmt.Sprintf("worker-%d", i),
			&Key{ID: fmt.Sprintf("key-%d", i)},
		)
	}

	// Verify they exist
	if cache.Size() != 5 {
		t.Errorf("Expected 5 entries, got %d", cache.Size())
	}

	// Clear all
	cache.Clear()

	// Verify they're gone
	if cache.Size() != 0 {
		t.Errorf("Expected 0 entries after clear, got %d", cache.Size())
	}

	// Verify each key is gone
	for i := 1; i <= 5; i++ {
		_, ok := cache.Get(fmt.Sprintf("worker-%d", i))
		if ok {
			t.Error("Expected all keys to be cleared")
		}
	}
}

func TestCacheHoldDown(t *testing.T) {
	holdDown := 50 * time.Millisecond
	cache := newKeyCache(5*time.Minute, holdDown)

	// Initially not in hold-down
	if cache.IsInHoldDown() {
		t.Error("Expected not in hold-down initially")
	}

	// Mark failure
	cache.MarkFailure()

	// Now should be in hold-down
	if !cache.IsInHoldDown() {
		t.Error("Expected to be in hold-down after failure")
	}

	// Wait for hold-down to expire
	time.Sleep(holdDown + 10*time.Millisecond)

	// Should be out of hold-down
	if cache.IsInHoldDown() {
		t.Error("Expected to be out of hold-down after timeout")
	}
}

func TestCacheCleanup(t *testing.T) {
	shortTTL := 50 * time.Millisecond
	longTTL := 5 * time.Minute
	cache := newKeyCache(longTTL, 30*time.Second)

	// Add entries with different expiry times
	cache.Set("worker-1", &Key{ID: "key-1"})
	cache.Set("worker-2", &Key{ID: "key-2"})

	// Manually expire some entries by setting their expiry time
	cache.mu.Lock()
	cache.entries["worker-1"].expiresAt = time.Now().Add(-1 * time.Hour)
	cache.mu.Unlock()

	// Cleanup should remove expired entries
	cleaned := cache.Cleanup()
	if cleaned != 1 {
		t.Errorf("Expected 1 expired entry to be cleaned, got %d", cleaned)
	}

	// Verify expired entry is gone
	_, ok := cache.Get("worker-1")
	if ok {
		t.Error("Expected expired entry to be removed")
	}

	// Verify non-expired entry still exists
	_, ok = cache.Get("worker-2")
	if !ok {
		t.Error("Expected non-expired entry to still exist")
	}
}

func TestCacheSize(t *testing.T) {
	cache := newKeyCache(5*time.Minute, 30*time.Second)

	// Initially empty
	if cache.Size() != 0 {
		t.Errorf("Expected size 0, got %d", cache.Size())
	}

	// Add entries
	for i := 1; i <= 3; i++ {
		cache.Set(
			fmt.Sprintf("worker-%d", i),
			&Key{ID: fmt.Sprintf("key-%d", i)},
		)
	}

	// Check size
	if cache.Size() != 3 {
		t.Errorf("Expected size 3, got %d", cache.Size())
	}

	// Delete one entry
	cache.Delete("worker-1")

	// Check size decreased
	if cache.Size() != 2 {
		t.Errorf("Expected size 2 after deletion, got %d", cache.Size())
	}

	// Clear all
	cache.Clear()

	// Check size is 0
	if cache.Size() != 0 {
		t.Errorf("Expected size 0 after clear, got %d", cache.Size())
	}
}

func TestCacheMultipleWorkers(t *testing.T) {
	cache := newKeyCache(5*time.Minute, 30*time.Second)

	// Add entries for multiple workers
	workers := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	for _, worker := range workers {
		cache.Set(worker, &Key{
			ID:  "key-" + worker,
			Key: "tskey-auth-" + worker,
		})
	}

	// Verify all entries exist
	if cache.Size() != len(workers) {
		t.Errorf("Expected %d entries, got %d", len(workers), cache.Size())
	}

	// Verify each worker can retrieve their key
	for _, worker := range workers {
		key, ok := cache.Get(worker)
		if !ok {
			t.Errorf("Worker %s: expected key to exist", worker)
		}
		if key == nil {
			t.Fatalf("Worker %s: expected non-nil key", worker)
		}
		if key.ID != "key-"+worker {
			t.Errorf("Worker %s: expected key ID %s, got %s", worker, "key-"+worker, key.ID)
		}
	}

	// Delete specific worker
	cache.Delete("bravo")

	// Verify it's gone but others remain
	_, ok := cache.Get("bravo")
	if ok {
		t.Error("Expected bravo to be deleted")
	}

	if cache.Size() != len(workers)-1 {
		t.Errorf("Expected %d entries after deletion, got %d", len(workers)-1, cache.Size())
	}

	// Verify others still exist
	for _, worker := range []string{"alpha", "charlie", "delta", "echo"} {
		_, ok := cache.Get(worker)
		if !ok {
			t.Errorf("Expected %s to still exist after bravo deletion", worker)
		}
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	cache := newKeyCache(5*time.Minute, 30*time.Second)
	done := make(chan bool)

	// Concurrent writers
	for i := 0; i < 10; i++ {
		go func(workerID string) {
			for j := 0; j < 100; j++ {
				cache.Set(workerID, &Key{
					ID:  workerID + "-key",
					Key: "tskey-auth-" + workerID,
				})
			}
			done <- true
		}(fmt.Sprintf("writer-%d", i))
	}

	// Concurrent readers
	for i := 0; i < 10; i++ {
		go func(workerID string) {
			for j := 0; j < 100; j++ {
				cache.Get(workerID)
				cache.Get("nonexistent")
			}
			done <- true
		}(fmt.Sprintf("writer-%d", i))
	}

	// Concurrent deleters
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				cache.Delete("writer-0")
				cache.Delete("nonexistent")
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 25; i++ {
		<-done
	}

	// Final size check (should be between 0 and 10, depending on race conditions)
	size := cache.Size()
	if size < 0 || size > 10 {
		t.Errorf("Unexpected cache size after concurrent operations: %d", size)
	}
}

func TestCacheOverwrite(t *testing.T) {
	cache := newKeyCache(5*time.Minute, 30*time.Second)

	// Set initial key
	cache.Set("worker-1", &Key{
		ID:  "key-v1",
		Key: "tskey-auth-v1",
	})

	// Overwrite with new key
	cache.Set("worker-1", &Key{
		ID:  "key-v2",
		Key: "tskey-auth-v2",
	})

	// Should get the new key
	key, ok := cache.Get("worker-1")
	if !ok {
		t.Fatal("Expected key to exist after overwrite")
	}
	if key.ID != "key-v2" {
		t.Errorf("Expected overwritten key ID, got %s", key.ID)
	}
	if key.Key != "tskey-auth-v2" {
		t.Errorf("Expected overwritten key value, got %s", key.Key)
	}

	// Size should still be 1
	if cache.Size() != 1 {
		t.Errorf("Expected size 1 after overwrite, got %d", cache.Size())
	}
}
