package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestBrownoutScheduler_DormantOnNoBrownout tests that the scheduler is dormant
// when no brownout windows are defined (fail-safe behavior)
func TestBrownoutScheduler_DormantOnNoBrownout(t *testing.T) {
	bs := NewBrownoutScheduler()

	// Create a route with deprecation but no brownout windows
	route := RouteEntry{
		PathTemplate: "/test",
		Method:       "GET",
		APIVersion:   "v1",
		Deprecated: &DeprecationInfo{
			Since:  "2024-01-01",
			Sunset:  "2024-12-31",
			// No brownout windows - should be DORMANT
		},
	}

	// Create a match with this route
	match := &RouteMatch{
		Route:      route,
		PathParams: map[string]string{},
	}

	// Create a request with the route match in context
	req := httptest.NewRequest("GET", "/test", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, "routeMatch", match)
	req = req.WithContext(ctx)

	// Create a response recorder
	w := httptest.NewRecorder()

	// Create a mock next handler that should be called (DORMANT behavior)
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Wrap with brownout middleware
	handler := bs.Middleware(next)
	handler.ServeHTTP(w, req)

	// Verify that next was called (DORMANT behavior)
	if !nextCalled {
		t.Error("Expected next handler to be called when no brownout windows defined")
	}

	// Verify normal response
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

// TestBrownoutScheduler_ActiveDuringBrownout tests that 410 is returned during brownout
func TestBrownoutScheduler_ActiveDuringBrownout(t *testing.T) {
	bs := NewBrownoutScheduler()

	// Mock clock to be during a brownout window
	testTime, _ := time.Parse(time.RFC3339, "2024-06-15T10:30:00Z")
	bs.SetClock(func() time.Time { return testTime })

	// Create a route with active brownout
	route := RouteEntry{
		PathTemplate: "/test",
		Method:       "GET",
		APIVersion:   "v1",
		Deprecated: &DeprecationInfo{
			Since:  "2024-01-01",
			Sunset:  "2024-12-31",
			Brownouts: []BrownoutWindow{
				{
					Start: "2024-06-15T00:00:00Z",
					End:   "2024-06-15T23:59:59Z",
				},
			},
		},
	}

	match := &RouteMatch{
		Route:      route,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest("GET", "/test", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, "routeMatch", match)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	handler := bs.Middleware(next)
	handler.ServeHTTP(w, req)

	// Verify that next was NOT called (brownout active)
	if nextCalled {
		t.Error("Expected next handler NOT to be called during brownout")
	}

	// Verify 410 Gone response
	if w.Code != http.StatusGone {
		t.Errorf("Expected status 410, got %d", w.Code)
	}

	// Verify brownout header
	if w.Header().Get("X-SEAM-Brownout") != "active" {
		t.Error("Expected X-SEAM-Brownout header to be 'active'")
	}

	// Verify response body is structured JSON
	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response JSON: %v", err)
	}

	if response["error"] != "gone" {
		t.Errorf("Expected error 'gone', got %v", response["error"])
	}

	if response["brownout"] == nil {
		t.Error("Expected brownout info in response")
	}
}

// TestBrownoutScheduler_OutsideBrownout tests normal operation outside brownout
func TestBrownoutScheduler_OutsideBrownout(t *testing.T) {
	bs := NewBrownoutScheduler()

	// Mock clock to be outside brownout window
	testTime, _ := time.Parse(time.RFC3339, "2024-05-15T10:30:00Z")
	bs.SetClock(func() time.Time { return testTime })

	route := RouteEntry{
		PathTemplate: "/test",
		Method:       "GET",
		APIVersion:   "v1",
		Deprecated: &DeprecationInfo{
			Since:  "2024-01-01",
			Sunset:  "2024-12-31",
			Brownouts: []BrownoutWindow{
				{
					Start: "2024-06-15T00:00:00Z",
					End:   "2024-06-15T23:59:59Z",
				},
			},
		},
	}

	match := &RouteMatch{
		Route:      route,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest("GET", "/test", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, "routeMatch", match)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	handler := bs.Middleware(next)
	handler.ServeHTTP(w, req)

	// Verify next was called (outside brownout window)
	if !nextCalled {
		t.Error("Expected next handler to be called outside brownout window")
	}

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

// TestBrownoutScheduler_NoRouteMatch tests pass-through when no route match
func TestBrownoutScheduler_NoRouteMatch(t *testing.T) {
	bs := NewBrownoutScheduler()

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	handler := bs.Middleware(next)
	handler.ServeHTTP(w, req)

	// Should pass through when no route match (fail-safe)
	if !nextCalled {
		t.Error("Expected next handler to be called when no route match")
	}
}

// TestBrownoutScheduler_NoDeprecationInfo tests pass-through with no deprecation
func TestBrownoutScheduler_NoDeprecationInfo(t *testing.T) {
	bs := NewBrownoutScheduler()

	route := RouteEntry{
		PathTemplate: "/test",
		Method:       "GET",
		APIVersion:   "v1",
		Deprecated:   nil, // No deprecation info
	}

	match := &RouteMatch{
		Route:      route,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest("GET", "/test", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, "routeMatch", match)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	handler := bs.Middleware(next)
	handler.ServeHTTP(w, req)

	// Should pass through with no deprecation info
	if !nextCalled {
		t.Error("Expected next handler to be called with no deprecation info")
	}
}

// TestBrownoutWindow_IsActiveAt tests the IsActiveAt method
func TestBrownoutWindow_IsActiveAt(t *testing.T) {
	tests := []struct {
		name      string
		window    BrownoutWindow
		testTime  string
		expected  bool
	}{
		{
			name: "Active during window",
			window: BrownoutWindow{
				Start: "2024-06-15T10:00:00Z",
				End:   "2024-06-15T11:00:00Z",
			},
			testTime: "2024-06-15T10:30:00Z",
			expected: true,
		},
		{
			name: "Before window",
			window: BrownoutWindow{
				Start: "2024-06-15T10:00:00Z",
				End:   "2024-06-15T11:00:00Z",
			},
			testTime: "2024-06-15T09:59:59Z",
			expected: false,
		},
		{
			name: "After window",
			window: BrownoutWindow{
				Start: "2024-06-15T10:00:00Z",
				End:   "2024-06-15T11:00:00Z",
			},
			testTime: "2024-06-15T11:00:01Z",
			expected: false,
		},
		{
			name: "Exactly at start",
			window: BrownoutWindow{
				Start: "2024-06-15T10:00:00Z",
				End:   "2024-06-15T11:00:00Z",
			},
			testTime: "2024-06-15T10:00:00Z",
			expected: true,
		},
		{
			name: "Exactly at end",
			window: BrownoutWindow{
				Start: "2024-06-15T10:00:00Z",
				End:   "2024-06-15T11:00:00Z",
			},
			testTime: "2024-06-15T11:00:00Z",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testTime, err := time.Parse(time.RFC3339, tt.testTime)
			if err != nil {
				t.Fatalf("Failed to parse test time: %v", err)
			}

			result := tt.window.IsActiveAt(testTime)
			if result != tt.expected {
				t.Errorf("IsActiveAt(%q) = %v, want %v", tt.testTime, result, tt.expected)
			}
		})
	}
}

// TestBrownoutScheduler_WithReplacement tests that replacement info is included
func TestBrownoutScheduler_WithReplacement(t *testing.T) {
	bs := NewBrownoutScheduler()

	testTime, _ := time.Parse(time.RFC3339, "2024-06-15T10:30:00Z")
	bs.SetClock(func() time.Time { return testTime })

	route := RouteEntry{
		PathTemplate: "/old-api",
		Method:       "GET",
		APIVersion:   "v1",
		Deprecated: &DeprecationInfo{
			Since:             "2024-01-01",
			Sunset:            "2024-12-31",
			ReplacementPath:   "/new-api",
			ReplacementVersion: "v2",
			Brownouts: []BrownoutWindow{
				{
					Start: "2024-06-15T00:00:00Z",
					End:   "2024-06-15T23:59:59Z",
				},
			},
		},
	}

	match := &RouteMatch{
		Route:      route,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest("GET", "/old-api", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, "routeMatch", match)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := bs.Middleware(next)
	handler.ServeHTTP(w, req)

	var response map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &response)

	replacement, ok := response["replacement"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected replacement info in response")
	}

	if replacement["path"] != "/new-api" {
		t.Errorf("Expected replacement path '/new-api', got %v", replacement["path"])
	}

	if replacement["version"] != "v2" {
		t.Errorf("Expected replacement version 'v2', got %v", replacement["version"])
	}

	// Verify Link header to replacement
	linkHeaders := w.Header()["Link"]
	foundReplacementLink := false
	for _, link := range linkHeaders {
		if containsString(link, "rel=\"alternate\"") {
			foundReplacementLink = true
			break
		}
	}
	if !foundReplacementLink {
		t.Error("Expected Link header with rel=alternate to replacement")
	}
}

// Helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
