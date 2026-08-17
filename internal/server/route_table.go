package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/ardenone/seam/internal/spec"
	"github.com/pb33f/libopenapi/datamodel/high/v3"
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

// BuildRouteTable creates a populated RouteTable from an OpenAPI v3 document.
// The route table contains the five HTTP methods SEAM currently proxies. A
// missing operation-level x-api-version extension is represented as v1.
func BuildRouteTable(spec *v3.Document) (*RouteTable, error) {
	if spec == nil {
		return nil, fmt.Errorf("OpenAPI spec cannot be nil")
	}

	if spec.Paths == nil {
		return nil, fmt.Errorf("OpenAPI spec is missing required paths")
	}
	if spec.Paths.PathItems == nil {
		return nil, fmt.Errorf("OpenAPI spec paths object is malformed")
	}

	table := &RouteTable{
		routes: make([]RouteEntry, 0),
	}

	seen := make(map[routeKey]struct{})

	for path, pathItem := range spec.Paths.PathItems.FromOldest() {
		if path == "" || !strings.HasPrefix(path, "/") {
			return nil, fmt.Errorf("invalid OpenAPI path %q: path must start with '/'", path)
		}
		if pathItem == nil {
			return nil, fmt.Errorf("OpenAPI path %q has a nil path item", path)
		}

		// Keep this list explicit. Additional OpenAPI operations such as HEAD and
		// OPTIONS are not part of the route-table contract yet.
		methods := []struct {
			method    string
			operation *v3.Operation
		}{
			{http.MethodGet, pathItem.Get},
			{http.MethodPost, pathItem.Post},
			{http.MethodPut, pathItem.Put},
			{http.MethodDelete, pathItem.Delete},
			{http.MethodPatch, pathItem.Patch},
		}

		for _, methodOp := range methods {
			if methodOp.operation == nil {
				continue
			}
			if methodOp.operation.Responses == nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s is missing required responses", methodOp.method, path)
			}

			apiVersion, err := extractAPIVersion(methodOp.operation)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}

			entry := RouteEntry{
				PathTemplate: path,
				Method:       methodOp.method,
				APIVersion:   apiVersion,
			}

			if err := addBuiltRoute(table, seen, entry); err != nil {
				return nil, err
			}
		}
	}

	return table, nil
}

type routeKey struct {
	path    string
	method  string
	version string
}

// addBuiltRoute centralizes insertion and duplicate detection. OpenAPI path
// maps normally enforce unique path keys themselves, but keeping this guard at
// the route-entry boundary also protects callers if a model is assembled by
// hand or a future parser exposes duplicate operations.
func addBuiltRoute(table *RouteTable, seen map[routeKey]struct{}, entry RouteEntry) error {
	key := routeKey{
		path:    entry.PathTemplate,
		method:  entry.Method,
		version: entry.APIVersion,
	}
	if _, exists := seen[key]; exists {
		return fmt.Errorf("duplicate route detected: path=%s method=%s version=%s", entry.PathTemplate, entry.Method, entry.APIVersion)
	}
	seen[key] = struct{}{}
	table.routes = append(table.routes, entry)
	return nil
}

// extractAPIVersion extracts the API version from operation extensions.
// It looks for the "x-api-version" extension and returns its value.
// If not found, defaults to "v1".
func extractAPIVersion(operation *v3.Operation) (string, error) {
	if operation == nil {
		return "", fmt.Errorf("operation cannot be nil")
	}

	if operation.Extensions == nil {
		return "v1", nil
	}

	if versionNode, ok := operation.Extensions.Get("x-api-version"); ok && versionNode != nil {
		var value any
		if err := versionNode.Decode(&value); err != nil {
			return "", fmt.Errorf("x-api-version must be a string: %w", err)
		}
		version, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("x-api-version must be a string")
		}
		if strings.TrimSpace(version) == "" {
			return "", fmt.Errorf("x-api-version must not be empty")
		}
		return version, nil
	}

	return "v1", nil
}

// NewRouteTable creates a new empty RouteTable.
// The spec.Loader parameter is accepted for backward compatibility but is not
// used; callers that have a parsed model should use BuildRouteTable.
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
