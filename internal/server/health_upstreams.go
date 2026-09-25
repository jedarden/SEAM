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

	// RouteTable is the route-table health the cache-health-sentinel design
	// deferred as "future: route table health".
	RouteTable RouteTableHealth `json:"route_table"`
}

// RouteTableHealth is the route-table half of the /health/upstreams response:
// fragment load state, live route count, and the last hot-reload result.
// Fragment counts and LastLoaded describe the last successful (re)load — a
// failed hot reload discards the replacement FragmentLoader, so they hold
// their previous values while HotReload.FailureCount climbs.
type RouteTableHealth struct {
	// FragmentMode reports whether the server merged its route table from
	// fragments (true) or a single static spec file (false). In static mode
	// the fragment counts are 0 and LastLoaded and HotReload are absent.
	FragmentMode bool `json:"fragment_mode"`

	// FragmentsLoaded is the number of valid (non-quarantined) fragments.
	FragmentsLoaded int `json:"fragments_loaded"`

	// FragmentsQuarantined is the number of fragments quarantined by
	// validation or path-collision detection.
	FragmentsQuarantined int `json:"fragments_quarantined"`

	// Routes is the number of routes in the live route table. Zero is also
	// what a nil holder or uninitialized table reports.
	Routes int `json:"routes"`

	// LastLoaded is when the fragment loader last read the fragments tree
	// (UTC); absent in static mode or before the first load.
	LastLoaded *time.Time `json:"last_loaded,omitempty"`

	// HotReload is the last hot-reload result; absent when the hot-reload
	// manager is not running (static mode, or fragment mode before Enable).
	HotReload *HotReloadHealth `json:"hot_reload,omitempty"`
}

// HotReloadHealth mirrors HotReloadManager.Status() with a stable JSON shape.
type HotReloadHealth struct {
	// Enabled reports whether the hot-reload manager is watching mounts.
	Enabled bool `json:"enabled"`

	// InProgress reports whether a reload is running right now.
	InProgress bool `json:"in_progress"`

	// ReloadCount is the number of successful reloads since process start.
	ReloadCount uint64 `json:"reload_count"`

	// FailureCount is the number of failed reloads since process start.
	FailureCount uint64 `json:"failure_count"`

	// LastReload is when the last reload succeeded (UTC); null when no
	// reload has succeeded yet.
	LastReload *time.Time `json:"last_reload_time"`
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
			Upstream:       last2xx.Upstream,
			Last2xx:        last2xx,
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
		RouteTable: s.routeTableHealth(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

// routeTableHealth projects the fragment loader, route table, and hot-reload
// manager onto the route-table health shape. Every source is nil-safe so the
// endpoint keeps answering in static mode and during partial initialization.
func (s *Server) routeTableHealth() RouteTableHealth {
	health := RouteTableHealth{
		Routes: s.routeTableHolder.RouteCount(), // nil-safe: reports zero
	}

	if s.hotReloadManager != nil {
		health.HotReload = hotReloadHealthFromStatus(s.hotReloadManager.Status())
	}

	if s.specLoader == nil || s.specLoader.FragmentLoader == nil {
		return health
	}

	fl := s.specLoader.FragmentLoader
	health.FragmentMode = true
	health.FragmentsLoaded = fl.GetValidFragmentCount()
	health.FragmentsQuarantined = fl.GetQuarantinedCount()
	if loaded := fl.LastLoaded(); !loaded.IsZero() {
		ts := loaded.UTC()
		health.LastLoaded = &ts
	}

	return health
}

// hotReloadHealthFromStatus projects HotReloadManager.Status() onto the stable
// HotReloadHealth wire shape, tolerating absent or mistyped keys so a change
// in the status map degrades to zero values rather than a panic.
func hotReloadHealthFromStatus(status map[string]interface{}) *HotReloadHealth {
	health := &HotReloadHealth{}
	if v, ok := status["enabled"].(bool); ok {
		health.Enabled = v
	}
	if v, ok := status["in_progress"].(bool); ok {
		health.InProgress = v
	}
	if v, ok := status["reload_count"].(uint64); ok {
		health.ReloadCount = v
	}
	if v, ok := status["failure_count"].(uint64); ok {
		health.FailureCount = v
	}
	if v, ok := status["last_reload_time"].(time.Time); ok && !v.IsZero() {
		ts := v.UTC()
		health.LastReload = &ts
	}
	return health
}
