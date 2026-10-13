package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ardenone/seam/internal/limits"
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
	providerProfilesPath := ""
	maxRequestBytesCeiling := limits.DefaultMaxRequestBytesCeiling
	maxResponseBytesCeiling := limits.DefaultMaxResponseBytesCeiling
	maxTimeoutCeiling := limits.DefaultMaxTimeoutCeiling
	jsonOutput := false

	fs.StringVar(&fragmentsDir, "fragments-dir", fragmentsDir, "Directory containing route fragments")
	fs.StringVar(&fragmentsDir, "fragments", fragmentsDir, "Alias for -fragments-dir")
	fs.StringVar(&schemaPath, "schema-path", schemaPath, "Path to route-fragment-schema.json")
	fs.StringVar(&schemaPath, "schema", schemaPath, "Alias for -schema-path")
	fs.StringVar(&allowlistPath, "upstream-allowlist", allowlistPath, "Operator-owned upstream-host allowlist (absent is inert before Phase 6a)")
	fs.StringVar(&allowlistPath, "upstream-allowlist-path", allowlistPath, "Alias for -upstream-allowlist")
	fs.StringVar(&allowlistPath, "allowlist-path", allowlistPath, "Alias for -upstream-allowlist")
	fs.StringVar(&allowlistPath, "allowlist", allowlistPath, "Alias for -upstream-allowlist")
	fs.StringVar(&providerProfilesPath, "provider-profiles", providerProfilesPath, "Operator-owned provider-profile file (absent uses SEAM_PROVIDER_PROFILES, then the compiled-in seed set)")
	fs.Int64Var(&maxRequestBytesCeiling, "max-request-bytes-ceiling", maxRequestBytesCeiling, "Operator ceiling for x-max-request-bytes")
	fs.Int64Var(&maxResponseBytesCeiling, "max-response-bytes-ceiling", maxResponseBytesCeiling, "Operator ceiling for x-max-response-bytes")
	fs.DurationVar(&maxTimeoutCeiling, "max-timeout-ceiling", maxTimeoutCeiling, "Operator ceiling for x-timeout")
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
	if value := os.Getenv(seamspec.ProviderProfilesEnvVar); value != "" && providerProfilesPath == "" {
		providerProfilesPath = value
	}
	for _, ceiling := range []struct {
		envVar   string
		flagName string
		dest     *int64
	}{
		{"SEAM_MAX_REQUEST_BYTES_CEILING", "max-request-bytes-ceiling", &maxRequestBytesCeiling},
		{"SEAM_MAX_RESPONSE_BYTES_CEILING", "max-response-bytes-ceiling", &maxResponseBytesCeiling},
	} {
		if value := os.Getenv(ceiling.envVar); value != "" && !flagWasSet(fs, ceiling.flagName) {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "seam lint: %s must be a positive integer byte count\n", ceiling.envVar)
				return 2
			}
			*ceiling.dest = parsed
		}
		if *ceiling.dest <= 0 {
			_, _ = fmt.Fprintf(stderr, "seam lint: --%s must be positive\n", ceiling.flagName)
			return 2
		}
	}
	if value := os.Getenv("SEAM_MAX_TIMEOUT_CEILING"); value != "" && !flagWasSet(fs, "max-timeout-ceiling") {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "seam lint: SEAM_MAX_TIMEOUT_CEILING must be a Go duration")
			return 2
		}
		maxTimeoutCeiling = parsed
	}
	if maxTimeoutCeiling <= 0 {
		_, _ = fmt.Fprintln(stderr, "seam lint: --max-timeout-ceiling must be positive")
		return 2
	}

	options := seamspec.LintOptions{
		FragmentsDir:            fragmentsDir,
		SchemaPath:              schemaPath,
		UpstreamAllowlistPath:   allowlistPath,
		ProviderProfilesPath:    providerProfilesPath,
		MaxRequestBytesCeiling:  maxRequestBytesCeiling,
		MaxResponseBytesCeiling: maxResponseBytesCeiling,
		MaxTimeoutCeiling:       maxTimeoutCeiling,
	}
	var report seamspec.LintReport
	var err error
	if len(positional) == 0 {
		report, err = seamspec.LintDirectory(options)
	} else if len(positional) == 1 {
		if info, statErr := os.Stat(positional[0]); statErr == nil && info.IsDir() {
			report, err = seamspec.LintDirectory(seamspec.LintOptions{
				FragmentsDir:            positional[0],
				SchemaPath:              schemaPath,
				UpstreamAllowlistPath:   allowlistPath,
				ProviderProfilesPath:    providerProfilesPath,
				MaxRequestBytesCeiling:  maxRequestBytesCeiling,
				MaxResponseBytesCeiling: maxResponseBytesCeiling,
				MaxTimeoutCeiling:       maxTimeoutCeiling,
			})
		} else {
			report, err = seamspec.LintFiles(positional, options)
		}
	} else {
		report, err = seamspec.LintFiles(positional, options)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "seam lint: %v\n", err)
		return 2
	}

	if jsonOutput {
		if err := writeLintJSON(stdout, report); err != nil {
			_, _ = fmt.Fprintf(stderr, "seam lint: write JSON report: %v\n", err)
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
		"fragments-dir":              true,
		"fragments":                  true,
		"schema-path":                true,
		"schema":                     true,
		"upstream-allowlist":         true,
		"upstream-allowlist-path":    true,
		"allowlist-path":             true,
		"allowlist":                  true,
		"max-request-bytes-ceiling":  true,
		"max-response-bytes-ceiling": true,
		"max-timeout-ceiling":        true,
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
		_, _ = fmt.Fprintf(stdout, "ERROR [%s] %s: %s\n", finding.Code, finding.File, finding.Message)
	}
	for _, finding := range report.Warnings {
		_, _ = fmt.Fprintf(stdout, "WARNING [%s] %s: %s\n", finding.Code, finding.File, finding.Message)
	}
	status := "passed"
	if report.HasErrors() {
		status = "failed"
	}
	_, _ = fmt.Fprintf(stdout, "seam lint: %s (%d file(s), %d error(s), %d warning(s))\n", status, report.Files, len(report.Errors), len(report.Warnings))
}
