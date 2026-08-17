package server

import (
	"fmt"
	"net/http"

	"github.com/ardenone/seam/internal/spec"
)

// RouteTable holds all routes for routing requests to upstream targets.
// It provides efficient lookup of routes based on path template, HTTP method, and API version.
type RouteTable struct {
	routes []RouteEntry
}

// RouteEntry represents a single route in the routing table.
// It contains all the information needed to match and forward a request to an upstream target.
type RouteEntry struct {
	// PathTemplate is the URL path template (e.g., "/api/v1/users/{id}")
	// Path parameters are enclosed in curly braces.
	PathTemplate string

	// Method is the HTTP method for this route (e.g., "GET", "POST", "PUT", "DELETE")
	Method string

	// APIVersion identifies the API version this route belongs to (e.g., "v1", "v2", "_unversioned")
	APIVersion string

	// UpstreamTarget is the base URL or identifier of the upstream service
	// that should handle requests matching this route (e.g., "http://userservice:8080")
	UpstreamTarget string
}

// RouteMatch represents a matched route with extracted path parameters.
// It is returned by RouteMatcher.Match when a request successfully matches a route.
type RouteMatch struct {
	// Route is the matched route entry
	Route RouteEntry

	// PathParams contains extracted path parameter values keyed by parameter name.
	// For example, if the PathTemplate is "/users/{id}" and the request path is "/users/123",
	// PathParams will contain {"id": "123"}.
	PathParams map[string]string
}

// RouteMatcher defines the interface for matching incoming requests against the route table.
// Implementations provide the matching logic that extracts route entries and path parameters
// from an HTTP request.
type RouteMatcher interface {
	// Match attempts to match the given HTTP request against the route table.
	// Returns a RouteMatch if a matching route is found, or an error if no match is found
	// or if the request is invalid.
	Match(req *http.Request) (*RouteMatch, error)
}

// NewRouteTable creates a new empty RouteTable.
// The spec.Loader parameter is accepted for backward compatibility but not used in this phase.
// Actual route table building from spec will be implemented in child bead 2.
func NewRouteTable(loader *spec.Loader) *RouteTable {
	return &RouteTable{
		routes: make([]RouteEntry, 0),
	}
}

// AddRoute adds a route entry to the route table.
func (t *RouteTable) AddRoute(entry RouteEntry) {
	t.routes = append(t.routes, entry)
}

// GetRoutes returns a copy of all routes in the table.
func (t *RouteTable) GetRoutes() []RouteEntry {
	routes := make([]RouteEntry, len(t.routes))
	copy(routes, t.routes)
	return routes
}

// RouteCount returns the number of routes in the table.
func (t *RouteTable) RouteCount() int {
	return len(t.routes)
}

// Validate checks if the route table entries are valid.
// Returns an error if any route entry has invalid field values.
func (t *RouteTable) Validate() error {
	for i, route := range t.routes {
		if err := validateRouteEntry(route, i); err != nil {
			return err
		}
	}
	return nil
}

// MatchRequest is a convenience method for backward compatibility.
// It returns a MatchResult with the Route field populated from RouteMatch.
// Deprecated: Use RouteMatcher.Match() instead.
func (t *RouteTable) MatchRequest(req *http.Request) *MatchResult {
	// This is a stub implementation for backward compatibility.
	// Full matching logic will be implemented in child beads.
	return &MatchResult{
		Route: RouteEntry{
			PathTemplate:   req.URL.Path,
			Method:         req.Method,
			APIVersion:     "_unversioned",
			UpstreamTarget: "",
		},
		PathParams: make(map[string]string),
		Matched:    false,
	}
}

// MatchResult represents a matched route result.
// It provides backward compatibility with existing code.
// Deprecated: Use RouteMatch instead.
type MatchResult struct {
	Route      RouteEntry
	PathParams map[string]string
	Matched    bool
	Path       string // Legacy field for backward compatibility
	Method     string // Legacy field for backward compatibility
	Version    string // Legacy field for backward compatibility
}

// validateRouteEntry validates a single route entry.
func validateRouteEntry(route RouteEntry, index int) error {
	if route.PathTemplate == "" {
		return fmt.Errorf("route at index %d: PathTemplate cannot be empty", index)
	}
	if route.Method == "" {
		return fmt.Errorf("route at index %d: Method cannot be empty", index)
	}
	if route.APIVersion == "" {
		return fmt.Errorf("route at index %d: APIVersion cannot be empty", index)
	}
	if route.UpstreamTarget == "" {
		return fmt.Errorf("route at index %d: UpstreamTarget cannot be empty", index)
	}
	return nil
}
