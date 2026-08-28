package server

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Origin uniquely identifies an upstream target for circuit breaking.
// It is the resolved scheme+host+port from the upstream URL.
type Origin string

// ParseOrigin extracts the origin (scheme+host+port) from a URL.
// For https://example.com:8443/path, the origin is https://example.com:8443.
// For https://example.com/path, the origin is https://example.com:443.
func ParseOrigin(upstreamURL string) (Origin, error) {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return "", fmt.Errorf("parse upstream URL: %w", err)
	}

	if u.Scheme == "" {
		return "", fmt.Errorf("upstream URL missing scheme: %s", upstreamURL)
	}

	if u.Host == "" {
		return "", fmt.Errorf("upstream URL missing host: %s", upstreamURL)
	}

	// Build origin: scheme://host:port
	// url.Parse includes the port in u.Host if present, but we need to add default ports
	host := u.Host
	if u.Port() == "" {
		// Add default port based on scheme
		switch u.Scheme {
		case "https":
			host = u.Host + ":443"
		case "http":
			host = u.Host + ":80"
		}
	}

	origin := fmt.Sprintf("%s://%s", u.Scheme, host)
	return Origin(origin), nil
}

// MustParseOrigin parses an origin or panics. Use only in tests.
func MustParseOrigin(upstreamURL string) Origin {
	origin, err := ParseOrigin(upstreamURL)
	if err != nil {
		panic(err)
	}
	return origin
}

// BreakerConfig holds the circuit breaker tuning parameters.
// These can be set at fragment-root or overridden per-instance in x-upstream-map.
type BreakerConfig struct {
	// Threshold is the number of consecutive failures before opening.
	// Default: 5 (Phase 11.1 requirement).
	Threshold int

	// OpenSeconds is the base open duration after threshold failures.
	// Default: 30 seconds.
	OpenSeconds int

	// MaxOpenSeconds is the maximum backoff cap.
	// Default: 300 seconds (5 minutes).
	MaxOpenSeconds int

	// Enabled controls whether the breaker is active.
	// Default: true (ON by default as Phase 11.1 requires).
	// false is the opt-out that is lint-flagged and enumerated at /config/status.
	Enabled bool
}

// DefaultBreakerConfig returns the Phase 11.1 default configuration.
// The breaker is ON by default; enabled: false is the opt-out.
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		Threshold:      5,
		OpenSeconds:    30,
		MaxOpenSeconds: 300,
		Enabled:        true,
	}
}

// Merge returns the stricter configuration when two configs for the same
// origin disagree. The "stricter" is the one more likely to open the breaker:
// lower threshold, shorter open duration, lower max cap.
// This is used at runtime when same-origin instances disagree.
func (c BreakerConfig) Merge(other BreakerConfig) BreakerConfig {
	merged := c

	// Take the stricter (lower) threshold
	if other.Threshold > 0 && other.Threshold < merged.Threshold {
		merged.Threshold = other.Threshold
	}

	// Take the stricter (shorter) open duration
	if other.OpenSeconds > 0 && other.OpenSeconds < merged.OpenSeconds {
		merged.OpenSeconds = other.OpenSeconds
	}

	// Take the stricter (lower) max open cap
	if other.MaxOpenSeconds > 0 && other.MaxOpenSeconds < merged.MaxOpenSeconds {
		merged.MaxOpenSeconds = other.MaxOpenSeconds
	}

	// If either is disabled, the merged config is disabled
	// (this is the conservative approach - disagreement disables)
	if !other.Enabled {
		merged.Enabled = false
	}

	return merged
}

// Disagreement reports whether other differs from this config in a
// meaningful way. This is used for lint checks and /config/status enumeration.
func (c BreakerConfig) Disagreement(other BreakerConfig) bool {
	return c.Threshold != other.Threshold ||
		c.OpenSeconds != other.OpenSeconds ||
		c.MaxOpenSeconds != other.MaxOpenSeconds ||
		c.Enabled != other.Enabled
}

// backoffDuration calculates the next backoff duration based on the number
// of consecutive open transitions. The sequence is: 30s, 60s, 120s, capped at
// MaxOpenSeconds (default 5 minutes).
func (c BreakerConfig) backoffDuration(openCount int) time.Duration {
	if openCount < 1 {
		openCount = 1
	}

	// Calculate exponential backoff: 30s * 2^(openCount-1)
	// openCount=1 -> 30s
	// openCount=2 -> 60s
	// openCount=3 -> 120s
	// openCount=4 -> 240s (if max allows)
	// openCount=5 -> 480s (capped to 300s by default)
	duration := time.Duration(c.OpenSeconds) * time.Second
	for i := 1; i < openCount; i++ {
		duration *= 2
	}

	// Cap at MaxOpenSeconds
	maxDuration := time.Duration(c.MaxOpenSeconds) * time.Second
	if duration > maxDuration {
		duration = maxDuration
	}

	return duration
}

// BreakerState is the state of a circuit breaker.
type BreakerState int

const (
	BreakerClosed BreakerState = iota
	BreakerOpen
	BreakerHalfOpen
)

