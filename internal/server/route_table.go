package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

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

// RouteTableHolder owns the route table used to match requests. Implementations
// may replace the table while requests are being served.
type RouteTableHolder interface {
	// Swap atomically replaces the current route table.
	Swap(table *RouteTable) error
	// Match matches a request against the current route table.
	Match(req *http.Request) (*RouteMatch, error)
}

// ThreadSafeTableHolder provides an atomic route-table swap point for hot
// reloads. Route tables are built completely before being passed to Swap and
// are treated as immutable after installation.
type ThreadSafeTableHolder struct {
	mu      sync.RWMutex
	current *RouteTable
}

var _ RouteTableHolder = (*ThreadSafeTableHolder)(nil)

// NewThreadSafeTableHolder creates a holder with table as its initial route
// table. A nil table creates an empty holder; the first non-nil table can be
// installed with Swap.
func NewThreadSafeTableHolder(table *RouteTable) *ThreadSafeTableHolder {
	return &ThreadSafeTableHolder{current: table}
}

// Swap atomically installs table as the current route table. Nil tables are
// rejected so an invalid reload cannot make an existing table unavailable.
func (h *ThreadSafeTableHolder) Swap(table *RouteTable) error {
	if h == nil {
		return fmt.Errorf("route table holder is nil")
	}
	if table == nil {
		return fmt.Errorf("route table cannot be nil")
	}

	h.mu.Lock()
	h.current = table
	h.mu.Unlock()
	return nil
}

// Match matches req against the route table that is current when the read
// begins. The read lock remains held for the duration of the match so a swap
// cannot expose a partially observed table to a caller.
func (h *ThreadSafeTableHolder) Match(req *http.Request) (*RouteMatch, error) {
	if h == nil {
		return nil, fmt.Errorf("route table holder is nil")
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.current == nil {
		return nil, fmt.Errorf("route table is not initialized")
	}
	return h.current.Match(req)
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

// Match returns the route matching req and extracts values for path
// parameters. When the request does not select an API version explicitly,
// the oldest matching version is selected, with _unversioned taking
// precedence over numbered versions.
func (t *RouteTable) Match(req *http.Request) (*RouteMatch, error) {
	if t == nil {
		return nil, fmt.Errorf("route table is nil")
	}
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("request is nil")
	}

	requestedVersion := req.Header.Get("X-SEAM-API-Version")
	method := strings.ToUpper(req.Method)
	var selected *RouteMatch
	selectedRank := int(^uint(0) >> 1)

	for _, route := range t.routes {
		if strings.ToUpper(route.Method) != method {
			continue
		}
		if requestedVersion != "" && route.APIVersion != requestedVersion {
			continue
		}

		pathParams, ok := matchRoutePath(route.PathTemplate, req.URL.Path)
		if !ok {
			continue
		}

		if requestedVersion != "" {
			return &RouteMatch{Route: route, PathParams: pathParams}, nil
		}

		rank := routeVersionRank(route.APIVersion)
		if selected == nil || rank < selectedRank {
			selected = &RouteMatch{Route: route, PathParams: pathParams}
			selectedRank = rank
		}
	}

	if selected == nil {
		return nil, fmt.Errorf("no route matched %s %s", method, req.URL.Path)
	}
	return selected, nil
}

func matchRoutePath(template, path string) (map[string]string, bool) {
	templateParts := strings.Split(template, "/")
	pathParts := strings.Split(path, "/")
	if len(templateParts) != len(pathParts) {
		return nil, false
	}

	params := make(map[string]string)
	for i, templatePart := range templateParts {
		pathPart := pathParts[i]
		if len(templatePart) >= 2 && templatePart[0] == '{' && templatePart[len(templatePart)-1] == '}' {
			name := templatePart[1 : len(templatePart)-1]
			if name == "" {
				return nil, false
			}
			params[name] = pathPart
			continue
		}
		if templatePart != pathPart {
			return nil, false
		}
	}
	return params, true
}

func routeVersionRank(version string) int {
	if version == "_unversioned" {
		return 0
	}
	if strings.HasPrefix(version, "v") {
		if number, err := strconv.Atoi(version[1:]); err == nil && number >= 0 {
			return number + 1
		}
	}
	return int(^uint(0) >> 1)
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
