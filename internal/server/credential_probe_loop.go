package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ardenone/seam/internal/vault"
)

// CredentialProbeLoop runs the background credential health probe loop.
// It acquires leadership via Kubernetes Lease before probing and releases
// the lease on shutdown. Without leadership, it serves traffic but probes
// NOTHING (fail closed).
type CredentialProbeLoop struct {
	mu               sync.RWMutex
	leaseLeader      *LeaseLeader
	probeRegistry    *CredentialProbeRegistry
	routeTableHolder *ThreadSafeTableHolder
	vaultClient      *vault.Client
	httpClient       *http.Client
	stopCh           chan struct{}
	stopped          bool
	probeTargets     map[string]*CredentialProbeTarget // key: "fragmentID:instanceID"
	lastProbeTimes   map[string]time.Time              // key: "fragmentID:instanceID"
	shutdownSignal   chan os.Signal
	onShutdown       func()
}

// ProbeLoopConfig holds the configuration for the credential probe loop.
type ProbeLoopConfig struct {
	// LeaseLeader is the Kubernetes Lease leader elector
	LeaseLeader *LeaseLeader

	// ProbeRegistry tracks probe results
	ProbeRegistry *CredentialProbeRegistry

	// RouteTableHolder provides access to current route table
	RouteTableHolder *ThreadSafeTableHolder

	// VaultClient fetches credentials from OpenBao
	VaultClient *vault.Client

	// HTTPClient makes probe requests
	HTTPClient *http.Client

	// OnShutdown is called when the loop stops
	OnShutdown func()
}

// NewCredentialProbeLoop creates a new credential probe loop.
func NewCredentialProbeLoop(cfg ProbeLoopConfig) (*CredentialProbeLoop, error) {
	if cfg.LeaseLeader == nil {
		return nil, fmt.Errorf("lease leader is required")
	}
	if cfg.ProbeRegistry == nil {
		cfg.ProbeRegistry = NewCredentialProbeRegistry()
	}
	if cfg.RouteTableHolder == nil {
		return nil, fmt.Errorf("route table holder is required")
	}
	if cfg.VaultClient == nil {
		return nil, fmt.Errorf("vault client is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout: 30 * time.Second,
		}
	}

	return &CredentialProbeLoop{
		leaseLeader:      cfg.LeaseLeader,
		probeRegistry:    cfg.ProbeRegistry,
		routeTableHolder: cfg.RouteTableHolder,
		vaultClient:      cfg.VaultClient,
		httpClient:       cfg.HTTPClient,
		stopCh:           make(chan struct{}),
		probeTargets:     make(map[string]*CredentialProbeTarget),
		lastProbeTimes:   make(map[string]time.Time),
		shutdownSignal:   make(chan os.Signal, 1),
		onShutdown:       cfg.OnShutdown,
	}, nil
}

// Start begins the credential probe loop.
// It acquires leadership, then runs probes at each fragment's configured cadence.
// Blocks until the loop is stopped or leadership is lost.
func (l *CredentialProbeLoop) Start(ctx context.Context) error {
	if l == nil {
		return fmt.Errorf("probe loop is nil")
	}

	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return fmt.Errorf("probe loop already stopped")
	}
	l.mu.Unlock()

	// Acquire leadership before probing
	log.Printf("[ProbeLoop] Attempting to acquire leadership for credential probing")
	if !l.leaseLeader.Acquire(ctx) {
		return fmt.Errorf("failed to acquire leadership")
	}

	log.Printf("[ProbeLoop] Leadership acquired, starting credential probe loop")

	// Start the lease renewal goroutine
	renewCtx, cancelRenew := context.WithCancel(ctx)
	defer cancelRenew()

	go func() {
		l.leaseLeader.Renew(renewCtx)
		log.Printf("[ProbeLoop] Leadership lost, stopping probe loop")
		l.Stop()
	}()

	// Build probe targets from current route table
	l.buildProbeTargets()

	// Main probe loop
	ticker := time.NewTicker(10 * time.Second) // Check every 10 seconds for due probes
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[ProbeLoop] Context cancelled, stopping probe loop")
			return ctx.Err()
		case <-l.stopCh:
			log.Printf("[ProbeLoop] Stop signal received, stopping probe loop")
			return nil
		case <-l.shutdownSignal:
			log.Printf("[ProbeLoop] Shutdown signal received, stopping probe loop")
			if l.onShutdown != nil {
				l.onShutdown()
			}
			return nil
		case <-ticker.C:
			if !l.leaseLeader.IsLeader() {
				log.Printf("[ProbeLoop] No longer leader, exiting probe loop")
				return nil
			}

			// Probe any due targets
			l.probeDueTargets(ctx)
		}
	}
}

