// seam-replay replays a captured corpus against both an incumbent proxy and the
// SEAM gateway, then differentially compares the responses to verify equivalence.
//
// This is the conformance test that gates a Phase 6b cutover: a service's
// route fragment does not ship, and its CLAUDE.md prose is not deleted,
// until its corpus passes the replay.
//
// Usage:
//
//	seam-replay --incumbent https://proxy.example.com \
//	            --seam http://localhost:9000 \
//	            --corpus argocd-corpus.json \
//	            --secrets argocd-secrets.local.json \
//	            --report argocd-report.json
//
// The tool outputs a per-service corpus pass report as JSON and a human-readable
// summary to stdout. Exit codes (docs/design/differential-replay-contract.md
// X1): 0 = no FAIL (passes and skips both fine); 1 = at least one FAIL or a
// harness failure (unreadable corpus or secrets file, a corpus with no
// replayable entries, an unwritable report); 2 = usage error.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/textproto"
	"os"
	"strings"
	"time"

	"github.com/ardenone/seam/tools/diffharness/internal/compare"
	"github.com/ardenone/seam/tools/diffharness/internal/corpus"
	"github.com/ardenone/seam/tools/diffharness/internal/secref"
)

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seam-replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	incumbentURL := fs.String("incumbent", "", "Base URL of the incumbent proxy (required)")
	seamURL := fs.String("seam", "", "Base URL of the SEAM gateway (required)")
	corpusPath := fs.String("corpus", "", "Path to the corpus JSON file (required)")
	secretsPath := fs.String("secrets", "", "Path to secrets file (optional, falls back to env)")
	reportPath := fs.String("report", "", "Path to write the JSON report (optional)")
	verbose := fs.Bool("verbose", false, "Log each replay as it runs")

	var ignoreHeaders stringSlice
	fs.Var(&ignoreHeaders, "ignore-header", "Header to ignore (may be repeated); defaults to Date, Server, etc.")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *incumbentURL == "" || *seamURL == "" || *corpusPath == "" {
		fmt.Fprintf(stderr, "seam-replay: missing required flags\n")
		fs.PrintDefaults()
		return 2
	}

	// Load corpus.
	cp, err := corpus.Load(*corpusPath)
	if err != nil {
		fmt.Fprintf(stderr, "seam-replay: load corpus: %v\n", err)
		return 1
	}
	if !cp.HasReplayable() {
		fmt.Fprintf(stderr, "seam-replay: corpus has no replayable entries (all entries have Expect.Skip set)\n")
		return 1
	}

	// Load secrets.
	resolver, err := secref.NewResolver(*secretsPath)
	if err != nil {
		fmt.Fprintf(stderr, "seam-replay: load secrets: %v\n", err)
		return 1
	}

	// Default ignore headers (volatile headers that differ call-to-call).
	defaultIgnore := []string{"Date", "Server", "X-Request-Id", "Set-Cookie", "ETag"}
	ignoreHeaders = append(ignoreHeaders, defaultIgnore...)

	r := &replayer{
		incumbentURL:  *incumbentURL,
		seamURL:       *seamURL,
		corpusPath:    *corpusPath,
		cp:            cp,
		resolver:      resolver,
		ignoreHeaders: ignoreHeaders,
		verbose:       *verbose,
	}

	report := r.run()

	// Write JSON report if requested.
	if *reportPath != "" {
		if err := writeReport(report, *reportPath); err != nil {
			fmt.Fprintf(stderr, "seam-replay: write report: %v\n", err)
			return 1
		}
	}

	// Human-readable summary.
	printSummary(report, stdout)

	// Exit status: FAIL if any entry failed (excluding SKIP).
	if report.FailCount > 0 {
		return 1
	}
	return 0
}

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type replayer struct {
	incumbentURL  string
	seamURL       string
	corpusPath    string
	cp            *corpus.Corpus
	resolver      *secref.Resolver
	ignoreHeaders []string
	verbose       bool
	client        *http.Client
}

