package server

import (
	"context"
	"log"
	"sync"
	"time"
)

// QuotaTracker manages quota enforcement based on accumulated cost
type QuotaTracker struct {
	mu              sync.RWMutex
	quotas          map[string]*quotaState // keyed by scope (global, per-token, per-user, per-route)
	costPerCall     map[string]float64     // route -> cost per call
	globalCostAccum float64                // global accumulated cost
	requestCosts    map[string]float64     // per-route accumulated cost
	tokensCosts     map[string]float64     // per-token accumulated cost
	usersCosts      map[string]float64     // per-user accumulated cost
	windowStart     time.Time              // start of current quota window
	windowDuration  time.Duration          // quota window duration
}

// quotaState holds quota state for a specific scope
type quotaState struct {
	limit          float64       // maximum cost allowed in window
	accumulated    float64       // accumulated cost in current window
	windowStart    time.Time     // start of this scope's window
	windowDuration time.Duration // window duration
}

// QuotaConfig holds quota configuration
type QuotaConfig struct {
	Limit  float64       // maximum cost allowed
	Window time.Duration // time window
	Scope  string        // global, per-token, per-user, per-route
}

// NewQuotaTracker creates a new quota tracker
func NewQuotaTracker() *QuotaTracker {
	return &QuotaTracker{
		quotas:         make(map[string]*quotaState),
		costPerCall:    make(map[string]float64),
		requestCosts:   make(map[string]float64),
		tokensCosts:    make(map[string]float64),
		usersCosts:     make(map[string]float64),
		windowStart:    time.Now(),
		windowDuration: 1 * time.Hour, // default window
	}
}

// SetCostPerCall sets the cost per call for a route
func (qt *QuotaTracker) SetCostPerCall(route string, cost float64) {
	qt.mu.Lock()
	defer qt.mu.Unlock()
	qt.costPerCall[route] = cost
}

// GetCostPerCall retrieves the cost per call for a route
// Returns 0 if no cost is configured for the route
func (qt *QuotaTracker) GetCostPerCall(route string) float64 {
	qt.mu.RLock()
	defer qt.mu.RUnlock()

	if cost, exists := qt.costPerCall[route]; exists {
		return cost
	}
	return 0
}

// SetQuota configures quota for a specific scope
func (qt *QuotaTracker) SetQuota(route string, config QuotaConfig) {
	qt.mu.Lock()
	defer qt.mu.Unlock()

	key := qt.scopeKey(config.Scope, route)
	qt.quotas[key] = &quotaState{
		limit:          config.Limit,
		accumulated:    0,
		windowStart:    time.Now(),
		windowDuration: config.Window,
	}
}

// scopeKey generates a unique key for a quota scope
func (qt *QuotaTracker) scopeKey(scope, route string) string {
	switch scope {
	case "global":
		return "global"
	case "per-route":
		return "route:" + route
	case "per-token", "per-user":
		// These are dynamic keys based on request context
		return scope + ":"
	default:
		return "global"
	}
}

// CheckAndRecordQuota checks if a request is within quota and records the cost
// Returns (allowed, remainingCost, error)
// For cache hits, pass cost=0 to skip quota deduction
func (qt *QuotaTracker) CheckAndRecordQuota(ctx context.Context, route string, cost float64, token, user string) (bool, float64, error) {
	qt.mu.Lock()
	defer qt.mu.Unlock()

	log.Printf("[QuotaTracker] CheckAndRecordQuota: route=%s, cost=$%.2f, token_present=%t, user=%s", route, cost, token != "", user)

	// Check if window has expired and reset if needed
	now := time.Now()
	if now.Sub(qt.windowStart) >= qt.windowDuration {
		qt.resetWindow(now)
	}

	// If cost is 0 (cache hit), just check quota without deducting
	if cost == 0 {
		allowed, remaining := qt.checkQuotaOnly(route, token, user)
		log.Printf("[QuotaTracker] Cache hit path: allowed=%v, remaining=$%.2f", allowed, remaining)
		return allowed, remaining, nil
	}

	// Use the passed cost parameter (determined by middleware)
	// The middleware already looked up the cost and applied cache hit logic
	routeCost := cost
	log.Printf("[QuotaTracker] Recording cost: route=%s, routeCost=$%.2f", route, routeCost)

	// Check all applicable quotas
	allowed, remaining := qt.checkQuotas(route, routeCost, token, user)
	if !allowed {
		log.Printf("[QuotaTracker] Quota exceeded: remaining=$%.2f", remaining)
		return false, remaining, nil // Quota exceeded
	}

	// Record the cost
	qt.recordCost(route, routeCost, token, user)
	log.Printf("[QuotaTracker] Cost recorded: globalAccum=$%.2f, routeAccum=$%.2f", qt.globalCostAccum, qt.requestCosts[route])

	return true, remaining - routeCost, nil
}

