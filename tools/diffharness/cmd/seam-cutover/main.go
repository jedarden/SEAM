// seam-cutover — the go/no-go gate runner for a per-service SEAM cutover.
//
// The runbook (docs/migration-runbook.md, Stage 2) mechanizes its gate items
// here: SEAM healthz/readyz, the operator-port trust-boundary refusal probed
// from a worker vantage, corpus-route presence in /openapi.json, the DNS
// dual-run preconditions, the prose-still-present guard, and the seam-replay
// differential as a subprocess. `rollback` prints the per-service rollback
// runbook with the concrete revert-finding commands filled in.
//
// The mechanism everywhere is a git revert in declarative-config — never a
// live mutation; ArgoCD selfHeal reverts those.
//
// Exit codes for `check`: 0 = no mechanical failures (MANUAL items still need
// operator attestation in the cutover PR); 1 = at least one mechanical FAIL
// (NO-GO); 2 = usage error. A NO-GO blocks this service only.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ardenone/seam/tools/diffharness/internal/corpus"
)

const usage = `seam-cutover — SEAM migration go/no-go gate runner (docs/migration-runbook.md)

Usage:
  seam-cutover check --service <svc> --seam <url> [flags]
  seam-cutover rollback --service <svc> [--level agent|fragment|binary|all] [flags]

check flags (runbook Stage 2 gate items):
  --service <svc>           service owner token (required)
  --seam <url>              SEAM caller-port base URL (required)
  --operator <url>          SEAM operator-port base URL; must refuse a worker
                            vantage (gate item 2)
  --incumbent <url>         incumbent proxy base URL; DNS dual-run
                            precondition (gate item 5)
  --corpus <path>           corpus JSON — runtime capture or the committed
                            testdata fixture (gate items 1 and 4)
  --secrets <path>          secrets file handed to seam-replay (never committed)
  --replay-bin <path>       seam-replay binary, run as the differential
                            subprocess gate (gate item 1)
  --agent-doc <path>        agent-facing prose file; must still contain the
                            incumbent pointer (wrong-direction sequencing guard)
  --agent-doc-contains <s>  needle to require in the prose
                            (default: the incumbent URL)
  --report <path>           write the JSON check report here; the replay
                            report lands beside it as <report>-replay.json
  --metered                 service is metered: adds the Phase 13
                            cost-governor MANUAL item (gate item 6)

Exit codes: 0 = no mechanical failures (MANUAL items still require operator
attestation in the cutover PR); 1 = at least one mechanical FAIL (NO-GO);
2 = usage error.

rollback flags:
  --service <svc>    service owner token (required)
  --level <level>    agent | fragment | binary | all (default all)
  --routes-dir <p>   fragment dir in declarative-config
                     (default k8s/rs-manager/seam/routes/<svc>)
  --docs-repo <p>    repo holding the agent-facing prose (default .)
  --prose-file <p>   agent-facing prose file (default CLAUDE.md)
  --bead <id>        cutover bead id, named by the L1 revert search
`

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "%s", usage)
		return 2
	}
	switch args[0] {
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "rollback":
		return runRollback(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintf(stdout, "%s", usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n\n%s", args[0], usage)
		return 2
	}
}

// checkConfig is the armed subset of the check flags. Fields left empty mean
// the corresponding gate checks SKIP rather than silently pass.
type checkConfig struct {
	service      string
	seamURL      string
	operatorURL  string
	incumbentURL string
	corpusPath   string
	secretsPath  string
	agentDoc     string
	// agentDocContains overrides the needle the prose guard looks for;
	// empty means the incumbent URL.
	agentDocContains string
	replayBin        string
	replayReport     string
	metered          bool
}

const (
	probeTimeout  = 10 * time.Second
	replayTimeout = 15 * time.Minute
)

func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	service := fs.String("service", "", "service owner token (required)")
	seamURL := fs.String("seam", "", "SEAM caller-port base URL (required)")
	operatorURL := fs.String("operator", "", "SEAM operator-port base URL")
	incumbentURL := fs.String("incumbent", "", "incumbent proxy base URL")
	corpusPath := fs.String("corpus", "", "corpus JSON path")
	secretsPath := fs.String("secrets", "", "secrets file for seam-replay")
	agentDoc := fs.String("agent-doc", "", "agent-facing prose file to guard")
	agentDocContains := fs.String("agent-doc-contains", "", "needle to require in the prose (default: incumbent URL)")
	replayBin := fs.String("replay-bin", "", "seam-replay binary to run as a subprocess")
	replayReport := fs.String("replay-report", "", "replay report path (default derived from --report)")
	reportPath := fs.String("report", "", "write the JSON check report here")
	metered := fs.Bool("metered", false, "service is metered: add the Phase 13 MANUAL item")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "%s", usage)
		return 2
	}
	if *service == "" || *seamURL == "" {
		fmt.Fprintln(stderr, "seam-cutover check requires --service and --seam")
		return 2
	}

	cfg := checkConfig{
		service:          *service,
		seamURL:          *seamURL,
		operatorURL:      *operatorURL,
		incumbentURL:     *incumbentURL,
		corpusPath:       *corpusPath,
		secretsPath:      *secretsPath,
		agentDoc:         *agentDoc,
		agentDocContains: *agentDocContains,
		replayBin:        *replayBin,
		replayReport:     *replayReport,
		metered:          *metered,
	}
	if cfg.replayBin != "" && cfg.replayReport == "" {
		cfg.replayReport = deriveReplayReport(*reportPath)
	}

	rep := runChecks(cfg)
	printReport(rep, stdout)
	if *reportPath != "" {
		if err := writeReport(rep, *reportPath); err != nil {
			fmt.Fprintf(stderr, "write report %s: %v\n", *reportPath, err)
			return 1
		}
		fmt.Fprintf(stdout, "report written to %s\n", *reportPath)
	}
	if rep.FailCount > 0 {
		return 1
	}
	return 0
}

