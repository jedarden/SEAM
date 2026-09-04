# SEAM Route Fragment Schema v1 - Go Struct Definitions

**Purpose:** Canonical Go struct definitions with validation tags for SEAM route fragments (OpenAPI 3.1 + SEAM extensions).

**Language:** Go (chosen per ADR-001).
**Validation:** go-playground/validator/v10 tags.

This file defines the complete schema that both `seam lint` (Phase 9) and the gateway's runtime quarantine (Phase 1) will validate against.

---

## Package Structure

```go
package seamapi

import (
	"time"
)
```

---

## Fragment Root Structure

```go
// RouteFragment represents a complete SEAM route fragment.
// It is an OpenAPI 3.1 fragment (paths + optional components) plus SEAM extension fields.
// Validate with validator.New().Struct(fragment) after JSON unmarshaling.
type RouteFragment struct {
	// OpenAPI 3.1 fragment fields
	OpenAPI    string                  `json:"openapi" validate:"required,^3\\.[1-9]\\.[0-9]+"`                  // e.g., "3.1.0"
	Info       *Info                   `json:"info,omitempty"`                                             // Optional metadata
	Paths      map[string]PathItem     `json:"paths" validate:"required,min=1,dive"`                       // /path -> PathItem
	Components *Components             `json:"components,omitempty"`                                       // Optional reusable schemas

	// SEAM extension fields (fragment-root level)
	SEAMOwner        string               `json:"x-seam-owner" validate:"required"`                                    // Owning service <svc>
	SEAMSchema       string               `json:"x-seam-schema" validate:"required,eq=v1"`                        // Schema version marker
	APIVersion       string               `json:"x-api-version,omitempty" validate:"omitempty,^v[1-9][0-9]*$"`    // v1, v2, ...
	Upstream         *string              `json:"x-upstream,omitempty" validate:"omitempty,url,startswith=https://"` // Single upstream URL
	UpstreamMap      map[string]Instance  `json:"x-upstream-map,omitempty"`                                         // Multi-instance mapping
	InstanceParam    string               `json:"x-instance-param,omitempty" validate:"omitempty,^[a-z][a-z0-9_-]*$"` // Path param name
	VaultPath        string               `json:"x-vault-path,omitempty" validate:"omitempty,^[a-z0-9]([a-z0-9-]*[a-z0-9])?(/[a-z0-9]([a-z0-9-]*[a-z0-9])?){2,}$"` // <base>/<owner>/<name>; base is deployment config (SEAM_VAULT_BASE_DIR)
	InjectAs         *InjectAs            `json:"x-inject-as,omitempty"`                                            // How credential injected
	RequiredScope     interface{}          `json:"x-required-scope,omitempty"`                                       // string or []string (fragment-root default)
	CredentialProbe  *CredentialProbe      `json:"x-credential-probe,omitempty"`                                    // Health probe config
	UpstreamTLS      *UpstreamTLS         `json:"x-upstream-tls,omitempty"`                                       // Upstream TLS config
	UpstreamStrip    string               `json:"x-upstream-strip-prefix,omitempty" validate:"omitempty,^/[^/]+$"` // Prefix to strip
	UpstreamPlaintext bool                 `json:"x-upstream-plaintext,omitempty"`                                 // Force HTTP to upstream
	Breaker          *CircuitBreaker      `json:"x-breaker,omitempty"`                                            // Circuit breaker config
	Deprecated       *Deprecation         `json:"x-seam-deprecated,omitempty"`                                     // Deprecation info
	Adapter          *Adapter             `json:"x-adapter,omitempty"`                                            // Contract adapter
	FanoutScope      interface{}          `json:"x-fanout-scope,omitempty"`                                        // string or []string (for _all fan-out)
	Unscrubbable     string               `json:"x-unscrubbable,omitempty" validate:"omitempty,eq=acknowledged"` // Scrubbing opt-out
	RequiresApproval bool                 `json:"x-requires-approval,omitempty"`                                  // Reserved field
}

// Info is a minimal OpenAPI 3.1 Info object (informational in fragments).
type Info struct {
	Title       string `json:"title" validate:"required"`
	Version     string `json:"version" validate:"required"`
	Description string `json:"description,omitempty"`
}

// PathItem represents an OpenAPI 3.1 Path Item object.
type PathItem struct {
	Summary     string                `json:"summary,omitempty" validate:"omitempty"`
	Description string               `json:"description,omitempty" validate:"omitempty"`
	Get        *Operation            `json:"get,omitempty" validate:"omitempty"`
	Put        *Operation            `json:"put,omitempty" validate:"omitempty"`
	Post       *Operation            `json:"post,omitempty" validate:"omitempty"`
	Delete     *Operation            `json:"delete,omitempty" validate:"omitempty"`
	Options    *Operation            `json:"options,omitempty" validate:"omitempty"`
	Head       *Operation            `json:"head,omitempty" validate:"omitempty"`
	Patch      *Operation            `json:"patch,omitempty" validate:"omitempty"`
	Trace      *Operation            `json:"trace,omitempty" validate:"omitempty"`
	Parameters []Parameter           `json:"parameters,omitempty" validate:"omitempty,dive"`
	Servers    []Server              `json:"servers,omitempty" validate:"omitempty,dive"`

	// SEAM path-item-level extensions
	UpstreamPathTemplate string `json:"x-upstream-path-template,omitempty" validate:"omitempty,^/"`
}

// Components represents OpenAPI 3.1 Components object (optional in fragments).
type Components struct {
	Schemas         map[string]interface{} `json:"schemas,omitempty"`
	Parameters      map[string]Parameter    `json:"parameters,omitempty"`
	Responses       map[string]Response    `json:"responses,omitempty"`
	Examples        map[string]Example     `json:"examples,omitempty"`
	RequestBodies   map[string]RequestBody `json:"requestBodies,omitempty"`
	Headers         map[string]Header      `json:"headers,omitempty"`
	SecuritySchemes map[string]SecurityScheme `json:"securitySchemes,omitempty"`
	Links           map[string]Link         `json:"links,omitempty"`
	Callbacks       map[string]Callback     `json:"callbacks,omitempty"`
	PathItems       map[string]PathItem     `json:"pathItems,omitempty"`
}
```

