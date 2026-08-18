package server

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// CircuitBreakerState is the externally visible state of a circuit breaker.
// The JSON spelling is kept stable because it is consumed by health tooling.
type CircuitBreakerState string

const (
	CircuitBreakerClosed   CircuitBreakerState = "closed"
	CircuitBreakerOpen     CircuitBreakerState = "open"
	CircuitBreakerHalfOpen CircuitBreakerState = "half_open"
)

// CircuitBreakerStatus is the non-secret state published by a circuit breaker.
// A per-origin breaker can update this record without coupling its request path
// to the health sentinel.
type CircuitBreakerStatus struct {
	Origin              string              `json:"origin"`
	State               CircuitBreakerState `json:"state"`
	Enabled             bool                `json:"enabled"`
	ConsecutiveFailures int                 `json:"consecutive_failures"`
	OpenedAt            *time.Time          `json:"opened_at,omitempty"`
	LastError           string              `json:"last_error,omitempty"`
	RetryAfterSeconds   int                 `json:"retry_after_seconds,omitempty"`
	Source              string              `json:"source,omitempty"`
}

// CircuitBreakerStateProvider supplies a consistent snapshot for health
// responses. Implementations must not return mutable state owned by the
// provider.
type CircuitBreakerStateProvider interface {
	Snapshot() []CircuitBreakerStatus
}

// CircuitBreakerStateRegistry is the in-process publication point shared by
// request handling and the health sentinel. It intentionally stores state by
// resolved origin, leaving breaker policy and admission decisions to the
// circuit-breaker implementation.
type CircuitBreakerStateRegistry struct {
	mu     sync.RWMutex
	states map[string]CircuitBreakerStatus
}

// NewCircuitBreakerStateRegistry creates an empty breaker-state registry.
func NewCircuitBreakerStateRegistry() *CircuitBreakerStateRegistry {
	return &CircuitBreakerStateRegistry{states: make(map[string]CircuitBreakerStatus)}
}

// Set publishes the current state for an origin. Empty origins are ignored so
// a malformed update cannot create an unaddressable health entry.
func (r *CircuitBreakerStateRegistry) Set(status CircuitBreakerStatus) {
	if r == nil {
		return
	}

	origin := strings.TrimSpace(status.Origin)
	if origin == "" {
		return
	}
	status.Origin = origin
	if status.State == "" {
		status.State = CircuitBreakerClosed
	}
	if status.ConsecutiveFailures < 0 {
		status.ConsecutiveFailures = 0
	}
	if status.RetryAfterSeconds < 0 {
		status.RetryAfterSeconds = 0
	}
	if status.OpenedAt != nil {
		openedAt := status.OpenedAt.UTC()
		status.OpenedAt = &openedAt
	}

	r.mu.Lock()
	if r.states == nil {
		r.states = make(map[string]CircuitBreakerStatus)
	}
	r.states[origin] = status
	r.mu.Unlock()

	// Update Prometheus metrics for upstream health
	setUpstreamHealth(origin, status.State, status.ConsecutiveFailures)
}

// Remove stops publishing state for an origin that is no longer configured.
func (r *CircuitBreakerStateRegistry) Remove(origin string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.states, strings.TrimSpace(origin))
	r.mu.Unlock()
}

// Snapshot returns a deterministic, deep-enough copy for JSON encoding. The
// ordering prevents health responses from changing merely because map order
// changed between requests.
func (r *CircuitBreakerStateRegistry) Snapshot() []CircuitBreakerStatus {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	states := make([]CircuitBreakerStatus, 0, len(r.states))
	for _, status := range r.states {
		if status.OpenedAt != nil {
			openedAt := *status.OpenedAt
			status.OpenedAt = &openedAt
		}
		states = append(states, status)
	}
	r.mu.RUnlock()

	sort.Slice(states, func(i, j int) bool {
		return states[i].Origin < states[j].Origin
	})
	return states
}
