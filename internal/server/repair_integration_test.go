package server

import (
	"context"
	"testing"
	"time"
)

// TestRepairQueueCreation tests that the repair queue can be created successfully.
func TestRepairQueueCreation(t *testing.T) {
	tmpDir := t.TempDir()
	queuePath := tmpDir + "/repair-queue.jsonl"

	queue, err := NewRepairQueue(RepairQueueConfig{
		QueuePath:          queuePath,
		MaxQueueSize:       100,
		MaxAttemptsPerItem: 3,
	})
	if err != nil {
		t.Fatalf("Failed to create repair queue: %v", err)
	}

	if queue == nil {
		t.Fatal("Queue is nil")
	}

	stats, err := queue.Stats(context.Background())
	if err != nil {
		t.Fatalf("Failed to get queue stats: %v", err)
	}

	if stats.TotalItems != 0 {
		t.Errorf("Expected empty queue, got %d items", stats.TotalItems)
	}
}

// TestRepairQueueEnqueueDequeue tests enqueue and dequeue operations.
func TestRepairQueueEnqueueDequeue(t *testing.T) {
	tmpDir := t.TempDir()
	queuePath := tmpDir + "/repair-queue.jsonl"

	queue, err := NewRepairQueue(RepairQueueConfig{
		QueuePath:          queuePath,
		MaxQueueSize:       100,
		MaxAttemptsPerItem: 3,
	})
	if err != nil {
		t.Fatalf("Failed to create repair queue: %v", err)
	}

	// Create a test item
	item := &RepairItem{
		Workspace:   "/test/workspace",
		AlertID:     "test-alert-1",
		RootCause:   "database-corrupt",
		Priority:    2,
		MaxAttempts: 3,
		Timestamp:   time.Now(),
	}

	// Enqueue the item
	enqueued, existingItem, err := queue.Enqueue(context.Background(), item)
	if err != nil {
		t.Fatalf("Failed to enqueue item: %v", err)
	}

	if !enqueued {
		t.Fatal("Item was not enqueued")
	}

	if existingItem != nil {
		t.Fatal("Existing item should be nil on first enqueue")
	}

	// Verify stats
	stats, err := queue.Stats(context.Background())
	if err != nil {
		t.Fatalf("Failed to get stats: %v", err)
	}

	if stats.TotalItems != 1 {
		t.Errorf("Expected 1 item, got %d", stats.TotalItems)
	}

	// Dequeue the item
	dequeuedItem, err := queue.Dequeue(context.Background())
	if err != nil {
		t.Fatalf("Failed to dequeue item: %v", err)
	}

	if dequeuedItem == nil {
		t.Fatal("Dequeued item is nil")
	}

	if dequeuedItem.RootCause != "database-corrupt" {
		t.Errorf("Expected root cause 'database-corrupt', got '%s'", dequeuedItem.RootCause)
	}

	// Verify queue is now empty
	stats, err = queue.Stats(context.Background())
	if err != nil {
		t.Fatalf("Failed to get stats after dequeue: %v", err)
	}

	if stats.TotalItems != 0 {
		t.Errorf("Expected empty queue after dequeue, got %d items", stats.TotalItems)
	}
}

// TestRepairQueueIdempotentEnqueue tests that duplicate items are not enqueued.
func TestRepairQueueIdempotentEnqueue(t *testing.T) {
	tmpDir := t.TempDir()
	queuePath := tmpDir + "/repair-queue.jsonl"

	queue, err := NewRepairQueue(RepairQueueConfig{
		QueuePath:          queuePath,
		MaxQueueSize:       100,
		MaxAttemptsPerItem: 3,
	})
	if err != nil {
		t.Fatalf("Failed to create repair queue: %v", err)
	}

	item := &RepairItem{
		Workspace:   "/test/workspace",
		AlertID:     "test-alert-unique",
		RootCause:   "index-corrupt",
		Priority:    2,
		MaxAttempts: 3,
		Timestamp:   time.Now(),
	}

	// Enqueue twice
	enqueued1, existing1, err1 := queue.Enqueue(context.Background(), item)
	if err1 != nil {
		t.Fatalf("First enqueue failed: %v", err1)
	}

	if !enqueued1 {
		t.Fatal("First enqueue should succeed")
	}

	enqueued2, existing2, err2 := queue.Enqueue(context.Background(), item)
	if err2 != nil {
		t.Fatalf("Second enqueue failed: %v", err2)
	}

	if enqueued2 {
		t.Fatal("Second enqueue should fail (duplicate)")
	}

	if existing2 == nil {
		t.Fatal("Second enqueue should return existing item")
	}

	// Verify only one item exists
	stats, err := queue.Stats(context.Background())
	if err != nil {
		t.Fatalf("Failed to get stats: %v", err)
	}

	if stats.TotalItems != 1 {
		t.Errorf("Expected 1 item after duplicate enqueue, got %d", stats.TotalItems)
	}
}

// TestRepairQueueByRootCause tests statistics grouping by root cause.
func TestRepairQueueByRootCause(t *testing.T) {
	tmpDir := t.TempDir()
	queuePath := tmpDir + "/repair-queue.jsonl"

	queue, err := NewRepairQueue(RepairQueueConfig{
		QueuePath:          queuePath,
		MaxQueueSize:       100,
		MaxAttemptsPerItem: 3,
	})
	if err != nil {
		t.Fatalf("Failed to create repair queue: %v", err)
	}

	// Enqueue items with different root causes
	items := []*RepairItem{
		{Workspace: "/test/ws1", AlertID: "alert1", RootCause: "database-corrupt", Priority: 2, MaxAttempts: 3, Timestamp: time.Now()},
		{Workspace: "/test/ws2", AlertID: "alert2", RootCause: "database-corrupt", Priority: 2, MaxAttempts: 3, Timestamp: time.Now()},
		{Workspace: "/test/ws3", AlertID: "alert3", RootCause: "index-corrupt", Priority: 2, MaxAttempts: 3, Timestamp: time.Now()},
		{Workspace: "/test/ws4", AlertID: "alert4", RootCause: "checkpoint-out-of-sync", Priority: 2, MaxAttempts: 3, Timestamp: time.Now()},
	}

	for _, item := range items {
		if _, _, err := queue.Enqueue(context.Background(), item); err != nil {
			t.Fatalf("Failed to enqueue item: %v", err)
		}
	}

	stats, err := queue.Stats(context.Background())
	if err != nil {
		t.Fatalf("Failed to get stats: %v", err)
	}

	if stats.TotalItems != 4 {
		t.Errorf("Expected 4 items, got %d", stats.TotalItems)
	}

	if stats.ByRootCause["database-corrupt"] != 2 {
		t.Errorf("Expected 2 database-corrupt items, got %d", stats.ByRootCause["database-corrupt"])
	}

	if stats.ByRootCause["index-corrupt"] != 1 {
		t.Errorf("Expected 1 index-corrupt item, got %d", stats.ByRootCause["index-corrupt"])
	}

	if stats.ByRootCause["checkpoint-out-of-sync"] != 1 {
		t.Errorf("Expected 1 checkpoint-out-of-sync item, got %d", stats.ByRootCause["checkpoint-out-of-sync"])
	}
}
