package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// UpstreamHealthResponse is the response from /health/upstreams.
// It aggregates per-upstream last-2xx state with circuit breaker state.
// All tracking is in-memory and restart-scoped (lost on process restart).
type UpstreamHealthResponse struct {
	// Timestamp is when this snapshot was taken (UTC).
	Timestamp time.Time `json:"timestamp"`

	// Upstreams is the list of all tracked upstreams with their last-2xx and breaker state.
	Upstreams []UpstreamHealthEntry `json:"upstreams"`
}

// UpstreamHealthEntry represents the health state of a single upstream.
type UpstreamHealthEntry struct {
	// Upstream is the resolved origin (e.g., "https://api.example.com:443").
	Upstream string `json:"upstream"`

	// Last2xx is the three-state last-2xx tracking for this upstream.
	Last2xx Last2xxStatus `json:"last_2xx"`

	// CircuitBreaker is the current circuit breaker state for this upstream.
	CircuitBreaker CircuitBreakerStatus `json:"circuit_breaker"`

	// Healthy is true if the upstream is considered healthy.
	// An upstream is healthy if it's not in a permanent failure state.
	Healthy bool `json:"healthy"`
}

// healthUpstreamsHandler returns per-upstream health with last-2xx state and circuit breaker state.
func (s *Server) healthUpstreamsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}

	// Get breaker states
	breakerStates := s.circuitBreakers.Snapshot()

	// Build upstream map from breaker states
	upstreamMap := make(map[string]CircuitBreakerStatus)
	for _, state := range breakerStates {
		upstreamMap[state.Origin] = state
	}

	// Get all last-2xx statuses
	last2xxStatuses := s.last2xxTracker.GetAllUpstreamStatuses()

	// Merge breaker and last-2xx state
	entries := make([]UpstreamHealthEntry, 0, len(last2xxStatuses))
	for _, last2xx := range last2xxStatuses {
		entry := UpstreamHealthEntry{
			Upstream:      last2xx.Upstream,
			Last2xx:       last2xx,
			CircuitBreaker: upstreamMap[last2xx.Upstream],
		}

		// Determine health: healthy if not in permanent failure state
		// An upstream is unhealthy if:
		// - Breaker is open and retry-after is high (> 5 minutes indicates permanent issue)
		// - Last2xx shows no success in many attempts (> 50 attempts suggests persistent failure)
		entry.Healthy = true

		if entry.CircuitBreaker.State == CircuitBreakerOpen {
			if entry.CircuitBreaker.RetryAfterSeconds > 300 {
				// Open with high retry-after suggests permanent failure
				entry.Healthy = false
			}
		}

		if entry.Last2xx.State == Last2xxNoSuccess {
			if entry.Last2xx.AttemptsSinceLastSuccess > 50 {
				// Many attempts with no success suggests persistent failure
				entry.Healthy = false
			}
		}

		entries = append(entries, entry)
	}

	// Include upstreams that have breaker state but no last-2xx tracking yet
	for origin, breakerState := range upstreamMap {
		found := false
		for _, entry := range entries {
			if entry.Upstream == origin {
				found = true
				break
			}
		}

		if !found {
			entries = append(entries, UpstreamHealthEntry{
				Upstream:       origin,
				Last2xx:        Last2xxStatus{State: Last2xxNoAttempt},
				CircuitBreaker: breakerState,
				Healthy:        breakerState.State != CircuitBreakerOpen || breakerState.RetryAfterSeconds <= 300,
			})
		}
	}

	// Sort by upstream for deterministic output
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Upstream < entries[j].Upstream
	})

	response := UpstreamHealthResponse{
		Timestamp:  time.Now().UTC(),
		Upstreams:  entries,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}
