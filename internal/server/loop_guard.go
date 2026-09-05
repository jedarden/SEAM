package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LoopGuardConfig holds the loop guard configuration parameters.
type LoopGuardConfig struct {
	// MaxRepeats is the number of identical failing requests tolerated within one window before the loop guard intervenes.
	MaxRepeats int

	// Window is the tumbling window duration (e.g., "10m", "1h").
	// Window format: ^[0-9]+(s|m|h|d)$
	// Process-anchored: restart resets unconditionally.
	Window string

	// windowDuration is the parsed window duration.
	windowDuration time.Duration
}

// DefaultLoopGuardConfig returns the default loop guard configuration.
func DefaultLoopGuardConfig() LoopGuardConfig {
	return LoopGuardConfig{
		MaxRepeats:     5,
		Window:         "10m",
		windowDuration: 10 * time.Minute,
	}
}

// ParseWindow parses the window duration string (e.g., "10m", "1h", "30s").
func ParseWindow(window string) (time.Duration, error) {
	if window == "" {
		return 0, fmt.Errorf("window cannot be empty")
	}

	// Window format: ^[0-9]+(s|m|h|d)$
	re := regexp.MustCompile(`^(\d+)(s|m|h|d)$`)
	matches := re.FindStringSubmatch(window)
	if matches == nil {
		return 0, fmt.Errorf("invalid window format: %s (expected format: ^[0-9]+(s|m|h|d)$)", window)
	}

	value, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, fmt.Errorf("invalid window value: %s", matches[1])
	}

	unit := matches[2]
	switch unit {
	case "s":
		return time.Duration(value) * time.Second, nil
	case "m":
		return time.Duration(value) * time.Minute, nil
	case "h":
		return time.Duration(value) * time.Hour, nil
	case "d":
		return time.Duration(value) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported window unit: %s", unit)
	}
}

// Validate validates the loop guard configuration and parses the window duration.
func (c *LoopGuardConfig) Validate() error {
	if c.MaxRepeats < 1 {
		return fmt.Errorf("maxRepeats must be at least 1, got %d", c.MaxRepeats)
	}

	if c.Window == "" {
		return fmt.Errorf("window cannot be empty")
	}

	duration, err := ParseWindow(c.Window)
	if err != nil {
		return err
	}
	c.windowDuration = duration

	return nil
}

// LoopGuard tracks identical failing requests per route to detect loops.
// Key is (route, normalized hash) - strictly more conservative pre-Phase 7.
// Phase 7 narrows with no fragment change.
type LoopGuard struct {
	// routeID identifies the route this guard protects
	routeID string

	// config is the active loop guard configuration
	config LoopGuardConfig

	// mu protects the hash tracking map
	mu sync.RWMutex

	// hashTracks tracks the failure count and last failure time for each hash
	hashTracks map[string]*hashTrack

	// windowStart is when the current tumbling window started
	windowStart time.Time

	// processStart is when this process started (for window anchoring)
	processStart time.Time
}

// hashTrack tracks failure state for a single normalized request hash.
type hashTrack struct {
	// failureCount is the number of consecutive failures for this hash
	failureCount int

	// lastFailureTime is when the last failure was recorded
	lastFailureTime time.Time

	// lastSuccessTime is when the last 2xx success was recorded
	lastSuccessTime time.Time
}

// NewLoopGuard creates a new loop guard for the given route.
func NewLoopGuard(routeID string, config LoopGuardConfig) (*LoopGuard, error) {
	if config.MaxRepeats < 1 {
		config.MaxRepeats = DefaultLoopGuardConfig().MaxRepeats
	}
	if config.Window == "" {
		config.Window = DefaultLoopGuardConfig().Window
		config.windowDuration = DefaultLoopGuardConfig().windowDuration
	} else {
		if err := config.Validate(); err != nil {
			return nil, err
		}
	}

	now := time.Now()
	return &LoopGuard{
		routeID:      routeID,
		config:       config,
		hashTracks:   make(map[string]*hashTrack),
		windowStart:  now,
		processStart: now,
	}, nil
}