// checkQuotaOnly checks quota without deducting (for cache hits)
func (qt *QuotaTracker) checkQuotaOnly(route, token, user string) (bool, float64) {
	// Check global quota
	if state, exists := qt.quotas["global"]; exists {
		if state.accumulated >= state.limit {
			return false, 0
		}
	}

	// Check per-route quota
	if state, exists := qt.quotas["route:"+route]; exists {
		if cost, ok := qt.requestCosts[route]; ok && cost >= state.limit {
			return false, 0
		}
	}

	// Check per-token quota
	if token != "" {
		if state, exists := qt.quotas["per-token:"+token]; exists {
			if cost, ok := qt.tokensCosts[token]; ok && cost >= state.limit {
				return false, 0
			}
		}
	}

	// Check per-user quota
	if user != "" {
		if state, exists := qt.quotas["per-user:"+user]; exists {
			if cost, ok := qt.usersCosts[user]; ok && cost >= state.limit {
				return false, 0
			}
		}
	}

	return true, 0
}

// checkQuotas checks all applicable quotas and returns if allowed and remaining cost
func (qt *QuotaTracker) checkQuotas(route string, cost float64, token, user string) (bool, float64) {
	minRemaining := float64(-1)

	// Check global quota
	if state, exists := qt.quotas["global"]; exists {
		remaining := state.limit - qt.globalCostAccum
		if remaining < cost {
			return false, remaining
		}
		if minRemaining == -1 || remaining < minRemaining {
			minRemaining = remaining
		}
	}

	// Check per-route quota
	if state, exists := qt.quotas["route:"+route]; exists {
		accumulated := qt.requestCosts[route]
		remaining := state.limit - accumulated
		if remaining < cost {
			return false, remaining
		}
		if minRemaining == -1 || remaining < minRemaining {
			minRemaining = remaining
		}
	}

	// Check per-token quota
	if token != "" {
		if state, exists := qt.quotas["per-token:"+token]; exists {
			accumulated := qt.tokensCosts[token]
			remaining := state.limit - accumulated
			if remaining < cost {
				return false, remaining
			}
			if minRemaining == -1 || remaining < minRemaining {
				minRemaining = remaining
			}
		}
	}

	// Check per-user quota
	if user != "" {
		if state, exists := qt.quotas["per-user:"+user]; exists {
			accumulated := qt.usersCosts[user]
			remaining := state.limit - accumulated
			if remaining < cost {
				return false, remaining
			}
			if minRemaining == -1 || remaining < minRemaining {
				minRemaining = remaining
			}
		}
	}

	return true, minRemaining
}

// recordCost records the cost for all applicable scopes
func (qt *QuotaTracker) recordCost(route string, cost float64, token, user string) {
	qt.globalCostAccum += cost
	qt.requestCosts[route] += cost

	if token != "" {
		qt.tokensCosts[token] += cost
	}
	if user != "" {
		qt.usersCosts[user] += cost
	}
}

// resetWindow resets the quota window
func (qt *QuotaTracker) resetWindow(now time.Time) {
	qt.windowStart = now
	qt.globalCostAccum = 0
	qt.requestCosts = make(map[string]float64)
	qt.tokensCosts = make(map[string]float64)
	qt.usersCosts = make(map[string]float64)

	// Reset individual quota states
	for _, state := range qt.quotas {
		state.accumulated = 0
		state.windowStart = now
	}
}

// GetQuotaStatus returns the current quota status
func (qt *QuotaTracker) GetQuotaStatus() map[string]interface{} {
	qt.mu.RLock()
	defer qt.mu.RUnlock()

	quotas := make(map[string]interface{})
	for key, state := range qt.quotas {
		accumulated := float64(0)
		switch {
		case key == "global":
			accumulated = qt.globalCostAccum
		case len(key) >= 6 && key[:6] == "route:":
			route := key[6:]
			accumulated = qt.requestCosts[route]
		default:
			// Handle per-token and per-user
		}

		quotas[key] = map[string]interface{}{
			"limit":           state.limit,
			"accumulated":     accumulated,
			"remaining":       state.limit - accumulated,
			"window_start":    state.windowStart,
			"window_duration": state.windowDuration.String(),
		}
	}

	return quotas
}
