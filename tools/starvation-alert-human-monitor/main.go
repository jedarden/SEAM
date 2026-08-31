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
		workspaceRoot       = flag.String("workspace-root", "/home/coding", "Root directory containing all workspaces")
		interval            = flag.Duration("interval", 5*time.Minute, "Check interval (default: 5 minutes)")
		minReevaluationAge = flag.Duration("min-age", 15*time.Minute, "Minimum age before re-evaluation (default: 15 minutes)")
		alertLabels        = flag.String("alert-labels", "human,alert:starvation:unknown", "Comma-separated list of labels to monitor")
		reevaluationLog    = flag.String("reevaluation-log", "", "Path to re-evaluation log file (default: .beads/diagnostics/human-alert-reevaluation.log)")
		leaseNamespace     = flag.String("lease-namespace", "seam-monitoring", "Kubernetes Lease namespace for leader election")
		leaseName          = flag.String("lease-name", "starvation-alert-human-monitor", "Kubernetes Lease name for leader election")
		leaseIdentity      = flag.String("lease-identity", "", "Kubernetes Lease identity (defaults to hostname)")
		enableLease        = flag.Bool("enable-lease", false, "Enable Kubernetes Lease leader election")
		once               = flag.Bool("once", false, "Run once and exit")
		verbose            = flag.Bool("verbose", false, "Enable verbose logging")
	)

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [OPTIONS]\n\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Starvation Alert Human Monitor")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "This daemon monitors starvation-alert beads marked with 'human' or")
		fmt.Fprintln(flag.CommandLine.Output(), "'alert:starvation:unknown' labels and automatically re-evaluates them.")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "When a re-evaluation finds that work is available (using bead pluck),")
		fmt.Fprintln(flag.CommandLine.Output(), "it closes the alert as 'transient-starvation' and removes the human label.")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "This catches false positives where the condition resolved before human")
		fmt.Fprintln(flag.CommandLine.Output(), "review but the alert was never updated.")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Re-evaluation triggers:")
		fmt.Fprintln(flag.CommandLine.Output(), "  1. Backoff window expires (5-15 minutes after alert creation)")
		fmt.Fprintln(flag.CommandLine.Output(), "  2. Any automated repair is attempted")
		fmt.Fprintln(flag.CommandLine.Output(), "  3. Configurable interval (default: 5 minutes)")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Options:")
		flag.PrintDefaults()
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Examples:")
		fmt.Fprintf(flag.CommandLine.Output(), "  %s --workspace-root /home/coding --interval 5m\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s --enable-lease --lease-namespace seam-monitoring\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s --once --verbose\n", os.Args[0])
	}

	flag.Parse()

	if *verbose {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	}

	log.Printf("Starting starvation alert human monitor")
	log.Printf("  Workspace root: %s", *workspaceRoot)
	log.Printf("  Check interval: %v", *interval)
	log.Printf("  Min re-evaluation age: %v", *minReevaluationAge)
	log.Printf("  Alert labels: %s", *alertLabels)
	log.Printf("  Leader election: %v", *enableLease)

	// Parse alert labels
	labels := splitCommaSeparated(*alertLabels)
	if len(labels) == 0 {
		labels = []string{"human", "alert:starvation:unknown"}
	}

	// Build configuration
	cfg := server.HumanMonitorConfig{
		WorkspaceRoot:       *workspaceRoot,
		CheckInterval:       *interval,
		MinReevaluationAge: *minReevaluationAge,
		AlertLabels:         labels,
		ReevaluationLogPath: *reevaluationLog,
	}

	// Set up leader election if enabled
	var leaseLeader *server.LeaseLeader
	if *enableLease {
		if *leaseIdentity == "" {
			hostname, err := os.Hostname()
			if err != nil {
				log.Fatalf("Failed to get hostname: %v", err)
			}
			*leaseIdentity = hostname
		}

		leaseLeader = server.NewLeaseLeader(*leaseNamespace, *leaseName, *leaseIdentity, 30*time.Second)
		log.Printf("Leader election enabled: namespace=%s, name=%s, identity=%s",
			*leaseNamespace, *leaseName, *leaseIdentity)
		cfg.LeaseLeader = leaseLeader
	}

	// Set up callback for re-evaluation results
	cfg.OnReevaluation = func(result *server.ReevaluationResult) {
		if result.Error != "" {
			log.Printf("✗ Error re-evaluating alert %s in %s: %s",
				result.AlertID, result.Workspace, result.Error)
			return
		}

		if result.Resolved {
			log.Printf("✓ Resolved human-marked alert %s in %s (ready=%d, strategy=%s, human_label_removed=%v, checks=%d, age=%.1f hours, trigger=%s)",
				result.AlertID, result.Workspace,
				result.ReadyCount, result.StrategyUsed, result.HumanLabelRemoved,
				result.ReevaluationCount, result.AlertAgeHours, result.Trigger)
		} else {
			log.Printf("• Monitored human-marked alert %s in %s (check #%d, age=%.1f hours, ready=%d, strategy=%s)",
				result.AlertID, result.Workspace,
				result.ReevaluationCount, result.AlertAgeHours,
				result.ReadyCount, result.StrategyUsed)
		}
	}

	// Create daemon
	daemon, err := server.NewStarvationAlertHumanMonitor(cfg)
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
		// One-shot mode: run one check cycle and exit
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

// splitCommaSeparated splits a comma-separated string into a slice.
func splitCommaSeparated(s string) []string {
	if s == "" {
		return []string{}
	}

	var result []string
	for _, part := range splitString(s, ",") {
		trimmed := trimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// splitString splits a string by a separator.
func splitString(s, sep string) []string {
	var result []string
	current := ""

	for _, ch := range s {
		if string(ch) == sep {
			result = append(result, current)
			current = ""
		} else {
			current += string(ch)
		}
	}
	result = append(result, current)
	return result
}

// trimSpace removes leading and trailing whitespace.
func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
