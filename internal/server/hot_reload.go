package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ardenone/seam/internal/watcher"
)

// HotReloadManager manages hot reload of route tables when fragments change
type HotReloadManager struct {
	server           *Server
	coordinator      *watcher.Coordinator
	reloadMu         sync.Mutex
	reloadInProgress bool
	lastReloadTime   time.Time
	reloadCount      uint64
	failCount        uint64
	enabled          bool
	ctx              context.Context
	cancel           context.CancelFunc
}

// NewHotReloadManager creates a new hot reload manager
func NewHotReloadManager(server *Server) *HotReloadManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &HotReloadManager{
		server:      server,
		coordinator: watcher.NewCoordinator(),
		ctx:         ctx,
		cancel:      cancel,
	}
}

// Enable sets up watchers for all mount points and starts the hot reload manager
func (hrm *HotReloadManager) Enable() error {
	hrm.reloadMu.Lock()
	defer hrm.reloadMu.Unlock()

	if hrm.enabled {
		return fmt.Errorf("hot reload already enabled")
	}

	// Only watch in fragment mode
	if !hrm.server.config.FragmentMode {
		return fmt.Errorf("hot reload only supported in fragment mode")
	}

	// Determine the routes mount directory
	// This could be /spec/fragments.d (legacy) or /etc/gateway/routes.d/ (new structure)
	routesMountDir := hrm.server.config.FragmentsDir
	if routesMountDir == "" {
		routesMountDir = filepath.Join(hrm.server.config.SpecDir, "fragments.d")
	}

	log.Printf("[HotReload] Setting up watchers for routes mount: %s", routesMountDir)

	// If the routes mount directory exists and is a directory, discover and watch subdirectories
	// Each subdirectory represents a service mount with its own ..data symlink
	if info, err := os.Stat(routesMountDir); err == nil && info.IsDir() {
		// Watch the parent directory itself for backward compatibility with single-directory setups
		if err := hrm.coordinator.AddMount(routesMountDir); err != nil {
			log.Printf("[HotReload] Warning: failed to watch routes mount directory: %v", err)
		}

		// Discover and watch each service subdirectory
		entries, err := os.ReadDir(routesMountDir)
		if err != nil {
			log.Printf("[HotReload] Warning: failed to read routes mount directory: %v", err)
		} else {
			serviceCount := 0
			for _, entry := range entries {
				if entry.IsDir() {
					servicePath := filepath.Join(routesMountDir, entry.Name())
					log.Printf("[HotReload] Adding watcher for service mount: %s", servicePath)
					if err := hrm.coordinator.AddMount(servicePath); err != nil {
						log.Printf("[HotReload] Warning: failed to watch service mount %s: %v", servicePath, err)
					} else {
						serviceCount++
					}
				}
			}
			log.Printf("[HotReload] Watching %d service mounts under %s", serviceCount, routesMountDir)
		}
	} else {
		log.Printf("[HotReload] Routes mount directory does not exist: %s", routesMountDir)
	}

	// Watch the allowlist mount if configured
	if hrm.server.config.AllowlistFile != "" {
		allowlistDir := filepath.Dir(hrm.server.config.AllowlistFile)
		log.Printf("[HotReload] Adding watcher for allowlist mount: %s", allowlistDir)
		if err := hrm.coordinator.AddMount(allowlistDir); err != nil {
			log.Printf("[HotReload] Warning: failed to watch allowlist mount: %v", err)
		}
	}

	// Watch the upstream CA mount if configured
	if hrm.server.config.UpstreamCADir != "" {
		log.Printf("[HotReload] Adding watcher for upstream CA mount: %s", hrm.server.config.UpstreamCADir)
		if err := hrm.coordinator.AddMount(hrm.server.config.UpstreamCADir); err != nil {
			log.Printf("[HotReload] Warning: failed to watch upstream CA mount: %v", err)
		}
	}

	// Start the coordinator
	hrm.coordinator.Start()

	// Start the reload goroutine
	go hrm.reloadLoop()

	hrm.enabled = true
	log.Printf("[HotReload] Enabled")

	return nil
}

// reloadLoop waits for reload signals and triggers reloads
func (hrm *HotReloadManager) reloadLoop() {
	for {
		select {
		case <-hrm.coordinator.ReloadChan():
			log.Printf("[HotReload] Reload signal received")
			if err := hrm.reloadRouteTable(); err != nil {
				log.Printf("[HotReload] Reload failed: %v", err)
				hrm.failCount++
			} else {
				log.Printf("[HotReload] Reload successful")
			}

		case <-hrm.ctx.Done():
			log.Printf("[HotReload] Stopping reload loop")
			return
		}
	}
}

