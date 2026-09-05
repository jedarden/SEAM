package server

import (
	"fmt"
	"sync"
	"time"
)

// Last2xxState is the three-state representation of last-2xx tracking.
// Phase 11.2: Per-path and per-upstream tracking of last successful 2xx response.
type Last2xxState string

const (
	// Last2xxNoAttempt indicates no attempt has been made since last restart.
	Last2xxNoAttempt Last2xxState = "no_attempt_since_restart"

	// Last2xxNoSuccess indicates attempts were made but none succeeded.
	Last2xxNoSuccess Last2xxState = "no_success_in_attempts_since_restart"

	// Last2xxSucceeded indicates at least one success has occurred.
	Last2xxSucceeded Last2xxState = "last_succeeded"
)

// Last2xxStatus represents the three-state last-2xx tracking for a path or upstream.
// All tracking is in-memory and restart-scoped (lost on process restart).
type Last2xxStatus struct {
	// Path is the route path (e.g., "/api/v1/users"). Empty for upstream-level tracking.
	Path string `json:"path,omitempty"`

	// Upstream is the resolved origin (e.g., "https://api.example.com:443").
	Upstream string `json:"upstream,omitempty"`

	// State is one of: no_attempt_since_restart, no_success_in_attempts_since_restart, last_succeeded.
	State Last2xxState `json:"state"`

	// LastAttemptAt is when the last attempt was made (restart-scoped).
	// Zero time means no attempt since restart.
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`

	// LastSuccessAt is when the last 2xx response was received (restart-scoped).
	// Zero time means no success since restart (intentionally empty in first two states).
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`

	// AttemptsSinceLastSuccess is the count of attempts since the last successful 2xx response.
	// Zero means we've never succeeded or all attempts succeeded.
	AttemptsSinceLastSuccess int `json:"attempts_since_last_success"`

	// LastError is the last error message from the circuit breaker (restart-scoped).
	// Only populated when State is no_success_in_attempts_since_restart.
	LastError string `json:"last_error,omitempty"`

	// Source indicates how the last success was achieved.
	// Empty for passive requests; populated by probe-labeling in Phase 12.
	Source string `json:"source,omitempty"`
}

// Last2xxTracker tracks last-2xx state per path and per upstream.
// All tracking is in-memory and restart-scoped (lost on process restart).
type Last2xxTracker struct {
	mu      sync.RWMutex
	paths   map[string]*last2xxEntry // Key: path template (e.g., "/api/v1/users")
	origins map[string]*last2xxEntry // Key: origin (e.g., "https://api.example.com:443")
}

// last2xxEntry holds the tracking data for a single path or upstream.
type last2xxEntry struct {
	lastAttemptAt            *time.Time
	lastSuccessAt            *time.Time
	attemptsSinceLastSuccess int
	lastError                string
	source                   string
	mu                       sync.RWMutex
}

// NewLast2xxTracker creates a new tracker for last-2xx state.
func NewLast2xxTracker() *Last2xxTracker {
	return &Last2xxTracker{
		paths:   make(map[string]*last2xxEntry),
		origins: make(map[string]*last2xxEntry),
	}
}

// RecordAttempt records that an attempt was made for the given path and upstream.
// Must be called before the request is sent.
func (t *Last2xxTracker) RecordAttempt(path, upstream string) {
	t.recordAttempt(path, t.paths)
	t.recordAttempt(upstream, t.origins)
}

func (t *Last2xxTracker) recordAttempt(key string, store map[string]*last2xxEntry) {
	if key == "" {
		return
	}

	t.mu.Lock()
	entry, ok := store[key]
	if !ok {
		entry = &last2xxEntry{}
		store[key] = entry
	}
	t.mu.Unlock()

	now := time.Now().UTC()
	entry.mu.Lock()
	defer entry.mu.Unlock()

	entry.lastAttemptAt = &now
	entry.attemptsSinceLastSuccess++
}

// RecordSuccess records a successful 2xx response for the given path and upstream.
// Must be called after receiving a 2xx response.
func (t *Last2xxTracker) RecordSuccess(path, upstream string, source string) {
	t.recordSuccess(path, t.paths, source)
	t.recordSuccess(upstream, t.origins, source)
}

func (t *Last2xxTracker) recordSuccess(key string, store map[string]*last2xxEntry, source string) {
	if key == "" {
		return
	}

	t.mu.Lock()
	entry, ok := store[key]
	if !ok {
		entry = &last2xxEntry{}
		store[key] = entry
	}
	t.mu.Unlock()

	now := time.Now().UTC()
	entry.mu.Lock()
	defer entry.mu.Unlock()

	entry.lastSuccessAt = &now
	entry.attemptsSinceLastSuccess = 0
	entry.lastError = ""
	entry.source = source
}