func runChecks(cfg checkConfig) *CheckReport {
	rep := CheckReport{Service: cfg.service, GeneratedAt: time.Now().UTC()}
	client := &http.Client{Timeout: probeTimeout}

	// Corpus load failure is mechanical: routes-in-spec FAILs naming it, and
	// the replay subprocess fails on its own.
	var cp *corpus.Corpus
	var corpusErr error
	if cfg.corpusPath != "" {
		cp, corpusErr = corpus.Load(cfg.corpusPath)
	}

	// Gate item 4: SEAM healthy and ready.
	if cfg.seamURL == "" {
		rep.add(Check{Name: "seam-healthz", Verdict: verdictSkip, Detail: "seam URL not set"})
		rep.add(Check{Name: "seam-readyz", Verdict: verdictSkip, Detail: "seam URL not set"})
	} else {
		base := strings.TrimRight(cfg.seamURL, "/")
		rep.add(checkEndpoint(client, "seam-healthz", base+"/_seam/healthz"))
		rep.add(checkEndpoint(client, "seam-readyz", base+"/_seam/readyz"))
	}

	// Gate item 2: the operator port refuses a worker vantage.
	rep.add(checkOperatorRefused(client, cfg))

	// Gate item 4: the service's corpus routes are present in /openapi.json.
	switch {
	case cfg.seamURL == "":
		rep.add(Check{Name: "routes-in-spec", Verdict: verdictSkip, Detail: "seam URL not set"})
	case corpusErr != nil:
		rep.add(Check{Name: "routes-in-spec", Verdict: verdictFail,
			Detail: fmt.Sprintf("load corpus %s: %v", cfg.corpusPath, corpusErr)})
	default:
		rep.add(checkRoutesInSpec(client, cfg, cp, nil))
	}

	// Gate item 5: dual-run precondition — both names resolvable.
	if cfg.seamURL == "" {
		rep.add(Check{Name: "seam-resolves", Verdict: verdictSkip, Detail: "seam URL not set"})
	} else {
		rep.add(checkDNS("seam-resolves", cfg.seamURL))
	}
	if cfg.incumbentURL == "" {
		rep.add(Check{Name: "incumbent-resolves", Verdict: verdictSkip, Detail: "incumbent URL not set"})
	} else {
		rep.add(checkDNS("incumbent-resolves", cfg.incumbentURL))
	}

	// Prose guard: fails in the wrong-direction sequencing case too — the
	// incumbent prose must not be deleted before the corpus passes.
	rep.add(checkAgentDoc(cfg))

	// Gate item 1: differential corpus green (the shipped-commit rule applies:
	// run seam-replay on the exact build that will serve traffic).
	rep.add(checkReplay(cfg))

	// Gate item 3: the one item that cannot be probed from here.
	rep.add(Check{Name: "agent-retry-budget", Verdict: verdictManual,
		Detail: "attest ≥60 s retry/backoff for every agent population of this service in the cutover PR (runbook Stage 2 item 3)"})

	// Gate item 6, metered services only.
	if cfg.metered {
		rep.add(Check{Name: "phase13-cost-governor", Verdict: verdictManual,
			Detail: "Phase 13 cost governor must be confirmed live before cutover (runbook Stage 2 item 6)"})
	}

	if rep.FailCount > 0 {
		rep.GoNoGo = "NO-GO"
	} else {
		rep.GoNoGo = "GO"
	}
	return &rep
}