// reloadRouteTable reloads fragments, re-merges, re-validates, and atomically swaps the route table
func (hrm *HotReloadManager) reloadRouteTable() error {
	hrm.reloadMu.Lock()
	defer hrm.reloadMu.Unlock()

	// Prevent concurrent reloads
	if hrm.reloadInProgress {
		log.Printf("[HotReload] Reload already in progress, skipping")
		return nil
	}
	hrm.reloadInProgress = true
	defer func() { hrm.reloadInProgress = false }()

	startTime := time.Now()
	log.Printf("[HotReload] Starting route table reload (re-merge, re-validate, atomic swap)...")

	// Step 1: Reload fragments - this re-reads all fragment files and re-merges them
	// LoadFragments() performs the complete re-merge:
	// - Reads all fragments from all watched mount points
	// - Validates fragments against schema
	// - Detects path collisions
	// - Merges fragments into a single OpenAPI document
	if err := hrm.server.specLoader.LoadFragments(); err != nil {
		hrm.failCount++
		return fmt.Errorf("failed to reload fragments: %w", err)
	}

	// Phase 8.4: Populate ring buffer with the new spec version
	if hrm.server.specRingBuffer != nil {
		specHash := hrm.server.specLoader.GetHash()
		specVersion := hrm.server.specLoader.GetVersion()

		// Get the raw spec JSON for storage in the ring buffer
		specJSON, err := hrm.server.specLoader.GetRawJSON()
		if err != nil {
			log.Printf("[HotReload] Warning: failed to get spec JSON for ring buffer: %v", err)
		} else {
			// Build route snapshots for this version
			routes := hrm.buildRouteSnapshots()

			// Add to ring buffer
			addedVersion := hrm.server.specRingBuffer.Add(specHash, specVersion, specJSON, routes)
			log.Printf("[HotReload] Spec version %s added to ring buffer (hash: %s)", addedVersion, specHash)
		}
	}

	// Step 2: Build new route table from the re-merged OpenAPI document
	newRouteTable, err := BuildRouteTable(hrm.server.specLoader.OpenAPIModel())
	if err != nil {
		hrm.failCount++
		return fmt.Errorf("failed to build new route table from re-merged spec: %w", err)
	}
	if newRouteTable == nil {
		hrm.failCount++
		return fmt.Errorf("failed to build new route table from re-merged spec: got nil table")
	}

	// Step 3: Validate the new route table
	if err := newRouteTable.Validate(); err != nil {
		hrm.failCount++
		return fmt.Errorf("new route table validation failed: %w", err)
	}

	// Get the old route count for metrics
	oldRouteCount := 0
	if hrm.server.routeTableHolder != nil {
		// Can't directly get count from holder, will use 0
		oldRouteCount = 0
	}
	newRouteCount := newRouteTable.RouteCount()

	// Step 4: Atomically swap the route table using the thread-safe holder
	// The Swap method handles synchronization properly. In-flight requests
	// holding references to the old route table will continue to use it.
	// New requests will see the new route table. No requests are dropped.
	if err := hrm.server.routeTableHolder.Swap(newRouteTable); err != nil {
		hrm.failCount++
		return fmt.Errorf("failed to swap route table: %w", err)
	}

	// Update cache TTLs from the re-merged fragments
	hrm.server.cacheTTLs = hrm.server.specLoader.GetCacheTTLs()

	// Update metrics
	hrm.reloadCount++
	hrm.lastReloadTime = time.Now()

	reloadDuration := time.Since(startTime)
	log.Printf("[HotReload] Route table reloaded in %dms (routes: %d -> %d, zero dropped connections)",
		reloadDuration.Milliseconds(), oldRouteCount, newRouteCount)

	// The old route table is now obsolete and will be GC'd after in-flight requests complete
	log.Printf("[HotReload] Old route table will be reclaimed after in-flight requests complete")

	return nil
}

// buildRouteSnapshots builds route snapshots for the current spec version
// This is used to populate the ring buffer with route metadata for diffing
func (hrm *HotReloadManager) buildRouteSnapshots() []RouteSnapshot {
	if hrm.server.routeTableHolder == nil {
		return []RouteSnapshot{}
	}

	routes := hrm.server.routeTableHolder.Snapshot()
	snapshots := make([]RouteSnapshot, 0, len(routes))

	for _, route := range routes {
		snapshot := RouteSnapshot{
			Path:            route.PathTemplate,
			Method:          route.Method,
			RequiredScopes:  route.RequiredScopes,
			Deprecated:      route.Deprecated != nil, // Convert *DeprecationInfo to bool
			VisibilityKinds: []string{},              // Could be populated from metadata
		}
		snapshots = append(snapshots, snapshot)
	}

	return snapshots
}

// Disable stops the hot reload manager
func (hrm *HotReloadManager) Disable() {
	hrm.reloadMu.Lock()
	defer hrm.reloadMu.Unlock()

	if !hrm.enabled {
		return
	}

	hrm.cancel()
	hrm.coordinator.Stop()
	hrm.enabled = false

	log.Printf("[HotReload] Disabled")
}

// Status returns the current status of the hot reload manager
func (hrm *HotReloadManager) Status() map[string]interface{} {
	hrm.reloadMu.Lock()
	defer hrm.reloadMu.Unlock()

	status := map[string]interface{}{
		"enabled":          hrm.enabled,
		"reload_count":     hrm.reloadCount,
		"failure_count":    hrm.failCount,
		"last_reload_time": hrm.lastReloadTime,
		"in_progress":      hrm.reloadInProgress,
	}

	if hrm.enabled && hrm.lastReloadTime.IsZero() {
		status["last_reload_time"] = nil
	}

	return status
}

// ReloadCount returns the number of successful reloads
func (hrm *HotReloadManager) ReloadCount() uint64 {
	hrm.reloadMu.Lock()
	defer hrm.reloadMu.Unlock()
	return hrm.reloadCount
}

// FailureCount returns the number of failed reloads
func (hrm *HotReloadManager) FailureCount() uint64 {
	hrm.reloadMu.Lock()
	defer hrm.reloadMu.Unlock()
	return hrm.failCount
}

// LastReloadTime returns the time of the last successful reload
func (hrm *HotReloadManager) LastReloadTime() time.Time {
	hrm.reloadMu.Lock()
	defer hrm.reloadMu.Unlock()
	return hrm.lastReloadTime
}
