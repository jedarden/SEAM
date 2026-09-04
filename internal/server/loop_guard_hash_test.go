package server

import (
	"net/url"
	"strings"
	"testing"
)

// TestLoopGuardHash_ReorderedKeysCollapse tests that JSON objects with
// reordered keys produce the same hash (RFC 8785 canonicalization).
func TestLoopGuardHash_ReorderedKeysCollapse(t *testing.T) {
	hasher := NewRequestHasher(1024)

	body1 := []byte(`{"name":"John","age":30,"city":"NYC"}`)
	body2 := []byte(`{"city":"NYC","age":30,"name":"John"}`)
	body3 := []byte(`{
		"name": "John",
		"age": 30,
		"city": "NYC"
	}`)

	hash1 := hasher.ComputeHash("POST", "/api/users", map[string]string{"id": "123"}, url.Values{}, body1)
	hash2 := hasher.ComputeHash("POST", "/api/users", map[string]string{"id": "123"}, url.Values{}, body2)
	hash3 := hasher.ComputeHash("POST", "/api/users", map[string]string{"id": "123"}, url.Values{}, body3)

	if hash1 != hash2 {
		t.Errorf("Hashes with reordered keys should match: got %s vs %s", hash1, hash2)
	}
	if hash1 != hash3 {
		t.Errorf("Hashes with whitespace should match: got %s vs %s", hash1, hash3)
	}
}

// TestLoopGuardHash_QueryParamOrderIndependence tests that query parameter
// order does not affect the hash.
func TestLoopGuardHash_QueryParamOrderIndependence(t *testing.T) {
	hasher := NewRequestHasher(1024)

	query1 := url.Values{}
	query1.Set("foo", "bar")
	query1.Set("baz", "qux")

	query2 := url.Values{}
	query2.Set("baz", "qux")
	query2.Set("foo", "bar")

	hash1 := hasher.ComputeHash("GET", "/api/search", map[string]string{}, query1, nil)
	hash2 := hasher.ComputeHash("GET", "/api/search", map[string]string{}, query2, nil)

	if hash1 != hash2 {
		t.Errorf("Hashes with reordered query params should match: got %s vs %s", hash1, hash2)
	}
}

// TestLoopGuardHash_DistinctBodiesDoNotCollide tests that different JSON
// bodies produce different hashes.
func TestLoopGuardHash_DistinctBodiesDoNotCollide(t *testing.T) {
	hasher := NewRequestHasher(1024)

	body1 := []byte(`{"name":"John","age":30}`)
	body2 := []byte(`{"name":"Jane","age":30}`)
	body3 := []byte(`{"name":"John","age":31}`)

	hash1 := hasher.ComputeHash("POST", "/api/users", map[string]string{}, url.Values{}, body1)
	hash2 := hasher.ComputeHash("POST", "/api/users", map[string]string{}, url.Values{}, body2)
	hash3 := hasher.ComputeHash("POST", "/api/users", map[string]string{}, url.Values{}, body3)

	if hash1 == hash2 {
		t.Errorf("Hashes with different values should not collide: both are %s", hash1)
	}
	if hash1 == hash3 {
		t.Errorf("Hashes with different values should not collide: both are %s", hash1)
	}
	if hash2 == hash3 {
		t.Errorf("Hashes with different values should not collide: both are %s", hash2)
	}
}

// TestLoopGuardHash_UppercasedMethod tests that method case is normalized.
func TestLoopGuardHash_UppercasedMethod(t *testing.T) {
	hasher := NewRequestHasher(1024)

	hash1 := hasher.ComputeHash("post", "/api/users", map[string]string{}, url.Values{}, nil)
	hash2 := hasher.ComputeHash("POST", "/api/users", map[string]string{}, url.Values{}, nil)
	hash3 := hasher.ComputeHash("pOsT", "/api/users", map[string]string{}, url.Values{}, nil)

	if hash1 != hash2 {
		t.Errorf("Hashes with different method cases should match: got %s vs %s", hash1, hash2)
	}
	if hash1 != hash3 {
		t.Errorf("Hashes with different method cases should match: got %s vs %s", hash1, hash3)
	}
}

// TestLoopGuardHash_PathParamSorting tests that path parameters are
// name-sorted before hashing.
func TestLoopGuardHash_PathParamSorting(t *testing.T) {
	hasher := NewRequestHasher(1024)

	// Same path params in different orders
	params1 := map[string]string{"id": "123", "version": "v1"}
	params2 := map[string]string{"version": "v1", "id": "123"}

	hash1 := hasher.ComputeHash("GET", "/api/users", params1, url.Values{}, nil)
	hash2 := hasher.ComputeHash("GET", "/api/users", params2, url.Values{}, nil)

	if hash1 != hash2 {
		t.Errorf("Hashes with reordered path params should match: got %s vs %s", hash1, hash2)
	}
}

// TestLoopGuardHash_PercentDecodedQueryParams tests that query parameters
// are percent-decoded before hashing.
func TestLoopGuardHash_PercentDecodedQueryParams(t *testing.T) {
	hasher := NewRequestHasher(1024)

	query1 := url.Values{}
	query1.Set("search", "hello world")

	query2 := url.Values{}
	query2.Set("search", "hello%20world")

	hash1 := hasher.ComputeHash("GET", "/api/search", map[string]string{}, query1, nil)
	hash2 := hasher.ComputeHash("GET", "/api/search", map[string]string{}, query2, nil)

	if hash1 != hash2 {
		t.Errorf("Hashes with percent-encoded vs decoded should match: got %s vs %s", hash1, hash2)
	}
}

