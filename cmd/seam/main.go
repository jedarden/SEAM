package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ardenone/seam/internal/server"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: seam <command> [<args>]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Available commands:")
		fmt.Fprintln(os.Stderr, "  serve    Start the SEAM gateway server")
		fmt.Fprintln(os.Stderr, "  lint     Validate SEAM route fragments")
		fmt.Fprintln(os.Stderr, "  diff     Show differences between fragment versions")
		fmt.Fprintln(os.Stderr, "  import   Import fragments into SEAM")
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "serve":
		serveCommand(args)
	case "lint":
		lintCommand(args)
	case "diff":
		diffCommand(args)
	case "import":
		importCommand(args)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		fmt.Fprintln(os.Stderr, "Available commands: serve, lint, diff, import")
		os.Exit(1)
	}
}

func serveCommand(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)

	callerPort := fs.Int("caller-port", 8080, "Port for the caller-facing listener")
	operatorPort := fs.Int("operator-port", 8081, "Port for the operator-only listener")
	baseURL := fs.String("base-url", "http://localhost:8080", "Base URL for the caller-facing interface")
	specDir := fs.String("spec-dir", "./spec", "Directory containing local OpenAPI spec files")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	// Override with environment variables if set
	if val := os.Getenv("SEAM_CALLER_PORT"); val != "" {
		fmt.Sscanf(val, "%d", callerPort)
	}
	if val := os.Getenv("SEAM_OPERATOR_PORT"); val != "" {
		fmt.Sscanf(val, "%d", operatorPort)
	}
	if val := os.Getenv("SEAM_BASE_URL"); val != "" {
		*baseURL = val
	}
	if val := os.Getenv("SEAM_SPEC_DIR"); val != "" {
		*specDir = val
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("Starting SEAM gateway server:")
	log.Printf("  Caller-facing port: %d", *callerPort)
	log.Printf("  Operator-only port: %d", *operatorPort)
	log.Printf("  Base URL: %s", *baseURL)
	log.Printf("  Spec directory: %s", *specDir)

	cfg := &server.Config{
		CallerPort:   *callerPort,
		OperatorPort: *operatorPort,
		BaseURL:      *baseURL,
		SpecDir:      *specDir,
	}

	srv := server.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	// Wait for interrupt signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Received shutdown signal")
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Error during shutdown: %v", err)
	}

	log.Println("Server stopped")
}

func lintCommand(args []string) {
	fmt.Println("lint command: not yet implemented")
	os.Exit(1)
}

func diffCommand(args []string) {
	fmt.Println("diff command: not yet implemented")
	os.Exit(1)
}

func importCommand(args []string) {
	fmt.Println("import command: not yet implemented")
	os.Exit(1)
}