---

## Extension Field Types

### x-seam-owner (required, fragment-root)

```go
// SEAMOwner is validated against the mounted parent directory name.
// A mismatch at merge time quarantines the entire fragment.
// Pattern: ^[a-z][a-z0-9_-]*$ (lowercase, alphanum, hyphen, underscore)
```

### x-seam-schema (required, fragment-root)

```go
// SEAMSchema versions the fragment format itself, distinct from x-api-version.
// Allows detectable evolution: future v2 fragments can coexist with v1.
// Must be "v1" for this specification.
```

### x-api-version (optional, fragment-root)

```go
// APIVersion sorts numerically: v1 < v2 < v10.
// A fragment with no x-api-version is keyed as "_unversioned" (sorts oldest).
// Pattern: ^v[1-9][0-9]*$ (no v0, no dots, no dates).
```

### x-upstream (optional, fragment-root, mutually exclusive with x-upstream-map)

```go
// Upstream is the single base URL this fragment's routes forward to.
// Must be HTTPS, host must resolve inside SEAM's operator-owned upstream-host allowlist.
// Mutually exclusive with x-upstream-map (both present = lint error).
```

### x-upstream-map (optional, fragment-root, mutually exclusive with x-upstream)

```go
// Instance maps instance parameter values to per-instance configurations.
// Required when x-instance-param is present; forbidden when absent.
type Instance struct {
	URL            string        `json:"url" validate:"required,url,startswith=https://"` // Per-instance upstream URL
	VaultPath      string        `json:"vaultPath,omitempty" validate:"omitempty,^[a-z0-9]([a-z0-9-]*[a-z0-9])?(/[a-z0-9]([a-z0-9-]*[a-z0-9])?){2,}$"` // <base>/<owner>/<name>; base is deployment config (SEAM_VAULT_BASE_DIR)
	InjectAs       *InjectAs     `json:"injectAs,omitempty" validate:"omitempty"`
	TLS            *InstanceTLS  `json:"tls,omitempty" validate:"omitempty"`
	Plaintext      string        `json:"plaintext,omitempty" validate:"omitempty,eq=acknowledged"`
	ProbeInterval  string        `json:"probeInterval,omitempty" validate:"omitempty,^[1-9][0-9]*[smhd]$"`
	Breaker        *CircuitBreaker `json:"breaker,omitempty" validate:"omitempty"`
	RequiredScope  []string      `json:"requiredScope,omitempty" validate:"omitempty,dive,^[a-z][a-z0-9_-:]+$"`
}

// Each entry's URL host is bound by the same upstream-host allowlist as x-upstream.
```

