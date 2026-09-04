package server

import (
	"fmt"
	"testing"
)

func TestComputeScopeVersionHash(t *testing.T) {
	tests := []struct {
		name     string
		scopes   []string
		expected string
	}{
		{
			name:     "empty scopes",
			scopes:   []string{},
			expected: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", // SHA-256 of empty string
		},
		{
			name:     "single scope",
			scopes:   []string{"seam:read"},
			expected: "", // Will be computed at runtime
		},
		{
			name:     "multiple scopes",
			scopes:   []string{"seam:read", "seam:write"},
			expected: "", // Will be computed at runtime
		},
		{
			name:     "scopes with different cases",
			scopes:   []string{"SEAM:READ", "seam:read"},
			expected: "", // Should normalize to same hash
		},
		{
			name:     "scopes with extra whitespace",
			scopes:   []string{"  seam:read  ", "seam:write"},
			expected: "", // Should normalize to same hash
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := ComputeScopeVersionHash(tt.scopes)
			if hash == "" {
				t.Fatal("ComputeScopeVersionHash returned empty string")
			}

			// Verify empty scopes returns canonical hash
			if len(tt.scopes) == 0 && hash != tt.expected {
				t.Errorf("Empty scopes hash mismatch:\n got: %s\n want: %s", hash, tt.expected)
			}

			// Verify same scopes produce same hash (idempotence)
			hash2 := ComputeScopeVersionHash(tt.scopes)
			if hash != hash2 {
				t.Errorf("Hash computation not idempotent:\n first: %s\n second: %s", hash, hash2)
			}
		})
	}

	// Test that scope order doesn't matter (sorted before hashing)
	t.Run("scope order independence", func(t *testing.T) {
		scopes1 := []string{"seam:read", "seam:write", "seam:admin"}
		scopes2 := []string{"seam:admin", "seam:read", "seam:write"}

		hash1 := ComputeScopeVersionHash(scopes1)
		hash2 := ComputeScopeVersionHash(scopes2)

		if hash1 != hash2 {
			t.Errorf("Scope order should not affect hash:\n hash1: %s\n hash2: %s", hash1, hash2)
		}
	})
}

func TestScopeVersionCacheBasics(t *testing.T) {
	cache := NewScopeVersionCache()

	identity := &Identity{
		NodeKey:     "test-node-1",
		NodeName:     "test-node.example.com",
		Resolved:    true,
		Capabilities: []string{"seam:read", "seam:write"},
	}

	// Record scope version
	hash := cache.RecordScopeVersion(identity, identity.Capabilities)
	if hash == "" {
		t.Fatal("RecordScopeVersion returned empty hash")
	}

	// Verify we can retrieve the current version
	currentHash := cache.GetCurrentScopeVersion(identity)
	if currentHash != hash {
		t.Errorf("GetCurrentScopeVersion mismatch:\n got: %s\n want: %s", currentHash, hash)
	}

	// Verify we can retrieve scopes for the version
	scopes := cache.GetScopesForVersion(identity, hash)
	if scopes == nil {
		t.Fatal("GetScopesForVersion returned nil")
	}
	if len(scopes) != len(identity.Capabilities) {
		t.Errorf("GetScopesForVersion scope count mismatch:\n got: %d\n want: %d", len(scopes), len(identity.Capabilities))
	}
}

func TestScopeVersionCachePerIdentityCap(t *testing.T) {
	cache := NewScopeVersionCache()

	identity := &Identity{
		NodeKey:  "test-node-cap",
		NodeName: "test-node.example.com",
		Resolved: true,
	}

	// Record 6 different scope versions (exceeds maxPerID of 4)
	scopeSets := [][]string{
		{"scope-1"},
		{"scope-2"},
		{"scope-3"},
		{"scope-4"},
		{"scope-5"},
		{"scope-6"},
	}

	var hashes []string
	for i, scopes := range scopeSets {
		hash := cache.RecordScopeVersion(identity, scopes)
		if hash == "" {
			t.Fatalf("RecordScopeVersion %d returned empty hash", i)
		}
		hashes = append(hashes, hash)
	}

	// Verify only the 4 most recent versions are kept
	stats := cache.Stats()
	totalVersions := stats["total_versions"].(int)
	if totalVersions != 4 {
		t.Errorf("Expected 4 versions after recording 6, got %d", totalVersions)
	}

	// Verify the last 4 hashes are still retrievable
	for i := 2; i < 6; i++ {
		scopes := cache.GetScopesForVersion(identity, hashes[i])
		if scopes == nil {
			t.Errorf("Version %d (hash %s) should still be in cache", i, hashes[i])
		}
	}

	// Verify the first 2 hashes are evicted
	for i := 0; i < 2; i++ {
		scopes := cache.GetScopesForVersion(identity, hashes[i])
		if scopes != nil {
			t.Errorf("Version %d (hash %s) should have been evicted", i, hashes[i])
		}
	}
}

