package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDeprecationHeaders_AddsHeaders tests that deprecation headers are added correctly
func TestDeprecationHeaders_AddsHeaders(t *testing.T) {
	dh := NewDeprecationHeaders()

	route := RouteEntry{
		PathTemplate: "/test",
		Method:       "GET",
		APIVersion:   "v1",
		Deprecated: &DeprecationInfo{
			Since:              "2024-01-01",
			Sunset:             "2024-12-31",
			ReplacementPath:    "/new-api",
			ReplacementVersion: "v2",
		},
	}

	match := &RouteMatch{
		Route:      route,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, routeMatchContextKey{}, match)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	handler := dh.Middleware(next)
	handler.ServeHTTP(w, req)

	// Verify Deprecation header
	deprecation := w.Header().Get("Deprecation")
	if deprecation != "since=2024-01-01" {
		t.Errorf("Expected Deprecation header 'since=2024-01-01', got '%s'", deprecation)
	}

	// Verify Sunset header
	sunset := w.Header().Get("Sunset")
	if sunset != "2024-12-31" {
		t.Errorf("Expected Sunset header '2024-12-31', got '%s'", sunset)
	}

	// Verify Link headers
	linkHeaders := w.Header()["Link"]
	if len(linkHeaders) < 2 {
		t.Errorf("Expected at least 2 Link headers, got %d", len(linkHeaders))
	}

	// Check for /docs/route link
	foundDocsLink := false
	foundChangesLink := false
	foundAlternateLink := false

	for _, link := range linkHeaders {
		if containsString(link, "/docs/route") && containsString(link, `rel="deprecation"`) {
			foundDocsLink = true
		}
		if containsString(link, "/changes") && containsString(link, `rel="deprecation"`) {
			foundChangesLink = true
		}
		if containsString(link, "rel=\"alternate\"") {
			foundAlternateLink = true
		}
	}

	if !foundDocsLink {
		t.Error("Expected Link header to /docs/route with rel=deprecation")
	}

	if !foundChangesLink {
		t.Error("Expected Link header to /changes with rel=deprecation")
	}

	if !foundAlternateLink {
		t.Error("Expected Link header with rel=alternate to replacement")
	}

	// Verify response is still 200 OK
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

// TestDeprecationHeaders_WithoutSunset tests headers when only since is set
func TestDeprecationHeaders_WithoutSunset(t *testing.T) {
	dh := NewDeprecationHeaders()

	route := RouteEntry{
		PathTemplate: "/test",
		Method:       "GET",
		APIVersion:   "v1",
		Deprecated: &DeprecationInfo{
			Since: "2024-01-01",
			// No sunset
		},
	}

	match := &RouteMatch{
		Route:      route,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, routeMatchContextKey{}, match)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := dh.Middleware(next)
	handler.ServeHTTP(w, req)

	// Verify Deprecation header
	deprecation := w.Header().Get("Deprecation")
	if deprecation != "since=2024-01-01" {
		t.Errorf("Expected Deprecation header 'since=2024-01-01', got '%s'", deprecation)
	}

	// Verify no Sunset header
	sunset := w.Header().Get("Sunset")
	if sunset != "" {
		t.Errorf("Expected no Sunset header, got '%s'", sunset)
	}
}

// TestDeprecationHeaders_NoDeprecation tests pass-through when not deprecated
func TestDeprecationHeaders_NoDeprecation(t *testing.T) {
	dh := NewDeprecationHeaders()

	route := RouteEntry{
		PathTemplate: "/test",
		Method:       "GET",
		APIVersion:   "v1",
		Deprecated:   nil,
	}

	match := &RouteMatch{
		Route:      route,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, routeMatchContextKey{}, match)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := dh.Middleware(next)
	handler.ServeHTTP(w, req)

	// Verify no deprecation headers
	deprecation := w.Header().Get("Deprecation")
	if deprecation != "" {
		t.Errorf("Expected no Deprecation header, got '%s'", deprecation)
	}

	sunset := w.Header().Get("Sunset")
	if sunset != "" {
		t.Errorf("Expected no Sunset header, got '%s'", sunset)
	}
}

// TestDeprecationHeaders_NoRouteMatch tests pass-through when no route match
func TestDeprecationHeaders_NoRouteMatch(t *testing.T) {
	dh := NewDeprecationHeaders()

	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	w := httptest.NewRecorder()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := dh.Middleware(next)
	handler.ServeHTTP(w, req)

	// Should pass through without error
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

// TestDeprecationHeaders_WithoutReplacement tests headers when no replacement specified
func TestDeprecationHeaders_WithoutReplacement(t *testing.T) {
	dh := NewDeprecationHeaders()

	route := RouteEntry{
		PathTemplate: "/test",
		Method:       "GET",
		APIVersion:   "v1",
		Deprecated: &DeprecationInfo{
			Since:  "2024-01-01",
			Sunset: "2024-12-31",
			// No replacement info
		},
	}

	match := &RouteMatch{
		Route:      route,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, routeMatchContextKey{}, match)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := dh.Middleware(next)
	handler.ServeHTTP(w, req)

	// Verify deprecation headers are present
	deprecation := w.Header().Get("Deprecation")
	if deprecation != "since=2024-01-01" {
		t.Errorf("Expected Deprecation header 'since=2024-01-01', got '%s'", deprecation)
	}

	// Verify no alternate link (no replacement)
	linkHeaders := w.Header()["Link"]
	for _, link := range linkHeaders {
		if containsString(link, "rel=\"alternate\"") {
			t.Error("Expected no alternate Link header when no replacement specified")
		}
	}
}

// TestDeprecationHeaders_WithForwardedHeaders tests handling of X-Forwarded-* headers
func TestDeprecationHeaders_WithForwardedHeaders(t *testing.T) {
	dh := NewDeprecationHeaders()

	route := RouteEntry{
		PathTemplate: "/test",
		Method:       "GET",
		APIVersion:   "v1",
		Deprecated: &DeprecationInfo{
			Since: "2024-01-01",
		},
	}

	match := &RouteMatch{
		Route:      route,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "api.example.com")
	ctx := req.Context()
	ctx = context.WithValue(ctx, routeMatchContextKey{}, match)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := dh.Middleware(next)
	handler.ServeHTTP(w, req)

	// Verify Link headers use forwarded info
	linkHeaders := w.Header()["Link"]
	found := false
	for _, link := range linkHeaders {
		if containsString(link, "https://api.example.com") {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected Link headers to use X-Forwarded-* headers")
	}
}