const (
	verdictPass   = "PASS"
	verdictFail   = "FAIL"
	verdictSkip   = "SKIP"
	verdictManual = "MANUAL"
)

// Check is one gate item's outcome.
type Check struct {
	Name    string `json:"name"`
	Verdict string `json:"verdict"`
	Detail  string `json:"detail"`
}

// CheckReport is the JSON report attached to the cutover PR (runbook,
// Evidence bundle item 2).
type CheckReport struct {
	Service     string    `json:"service"`
	GeneratedAt time.Time `json:"generatedAt"`
	GoNoGo      string    `json:"goNoGo"`
	PassCount   int       `json:"pass"`
	FailCount   int       `json:"fail"`
	SkipCount   int       `json:"skip"`
	ManualCount int       `json:"manual"`
	Checks      []Check   `json:"checks"`
}

func (r *CheckReport) add(c Check) {
	r.Checks = append(r.Checks, c)
	switch c.Verdict {
	case verdictPass:
		r.PassCount++
	case verdictFail:
		r.FailCount++
	case verdictManual:
		r.ManualCount++
	default:
		r.SkipCount++
	}
}

func pass(name, detail string) Check {
	return Check{Name: name, Verdict: verdictPass, Detail: detail}
}

func fail(name, detail string) Check {
	return Check{Name: name, Verdict: verdictFail, Detail: detail}
}

// pathMatchesTemplate reports whether a concrete request path hits a spec
// path template: equal segment counts, literal segments equal, and every
// {param} filled by a non-empty concrete segment.
func pathMatchesTemplate(concrete, tmpl string) bool {
	cs := strings.Split(concrete, "/")
	ts := strings.Split(tmpl, "/")
	if len(cs) != len(ts) {
		return false
	}
	for i := range ts {
		if isPathTemplateParam(ts[i]) {
			if cs[i] == "" {
				return false
			}
			continue
		}
		if cs[i] != ts[i] {
			return false
		}
	}
	return true
}

func isPathTemplateParam(seg string) bool {
	return len(seg) > 2 && strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")
}

// checkOperatorRefused is gate item 2's refusal half — the item easiest to
// skip because nothing fails when it is wrong. A transport-level failure is
// exactly what a tailnet ACL denial looks like from a worker vantage; any
// HTTP answer (even 403) means the trust boundary does not hold.
func checkOperatorRefused(client *http.Client, cfg checkConfig) Check {
	const name = "operator-port-refused"
	if cfg.operatorURL == "" {
		return Check{Name: name, Verdict: verdictSkip,
			Detail: "operator URL not set (worker-vantage ACL check not armed)"}
	}
	probe := strings.TrimRight(cfg.operatorURL, "/") + "/config/status"
	resp, err := client.Get(probe)
	if err != nil {
		return pass(name, fmt.Sprintf("operator port did not answer (%v) — the ACL refusal holds", err))
	}
	defer func() { _ = resp.Body.Close() }()
	return fail(name, fmt.Sprintf("operator port ANSWERED with HTTP %d at %s — worker-tagged clients are not being refused", resp.StatusCode, probe))
}