### x-instance-param (optional, fragment-root, required when x-upstream-map present)

```go
// InstanceParam is the bare parameter name (e.g., "cluster" for /k8s/{cluster}/pods).
// Required when x-upstream-map is present; forbidden when absent.
//
// SEAM uses the segment bound to this parameter to select the map entry (or "_all").
// The segment is deleted from the computed upstream path before forwarding.
```

### x-vault-path (optional, fragment-root)

```go
// VaultPath specifies which OpenBao secret to fetch.
// Shaped <base>/<x-seam-owner>/<name>: at least one base segment above the
// owner and a name below it. The base prefix is deployment configuration
// (SEAM_VAULT_BASE_DIR, default rs-manager/rs-manager/seam/routes) and is
// deliberately not encoded in the tag — pinning it would turn every base move
// into a schema release. The pre-consolidation base `seam/routes` is retired.
// Example: "rs-manager/rs-manager/seam/routes/argocd-ro/readonly-token"
//
// Lint checks allowlist prefix. Gateway re-checks at merge time.
```

### x-inject-as (optional, fragment-root)

```go
// InjectAs specifies how the fetched credential is injected into upstream requests.
// This is an object, not a bare enum, because it carries both kind and name.
type InjectAs struct {
	Kind  string `json:"kind" validate:"required,oneof=header bearer query"` // Injection method
	Name  string `json:"name" validate:"required"`                            // Header/query param name
}

// Examples:
//   {kind: "header", name: "Authorization"}   // Header: Authorization: Bearer <token>
//   {kind: "bearer", name: "ignored"}         // Header: Authorization: Bearer <token> (name ignored)
//   {kind: "query", name: "api_key"}         // Query: ?api_key= <credential>
```

### x-required-scope (optional, operation-level with optional fragment-root default)

```go
// RequiredScope specifies OIDC scopes a caller must possess.
// This field is operation-level with an optional fragment-root default.
// Any operation-level value replaces (does not merge with) the fragment-root default.
//
// The value is an array of scope strings; a bare string is sugar for a one-element array.
// Scopes are interpreted conjunctively (caller must hold ALL listed scopes).
//
// Examples:
//   fragment-root: ["k8s-ro"]                          // All routes require k8s-ro
//   operation:     ["k8s-ro:delete", "k8s-ro:patch"]  // DELETE/PATCH require more
//
// Scope pattern: ^[a-z][a-z0-9_-:]+$ (lowercase, alphanum, hyphen, underscore, colon)
```

### x-credential-probe (optional, fragment-root)

```go
// CredentialProbe configures a health check for the fragment's secret.
type CredentialProbe struct {
	Path     string `json:"path" validate:"required,^/"`        // e.g., "/api/v1/me"
	Method   string `json:"method" validate:"required,oneof=GET HEAD OPTIONS"` // Safe methods only
	Interval string `json:"interval" validate:"required,^[1-9][0-9]*[smhd]$"`    // 1s to 7d
}

// The probe runs at the configured interval, fetching the secret and making this
// request to the upstream. A 401/403 means the secret is invalid or rotated.
//
// On multi-instance fragments, this is the default interval; each x-upstream-map
// entry can override it via probeInterval (instances may have different rotation cadences).
```

### x-upstream-tls (optional, fragment-root)

```go
// UpstreamTLS configures TLS verification for upstream connections.
type UpstreamTLS struct {
	CABundle           string `json:"caBundle,omitempty" validate:"omitempty,^[a-z][a-z0-9_-]+$"`  // PEM key in seam-upstream-ca ConfigMap
	ServerName         string `json:"serverName,omitempty" validate:"omitempty,fqdn"`             // SNI override
	InsecureSkipVerify string `json:"insecureSkipVerify,omitempty" validate:"omitempty,eq=acknowledged"` // Must be "acknowledged"
}

// Absence means: verify against system trust store with hostname checking (default).
//
// caBundle names a PEM key in SEAM's manifest-owned seam-upstream-ca ConfigMap,
// mounted at /etc/gateway/upstream-ca/<caBundle>.
//
// insecureSkipVerify, if present, must equal "acknowledged" (not boolean false).
// This is lint-flagged and enumerated at /config/status.
//
// Per-instance TLS override is available via x-upstream-map entries' tls field.
```

