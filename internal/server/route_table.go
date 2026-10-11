package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ardenone/seam/internal/spec"
	"github.com/ardenone/seam/internal/vault"
	"github.com/ardenone/seam/internal/version"
	"github.com/pb33f/libopenapi/datamodel/high/v3"
	"go.yaml.in/yaml/v4"
)

// RouteTable holds all routes for routing requests to upstream targets.
// It provides efficient lookup of routes based on path template, HTTP method, and API version.
type RouteTable struct {
	routes []RouteEntry

	secretMu                 sync.Mutex
	credentialSource         CredentialSource
	credentialSourceOverride bool
	envCredentials           *envCredentialSource
	fileCredentials          *fileCredentialSource
	// secretClient remains an opaque source-construction hook for existing
	// in-package metrics tests. It is adapted to CredentialSource by the
	// vault-backed implementation before any read occurs.
	secretClient any
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
	// InjectAsList is the normalized multi-credential form. The legacy
	// InjectAs field remains populated for the object form so existing callers
	// and corpus fixtures retain their behavior.
	InjectAsList     []*InjectAs
	StaticInjections []*StaticInjection

	// CredentialSource, CredentialEnv, and CredentialFile identify alternate
	// process or mounted-file credentials. An empty source preserves the legacy
	// vault route.
	CredentialSource string
	CredentialEnv    string
	CredentialFile   string

	// Unscrubbable is true only when the route explicitly acknowledges that an
	// injected response cannot be scanned. The acknowledgement is enumerable
	// through /config/status and is never inferred from a runtime failure.
	Unscrubbable bool

	// BodyMode controls whether request bodies are validated against their
	// OpenAPI schema. Empty preserves the validated default for legacy routes.
	BodyMode string

	// Streaming requests an incremental response path. It is intentionally
	// carried per route so providers that emit SSE are never whole-response
	// buffered, while legacy routes retain their existing buffering behavior.
	Streaming bool

	// CostPerCall is the flat fallback for response-derived metering and the
	// legacy admission-time charge when CostFromResponse is nil.
	CostPerCall *CostPerCallConfig

	// CostFromResponse describes usage counters in the final JSON response or
	// final SSE event. A nil value preserves the legacy flat-cost behavior.
	CostFromResponse *ResponseCostConfig

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

	// FanoutScope carries per-instance scope constraints from x-fanout-scope.
	// The map keys are instance IDs and values are arrays of allowed scope IDs.
	// This is used to filter instances at dispatch time based on the request's
	// effective scope.
	FanoutScope map[string][]string

	// AdapterConfig holds the x-adapter configuration for version migration.
	// When non-nil, this route adapts requests to a targetVersion and applies
	// request/response transforms per Phase 8.2.
	AdapterConfig *AdapterConfig

	// BreakerConfig holds the fragment-root default circuit breaker configuration.
	// Per-instance overrides are stored in each UpstreamMap entry's BreakerConfig.
	// The runtime uses the stricter (more likely to open) value when same-origin
	// instances disagree, and exposes the disagreement at /config/status.
	BreakerConfig *BreakerConfig

	// BreakerDisagreements tracks origins with conflicting breaker configs
	// across different instances of the same upstream. This is populated during
	// route table construction and exposed at /config/status.
	BreakerDisagreements map[Origin][]string

	// LoopGuardConfig holds the loop guard configuration for this route.
	// Per Phase 13.1: protects against repeated identical failing requests.
	LoopGuardConfig *LoopGuardConfig

	// RequiredScopes holds the scope requirements for this route.
	// Per Phase 7: if non-empty, the caller must have at least one of these scopes.
	// Extracted from x-required-scope extension in OpenAPI operation.
	RequiredScopes []string

	// Deprecated holds deprecation metadata for this route.
	// Per Phase 8.3: populated from x-seam-deprecated fragment extension.
	Deprecated *DeprecationInfo

	// CredentialProbeConfig holds the fragment-root credential probe configuration.
	// Per Phase 12: populated from x-credential-probe fragment extension.
	CredentialProbeConfig *CredentialProbeConfig
}

// RouteTarget is the resolved forwarding and injection metadata for one
// upstream-map entry.
type RouteTarget struct {
	URL              string
	VaultPath        string
	InjectAs         *InjectAs
	InjectAsList     []*InjectAs
	StaticInjections []*StaticInjection

	// BreakerConfig is the per-instance override for circuit breaker configuration.
	// If nil, the fragment-root BreakerConfig is used.
	BreakerConfig *BreakerConfig

	// RequiredScopes holds the scope requirements for this specific instance.
	// Per Phase 7: callers must have at least one of these scopes to access
	// this instance. The effective requirement is the union of operation-level
	// requiredScopes and this instance's RequiredScopes.
	RequiredScopes []string

	// ProbeInterval is the per-instance probe interval override.
	// Per Phase 12: populated from x-upstream-map entry's probeInterval field.
	// Zero means use the fragment-root default from CredentialProbeConfig.
	ProbeInterval time.Duration
}

// DeprecationInfo holds deprecation metadata for a route.
// Per Phase 8.3: populated from x-seam-deprecated fragment extension.
type DeprecationInfo struct {
	// Since is the ISO date when deprecation was declared (YYYY-MM-DD).
	Since string

	// Sunset is the optional ISO date when the route will be removed (YYYY-MM-DD).
	// Empty string means no sunset date is set.
	Sunset string

	// Brownouts is an array of scheduled brownout windows.
	// Each window is a period where the route returns 410 Gone.
	// Windows must be ordered and non-overlapping.
	Brownouts []BrownoutWindow

	// ReplacementPath is the optional path to the replacement route.
	// Populated from fragment metadata or inferred from version migration.
	ReplacementPath string

	// ReplacementVersion is the optional API version of the replacement route.
	ReplacementVersion string
}

// BrownoutWindow represents a scheduled brownout period.
type BrownoutWindow struct {
	// Start is the RFC 3339 date-time when the brownout begins.
	Start string

	// End is the RFC 3339 date-time when the brownout ends.
	End string
}

