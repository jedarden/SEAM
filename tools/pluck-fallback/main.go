package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"

	"github.com/ardenone/seam/internal/pluckfallback"
)

func main() {
	var (
		workspace      = flag.String("workspace", ".", "Path to workspace directory")
		verbose        = flag.Bool("verbose", false, "Enable verbose logging")
		jsonOutput     = flag.Bool("json", false, "Output results in JSON format")
		diagnosticLog  = flag.String("diagnostic-log", "", "Path to diagnostic log file")
		count          = flag.Int("count", 1, "Number of beads to return")
		createBead     = flag.Bool("create-diagnostic-bead", false, "Create a diagnostic bead when fallback is triggered")
	)
	flag.Parse()

	if *verbose {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	}

	pf, err := pluckfallback.NewPluckFallback(*verbose, *diagnosticLog)
	if err != nil {
		log.Fatalf("Failed to create pluck fallback: %v", err)
	}
	defer pf.Close()

	ctx := context.Background()

	candidates, strategy, discrepancies, err := pf.Pluck(ctx, *workspace)
	if err != nil {
		log.Fatalf("All query strategies failed: %v", err)
	}

	// Log discrepancies
	for _, d := range discrepancies {
		log.Printf("DISCREPANCY: %s", d)
	}

	// Limit results
	if len(candidates) > *count && *count > 0 {
		candidates = candidates[:*count]
	}

	if *jsonOutput {
		output := struct {
			Candidates     []pluckfallback.PluckResult `json:"candidates"`
			StrategyUsed   string                        `json:"strategy_used"`
			Discrepancies  []string                      `json:"discrepancies,omitempty"`
			TotalAvailable int                           `json:"total_available"`
		}{
			Candidates:     candidates,
			StrategyUsed:   strategy,
			Discrepancies:  discrepancies,
			TotalAvailable: len(candidates),
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(output)
	} else {
		fmt.Printf("Strategy used: %s\n", strategy)
		fmt.Printf("Candidates returned: %d (total available: %d)\n", len(candidates), len(candidates))
		for i, c := range candidates {
			fmt.Printf("%d. %s [%s] - %s (priority %d)\n", i+1, c.ID, c.Status, c.Title, c.Priority)
		}
		if len(discrepancies) > 0 {
			fmt.Printf("\nVisibility discrepancies detected:\n")
			for _, d := range discrepancies {
				fmt.Printf("  - %s\n", d)
			}
		}
	}

	// Create diagnostic bead if requested and discrepancies found
	if *createBead && len(discrepancies) > 0 {
		if err := pluckfallback.CreateDiagnosticBead(ctx, *workspace, strategy, discrepancies, candidates); err != nil {
			log.Printf("Failed to create diagnostic bead: %v", err)
		}
	}

	// Exit code: 0 if found candidates, 1 if primary strategy used (no fallback), 2 if fallback triggered
	if strategy != "primary" && len(candidates) > 0 {
		os.Exit(2) // Fallback was triggered
	} else if len(candidates) == 0 {
		os.Exit(3) // No candidates found
	}
	os.Exit(0)
}