### InstanceTLS (per-instance override)

```go
// InstanceTLS configures TLS verification for a specific instance in x-upstream-map.
type InstanceTLS struct {
	CABundle           string `json:"caBundle,omitempty" validate:"omitempty,^[a-z][a-z0-9_-]+$"`
	ServerName         string `json:"serverName,omitempty" validate:"omitempty,fqdn"`
	InsecureSkipVerify string `json:"insecureSkipVerify,omitempty" validate:"omitempty,eq=acknowledged"`
}
```

### x-upstream-strip-prefix (optional, fragment-root)

```go
// UpstreamStrip is a literal prefix (no parameters, leading /, no trailing /)
// that is removed from all paths in this fragment before forwarding upstream.
//
// Example: if fragment has path "/argocd/applications/{name}" and strip="/argocd",
// SEAM forwards to "/applications/{name}" on the upstream.
//
// Lint fails if strip is not a prefix of EVERY path in the fragment.
// This field is mutually exclusive with x-upstream-path-template (path-item level).
//
// This is an upstream-facing field: an x-adapter fragment cannot declare it.
```

### x-upstream-plaintext (optional, fragment-root)

```go
// UpstreamPlaintext disables TLS to the upstream (uses http:// instead of https://).
// This is an escape hatch for legacy internal services that do not support TLS.
//
// Must be "acknowledged" (not boolean true). Lint-flagged. Enumerated at /config/status.
// Per-instance override available via x-upstream-map entries' plaintext field.
//
// This is an upstream-facing field: an x-adapter fragment cannot declare it.
```

### x-breaker (optional, fragment-root)

```go
// CircuitBreaker configures the per-upstream circuit breaker.
type CircuitBreaker struct {
	Threshold     *uint  `json:"threshold,omitempty" validate:"omitempty,min=1"`         // Consecutive failures to trip
	OpenSeconds   *uint  `json:"openSeconds,omitempty" validate:"omitempty,min=1"`        // Base time in OPEN state
	MaxOpenSeconds *uint  `json:"maxOpenSeconds,omitempty" validate:"omitempty,min=1"`    // Max backoff cap
	Enabled       *bool  `json:"enabled,omitempty"`                                      // false = lint-flagged opt-out
}

// Absence means SEAM default policy (specified in Passive Route Health).
// Unlike x-loop-guard and x-cost-per-call/x-quota, absence is NOT "no guard" —
// the breaker is infrastructure-level and on by default. enabled:false is opt-out.
//
// This field is at fragment root because a fragment names exactly one upstream.
// Per-instance override available via x-upstream-map entries' breaker field.
```

### x-seam-deprecated (optional, fragment-root)

```go
// Deprecation marks every route in this fragment deprecated.
type Deprecation struct {
	Since    string     `json:"since" validate:"required,^\\d{4}-\\d{2}-\\d{2}$"` // RFC 3339 full-date
	Sunset   string     `json:"sunset,omitempty" validate:"omitempty,^\\d{4}-\\d{2}-\\d{2}$"` // RFC 3339 full-date
	Brownout []Brownout `json:"brownout,omitempty" validate:"omitempty,dive"`              // Optional brownout windows
}

type Brownout struct {
	Start string `json:"start" validate:"required,^\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}Z$"` // RFC 3339 instant
	End   string `json:"end" validate:"required,^\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}Z$"`   // RFC 3339 instant
}

// All three date formats are RFC 3339 (ISO 8601):
//   - since/sunset: full-date (YYYY-MM-DD)
//   - brownout start/end: date-time (YYYY-MM-DDTHH:MM:SSZ)
//
// Brownout windows are ordered, non-overlapping, and must fall within [since, sunset].
// Brownout requires a resolved sunset (no brownout without sunset = hard error).
//
// This field drives the Deprecation and Sunset HTTP headers (RFC 9745, RFC 8594).
// Brownout is machine-written by seam-retirement-evaluator, not hand-authored.
```