// CheckRequest checks if a request should be allowed through.
// Returns (allow, retryAfterSeconds, error).
// If allow is false, the caller should return a 429 response.
func (lg *LoopGuard) CheckRequest(hash string) (bool, int, error) {
	lg.mu.Lock()
	defer lg.mu.Unlock()

	now := time.Now()

	// Check if we need to roll the window
	if lg.shouldRollWindowLocked(now) {
		lg.rollWindowLocked(now)
	}

	track := lg.hashTracks[hash]

	// No previous failures for this hash - allow
	if track == nil {
		return true, 0, nil
	}

	// Check if we've had a successful 2xx since the last failure
	if !track.lastSuccessTime.IsZero() && track.lastSuccessTime.After(track.lastFailureTime) {
		// Success cleared the failure run - allow
		return true, 0, nil
	}

	// Check if the failure run has outgrown the tolerated repeat budget.
	// MaxRepeats failures are still allowed through; only a run that has
	// already exceeded them blocks the next attempt.
	if track.failureCount > lg.config.MaxRepeats {
		// Calculate retry-after: seconds to window close
		windowEnd := lg.windowStart.Add(lg.config.windowDuration)
		remaining := windowEnd.Sub(now)
		retryAfter := int(remaining.Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		return false, retryAfter, nil
	}

	return true, 0, nil
}

// RecordSuccess records a successful response (2xx) for the given hash.
// Per Phase 13.1: A 2xx on the same hash clears that hash's counter.
func (lg *LoopGuard) RecordSuccess(hash string) {
	lg.mu.Lock()
	defer lg.mu.Unlock()

	now := time.Now()

	// Check if we need to roll the window
	if lg.shouldRollWindowLocked(now) {
		lg.rollWindowLocked(now)
	}

	track := lg.hashTracks[hash]
	if track == nil {
		// No failures yet, nothing to clear
		return
	}

	// Record the success time and clear the run. Per Phase 13.1 a 2xx on the
	// same hash clears that hash's counter, so the next failure starts a fresh
	// run rather than resuming the pre-success tally.
	track.failureCount = 0
	track.lastSuccessTime = now
}

// RecordFailure records a failed response (non-2xx) for the given hash.
func (lg *LoopGuard) RecordFailure(hash string) {
	lg.mu.Lock()
	defer lg.mu.Unlock()

	now := time.Now()

	// Check if we need to roll the window
	if lg.shouldRollWindowLocked(now) {
		lg.rollWindowLocked(now)
	}

	track := lg.hashTracks[hash]
	if track == nil {
		track = &hashTrack{}
		lg.hashTracks[hash] = track
	}

	// Only increment if this is a continuing failure run (no recent success)
	if track.lastSuccessTime.IsZero() || track.lastSuccessTime.Before(track.lastFailureTime) {
		track.failureCount++
	}
	track.lastFailureTime = now
}

// shouldRollWindowLocked checks if the tumbling window should roll.
// Caller must hold lg.mu.
func (lg *LoopGuard) shouldRollWindowLocked(now time.Time) bool {
	windowEnd := lg.windowStart.Add(lg.config.windowDuration)
	return now.After(windowEnd)
}

// rollWindowLocked rolls the tumbling window and clears stale hash tracks.
// Caller must hold lg.mu.
func (lg *LoopGuard) rollWindowLocked(now time.Time) {
	// Reset window start (tumbling window anchored at process start)
	// Find the next window boundary
	elapsed := now.Sub(lg.processStart)
	windowsElapsed := int(elapsed / lg.config.windowDuration)
	lg.windowStart = lg.processStart.Add(time.Duration(windowsElapsed) * lg.config.windowDuration)

	// Clear all hash tracks - window is fresh
	// (We could also selectively clear, but full clear is simpler and correct)
	lg.hashTracks = make(map[string]*hashTrack)
}

// RouteID returns the route ID this guard protects.
func (lg *LoopGuard) RouteID() string {
	return lg.routeID
}

// Config returns the loop guard configuration.
func (lg *LoopGuard) Config() LoopGuardConfig {
	return lg.config
}

// Snapshot returns the current loop guard state for testing/inspection.
func (lg *LoopGuard) Snapshot() map[string]interface{} {
	lg.mu.RLock()
	defer lg.mu.RUnlock()

	tracks := make(map[string]interface{})
	for hash, track := range lg.hashTracks {
		tracks[hash] = map[string]interface{}{
			"failure_count":     track.failureCount,
			"last_failure_time": track.lastFailureTime,
			"last_success_time": track.lastSuccessTime,
		}
	}

	return map[string]interface{}{
		"route_id":      lg.routeID,
		"max_repeats":   lg.config.MaxRepeats,
		"window":        lg.config.Window,
		"window_start":  lg.windowStart,
		"process_start": lg.processStart,
		"hash_tracks":   tracks,
	}
}

// RequestHasher computes canonical hashes for loop guard detection.
// Per Phase 13.1: Hash order is uppercased method; matched path template +
// name-sorted resolved bindings; query params name-then-value sorted,
// percent-decoded first; canonical JSON body (RFC 8785-style) over the Phase
// 2 tee - above maxReplayableRequestBytes hash raw streamed bytes (strictly
// coarser, under-match only); EVERYTHING else excluded, headers wholesale.
// Hash taken AFTER adapter transform (inert until Phase 8).
type RequestHasher struct {
	// maxReplayableRequestBytes is the threshold above which we hash raw bytes
	maxReplayableRequestBytes int64
}

// NewRequestHasher creates a new request hasher.
func NewRequestHasher(maxReplayableRequestBytes int64) *RequestHasher {
	return &RequestHasher{
		maxReplayableRequestBytes: maxReplayableRequestBytes,
	}
}

// ComputeHash computes the canonical hash for a request.
func (rh *RequestHasher) ComputeHash(method, pathTemplate string, pathParams map[string]string, query url.Values, body []byte) string {
	// Phase 13.1 hash preimage order:
	// 1. Upper-cased method
	// 2. Matched path template + name-sorted resolved bindings
	// 3. Query params name-then-value sorted, percent-decoded
	// 4. Canonical JSON body (RFC 8785-style) OR raw bytes if above maxReplayableRequestBytes

	hash := sha256.New()

	// 1. Upper-cased method
	hash.Write([]byte(strings.ToUpper(method)))
	hash.Write([]byte("\x00"))

	// 2. Matched path template + name-sorted resolved bindings
	hash.Write([]byte(pathTemplate))
	hash.Write([]byte("\x00"))

	// Sort path parameter names
	sortedParamNames := make([]string, 0, len(pathParams))
	for name := range pathParams {
		sortedParamNames = append(sortedParamNames, name)
	}
	sort.Strings(sortedParamNames)

	// Write path parameters
	for _, name := range sortedParamNames {
		value := pathParams[name]
		hash.Write([]byte(name))
		hash.Write([]byte("="))
		hash.Write([]byte(value))
		hash.Write([]byte("\x00"))
	}
	hash.Write([]byte("\x00"))

	// 3. Query params name-then-value sorted, percent-decoded
	if len(query) > 0 {
		sortedQueryNames := make([]string, 0, len(query))
		for name := range query {
			sortedQueryNames = append(sortedQueryNames, name)
		}
		sort.Strings(sortedQueryNames)

		for _, name := range sortedQueryNames {
			values := query[name]
			sort.Strings(values) // Sort multi-value params

			for _, value := range values {
				// Percent-decode the query parameter
				decoded, err := url.PathUnescape(value)
				if err != nil {
					// If decoding fails, use original value
					decoded = value
				}

				hash.Write([]byte(name))
				hash.Write([]byte("="))
				hash.Write([]byte(decoded))
				hash.Write([]byte("\x00"))
			}
		}
	}
	hash.Write([]byte("\x00"))

	// 4. Body: canonical JSON if small enough, raw bytes if large
	if len(body) > 0 {
		if int64(len(body)) > rh.maxReplayableRequestBytes {
			// Hash raw bytes (strictly coarser, under-match only)
			hash.Write(body)
		} else {
			// Try to canonicalize as JSON (RFC 8785-style)
			canonical, err := canonicalJSON(body)
			if err != nil {
				// If not JSON or canonicalization fails, hash raw bytes
				hash.Write(body)
			} else {
				hash.Write(canonical)
			}
		}
	}

	// Return hex-encoded hash (first 16 chars for efficiency, like spec hash)
	hashBytes := hash.Sum(nil)
	return hex.EncodeToString(hashBytes)[:16]
}

// canonicalJSON produces RFC 8785-style canonical JSON:
// - Keys sorted
// - Whitespace stripped (no spaces, no newlines)
// - Compact encoding
func canonicalJSON(data []byte) ([]byte, error) {
	var obj interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}

	// Re-marshal with sorted keys and no whitespace
	return json.Marshal(obj)
}

// ReadReplayableBody reads the request body in a replayable manner.
// This uses the Phase 2 tee if available.
func ReadReplayableBody(r *http.Request, maxSize int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}

	// Read one byte past the limit so an over-limit body is detectable
	// without having to buffer the whole thing.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSize+1))
	if err != nil {
		// Hand back whatever was read before the failure, followed by the
		// unread remainder, so a downstream handler still sees every byte.
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}

	if int64(len(body)) > maxSize {
		// Over the limit: hash only the prefix (strictly coarser, under-match
		// only, per Phase 13.1) but put back every byte read plus the unread
		// remainder, so the upstream still receives the complete body.
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		return body[:maxSize], nil
	}

	// Restore the body for re-reading
	r.Body = io.NopCloser(bytes.NewReader(body))

	return body, nil
}