// buildProbeTargets builds the probe target map from the current route table.
// This is called on startup and when the route table is reloaded.
func (l *CredentialProbeLoop) buildProbeTargets() {
	l.mu.Lock()
	defer l.mu.Unlock()

	routes := l.routeTableHolder.Snapshot()
	if routes == nil {
		return
	}

	newTargets := make(map[string]*CredentialProbeTarget)
	newLastProbeTimes := make(map[string]time.Time)

	// Preserve last probe times from existing targets
	for key, lastTime := range l.lastProbeTimes {
		newLastProbeTimes[key] = lastTime
	}

	for _, route := range routes {
		if route.CredentialProbeConfig == nil {
			continue // No probe configured for this route
		}

		// Determine fragment ID
		fragmentID := l.fragmentIDForRoute(route)

		// Build targets for each instance
		targets := l.buildTargetsForRoute(fragmentID, route)
		for _, target := range targets {
			key := target.FragmentID + ":" + target.InstanceID
			newTargets[key] = target

			// Preserve last probe time if target existed before
			if oldTime, exists := l.lastProbeTimes[key]; exists {
				newLastProbeTimes[key] = oldTime
			}
		}
	}

	l.probeTargets = newTargets
	l.lastProbeTimes = newLastProbeTimes

	log.Printf("[ProbeLoop] Built %d probe targets from route table", len(newTargets))
}

// fragmentIDForRoute generates a fragment ID from a route entry.
func (l *CredentialProbeLoop) fragmentIDForRoute(route RouteEntry) string {
	// Use path template and method as fragment identifier
	// This is stable across reloads
	return strings.TrimPrefix(route.PathTemplate, "/") + ":" + strings.ToLower(route.Method)
}

// buildTargetsForRoute builds probe targets for a route's instances.
func (l *CredentialProbeLoop) buildTargetsForRoute(fragmentID string, route RouteEntry) []*CredentialProbeTarget {
	var targets []*CredentialProbeTarget

	probeConfig := route.CredentialProbeConfig
	if probeConfig == nil {
		return targets
	}

	// Default interval from fragment-root config
	defaultInterval, _ := probeConfig.ParseInterval()

	if len(route.UpstreamMap) == 0 {
		// Single-instance route
		target := &CredentialProbeTarget{
			FragmentID:  fragmentID,
			InstanceID:  "_default",
			VaultPath:   route.VaultPath,
			ProbeURL:    route.UpstreamTarget + probeConfig.Path,
			ProbePath:   probeConfig.Path,
			ProbeMethod: probeConfig.Method,
			Interval:    defaultInterval,
			InjectAs:    route.InjectAs,
		}
		targets = append(targets, target)
		return targets
	}

	// Multi-instance route
	for instanceID, instance := range route.UpstreamMap {
		// Per-instance probeInterval overrides fragment-root default
		interval := defaultInterval
		if instance.ProbeInterval > 0 {
			interval = instance.ProbeInterval
		}

		target := &CredentialProbeTarget{
			FragmentID:  fragmentID,
			InstanceID:  instanceID,
			VaultPath:   instance.VaultPath,
			ProbeURL:    instance.URL + probeConfig.Path,
			ProbePath:   probeConfig.Path,
			ProbeMethod: probeConfig.Method,
			Interval:    interval,
			InjectAs:    instance.InjectAs,
		}
		targets = append(targets, target)
	}

	return targets
}

// probeDueTargets probes any targets that are due for probing.
func (l *CredentialProbeLoop) probeDueTargets(ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	for key, target := range l.probeTargets {
		lastProbe, exists := l.lastProbeTimes[key]
		if !exists {
			// Never probed - probe now
			l.probeTarget(ctx, target, now)
			continue
		}

		// Check if probe is due
		if now.Sub(lastProbe) >= target.Interval {
			l.probeTarget(ctx, target, now)
		}
	}
}