// parseWindowInstant parses an RFC 3339 instant the way brownout windows are
// compared: as an absolute instant, so a window written with a non-UTC offset
// is exactly equal to its UTC rendering and the gateway's own local timezone
// never participates. The lenient separators the lint side accepts
// (spec.isValidISODateTime: a space or lower-case 't' separator, a lower-case
// 'z' zone) are normalised first, so every window lint passes is also
// parseable here. The bool is false for anything unparseable; callers treat
// that as inert (never active) — the fail-safe direction.
func parseWindowInstant(datetime string) (time.Time, bool) {
	normalized := datetime
	if len(normalized) >= 11 && (normalized[10] == ' ' || normalized[10] == 't') {
		normalized = normalized[:10] + "T" + normalized[11:]
	}
	if strings.HasSuffix(normalized, "z") {
		normalized = normalized[:len(normalized)-1] + "Z"
	}
	t, err := time.Parse(time.RFC3339, normalized)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// IsActiveAt reports whether the brownout window is active at the given time.
// Both boundaries are inclusive: [start, end]. A window that does not parse
// is never active — the fail-safe direction; lint is the up-front gate that
// rejects malformed windows before they reach the gateway.
func (w BrownoutWindow) IsActiveAt(t time.Time) bool {
	start, ok := parseWindowInstant(w.Start)
	if !ok {
		return false
	}
	end, ok := parseWindowInstant(w.End)
	if !ok {
		return false
	}
	return (t.Equal(start) || t.After(start)) && (t.Before(end) || t.Equal(end))
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

// MatchForLoopGuard resolves the route a loop-guard check applies to, without
// any of the side effects Match carries. Route matching normally runs inside
// the caller mux, after every middleware, so a middleware cannot read a match
// published into the request context and has to resolve the route itself. This
// read deliberately neither sanitises the request nor publishes a match into
// it, so the authoritative stage-4 match is left untouched.
func (h *ThreadSafeTableHolder) MatchForLoopGuard(req *http.Request) *RouteMatch {
	if h == nil {
		return nil
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.current.loopGuardMatch(req)
}

// MatchForBrownout resolves the route a brownout-window check applies to,
// with the same no-side-effects contract as MatchForLoopGuard: no request
// sanitisation and no publication into the request context, so the
// authoritative stage-4 match is left untouched. The brownout middleware
// shares the loop guard's constraint — it runs on the caller chain before
// dispatch has published a match it could read.
func (h *ThreadSafeTableHolder) MatchForBrownout(req *http.Request) *RouteMatch {
	return h.MatchForLoopGuard(req)
}

// Snapshot returns a copy of the current route table's routes for inspection.
// The returned routes are a snapshot at the time of the call and are not
// affected by subsequent swaps.
func (h *ThreadSafeTableHolder) Snapshot() []RouteEntry {
	if h == nil {
		return nil
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.current == nil {
		return nil
	}
	return h.current.GetRoutes()
}

func (h *ThreadSafeTableHolder) currentTable() *RouteTable {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

// RouteCount returns the number of routes in the current route table. A nil
// holder or an uninitialized table reports zero, which is the readiness
// gate's not-ready signal for a server that has no valid fragments.
func (h *ThreadSafeTableHolder) RouteCount() int {
	if h == nil {
		return 0
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.current == nil {
		return 0
	}
	return h.current.RouteCount()
}

// OpenBaoCacheStats returns the OpenBao cache statistics from the current route table.
func (h *ThreadSafeTableHolder) OpenBaoCacheStats() vault.CacheStats {
	if h == nil {
		return vault.CacheStats{}
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.current == nil {
		return vault.CacheStats{}
	}
	return h.current.OpenBaoCacheStats()
}

// GetVersionsForPath returns all API versions available for a given path and method.
// If method is empty, returns versions for all methods at that path.
// The returned versions are sorted from oldest to newest (by rank).
// Returns empty slice if the holder is nil or the table is not initialized.
func (h *ThreadSafeTableHolder) GetVersionsForPath(path string, method string) []string {
	if h == nil {
		return []string{}
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.current == nil {
		return []string{}
	}

	return h.current.GetVersionsForPath(path, method)
}

// BuildRouteTable creates a populated RouteTable from an OpenAPI v3 document.
// The route table contains the five HTTP methods SEAM currently proxies. A
// missing operation-level x-api-version extension is represented as v1.
func BuildRouteTable(spec *v3.Document) (*RouteTable, error) {
	return buildRouteTable(spec, nil)
}

// buildRouteTable optionally reuses the process-start environment snapshot.
// Hot reload must not reread credentials from the process environment.
func buildRouteTable(spec *v3.Document, envSnapshot *envCredentialSource) (*RouteTable, error) {
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
			credentialSource, err := extractStringExtension(methodOp.operation, pathItem, spec, "x-credential-source")
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			credentialEnv, err := extractStringExtension(methodOp.operation, pathItem, spec, "x-credential-env")
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			credentialFile, err := extractStringExtension(methodOp.operation, pathItem, spec, "x-credential-file")
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			if credentialSource != "" && credentialSource != credentialSourceVault && credentialSource != credentialSourceEnv && credentialSource != credentialSourceFile {
				return nil, fmt.Errorf("OpenAPI operation %s %s: x-credential-source %q is not supported", methodOp.method, path, credentialSource)
			}
			if credentialSource == credentialSourceEnv && (credentialEnv == "" || credentialFile != "" || vaultPath != "") {
				return nil, fmt.Errorf("OpenAPI operation %s %s: env credentials require x-credential-env and cannot declare x-credential-file or x-vault-path", methodOp.method, path)
			}
			if credentialSource == credentialSourceFile && (credentialFile == "" || credentialEnv != "" || vaultPath != "") {
				return nil, fmt.Errorf("OpenAPI operation %s %s: file credentials require x-credential-file and cannot declare x-credential-env or x-vault-path", methodOp.method, path)
			}
			if credentialSource == credentialSourceVault && (vaultPath == "" || credentialEnv != "" || credentialFile != "") {
				return nil, fmt.Errorf("OpenAPI operation %s %s: vault credentials require x-vault-path and cannot declare x-credential-env or x-credential-file", methodOp.method, path)
			}
			if credentialSource == "" && credentialEnv != "" {
				return nil, fmt.Errorf("OpenAPI operation %s %s: x-credential-env requires x-credential-source: env", methodOp.method, path)
			}
			if credentialSource == "" && credentialFile != "" {
				return nil, fmt.Errorf("OpenAPI operation %s %s: x-credential-file requires x-credential-source: file", methodOp.method, path)
			}
			if credentialSource == credentialSourceFile && !credentialFileNamePattern.MatchString(credentialFile) {
				return nil, fmt.Errorf("OpenAPI operation %s %s: x-credential-file must be a SEAM_CRED_<OWNER>_<NAME> filename", methodOp.method, path)
			}
			injectAs, injectAsList, err := extractInjectAs(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			staticInjections, err := extractStaticInjections(methodOp.operation, pathItem, spec)
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
			upstreamMap, err := extractUpstreamMapWithProbeIntervals(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			fanoutScope, err := extractFanoutScope(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}

			// Extract fragment-root breaker configuration
			breakerConfig, err := extractBreakerConfig(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}

			// Detect same-origin disagreements if using upstream map
			var breakerDisagreements map[Origin][]string
			if len(upstreamMap) > 0 {
				breakerDisagreements, err = detectBreakerDisagreements(upstreamMap, breakerConfig)
				if err != nil {
					return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
				}
			}

			// Extract fragment-root loop guard configuration
			loopGuardConfig, err := extractLoopGuardConfig(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}

			// Extract required scopes (Phase 7)
			requiredScopes, err := extractRequiredScopes(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}

			// Extract credential probe config (Phase 12)
			credentialProbeConfig, err := extractCredentialProbeConfig(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			bodyMode, err := extractStringExtension(methodOp.operation, pathItem, spec, "x-body-mode")
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			if bodyMode == "" {
				bodyMode = "validated"
			}
			if bodyMode != "validated" && bodyMode != "opaque" {
				return nil, fmt.Errorf("OpenAPI operation %s %s: x-body-mode must be validated or opaque", methodOp.method, path)
			}
			streaming, err := extractBoolExtension(methodOp.operation, pathItem, spec, "x-streaming")
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			costFromResponse, err := extractResponseCostConfig(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			costPerCall, err := extractCostPerCallConfig(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}

			// Extract deprecation information (Phase 8.3)
			deprecated, err := extractDeprecation(methodOp.operation, pathItem, spec)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI operation %s %s: %w", methodOp.method, path, err)
			}
			entry := RouteEntry{
				PathTemplate:          path,
				Method:                methodOp.method,
				APIVersion:            apiVersion,
				UpstreamTarget:        upstreamTarget,
				TLSConfig:             tlsConfig,
				VaultPath:             vaultPath,
				InjectAs:              injectAs,
				InjectAsList:          injectAsList,
				StaticInjections:      staticInjections,
				CredentialSource:      credentialSource,
				CredentialEnv:         credentialEnv,
				CredentialFile:        credentialFile,
				Unscrubbable:          unscrubbable,
				BodyMode:              bodyMode,
				Streaming:             streaming,
				CostPerCall:           costPerCall,
				CostFromResponse:      costFromResponse,
				InstanceParam:         instanceParam,
				UpstreamPathTemplate:  upstreamPathTemplate,
				Deprecated:            deprecated,
				UpstreamStripPrefix:   upstreamStripPrefix,
				UpstreamMap:           upstreamMap,
				FanoutScope:           fanoutScope,
				BreakerConfig:         breakerConfig,
				BreakerDisagreements:  breakerDisagreements,
				LoopGuardConfig:       loopGuardConfig,
				RequiredScopes:        requiredScopes,
				CredentialProbeConfig: credentialProbeConfig,
			}

			if err := addBuiltRoute(table, seen, entry); err != nil {
				return nil, err
			}
		}
	}

	envNames := make([]string, 0)
	for _, route := range table.routes {
		if route.CredentialSource == credentialSourceEnv {
			envNames = append(envNames, route.CredentialEnv)
		}
		for _, injection := range route.injections() {
			if injection == nil {
				continue
			}
			source := injection.CredentialSource
			if source == "" && injection.CredentialEnv != "" {
				source = credentialSourceEnv
			}
			if source == credentialSourceEnv {
				envNames = append(envNames, injection.CredentialEnv)
			}
		}
	}
	if envSnapshot == nil {
		table.envCredentials = newEnvCredentialSource(envNames)
	} else {
		for _, name := range envNames {
			if !envSnapshot.hasReference(name) {
				return nil, fmt.Errorf("route references env credential %q that was not captured at process start; restart SEAM to load it", name)
			}
		}
		table.envCredentials = envSnapshot
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

func extractBoolExtension(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document, name string) (bool, error) {
	node, ok := firstExtension(operation, pathItem, document, name)
	if !ok || node == nil {
		return false, nil
	}
	var value bool
	if err := node.Decode(&value); err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}
	return value, nil
}

func extractResponseCostConfig(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) (*ResponseCostConfig, error) {
	node, ok := firstExtension(operation, pathItem, document, "x-cost-from-response")
	if !ok || node == nil {
		return nil, nil
	}
	var raw any
	if err := node.Decode(&raw); err != nil {
		return nil, fmt.Errorf("x-cost-from-response must be an object or list: %w", err)
	}
	config, err := parseResponseCostConfig(raw)
	if err != nil {
		return nil, err
	}
	return config, nil
}

func extractCostPerCallConfig(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) (*CostPerCallConfig, error) {
	node, ok := firstExtension(operation, pathItem, document, "x-cost-per-call")
	if !ok || node == nil {
		return nil, nil
	}
	var raw any
	if err := node.Decode(&raw); err != nil {
		return nil, fmt.Errorf("x-cost-per-call must be an object: %w", err)
	}
	return parseCostPerCallConfig(raw)
}

func extractInjectAs(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) (*InjectAs, []*InjectAs, error) {
	node, ok := firstExtension(operation, pathItem, document, "x-inject-as")
	if !ok || node == nil {
		return nil, nil, nil
	}
	var raw any
	if err := node.Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("x-inject-as must be an object or list: %w", err)
	}
	values, isList := raw.([]any)
	if !isList {
		values = []any{raw}
	}
	parsed := make([]*InjectAs, 0, len(values))
	for index, item := range values {
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, nil, fmt.Errorf("x-inject-as entry %d is invalid: %w", index, err)
		}
		var value struct {
			Kind             InjectionKind `yaml:"kind" json:"kind"`
			Name             string        `yaml:"name" json:"name"`
			Prefix           string        `yaml:"prefix" json:"prefix"`
			TokenURL         string        `yaml:"tokenUrl" json:"tokenUrl"`
			ClientID         string        `yaml:"clientId" json:"clientId"`
			TokenField       string        `yaml:"tokenField" json:"tokenField"`
			CredentialSource string        `yaml:"source" json:"source"`
			VaultPath        string        `yaml:"vaultPath" json:"vaultPath"`
			CredentialEnv    string        `yaml:"credentialEnv" json:"credentialEnv"`
			CredentialFile   string        `yaml:"credentialFile" json:"credentialFile"`
		}
		if err := json.Unmarshal(encoded, &value); err != nil {
			return nil, nil, fmt.Errorf("x-inject-as entry %d must be an object: %w", index, err)
		}
		injectAs := &InjectAs{Kind: value.Kind, Name: value.Name, Prefix: value.Prefix,
			TokenURL: value.TokenURL, ClientID: value.ClientID, TokenField: value.TokenField,
			CredentialSource: value.CredentialSource, VaultPath: value.VaultPath,
			CredentialEnv: value.CredentialEnv, CredentialFile: value.CredentialFile}
		if err := injectAs.validate(); err != nil {
			return nil, nil, fmt.Errorf("x-inject-as entry %d: %w", index, err)
		}
		parsed = append(parsed, injectAs)
	}
	if isList {
		return nil, parsed, nil
	}
	return parsed[0], nil, nil
}

func decodeInjectAsRaw(raw json.RawMessage) (*InjectAs, []*InjectAs, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil, nil
	}
	var values []json.RawMessage
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, nil, fmt.Errorf("injectAs must be an object or list: %w", err)
		}
	} else {
		values = []json.RawMessage{raw}
	}
	parsed := make([]*InjectAs, 0, len(values))
	for index, encoded := range values {
		var value struct {
			Kind             InjectionKind `json:"kind"`
			Name             string        `json:"name"`
			Prefix           string        `json:"prefix"`
			TokenURL         string        `json:"tokenUrl"`
			ClientID         string        `json:"clientId"`
			TokenField       string        `json:"tokenField"`
			CredentialSource string        `json:"source"`
			VaultPath        string        `json:"vaultPath"`
			CredentialEnv    string        `json:"credentialEnv"`
			CredentialFile   string        `json:"credentialFile"`
		}
		if err := json.Unmarshal(encoded, &value); err != nil {
			return nil, nil, fmt.Errorf("injectAs entry %d must be an object: %w", index, err)
		}
		injection := &InjectAs{Kind: value.Kind, Name: value.Name, Prefix: value.Prefix,
			TokenURL: value.TokenURL, ClientID: value.ClientID, TokenField: value.TokenField,
			CredentialSource: value.CredentialSource, VaultPath: value.VaultPath,
			CredentialEnv: value.CredentialEnv, CredentialFile: value.CredentialFile}
		if err := injection.validate(); err != nil {
			return nil, nil, fmt.Errorf("injectAs entry %d: %w", index, err)
		}
		parsed = append(parsed, injection)
	}
	if len(raw) > 0 && raw[0] == '[' {
		return nil, parsed, nil
	}
	return parsed[0], nil, nil
}

func extractStaticInjections(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) ([]*StaticInjection, error) {
	node, ok := firstExtension(operation, pathItem, document, "x-inject-static")
	if !ok || node == nil {
		return nil, nil
	}
	var raw any
	if err := node.Decode(&raw); err != nil {
		return nil, fmt.Errorf("x-inject-static must be an object or list: %w", err)
	}
	values, isList := raw.([]any)
	if !isList {
		values = []any{raw}
	}
	result := make([]*StaticInjection, 0, len(values))
	for index, item := range values {
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("x-inject-static entry %d is invalid: %w", index, err)
		}
		var value struct {
			Kind  InjectionKind `json:"kind"`
			Name  string        `json:"name"`
			Value string        `json:"value"`
		}
		if err := json.Unmarshal(encoded, &value); err != nil {
			return nil, fmt.Errorf("x-inject-static entry %d must be an object: %w", index, err)
		}
		injection := &StaticInjection{Kind: value.Kind, Name: value.Name, Value: value.Value}
		if err := injection.validate(); err != nil {
			return nil, fmt.Errorf("x-inject-static entry %d: %w", index, err)
		}
		result = append(result, injection)
	}
	return result, nil
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

//nolint:unused // staged for the x-upstream-map wiring (phase 5.2)
func extractUpstreamMap(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) (map[string]RouteTarget, error) {
	node, ok := firstExtension(operation, pathItem, document, "x-upstream-map")
	if !ok || node == nil {
		return nil, nil
	}
	var raw map[string]struct {
		URL           string                 `yaml:"url" json:"url"`
		VaultPath     string                 `yaml:"vaultPath" json:"vaultPath"`
		InjectAs      json.RawMessage        `yaml:"injectAs" json:"injectAs"`
		Breaker       map[string]interface{} `yaml:"breaker" json:"breaker"`
		RequiredScope []string               `yaml:"requiredScope" json:"requiredScope"`
	}
	if err := node.Decode(&raw); err != nil {
		return nil, fmt.Errorf("x-upstream-map must be an object: %w", err)
	}
	result := make(map[string]RouteTarget, len(raw))
	for key, value := range raw {
		if strings.TrimSpace(value.URL) == "" {
			return nil, fmt.Errorf("x-upstream-map entry %q is missing url", key)
		}
		injectAs, injectAsList, err := decodeInjectAsRaw(value.InjectAs)
		if err != nil {
			return nil, fmt.Errorf("x-upstream-map entry %q: %w", key, err)
		}

		// Extract per-instance breaker config if present
		var breakerConfig *BreakerConfig
		if len(value.Breaker) > 0 {
			config, err := parseBreakerConfig(value.Breaker)
			if err != nil {
				return nil, fmt.Errorf("x-upstream-map entry %q: %w", key, err)
			}
			breakerConfig = &config
		}

		result[key] = RouteTarget{
			URL:            strings.TrimSpace(value.URL),
			VaultPath:      value.VaultPath,
			InjectAs:       injectAs,
			InjectAsList:   injectAsList,
			BreakerConfig:  breakerConfig,
			RequiredScopes: value.RequiredScope,
		}
	}
	return result, nil
}

// extractBreakerConfig extracts the fragment-root x-breaker configuration.
// Returns nil if x-breaker is not present (uses default).
func extractBreakerConfig(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) (*BreakerConfig, error) {
	node, ok := firstExtension(operation, pathItem, document, "x-breaker")
	if !ok || node == nil {
		// No breaker config specified, use defaults
		config := DefaultBreakerConfig()
		return &config, nil
	}

	var raw map[string]interface{}
	if err := node.Decode(&raw); err != nil {
		return nil, fmt.Errorf("x-breaker must be an object: %w", err)
	}

	config, err := parseBreakerConfig(raw)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

// parseBreakerConfig parses a raw map into a BreakerConfig.
func parseBreakerConfig(raw map[string]interface{}) (BreakerConfig, error) {
	config := DefaultBreakerConfig()

	// Parse threshold if present
	if val, ok := raw["threshold"]; ok {
		switch v := val.(type) {
		case int:
			config.Threshold = v
		case float64:
			config.Threshold = int(v)
		default:
			return BreakerConfig{}, fmt.Errorf("threshold must be an integer")
		}
	}

	// Parse openSeconds if present
	if val, ok := raw["openSeconds"]; ok {
		switch v := val.(type) {
		case int:
			config.OpenSeconds = v
		case float64:
			config.OpenSeconds = int(v)
		default:
			return BreakerConfig{}, fmt.Errorf("openSeconds must be an integer")
		}
	}

	// Parse maxOpenSeconds if present
	if val, ok := raw["maxOpenSeconds"]; ok {
		switch v := val.(type) {
		case int:
			config.MaxOpenSeconds = v
		case float64:
			config.MaxOpenSeconds = int(v)
		default:
			return BreakerConfig{}, fmt.Errorf("maxOpenSeconds must be an integer")
		}
	}

	// Parse enabled if present
	if val, ok := raw["enabled"]; ok {
		switch v := val.(type) {
		case bool:
			config.Enabled = v
		default:
			return BreakerConfig{}, fmt.Errorf("enabled must be a boolean")
		}
	}

	// Validate thresholds
	if config.Threshold < 1 {
		return BreakerConfig{}, fmt.Errorf("threshold must be >= 1")
	}
	if config.OpenSeconds < 1 {
		return BreakerConfig{}, fmt.Errorf("openSeconds must be >= 1")
	}
	if config.MaxOpenSeconds < 1 {
		return BreakerConfig{}, fmt.Errorf("maxOpenSeconds must be >= 1")
	}
	if config.OpenSeconds > config.MaxOpenSeconds {
		return BreakerConfig{}, fmt.Errorf("openSeconds cannot exceed maxOpenSeconds")
	}

	return config, nil
}

// extractLoopGuardConfig extracts the fragment-root x-loop-guard configuration.
// Returns nil if x-loop-guard is not present (uses default).
func extractLoopGuardConfig(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) (*LoopGuardConfig, error) {
	node, ok := firstExtension(operation, pathItem, document, "x-loop-guard")
	if !ok || node == nil {
		// No loop guard config specified, return nil (disabled by default)
		return nil, nil
	}

	var raw map[string]interface{}
	if err := node.Decode(&raw); err != nil {
		return nil, fmt.Errorf("x-loop-guard must be an object: %w", err)
	}

	config, err := parseLoopGuardConfig(raw)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

// parseLoopGuardConfig parses a raw map into a LoopGuardConfig.
func parseLoopGuardConfig(raw map[string]interface{}) (LoopGuardConfig, error) {
	config := LoopGuardConfig{}

	// Parse maxRepeats (required)
	if val, ok := raw["maxRepeats"]; ok {
		switch v := val.(type) {
		case int:
			config.MaxRepeats = v
		case float64:
			config.MaxRepeats = int(v)
		default:
			return LoopGuardConfig{}, fmt.Errorf("maxRepeats must be an integer")
		}
	} else {
		return LoopGuardConfig{}, fmt.Errorf("maxRepeats is required")
	}

	// Parse window (required)
	if val, ok := raw["window"]; ok {
		switch v := val.(type) {
		case string:
			config.Window = v
		default:
			return LoopGuardConfig{}, fmt.Errorf("window must be a string")
		}
	} else {
		return LoopGuardConfig{}, fmt.Errorf("window is required")
	}

	// Validate the configuration
	if err := config.Validate(); err != nil {
		return LoopGuardConfig{}, err
	}

	return config, nil
}

// detectBreakerDisagreements checks if different instances targeting the same
// origin have conflicting breaker configurations. Returns a map of origin to
// list of instances with conflicting configs.
func detectBreakerDisagreements(upstreamMap map[string]RouteTarget, fragmentConfig *BreakerConfig) (map[Origin][]string, error) {
	disagreements := make(map[Origin][]string)

	// Group instances by origin and track their configs
	originConfigs := make(map[Origin]map[string]BreakerConfig) // origin -> instance -> config

	for instanceID, target := range upstreamMap {
		// Get the config for this instance (per-instance override or fragment default)
		config := fragmentConfig
		if target.BreakerConfig != nil {
			config = target.BreakerConfig
		}
		if config == nil {
			defaultConfig := DefaultBreakerConfig()
			config = &defaultConfig
		}

		origin, err := ParseOrigin(target.URL)
		if err != nil {
			return nil, fmt.Errorf("instance %s: parse origin: %w", instanceID, err)
		}

		if originConfigs[origin] == nil {
			originConfigs[origin] = make(map[string]BreakerConfig)
		}
		originConfigs[origin][instanceID] = *config
	}

	// Check for disagreements within each origin
	for origin, instanceConfigs := range originConfigs {
		if len(instanceConfigs) < 2 {
			continue // No disagreement if only one instance targets this origin
		}

		// Find the first config and compare all others against it
		var firstInstance string
		var firstConfig BreakerConfig
		for instance, config := range instanceConfigs {
			firstInstance = instance
			firstConfig = config
			break
		}

		conflictingInstances := []string{}
		for instance, config := range instanceConfigs {
			if instance == firstInstance {
				continue
			}
			if config.Disagreement(firstConfig) {
				conflictingInstances = append(conflictingInstances, instance)
			}
		}

		if len(conflictingInstances) > 0 {
			// Include all instances involved in the disagreement
			allInvolved := make([]string, 0, len(instanceConfigs))
			for instance := range instanceConfigs {
				allInvolved = append(allInvolved, instance)
			}
			disagreements[origin] = allInvolved
		}
	}

	return disagreements, nil
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

// OpenBaoCacheStats exposes only aggregate, non-secret cache counters to the
// Prometheus collector. A route table that has not fetched a credential yet
// reports zeros.
func (t *RouteTable) OpenBaoCacheStats() vault.CacheStats {
	if t == nil {
		return vault.CacheStats{}
	}
	t.secretMu.Lock()
	source := t.credentialSource
	if source == nil && t.secretClient != nil {
		source = newVaultCredentialSourceFromLegacyClient(t.secretClient)
		t.credentialSource = source
	}
	t.secretMu.Unlock()
	stats, ok := source.(credentialCacheStatsProvider)
	if !ok {
		return vault.CacheStats{}
	}
	return stats.CacheStats()
}

// SetCredentialSource injects the source used by future request matches. It is
// primarily a test seam; production tables lazily construct the vault source.
func (t *RouteTable) SetCredentialSource(source CredentialSource) {
	if t == nil {
		return
	}
	t.secretMu.Lock()
	t.credentialSource = source
	t.credentialSourceOverride = source != nil
	t.secretMu.Unlock()
}

func (t *RouteTable) envCredentialSnapshot() *envCredentialSource {
	if t == nil {
		return nil
	}
	t.secretMu.Lock()
	defer t.secretMu.Unlock()
	return t.envCredentials
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
			withRouteMatch(req, match, routeTableCredentialResolver{table: t})
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
	withRouteMatch(req, selected, routeTableCredentialResolver{table: t})
	return selected, nil
}

// loopGuardMatch is Match with the request sanitisation and context publication
// removed, so a caller that only needs the route entry can resolve it without
// mutating the request. Selection mirrors Match: X-SEAM-API-Version pins the
// version when present, otherwise the highest-ranked version wins.
func (t *RouteTable) loopGuardMatch(req *http.Request) *RouteMatch {
	if t == nil || req == nil || req.URL == nil {
		return nil
	}

	requestedVersion := req.Header.Get("X-SEAM-API-Version")
	method := strings.ToUpper(req.Method)
	requestPath := req.URL.EscapedPath()
	if requestPath == "" {
		requestPath = req.URL.Path
	}

	var selected *RouteEntry
	var selectedParams map[string]string
	selectedRank := int(^uint(0) >> 1)
	for i := range t.routes {
		route := &t.routes[i]
		if strings.ToUpper(route.Method) != method {
			continue
		}
		if requestedVersion != "" && route.APIVersion != requestedVersion {
			continue
		}
		pathParams, ok := matchRoutePath(route.PathTemplate, requestPath)
		if !ok {
			continue
		}
		if requestedVersion != "" {
			return &RouteMatch{Route: *route, PathParams: pathParams}
		}
		if rank := routeVersionRank(route.APIVersion); rank < selectedRank {
			selected = route
			selectedRank = rank
			selectedParams = pathParams
		}
	}

	if selected == nil {
		return nil
	}
	return &RouteMatch{Route: *selected, PathParams: selectedParams}
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

// InjectableNames returns the injectable header and query parameter names for
// a given request. This is used by the capture middleware to identify which
// request headers and query parameters contain injected credentials that should
// be redacted from captured corpus data.
func (t *RouteTable) InjectableNames(r *http.Request) (map[string]bool, map[string]bool) {
	if t == nil || r == nil || r.URL == nil {
		return nil, nil
	}

	method := strings.ToUpper(r.Method)
	path := r.URL.EscapedPath()
	if path == "" {
		path = r.URL.Path
	}

	for _, route := range t.routes {
		if strings.ToUpper(route.Method) != method {
			continue
		}
		if _, ok := matchRoutePath(route.PathTemplate, path); !ok {
			continue
		}

		// Found a matching route - extract injectable names
		headerNames := make(map[string]bool)
		queryNames := make(map[string]bool)

		for name := range route.injectableHeaderNames() {
			headerNames[name] = true
		}
		for name := range route.injectableQueryNames() {
			queryNames[name] = true
		}

		return headerNames, queryNames
	}

	return nil, nil
}

// injectableHeaderNames returns the header names a route fills with an
// injected credential, keyed in lower case. Header names are case-insensitive,
// and redactCapturedHeaders looks entries up by the lower-cased header name
// from the request, so a route configured as "X-Partner-Credential" must still
// match a request carrying "x-partner-credential". Query names are compared
// verbatim by redactCapturedQuery and stay case-sensitive.
func (route RouteEntry) injectableHeaderNames() map[string]struct{} {
	result := make(map[string]struct{})
	add := func(injectAs *InjectAs) {
		if name := injectAs.headerName(); name != "" {
			result[strings.ToLower(name)] = struct{}{}
		}
	}
	for _, injectAs := range route.injections() {
		add(injectAs)
	}
	for _, target := range route.UpstreamMap {
		for _, injectAs := range target.injections() {
			add(injectAs)
		}
	}
	return result
}

func (route RouteEntry) injectableQueryNames() map[string]struct{} {
	result := make(map[string]struct{})
	add := func(injectAs *InjectAs) {
		if name := injectAs.queryName(); name != "" {
			result[name] = struct{}{}
		}
	}
	for _, injectAs := range route.injections() {
		add(injectAs)
	}
	for _, target := range route.UpstreamMap {
		for _, injectAs := range target.injections() {
			add(injectAs)
		}
	}
	return result
}

func (route RouteEntry) injections() []*InjectAs {
	if route.InjectAsList != nil {
		return route.InjectAsList
	}
	if route.InjectAs == nil {
		return nil
	}
	return []*InjectAs{route.InjectAs}
}

func (target RouteTarget) injections() []*InjectAs {
	if target.InjectAsList != nil {
		return target.InjectAsList
	}
	if target.InjectAs == nil {
		return nil
	}
	return []*InjectAs{target.InjectAs}
}

func (route RouteEntry) staticHeaderNames() map[string]struct{} {
	result := make(map[string]struct{})
	for _, injection := range route.StaticInjections {
		if injection != nil && injection.Kind == InjectionHeader {
			result[injection.Name] = struct{}{}
		}
	}
	for _, target := range route.UpstreamMap {
		for _, injection := range target.StaticInjections {
			if injection != nil && injection.Kind == InjectionHeader {
				result[injection.Name] = struct{}{}
			}
		}
	}
	return result
}

func (route RouteEntry) staticQueryNames() map[string]struct{} {
	result := make(map[string]struct{})
	for _, injection := range route.StaticInjections {
		if injection != nil && injection.Kind == InjectionQuery {
			result[injection.Name] = struct{}{}
		}
	}
	for _, target := range route.UpstreamMap {
		for _, injection := range target.StaticInjections {
			if injection != nil && injection.Kind == InjectionQuery {
				result[injection.Name] = struct{}{}
			}
		}
	}
	return result
}

func (route RouteEntry) effectiveTarget(pathParams map[string]string) RouteEntry {
	if route.InstanceParam == "" || len(route.UpstreamMap) == 0 {
		return route
	}
	instance := pathParams[route.InstanceParam]

	// _all is a special value that triggers fan-out mode
	// The route entry is returned unchanged - fan-out is handled at dispatch time
	if instance == "_all" {
		return route
	}

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
	if target.InjectAsList != nil {
		route.InjectAs = nil
		route.InjectAsList = target.InjectAsList
	}
	if target.StaticInjections != nil {
		route.StaticInjections = target.StaticInjections
	}
	return route
}

type routeTableCredentialResolver struct{ table *RouteTable }

type routeTableHolderCredentialResolver struct{ holder *ThreadSafeTableHolder }

func (r routeTableHolderCredentialResolver) ResolveAll(ctx context.Context, route RouteEntry) ([][]byte, error) {
	if r.holder == nil {
		return nil, fmt.Errorf("route table holder is not initialized")
	}
	return r.holder.currentTable().resolveCredentials(ctx, route)
}

func (r routeTableHolderCredentialResolver) RefreshableAll(ctx context.Context, route RouteEntry) ([][]byte, error) {
	if r.holder == nil {
		return nil, fmt.Errorf("route table holder is not initialized")
	}
	return r.holder.currentTable().refreshCredentials(ctx, route)
}

func (r routeTableHolderCredentialResolver) Resolve(ctx context.Context, route RouteEntry) ([]byte, error) {
	if r.holder == nil {
		return nil, fmt.Errorf("route table holder is not initialized")
	}
	return routeTableCredentialResolver{table: r.holder.currentTable()}.Resolve(ctx, route)
}

func (r routeTableHolderCredentialResolver) Refreshable(ctx context.Context, route RouteEntry) ([]byte, error) {
	if r.holder == nil {
		return nil, fmt.Errorf("route table holder is not initialized")
	}
	return routeTableCredentialResolver{table: r.holder.currentTable()}.Refreshable(ctx, route)
}

func (r routeTableCredentialResolver) Resolve(ctx context.Context, route RouteEntry) ([]byte, error) {
	if r.table == nil {
		return nil, fmt.Errorf("route table is not initialized")
	}
	return r.table.resolveCredential(ctx, route)
}

func (r routeTableCredentialResolver) ResolveAll(ctx context.Context, route RouteEntry) ([][]byte, error) {
	if r.table == nil {
		return nil, fmt.Errorf("route table is not initialized")
	}
	return r.table.resolveCredentials(ctx, route)
}

func (r routeTableCredentialResolver) Refreshable(ctx context.Context, route RouteEntry) ([]byte, error) {
	if r.table == nil {
		return nil, fmt.Errorf("route table is not initialized")
	}
	return r.table.refreshCredential(ctx, route)
}

func (r routeTableCredentialResolver) RefreshableAll(ctx context.Context, route RouteEntry) ([][]byte, error) {
	if r.table == nil {
		return nil, fmt.Errorf("route table is not initialized")
	}
	return r.table.refreshCredentials(ctx, route)
}

func (t *RouteTable) sourceForRoute(route RouteEntry) (CredentialSource, error) {
	if t == nil {
		return nil, fmt.Errorf("route table is nil")
	}
	t.secretMu.Lock()
	source := t.credentialSource
	override := t.credentialSourceOverride
	envSource := t.envCredentials
	fileSource := t.fileCredentials
	if override && source != nil {
		t.secretMu.Unlock()
		return source, nil
	}
	if route.CredentialSource == credentialSourceEnv {
		if envSource == nil {
			envSource = newEnvCredentialSource([]string{route.CredentialEnv})
			t.envCredentials = envSource
		}
		t.secretMu.Unlock()
		return envSource, nil
	}
	if route.CredentialSource == credentialSourceFile {
		if fileSource == nil {
			fileSource = newFileCredentialSource(fileCredentialMountDir)
			t.fileCredentials = fileSource
		}
		t.secretMu.Unlock()
		return fileSource, nil
	}
	if source == nil && t.secretClient != nil {
		source = newVaultCredentialSourceFromLegacyClient(t.secretClient)
		t.credentialSource = source
	}
	t.secretMu.Unlock()
	if source == nil {
		var err error
		source, err = newVaultCredentialSourceFromEnv()
		if err != nil {
			return nil, err
		}
		t.secretMu.Lock()
		if t.credentialSource == nil {
			t.credentialSource = source
		} else {
			source = t.credentialSource
		}
		t.secretMu.Unlock()
	}
	return source, nil
}

func (t *RouteTable) resolveCredential(ctx context.Context, route RouteEntry) ([]byte, error) {
	source, err := t.sourceForRoute(route)
	if err != nil {
		return nil, err
	}
	return source.Resolve(ctx, route)
}

func routeForInjection(route RouteEntry, injection *InjectAs) RouteEntry {
	if injection == nil {
		return route
	}
	resolved := route
	resolved.InjectAs = injection
	resolved.InjectAsList = nil
	if injection.CredentialSource != "" {
		resolved.CredentialSource = injection.CredentialSource
	} else if injection.CredentialEnv != "" {
		resolved.CredentialSource = credentialSourceEnv
	} else if injection.CredentialFile != "" {
		resolved.CredentialSource = credentialSourceFile
	} else if injection.VaultPath != "" {
		resolved.CredentialSource = credentialSourceVault
	}
	if injection.VaultPath != "" {
		resolved.VaultPath = injection.VaultPath
	}
	if injection.CredentialEnv != "" {
		resolved.CredentialEnv = injection.CredentialEnv
	}
	if injection.CredentialFile != "" {
		resolved.CredentialFile = injection.CredentialFile
	}
	return resolved
}

func (t *RouteTable) resolveCredentials(ctx context.Context, route RouteEntry) ([][]byte, error) {
	injections := route.injections()
	if len(injections) == 0 {
		return nil, nil
	}
	secrets := make([][]byte, 0, len(injections))
	for _, injection := range injections {
		secret, err := t.resolveCredential(ctx, routeForInjection(route, injection))
		if err != nil {
			return nil, err
		}
		secrets = append(secrets, secret)
	}
	return secrets, nil
}

func (t *RouteTable) refreshCredential(ctx context.Context, route RouteEntry) ([]byte, error) {
	source, err := t.sourceForRoute(route)
	if err != nil {
		return nil, err
	}
	return source.Refreshable(ctx, route)
}

func (t *RouteTable) refreshCredentials(ctx context.Context, route RouteEntry) ([][]byte, error) {
	injections := route.injections()
	if len(injections) == 0 {
		return nil, nil
	}
	secrets := make([][]byte, 0, len(injections))
	for _, injection := range injections {
		secret, err := t.refreshCredential(ctx, routeForInjection(route, injection))
		if err != nil {
			return nil, err
		}
		secrets = append(secrets, secret)
	}
	return secrets, nil
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

// GetVersionsForPath returns all API versions available for a given path and method.
// If method is empty, returns versions for all methods at that path.
// The returned versions are sorted from oldest to newest (by rank).
func (t *RouteTable) GetVersionsForPath(path string, method string) []string {
	seen := make(map[string]bool)
	versions := []string{}

	for _, route := range t.routes {
		// Check if path matches
		if !pathTemplatesMatch(route.PathTemplate, path) {
			continue
		}

		// Check if method matches (or if we're querying all methods)
		if method != "" && strings.ToUpper(route.Method) != method {
			continue
		}

		// Add version if not already seen
		if !seen[route.APIVersion] {
			seen[route.APIVersion] = true
			versions = append(versions, route.APIVersion)
		}
	}

	// Sort versions by rank (oldest first)
	sortVersionsByRank(versions)
	return versions
}

// pathTemplatesMatch checks if a path template matches a concrete path.
// This is a simplified check - it doesn't do full parameter matching.
func pathTemplatesMatch(template, path string) bool {
	// For now, just do exact string matching
	// TODO: implement proper template matching with parameter extraction
	return template == path
}

// sortVersionsByRank sorts version strings by their rank (oldest first).
// Uses the version package's Rank function for proper numerical ordering,
// ensuring _unversioned (rank 0) is always first, followed by v1, v2, etc.
func sortVersionsByRank(versions []string) {
	sort.Slice(versions, func(i, j int) bool {
		rankI := version.Rank(versions[i])
		rankJ := version.Rank(versions[j])
		return rankI < rankJ
	})
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

// IsFanOutRequest checks if the given path parameters indicate a fan-out request.
func (route RouteEntry) IsFanOutRequest(pathParams map[string]string) bool {
	if route.InstanceParam == "" || len(route.UpstreamMap) == 0 {
		return false
	}
	instance := pathParams[route.InstanceParam]
	return instance == "_all"
}

// ShouldFanOut reports whether this route entry supports fan-out mode.
// A route can fan out if it has an instance parameter and an upstream map.
func (route RouteEntry) ShouldFanOut() bool {
	return route.InstanceParam != "" && len(route.UpstreamMap) > 0
}

// GetAllInstanceTargets returns all instance targets from the upstream map.
// This is used when the instance parameter is "_all" to dispatch to all instances.
func (route RouteEntry) GetAllInstanceTargets() map[string]RouteTarget {
	if len(route.UpstreamMap) == 0 {
		return nil
	}
	// Return a copy to prevent mutation of the route table
	result := make(map[string]RouteTarget, len(route.UpstreamMap))
	for key, target := range route.UpstreamMap {
		result[key] = target
	}
	return result
}

// GetFanOutInstanceCount returns the number of instances in the upstream map.
func (route RouteEntry) GetFanOutInstanceCount() int {
	return len(route.UpstreamMap)
}

func extractFanoutScope(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) (map[string][]string, error) {
	node, ok := firstExtension(operation, pathItem, document, "x-fanout-scope")
	if !ok || node == nil {
		return nil, nil
	}
	var raw map[string]struct {
		Scopes []string `yaml:"scopes" json:"scopes"`
	}
	if err := node.Decode(&raw); err != nil {
		return nil, fmt.Errorf("x-fanout-scope must be an object: %w", err)
	}
	result := make(map[string][]string, len(raw))
	for instanceID, scopeEntry := range raw {
		if len(scopeEntry.Scopes) == 0 {
			return nil, fmt.Errorf("x-fanout-scope entry %q: scopes array cannot be empty", instanceID)
		}
		scopes := make([]string, len(scopeEntry.Scopes))
		copy(scopes, scopeEntry.Scopes)
		result[instanceID] = scopes
	}
	return result, nil
}

// extractRequiredScopes extracts the x-required-scope extension from operation.
// Returns nil if no scopes are required (public route).
// Per Phase 7: routes can require specific scopes for access.
func extractRequiredScopes(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) ([]string, error) {
	node, ok := firstExtension(operation, pathItem, document, "x-required-scope")
	if !ok || node == nil {
		return nil, nil
	}

	// Check if it's a single string
	var singleScope string
	if err := node.Decode(&singleScope); err == nil && singleScope != "" {
		return []string{singleScope}, nil
	}

	// Check if it's an array of strings
	var scopeArray []string
	if err := node.Decode(&scopeArray); err != nil {
		return nil, fmt.Errorf("x-required-scope must be a string or array of strings: %w", err)
	}

	if len(scopeArray) == 0 {
		return nil, fmt.Errorf("x-required-scope array cannot be empty")
	}

	// Validate all scopes are non-empty
	for i, scope := range scopeArray {
		if strings.TrimSpace(scope) == "" {
			return nil, fmt.Errorf("x-required-scope array element %d is empty", i)
		}
	}

	return scopeArray, nil
}

// extractDeprecation extracts x-seam-deprecated information from an operation.
// Per Phase 8.3: fragment-root only, not operation-specific.
func extractDeprecation(operation *v3.Operation, pathItem *v3.PathItem, spec *v3.Document) (*DeprecationInfo, error) {
	// x-seam-deprecated is fragment-root only, so check pathItem extensions
	// first: the plain key for a block placed directly on the path item, then
	// the marker PropagateRouteMetadata stamps when the block sat at the
	// fragment root (the form the route-fragment schema sanctions and the
	// retirement evaluator proposes — merge would otherwise drop it). The
	// operation level is deliberately not consulted: x-seam-deprecated covers
	// a whole fragment, not one operation. Both a nil pathItem and a nil
	// Extensions map are ordinary here — a fragment root carries no extensions
	// unless the author set one, and a PathItem built in Go rather than parsed
	// leaves the map nil — so fall through to the operation-level deprecated
	// flag instead of dereferencing.
	var extensionNode *yaml.Node
	var found bool
	if pathItem != nil && pathItem.Extensions != nil {
		extensionNode, found = pathItem.Extensions.Get("x-seam-deprecated")
		if !found {
			extensionNode, found = pathItem.Extensions.Get("x-seam-internal-deprecated")
		}
	}
	if !found || extensionNode == nil {
		// Check if operation has OpenAPI deprecated field
		if operation != nil && operation.Deprecated != nil && *operation.Deprecated {
			// Per-operation deprecated: true honored - return minimal deprecation info
			return &DeprecationInfo{
				Since: "unknown", // OpenAPI deprecated has no since date
			}, nil
		}
		return nil, nil
	}

	// Parse x-seam-deprecated object
	var deprecatedMap map[string]interface{}
	if err := extensionNode.Decode(&deprecatedMap); err != nil {
		return nil, fmt.Errorf("x-seam-deprecated must be an object: %w", err)
	}

	info := &DeprecationInfo{}

	// Extract since (required)
	since, ok := deprecatedMap["since"].(string)
	if !ok || since == "" {
		return nil, fmt.Errorf("x-seam-deprecated.since is required and must be a string")
	}
	info.Since = since

	// Extract sunset (optional)
	if sunset, ok := deprecatedMap["sunset"].(string); ok && sunset != "" {
		info.Sunset = sunset
	}

	// Extract brownout array (optional)
	if brownoutNode := deprecatedMap["brownout"]; brownoutNode != nil {
		brownoutArray, ok := brownoutNode.([]interface{})
		if !ok {
			return nil, fmt.Errorf("x-seam-deprecated.brownout must be an array")
		}

		info.Brownouts = make([]BrownoutWindow, 0, len(brownoutArray))
		for i, window := range brownoutArray {
			windowMap, ok := window.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("x-seam-deprecated.brownout[%d] must be an object", i)
			}

			start, ok := windowMap["start"].(string)
			if !ok || start == "" {
				return nil, fmt.Errorf("x-seam-deprecated.brownout[%d].start is required", i)
			}

			end, ok := windowMap["end"].(string)
			if !ok || end == "" {
				return nil, fmt.Errorf("x-seam-deprecated.brownout[%d].end is required", i)
			}

			info.Brownouts = append(info.Brownouts, BrownoutWindow{
				Start: start,
				End:   end,
			})
		}
	}

	return info, nil
}
