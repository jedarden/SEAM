package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/ardenone/seam/internal/spec"
	"github.com/ardenone/seam/internal/vault"
	"github.com/pb33f/libopenapi/datamodel/high/v3"
	"go.yaml.in/yaml/v4"
)

// RouteTable holds all routes for routing requests to upstream targets.
// It provides efficient lookup of routes based on path template, HTTP method, and API version.
type RouteTable struct {
	routes []RouteEntry

	secretMu     sync.Mutex
	secretClient *vault.Client
}

// UpstreamTLSConfig represents TLS configuration for upstream connections.
// Absent fields mean system trust store with hostname checking (default secure behavior).
type UpstreamTLSConfig struct {
	// CaBundle names a PEM key in the upstream CA ConfigMap, resolved as a file
	// under /etc/gateway/upstream-ca/<key>. Absent means system trust store.
	CaBundle string

	// ServerName overrides the SNI/hostname verification. Absent means use the
	// upstream URL's hostname.
	ServerName string

	// InsecureSkipVerify is true only when the fragment author explicitly set
	// x-upstream-tls.insecureSkipVerify: "acknowledged". This is NEVER set by a
	// global flag - it is per-route only and requires human acknowledgment.
	InsecureSkipVerify bool

	// PlaintextAck is true when the fragment author acknowledged that this route
	// uses plaintext http:// (x-upstream-plaintext: "acknowledged").
	PlaintextAck bool
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

	// TLSConfig holds the TLS configuration for this route's upstream connection.
	// If nil, the route uses system trust store with hostname checking.
	TLSConfig *UpstreamTLSConfig

	// VaultPath and InjectAs describe the optional server-side credential
	// injection. VaultPath is a reference only; no secret value is retained in
	// the route table.
	VaultPath string
	InjectAs  *InjectAs

	// Unscrubbable is true only when the route explicitly acknowledges that an
	// injected response cannot be scanned. The acknowledgement is enumerable
	// through /config/status and is never inferred from a runtime failure.
	Unscrubbable bool

	// InstanceParam identifies the path binding consumed by an upstream map.
	// It is removed from the upstream path before any remaining bindings are
	// substituted.
	InstanceParam string

	// UpstreamPathTemplate is the path-item rewrite and wins over the
	// fragment-level UpstreamStripPrefix shorthand.
	UpstreamPathTemplate string
	UpstreamStripPrefix  string

	// UpstreamMap carries per-instance targets. The map is intentionally a
	// route-table concern; the served OpenAPI document never needs to expose
	// these forwarding details.
	UpstreamMap map[string]RouteTarget
}