func (r *replayer) run() *Report {
	start := time.Now()
	r.client = &http.Client{
		Timeout: 30 * time.Second,
		// Don't follow redirects; we want to capture the exact response.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	report := &Report{
		Corpus:     r.cp.Service,
		CorpusPath: r.corpusPath,
		Incumbent:  r.cp.Incumbent,
		Seam:       r.seamURL,
		RunAt:      time.Now().Format(time.RFC3339),
		Entries:    make([]EntryReport, len(r.cp.Entries)),
	}

	for i, entry := range r.cp.Entries {
		er := r.replayEntry(entry)
		report.Entries[i] = er
		switch er.Verdict {
		case compare.VerdictPass:
			report.PassCount++
		case compare.VerdictFail:
			report.FailCount++
		case compare.VerdictSkip:
			report.SkipCount++
		}
	}

	report.Duration = time.Since(start).Seconds()
	return report
}

func (r *replayer) replayEntry(entry corpus.Entry) EntryReport {
	if r.verbose {
		log.Printf("replaying: %s", entry.ID)
	}

	er := EntryReport{
		ID:          entry.ID,
		Description: entry.Description,
	}

	// Check for explicit skip.
	if entry.Expect != nil && entry.Expect.Skip != "" {
		er.Verdict = compare.VerdictSkip
		er.SkipReason = entry.Expect.Skip
		return er
	}

	// Resolve secrets.
	secrets := make([]compare.SecretValue, 0, len(entry.Secrets))
	unresolved := false
	for _, s := range entry.Secrets {
		bare, ok := r.resolver.Resolve(s.Ref)
		if !ok {
			unresolved = true
			er.Verdict = compare.VerdictSkip
			er.SkipReason = fmt.Sprintf("unresolved secret ref %q", s.Ref)
			return er
		}
		secrets = append(secrets, compare.SecretValue{
			Ref:      s.Ref,
			Bare:     bare,
			InjectAs: s.InjectAs,
		})
	}
	if unresolved {
		return er
	}

	// Replay against incumbent.
	incResp, incErr := r.replayOne(r.incumbentURL, entry)
	if incErr != nil {
		er.Verdict = compare.VerdictSkip
		er.SkipReason = fmt.Sprintf("incumbent request failed: %v", incErr)
		return er
	}

	// Replay against SEAM.
	seamResp, seamErr := r.replayOne(r.seamURL, entry)
	if seamErr != nil {
		er.Verdict = compare.VerdictFail
		er.Reasons = append(er.Reasons, fmt.Sprintf("SEAM request failed: %v", seamErr))
		return er
	}

	// Build compare options.
	opts := compare.Options{
		IgnoreHeaders: r.ignoreHeaders,
	}
	if entry.Expect != nil {
		opts.IgnoreHeaders = append(opts.IgnoreHeaders, entry.Expect.IgnoreHeaders...)
		if entry.Expect.IgnoreBody {
			opts.IgnoreBody = true
		}
		if entry.Expect.Status != nil {
			opts.ExpectedStatus = entry.Expect.Status
		}
	}

	// Compare.
	result := compare.Compare(incResp, seamResp, secrets, opts)
	er.Verdict = result.Verdict
	er.SecretLeaked = result.SecretLeaked
	er.LeakedSecret = result.LeakedSecret
	er.LeakedWhere = result.LeakedWhere
	er.Reasons = result.Reasons
	er.StatusDiff = result.StatusDiff
	er.HeaderDiffs = result.HeaderDiffs
	er.TrailerDiffs = result.TrailerDiffs
	er.BodyDiff = result.BodyDiff
	er.BodyIgnored = result.BodyIgnored

	return er
}

