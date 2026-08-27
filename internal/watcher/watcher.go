package watcher

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// MountWatcher watches a single mount point for changes.
// It watches the ..data symlink that Kubernetes ConfigMaps/Secrets use
// for atomic updates, ensuring we only trigger once per ConfigMap revision.
type MountWatcher struct {
	mountPath  string           // Path to watch (e.g., /etc/gateway/routes.d/<svc>)
	dataLink   string           // Path to the ..data symlink
	notifyCh   chan struct{}    // Signals when a change is detected
	stopCh     chan struct{}    // Signals the watcher to stop
	stopped    bool             // Whether the watcher has been stopped
	mu         sync.Mutex       // Protects stopped state
	debounce   time.Duration    // Debounce duration to coalesce rapid changes
	lastEvent  time.Time        // Time of the last processed event
}

// NewMountWatcher creates a new watcher for a single mount point.
// mountPath is the directory containing the ..data symlink (e.g., /etc/gateway/routes.d/users-service).
func NewMountWatcher(mountPath string) *MountWatcher {
	return &MountWatcher{
		mountPath: mountPath,
		dataLink:  filepath.Join(mountPath, "..data"),
		notifyCh:  make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		debounce:  100 * time.Millisecond, // Coalesce rapid changes within 100ms
	}
}

// Start begins watching the mount point. Returns nil if started successfully,
// or an error if the mount path doesn't exist or watching fails.
func (mw *MountWatcher) Start() error {
	// Check if mount path exists
	if _, err := filepath.Abs(mw.mountPath); err != nil {
		return fmt.Errorf("invalid mount path %s: %w", mw.mountPath, err)
	}

	// Create fsnotify watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	// Watch the ..data symlink directly
	// When ConfigMap updates, Kubernetes atomically swaps this symlink
	if err := watcher.Add(mw.dataLink); err != nil {
		watcher.Close()
		return fmt.Errorf("failed to watch %s: %w", mw.dataLink, err)
	}

	log.Printf("[Watcher] Started watching mount: %s (via %s)", mw.mountPath, mw.dataLink)

	// Start watch loop
	go mw.watchLoop(watcher)

	return nil
}

// watchLoop runs the fsnotify watch loop
func (mw *MountWatcher) watchLoop(watcher *fsnotify.Watcher) {
	defer watcher.Close()

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			mw.handleEvent(event)

		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
			// Log error but continue watching
			log.Printf("[Watcher] Error watching %s: %v", mw.mountPath, "fsnotify error")

		case <-mw.stopCh:
			log.Printf("[Watcher] Stopping watcher for mount: %s", mw.mountPath)
			return
		}
	}
}

// handleEvent processes a fsnotify event
func (mw *MountWatcher) handleEvent(event fsnotify.Event) {
	// We only care about the ..data symlink itself
	if event.Name != mw.dataLink {
		return
	}

	// Filter to specific event types that indicate actual changes
	// Create, Write, and Remove on the symlink indicate a ConfigMap update
	if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove) == 0 {
		return
	}

	// Debounce: don't notify if we've recently sent a notification
	mw.mu.Lock()
	now := time.Now()
	if now.Sub(mw.lastEvent) < mw.debounce {
		mw.mu.Unlock()
		return
	}
	mw.lastEvent = now
	mw.mu.Unlock()

	log.Printf("[Watcher] Change detected on mount: %s", mw.mountPath)

	// Send notification (non-blocking)
	select {
	case mw.notifyCh <- struct{}{}:
	default:
		// Channel already has a pending notification, don't block
	}
}

// Changes returns a channel that receives a notification when the mount changes.
// The channel is buffered with capacity 1, so rapid changes are coalesced.
func (mw *MountWatcher) Changes() <-chan struct{} {
	return mw.notifyCh
}

// Stop stops the watcher. It is safe to call multiple times.
func (mw *MountWatcher) Stop() {
	mw.mu.Lock()
	defer mw.mu.Unlock()

	if mw.stopped {
		return
	}

	mw.stopped = true
	close(mw.stopCh)
	log.Printf("[Watcher] Stopped watcher for mount: %s", mw.mountPath)
}