// RouteTarget is the resolved forwarding and injection metadata for one
// upstream-map entry.
type RouteTarget struct {
	URL       string
	VaultPath string
	InjectAs  *InjectAs
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

			apiVersion, err := extractAPIVersionWithContext(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}

			upstreamTarget, err := extractUpstreamTargetWithContext(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}

			tlsConfig, err := extractUpstreamTLSConfig(methodOp.operation)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}

			vaultPath, err := extractStringExtension(methodOp.operation, pathItem, spec, "x-vault-path")
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			injectAs, err := extractInjectAs(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			unscrubbable, err := extractAcknowledgedExtension(methodOp.operation, pathItem, spec, "x-unscrubbable")
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			instanceParam, err := extractStringExtension(methodOp.operation, pathItem, spec, "x-instance-param")
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			upstreamPathTemplate, err := extractStringExtension(methodOp.operation, pathItem, nil, "x-upstream-path-template")
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			upstreamStripPrefix, err := extractStringExtension(methodOp.operation, pathItem, spec, "x-upstream-strip-prefix")
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			upstreamMap, err := extractUpstreamMap(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}

			entry := RouteEntry{
				PathTemplate:         path,
				Method:               methodOp.method,
				APIVersion:           apiVersion,
				UpstreamTarget:       upstreamTarget,
				TLSConfig:            tlsConfig,
				VaultPath:            vaultPath,
				InjectAs:             injectAs,
				Unscrubbable:         unscrubbable,
				InstanceParam:        instanceParam,
				UpstreamPathTemplate: upstreamPathTemplate,
				UpstreamStripPrefix:  upstreamStripPrefix,
				UpstreamMap:          upstreamMap,
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
	return extractAPIVersionWithContext(operation, nil, nil)
}

func extractAPIVersionWithContext(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) (string, error) {
	if operation == nil {
		return "", fmt.Errorf("operation cannot be nil")
	}

	if versionNode, ok := firstExtension(operation, pathItem, document, "x-api-version"); ok && versionNode != nil {
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

// extractUpstreamTarget extracts the upstream target URL from operation extensions.
// It looks for the "x-upstream" extension and returns its value.
// If not found, returns an empty string, which means no upstream is configured.
func extractUpstreamTarget(operation *v3.Operation) (string, error) {
	return extractUpstreamTargetWithContext(operation, nil, nil)
}

func extractUpstreamTargetWithContext(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) (string, error) {
	if operation == nil {
		return "", fmt.Errorf("operation cannot be nil")
	}

	if upstreamNode, ok := firstExtension(operation, pathItem, document, "x-upstream"); ok && upstreamNode != nil {
		var value any
		if err := upstreamNode.Decode(&value); err != nil {
			return "", fmt.Errorf("x-upstream must be a string: %w", err)
		}
		upstream, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("x-upstream must be a string")
		}
		upstream = strings.TrimSpace(upstream)
		if upstream == "" {
			return "", nil
		}
		return upstream, nil
	}

	return "", nil
}

func extractStringExtension(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document, name string) (string, error) {
	node, ok := firstExtension(operation, pathItem, document, name)
	if !ok || node == nil {
		return "", nil
	}
	var value any
	if err := node.Decode(&value); err != nil {
		return "", fmt.Errorf("%s must be a string: %w", name, err)
	}
	stringValue, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return strings.TrimSpace(stringValue), nil
}

func extractInjectAs(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) (*InjectAs, error) {
	node, ok := firstExtension(operation, pathItem, document, "x-inject-as")
	if !ok || node == nil {
		return nil, nil
	}
	var value struct {
		Kind InjectionKind `yaml:"kind" json:"kind"`
		Name string        `yaml:"name" json:"name"`
	}
	if err := node.Decode(&value); err != nil {
		return nil, fmt.Errorf("x-inject-as must be an object: %w", err)
	}
	injectAs := &InjectAs{Kind: value.Kind, Name: value.Name}
	if err := injectAs.validate(); err != nil {
		return nil, err
	}
	return injectAs, nil
}

func extractAcknowledgedExtension(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document, name string) (bool, error) {
	node, ok := firstExtension(operation, pathItem, document, name)
	if !ok || node == nil {
		return false, nil
	}
	var value string
	if err := node.Decode(&value); err != nil {
		return false, fmt.Errorf("%s must be a string: %w", name, err)
	}
	if value != "acknowledged" {
		return false, fmt.Errorf("%s must be \"acknowledged\" or absent; got %q", name, value)
	}
	return true, nil
}

func extractUpstreamMap(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) (map[string]RouteTarget, error) {
	node, ok := firstExtension(operation, pathItem, document, "x-upstream-map")
	if !ok || node == nil {
		return nil, nil
	}
	var raw map[string]struct {
		URL       string    `yaml:"url" json:"url"`
		VaultPath string    `yaml:"vaultPath" json:"vaultPath"`
		InjectAs  *InjectAs `yaml:"injectAs" json:"injectAs"`
	}
	if err := node.Decode(&raw); err != nil {
		return nil, fmt.Errorf("x-upstream-map must be an object: %w", err)
	}
	result := make(map[string]RouteTarget, len(raw))
	for key, value := range raw {
		if strings.TrimSpace(value.URL) == "" {
			return nil, fmt.Errorf("x-upstream-map entry %q is missing url", key)
		}
		if value.InjectAs != nil {
			if err := value.InjectAs.validate(); err != nil {
				return nil, fmt.Errorf("x-upstream-map entry %q: %w", key, err)
			}
		}
		result[key] = RouteTarget{URL: strings.TrimSpace(value.URL), VaultPath: value.VaultPath, InjectAs: value.InjectAs}
	}
	return result, nil
}

func firstExtension(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document, name string) (*yaml.Node, bool) {
	keys := []string{name}
	if strings.HasPrefix(name, "x-") {
		keys = append(keys, "x-seam-internal-"+strings.TrimPrefix(name, "x-"))
	}
	lookup := func(extensions interface {
		Get(string) (*yaml.Node, bool)
	}) (*yaml.Node, bool) {
		for _, key := range keys {
			if node, ok := extensions.Get(key); ok {
				return node, true
			}
		}
		return nil, false
	}
	if operation != nil && operation.Extensions != nil {
		if node, ok := lookup(operation.Extensions); ok {
			return node, true
		}
	}
	if pathItem != nil && pathItem.Extensions != nil {
		if node, ok := lookup(pathItem.Extensions); ok {
			return node, true
		}
	}
	if document != nil && document.Extensions != nil {
		if node, ok := lookup(document.Extensions); ok {
			return node, true
		}
	}
	return nil, false
}

// extractUpstreamTLSConfig extracts TLS configuration from operation extensions.
// It looks for "x-upstream-tls" and "x-upstream-plaintext" extensions.
// Returns nil if no TLS configuration is specified (system trust store default).
func extractUpstreamTLSConfig(operation *v3.Operation) (*UpstreamTLSConfig, error) {
	if operation == nil {
		return nil, fmt.Errorf("operation cannot be nil")
	}

	if operation.Extensions == nil {
		return nil, nil
	}

	var tlsConfig UpstreamTLSConfig
	hasTLSConfig := false

	// Extract x-upstream-tls configuration
	if tlsNode, ok := operation.Extensions.Get("x-upstream-tls"); ok && tlsNode != nil {
		var tlsMap map[string]any
		if err := tlsNode.Decode(&tlsMap); err != nil {
			return nil, fmt.Errorf("x-upstream-tls must be an object: %w", err)
		}

		// Extract caBundle
		if caBundle, ok := tlsMap["caBundle"].(string); ok && caBundle != "" {
			tlsConfig.CaBundle = caBundle
			hasTLSConfig = true
		}

		// Extract serverName
		if serverName, ok := tlsMap["serverName"].(string); ok && serverName != "" {
			tlsConfig.ServerName = serverName
			hasTLSConfig = true
		}

		// Extract insecureSkipVerify - only "acknowledged" is accepted
		if insecureSkipVerify, ok := tlsMap["insecureSkipVerify"].(string); ok {
			if insecureSkipVerify == "acknowledged" {
				tlsConfig.InsecureSkipVerify = true
				hasTLSConfig = true
			} else if insecureSkipVerify != "" {
				return nil, fmt.Errorf("x-upstream-tls.insecureSkipVerify must be \"acknowledged\" or absent; got %q", insecureSkipVerify)
			}
		}
	}

	// Extract x-upstream-plaintext acknowledgment
	if plaintextNode, ok := operation.Extensions.Get("x-upstream-plaintext"); ok && plaintextNode != nil {
		var plaintext string
		if err := plaintextNode.Decode(&plaintext); err != nil {
			return nil, fmt.Errorf("x-upstream-plaintext must be a string: %w", err)
		}
		if plaintext == "acknowledged" {
			tlsConfig.PlaintextAck = true
			hasTLSConfig = true
		} else if plaintext != "" {
			return nil, fmt.Errorf("x-upstream-plaintext must be \"acknowledged\" or absent; got %q", plaintext)
		}
	}

	if !hasTLSConfig {
		return nil, nil
	}

	return &tlsConfig, nil
}

// NewRouteTable creates a route table from the loaded OpenAPI model. A nil or
// temporarily unbuildable loader still produces an empty table for the
// control-plane-only startup path; explicit callers should use BuildRouteTable
// when they need the construction error.
func NewRouteTable(loader *spec.Loader) *RouteTable {
	if loader != nil {
		if model := loader.OpenAPIModel(); model != nil {
			if table, err := BuildRouteTable(model); err == nil {
				return table
			}
		}
	}
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

		requestPath := req.URL.EscapedPath()
		if requestPath == "" {
			requestPath = req.URL.Path
		}
		pathParams, ok := matchRoutePath(route.PathTemplate, requestPath)
		if !ok {
			continue
		}

		if requestedVersion != "" {
			match := &RouteMatch{Route: route, PathParams: pathParams}
			match.Route = match.Route.effectiveTarget(pathParams)
			if err := t.SanitizeRequest(req); err != nil {
				return nil, err
			}
			withRouteMatch(req, match, t.resolveCredential)
			return match, nil
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
	selected.Route = selected.Route.effectiveTarget(selected.PathParams)
	if err := t.SanitizeRequest(req); err != nil {
		return nil, err
	}
	withRouteMatch(req, selected, t.resolveCredential)
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
		pathPart, err := url.PathUnescape(pathParts[i])
		if err != nil {
			return nil, false
		}
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

func (t *RouteTable) matchingRoutes(req *http.Request) []RouteEntry {
	if t == nil || req == nil || req.URL == nil {
		return nil
	}
	method := strings.ToUpper(req.Method)
	path := req.URL.EscapedPath()
	if path == "" {
		path = req.URL.Path
	}
	var matches []RouteEntry
	for _, route := range t.routes {
		if strings.ToUpper(route.Method) != method {
			continue
		}
		if _, ok := matchRoutePath(route.PathTemplate, path); ok {
			matches = append(matches, route)
		}
	}
	return matches
}

func (route RouteEntry) injectableHeaderNames() map[string]struct{} {
	result := make(map[string]struct{})
	add := func(injectAs *InjectAs) {
		if injectAs == nil {
			return
		}
		switch injectAs.Kind {
		case InjectionHeader:
			result[injectAs.Name] = struct{}{}
		case InjectionBearer:
			result["Authorization"] = struct{}{}
		}
	}
	add(route.InjectAs)
	for _, target := range route.UpstreamMap {
		add(target.InjectAs)
	}
	return result
}

func (route RouteEntry) injectableQueryNames() map[string]struct{} {
	result := make(map[string]struct{})
	add := func(injectAs *InjectAs) {
		if injectAs != nil && injectAs.Kind == InjectionQuery {
			result[injectAs.Name] = struct{}{}
		}
	}
	add(route.InjectAs)
	for _, target := range route.UpstreamMap {
		add(target.InjectAs)
	}
	return result
}

func (route RouteEntry) effectiveTarget(pathParams map[string]string) RouteEntry {
	if route.InstanceParam == "" || len(route.UpstreamMap) == 0 {
		return route
	}
	instance := pathParams[route.InstanceParam]
	target, ok := route.UpstreamMap[instance]
	if !ok {
		target, ok = route.UpstreamMap["_default"]
	}
	if !ok {
		return route
	}
	if target.URL != "" {
		route.UpstreamTarget = target.URL
	}
	if target.VaultPath != "" {
		route.VaultPath = target.VaultPath
	}
	if target.InjectAs != nil {
		route.InjectAs = target.InjectAs
	}
	return route
}

func (t *RouteTable) resolveCredential(ctx context.Context, route RouteEntry) ([]byte, error) {
	if route.VaultPath == "" {
		return nil, fmt.Errorf("route has no x-vault-path")
	}
	t.secretMu.Lock()
	client := t.secretClient
	if client == nil {
		var err error
		client, err = vault.NewFromEnv()
		if err != nil {
			t.secretMu.Unlock()
			return nil, err
		}
		t.secretClient = client
	}
	t.secretMu.Unlock()

	secret, err := client.GetSecret(ctx, route.VaultPath)
	if err != nil {
		return nil, err
	}
	return credentialValue(secret)
}

func credentialValue(secret vault.Secret) ([]byte, error) {
	for _, key := range []string{"value", "token", "secret", "api_key", "api-key", "key"} {
		if value, ok := secret[key]; ok {
			return secretValueString(value)
		}
	}
	if len(secret) == 1 {
		for _, value := range secret {
			return secretValueString(value)
		}
	}
	return nil, fmt.Errorf("secret has no usable credential field")
}

func secretValueString(value any) ([]byte, error) {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil, fmt.Errorf("secret credential is empty")
		}
		return []byte(typed), nil
	case []byte:
		if len(typed) == 0 {
			return nil, fmt.Errorf("secret credential is empty")
		}
		return append([]byte(nil), typed...), nil
	default:
		return nil, fmt.Errorf("secret credential is not a string")
	}
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
