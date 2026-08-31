package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RepairQueue manages auto-repairable starvation alerts in a persistent queue.
// Auto-repairable issues are queued for the repair daemon instead of creating
// human-blocked beads. Non-repairable issues still escalate to human review.
type RepairQueue struct {
	mu              sync.RWMutex
	queuePath       string // Path to the queue file (JSONL)
	maxQueueSize    int    // Maximum number of items in queue
	queueItems      []*RepairItem
	onItemAdded     func(item *RepairItem)
	onItemProcessed func(item *RepairItem, result *RepairResult)
}

// RepairItem represents an auto-repairable issue in the queue.
type RepairItem struct {
	ID          string    `json:"id"`           // Unique identifier (timestamp-workspace-alertID)
	Timestamp   time.Time `json:"timestamp"`    // When the item was queued
	Workspace   string    `json:"workspace"`    // Workspace path
	AlertID     string    `json:"alert_id"`     // Original starvation alert bead ID
	RootCause   string    `json:"root_cause"`   // Root cause category (e.g., "database-corrupt")
	Priority    int       `json:"priority"`     // 0-4, higher is more urgent
	Attempts    int       `json:"attempts"`     // Number of repair attempts so far
	MaxAttempts int       `json:"max_attempts"` // Maximum attempts before escalation
	Escalated   bool      `json:"escalated"`    // Whether escalated to human review
	QueuedBy    string    `json:"queued_by"`    // What system queued this (e.g., "starvation-recovery")
}

// RepairResult represents the outcome of a repair attempt.
type RepairResult struct {
	ItemID      string        `json:"item_id"`      // RepairItem ID
	Timestamp   time.Time      `json:"timestamp"`     // When the repair was attempted
	Success     bool          `json:"success"`       // Whether repair succeeded
	Strategy    string        `json:"strategy"`      // Repair strategy used
	Command     string        `json:"command"`       // Command that was executed
	Output      string        `json:"output"`        // Command output
	Error       string        `json:"error"`         // Error if failed
	Duration    time.Duration `json:"duration"`      // How long the repair took
	Attempts    int           `json:"attempts"`      // Total attempts for this item
	Escalated   bool          `json:"escalated"`     // Whether escalated to human review
	ReadyCount  int           `json:"ready_count"`   // Ready bead count after repair (for verification)
}

// RepairQueueConfig holds configuration for the repair queue.
type RepairQueueConfig struct {
	// QueuePath is the path to the queue file (JSONL format for append-only durability)
	QueuePath string

	// MaxQueueSize is the maximum number of items allowed in the queue (default: 1000)
	MaxQueueSize int

	// MaxAttemptsPerItem is the default maximum repair attempts before escalation (default: 3)
	MaxAttemptsPerItem int

	// OnItemAdded is called when an item is added to the queue
	OnItemAdded func(item *RepairItem)

	// OnItemProcessed is called when a repair attempt completes
	OnItemProcessed func(item *RepairItem, result *RepairResult)
}

// NewRepairQueue creates a new repair queue.
func NewRepairQueue(cfg RepairQueueConfig) (*RepairQueue, error) {
	if cfg.QueuePath == "" {
		return nil, fmt.Errorf("queue path is required")
	}
	if cfg.MaxQueueSize == 0 {
		cfg.MaxQueueSize = 1000
	}
	if cfg.MaxAttemptsPerItem == 0 {
		cfg.MaxAttemptsPerItem = 3
	}

	// Ensure queue directory exists
	queueDir := filepath.Dir(cfg.QueuePath)
	if err := os.MkdirAll(queueDir, 0755); err != nil {
		return nil, fmt.Errorf("create queue directory: %w", err)
	}

	queue := &RepairQueue{
		queuePath:       cfg.QueuePath,
		maxQueueSize:    cfg.MaxQueueSize,
		queueItems:      make([]*RepairItem, 0),
		onItemAdded:     cfg.OnItemAdded,
		onItemProcessed: cfg.OnItemProcessed,
	}

	// Load existing queue from disk
	if err := queue.load(); err != nil {
		log.Printf("[RepairQueue] Failed to load existing queue: %v (starting fresh)", err)
	}

	log.Printf("[RepairQueue] Repair queue initialized (path=%s, max_items=%d, max_attempts=%d, loaded=%d)",
		cfg.QueuePath, cfg.MaxQueueSize, cfg.MaxAttemptsPerItem, len(queue.queueItems))

	return queue, nil
}

// load loads the queue from disk (JSONL format).
func (q *RepairQueue) load() error {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Clear existing items
	q.queueItems = make([]*RepairItem, 0)

	// Open queue file for reading
	f, err := os.Open(q.queuePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Queue file doesn't exist yet - this is fine for first run
			return nil
		}
		return err
	}
	defer f.Close()

	// Parse JSONL file
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue // Skip empty lines
		}

		var item RepairItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			log.Printf("[RepairQueue] Failed to parse queue line %d: %v (skipping)", lineNum, err)
			continue
		}

		q.queueItems = append(q.queueItems, &item)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan queue file: %w", err)
	}

	return nil
}