func (s BreakerState) String() string {
	switch s {
	case BreakerClosed:
		return "closed"
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// ToCircuitBreakerState converts to the public CircuitBreakerState type.
func (s BreakerState) ToCircuitBreakerState() CircuitBreakerState {
	switch s {
	case BreakerClosed:
		return CircuitBreakerClosed
	case BreakerOpen:
		return CircuitBreakerOpen
	case BreakerHalfOpen:
		return CircuitBreakerHalfOpen
	default:
		return CircuitBreakerClosed
	}
}

// CircuitBreaker is a per-origin circuit breaker implementing Phase 11.1.
// It tracks consecutive failures, manages state transitions, and controls
// request admission based on the current state.
type CircuitBreaker struct {
	// Origin is the resolved upstream origin this breaker protects.
	origin Origin

	// Config is the active breaker configuration.
	config BreakerConfig

	// State management
	mu                  sync.Mutex
	state               BreakerState
	consecutiveFailures int
	openCount           int       // Number of times we've transitioned to open
	openedAt            time.Time // When we last entered the open state
	halfOpenAdmitted    bool      // Whether half-open has admitted its one allowed request
	lastError           string    // Last failure message

	// Registry for publishing state updates
	registry *CircuitBreakerStateRegistry
}

// NewCircuitBreaker creates a new circuit breaker for the given origin.
func NewCircuitBreaker(origin Origin, config BreakerConfig, registry *CircuitBreakerStateRegistry) *CircuitBreaker {
	if config.Threshold <= 0 {
		config.Threshold = DefaultBreakerConfig().Threshold
	}
	if config.OpenSeconds <= 0 {
		config.OpenSeconds = DefaultBreakerConfig().OpenSeconds
	}
	if config.MaxOpenSeconds <= 0 {
		config.MaxOpenSeconds = DefaultBreakerConfig().MaxOpenSeconds
	}

	return &CircuitBreaker{
		origin:   origin,
		config:   config,
		state:    BreakerClosed,
		registry: registry,
	}
}

// Allow checks whether a request should be allowed through.
// It returns false if the breaker is open or if the caller is not the
// single allowed request in half-open state.
// On success (allow=true), the caller must call RecordSuccess or RecordFailure.
func (b *CircuitBreaker) Allow() (allow bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Disabled breakers always allow
	if !b.config.Enabled {
		return true
	}

	now := time.Now()

	switch b.state {
	case BreakerClosed:
		// Closed state allows all requests
		return true

	case BreakerOpen:
		// Check if we should transition to half-open
		if b.openedAt.IsZero() {
			// Shouldn't happen, but be safe
			b.transitionToClosedLocked()
			return true
		}

		openDuration := b.config.backoffDuration(b.openCount)
		if now.Sub(b.openedAt) >= openDuration {
			// Time to try half-open
			b.transitionToHalfOpenLocked()
			// This first call is the ONE allowed request in half-open
			b.halfOpenAdmitted = true
			b.publishStateLocked()
			return true
		}

		// Still open, deny request
		return false

	case BreakerHalfOpen:
		// Half-open admits ONE real caller (no synthetic probe)
		if !b.halfOpenAdmitted {
			b.halfOpenAdmitted = true
			b.publishStateLocked()
			return true
		}
		// Already admitted the one request, deny until we hear back
		return false

	default:
		// Unknown state, treat as closed (fail-open for safety)
		return true
	}
}

// RecordSuccess records a successful response.
// In half-open state, this transitions the breaker to closed.
// In closed state, it resets the consecutive failure counter.
func (b *CircuitBreaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.config.Enabled {
		return
	}

	switch b.state {
	case BreakerHalfOpen:
		// Success in half-open closes the breaker
		b.transitionToClosedLocked()

	case BreakerClosed:
		// Reset failure counter on success in closed state
		if b.consecutiveFailures > 0 {
			b.consecutiveFailures = 0
			b.publishStateLocked()
		}
	}
}

// RecordFailure records a failed response.
// In half-open state, this re-opens the breaker with backoff.
// In closed state, this increments the consecutive failure counter.
func (b *CircuitBreaker) RecordFailure(errMsg string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.config.Enabled {
		return
	}

	b.lastError = errMsg
	b.consecutiveFailures++

	switch b.state {
	case BreakerHalfOpen:
		// Failure in half-open re-opens with backoff
		b.transitionToOpenLocked()

	case BreakerClosed:
		// Check if we've hit the threshold
		if b.consecutiveFailures >= b.config.Threshold {
			b.transitionToOpenLocked()
		} else {
			// Still below threshold, just publish the failure count
			b.publishStateLocked()
		}
	}
}

// transitionToOpenLocked transitions the breaker to open state.
// Caller must hold b.mu.
func (b *CircuitBreaker) transitionToOpenLocked() {
	b.openCount++
	b.state = BreakerOpen
	b.openedAt = time.Now()
	b.publishStateLocked()
}

// transitionToHalfOpenLocked transitions the breaker to half-open state.
// Caller must hold b.mu.
func (b *CircuitBreaker) transitionToHalfOpenLocked() {
	b.state = BreakerHalfOpen
	b.halfOpenAdmitted = false // Will be set when first request is admitted
	b.publishStateLocked()
}

// transitionToClosedLocked transitions the breaker to closed state.
// Caller must hold b.mu.
func (b *CircuitBreaker) transitionToClosedLocked() {
	b.state = BreakerClosed
	b.consecutiveFailures = 0
	b.openCount = 0 // Reset backoff sequence on successful close
	b.openedAt = time.Time{}
	b.halfOpenAdmitted = false
	b.publishStateLocked()
}

// publishStateLocked publishes the current breaker state to the registry.
// Caller must hold b.mu.
func (b *CircuitBreaker) publishStateLocked() {
	if b.registry == nil {
		return
	}

	var openedAt *time.Time
	if !b.openedAt.IsZero() {
		openedAt = &b.openedAt
	}

	retryAfter := 0
	if b.state == BreakerOpen && !b.openedAt.IsZero() {
		openDuration := b.config.backoffDuration(b.openCount)
		elapsed := time.Since(b.openedAt)
		remaining := openDuration - elapsed
		if remaining.Seconds() > 0 {
			retryAfter = int(remaining.Seconds())
			if retryAfter < 1 {
				retryAfter = 1 // Floor at 1 second per Phase 11.1 spec
			}
		}
	}

	b.registry.Set(CircuitBreakerStatus{
		Origin:              string(b.origin),
		State:               b.state.ToCircuitBreakerState(),
		Enabled:             b.config.Enabled,
		ConsecutiveFailures: b.consecutiveFailures,
		OpenedAt:            openedAt,
		LastError:           b.lastError,
		RetryAfterSeconds:   retryAfter,
		Source:              "passive_route_health",
	})
}

// Snapshot returns the current breaker state for testing/inspection.
func (b *CircuitBreaker) Snapshot() CircuitBreakerStatus {
	b.mu.Lock()
	defer b.mu.Unlock()

	var openedAt *time.Time
	if !b.openedAt.IsZero() {
		openedAt = &b.openedAt
	}

	retryAfter := 0
	if b.state == BreakerOpen && !b.openedAt.IsZero() {
		openDuration := b.config.backoffDuration(b.openCount)
		elapsed := time.Since(b.openedAt)
		remaining := openDuration - elapsed
		if remaining.Seconds() > 0 {
			retryAfter = int(remaining.Seconds())
			if retryAfter < 1 {
				retryAfter = 1
			}
		}
	}

	return CircuitBreakerStatus{
		Origin:              string(b.origin),
		State:               b.state.ToCircuitBreakerState(),
		Enabled:             b.config.Enabled,
		ConsecutiveFailures: b.consecutiveFailures,
		OpenedAt:            openedAt,
		LastError:           b.lastError,
		RetryAfterSeconds:   retryAfter,
		Source:              "passive_route_health",
	}
}

// Origin returns the origin this breaker protects.
func (b *CircuitBreaker) Origin() Origin {
	return b.origin
}

// Config returns the breaker's configuration.
func (b *CircuitBreaker) Config() BreakerConfig {
	return b.config
}

// IsFailureStatusCode determines if an HTTP status code represents a failure
// that should increment the circuit breaker counter.
// Per Phase 11.1: only 5xx and transport faults count; 4xx NEVER counts.
// 401 NEVER counts (even one triggering refetch-retry).
// SEAM's own 400/429/500 never count (they're generated by SEAM, not the upstream).
func IsFailureStatusCode(status int) bool {
	// 5xx from upstream counts as failure
	if status >= 500 && status <= 599 {
		return true
	}

	// Everything else (including all 4xx) does NOT count
	// This is deliberately permissive: 401, 429, etc. are not failures
	return false
}

// IsFailureError determines if an error represents a transport failure.
// Transport faults: refused, reset, DNS errors, TLS handshake failures.
func IsFailureError(err error) bool {
	if err == nil {
		return false
	}

	// Check for common transport fault patterns
	errStr := err.Error()

	// Connection refused
	if strings.Contains(errStr, "connection refused") {
		return true
	}

	// Connection reset
	if strings.Contains(errStr, "connection reset") {
		return true
	}

	// DNS errors
	if strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "lookup") ||
		strings.Contains(errStr, "dns") {
		return true
	}

	// TLS errors
	if strings.Contains(errStr, "tls") ||
		strings.Contains(errStr, "certificate") ||
		strings.Contains(errStr, "handshake") {
		return true
	}

	// Timeout
	if strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "deadline exceeded") {
		return true
	}

	return false
}

// ShouldCountAsFailure determines if a response/error should be counted
// as a circuit breaker failure. Returns true if the error or status code
// represents a failure per Phase 11.1 rules.
func ShouldCountAsFailure(err error, statusCode int) bool {
	// Transport faults always count
	if IsFailureError(err) {
		return true
	}

	// Upstream 5xx counts
	if IsFailureStatusCode(statusCode) {
		return true
	}

	// Everything else does NOT count (including all 4xx)
	return false
}