### x-adapter (optional, fragment-root)

```go
// Adapter declares transforms to adapt this older fragment's contract into targetVersion.
type Adapter struct {
	TargetVersion string       `json:"targetVersion" validate:"required,^v[1-9][0-9]*$"` // Live version to adapt into
	Request       []Transform  `json:"request,omitempty" validate:"omitempty,dive"`              // Request transforms
	Response      []Transform  `json:"response,omitempty" validate:"omitempty,dive"`             // Response transforms
}

// Transform is a single declarative transformation step.
type Transform struct {
	Type string `json:"type" validate:"required,oneof=renameField defaultField dropField unwrapEnvelope wrapEnvelope renameParam renameHeader"`

	// Fields populated based on Type:
	From      string      `json:"from,omitempty" validate:"omitempty"`
	To        string      `json:"to,omitempty" validate:"omitempty"`
	JSONPointer string     `json:"jsonPointer,omitempty" validate:"omitempty"`
	Value     interface{} `json:"value,omitempty"`
	Envelope  string      `json:"envelope,omitempty" validate:"omitempty"`
	ParamName string      `json:"paramName,omitempty" validate:"omitempty"`
	HeaderName string     `json:"headerName,omitempty" validate:"omitempty"`
}

// x-adapter is mutually exclusive with x-upstream/x-upstream-map.
// An adapting fragment delegates to targetVersion's route (no upstream of its own).
// Upstream-facing fields (x-vault-path, x-inject-as, etc.) are taken from target.
// Caller-facing fields (schemas, x-seam-deprecated, echoed X-SEAM-API-Version) stay local.
// x-required-scope is enforced as the union of both fragments' requirements.
// Guards (x-quota, x-cost-per-call, x-loop-guard) are charged to target's route.
```

### x-fanout-scope (optional, fragment-root)

```go
// FanoutScope specifies additional scope requirement for _all multi-instance fan-out.
// This is in addition to operation-level scope.
// Format: same as x-required-scope (string or []string of scope strings).
```

---

## Path-Item Level Extensions

### x-upstream-path-template (optional, path-item level)

```go
// UpstreamPathTemplate is a path string in the upstream's terms.
// May reference any path parameter except the designated instance parameter.
//
// Example: "/api/v1/applications/{name}/sync" on SEAM path "/argocd/{name}/sync"
//
// This wins over x-upstream-strip-prefix where both are present.
// Lint checks: begins with /, every {param} exists in matched template,
// and does not name the designated instance parameter (x-instance-param).
//
// This is an upstream-facing field: an x-adapter fragment cannot declare it.
```

---

## Operation-Level Extensions

```go
// Operation represents a single OpenAPI operation (GET /items, POST /items, etc).
type Operation struct {
	Summary     string                `json:"summary,omitempty" validate:"omitempty"`
	Description string               `json:"description,omitempty" validate:"omitempty"`
	OperationID string               `json:"operationId,omitempty" validate:"omitempty,^[a-zA-Z0-9._-]+$"`
	Parameters  []Parameter           `json:"parameters,omitempty" validate:"omitempty,dive"`
	RequestBody *RequestBody          `json:"requestBody,omitempty" validate:"omitempty"`
	Responses   map[string]Response   `json:"responses" validate:"required,min=1"` // Status code -> response
	Deprecated  bool                  `json:"deprecated,omitempty"`
	Security    []SecurityRequirement  `json:"security,omitempty" validate:"omitempty,dive"`

	// SEAM operation-level extensions
	RequiredScope interface{}   `json:"x-required-scope,omitempty"` // Overrides fragment-root
	LoopGuard    *LoopGuard     `json:"x-loop-guard,omitempty" validate:"omitempty"`
	CostPerCall  *CostPerCall   `json:"x-cost-per-call,omitempty" validate:"omitempty"`
	Quota        *Quota         `json:"x-quota,omitempty" validate:"omitempty"`
	Unscrubbable string         `json:"x-unscrubbable,omitempty" validate:"omitempty,eq=acknowledged"`
	RequiresApproval bool        `json:"x-requires-approval,omitempty"`
}
```

### x-loop-guard (optional, operation-level)

