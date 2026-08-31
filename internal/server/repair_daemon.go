package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RepairDaemon processes auto-repairable items from the repair queue.
// It executes appropriate repair commands based on the root cause category
// and tracks repair attempts. Items that exceed max attempts are escalated
// to human review.
type RepairDaemon struct {
	mu               sync.RWMutex
	queue            *RepairQueue
	leaseLeader      *LeaseLeader
	stopCh           chan struct{}
	stopped          bool
	checkInterval    time.Duration
	maxAttempts      int
	onRepairComplete func(result *RepairResult)
	metrics          *Metrics
}

// RepairDaemonConfig holds configuration for the repair daemon.
type RepairDaemonConfig struct {
	// Queue is the repair queue to process
	Queue *RepairQueue

	// LeaseLeader is the Kubernetes Lease leader elector (optional)
	LeaseLeader *LeaseLeader

	// CheckInterval is how often to check for queue items (default: 1 minute)
	CheckInterval time.Duration

	// MaxAttempts is the maximum repair attempts per item (default: 3)
	MaxAttempts int

	// OnRepairComplete is called when a repair attempt completes
	OnRepairComplete func(result *RepairResult)

	// Metrics is the Prometheus metrics publisher
	Metrics *Metrics
}

// NewRepairDaemon creates a new repair daemon.
func NewRepairDaemon(cfg RepairDaemonConfig) (*RepairDaemon, error) {
	if cfg.Queue == nil {
		return nil, fmt.Errorf("repair queue is required")
	}
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 1 * time.Minute
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 3
	}

	daemon := &RepairDaemon{
		queue:            cfg.Queue,
		leaseLeader:      cfg.LeaseLeader,
		checkInterval:    cfg.CheckInterval,
		maxAttempts:      cfg.MaxAttempts,
		stopCh:          make(chan struct{}),
		onRepairComplete: cfg.OnRepairComplete,
		metrics:         cfg.Metrics,
	}

	return daemon, nil
}

// Start begins the repair daemon loop.
func (d *RepairDaemon) Start(ctx context.Context) error {
	if d == nil {
		return fmt.Errorf("daemon is nil")
	}

	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return fmt.Errorf("daemon already stopped")
	}
	d.mu.Unlock()

	// Acquire leadership if configured
	if d.leaseLeader != nil {
		log.Printf("[RepairDaemon] Attempting to acquire leadership")
		if !d.leaseLeader.Acquire(ctx) {
			return fmt.Errorf("failed to acquire leadership")
		}
		log.Printf("[RepairDaemon] Leadership acquired")

		// Start the lease renewal goroutine
		renewCtx, cancelRenew := context.WithCancel(ctx)
		defer cancelRenew()

		go func() {
			d.leaseLeader.Renew(renewCtx)
			log.Printf("[RepairDaemon] Leadership lost, stopping daemon")
			d.Stop()
		}()
	} else {
		log.Printf("[RepairDaemon] Running without leadership (local mode)")
	}

	// Main daemon loop
	ticker := time.NewTicker(d.checkInterval)
	defer ticker.Stop()

	log.Printf("[RepairDaemon] Starting repair daemon (check interval: %v)", d.checkInterval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[RepairDaemon] Context cancelled, stopping daemon")
			return ctx.Err()
		case <-d.stopCh:
			log.Printf("[RepairDaemon] Stop signal received, stopping daemon")
			return nil
		case <-ticker.C:
			if d.leaseLeader != nil && !d.leaseLeader.IsLeader() {
				log.Printf("[RepairDaemon] No longer leader, skipping check")
				continue
			}

			// Process next item from queue
			d.processNextItem(ctx)
		}
	}
}

// processNextItem dequeues and processes the next repair item.
func (d *RepairDaemon) processNextItem(ctx context.Context) {
	// Dequeue next item
	item, err := d.queue.Dequeue(ctx)
	if err != nil {
		log.Printf("[RepairDaemon] Failed to dequeue item: %v", err)
		return
	}

	if item == nil {
		// Queue is empty
		return
	}

	log.Printf("[RepairDaemon] Processing repair item %s (root_cause=%s, workspace=%s, attempt=%d/%d)",
		item.ID, item.RootCause, item.Workspace, item.Attempts+1, item.MaxAttempts)

	// Execute repair based on root cause
	result := d.executeRepair(ctx, item)

	// Record result in queue
	if err := d.queue.RecordResult(ctx, result); err != nil {
		log.Printf("[RepairDaemon] Failed to record repair result: %v", err)
	}

	// Check if repair succeeded or should be escalated/retried
	d.handleRepairResult(ctx, item, result)

	// Notify callback
	if d.onRepairComplete != nil {
		d.onRepairComplete(result)
	}
}

