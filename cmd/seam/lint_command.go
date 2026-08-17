package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	seamspec "github.com/ardenone/seam/internal/spec"
)

// lintCommand is the CLI wrapper. The implementation is kept in
// runLintCommand so command-line behavior can be tested without terminating a
// test process via os.Exit.
func lintCommand(args []string) {
	os.Exit(runLintCommand(args, os.Stdout, os.Stderr))
}

func runLintCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	args = reorderLintArgs(args)

	fragmentsDir := "./fragments"
	schemaPath := "./spec/route-fragment-schema.json"
	allowlistPath := ""
	jsonOutput := false

	fs.StringVar(&fragmentsDir, "fragments-dir", fragmentsDir, "Directory containing route fragments")
	fs.StringVar(&fragmentsDir, "fragments", fragmentsDir, "Alias for -fragments-dir")
	fs.StringVar(&schemaPath, "schema-path", schemaPath, "Path to route-fragment-schema.json")
	fs.StringVar(&schemaPath, "schema", schemaPath, "Alias for -schema-path")
	fs.StringVar(&allowlistPath, "upstream-allowlist", allowlistPath, "Operator-owned upstream-host allowlist (absent is inert before Phase 6a)")
	fs.StringVar(&allowlistPath, "upstream-allowlist-path", allowlistPath, "Alias for -upstream-allowlist")
	fs.StringVar(&allowlistPath, "allowlist-path", allowlistPath, "Alias for -upstream-allowlist")
	fs.StringVar(&allowlistPath, "allowlist", allowlistPath, "Alias for -upstream-allowlist")
	fs.BoolVar(&jsonOutput, "json", jsonOutput, "Emit a machine-readable JSON report")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	positional := fs.Args()

	if value := os.Getenv("SEAM_FRAGMENTS_DIR"); value != "" && fragmentsDir == "./fragments" {
		fragmentsDir = value
	}
	if value := os.Getenv("SEAM_SCHEMA_PATH"); value != "" && schemaPath == "./spec/route-fragment-schema.json" {
		schemaPath = value
	}
	if value := os.Getenv("SEAM_UPSTREAM_ALLOWLIST"); value != "" && allowlistPath == "" {
		allowlistPath = value
	}

	options := seamspec.LintOptions{
		FragmentsDir:          fragmentsDir,
		SchemaPath:            schemaPath,
		UpstreamAllowlistPath: allowlistPath,
	}
	var report seamspec.LintReport
	var err error
	if len(positional) == 0 {
		report, err = seamspec.LintDirectory(options)
	} else if len(positional) == 1 {
		if info, statErr := os.Stat(positional[0]); statErr == nil && info.IsDir() {
			report, err = seamspec.LintDirectory(seamspec.LintOptions{
				FragmentsDir:          positional[0],
				SchemaPath:            schemaPath,
				UpstreamAllowlistPath: allowlistPath,
			})
		} else {
			report, err = seamspec.LintFiles(positional, options)
		}
	} else {
		report, err = seamspec.LintFiles(positional, options)
	}
	if err != nil {
		fmt.Fprintf(stderr, "seam lint: %v\n", err)
		return 2
	}

	if jsonOutput {
		if err := writeLintJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "seam lint: write JSON report: %v\n", err)
			return 2
		}
	} else {
		writeLintText(stdout, report)
	}
	if report.HasErrors() {
		return 1
	}
	return 0
}

// The standard flag package stops parsing at the first positional argument.
// Accepting flags after a file path keeps the command pleasant to use with
// shell-expanded fragment globs while retaining the standard flag syntax.
func reorderLintArgs(args []string) []string {
	valueFlags := map[string]bool{
		"fragments-dir":           true,
		"fragments":               true,
		"schema-path":             true,
		"schema":                  true,
		"upstream-allowlist":      true,
		"upstream-allowlist-path": true,
		"allowlist-path":          true,
		"allowlist":               true,
	}
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		argument := args[i]
		if argument == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(argument, "-") {
			positionals = append(positionals, argument)
			continue
		}
		flags = append(flags, argument)
		if valueFlags[strings.TrimLeft(argument, "-")] && !strings.Contains(argument, "=") && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positionals...)
}

func writeLintJSON(stdout io.Writer, report seamspec.LintReport) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func writeLintText(stdout io.Writer, report seamspec.LintReport) {
	for _, finding := range report.Errors {
		fmt.Fprintf(stdout, "ERROR [%s] %s: %s\n", finding.Code, finding.File, finding.Message)
	}
	for _, finding := range report.Warnings {
		fmt.Fprintf(stdout, "WARNING [%s] %s: %s\n", finding.Code, finding.File, finding.Message)
	}
	status := "passed"
	if report.HasErrors() {
		status = "failed"
	}
	fmt.Fprintf(stdout, "seam lint: %s (%d file(s), %d error(s), %d warning(s))\n", status, report.Files, len(report.Errors), len(report.Warnings))
}