// checkEndpoint probes one SEAM health endpoint; only 200 passes.
func checkEndpoint(client *http.Client, name, rawURL string) Check {
	resp, err := client.Get(rawURL)
	if err != nil {
		return fail(name, fmt.Sprintf("%s unreachable: %v", rawURL, err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fail(name, fmt.Sprintf("%s returned HTTP %d, want 200", rawURL, resp.StatusCode))
	}
	return pass(name, fmt.Sprintf("%s → 200", rawURL))
}

// checkDNS is gate item 5's resolvability half. Reachability is the replay's
// job (it sends real requests to both sides); here the dual-run precondition
// is that both names still resolve.
func checkDNS(name, rawURL string) Check {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fail(name, fmt.Sprintf("parse %s: %v", rawURL, err))
	}
	host := u.Hostname()
	if host == "" {
		return fail(name, fmt.Sprintf("%s has no host to resolve", rawURL))
	}
	addrs, err := net.LookupHost(host)
	if err != nil {
		return fail(name, fmt.Sprintf("cannot resolve %s: %v", host, err))
	}
	return pass(name, fmt.Sprintf("%s resolves (%d address(es))", host, len(addrs)))
}

// checkRoutesInSpec is gate item 4's route-presence half: every replayable
// corpus entry must hit a route SEAM's published spec serves, matching path
// templates segment-wise ({name} fills any non-empty segment). skip holds
// entry IDs attested as not-onboarded outside the corpus; nil means only
// corpus-level Expect.Skip applies.
func checkRoutesInSpec(client *http.Client, cfg checkConfig, cp *corpus.Corpus, skip map[string]bool) Check {
	const name = "routes-in-spec"
	if cfg.seamURL == "" {
		return Check{Name: name, Verdict: verdictSkip, Detail: "seam URL not set"}
	}
	specURL := strings.TrimRight(cfg.seamURL, "/") + "/openapi.json"
	resp, err := client.Get(specURL)
	if err != nil {
		return fail(name, fmt.Sprintf("cannot reach %s: %v", specURL, err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fail(name, fmt.Sprintf("%s returned HTTP %d, want 200", specURL, resp.StatusCode))
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fail(name, fmt.Sprintf("parse %s: %v", specURL, err))
	}

	var missing []string
	var checked, skipped int
	if cp != nil {
		for _, e := range cp.Entries {
			if e.Expect != nil && e.Expect.Skip != "" {
				skipped++
				continue
			}
			if skip[e.ID] {
				skipped++
				continue
			}
			checked++
			if !specServes(doc.Paths, e.Request.Method, e.Request.Path) {
				missing = append(missing, e.ID)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fail(name, fmt.Sprintf("%d corpus route(s) missing from /openapi.json: %s", len(missing), strings.Join(missing, ", ")))
	}
	return pass(name, fmt.Sprintf("%d corpus route(s) present in /openapi.json (%d skipped)", checked, skipped))
}

func specServes(paths map[string]map[string]json.RawMessage, method, concrete string) bool {
	op := strings.ToLower(strings.TrimSpace(method))
	for specPath, item := range paths {
		if item == nil {
			continue
		}
		if _, ok := item[op]; !ok {
			continue
		}
		if pathMatchesTemplate(concrete, specPath) {
			return true
		}
	}
	return false
}

// checkAgentDoc is the prose-still-present guard: the agent-facing docs must
// still carry the incumbent pointer until the Stage 3 cutover commit deletes
// it. Fails if the file is missing, and names the wrong-direction case
// (prose deleted before the corpus passed) explicitly.
func checkAgentDoc(cfg checkConfig) Check {
	const name = "prose-still-present"
	if cfg.agentDoc == "" {
		return Check{Name: name, Verdict: verdictSkip, Detail: "agent doc not set (prose guard not armed)"}
	}
	body, err := os.ReadFile(cfg.agentDoc)
	if err != nil {
		return fail(name, fmt.Sprintf("cannot read agent doc %s: %v", cfg.agentDoc, err))
	}
	needle := cfg.agentDocContains
	if needle == "" {
		needle = cfg.incumbentURL
	}
	if needle == "" {
		return Check{Name: name, Verdict: verdictSkip, Detail: "no incumbent URL or --agent-doc-contains needle to look for"}
	}
	if strings.Contains(string(body), needle) {
		return pass(name, fmt.Sprintf("%s still points at the incumbent (%s)", cfg.agentDoc, needle))
	}
	return fail(name, fmt.Sprintf("%s appears already deleted — %q is no longer present; prose is deleted only in the Stage 3 cutover commit", cfg.agentDoc, needle))
}

// checkReplay is gate item 1, the hard gate: the corpus must be green on the
// exact build that will serve traffic. The differential itself lives in
// seam-replay; here it runs as a subprocess and its exit code decides.
func checkReplay(cfg checkConfig) Check {
	const name = "corpus-replay"
	if cfg.replayBin == "" {
		return Check{Name: name, Verdict: verdictSkip,
			Detail: "replay binary not set (differential gate not armed on this host)"}
	}
	args := []string{
		"--corpus", cfg.corpusPath,
		"--incumbent", cfg.incumbentURL,
		"--seam", cfg.seamURL,
		"--report", cfg.replayReport,
	}
	if cfg.secretsPath != "" {
		args = append(args, "--secrets", cfg.secretsPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), replayTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, cfg.replayBin, args...).CombinedOutput()
	if err == nil {
		return pass(name, fmt.Sprintf("seam-replay passed; report at %s", cfg.replayReport))
	}
	return fail(name, fmt.Sprintf("seam-replay failed: %v\n%s", err, outputTail(out)))
}

// outputTail keeps the last few KB of subprocess output — the FAIL summary
// lives at the end of a replay run.
func outputTail(out []byte) string {
	const max = 4000
	s := strings.TrimRight(string(out), "\n")
	if s == "" {
		return "(no output)"
	}
	if len(s) > max {
		return "…" + s[len(s)-max:]
	}
	return s
}

// deriveReplayReport puts the replay report beside the check report — the
// cutover PR attaches both, and they belong together on disk too.
func deriveReplayReport(report string) string {
	if report == "" {
		return "cutover-replay.json"
	}
	return strings.TrimSuffix(report, ".json") + "-replay.json"
}

func printReport(rep *CheckReport, w io.Writer) {
	fmt.Fprintf(w, "SEAM cutover go/no-go — service %s\n", rep.Service)
	for _, c := range rep.Checks {
		fmt.Fprintf(w, "  %-6s %s — %s\n", c.Verdict, c.Name, oneLine(c.Detail))
	}
	fmt.Fprintf(w, "\n%s: %d failed, %d passed, %d skipped, %d manual\n",
		rep.GoNoGo, rep.FailCount, rep.PassCount, rep.SkipCount, rep.ManualCount)
	if rep.ManualCount > 0 {
		fmt.Fprintln(w, "MANUAL items require operator attestation in the cutover PR (docs/migration-runbook.md, Stage 2).")
	}
}

func oneLine(s string) string {
	return strings.ReplaceAll(s, "\n", "; ")
}

func writeReport(rep *CheckReport, path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	body, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o644)
}

// rollbackConfig parameterizes printRollback. Zero values resolve to the
// per-service defaults the runbook uses.
type rollbackConfig struct {
	service   string
	level     string // agent | fragment | binary | all ("" → all)
	routesDir string // fragment dir in declarative-config; default derived from service
	docsRepo  string // where the agent-facing prose lives; default "."
	proseFile string // the agent-facing prose file; default CLAUDE.md
	bead      string // the cutover bead — names the L1 revert search
}

func defaultRoutesDir(service string) string {
	return "k8s/rs-manager/seam/routes/" + service
}

// printRollback writes the per-service rollback runbook (runbook §Rollback):
// three escalating levels, git revert in declarative-config as the mechanism
// everywhere — never a live mutation, never kubectl, never a force-push.
func printRollback(cfg rollbackConfig, w io.Writer) {
	level := cfg.level
	if level == "" {
		level = "all"
	}
	routesDir := cfg.routesDir
	if routesDir == "" {
		routesDir = defaultRoutesDir(cfg.service)
	}
	docsRepo := cfg.docsRepo
	if docsRepo == "" {
		docsRepo = "."
	}
	proseFile := cfg.proseFile
	if proseFile == "" {
		proseFile = "CLAUDE.md"
	}
	bead := cfg.bead
	if bead == "" {
		bead = "<cutover-bead>"
	}

	fmt.Fprintf(w, "SEAM rollback runbook — service %s (level: %s)\n", cfg.service, level)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Mechanism, everywhere: git revert in declarative-config — never a live mutation.")
	fmt.Fprintln(w, "ArgoCD selfHeal reverts live mutations, so an imperative rollback fights the controller and loses;")
	fmt.Fprintln(w, "forcing the ArgoCD Application sync is the sanctioned accelerator (it applies the repo, it does not bypass it).")
	fmt.Fprintln(w, "Never force-push the revert; reconcile with a merge commit if diverged.")
	fmt.Fprintf(w, "Fragment dir for this service (declarative-config): %s\n", routesDir)
	fmt.Fprintln(w)

	if level == "all" || level == "agent" {
		fmt.Fprintln(w, "── L1 — agent traffic ─────────────────────────────────────────────")
		fmt.Fprintln(w, "Scope: one service. Trigger: post-cutover errors/regression on this service from SEAM.")
		fmt.Fprintln(w, "Revert the prose-deletion commit in the agent docs — one commit, zero SEAM changes;")
		fmt.Fprintln(w, "SEAM sessions drain naturally and new sessions return to the incumbent.")
		fmt.Fprintf(w, "  git -C %s log --oneline --grep='%s' -- %s   # the deletion commit names the service and the bead\n", docsRepo, bead, proseFile)
		fmt.Fprintf(w, "  git -C %s show <commit> -- %s   # confirm it is the right one\n", docsRepo, proseFile)
		fmt.Fprintf(w, "  git -C %s revert <commit>\n", docsRepo)
		fmt.Fprintln(w, "If the prose was host-local and untracked, restore the copy kept in the cutover PR/bead.")
		fmt.Fprintln(w)
	}
	if level == "all" || level == "fragment" {
		fmt.Fprintln(w, "── L2 — fragment ──────────────────────────────────────────────────")
		fmt.Fprintln(w, "Scope: this service, all its callers. Trigger: the fragment misbehaves for every caller.")
		fmt.Fprintln(w, "Revert the fragment commit (or remove the fragment file) — hot-reload via ConfigMap,")
		fmt.Fprintln(w, "no pod restart, no gap; other services unaffected.")
		fmt.Fprintf(w, "  git -C declarative-config log --oneline -- %s\n", routesDir)
		fmt.Fprintln(w, "  git -C declarative-config revert <commit>")
		fmt.Fprintln(w, "Pair with L1 whenever the fragment is wrong rather than merely risky:")
		fmt.Fprintln(w, "revert the prose first, then the fragment.")
		fmt.Fprintln(w)
	}
	if level == "all" || level == "binary" {
		fmt.Fprintln(w, "── L3 — binary ────────────────────────────────────────────────────")
		fmt.Fprintln(w, "Scope: ALL services — the last resort; a single symptomatic service is usually L1/L2.")
		fmt.Fprintln(w, "Trigger: mechanical, not negotiable — /_seam/readyz still failing at 2 minutes after")
		fmt.Fprintln(w, "rollout start is a rollback, not a debugging session — or ready-but-wrong (corpus")
		fmt.Fprintln(w, "failure, error-rate step, p99 breach).")
		fmt.Fprintln(w, "Revert the image-digest commit in declarative-config, push, and force the ArgoCD")
		fmt.Fprintln(w, "Application sync if it lags.")
		fmt.Fprintln(w, "  git -C declarative-config log --oneline -- k8s/rs-manager/seam   # the image-digest commit")
		fmt.Fprintln(w, "  git -C declarative-config revert <commit>")
		fmt.Fprintln(w, "No state capture is needed first: every in-process store (guard counters, quota")
		fmt.Fprintln(w, "ledgers, breaker state, last-2xx, cache) is restart-scoped by construction.")
	}
}

func runRollback(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	fs.SetOutput(stderr)
	service := fs.String("service", "", "service owner token (required)")
	level := fs.String("level", "all", "agent | fragment | binary | all")
	routesDir := fs.String("routes-dir", "", "fragment dir in declarative-config (default derived from --service)")
	docsRepo := fs.String("docs-repo", "", "repo holding the agent-facing prose (default .)")
	proseFile := fs.String("prose-file", "", "agent-facing prose file (default CLAUDE.md)")
	bead := fs.String("bead", "", "cutover bead id, named by the L1 revert search")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *service == "" {
		fmt.Fprintln(stderr, "seam-cutover rollback requires --service")
		return 2
	}
	switch *level {
	case "agent", "fragment", "binary", "all":
	default:
		fmt.Fprintf(stderr, "invalid --level %q (want agent|fragment|binary|all)\n", *level)
		return 2
	}
	printRollback(rollbackConfig{
		service:   *service,
		level:     *level,
		routesDir: *routesDir,
		docsRepo:  *docsRepo,
		proseFile: *proseFile,
		bead:      *bead,
	}, stdout)
	return 0
}