```go
// LoopGuard protects against repeated failing requests (agent loop bugs).
type LoopGuard struct {
	MaxRepeats uint   `json:"maxRepeats" validate:"required,min=1"`          // Tolerated repeats in window
	Window     string `json:"window" validate:"required,^[1-9][0-9]*[smhd]$"`   // Tumbling window duration
}

// Window grammar: ^[1-9][0-9]*[smhd]$ (plain duration, no calendar-aligned form).
// Window is tumbling, anchored at process start. Next window opens immediately after.
// A SEAM restart resets the guard unconditionally (per-process in-memory state).
//
// A 2xx response on the same hash clears that hash's counter (success = reset).
// When the guard fires, structured 429 carries Retry-After = seconds remaining in window.
//
// Absence = no loop guard on that route (opt-in).
```

### x-cost-per-call (optional, operation-level)

```go
// CostPerCall annotates the cost of a single call for budget tracking.
type CostPerCall struct {
	Amount float64 `json:"amount" validate:"required,gt=0"`       // Cost per call
	Unit   string  `json:"unit" validate:"required,oneof=USD credits quota calls"` // Cost unit
}

// Units:
//   - USD: dollar cost (e.g., twitterapi.io)
//   - credits: API credits (e.g., $1 = 1000 credits)
//   - quota: quota units (e.g., z.ai)
//   - calls: raw call count (no monetary cost)
//
// This is paired with x-quota (per-caller budget). Absence = no cost guard.
```

### x-quota (optional, operation-level)

```go
// Quota configures a per-caller budget for a route.
type Quota struct {
	Amount float64 `json:"amount" validate:"required,gt=0"`       // Budget amount
	Unit   string  `json:"unit" validate:"required,oneof=USD credits quota calls"` // Must match x-cost-per-call.unit
	Window string  `json:"window" validate:"required,^[1-9][0-9]*[smhd]$"` // Reset window duration
}

// Window grammar: ^[1-9][0-9]*[smhd]$ (tumbling window, not calendar-aligned).
//
// Example: {amount: 10.00, unit: "USD", window: "1h"} = $10/hour per caller.
//
// This guard is charged per caller identity (extracted from Tailscale WhoIs).
// Absence = no quota guard on that route (opt-in).
```

---

## Supporting Types (OpenAPI 3.1)

