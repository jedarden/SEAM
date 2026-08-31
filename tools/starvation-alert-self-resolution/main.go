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
	var (
		workspaceRoot      = flag.String("workspace-root", "/home/coding", "Root directory containing all workspaces")
		interval           = flag.Duration("interval", 5*time.Minute, "Check interval (default: 5 minutes)")
		alertLabel         = flag.String("alert-label", "starvation-alert", "Label identifying starvation alert beads")
		enablePluckFallback = flag.Bool("enable-pluck-fallback", true, "Enable PluckFallback for resilient verification")
		diagnosticLog      = flag.String("diagnostic-log", "", "Path to diagnostic log for PluckFallback discrepancies")
		maxConsecutiveChecks = flag.Int("max-consecutive-checks", 3, "Number of consecutive checks before escalation (default: 3)")
		once               = flag.Bool("once", false, "Run once and exit")
		verbose            = flag.Bool("verbose", false, "Enable verbose logging")
	)
	flag.Parse()

	log.SetFlags(0)
	if *verbose {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	}

	log.Printf("Starting starvation alert self-resolution daemon")
	log.Printf("  Workspace root: %s", *workspaceRoot)
	log.Printf("  Check interval: %v", *interval)
	log.Printf("  Alert label: %s", *alertLabel)
	log.Printf("  PluckFallback enabled: %v", *enablePluckFallback)
	log.Printf("  Max consecutive checks: %d", *maxConsecutiveChecks)

	// Configure the daemon
	cfg := server.SelfResolutionConfig{
		WorkspaceRoot:          *workspaceRoot,
		CheckInterval:          *interval,
		AlertLabel:             *alertLabel,
		EnablePluckFallback:    *enablePluckFallback,
		PluckFallbackDiagnosticLog: *diagnosticLog,
		MaxConsecutiveChecks:   *maxConsecutiveChecks,
		OnResolution:           func(resolution *server.AlertResolution) {
			if resolution.Error != "" {
				log.Printf("✗ Error processing alert %s in %s: %s",
					resolution.AlertID, resolution.Workspace, resolution.Error)
				return
			}

			if resolution.Resolved {
				log.Printf("✓ Resolved alert %s in %s (ready=%d, strategy=%s, checks=%d)",
					resolution.AlertID, resolution.Workspace,
					resolution.ReadyCount, resolution.StrategyUsed, resolution.ConsecutiveChecks)
			} else if resolution.Escalated {
				log.Printf("→ Escalated alert %s in %s to bead %s after %d checks",
					resolution.AlertID, resolution.Workspace,
					resolution.EscalationBeadID, resolution.ConsecutiveChecks)
			} else {
				log.Printf("• Monitored alert %s in %s (check #%d/%d, ready=%d, strategy=%s)",
					resolution.AlertID, resolution.Workspace,
					resolution.ConsecutiveChecks, *maxConsecutiveChecks,
					resolution.ReadyCount, resolution.StrategyUsed)
			}
		},
	}

	daemon, err := server.NewStarvationAlertSelfResolution(cfg)
	if err != nil {
		log.Fatalf("Failed to create daemon: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start the daemon in a goroutine
	errCh := make(chan error, 1)
	go func() {
		if err := daemon.Start(ctx); err != nil {
			errCh <- err
		}
	}()

	if *once {
		// One-shot mode: run once and exit
		log.Printf("Running one-time check...")
		time.Sleep(*interval) // Wait for one check cycle
		log.Printf("One-time check complete, exiting")
		daemon.Stop()
		return
	}

	// Wait for shutdown signal or error
	select {
	case <-sigCh:
		log.Printf("Received shutdown signal")
		cancel()
	case err := <-errCh:
		log.Printf("Daemon stopped with error: %v", err)
		cancel()
	}

	// Stop the daemon gracefully
	daemon.Stop()
	log.Printf("Daemon stopped")
}