func (r *replayer) replayOne(baseURL string, entry corpus.Entry) (*compare.Response, error) {
	// Build the full URL.
	u := baseURL + entry.Request.Path
	if entry.Request.Query != "" {
		u += "?" + entry.Request.Query
	}

	// Build body.
	var body io.Reader
	if entry.Request.BodyB64 != "" {
		raw, err := base64.StdEncoding.DecodeString(entry.Request.BodyB64)
		if err != nil {
			return nil, fmt.Errorf("decode body: %w", err)
		}
		body = bytes.NewReader(raw)
	}

	// Build request.
	req, err := http.NewRequest(entry.Request.Method, u, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Copy headers.
	for k, vv := range entry.Request.Headers {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	if body != nil && entry.Request.BodyContentType != "" {
		req.Header.Set("Content-Type", entry.Request.BodyContentType)
	}

	// Execute.
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	// Read body.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// Read trailers.
	trailers := make(map[string][]string)
	for k, vv := range resp.Trailer {
		ck := textproto.CanonicalMIMEHeaderKey(k)
		trailers[ck] = vv
	}

	// Canonicalize headers.
	headers := make(map[string][]string)
	for k, vv := range resp.Header {
		ck := textproto.CanonicalMIMEHeaderKey(k)
		headers[ck] = vv
	}

	return &compare.Response{
		Status:   resp.StatusCode,
		Headers:  headers,
		Body:     respBody,
		Trailers: trailers,
	}, nil
}

// Report is the top-level report written to JSON.
type Report struct {
	Corpus     string        `json:"corpus"`
	CorpusPath string        `json:"corpusPath"`
	Incumbent  string        `json:"incumbent"`
	Seam       string        `json:"seam"`
	RunAt      string        `json:"runAt"`
	Duration   float64       `json:"durationSeconds"`
	PassCount  int           `json:"passCount"`
	FailCount  int           `json:"failCount"`
	SkipCount  int           `json:"skipCount"`
	Entries    []EntryReport `json:"entries"`
}

// EntryReport is the per-entry result.
type EntryReport struct {
	ID           string               `json:"id"`
	Description  string               `json:"description"`
	Verdict      compare.Verdict      `json:"verdict"`
	SkipReason   string               `json:"skipReason,omitempty"`
	SecretLeaked bool                 `json:"secretLeaked,omitempty"`
	LeakedSecret string               `json:"leakedSecret,omitempty"`
	LeakedWhere  string               `json:"leakedWhere,omitempty"`
	Reasons      []string             `json:"reasons,omitempty"`
	StatusDiff   *compare.StatusDiff  `json:"statusDiff,omitempty"`
	HeaderDiffs  []compare.HeaderDiff `json:"headerDiffs,omitempty"`
	TrailerDiffs []compare.HeaderDiff `json:"trailerDiffs,omitempty"`
	BodyDiff     *compare.BodyDiff    `json:"bodyDiff,omitempty"`
	BodyIgnored  bool                 `json:"bodyIgnored,omitempty"`
}

func writeReport(report *Report, path string) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

func printSummary(report *Report, w io.Writer) {
	fmt.Fprintf(w, "SEAM Differential Replay Report\n")
	fmt.Fprintf(w, "=============================\n\n")
	fmt.Fprintf(w, "Corpus:       %s\n", report.Corpus)
	fmt.Fprintf(w, "Incumbent:    %s\n", report.Incumbent)
	fmt.Fprintf(w, "SEAM:         %s\n", report.Seam)
	fmt.Fprintf(w, "Run at:       %s\n", report.RunAt)
	fmt.Fprintf(w, "Duration:     %.2fs\n\n", report.Duration)

	fmt.Fprintf(w, "Results:\n")
	fmt.Fprintf(w, "  PASS:  %d\n", report.PassCount)
	fmt.Fprintf(w, "  FAIL:  %d\n", report.FailCount)
	fmt.Fprintf(w, "  SKIP:  %d\n\n", report.SkipCount)

	if report.FailCount > 0 {
		fmt.Fprintf(w, "Failed entries:\n")
		for _, e := range report.Entries {
			if e.Verdict == compare.VerdictFail {
				fmt.Fprintf(w, "  - %s", e.ID)
				if e.Description != "" {
					fmt.Fprintf(w, " (%s)", e.Description)
				}
				fmt.Fprintln(w)
				if e.SecretLeaked {
					fmt.Fprintf(w, "    SECURITY: secret %q leaked in %s\n", e.LeakedSecret, e.LeakedWhere)
				}
				for _, r := range e.Reasons {
					fmt.Fprintf(w, "      - %s\n", r)
				}
			}
		}
		fmt.Fprintln(w)
	}

	if report.SkipCount > 0 {
		fmt.Fprintf(w, "Skipped entries:\n")
		for _, e := range report.Entries {
			if e.Verdict == compare.VerdictSkip {
				fmt.Fprintf(w, "  - %s: %s\n", e.ID, e.SkipReason)
			}
		}
		fmt.Fprintln(w)
	}

	if report.PassCount == len(report.Entries) {
		fmt.Fprintf(w, "✓ All entries passed!\n")
	}
}