// executeRepair executes the appropriate repair command based on root cause.
func (d *RepairDaemon) executeRepair(ctx context.Context, item *RepairItem) *RepairResult {
	startTime := time.Now()

	result := &RepairResult{
		ItemID:    item.ID,
		Timestamp: startTime,
		Attempts:  item.Attempts + 1,
	}

	strategy, cmd, args := d.getRepairStrategy(item.RootCause)
	result.Strategy = strategy
	result.Command = fmt.Sprintf("%s %s", cmd, strings.Join(args, " "))

	// Execute repair command
	repairCmd := exec.CommandContext(ctx, cmd, args...)
	repairCmd.Dir = item.Workspace
	output, err := repairCmd.CombinedOutput()
	result.Output = string(output)
	result.Duration = time.Since(startTime)

	if err != nil {
		result.Error = err.Error()
		result.Success = false
		log.Printf("[RepairDaemon] ✗ Repair failed for item %s: %v (output: %s)",
			item.ID, err, truncateOutput(string(output), 500))

		if d.metrics != nil {
			d.metrics.RecordRepairResult(item.Workspace, item.RootCause, false)
		}
	} else {
		result.Success = true
		log.Printf("[RepairDaemon] ✓ Repair succeeded for item %s (duration=%v)",
			item.ID, result.Duration)

		if d.metrics != nil {
			d.metrics.RecordRepairResult(item.Workspace, item.RootCause, true)
		}
	}

	return result
}

// getRepairStrategy returns the repair strategy (command + args) for a given root cause.
func (d *RepairDaemon) getRepairStrategy(rootCause string) (strategy string, cmd string, args []string) {
	switch rootCause {
	case "index-corrupt":
		// Rebuild database from checkpoint: bead init + sync import-only
		return "checkpoint-rebuild", "bead", []string{"init"}

	case "database-corrupt":
		// Run bead doctor --repair
		return "doctor-repair", "bead", []string{"doctor", "--repair"}

	case "checkpoint-out-of-sync":
		// Flush checkpoint to sync state
		return "checkpoint-flush", "bead", []string{"sync", "flush-only"}

	case "filter-mismatch":
		// Release affected beads (requires identifying them first)
		return "bead-release", "bead", []string{"list", "--status", "open", "--json"}

	case "stale-assignment":
		// Release stale assignments
		return "bead-release", "bead", []string{"list", "--status", "open", "--json"}

	default:
		// Unknown root cause - use general repair
		return "general-repair", "bead", []string{"doctor", "--repair"}
	}
}

// handleRepairResult determines if repair succeeded, needs retry, or escalation.
func (d *RepairDaemon) handleRepairResult(ctx context.Context, item *RepairItem, result *RepairResult) {
	workspaceName := filepath.Base(item.Workspace)

	if result.Success {
		// Verify repair by checking if candidates are now available
		readyCount, err := d.verifyRepair(ctx, item.Workspace)
		result.ReadyCount = readyCount

		if readyCount > 0 {
			log.Printf("[RepairDaemon] ✓ Repair verified for %s: %d candidates now available in %s",
				item.ID, readyCount, workspaceName)
			return // Item is repaired, done
		}

		if err != nil {
			log.Printf("[RepairDaemon] Warning: Could not verify repair for %s: %v", item.ID, err)
		}

		log.Printf("[RepairDaemon] Repair command succeeded but no candidates available in %s (may need additional steps)", workspaceName)
		// Continue to retry logic
	}

	// Repair failed or verification failed - check if we should retry or escalate
	if result.Attempts >= item.MaxAttempts {
		// Escalate to human review
		d.escalateToHumanReview(ctx, item, result)
		result.Escalated = true
	} else {
		// Re-queue for retry
		item.Attempts = result.Attempts
		if err := d.queue.UpdateItem(ctx, item.ID, item.Attempts, false); err != nil {
			log.Printf("[RepairDaemon] Failed to re-queue item %s: %v", item.ID, err)
		}
		log.Printf("[RepairDaemon] Re-queued item %s for retry (attempt %d/%d)",
			item.ID, result.Attempts, item.MaxAttempts)
	}
}

