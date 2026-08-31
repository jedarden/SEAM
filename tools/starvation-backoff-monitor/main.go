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
		workspaceRoot   = flag.String("workspace-root", "/home/coding", "Root directory containing all workspaces")
		interval        = flag.Duration("interval", 30*time.Second, "Check interval (default: 30 seconds)")
		leaseNamespace = flag.String("lease-namespace", "seam-monitoring", "Kubernetes Lease namespace for leader election")
		leaseName      = flag.String("lease-name", "starvation-backoff-monitor", "Kubernetes Lease name for leader election")
		leaseIdentity = flag.String("lease-identity", "", "Kubernetes Lease identity (defaults to hostname)")
		enableLease    = flag.Bool("enable-lease", false, "Enable Kubernetes Lease leader election")
		verbose        = flag.Bool("verbose", false, "Enable verbose logging")
	)

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [OPTIONS]\n\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Transient Starvation Backoff Monitor")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "This daemon monitors workspaces for starvation conditions (ready=0 with open beads)")
		fmt.Fprintln(flag.CommandLine.Output(), "and implements exponential backoff before creating alert beads.")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Backoff sequence: 30s, 2m, 5m, 15m")
		fmt.Fprintln(flag.CommandLine.Output(), "Only creates alert beads if starvation persists through ALL intervals.")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "This reduces false-positive alert noise by giving transient issues time to resolve.")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Options:")
		flag.PrintDefaults()
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Examples:")
		fmt.Fprintf(flag.CommandLine.Output(), "  %s --workspace-root /home/coding --interval 30s\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s --enable-lease --lease-namespace seam-monitoring\n", os.Args[0])
	}

	flag.Parse()

	if *verbose {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	}

	log.Printf("Starting transient starvation backoff monitor")
	log.Printf("Workspace root: %s", *workspaceRoot)
	log.Printf("Check interval: %v", *interval)
	log.Printf("Leader election: %v", *enableLease)

	// Build configuration
	cfg := server.BackoffConfig{
		WorkspaceRoot: *workspaceRoot,
		CheckInterval: *interval,
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

	// Set up callback for when alerts are created
	cfg.OnCreateAlert = func(workspace string, state *server.BackoffState) {
		if state.Escalated {
			log.Printf("[BackoffMonitor] ✓ Alert bead created for %s: %s", workspace, state.AlertBeadID)
		}
	}

	// Create daemon
	daemon, err := server.NewTransientStarvationBackoff(cfg)
	if err != nil {
		log.Fatalf("Failed to create daemon: %v", err)
	}

	// Set up signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start shutdown goroutine
	go func() {
		sig := <-sigCh
		log.Printf("Received signal: %v", sig)
		log.Println("Shutting down daemon...")
		cancel()
		daemon.Stop()
	}()

	// Start the daemon
	log.Println("Starting daemon...")
	if err := daemon.Start(ctx); err != nil {
		if err == context.Canceled {
			log.Println("Daemon stopped by signal")
		} else {
			log.Fatalf("Daemon failed: %v", err)
		}
	}

	log.Println("Daemon stopped")
}
