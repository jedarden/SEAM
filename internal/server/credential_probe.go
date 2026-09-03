package server

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pb33f/libopenapi/datamodel/high/v3"
)

// CredentialProbeConfig is the x-credential-probe configuration at the fragment root.
// It defines how credentials are probed for liveness and expiry detection.
type CredentialProbeConfig struct {
	// Path is the HTTP path to probe (e.g., "/api/v1/namespaces/default")
	Path string `yaml:"path" json:"path"`

	// Method is the HTTP method to use (default: GET)
	Method string `yaml:"method" json:"method"`

	// Interval is the per-fragment probe cadence (e.g., "1h", "45m")
	// Per-instance probeInterval overrides in x-upstream-map take precedence.
	Interval string `yaml:"interval" json:"interval"`
}

// ParseInterval parses the interval string into a duration.
// Supports: "30s", "5m", "1h", "2h", etc.
func (c *CredentialProbeConfig) ParseInterval() (time.Duration, error) {
	if c.Interval == "" {
		return 1 * time.Hour, nil // Default 1 hour
	}
	return parseDurationString(c.Interval)
}

// parseDurationString parses a duration string like "1h", "45m", "30s".
func parseDurationString(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	// Parse number and unit
	var num int
	var unit string
	for i, r := range s {
		if r >= '0' && r <= '9' {
			num = num*10 + int(r-'0')
		} else {
			unit = s[i:]
			break
		}
	}

	switch unit {
	case "s":
		return time.Duration(num) * time.Second, nil
	case "m":
		return time.Duration(num) * time.Minute, nil
	case "h":
		return time.Duration(num) * time.Hour, nil
	case "d":
		return time.Duration(num) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown duration unit %q", unit)
	}
}

// extractCredentialProbeConfig extracts x-credential-probe from operation extensions.
func extractCredentialProbeConfig(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) (*CredentialProbeConfig, error) {
	node, ok := firstExtension(operation, pathItem, document, "x-credential-probe")
	if !ok || node == nil {
		return nil, nil
	}

	var config CredentialProbeConfig
	if err := node.Decode(&config); err != nil {
		return nil, fmt.Errorf("x-credential-probe must be an object: %w", err)
	}

	// Validate required fields
	if strings.TrimSpace(config.Path) == "" {
		return nil, fmt.Errorf("x-credential-probe.path is required")
	}

	// Default method to GET
	if config.Method == "" {
		config.Method = "GET"
	} else {
		config.Method = strings.ToUpper(config.Method)
	}

	// Validate method
	validMethods := map[string]bool{"GET": true, "HEAD": true, "OPTIONS": true}
	if !validMethods[config.Method] {
		return nil, fmt.Errorf("x-credential-probe.method %q is not supported (use GET, HEAD, or OPTIONS)", config.Method)
	}

	// Validate interval if specified
	if config.Interval != "" {
		if _, err := config.ParseInterval(); err != nil {
			return nil, fmt.Errorf("x-credential-probe.interval: %w", err)
		}
	}

	return &config, nil
}

// extractUpstreamMapWithProbeIntervals extracts x-upstream-map with per-instance probeInterval support.
// This extends extractUpstreamMap to also capture probeInterval per instance.
func extractUpstreamMapWithProbeIntervals(operation *v3.Operation, pathItem *v3.PathItem, document *v3.Document) (map[string]RouteTarget, error) {
	node, ok := firstExtension(operation, pathItem, document, "x-upstream-map")
	if !ok || node == nil {
		return nil, nil
	}

	var raw map[string]struct {
		URL           string                 `yaml:"url" json:"url"`
		VaultPath     string                 `yaml:"vaultPath" json:"vaultPath"`
		InjectAs      *InjectAs              `yaml:"injectAs" json:"injectAs"`
		Breaker       map[string]interface{} `yaml:"breaker" json:"breaker"`
		RequiredScope []string               `yaml:"requiredScope" json:"requiredScope"`
		ProbeInterval string                 `yaml:"probeInterval" json:"probeInterval"` // Phase 12
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

		// Extract per-instance breaker config if present
		var breakerConfig *BreakerConfig
		if len(value.Breaker) > 0 {
			config, err := parseBreakerConfig(value.Breaker)
			if err != nil {
				return nil, fmt.Errorf("x-upstream-map entry %q: %w", key, err)
			}
			breakerConfig = &config
		}

		// Validate probeInterval if present
		var probeInterval time.Duration
		if value.ProbeInterval != "" {
			var err error
			probeInterval, err = parseDurationString(value.ProbeInterval)
			if err != nil {
				return nil, fmt.Errorf("x-upstream-map entry %q: probeInterval: %w", key, err)
			}
		}

		result[key] = RouteTarget{
			URL:            strings.TrimSpace(value.URL),
			VaultPath:      value.VaultPath,
			InjectAs:       value.InjectAs,
			BreakerConfig:  breakerConfig,
			RequiredScopes: value.RequiredScope,
			ProbeInterval:  probeInterval,
		}
	}
	return result, nil
}