// RecordError records an error for the given path and upstream.
// Must be called when a request fails (non-2xx response or transport error).
func (t *Last2xxTracker) RecordError(path, upstream string, errMsg string) {
	t.recordError(path, t.paths, errMsg)
	t.recordError(upstream, t.origins, errMsg)
}

func (t *Last2xxTracker) recordError(key string, store map[string]*last2xxEntry, errMsg string) {
	if key == "" {
		return
	}

	t.mu.Lock()
	entry, ok := store[key]
	if !ok {
		entry = &last2xxEntry{}
		store[key] = entry
	}
	t.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()

	entry.lastError = errMsg
}

// GetPathStatus returns the current last-2xx status for a path.
func (t *Last2xxTracker) GetPathStatus(path string) Last2xxStatus {
	return t.getStatus(path, t.paths, "", "")
}

// GetUpstreamStatus returns the current last-2xx status for an upstream.
func (t *Last2xxTracker) GetUpstreamStatus(upstream string) Last2xxStatus {
	return t.getStatus(upstream, t.origins, "", "")
}

// GetStatus returns the current last-2xx status with both path and upstream populated.
func (t *Last2xxTracker) GetStatus(path, upstream string) Last2xxStatus {
	return t.getStatus(upstream, t.origins, path, upstream)
}

func (t *Last2xxTracker) getStatus(key string, store map[string]*last2xxEntry, path, upstream string) Last2xxStatus {
	if key == "" {
		return Last2xxStatus{
			State: Last2xxNoAttempt,
		}
	}

	t.mu.RLock()
	entry, ok := store[key]
	t.mu.RUnlock()

	if !ok {
		return Last2xxStatus{
			State: Last2xxNoAttempt,
		}
	}

	entry.mu.RLock()
	defer entry.mu.RUnlock()

	status := Last2xxStatus{
		Path:     path,
		Upstream: upstream,
	}

	if entry.lastAttemptAt == nil {
		// No attempt since restart
		status.State = Last2xxNoAttempt
		return status
	}

	status.LastAttemptAt = copyTimePtr(entry.lastAttemptAt)

	if entry.lastSuccessAt == nil {
		// Attempts made but no success
		status.State = Last2xxNoSuccess
		status.AttemptsSinceLastSuccess = entry.attemptsSinceLastSuccess
		status.LastError = entry.lastError
		return status
	}

	// Last succeeded
	status.State = Last2xxSucceeded
	status.LastSuccessAt = copyTimePtr(entry.lastSuccessAt)
	status.AttemptsSinceLastSuccess = entry.attemptsSinceLastSuccess
	status.Source = entry.source

	return status
}

// GetAllPathStatuses returns all path-level statuses.
func (t *Last2xxTracker) GetAllPathStatuses() []Last2xxStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	statuses := make([]Last2xxStatus, 0, len(t.paths))
	for path := range t.paths {
		status := t.GetPathStatus(path)
		status.Path = path
		statuses = append(statuses, status)
	}

	return statuses
}

// GetAllUpstreamStatuses returns all upstream-level statuses.
func (t *Last2xxTracker) GetAllUpstreamStatuses() []Last2xxStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	statuses := make([]Last2xxStatus, 0, len(t.origins))
	for upstream := range t.origins {
		status := t.GetUpstreamStatus(upstream)
		status.Upstream = upstream
		statuses = append(statuses, status)
	}

	return statuses
}

// String returns a human-readable description of the state.
func (s Last2xxState) String() string {
	switch s {
	case Last2xxNoAttempt:
		return "no attempt since last restart"
	case Last2xxNoSuccess:
		return "no success in attempts since last restart"
	case Last2xxSucceeded:
		return "last succeeded"
	default:
		return "unknown"
	}
}

// Describe returns a detailed description including time ago.
func (s Last2xxStatus) Describe() string {
	now := time.Now().UTC()

	switch s.State {
	case Last2xxNoAttempt:
		return "no attempt since last restart"

	case Last2xxNoSuccess:
		ago := "never"
		if s.LastAttemptAt != nil {
			ago = durationFriendly(now.Sub(*s.LastAttemptAt))
		}
		return fmt.Sprintf("no success in %d attempts since last restart (last attempt: %s ago; last error: %s)",
			s.AttemptsSinceLastSuccess, ago, s.LastError)

	case Last2xxSucceeded:
		if s.LastSuccessAt == nil {
			return "last succeeded (time unknown)"
		}
		ago := durationFriendly(now.Sub(*s.LastSuccessAt))
		source := ""
		if s.Source != "" {
			source = fmt.Sprintf(" (source: %s)", s.Source)
		}
		return fmt.Sprintf("last succeeded %s ago%s", ago, source)

	default:
		return "unknown state"
	}
}

// durationFriendly converts a duration to a human-readable string.
func durationFriendly(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// copyTimePtr creates a copy of a time pointer for safe export.
func copyTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	copied := *t
	return &copied
}
