package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// CredentialHealthResponse is the read-only health-sentinel view of the
// credential subsystem. It contains breaker metadata only; credential values
// are never included.
type CredentialHealthResponse struct {
	Status          string                 `json:"status"`
	Timestamp       time.Time              `json:"timestamp"`
	Credentials     CredentialAvailability `json:"credentials"`
	CircuitBreaker  CircuitBreakerHealth   `json:"circuit_breaker"`
	CircuitBreakers []CircuitBreakerStatus `json:"circuit_breakers,omitempty"`
}

// CredentialAvailability describes whether the credential subsystem is
// available without exposing the credential or its retrieval path.
type CredentialAvailability struct {
	Available   bool       `json:"available"`
	LastRefresh *time.Time `json:"last_refresh,omitempty"`
}

// CircuitBreakerHealth is an aggregate view for callers that only need the
// overall state. The per-origin records remain available in circuit_breakers.
type CircuitBreakerHealth struct {
	Enabled             bool                `json:"enabled"`
	State               CircuitBreakerState `json:"state"`
	ConsecutiveFailures int                 `json:"consecutive_failures"`
	OpenedAt            *time.Time          `json:"opened_at,omitempty"`
	LastError           string              `json:"last_error,omitempty"`
	RetryAfterSeconds   int                 `json:"retry_after_seconds,omitempty"`
}

// CredentialHealthSentinel renders current credential and breaker health. It
// does not perform a probe and therefore cannot mutate breaker state.
type CredentialHealthSentinel struct {
	breakers CircuitBreakerStateProvider
}

// CircuitBreakerStates returns the registry used by the server's health
// sentinel. The per-origin breaker implementation can publish updates here;
// callers receive only state metadata, never credentials.
func (s *Server) CircuitBreakerStates() *CircuitBreakerStateRegistry {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.circuitBreakers == nil {
		s.circuitBreakers = NewCircuitBreakerStateRegistry()
	}
	return s.circuitBreakers
}

func (s *Server) credentialsHealthHandler(w http.ResponseWriter, r *http.Request) {
	NewCredentialHealthSentinel(s.CircuitBreakerStates()).ServeHTTP(w, r)
}

// NewCredentialHealthSentinel creates a health sentinel backed by breaker
// state. A nil provider is treated as an empty registry.
func NewCredentialHealthSentinel(breakers CircuitBreakerStateProvider) *CredentialHealthSentinel {
	return &CredentialHealthSentinel{breakers: breakers}
}

// Snapshot builds a fresh response on every call. In particular, callers must
// not cache this result because an open breaker must become visible promptly.
func (h *CredentialHealthSentinel) Snapshot() CredentialHealthResponse {
	var states []CircuitBreakerStatus
	if h != nil && h.breakers != nil {
		states = h.breakers.Snapshot()
	}

	return CredentialHealthResponse{
		Status:          credentialHealthStatus(states),
		Timestamp:       time.Now().UTC(),
		Credentials:     CredentialAvailability{Available: true},
		CircuitBreaker:  aggregateCircuitBreakerHealth(states),
		CircuitBreakers: states,
	}
}

// ServeHTTP exposes the operator health sentinel. Cache-Control is defensive
// for callers that invoke the handler without SEAM's reserved-path middleware;
// the middleware still guarantees that this endpoint never enters the cache.
func (h *CredentialHealthSentinel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(h.Snapshot())
}

func credentialHealthStatus(states []CircuitBreakerStatus) string {
	status := "healthy"
	for _, state := range states {
		switch state.State {
		case CircuitBreakerOpen:
			return "unhealthy"
		case CircuitBreakerHalfOpen:
			status = "degraded"
		}
	}
	return status
}

func aggregateCircuitBreakerHealth(states []CircuitBreakerStatus) CircuitBreakerHealth {
	aggregate := CircuitBreakerHealth{State: CircuitBreakerClosed}
	for _, state := range states {
		if state.Enabled {
			aggregate.Enabled = true
		}
		stateRank := breakerStateRank(state.State)
		aggregateRank := breakerStateRank(aggregate.State)
		if stateRank > aggregateRank ||
			(stateRank == aggregateRank && state.ConsecutiveFailures > aggregate.ConsecutiveFailures) {
			aggregate.State = state.State
			aggregate.ConsecutiveFailures = state.ConsecutiveFailures
			aggregate.OpenedAt = state.OpenedAt
			aggregate.LastError = state.LastError
			aggregate.RetryAfterSeconds = state.RetryAfterSeconds
		}
	}
	return aggregate
}

func breakerStateRank(state CircuitBreakerState) int {
	switch state {
	case CircuitBreakerOpen:
		return 2
	case CircuitBreakerHalfOpen:
		return 1
	default:
		return 0
	}
}
