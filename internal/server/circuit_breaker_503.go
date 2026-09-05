package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// CircuitBreakerRefusedResponse is the structured 503 response returned when
// a circuit breaker refuses a request. Per Phase 11.1, it includes upstream,
// openedAt, lastError, and derived Retry-After (seconds remaining, floored at 1).
type CircuitBreakerRefusedResponse struct {
	Error string `json:"error"` // Always "service_unavailable"

	// Upstream is the resolved origin (scheme+host+port) that refused the request.
	Upstream string `json:"upstream"`

	// OpenedAt is when the breaker entered the open state.
	OpenedAt time.Time `json:"opened_at"`

	// LastError is the last failure message that triggered the breaker.
	LastError string `json:"last_error,omitempty"`

	// RetryAfter is derived from the breaker's backoff duration and elapsed time.
	// It is the number of seconds remaining before the breaker will admit requests,
	// floored at 1 second per Phase 11.1 spec.
	RetryAfter int `json:"retry_after"`
}

// WriteCircuitBreakerRefused writes a structured 503 response when the circuit
// breaker refuses a request. It sets both the response body and the Retry-After
// header to the same value.
func WriteCircuitBreakerRefused(w http.ResponseWriter, r *http.Request, status CircuitBreakerStatus) {
	response := CircuitBreakerRefusedResponse{
		Error:      "service_unavailable",
		Upstream:   status.Origin,
		LastError:  status.LastError,
		RetryAfter: status.RetryAfterSeconds,
	}

	if status.OpenedAt != nil {
		response.OpenedAt = *status.OpenedAt
	}

	// Set Retry-After header to match body
	w.Header().Set("Retry-After", fmt.Sprintf("%d", status.RetryAfterSeconds))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusServiceUnavailable)

	payload, err := json.Marshal(response)
	if err != nil {
		log.Printf("[circuit-breaker] Failed to marshal 503 response: %v", err)
		// Fall back to basic 503
		return
	}

	payload = append(payload, '\n')
	_, _ = w.Write(payload)
}