// verifyRepair checks if the repair was successful by counting ready candidates.
func (d *RepairDaemon) verifyRepair(ctx context.Context, workspacePath string) (int, error) {
	cmd := exec.CommandContext(ctx, "bead", "list", "--ready", "--json")
	cmd.Dir = workspacePath

	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("bead list --ready: %w", err)
	}

	// Count beads in JSON output
	count := strings.Count(string(output), `"id":`)
	return count, nil
}

// escalateToHumanReview creates a human-review bead when repair fails after max attempts.
func (d *RepairDaemon) escalateToHumanReview(ctx context.Context, item *RepairItem, result *RepairResult) {
	log.Printf("[RepairDaemon] Escalating item %s to human review after %d failed attempts", item.ID, result.Attempts)

	// Mark item as escalated in queue
	if err := d.queue.UpdateItem(ctx, item.ID, item.Attempts, true); err != nil {
		log.Printf("[RepairDaemon] Failed to mark item as escalated: %v", err)
	}

	// Create human-review bead
	escalationTitle := fmt.Sprintf("Auto-repair failed - %s: %s", filepath.Base(item.Workspace), item.RootCause)
	escalationDesc := fmt.Sprintf(
		"Auto-repair for starvation alert %s failed after %d repair attempts.\n\n"+
			"**Item Details:**\n"+
			"- Item ID: %s\n"+
			"- Workspace: %s\n"+
			"- Root Cause: %s\n"+
			"- Original Alert: %s\n"+
			"- Queued: %s\n"+
			"- Priority: %d\n\n"+
			"**Repair Attempts:**\n"+
			"- Total attempts: %d\n"+
			"- Last strategy: %s\n"+
			"- Last command: %s\n"+
			"- Last error: %s\n\n"+
			"**Action Required:**\n"+
			"Manual investigation and repair needed:\n"+
			"1. Review the repair command output above\n"+
			"2. Check workspace database and checkpoint integrity\n"+
			"3. Investigate underlying infrastructure issues\n"+
			"4. Apply appropriate manual repair based on root cause\n\n"+
			"This escalation bead was automatically created by the repair daemon after exhausting automated repair attempts.",
		item.AlertID, result.Attempts, item.ID, item.Workspace, item.RootCause, item.AlertID,
		item.Timestamp.Format(time.RFC3339), item.Priority, result.Attempts, result.Strategy,
		result.Command, result.Error,
	)

	cmd := exec.CommandContext(ctx, "bead", "create",
		"--title", escalationTitle,
		"--priority", fmt.Sprintf("%d", item.Priority),
		"--issue-type", "task",
		"--label", "human-review-required",
		"--label", "repair-failed",
		"--label", fmt.Sprintf("repair:%s", item.RootCause),
	)
	cmd.Dir = item.Workspace
	cmd.Stdin = strings.NewReader(escalationDesc)

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[RepairDaemon] Failed to create escalation bead for item %s: %v", item.ID, err)
		return
	}

	// Extract bead ID from output
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	escalationBeadID := ""
	if len(lines) > 0 {
		escalationBeadID = strings.TrimSpace(lines[len(lines)-1])
	}

	log.Printf("[RepairDaemon] → Escalated item %s to bead %s after %d failed attempts",
		item.ID, escalationBeadID, result.Attempts)

	if d.metrics != nil {
		d.metrics.RecordRepairEscalation(item.Workspace, item.RootCause)
	}
}

// Stop stops the repair daemon.
func (d *RepairDaemon) Stop() {
	if d == nil {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.stopped {
		return
	}

	d.stopped = true
	close(d.stopCh)

	// Release lease leadership if configured
	if d.leaseLeader != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.leaseLeader.Release(ctx)
	}

	log.Printf("[RepairDaemon] Repair daemon stopped")
}

// IsRunning reports whether the daemon is currently running.
func (d *RepairDaemon) IsRunning() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return !d.stopped && (d.leaseLeader == nil || d.leaseLeader.IsLeader())
}

// truncateOutput truncates output to a maximum length.
func truncateOutput(output string, maxLen int) string {
	if len(output) <= maxLen {
		return output
	}
	return output[:maxLen] + "... (truncated)"
}
