package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ardenone/seam/internal/server"
)

func main() {
	cfg := parseFlags()

	// Create repair queue
	repairQueue, err := server.NewRepairQueue(server.RepairQueueConfig{
		QueuePath: cfg.RepairQueuePath,
	})
	if err != nil {
		log.Fatalf("Failed to create repair queue: %v", err)
	}

	// Create diagnostic daemon
	daemon, err := server.NewStarvationDiagnosticDaemon(server.DiagnosticConfig{
		WorkspaceRoot: cfg.WorkspaceRoot,
		LeaseLeader:   nil, // Local mode, no leadership
		CheckInterval: cfg.CheckInterval,
		RepairQueue:   repairQueue,
		OnDiagnosticComplete: func(result *server.DiagnosticResult) {
			log.Printf("[Main] Diagnostic completed: bead=%s, root_cause=%s, repairable=%v, queued=%v",
				result.BeadID, result.RootCause, result.Repairable, result.Queued)
			if result.Error != "" {
				log.Printf("[Main] Diagnostic error: %s", result.Error)
			}
		},
	})
	if err != nil {
		log.Fatalf("Failed to create diagnostic daemon: %v", err)
	}

	// Start daemon
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run daemon in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- daemon.Start(ctx)
	}()

	log.Printf("[Main] Starvation diagnostic daemon started")
	log.Printf("[Main] Workspace root: %s", cfg.WorkspaceRoot)
	log.Printf("[Main] Check interval: %v", cfg.CheckInterval)
	log.Printf("[Main] Repair queue: %s", cfg.RepairQueuePath)

	// Wait for interrupt signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
		log.Println("[Main] Received shutdown signal")
	case err := <-errCh:
		log.Printf("[Main] Daemon stopped with error: %v", err)
		cancel()
	}

	// Stop daemon
	daemon.Stop()
	log.Println("[Main] Daemon stopped gracefully")
}

// Config holds the tool configuration.
type Config struct {
	WorkspaceRoot    string
	CheckInterval    time.Duration
	RepairQueuePath  string
}

// parseFlags parses command-line flags.
func parseFlags() Config {
	var (
		workspaceRoot   = flag.String("workspace-root", "/home/coding", "Root directory containing all workspaces")
		checkInterval   = flag.Duration("check-interval", 2*time.Minute, "How often to scan for starvation-alert beads")
		repairQueuePath = flag.String("repair-queue", "/home/coding/SEAM/.beads/diagnostics/repair-queue.jsonl", "Path to repair queue file")
	)

	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	return Config{
		WorkspaceRoot:   *workspaceRoot,
		CheckInterval:   *checkInterval,
		RepairQueuePath: *repairQueuePath,
	}
}
