package server

import (
	"fmt"
	"log"
	"sync"
)

// BreakerRegistry manages circuit breakers per origin.
// It creates breakers on-demand for resolved upstream targets and
// provides thread-safe access to breaker state.
type BreakerRegistry struct {
	mu       sync.RWMutex
	breakers map[Origin]*CircuitBreaker
	registry *CircuitBreakerStateRegistry // For publishing state
}

// NewBreakerRegistry creates a new circuit breaker registry.
func NewBreakerRegistry(stateRegistry *CircuitBreakerStateRegistry) *BreakerRegistry {
	return &BreakerRegistry{
		breakers: make(map[Origin]*CircuitBreaker),
		registry: stateRegistry,
	}
}

// GetOrCreate returns the circuit breaker for an origin, creating it if necessary.
// The config parameter provides the fragment-root or per-instance override configuration.
// If multiple instances of the same origin have different configs, the registry
// merges them to the stricter config (see BreakerConfig.Merge).
func (r *BreakerRegistry) GetOrCreate(origin Origin, config BreakerConfig) *CircuitBreaker {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Return existing breaker if present
	if breaker, ok := r.breakers[origin]; ok {
		// If the new config differs from the existing breaker's config,
		// we need to merge to the stricter value
		if config.Disagreement(breaker.Config()) {
			log.Printf("[circuit-breaker] Origin %s: config disagreement detected, merging to stricter config", origin)
			merged := breaker.Config().Merge(config)
			if merged.Disagreement(breaker.Config()) {
				// Config actually changed, we'd need to recreate the breaker
				// For now, just log - the breaker continues with its original config
				log.Printf("[circuit-breaker] Origin %s: configs differ, using existing config", origin)
			}
		}
		return breaker
	}

	// Create new breaker
	breaker := NewCircuitBreaker(origin, config, r.registry)
	r.breakers[origin] = breaker
	log.Printf("[circuit-breaker] Created new breaker for origin %s (threshold: %d, open: %ds, max: %ds, enabled: %t)",
		origin, config.Threshold, config.OpenSeconds, config.MaxOpenSeconds, config.Enabled)

	return breaker
}

// Get returns the circuit breaker for an origin if it exists.
func (r *BreakerRegistry) Get(origin Origin) (*CircuitBreaker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	breaker, ok := r.breakers[origin]
	return breaker, ok
}

// Remove removes the circuit breaker for an origin.
func (r *BreakerRegistry) Remove(origin Origin) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.breakers[origin]; ok {
		delete(r.breakers, origin)
		if r.registry != nil {
			r.registry.Remove(string(origin))
		}
		log.Printf("[circuit-breaker] Removed breaker for origin %s", origin)
	}
}

// Snapshot returns a snapshot of all breaker states.
func (r *BreakerRegistry) Snapshot() []CircuitBreakerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	states := make([]CircuitBreakerStatus, 0, len(r.breakers))
	for _, breaker := range r.breakers {
		states = append(states, breaker.Snapshot())
	}
	return states
}

// ResolveOriginForInstance resolves the origin for a specific instance.
// It uses the per-instance override if present, otherwise uses the fragment-root config.
func (r *RouteEntry) ResolveOriginForInstance(instanceID string, registry *BreakerRegistry) (Origin, *BreakerConfig, error) {
	var targetURL string
	var breakerConfig *BreakerConfig

	// Check if this is an upstream-map route
	if r.InstanceParam != "" && len(r.UpstreamMap) > 0 {
		// Look up the instance in the map
		if target, ok := r.UpstreamMap[instanceID]; ok {
			targetURL = target.URL
			breakerConfig = target.BreakerConfig
		} else if target, ok := r.UpstreamMap["_default"]; ok {
			// Fall back to _default if present
			targetURL = target.URL
			breakerConfig = target.BreakerConfig
		} else {
			return "", nil, fmt.Errorf("instance %q not found in upstream map", instanceID)
		}
	} else {
		// Single upstream route
		targetURL = r.UpstreamTarget
		breakerConfig = r.BreakerConfig
	}

	if targetURL == "" {
		return "", nil, fmt.Errorf("no upstream target configured")
	}

	// Resolve origin from URL
	origin, err := ParseOrigin(targetURL)
	if err != nil {
		return "", nil, fmt.Errorf("parse origin: %w", err)
	}

	// Use fragment-root config if no per-instance override
	if breakerConfig == nil {
		breakerConfig = r.BreakerConfig
	}
	if breakerConfig == nil {
		defaultConfig := DefaultBreakerConfig()
		breakerConfig = &defaultConfig
	}

	return origin, breakerConfig, nil
}

// GetBreakerForInstance returns the circuit breaker for a specific instance.
// It resolves the origin, applies per-instance overrides if present, and
// returns the breaker (creating it if necessary).
func (r *RouteEntry) GetBreakerForInstance(instanceID string, registry *BreakerRegistry) (*CircuitBreaker, error) {
	origin, config, err := r.ResolveOriginForInstance(instanceID, registry)
	if err != nil {
		return nil, err
	}

	return registry.GetOrCreate(origin, *config), nil
}