// CredentialProbeTarget represents a single (fragment, instance) probe target.
type CredentialProbeTarget struct {
	// FragmentID is the fragment identifier (owner/schema)
	FragmentID string

	// InstanceID is the instance identifier (_default for single-instance)
	InstanceID string

	// VaultPath is the OpenBao path to the credential
	VaultPath string

	// ProbeURL is the full URL to probe (including scheme and host)
	ProbeURL string

	// ProbePath is the path component of the probe URL
	ProbePath string

	// ProbeMethod is the HTTP method to use (GET, HEAD, OPTIONS)
	ProbeMethod string

	// Interval is the probe cadence for this target
	Interval time.Duration

	// InjectAs describes how to inject the credential into the probe request
	InjectAs *InjectAs
}

// CredentialProbeResult is the result of a credential probe.
type CredentialProbeResult struct {
	// FragmentID is the fragment identifier
	FragmentID string

	// InstanceID is the instance identifier
	InstanceID string

	// LastVerified is when the last successful probe completed
	LastVerified *time.Time

	// Status is the current credential health status
	Status CredentialHealthStatus `json:"status"`

	// KnownExpiry is when the credential is known to expire (if available)
	KnownExpiry *time.Time `json:"known_expiry,omitempty"`

	// ProbeSpendRate is the time spent on the last probe (in milliseconds)
	ProbeSpendRate int64 `json:"probe_spend_rate_ms"`

	// LastError is the last probe error (if any)
	LastError string `json:"last_error,omitempty"`

	// ConsecutiveFailures is the count of consecutive probe failures
	ConsecutiveFailures int `json:"consecutive_failures"`
}

// CredentialHealthStatus is the health status of a credential.
type CredentialHealthStatus string

const (
	// CredentialHealthy means the last probe succeeded
	CredentialHealthy CredentialHealthStatus = "healthy"
	// CredentialDegraded means probes are intermittently failing
	CredentialDegraded CredentialHealthStatus = "degraded"
	// CredentialUnhealthy means probes are consistently failing
	CredentialUnhealthy CredentialHealthStatus = "unhealthy"
	// CredentialUnknown means no probes have been issued yet
	CredentialUnknown CredentialHealthStatus = "unknown"
)

// CredentialProbeRegistry tracks probe results for all (fragment, instance) pairs.
type CredentialProbeRegistry struct {
	mu      sync.RWMutex
	results map[string]*CredentialProbeResult // key: "fragmentID:instanceID"
}

// NewCredentialProbeRegistry creates a new credential probe registry.
func NewCredentialProbeRegistry() *CredentialProbeRegistry {
	return &CredentialProbeRegistry{
		results: make(map[string]*CredentialProbeResult),
	}
}

// Set records a probe result for a (fragment, instance) pair.
func (r *CredentialProbeRegistry) Set(result CredentialProbeResult) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	key := result.FragmentID + ":" + result.InstanceID

	// Preserve consecutive failures if this is a success
	if existing, ok := r.results[key]; ok {
		if result.Status == CredentialHealthy {
			result.ConsecutiveFailures = 0
		} else if result.Status == existing.Status {
			result.ConsecutiveFailures = existing.ConsecutiveFailures + 1
		}
	}

	r.results[key] = &result
}

// Get retrieves the probe result for a (fragment, instance) pair.
func (r *CredentialProbeRegistry) Get(fragmentID, instanceID string) *CredentialProbeResult {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := fragmentID + ":" + instanceID
	result := r.results[key]
	if result == nil {
		return nil
	}

	// Return a copy to avoid mutation
	copy := *result
	if result.LastVerified != nil {
		tv := *result.LastVerified
		copy.LastVerified = &tv
	}
	if result.KnownExpiry != nil {
		te := *result.KnownExpiry
		copy.KnownExpiry = &te
	}
	return &copy
}

// Snapshot returns all probe results sorted by fragment ID, then instance ID.
func (r *CredentialProbeRegistry) Snapshot() []CredentialProbeResult {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	results := make([]CredentialProbeResult, 0, len(r.results))
	for _, result := range r.results {
		copy := *result
		if result.LastVerified != nil {
			tv := *result.LastVerified
			copy.LastVerified = &tv
		}
		if result.KnownExpiry != nil {
			te := *result.KnownExpiry
			copy.KnownExpiry = &te
		}
		results = append(results, copy)
	}

	// Sort by fragment ID, then instance ID
	sort.Slice(results, func(i, j int) bool {
		if results[i].FragmentID != results[j].FragmentID {
			return results[i].FragmentID < results[j].FragmentID
		}
		return results[i].InstanceID < results[j].InstanceID
	})

	return results
}

// Remove clears probe results for a specific fragment.
func (r *CredentialProbeRegistry) Remove(fragmentID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	prefix := fragmentID + ":"
	for key := range r.results {
		if strings.HasPrefix(key, prefix) {
			delete(r.results, key)
		}
	}
}