// probeTarget executes a single credential probe.
func (l *CredentialProbeLoop) probeTarget(ctx context.Context, target *CredentialProbeTarget, now time.Time) {
	startTime := time.Now()

	result := CredentialProbeResult{
		FragmentID: target.FragmentID,
		InstanceID: target.InstanceID,
		Status:     CredentialUnknown,
	}

	key := target.FragmentID + ":" + target.InstanceID
	defer func() {
		l.probeRegistry.Set(result)
		l.lastProbeTimes[key] = now
	}()

	// Fetch credential from OpenBao
	secret, err := l.vaultClient.GetSecret(ctx, target.VaultPath)
	if err != nil {
		result.Status = CredentialUnhealthy
		result.LastError = fmt.Sprintf("failed to fetch credential: %v", err)
		result.ConsecutiveFailures++
		log.Printf("[ProbeLoop] Probe failed for %s:%s: %v", target.FragmentID, target.InstanceID, err)
		return
	}

	// Extract credential value
	credValue, err := credentialValue(secret)
	if err != nil {
		result.Status = CredentialUnhealthy
		result.LastError = fmt.Sprintf("failed to extract credential value: %v", err)
		result.ConsecutiveFailures++
		log.Printf("[ProbeLoop] Probe failed for %s:%s: %v", target.FragmentID, target.InstanceID, err)
		return
	}

	// Build probe request with IN-PROCESS origin tag
	// The origin tag is set in context, not as a wire header (X-SEAM-* are stripped)
	probeCtx := contextWithProbeOrigin(ctx, target.FragmentID, target.InstanceID)

	// Build authenticated probe request
	req, err := http.NewRequestWithContext(probeCtx, target.ProbeMethod, target.ProbeURL, nil)
	if err != nil {
		result.Status = CredentialUnhealthy
		result.LastError = fmt.Sprintf("failed to build probe request: %v", err)
		result.ConsecutiveFailures++
		log.Printf("[ProbeLoop] Probe failed for %s:%s: %v", target.FragmentID, target.InstanceID, err)
		return
	}

	// Inject credential based on InjectAs configuration
	if err := InjectSecret(req, target.InjectAs, credValue); err != nil {
		result.Status = CredentialUnhealthy
		result.LastError = fmt.Sprintf("failed to inject credential: %v", err)
		result.ConsecutiveFailures++
		log.Printf("[ProbeLoop] Probe failed for %s:%s: %v", target.FragmentID, target.InstanceID, err)
		return
	}

	// Mark request as a probe (excluded from quota, loop breaker, retirement counter)
	// This header is checked by isProbeRequest() in loop_guard_middleware.go and quota_middleware.go
	req.Header.Set("X-SEAM-Probe", "true")

	// Execute probe
	resp, err := l.httpClient.Do(req)
	if err != nil {
		result.Status = CredentialUnhealthy
		result.LastError = fmt.Sprintf("probe request failed: %v", err)
		result.ConsecutiveFailures++
		log.Printf("[ProbeLoop] Probe failed for %s:%s: %v", target.FragmentID, target.InstanceID, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Record probe spend rate
	result.ProbeSpendRate = time.Since(startTime).Milliseconds()

	// Evaluate response
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		result.Status = CredentialHealthy
		tv := now
		result.LastVerified = &tv
		result.ConsecutiveFailures = 0
		log.Printf("[ProbeLoop] Probe succeeded for %s:%s (status=%d, spend=%dms)",
			target.FragmentID, target.InstanceID, resp.StatusCode, result.ProbeSpendRate)
	} else if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		// 4xx errors suggest credential issues (auth/forbidden)
		result.Status = CredentialUnhealthy
		result.LastError = fmt.Sprintf("probe returned %d", resp.StatusCode)
		result.ConsecutiveFailures++
		log.Printf("[ProbeLoop] Probe unhealthy for %s:%s: status=%d",
			target.FragmentID, target.InstanceID, resp.StatusCode)
	} else {
		// 5xx or other errors are degraded
		result.Status = CredentialDegraded
		result.LastError = fmt.Sprintf("probe returned %d", resp.StatusCode)
		log.Printf("[ProbeLoop] Probe degraded for %s:%s: status=%d",
			target.FragmentID, target.InstanceID, resp.StatusCode)
	}
}

// contextWithProbeOrigin adds probe origin metadata to context.
// This is an IN-PROCESS tag that never becomes a wire header.
func contextWithProbeOrigin(ctx context.Context, fragmentID, instanceID string) context.Context {
	return context.WithValue(ctx, contextKey(probeOriginContextKey), probeOriginMetadata{
		FragmentID: fragmentID,
		InstanceID: instanceID,
		Timestamp:  time.Now(),
	})
}

const (
	probeOriginContextKey contextKey = iota + 1000 // Avoid collision with existing keys
)

type probeOriginMetadata struct {
	FragmentID string
	InstanceID string
	Timestamp  time.Time
}

// Stop stops the credential probe loop and releases leadership.
func (l *CredentialProbeLoop) Stop() {
	if l == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.stopped {
		return
	}

	l.stopped = true
	close(l.stopCh)

	// Release lease leadership
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	l.leaseLeader.Release(ctx)

	log.Printf("[ProbeLoop] Credential probe loop stopped")
}

// IsRunning reports whether the probe loop is currently running.
func (l *CredentialProbeLoop) IsRunning() bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return !l.stopped && l.leaseLeader.IsLeader()
}

// OnReload rebuilds probe targets when the route table is reloaded.
func (l *CredentialProbeLoop) OnReload() {
	if l == nil {
		return
	}
	l.buildProbeTargets()
}
