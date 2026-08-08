package server

import (
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
