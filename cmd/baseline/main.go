package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ardenone/seam/benchmarks/baseline"
)

var (
	benchmarkType   = flag.String("type", "", "Benchmark type: openbao, memory, throughput")
	saveBaseline    = flag.Bool("save-baseline", false, "Save current run as baseline")
	checkRegression = flag.Bool("check-regression", false, "Check for regressions against baseline")
	threshold       = flag.Float64("threshold", baseline.DefaultThreshold, "Regression threshold percentage")
	verbose         = flag.Bool("v", false, "Verbose output")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: baseline [options]\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Benchmark baseline management tool for SEAM\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\nExamples:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  Save baseline:  go test -bench=. ./benches/... | baseline -type=openbao -save-baseline\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  Check regression: go test -bench=. ./benches/... | baseline -type=memory -check-regression\n")
		fmt.Fprintf(flag.CommandLine.Output(), "\nBenchmark types:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  openbao   - OpenBao latency benchmarks\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  memory    - Memory and concurrency benchmarks\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  throughput - Connection scaling and throughput benchmarks\n")
	}

	flag.Parse()

	if err := baseline.ValidateBenchmarkType(*benchmarkType); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		flag.Usage()
		os.Exit(1)
	}

	// Read benchmark output from stdin
	input, err := readInput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading benchmark output: %v\n", err)
		os.Exit(1)
	}

	// Parse benchmark output
	metrics := baseline.ParseBenchmarkOutput(input)
	if len(metrics) == 0 {
		fmt.Fprintln(os.Stderr, "Error: No benchmark metrics found in input")
		os.Exit(1)
	}

	if *verbose {
		fmt.Printf("Parsed %d benchmark metrics\n", len(metrics))
		for name := range metrics {
			fmt.Printf("  - %s\n", name)
		}
	}

	switch {
	case *saveBaseline:
		if err := saveBaselineFile(*benchmarkType, metrics); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving baseline: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Baseline saved for '%s' benchmarks\n", *benchmarkType)
		printBaselineInfo(*benchmarkType)

	case *checkRegression:
		if err := checkRegressions(*benchmarkType, metrics, *threshold); err != nil {
			fmt.Fprintf(os.Stderr, "Error checking regressions: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintln(os.Stderr, "Error: Must specify -save-baseline or -check-regression")
		flag.Usage()
		os.Exit(1)
	}
}

// readInput reads benchmark output from stdin or from a pipe
func readInput() (string, error) {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		// stdin is a terminal (interactive), not a pipe
		return "", fmt.Errorf("no input provided (pipe benchmark output to this command)")
	}

	// Read all input from stdin
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// saveBaselineFile saves the current metrics as a baseline
func saveBaselineFile(benchType string, metrics map[string]baseline.BenchmarkMetric) error {
	if err := baseline.SaveBaseline(benchType, metrics); err != nil {
		return err
	}
	return nil
}

// checkRegressions checks for performance regressions against the baseline
func checkRegressions(benchType string, metrics map[string]baseline.BenchmarkMetric, threshold float64) error {
	report, err := baseline.CompareWithBaseline(benchType, metrics, threshold)
	if err != nil {
		return err
	}

	// Print formatted report
	fmt.Print(baseline.FormatReport(report))

	// Exit with error code if regressions detected
	if !report.Passed {
		os.Exit(1)
	}

	return nil
}

// printBaselineInfo prints information about the saved baseline
func printBaselineInfo(benchType string) {
	filename, ok := baseline.BenchmarkFiles[benchType]
	if !ok {
		return
	}

	path := filepath.Join(baseline.BaselineDir, filename)

	// Get file info
	info, err := os.Stat(path)
	if err != nil {
		return
	}

	fmt.Printf("  Location: %s\n", path)
	fmt.Printf("  Size: %d bytes\n", info.Size())
	fmt.Printf("  Modified: %s\n", info.ModTime().Format("2006-01-02 15:04:05"))

	// Try to read and display commit info
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var bl baseline.Baseline
	if err := json.Unmarshal(data, &bl); err != nil {
		return
	}

	commit := bl.CommitSHA
	if len(commit) > 8 {
		commit = commit[:8]
	}
	fmt.Printf("  Commit: %s\n", commit)
	if bl.CommitMsg != "" {
		// Show first line of commit message
		lines := strings.Split(bl.CommitMsg, "\n")
		if len(lines) > 0 {
			fmt.Printf("  Message: %s\n", strings.TrimSpace(lines[0]))
		}
	}

	// List captured benchmarks
	fmt.Printf("  Benchmarks: %d\n", len(bl.Metrics))
}