// Enqueue adds a repair item to the queue if it doesn't already exist.
// Returns (enqueued, existingItem, error).
func (q *RepairQueue) Enqueue(ctx context.Context, item *RepairItem) (bool, *RepairItem, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Check if item already exists (by alert ID)
	for _, existing := range q.queueItems {
		if existing.AlertID == item.AlertID && existing.Workspace == item.Workspace {
			log.Printf("[RepairQueue] Item already exists for alert %s in %s (item_id=%s)",
				item.AlertID, item.Workspace, existing.ID)
			return false, existing, nil
		}
	}

	// Check queue size limit
	if len(q.queueItems) >= q.maxQueueSize {
		return false, nil, fmt.Errorf("queue full (max=%d)", q.maxQueueSize)
	}

	// Set defaults
	if item.MaxAttempts == 0 {
		item.MaxAttempts = 3
	}
	if item.Priority == 0 {
		item.Priority = 2 // Default to P2 (medium priority)
	}
	if item.Timestamp.IsZero() {
		item.Timestamp = time.Now()
	}
	if item.ID == "" {
		item.ID = fmt.Sprintf("%d-%s-%s", item.Timestamp.Unix(), filepath.Base(item.Workspace), item.AlertID)
	}
	if item.QueuedBy == "" {
		item.QueuedBy = "starvation-recovery"
	}

	// Append to queue
	q.queueItems = append(q.queueItems, item)

	// Persist to disk (append-only)
	if err := q.appendItem(item); err != nil {
		// Remove from memory if persist failed
		q.queueItems = q.queueItems[:len(q.queueItems)-1]
		return false, nil, fmt.Errorf("persist item: %w", err)
	}

	log.Printf("[RepairQueue] ✓ Enqueued repair item %s (root_cause=%s, workspace=%s, alert=%s, priority=%d)",
		item.ID, item.RootCause, item.Workspace, item.AlertID, item.Priority)

	// Notify callback
	if q.onItemAdded != nil {
		go q.onItemAdded(item)
	}

	return true, item, nil
}

// appendItem appends a single item to the queue file (JSONL append-only write).
func (q *RepairQueue) appendItem(item *RepairItem) error {
	f, err := os.OpenFile(q.queuePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	data, err := json.Marshal(item)
	if err != nil {
		return err
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}

	return nil
}

// Dequeue removes and returns the next item to repair.
// Items are returned in priority order (highest first), then FIFO.
func (q *RepairQueue) Dequeue(ctx context.Context) (*RepairItem, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.queueItems) == 0 {
		return nil, nil // Empty queue
	}

	// Find highest priority non-escalated item
	var selectedIndex int = -1
	var selectedPriority int = -1

	for i, item := range q.queueItems {
		if item.Escalated {
			continue // Skip escalated items
		}
		if item.Priority > selectedPriority {
			selectedIndex = i
			selectedPriority = item.Priority
		}
	}

	if selectedIndex == -1 {
		return nil, nil // No non-escalated items available
	}

	// Remove item from queue
	item := q.queueItems[selectedIndex]
	q.queueItems = append(q.queueItems[:selectedIndex], q.queueItems[selectedIndex+1:]...)

	// Rewrite queue file (in-place update)
	if err := q.rewriteQueueFile(); err != nil {
		// Add item back if rewrite failed
		q.queueItems = append(q.queueItems[:selectedIndex], append([]*RepairItem{item}, q.queueItems[selectedIndex:]...)...)
		return nil, fmt.Errorf("rewrite queue file: %w", err)
	}

	log.Printf("[RepairQueue] Dequeued item %s (root_cause=%s, workspace=%s)",
		item.ID, item.RootCause, item.Workspace)

	return item, nil
}

// rewriteQueueFile rewrites the entire queue file to disk (for dequeue operations).
func (q *RepairQueue) rewriteQueueFile() error {
	f, err := os.Create(q.queuePath)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, item := range q.queueItems {
		data, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			return err
		}
	}

	return nil
}

// Update attempts and escalation status for an item.
// Used when a repair attempt fails and the item is re-queued.
func (q *RepairQueue) UpdateItem(ctx context.Context, itemID string, attempts int, escalated bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, item := range q.queueItems {
		if item.ID == itemID {
			item.Attempts = attempts
			item.Escalated = escalated

			// Rewrite queue file
			if err := q.rewriteQueueFile(); err != nil {
				return fmt.Errorf("rewrite queue file after update: %w", err)
			}

			log.Printf("[RepairQueue] Updated item %s (attempts=%d, escalated=%v)",
				itemID, attempts, escalated)
			return nil
		}
	}

	return fmt.Errorf("item not found: %s", itemID)
}

// List returns all items in the queue.
func (q *RepairQueue) List(ctx context.Context) ([]*RepairItem, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	// Return a copy to avoid race conditions
	items := make([]*RepairItem, len(q.queueItems))
	copy(items, q.queueItems)
	return items, nil
}

// Stats returns queue statistics.
func (q *RepairQueue) Stats(ctx context.Context) (*QueueStats, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	stats := &QueueStats{
		TotalItems:   len(q.queueItems),
		PendingItems: 0,
		Escalated:    0,
	}

	// Count by root cause
	stats.ByRootCause = make(map[string]int)
	// Count by priority
	stats.ByPriority = make(map[int]int)

	for _, item := range q.queueItems {
		if item.Escalated {
			stats.Escalated++
		} else {
			stats.PendingItems++
		}
		stats.ByRootCause[item.RootCause]++
		stats.ByPriority[item.Priority]++
	}

	return stats, nil
}

// QueueStats holds statistics about the repair queue.
type QueueStats struct {
	TotalItems   int            `json:"total_items"`
	PendingItems int            `json:"pending_items"`
	Escalated    int            `json:"escalated"`
	ByRootCause  map[string]int `json:"by_root_cause"`
	ByPriority   map[int]int    `json:"by_priority"`
}

// RecordResult records a repair result (called by RepairDaemon after attempting a repair).
func (q *RepairQueue) RecordResult(ctx context.Context, result *RepairResult) error {
	// Notify callback
	if q.onItemProcessed != nil {
		go q.onItemProcessed(result.Item, result)
	}

	log.Printf("[RepairQueue] Recorded repair result for item %s (success=%v, strategy=%s, duration=%v)",
		result.ItemID, result.Success, result.Strategy, result.Duration)

	return nil
}