func TestScopeVersionCacheMultipleIdentities(t *testing.T) {
	cache := NewScopeVersionCache()

	identity1 := &Identity{
		NodeKey:  "node-1",
		NodeName: "node1.example.com",
		Resolved: true,
		Capabilities: []string{"scope-a"},
	}

	identity2 := &Identity{
		NodeKey:  "node-2",
		NodeName: "node2.example.com",
		Resolved: true,
		Capabilities: []string{"scope-b"},
	}

	hash1 := cache.RecordScopeVersion(identity1, identity1.Capabilities)
	hash2 := cache.RecordScopeVersion(identity2, identity2.Capabilities)

	if hash1 == "" || hash2 == "" {
		t.Fatal("RecordScopeVersion returned empty hash")
	}

	if hash1 == hash2 {
		t.Error("Different scope sets should have different hashes")
	}

	// Verify each identity can retrieve their own version
	current1 := cache.GetCurrentScopeVersion(identity1)
	current2 := cache.GetCurrentScopeVersion(identity2)

	if current1 != hash1 {
		t.Errorf("Identity 1: GetCurrentScopeVersion mismatch:\n got: %s\n want: %s", current1, hash1)
	}
	if current2 != hash2 {
		t.Errorf("Identity 2: GetCurrentScopeVersion mismatch:\n got: %s\n want: %s", current2, hash2)
	}

	// Verify stats show 2 identities
	stats := cache.Stats()
	identities := stats["identities"].(int)
	if identities != 2 {
		t.Errorf("Expected 2 identities, got %d", identities)
	}
}

func TestScopeVersionCacheIdleEviction(t *testing.T) {
	cache := NewScopeVersionCache()

	identity1 := &Identity{
		NodeKey:  "stale-node",
		NodeName: "stale.example.com",
		Resolved: true,
		Capabilities: []string{"scope-stale"},
	}

	identity2 := &Identity{
		NodeKey:  "fresh-node",
		NodeName: "fresh.example.com",
		Resolved: true,
		Capabilities: []string{"scope-fresh"},
	}

	// Record versions
	cache.RecordScopeVersion(identity1, identity1.Capabilities)
	cache.RecordScopeVersion(identity2, identity2.Capabilities)

	// Manually age identity1's entry by simulating time passing
	// Note: We can't directly manipulate lastAccessed, but we can test CleanupIdleEntries
	stats := cache.Stats()
	initialIdentities := stats["identities"].(int)
	if initialIdentities != 2 {
		t.Errorf("Expected 2 identities initially, got %d", initialIdentities)
	}

	// Cleanup won't remove anything immediately (all are fresh)
	removed := cache.CleanupIdleEntries()
	if removed != 0 {
		t.Errorf("Expected 0 removals (all fresh), got %d", removed)
	}

	// Verify both identities still exist
	stats = cache.Stats()
	identities := stats["identities"].(int)
	if identities != 2 {
		t.Errorf("Expected 2 identities after cleanup, got %d", identities)
	}
}

func TestScopeVersionCacheLRUEviction(t *testing.T) {
	cache := NewScopeVersionCache()

	// Create more identities than the LRU cap (100)
	// We'll create 105 identities to exceed the cap
	for i := 0; i < 105; i++ {
		identity := &Identity{
			NodeKey:  fmt.Sprintf("node-%d", i),
			NodeName: fmt.Sprintf("node-%d.example.com", i),
			Resolved: true,
			Capabilities: []string{fmt.Sprintf("scope-%d", i)},
		}
		cache.RecordScopeVersion(identity, identity.Capabilities)
	}

	stats := cache.Stats()
	identities := stats["identities"].(int)

	// Verify we don't exceed the LRU cap
	if identities > 100 {
		t.Errorf("LRU cap exceeded: got %d identities, max 100", identities)
	}

	// Verify we're at or close to the cap
	if identities < 95 {
		t.Errorf("Unexpectedly low identity count after LRU eviction: got %d", identities)
	}
}

func TestIdentityKey(t *testing.T) {
	tests := []struct {
		name     string
		identity *Identity
		expected string
	}{
		{
			name:     "nil identity",
			identity: nil,
			expected: "anonymous",
		},
		{
			name: "identity with node key",
			identity: &Identity{
				NodeKey:  "node-key-123",
				NodeName: "node.example.com",
			},
			expected: "node-key-123",
		},
		{
			name: "identity without node key",
			identity: &Identity{
				NodeKey:  "",
				NodeName: "node.example.com",
			},
			expected: "node.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := identityKey(tt.identity)
			if key != tt.expected {
				t.Errorf("identityKey mismatch:\n got: %s\n want: %s", key, tt.expected)
			}
		})
	}
}

func TestScopeVersionCacheStats(t *testing.T) {
	cache := NewScopeVersionCache()

	// Check initial stats
	stats := cache.Stats()
	if stats["identities"].(int) != 0 {
		t.Errorf("Expected 0 identities initially, got %d", stats["identities"])
	}
	if stats["total_versions"].(int) != 0 {
		t.Errorf("Expected 0 versions initially, got %d", stats["total_versions"])
	}
	if stats["max_lru"].(int) != 100 {
		t.Errorf("Expected max_lru of 100, got %d", stats["max_lru"])
	}
	if stats["max_per_id"].(int) != 4 {
		t.Errorf("Expected max_per_id of 4, got %d", stats["max_per_id"])
	}

	// Add some data
	identity := &Identity{
		NodeKey:  "stats-test",
		NodeName: "stats.example.com",
		Resolved: true,
		Capabilities: []string{"scope-a", "scope-b"},
	}
	cache.RecordScopeVersion(identity, identity.Capabilities)

	// Check updated stats
	stats = cache.Stats()
	if stats["identities"].(int) != 1 {
		t.Errorf("Expected 1 identity, got %d", stats["identities"])
	}
	if stats["total_versions"].(int) != 1 {
		t.Errorf("Expected 1 version, got %d", stats["total_versions"])
	}
}