```go
// Parameter represents an OpenAPI 3.1 Parameter object.
type Parameter struct {
	Name            string      `json:"name" validate:"required"`
	In              string      `json:"in" validate:"required,oneof=query header path cookie"`
	Description     string      `json:"description,omitempty" validate:"omitempty"`
	Required        bool        `json:"required,omitempty"`
	Deprecated      bool        `json:"deprecated,omitempty"`
	AllowEmptyValue bool        `json:"allowEmptyValue,omitempty"`
	Style           string      `json:"style,omitempty" validate:"omitempty"`
	Explode         *bool       `json:"explode,omitempty" validate:"omitempty"`
	AllowReserved   bool        `json:"allowReserved,omitempty"`
	Schema          interface{} `json:"schema,omitempty" validate:"omitempty"`
	Content         map[string]MediaType `json:"content,omitempty" validate:"omitempty"`
	Example         interface{} `json:"example,omitempty" validate:"omitempty"`
	Examples        map[string]Example `json:"examples,omitempty" validate:"omitempty,dive"`
}

// RequestBody represents an OpenAPI 3.1 Request Body object.
type RequestBody struct {
	Description string                 `json:"description,omitempty" validate:"omitempty"`
	Required    bool                   `json:"required,omitempty"`
	Content     map[string]MediaType   `json:"content" validate:"required,min=1,dive"`
}

// MediaType represents an OpenAPI 3.1 Media Type object.
type MediaType struct {
	Schema   interface{}          `json:"schema,omitempty" validate:"omitempty"`
	Encoding map[string]Encoding `json:"encoding,omitempty" validate:"omitempty,dive"`
	Example  interface{}          `json:"example,omitempty" validate:"omitempty"`
	Examples map[string]Example  `json:"examples,omitempty" validate:"omitempty,dive"`
}

// Response represents an OpenAPI 3.1 Response object.
type Response struct {
	Description string                 `json:"description" validate:"required"`
	Headers     map[string]Header      `json:"headers,omitempty" validate:"omitempty,dive"`
	Content     map[string]MediaType   `json:"content,omitempty" validate:"omitempty,dive"`
	Links       map[string]Link        `json:"links,omitempty" validate:"omitempty,dive"`
}

// Example represents an OpenAPI 3.1 Example object.
type Example struct {
	Summary       string      `json:"summary,omitempty" validate:"omitempty"`
	Description   string      `json:"description,omitempty" validate:"omitempty"`
	Value         interface{} `json:"value,omitempty" validate:"omitempty"`
	ExternalValue string     `json:"externalValue,omitempty" validate:"omitempty,url"`
}

// Header represents an OpenAPI 3.1 Header object.
type Header struct {
	Description     string      `json:"description,omitempty" validate:"omitempty"`
	Required        bool        `json:"required,omitempty"`
	Deprecated      bool        `json:"deprecated,omitempty"`
	AllowEmptyValue bool        `json:"allowEmptyValue,omitempty"`
	Style           string      `json:"style,omitempty" validate:"omitempty"`
	Explode         *bool       `json:"explode,omitempty" validate:"omitempty"`
	AllowReserved   bool        `json:"allowReserved,omitempty"`
	Schema          interface{} `json:"schema,omitempty" validate:"omitempty"`
	Content         map[string]MediaType `json:"content,omitempty" validate:"omitempty"`
	Example         interface{} `json:"example,omitempty" validate:"omitempty"`
	Examples        map[string]Example `json:"examples,omitempty" validate:"omitempty,dive"`
}

// Encoding represents an OpenAPI 3.1 Encoding object.
type Encoding struct {
	ContentType string             `json:"contentType,omitempty" validate:"omitempty"`
	Headers     map[string]Header  `json:"headers,omitempty" validate:"omitempty,dive"`
	Style       string             `json:"style,omitempty" validate:"omitempty"`
	Explode     *bool              `json:"explode,omitempty" validate:"omitempty"`
	AllowReserved bool             `json:"allowReserved,omitempty"`
}

// Link represents an OpenAPI 3.1 Link object.
type Link struct {
	OperationRef string                 `json:"operationRef,omitempty" validate:"omitempty,url"`
	OperationID  string                 `json:"operationId,omitempty" validate:"omitempty"`
	Parameters   map[string]interface{} `json:"parameters,omitempty" validate:"omitempty"`
	RequestBody  interface{}            `json:"requestBody,omitempty" validate:"omitempty"`
	Description  string                 `json:"description,omitempty" validate:"omitempty"`
	Server       *Server                `json:"server,omitempty" validate:"omitempty"`
}

// Callback represents an OpenAPI 3.1 Callback object.
type Callback struct {
	// Map of expression -> PathItem
}

// SecurityScheme represents an OpenAPI 3.1 Security Scheme object.
type SecurityScheme struct {
	Type             string                       `json:"type" validate:"required,oneof=apiKey http oauth2 openIdConnect mutualTLS"`
	Description       string                      `json:"description,omitempty" validate:"omitempty"`
	Name              string                      `json:"name,omitempty" validate:"omitempty"`
	In                string                      `json:"in,omitempty" validate:"omitempty,oneof=query header cookie"`
	Scheme            string                      `json:"scheme,omitempty" validate:"omitempty,oneof=basic bearer digest hoba mutual negotiate oauth vapid"`
	BearerFormat      string                      `json:"bearerFormat,omitempty" validate:"omitempty"`
	Flows             *OAuthFlows                `json:"flows,omitempty" validate:"omitempty"`
	OpenIdConnectUrl  string                      `json:"openIdConnectUrl,omitempty" validate:"omitempty,url"`
}

// OAuthFlows represents OAuth2 flows configuration.
type OAuthFlows struct {
	Implicit          *OAuthFlow  `json:"implicit,omitempty" validate:"omitempty"`
	Password          *OAuthFlow  `json:"password,omitempty" validate:"omitempty"`
	ClientCredentials *OAuthFlow  `json:"clientCredentials,omitempty" validate:"omitempty"`
	AuthorizationCode *OAuthFlow  `json:"authorizationCode,omitempty" validate:"omitempty"`
}

// OAuthFlow represents a single OAuth2 flow.
type OAuthFlow struct {
	AuthorizationUrl string            `json:"authorizationUrl,omitempty" validate:"omitempty,url"`
	TokenUrl         string            `json:"tokenUrl,omitempty" validate:"omitempty,url"`
	RefreshUrl       string            `json:"refreshUrl,omitempty" validate:"omitempty,url"`
	Scopes           map[string]string `json:"scopes,omitempty" validate:"omitempty"`
}

// SecurityRequirement represents a security requirement for an operation.
type SecurityRequirement map[string][]string

// Server represents an OpenAPI 3.1 Server object.
type Server struct {
	URL         string                  `json:"url" validate:"required"`
	Description string                  `json:"description,omitempty" validate:"omitempty"`
	Variables   map[string]ServerVariable `json:"variables,omitempty" validate:"omitempty,dive"`
}

// ServerVariable represents a server variable.
type ServerVariable struct {
	Default     string   `json:"default" validate:"required"`
	Enum        []string `json:"enum,omitempty" validate:"omitempty,dive"`
	Description string   `json:"description,omitempty" validate:"omitempty"`
}
```