// TestLoopGuardHash_RawBytesAboveMaxReplayable tests that bodies larger
// than maxReplayableRequestBytes are hashed as raw bytes (not canonicalized).
func TestLoopGuardHash_RawBytesAboveMaxReplayable(t *testing.T) {
	maxSize := int64(100)

	// Create a JSON body larger than maxSize
	largeBody := []byte(`{
		"name": "John",
		"age": 30,
		"city": "NYC",
		"extra": "` + strings.Repeat("x", 200) + `"
	}`)

	hasher1 := NewRequestHasher(maxSize)
	hash1 := hasher1.ComputeHash("POST", "/api/users", map[string]string{}, url.Values{}, largeBody)

	// With a larger max size, canonicalization should apply
	hasher2 := NewRequestHasher(maxSize * 10)
	hash2 := hasher2.ComputeHash("POST", "/api/users", map[string]string{}, url.Values{}, largeBody)

	// Hashes should be different because one is canonicalized and one is raw
	if hash1 == hash2 {
		t.Errorf("Hashes with different maxReplayableRequestBytes should differ when body exceeds limit: both are %s", hash1)
	}
}

// TestLoopGuardHash_PathTemplateIncluded tests that the path template
// is included in the hash (not just the resolved path).
func TestLoopGuardHash_PathTemplateIncluded(t *testing.T) {
	hasher := NewRequestHasher(1024)

	hash1 := hasher.ComputeHash("GET", "/api/users/{id}", map[string]string{"id": "123"}, url.Values{}, nil)
	hash2 := hasher.ComputeHash("GET", "/api/posts/{id}", map[string]string{"id": "123"}, url.Values{}, nil)

	if hash1 == hash2 {
		t.Errorf("Hashes with different path templates should differ: both are %s", hash1)
	}
}

// TestLoopGuardHash_QueryParamMultiValueSorting tests that multi-value
// query parameters are sorted before hashing.
func TestLoopGuardHash_QueryParamMultiValueSorting(t *testing.T) {
	hasher := NewRequestHasher(1024)

	query1 := url.Values{}
	query1.Add("tag", "blue")
	query1.Add("tag", "red")
	query1.Add("tag", "green")

	query2 := url.Values{}
	query2.Add("tag", "green")
	query2.Add("tag", "blue")
	query2.Add("tag", "red")

	hash1 := hasher.ComputeHash("GET", "/api/items", map[string]string{}, query1, nil)
	hash2 := hasher.ComputeHash("GET", "/api/items", map[string]string{}, query2, nil)

	if hash1 != hash2 {
		t.Errorf("Hashes with reordered multi-value params should match: got %s vs %s", hash1, hash2)
	}
}

// TestLoopGuardHash_NoBody tests that requests with no body hash correctly.
func TestLoopGuardHash_NoBody(t *testing.T) {
	hasher := NewRequestHasher(1024)

	hash1 := hasher.ComputeHash("GET", "/api/users", map[string]string{}, url.Values{}, nil)
	hash2 := hasher.ComputeHash("GET", "/api/users", map[string]string{}, url.Values{}, []byte{})

	if hash1 != hash2 {
		t.Errorf("Hashes with nil vs empty body should match: got %s vs %s", hash1, hash2)
	}
}

// TestLoopGuardHash_NestedJSON tests that nested JSON objects are
// canonicalized correctly.
func TestLoopGuardHash_NestedJSON(t *testing.T) {
	hasher := NewRequestHasher(1024)

	// Object keys are reordered at both depths; array order is preserved,
	// since RFC 8785 canonicalizes object keys only.
	body1 := []byte(`{"user":{"name":"John","age":30},"tags":["blue","red"]}`)
	body2 := []byte(`{"tags":["blue","red"],"user":{"age":30,"name":"John"}}`)

	hash1 := hasher.ComputeHash("POST", "/api/users", map[string]string{}, url.Values{}, body1)
	hash2 := hasher.ComputeHash("POST", "/api/users", map[string]string{}, url.Values{}, body2)

	if hash1 != hash2 {
		t.Errorf("Hashes with reordered nested JSON should match: got %s vs %s", hash1, hash2)
	}

	// Array order is significant and must not be canonicalized away.
	body3 := []byte(`{"tags":["red","blue"],"user":{"age":30,"name":"John"}}`)
	hash3 := hasher.ComputeHash("POST", "/api/users", map[string]string{}, url.Values{}, body3)
	if hash1 == hash3 {
		t.Errorf("Hashes with reordered array elements should not collide: both are %s", hash1)
	}
}

// TestLoopGuardHash_NonJSONBody tests that non-JSON bodies are hashed
// as raw bytes.
func TestLoopGuardHash_NonJSONBody(t *testing.T) {
	hasher := NewRequestHasher(1024)

	body1 := []byte(`plain text content`)
	body2 := []byte(`plain text content`)

	hash1 := hasher.ComputeHash("POST", "/api/data", map[string]string{}, url.Values{}, body1)
	hash2 := hasher.ComputeHash("POST", "/api/data", map[string]string{}, url.Values{}, body2)

	if hash1 != hash2 {
		t.Errorf("Hashes with identical non-JSON bodies should match: got %s vs %s", hash1, hash2)
	}
}