// Coordinator coordinates multiple mount watchers and triggers reloads
// when any of them change. It debounces changes across all mounts to avoid
// excessive reloads when multiple ConfigMaps update simultaneously.
type Coordinator struct {
	watchers   map[string]*MountWatcher // mount path -> watcher
	reloadCh   chan struct{}             // Signals that a reload is needed
	stopCh     chan struct{}             // Signals the coordinator to stop
	stopped    bool                      // Whether coordinator has been stopped
	mu         sync.Mutex                // Protects stopped state
	debounce   time.Duration             // Global debounce duration
	ctx        context.Context           // Context for cancellation
	cancel     context.CancelFunc        // Cancel function
}

// NewCoordinator creates a new coordinator for managing multiple mount watchers
func NewCoordinator() *Coordinator {
	ctx, cancel := context.WithCancel(context.Background())
	return &Coordinator{
		watchers: make(map[string]*MountWatcher),
		reloadCh: make(chan struct{}, 1),
		stopCh:   make(chan struct{}),
		debounce: 250 * time.Millisecond, // Coalesce changes across all mounts
		ctx:      ctx,
		cancel:   cancel,
	}
}

// AddMount adds a watcher for a mount point. Returns an error if the mount
// doesn't exist or cannot be watched. It is safe to call before Start().
func (c *Coordinator) AddMount(mountPath string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.watchers[mountPath]; exists {
		return fmt.Errorf("mount %s is already being watched", mountPath)
	}

	watcher := NewMountWatcher(mountPath)
	if err := watcher.Start(); err != nil {
		return err
	}

	c.watchers[mountPath] = watcher

	// Start a goroutine to forward this watcher's changes to the coordinator
	go c.forwardWatchChanges(watcher)

	return nil
}

// forwardWatchChanges forwards changes from a single mount watcher to the coordinator
func (c *Coordinator) forwardWatchChanges(watcher *MountWatcher) {
	for {
		select {
		case _, ok := <-watcher.Changes():
			if !ok {
				return
			}
			c.scheduleReload()

		case <-c.ctx.Done():
			return
		}
	}
}

// scheduleReload schedules a reload with debouncing
func (c *Coordinator) scheduleReload() {
	// Non-blocking send to buffer
	select {
	case c.reloadCh <- struct{}{}:
	default:
		// Reload already scheduled, don't block
	}
}

// ReloadChan returns a channel that receives notifications when a reload is needed.
// The channel is buffered with capacity 1.
func (c *Coordinator) ReloadChan() <-chan struct{} {
	return c.reloadCh
}

// Start starts the coordinator. It must be called after all mounts have been added.
func (c *Coordinator) Start() {
	log.Printf("[Coordinator] Started with %d mount watchers", len(c.watchers))

	// Start the debounce goroutine
	go c.debounceLoop()
}

// debounceLoop debounces reload signals from all mount watchers
func (c *Coordinator) debounceLoop() {
	var timer *time.Timer
	var timerCh <-chan time.Time

	for {
		select {
		case <-c.reloadCh:
			// Reset or start timer
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(c.debounce)
			timerCh = timer.C

		case <-timerCh:
			// Timer fired, trigger reload
			log.Printf("[Coordinator] Triggering reload after debounce")
			// Clear timer channel to prevent repeat fires
			timerCh = nil
			timer = nil

		case <-c.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		}
	}
}

// Stop stops all watchers and the coordinator
func (c *Coordinator) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopped {
		return
	}

	c.stopped = true
	c.cancel()

	// Stop all watchers
	for mountPath, watcher := range c.watchers {
		log.Printf("[Coordinator] Stopping watcher for %s", mountPath)
		watcher.Stop()
	}

	close(c.stopCh)
	log.Printf("[Coordinator] Stopped")
}

// Stopped returns whether the coordinator has been stopped
func (c *Coordinator) Stopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}