---

## Cross-Field Validation Rules (Not Expressible in Tags)

These rules require custom validation logic in Go (beyond struct tags):

### Fragment-Root Validation

1. **x-upstream XOR x-upstream-map:** Exactly one must be present (mutually exclusive)
2. **x-instance-param requirement:** Required with x-upstream-map, forbidden without it
3. **x-vault-path AND x-inject-as pairing:** Both-or-neither constraint
4. **x-seam-owner vs directory match:** Must equal mounted parent directory
5. **x-upstream-strip-prefix prefix check:** Must be prefix of ALL paths in fragment
6. **x-upstream-plaintext with http://:** Required when x-upstream is http://

### Instance Map Validation

1. **Each entry's URL host:** Must be in upstream-host allowlist
2. **probeInterval format:** Must match ^[1-9][0-9]*[smhd]$

### Path-Item Validation

1. **x-upstream-path-template parameters:** Every {param} must exist in matched path
2. **x-upstream-path-template excludes instance param:** Cannot name x-instance-param value

### Operation-Level Validation

1. **x-quota requires x-cost-per-call:** Both must be present on same operation
2. **x-quota.unit must equal x-cost-per-call.unit:** Units must match

### Adapter Validation

1. **x-adapter excludes upstream fields:** Cannot have x-upstream, x-upstream-map, x-vault-path, x-inject-as, x-upstream-tls, x-upstream-strip-prefix, x-upstream-plaintext, x-credential-probe, x-breaker

### Brownout Validation

1. **Brownout requires sunset:** Cannot have brownout without sunset date
2. **Brownout windows:** Must be ordered, non-overlapping, within [since, sunset]

---

## Usage Example

```go
package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/go-playground/validator/v10"
	"seam/internal/seamapi"
)

func validateFragment(data []byte) error {
	var fragment seamapi.RouteFragment
	if err := json.Unmarshal(data, &fragment); err != nil {
		return fmt.Errorf("unmarshal failed: %w", err)
	}

	validate := validator.New()
	if err := validate.Struct(fragment); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Custom cross-field validation (implement as needed)
	// - x-upstream XOR x-upstream-map
	// - x-instance-param requirement
	// - etc.

	return nil
}

func main() {
	data := []byte(`{
		"openapi": "3.1.0",
		"x-seam-owner": "argocd",
		"x-seam-schema": "v1",
		"x-upstream": "https://argocd-ro.example.com:8444",
		"paths": {
			"/argocd/api/v1/applications": {
				"get": {
					"summary": "List applications",
					"responses": {
						"200": {"description": "Success"}
					}
				}
			}
		}
	}`)

	if err := validateFragment(data); err != nil {
		log.Fatalf("Fragment validation failed: %v", err)
	}

	fmt.Println("Fragment is valid!")
}
```

---

## Version Migration

When x-seam-schema v2 is introduced:

1. Add v2-specific fields to a new `RouteFragmentV2` struct
2. Update validator to accept `"v1" | "v2"` in x-seam-schema
3. Gateway at merge time dispatches to v1 or v2 parser based on the marker
4. `seam lint` validates against the appropriate schema version

This design ensures detectable schema evolution without breaking existing fragments.

---

**Status:** ✅ Complete — ready for gateway implementation (Phase 1) and `seam lint` (Phase 9).
