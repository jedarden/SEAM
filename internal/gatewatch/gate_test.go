// Package gatewatch holds regression tests for the seam-ci gate scripts,
// scripts/ci-gate.sh above all.
//
// ci-gate.sh answers one question -- "is SEAM's CI gate red?" -- with an
// exit code the whole enforcement stack maps to go/stop/hold/hold: the
// pre-commit backstop blocks on 1 only, the claim gate fails open on
// anything but 0 and 1, and the frontier watch loop holds on 2 and 3 (see
// AGENTS.md, "The seam-ci gate"). A verdict that drifts -- a Failed run
// read as green, a green run for some other revision gating this one --
// silently reopens the exact failure the gate exists to stop (25+ commits
// landed on a broken tree 2026-08-27..31), so the matrix is pinned here
// against fixtures.
//
// The tests run the real checked-in script and stub only its boundary:
// kubectl is a PATH shim replaying a fixture workflow list, and
// SEAM_CI_KUBECTL_SERVER points at a host nothing serves, so no cluster is
// ever contacted. --revision keeps the script off `git ls-remote`; the one
// case that exercises revision resolution builds a throwaway repo with a
// local bare origin inside the temp dir. Every run cwd's into a throwaway
// directory with GIT_* stripped, so neither the tests nor a developer's
// checkout contribute state.
//
// The same harness drives scripts/ci-gate-bead.sh and
// scripts/ci-gate-watch.sh, which act on the bead store rather than the
// cluster: there `bead` itself is the PATH shim, replaying per-subcommand
// fixture responses and recording every argv, so a test can pin exactly
// which store mutation a red or green gate performs. The real bead binary
// is never exec'd, and the runs redirect HOME and SEAM_CI_GATE_STATE_DIR
// into the temp dir too, so no live bead store or watch state is reachable
// from the suite.
//
// See beads seam-8b2a75e6 (kubectl shim) and seam-a7c2f29f (bead shim).
package gatewatch

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	// gateRevision is the revision every matrix fixture asks about. Long
	// enough that the verdict line's 9-char revision= prefix is distinct
	// from the whole sha, pinning the truncation.
	gateRevision = "a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d3e"
	revPrefix    = "a1b2c3d4e"
	// otherRevision is some other push's sha: runs for it must never
	// decide this one's verdict.
	otherRevision = "f9e8d7c6b5a49382716051423344556677889900"
	// fakeServer is what SEAM_CI_KUBECTL_SERVER points every run at: a
	// reserved host nothing serves, so any reach for a real cluster fails
	// loudly instead of succeeding quietly.
	fakeServer = "http://gatewatch.invalid:8001"
	// gateBeadTitle is the exact title scripts/ci-gate-bead.sh gives its
	// gate bead. The fixtures must spell it identically: gate_id() matches
	// on title equality, so a drift here reads as "no gate bead".
	gateBeadTitle = "GATE: seam-ci is red - do not claim SEAM beads"
	// gateBeadRef is the --unique-ref the script creates the bead with.
	gateBeadRef = "seam:ci-red-gate"
)

// kubectlShim stands in for kubectl. It appends its argv to the file named
// by GATEWATCH_KUBECTL_CALLS -- so a test can pin exactly which endpoint,
// namespace and label selector the gate queried -- and replays the payload
// file named by GATEWATCH_KUBECTL_PAYLOAD. GATEWATCH_KUBECTL_MODE=unreachable
// reproduces a dead endpoint instead. A missing payload file dies loudly
// (exit 91) rather than quietly reading as a verdict: a misconfigured
// fixture must never masquerade as cluster output. The real kubectl is
// never exec'd.
const kubectlShim = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$GATEWATCH_KUBECTL_CALLS"
case "${GATEWATCH_KUBECTL_MODE:-replay}" in
  unreachable)
    echo "kubectl: could not connect: dial gatewatch.invalid:8001: connection refused" >&2
    exit 1
    ;;
  replay)
    if [[ ! -r "$GATEWATCH_KUBECTL_PAYLOAD" ]]; then
      echo "gatewatch kubectl shim: payload $GATEWATCH_KUBECTL_PAYLOAD is not readable" >&2
      exit 91
    fi
    cat "$GATEWATCH_KUBECTL_PAYLOAD"
    ;;
  *)
    echo "gatewatch kubectl shim: unknown mode ${GATEWATCH_KUBECTL_MODE}" >&2
    exit 91
    ;;
esac
`

// beadShim stands in for bead, for the scripts that act on the bead store
// rather than the cluster (ci-gate-bead.sh, and ci-gate-watch.sh through
// it). Like kubectlShim it appends its argv, one line per call, to the file
// named by GATEWATCH_BEAD_CALLS -- which is how a test pins exactly which
// subcommand, id and flag the gate scripts issued -- and replays one
// fixture file per subcommand from the directory named by
// GATEWATCH_BEAD_FIXTURES:
//
//	list ...          -> list.jsonl       one JSON object per line: every bead
//	list --ready ...  -> list-ready.jsonl the ready frontier
//	show <id> --json  -> show.json        one-element JSON array
//	create            -> create.out       stdout: the new bead id
//	update/reopen/close/dep -> <sub>.out  mutators: empty output, exit 0
//
// A missing or unreadable fixture dies loudly (exit 91, like the kubectl
// shim) rather than quietly reading as an empty store or a successful
// mutation: a misconfigured fixture must never masquerade as store state,
// and with pipefail upstream a loud exit is what turns into a visible
// script failure instead of a silent no-op. An unknown subcommand is a
// fixture-shape bug and dies the same way -- so a future script subcommand
// can never fall through to the real binary. The real bead is never exec'd.
const beadShim = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$GATEWATCH_BEAD_CALLS"
if [[ $# -lt 1 ]]; then
  echo "gatewatch bead shim: no subcommand given" >&2
  exit 91
fi
case "$1" in
  list)
    fixture=list.jsonl
    for arg in "$@"; do
      if [[ "$arg" == "--ready" ]]; then
        fixture=list-ready.jsonl
      fi
    done
    ;;
  show)   fixture=show.json ;;
  create) fixture=create.out ;;
  update) fixture=update.out ;;
  reopen) fixture=reopen.out ;;
  close)  fixture=close.out ;;
  dep)    fixture=dep.out ;;
  *)
    echo "gatewatch bead shim: unknown subcommand $1" >&2
    exit 91
    ;;
esac
file="$GATEWATCH_BEAD_FIXTURES/$fixture"
if [[ ! -r "$file" ]]; then
  echo "gatewatch bead shim: fixture $file is not readable" >&2
  exit 91
fi
cat "$file"
`

// The fixture shapes mirror what `kubectl get workflows -o json` returns
// for seam-ci runs; only the fields ci-gate.sh's parser reads are spelled
// out, and JSON zero values stand in for everything else.

type gateParam struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type gateRunStatus struct {
	Phase string `json:"phase"`
}

type gateWorkflow struct {
	Metadata struct {
		Name              string `json:"name"`
		CreationTimestamp string `json:"creationTimestamp"`
	} `json:"metadata"`
	Spec struct {
		Arguments struct {
			Parameters []gateParam `json:"parameters,omitempty"`
		} `json:"arguments"`
	} `json:"spec"`
	Status *gateRunStatus `json:"status,omitempty"`
}

type gateWorkflowList struct {
	Items []gateWorkflow `json:"items"`
}

// seamRun builds one seam-ci workflow item the way the Argo Events sensor
// leaves them: the revision under spec.arguments.parameters and a phase
// under status -- the exact paths ci-gate.sh's parser walks.
func seamRun(name, created, revision, phase string) gateWorkflow {
	var wf gateWorkflow
	wf.Metadata.Name = name
	wf.Metadata.CreationTimestamp = created
	wf.Spec.Arguments.Parameters = []gateParam{{Name: "revision", Value: revision}}
	wf.Status = &gateRunStatus{Phase: phase}
	return wf
}

// verdict is one quarter of the matrix: the exit code and the first-line
// word every consumer maps to an action.
type verdict struct {
	word string // green | red | pending | error
	exit int
}

var (
	green    = verdict{"green", 0}
	red      = verdict{"red", 1}
	gateErr  = verdict{"error", 2}
	pending  = verdict{"pending", 3}
	verdicts = []verdict{green, red, gateErr, pending}
)

// gateFixture is one throwaway run environment: the checked-in scripts, a
// non-repo cwd, and a bin dir whose only residents are the kubectl and bead
// shims.
type gateFixture struct {
	gate         string // the checked-in ci-gate.sh under test
	beadGate     string // the checked-in ci-gate-bead.sh
	watch        string // the checked-in ci-gate-watch.sh
	scratch      string // cwd for every run: a temp dir outside any repo
	bin          string // holds the kubectl and bead shims; prepended to PATH
	emptyBin     string // PATH for the kubectl-missing case: a dir with nothing in it
	state        string // SEAM_CI_GATE_STATE_DIR for every bead-gate/watch run
	payload      string // file the kubectl shim replays
	calls        string // file the kubectl shim appends its argv to
	mode         string // kubectl shim mode: replay (default) or unreachable
	beadFixtures string // dir the bead shim replays per-subcommand files from
	beadLog      string // file the bead shim appends its argv to
}

// repoRoot anchors on this file's location so the checked-in script is found
// no matter which directory go test was invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

func newFixture(t *testing.T) gateFixture {
	t.Helper()
	root := repoRoot(t)
	gate := filepath.Join(root, "scripts", "ci-gate.sh")
	beadGate := filepath.Join(root, "scripts", "ci-gate-bead.sh")
	watch := filepath.Join(root, "scripts", "ci-gate-watch.sh")
	for _, script := range []string{gate, beadGate, watch} {
		if _, err := os.Stat(script); err != nil {
			t.Fatalf("stat checked-in script (the tests run it, not a copy in this package): %v", err)
		}
	}
	scratch := t.TempDir()
	f := gateFixture{
		gate:         gate,
		beadGate:     beadGate,
		watch:        watch,
		scratch:      scratch,
		bin:          filepath.Join(scratch, "bin"),
		emptyBin:     filepath.Join(scratch, "empty-bin"),
		state:        filepath.Join(scratch, "state"),
		payload:      filepath.Join(scratch, "kubectl-payload.json"),
		calls:        filepath.Join(scratch, "kubectl-calls.log"),
		beadFixtures: filepath.Join(scratch, "bead-fixtures"),
		beadLog:      filepath.Join(scratch, "bead-calls.log"),
	}
	if err := os.MkdirAll(f.bin, 0o755); err != nil {
		t.Fatalf("mkdir shim dir: %v", err)
	}
	if err := os.MkdirAll(f.emptyBin, 0o755); err != nil {
		t.Fatalf("mkdir empty dir: %v", err)
	}
	if err := os.MkdirAll(f.beadFixtures, 0o755); err != nil {
		t.Fatalf("mkdir bead fixture dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.bin, "kubectl"), []byte(kubectlShim), 0o755); err != nil {
		t.Fatalf("write kubectl shim: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.bin, "bead"), []byte(beadShim), 0o755); err != nil {
		t.Fatalf("write bead shim: %v", err)
	}
	return f
}

// replay points the shim at payload -- a fixture value to marshal, or a raw
// []byte for the malformed-output cases -- and has kubectl exit 0.
func (f *gateFixture) replay(t *testing.T, payload any) {
	t.Helper()
	f.mode = "replay"
	var data []byte
	switch p := payload.(type) {
	case []byte:
		data = p
	default:
		var err error
		if data, err = json.Marshal(payload); err != nil {
			t.Fatalf("marshal fixture payload: %v", err)
		}
	}
	if err := os.WriteFile(f.payload, data, 0o644); err != nil {
		t.Fatalf("write fixture payload: %v", err)
	}
}

// failKubectl has the shim die like a dead endpoint: message on stderr,
// exit 1, nothing on stdout.
func (f *gateFixture) failKubectl() {
	f.mode = "unreachable"
}

type gateResult struct {
	stdout string
	stderr string
	exit   int
}

// run executes the checked-in script with the shim first on PATH. It never
// fails on a nonzero exit -- the matrix IS the exit codes -- so each
// assertion decides what its case allows.
func (f *gateFixture) run(t *testing.T, args ...string) gateResult {
	t.Helper()
	return f.exec(t, f.bin+string(os.PathListSeparator)+os.Getenv("PATH"), args...)
}

// runWithoutKubectl is run with a PATH that has no kubectl on it at all --
// not even the shim, and not the developer's own binary -- pinning the
// script's guard for a missing kubectl. That guard fires before anything
// else needs the PATH, so an empty dir is the whole PATH.
func (f *gateFixture) runWithoutKubectl(t *testing.T, args ...string) gateResult {
	t.Helper()
	return f.exec(t, f.emptyBin, args...)
}

// runBeadGate runs scripts/ci-gate-bead.sh from the throwaway cwd.
func (f *gateFixture) runBeadGate(t *testing.T, args ...string) gateResult {
	t.Helper()
	return f.runBeadGateIn(t, f.scratch, args...)
}

// runBeadGateIn is runBeadGate with an explicit cwd: the close path and the
// watch loop resolve the revision from the cwd's repository, so those runs
// happen inside the throwaway repo instead of the bare scratch dir.
func (f *gateFixture) runBeadGateIn(t *testing.T, dir string, args ...string) gateResult {
	t.Helper()
	return f.runScript(t, f.beadGate, dir, f.shimPath(), args...)
}

// runWatch runs one pass of scripts/ci-gate-watch.sh: it consults
// ci-gate.sh, then acts on the bead store through the same shim, writing
// its log under the redirected state dir.
func (f *gateFixture) runWatch(t *testing.T, dir string) gateResult {
	t.Helper()
	return f.runScript(t, f.watch, dir, f.shimPath())
}

// shimPath is the PATH with both shim dirs first: the scripts resolve
// kubectl and bead to the shims and everything else (bash, python3, git) to
// the system.
func (f *gateFixture) shimPath() string {
	return f.bin + string(os.PathListSeparator) + os.Getenv("PATH")
}

func (f *gateFixture) exec(t *testing.T, path string, args ...string) gateResult {
	t.Helper()
	return f.runScript(t, f.gate, f.scratch, path, args...)
}

// scriptEnv is the hermetic environment every script run gets: no GIT_* from
// an outer session, no XDG_* redirecting config or cache elsewhere, HOME and
// SEAM_CI_GATE_STATE_DIR pointed into the temp dir, the shims' PATH, the
// kubectl half pointed at the fake server, and both shims' replay/recording
// variables wired. ci-gate.sh ignores the bead half and ci-gate-bead.sh
// reads the kubectl half only through ci-gate.sh; carrying both lets one
// runner serve every script.
func (f *gateFixture) scriptEnv(path string) []string {
	env := make([]string, 0, len(os.Environ())+10)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "XDG_") ||
			strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "SEAM_CI_GATE_STATE_DIR=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+f.scratch,
		"SEAM_CI_GATE_STATE_DIR="+f.state,
		"PATH="+path,
		"SEAM_CI_KUBECTL_SERVER="+fakeServer,
		"GATEWATCH_KUBECTL_PAYLOAD="+f.payload,
		"GATEWATCH_KUBECTL_CALLS="+f.calls,
		"GATEWATCH_KUBECTL_MODE="+f.mode,
		"GATEWATCH_BEAD_FIXTURES="+f.beadFixtures,
		"GATEWATCH_BEAD_CALLS="+f.beadLog,
	)
}

// runScript executes one of the checked-in scripts under scriptEnv's
// hermetic environment. It never fails on a nonzero exit -- verdicts and
// refusals ARE exit codes -- so each assertion decides what its case
// allows.
func (f *gateFixture) runScript(t *testing.T, script, dir, path string, args ...string) gateResult {
	t.Helper()
	env := f.scriptEnv(path)
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	res := gateResult{stdout: out.String(), stderr: errOut.String()}
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %s: %v\nstdout: %s\nstderr: %s", filepath.Base(script), err, res.stdout, res.stderr)
		}
		res.exit = exitErr.ExitCode()
	}
	return res
}

// kubectlCalls returns one line per shim invocation: the argv kubectl was
// given, which is how the query itself is pinned.
func (f *gateFixture) kubectlCalls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read kubectl call log: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// beadRec is the sliver of a bead record the gate scripts' python parsers
// read: id everywhere, title to recognize the gate bead, status on `show`.
// Everything else a real bead record carries is noise to these scripts and
// stays unrepresented.
type beadRec struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
}

// gateBead is the gate bead itself as the fixtures see it.
func gateBead(id, status string) beadRec {
	return beadRec{ID: id, Title: gateBeadTitle, Status: status}
}

// readyBead is an ordinary claimable bead: any title but the gate title,
// so the scripts count it against the frontier and wire a blocker onto it.
func readyBead(id string) beadRec {
	return beadRec{ID: id, Title: "ready work item " + id}
}

// writeBeadFixture drops one shim fixture file into place.
func (f *gateFixture) writeBeadFixture(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.beadFixtures, name), data, 0o644); err != nil {
		t.Fatalf("write bead fixture %s: %v", name, err)
	}
}

// beadJSONL marshals beads as the JSONL `bead list --json` prints.
func beadJSONL(t *testing.T, beads []beadRec) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, b := range beads {
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal bead %s: %v", b.ID, err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// beadListAll fills the fixture every plain `bead list --json` replays: the
// whole store, the list gate_id() scans for the gate bead.
func (f *gateFixture) beadListAll(t *testing.T, beads []beadRec) {
	t.Helper()
	f.writeBeadFixture(t, "list.jsonl", beadJSONL(t, beads))
}

// beadListReady fills the fixture every `bead list --ready --json` replays:
// the frontier the open path wires blockers onto.
func (f *gateFixture) beadListReady(t *testing.T, beads []beadRec) {
	t.Helper()
	f.writeBeadFixture(t, "list-ready.jsonl", beadJSONL(t, beads))
}

// beadShow fills the fixture every `bead show <id> --json` replays: a
// one-element array, the shape gate_status_value() parses.
func (f *gateFixture) beadShow(t *testing.T, b beadRec) {
	t.Helper()
	data, err := json.Marshal([]beadRec{b})
	if err != nil {
		t.Fatalf("marshal show fixture: %v", err)
	}
	f.writeBeadFixture(t, "show.json", data)
}

// beadMutations arms every mutator subcommand -- create's stdout (the new
// id) plus the four whose output the scripts discard -- so any of them can
// fire without the shim dying on a missing fixture. Whether they SHOULD
// fire is exactly what the recorded calls decide.
func (f *gateFixture) beadMutations(t *testing.T, newID string) {
	t.Helper()
	f.writeBeadFixture(t, "create.out", []byte(newID+"\n"))
	for _, name := range []string{"update.out", "reopen.out", "close.out", "dep.out"} {
		f.writeBeadFixture(t, name, nil)
	}
}

// beadCalls returns one line per shim invocation: the full argv the gate
// scripts handed to bead, which is how the store mutation itself is pinned.
func (f *gateFixture) beadCalls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.beadLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read bead call log: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// noLiveStore fails the test if anything created a bead store in the
// throwaway cwd. With `bead` shimmed the store is unreachable by
// construction; this is the tripwire that keeps the construction honest --
// if the real binary ever ran, it is the first thing it would leave behind.
func (f *gateFixture) noLiveStore(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.scratch, ".beads")); err == nil {
		t.Errorf("a .beads store appeared in the throwaway cwd: the real bead binary ran")
	}
}

// originRepo builds a throwaway git repository with a local bare origin and
// returns the repo dir plus main's sha: the minimum a `git ls-remote
// origin` inside ci-gate.sh needs to resolve the revision offline. Both
// live in the fixture's temp dir, and the commit carries its own identity,
// so no developer git config or remote is consulted.
func (f *gateFixture) originRepo(t *testing.T) (dir, revision string) {
	t.Helper()
	dir = filepath.Join(f.scratch, "repo")
	origin := filepath.Join(f.scratch, "origin.git")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "HOME="+f.scratch)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(f.scratch, "init", "--bare", "-q", "-b", "main", origin)
	run(f.scratch, "init", "-q", "-b", "main", dir)
	run(dir, "remote", "add", "origin", origin)
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("gatewatch revision anchor\n"), 0o644); err != nil {
		t.Fatalf("seed repo file: %v", err)
	}
	run(dir, "add", "seed.txt")
	run(dir, "-c", "user.name=gatewatch", "-c", "user.email=gatewatch@invalid", "commit", "-q", "-m", "seed")
	run(dir, "push", "-q", "origin", "main")
	return dir, run(dir, "rev-parse", "HEAD")
}

// want asserts the full verdict contract on one run: the exit code, a first
// line of the uniform `GATE <word> revision=<sha9> workflow=<name> (...)`
// shape, the word and the exit code agreeing (a green word on exit 1 is the
// false verdict this harness exists to catch), no opposite verdict anywhere
// in the output, and any extra substrings the caller pins.
func (f *gateFixture) want(t *testing.T, res gateResult, v verdict, lineParts ...string) {
	t.Helper()
	if res.exit != v.exit {
		t.Fatalf("exit %d, want %d (%s)\nstdout: %s\nstderr: %s", res.exit, v.exit, v.word, res.stdout, res.stderr)
	}
	lines := strings.Split(strings.TrimRight(res.stdout, "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("no verdict line on stdout\nstderr: %s", res.stderr)
	}
	line := lines[0]
	if !strings.HasPrefix(line, "GATE "+v.word+" ") {
		t.Fatalf("first line is not a %s verdict: %q", v.word, line)
	}
	// Every verdict line names the revision and the workflow -- even the
	// could-not-tell lines, which say workflow=none.
	for _, part := range []string{"revision=", "workflow="} {
		if !strings.Contains(line, part) {
			t.Errorf("verdict line does not name the %s: %q", strings.TrimSuffix(part, "="), line)
		}
	}
	for _, part := range lineParts {
		if !strings.Contains(line, part) {
			t.Errorf("verdict line does not mention %q: %q", part, line)
		}
	}
	// A green or a red must never appear anywhere a different verdict is
	// reported: a false green or a false red is the one unforgivable
	// output a gate can produce.
	for _, other := range verdicts {
		if other == v || (other.word != "green" && other.word != "red") {
			continue
		}
		if strings.Contains(res.stdout, "GATE "+other.word) {
			t.Errorf("a %s run also reported %s:\n%s", v.word, other.word, res.stdout)
		}
	}
}

// TestCiGateVerdictMatrix pins the exit matrix across all four CI states,
// from the real script with kubectl replaying fixtures: Succeeded is green,
// Failed is red, a run in flight -- or no completed run for this revision
// -- is pending, and the verdict comes from the latest run for the queried
// revision only.
func TestCiGateVerdictMatrix(t *testing.T) {
	t.Run("succeeded run for the revision is green", func(t *testing.T) {
		f := newFixture(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-grn01", "2026-09-18T10:00:00Z", gateRevision, "Succeeded"),
		}})
		f.want(t, f.run(t, "--revision", gateRevision), green,
			"revision="+revPrefix, "workflow=seam-ci-grn01", "phase=Succeeded")
	})

	t.Run("failed run for the revision is red", func(t *testing.T) {
		f := newFixture(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-red01", "2026-09-18T10:00:00Z", gateRevision, "Failed"),
		}})
		f.want(t, f.run(t, "--revision", gateRevision), red,
			"revision="+revPrefix, "workflow=seam-ci-red01", "phase=Failed")
	})

	t.Run("run in flight is pending", func(t *testing.T) {
		f := newFixture(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-run01", "2026-09-18T10:00:00Z", gateRevision, "Running"),
		}})
		f.want(t, f.run(t, "--revision", gateRevision), pending,
			"revision="+revPrefix, "workflow=seam-ci-run01", "phase=Running")
	})

	t.Run("queued run is pending", func(t *testing.T) {
		f := newFixture(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-que01", "2026-09-18T10:00:00Z", gateRevision, "Pending"),
		}})
		f.want(t, f.run(t, "--revision", gateRevision), pending,
			"revision="+revPrefix, "workflow=seam-ci-que01", "phase=Pending")
	})

	t.Run("no run for the revision is pending even when other revisions ran", func(t *testing.T) {
		f := newFixture(t)
		// A green run and a red run for some other push: neither is
		// evidence about this revision, so the gate must hold rather
		// than inherit either verdict. This is the no-false-green,
		// no-false-red pin at its sharpest.
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-othgrn", "2026-09-18T09:00:00Z", otherRevision, "Succeeded"),
			seamRun("seam-ci-othred", "2026-09-18T09:30:00Z", otherRevision, "Failed"),
		}})
		f.want(t, f.run(t, "--revision", gateRevision), pending,
			"revision="+revPrefix, "workflow=none")
	})

	t.Run("empty workflow list is pending", func(t *testing.T) {
		// An explicit empty items list -- what kubectl returns when no
		// seam-ci run exists at all.
		f := newFixture(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{}})
		f.want(t, f.run(t, "--revision", gateRevision), pending,
			"revision="+revPrefix, "workflow=none")
	})

	t.Run("a run with no revision parameter is never evidence", func(t *testing.T) {
		f := newFixture(t)
		// A seam-ci run whose arguments carry no revision says nothing
		// about this revision; treating it as evidence would let any
		// stray green decide the gate.
		wf := seamRun("seam-ci-norev", "2026-09-18T10:00:00Z", gateRevision, "Succeeded")
		wf.Spec.Arguments.Parameters = nil
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{wf}})
		f.want(t, f.run(t, "--revision", gateRevision), pending,
			"revision="+revPrefix, "workflow=none")
	})

	t.Run("latest run decides: newer failed over older succeeded is red", func(t *testing.T) {
		f := newFixture(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-oldgrn", "2026-09-18T09:00:00Z", gateRevision, "Succeeded"),
			seamRun("seam-ci-newred", "2026-09-18T10:00:00Z", gateRevision, "Failed"),
		}})
		f.want(t, f.run(t, "--revision", gateRevision), red,
			"revision="+revPrefix, "workflow=seam-ci-newred")
	})

	t.Run("latest run decides: newer succeeded over older failed is green", func(t *testing.T) {
		f := newFixture(t)
		// The mirror case: a fix that landed green must release the
		// frontier even though the previous run for this revision
		// failed -- stale reds block work forever.
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-oldred", "2026-09-18T09:00:00Z", gateRevision, "Failed"),
			seamRun("seam-ci-newgrn", "2026-09-18T10:00:00Z", gateRevision, "Succeeded"),
		}})
		f.want(t, f.run(t, "--revision", gateRevision), green,
			"revision="+revPrefix, "workflow=seam-ci-newgrn")
	})
}

// TestCiGateErrorsHold pins the other half of the matrix: every
// could-not-tell lands on exit 2, which every consumer treats as hold --
// never as a verdict. A mangled cluster response must not be able to green
// or red the gate.
func TestCiGateErrorsHold(t *testing.T) {
	cases := []struct {
		name  string
		give  any // what kubectl replays
		parts []string
	}{
		{
			name:  "unparsable json",
			give:  []byte("<html>503 Service Unavailable</html>"),
			parts: []string{"workflow=none"},
		},
		{
			name:  "items that is not a list",
			give:  []byte(`{"items": {"metadata": {"name": "seam-ci-shape"}}}`),
			parts: []string{"workflow=none"},
		},
		{
			name: "empty phase string",
			give: gateWorkflowList{Items: []gateWorkflow{
				seamRun("seam-ci-empt", "2026-09-18T10:00:00Z", gateRevision, ""),
			}},
			parts: []string{"workflow=seam-ci-empt"},
		},
		{
			name: "workflow with no status",
			give: func() any {
				wf := seamRun("seam-ci-nost", "2026-09-18T10:00:00Z", gateRevision, "Succeeded")
				wf.Status = nil
				return gateWorkflowList{Items: []gateWorkflow{wf}}
			}(),
			parts: []string{"workflow=seam-ci-nost"},
		},
		{
			name: "argo Error phase is never read as a red",
			give: gateWorkflowList{Items: []gateWorkflow{
				seamRun("seam-ci-err01", "2026-09-18T10:00:00Z", gateRevision, "Error"),
			}},
			parts: []string{"workflow=seam-ci-err01"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.replay(t, tc.give)
			f.want(t, f.run(t, "--revision", gateRevision), gateErr,
				append([]string{"revision=" + revPrefix}, tc.parts...)...)
		})
	}

	t.Run("unreachable cluster", func(t *testing.T) {
		f := newFixture(t)
		f.replay(t, gateWorkflowList{}) // payload present, but the shim dies first
		f.failKubectl()
		f.want(t, f.run(t, "--revision", gateRevision), gateErr,
			"revision="+revPrefix, "workflow=none")
	})

	t.Run("missing kubectl", func(t *testing.T) {
		f := newFixture(t)
		f.replay(t, gateWorkflowList{})
		f.want(t, f.runWithoutKubectl(t, "--revision", gateRevision), gateErr,
			"revision="+revPrefix, "workflow=none")
	})

	t.Run("revision that cannot be resolved", func(t *testing.T) {
		f := newFixture(t)
		// No --revision and a cwd that is not a repo: ls-remote and the
		// origin/main fallback both fail, so the gate holds instead of
		// guessing whose verdict to wear.
		f.want(t, f.run(t), gateErr, "revision=unknown", "workflow=none")
	})
}

// TestGateAsksKubectlForExactlyTheSeamCiRuns pins the query itself: the
// verdict must come from the endpoint SEAM_CI_KUBECTL_SERVER configures, in
// the argo-workflows namespace, selected by the seam-ci trigger label --
// drop that selector and any workflow on the cluster could decide the gate.
func TestGateAsksKubectlForExactlyTheSeamCiRuns(t *testing.T) {
	f := newFixture(t)
	f.replay(t, gateWorkflowList{Items: []gateWorkflow{
		seamRun("seam-ci-sel01", "2026-09-18T10:00:00Z", gateRevision, "Succeeded"),
	}})
	f.want(t, f.run(t, "--revision", gateRevision), green, "workflow=seam-ci-sel01")

	calls := f.kubectlCalls(t)
	if len(calls) != 1 {
		t.Fatalf("kubectl invoked %d times, want 1:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	argv := calls[0]
	for _, want := range []string{
		"--server=" + fakeServer, // the configured endpoint, not a hardcoded one
		"-n argo-workflows",
		"-l events.argoproj.io/trigger=seam-ci",
		"-o json",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("kubectl query does not contain %q: %s", want, argv)
		}
	}
}

// containsCall reports whether calls includes want as either an exact argv
// line or a prefix of one -- "update gate-01 --status open" matching
// "update gate-01 --status open" but not "update gate-011 ...".
func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want || strings.HasPrefix(c, want+" ") {
			return true
		}
	}
	return false
}

// countCalls counts the recorded calls that are exactly want or start with
// it -- the counting half of containsCall, for pins like "exactly one create
// across two runs".
func countCalls(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want || strings.HasPrefix(c, want+" ") {
			n++
		}
	}
	return n
}

// mustRead returns a file's contents, failing the test if it is not there.
func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestCiGateBeadStatusThroughBeadShim is the smoke test for the bead half
// of the harness: the real scripts/ci-gate-bead.sh status, end to end,
// against a fake store. A status is read-only, so the recorded calls are
// the pin -- exactly one store probe to find the gate bead, a probe plus a
// show for its status, and the ready list -- with every mutator armed in
// the fixtures so that a stray mutation would be visible in the log rather
// than fatal to the run.
func TestCiGateBeadStatusThroughBeadShim(t *testing.T) {
	f := newFixture(t)
	f.beadListAll(t, []beadRec{gateBead("gate-01", "deferred")})
	f.beadListReady(t, []beadRec{gateBead("gate-01", "deferred"), readyBead("ready-01")})
	f.beadShow(t, gateBead("gate-01", "deferred"))
	f.beadMutations(t, "gate-01")

	res := f.runBeadGate(t, "status")
	if res.exit != 0 {
		t.Fatalf("status exited %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	for _, want := range []string{
		// ci-gate.sh ran first through the kubectl shim and, in a non-repo
		// cwd, could not resolve a revision; status reports anyway.
		"GATE error revision=unknown",
		"gate bead: gate-01 status=deferred",
		"ready frontier: 1 non-gate bead(s)",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("status output does not contain %q:\n%s", want, res.stdout)
		}
	}

	want := []string{
		"list --limit 999999 --json", // find the gate bead
		"list --limit 999999 --json", // find it again for its status
		"show gate-01 --json",
		"list --ready --limit 999999 --json", // the frontier
	}
	got := f.beadCalls(t)
	if len(got) != len(want) {
		t.Fatalf("bead invoked %d times, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bead call %d = %q, want %q", i+1, got[i], want[i])
		}
	}
	f.noLiveStore(t)
}

// TestCiGateBeadOpenWiresTheFrontier pins what a red gate does to the store:
// create the gate bead with the exact title the frontier recognizes and the
// unique ref that makes re-runs idempotent, defer it out of the claimable
// frontier, and wire a blocker edge from every ready bead to it.
func TestCiGateBeadOpenWiresTheFrontier(t *testing.T) {
	f := newFixture(t)
	f.beadListAll(t, nil) // no gate bead yet
	f.beadListReady(t, []beadRec{readyBead("ready-a"), readyBead("ready-b")})
	f.beadMutations(t, "gate-01")

	res := f.runBeadGate(t, "open")
	if res.exit != 0 {
		t.Fatalf("open exited %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	for _, want := range []string{
		"created gate bead gate-01",
		"  blocked ready-a",
		"  blocked ready-b",
		"gate bead gate-01 open; 2 ready bead(s) newly blocked; 2 non-gate bead(s) still ready",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("open output does not contain %q:\n%s", want, res.stdout)
		}
	}

	calls := f.beadCalls(t)
	var created bool
	for _, c := range calls {
		if !strings.HasPrefix(c, "create ") {
			continue
		}
		created = true
		for _, part := range []string{
			"--title " + gateBeadTitle,
			"--unique-ref " + gateBeadRef, // the ref is what makes re-opens idempotent
			"--priority 0",
		} {
			if !strings.Contains(c, part) {
				t.Errorf("create call does not contain %q: %s", part, c)
			}
		}
	}
	if !created {
		t.Fatalf("open never created the gate bead:\n%s", strings.Join(calls, "\n"))
	}
	if !containsCall(calls, "update gate-01 --status deferred --notes") {
		t.Errorf("the gate bead was not deferred out of the frontier:\n%s", strings.Join(calls, "\n"))
	}
	for _, id := range []string{"ready-a", "ready-b"} {
		// Edge direction is the script's contract: dep add <blocked> <blocker>.
		if !containsCall(calls, "dep add "+id+" gate-01") {
			t.Errorf("no blocker edge wired onto %s:\n%s", id, strings.Join(calls, "\n"))
		}
	}
	if len(calls) != 10 {
		// list, create, then three status probes (the missing-status echo
		// and the defer check each re-run gate_id), update, list-ready,
		// dep x2, list-ready.
		t.Errorf("bead invoked %d times, want 10:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	f.noLiveStore(t)
}

// TestCiGateBeadOpenReopensAClosedGate pins the re-open half of open: an
// existing gate bead that a previous green cycle closed is reopened and
// re-deferred, with no second bead created and the edges re-checked.
func TestCiGateBeadOpenReopensAClosedGate(t *testing.T) {
	f := newFixture(t)
	f.beadListAll(t, []beadRec{gateBead("gate-01", "closed")})
	f.beadListReady(t, []beadRec{gateBead("gate-01", "closed")}) // only the gate bead itself
	f.beadShow(t, gateBead("gate-01", "closed"))
	f.beadMutations(t, "gate-01")

	res := f.runBeadGate(t, "open")
	if res.exit != 0 {
		t.Fatalf("open exited %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	for _, want := range []string{
		"reopened gate bead gate-01",
		"gate bead gate-01 open; 0 ready bead(s) newly blocked; 0 non-gate bead(s) still ready",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("open output does not contain %q:\n%s", want, res.stdout)
		}
	}

	calls := f.beadCalls(t)
	if !containsCall(calls, "reopen gate-01") {
		t.Errorf("closed gate bead was not reopened:\n%s", strings.Join(calls, "\n"))
	}
	if !containsCall(calls, "update gate-01 --status deferred --notes") {
		t.Errorf("reopened gate bead was not re-deferred:\n%s", strings.Join(calls, "\n"))
	}
	for _, banned := range []string{"create", "close"} {
		if containsCall(calls, banned) {
			t.Errorf("open of an existing gate bead also issued %q:\n%s", banned, strings.Join(calls, "\n"))
		}
	}
	f.noLiveStore(t)
}

// TestCiGateBeadOpenDefersAnExistingOpenGateBead pins the dangerous middle
// state: a gate bead that already exists and is still plain open. That is
// exactly the shape the defer exists to prevent -- the script's own comment
// records a plain open P0 being claimed by a fleet worker within seconds of
// creation -- so open must defer it on sight and wire the frontier, and
// above all must not create a second gate bead beside it. The gate bead also
// sits in the ready fixture here, because an open bead is by definition
// claimable: the run must skip it while wiring edges, never block itself.
func TestCiGateBeadOpenDefersAnExistingOpenGateBead(t *testing.T) {
	f := newFixture(t)
	f.beadListAll(t, []beadRec{gateBead("gate-01", "open")})
	f.beadListReady(t, []beadRec{gateBead("gate-01", "open"), readyBead("ready-a"), readyBead("ready-b")})
	f.beadShow(t, gateBead("gate-01", "open"))
	f.beadMutations(t, "gate-01")

	res := f.runBeadGate(t, "open")
	if res.exit != 0 {
		t.Fatalf("open exited %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	for _, banned := range []string{"created gate bead", "reopened gate bead"} {
		if strings.Contains(res.stdout, banned) {
			t.Errorf("open of an existing open gate bead reported %q:\n%s", banned, res.stdout)
		}
	}
	for _, want := range []string{
		"  blocked ready-a",
		"  blocked ready-b",
		"gate bead gate-01 open; 2 ready bead(s) newly blocked; 2 non-gate bead(s) still ready",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("open output does not contain %q:\n%s", want, res.stdout)
		}
	}

	want := []string{
		"list --limit 999999 --json", // gate_id: the bead is already there
		"list --limit 999999 --json", // status read for the case
		"show gate-01 --json",
		"list --limit 999999 --json", // status read for the defer check
		"show gate-01 --json",
		"update gate-01 --status deferred --notes", // the claimable P0 leaves the frontier
		"list --ready --limit 999999 --json",
		"dep add ready-a gate-01",
		"dep add ready-b gate-01",
		"list --ready --limit 999999 --json",
	}
	got := f.beadCalls(t)
	if len(got) != len(want) {
		t.Fatalf("bead invoked %d times, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("bead call %d = %q, want prefix %q", i+1, got[i], want[i])
		}
	}
	if containsCall(got, "dep add gate-01 gate-01") {
		t.Errorf("the gate bead was wired to block itself:\n%s", strings.Join(got, "\n"))
	}
	f.noLiveStore(t)
}

// TestCiGateBeadOpenLeavesADeferredGateBeadAlone pins the steady state a red
// gate spends most of its life in: the gate bead already deferred, doing its
// job. A re-run of open must then touch the bead's status not at all -- no
// update, no reopen, no close -- while still wiring blocker edges onto the
// beads created while the gate was red; that re-run is the header's own
// promise. "Left alone" is about the gate bead; wiring the frontier is the
// point of the run.
func TestCiGateBeadOpenLeavesADeferredGateBeadAlone(t *testing.T) {
	f := newFixture(t)
	f.beadListAll(t, []beadRec{gateBead("gate-01", "deferred")})
	// Deferred keeps the gate bead out of the ready frontier; the beads here
	// are work items created after the gate opened, edge-less until now.
	f.beadListReady(t, []beadRec{readyBead("ready-c"), readyBead("ready-d")})
	f.beadShow(t, gateBead("gate-01", "deferred"))
	f.beadMutations(t, "gate-01")

	res := f.runBeadGate(t, "open")
	if res.exit != 0 {
		t.Fatalf("open exited %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "gate bead gate-01 is deferred -- leaving status alone") {
		t.Errorf("the leave-alone decision is not reported:\n%s", res.stdout)
	}
	for _, want := range []string{
		"  blocked ready-c",
		"  blocked ready-d",
		"gate bead gate-01 open; 2 ready bead(s) newly blocked; 2 non-gate bead(s) still ready",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("open output does not contain %q:\n%s", want, res.stdout)
		}
	}

	calls := f.beadCalls(t)
	for _, banned := range []string{"create", "update", "reopen", "close"} {
		if containsCall(calls, banned) {
			t.Errorf("open of an already-deferred gate bead issued %q -- the bead was not left alone:\n%s",
				banned, strings.Join(calls, "\n"))
		}
	}
	if !containsCall(calls, "dep add ready-c gate-01") || !containsCall(calls, "dep add ready-d gate-01") {
		t.Errorf("beads created while the gate was red did not pick up the blocker edge:\n%s", strings.Join(calls, "\n"))
	}
	if len(calls) != 11 {
		// list, then three status reads (the case, the leave-alone echo, the
		// defer check -- each a list plus a show), list-ready, dep x2,
		// list-ready.
		t.Errorf("bead invoked %d times, want 11:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	f.noLiveStore(t)
}

// TestCiGateBeadOpenReRunCreatesNoSecondBead pins idempotence across
// invocations, which is how the script is actually used: open fires when the
// gate goes red and again whenever someone notices new beads need edges. The
// first run creates the gate bead; the second -- against a store that now
// contains it -- must create nothing, so a long red spell can never
// accumulate duplicate gate beads. The shim's static fixtures stand in for
// the store's after-state: the list fixture gains the bead the first run
// created, and a second ready bead appears to give the re-run something to
// wire.
func TestCiGateBeadOpenReRunCreatesNoSecondBead(t *testing.T) {
	f := newFixture(t)
	f.beadListAll(t, nil)
	f.beadListReady(t, []beadRec{readyBead("ready-a")})
	f.beadMutations(t, "gate-01")

	first := f.runBeadGate(t, "open")
	if first.exit != 0 {
		t.Fatalf("first open exited %d\nstdout: %s\nstderr: %s", first.exit, first.stdout, first.stderr)
	}

	// The store as the first open left it: gate bead present and deferred,
	// plus ready-b created by a worker who missed the gate going red.
	f.beadListAll(t, []beadRec{gateBead("gate-01", "deferred")})
	f.beadShow(t, gateBead("gate-01", "deferred"))
	f.beadListReady(t, []beadRec{readyBead("ready-a"), readyBead("ready-b")})

	second := f.runBeadGate(t, "open")
	if second.exit != 0 {
		t.Fatalf("second open exited %d\nstdout: %s\nstderr: %s", second.exit, second.stdout, second.stderr)
	}

	if got := strings.Count(first.stdout+second.stdout, "created gate bead"); got != 1 {
		t.Errorf("two opens reported %d creations, want exactly one:\n%s---\n%s", got, first.stdout, second.stdout)
	}
	calls := f.beadCalls(t)
	if n := countCalls(calls, "create"); n != 1 {
		t.Errorf("two opens issued %d create calls, want exactly 1:\n%s", n, strings.Join(calls, "\n"))
	}
	if n := countCalls(calls, "update gate-01"); n != 1 {
		// The first run's defer; a second would mean the re-run re-deferred
		// a bead that was already deferred.
		t.Errorf("two opens issued %d update calls, want exactly the first run's 1:\n%s", n, strings.Join(calls, "\n"))
	}
	for _, banned := range []string{"reopen", "close"} {
		if containsCall(calls, banned) {
			t.Errorf("two opens issued %q:\n%s", banned, strings.Join(calls, "\n"))
		}
	}
	if !containsCall(calls, "dep add ready-b gate-01") {
		t.Errorf("the bead created while the gate was red did not pick up the edge on the re-run:\n%s",
			strings.Join(calls, "\n"))
	}
	f.noLiveStore(t)
}

// TestCiGateBeadClose pins the green half of the cycle. close first
// re-checks ci-gate.sh itself -- the frontier is never released by hand on
// a gate the cluster does not vouch for -- then walks a deferred gate bead
// back to open (the only status close is reachable from) and closes it.
func TestCiGateBeadClose(t *testing.T) {
	t.Run("green closes a deferred gate bead", func(t *testing.T) {
		f := newFixture(t)
		repo, rev := f.originRepo(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-close01", "2026-09-18T10:00:00Z", rev, "Succeeded"),
		}})
		f.beadListAll(t, []beadRec{gateBead("gate-01", "deferred")})
		f.beadShow(t, gateBead("gate-01", "deferred"))
		f.beadMutations(t, "gate-01")

		res := f.runBeadGateIn(t, repo, "close")
		if res.exit != 0 {
			t.Fatalf("close exited %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
		}
		for _, want := range []string{
			"GATE green revision=" + rev[:9], // ci-gate.sh's own green, not assumed
			"closed gate bead gate-01",
		} {
			if !strings.Contains(res.stdout, want) {
				t.Errorf("close output does not contain %q:\n%s", want, res.stdout)
			}
		}
		calls := f.beadCalls(t)
		if !containsCall(calls, "update gate-01 --status open") {
			t.Errorf("deferred gate bead was not walked back to open before close:\n%s", strings.Join(calls, "\n"))
		}
		if !containsCall(calls, "close gate-01 --reason") {
			t.Errorf("gate bead was not closed with a reason:\n%s", strings.Join(calls, "\n"))
		}
		if len(calls) != 7 {
			// list, then two status probes (closed-check and deferred-check,
			// each a list plus a show), update, close.
			t.Errorf("bead invoked %d times, want 7:\n%s", len(calls), strings.Join(calls, "\n"))
		}
		f.noLiveStore(t)
	})

	t.Run("refuses to close while the gate is not green", func(t *testing.T) {
		f := newFixture(t)
		// Mutators armed and a store that says the gate is open -- but the
		// cwd is a non-repo, so ci-gate.sh cannot resolve a revision and
		// holds. A hold is not a green: the store must be untouched.
		f.beadListAll(t, []beadRec{gateBead("gate-01", "open")})
		f.beadShow(t, gateBead("gate-01", "open"))
		f.beadMutations(t, "gate-01")

		res := f.runBeadGate(t, "close")
		if res.exit != 1 {
			t.Fatalf("close exited %d, want 1\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
		}
		if !strings.Contains(res.stderr, "refusing to close the gate bead") {
			t.Errorf("refusal not explained on stderr:\n%s", res.stderr)
		}
		if calls := f.beadCalls(t); len(calls) != 0 {
			t.Errorf("a refused close still touched the store:\n%s", strings.Join(calls, "\n"))
		}
	})

	t.Run("already closed is a no-op", func(t *testing.T) {
		f := newFixture(t)
		repo, rev := f.originRepo(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-close02", "2026-09-18T10:00:00Z", rev, "Succeeded"),
		}})
		f.beadListAll(t, []beadRec{gateBead("gate-01", "closed")})
		f.beadShow(t, gateBead("gate-01", "closed"))
		f.beadMutations(t, "gate-01")

		res := f.runBeadGateIn(t, repo, "close")
		if res.exit != 0 {
			t.Fatalf("close exited %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
		}
		if !strings.Contains(res.stdout, "gate bead gate-01 already closed") {
			t.Errorf("idempotent close not reported:\n%s", res.stdout)
		}
		calls := f.beadCalls(t)
		if containsCall(calls, "close") || containsCall(calls, "update") {
			t.Errorf("an already-closed gate bead was mutated anyway:\n%s", strings.Join(calls, "\n"))
		}
	})
}

// TestBeadShimFailsLoudlyOnMisconfiguredFixture pins the shim's own
// contract, against the shim directly: a missing fixture is a harness bug
// and must exit 91 with the offending path on stderr -- never read as an
// empty store or a successful mutation -- and the failed call is still
// recorded, so the test that triggered it can be found.
func TestBeadShimFailsLoudlyOnMisconfiguredFixture(t *testing.T) {
	f := newFixture(t)
	runShim := func(args ...string) gateResult {
		t.Helper()
		cmd := exec.Command(filepath.Join(f.bin, "bead"), args...)
		cmd.Env = f.scriptEnv(f.shimPath())
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		res := gateResult{}
		if err := cmd.Run(); err != nil {
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run bead shim: %v", err)
			}
			res.exit = exitErr.ExitCode()
		}
		res.stdout, res.stderr = out.String(), errOut.String()
		return res
	}

	t.Run("missing list fixture", func(t *testing.T) {
		res := runShim("list", "--limit", "1", "--json")
		if res.exit != 91 {
			t.Fatalf("missing fixture exited %d, want 91\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
		}
		if !strings.Contains(res.stderr, "list.jsonl") || !strings.Contains(res.stderr, "not readable") {
			t.Errorf("stderr does not name the missing fixture:\n%s", res.stderr)
		}
		if res.stdout != "" {
			t.Errorf("a misconfigured fixture produced output:\n%s", res.stdout)
		}
		// The failure is on the record too: the call log is how a test
		// explains which invocation hit the missing fixture.
		if calls := f.beadCalls(t); len(calls) != 1 || calls[0] != "list --limit 1 --json" {
			t.Errorf("failed call not recorded:\n%v", calls)
		}
	})

	t.Run("missing show fixture", func(t *testing.T) {
		res := runShim("show", "gate-01", "--json")
		if res.exit != 91 || !strings.Contains(res.stderr, "show.json") {
			t.Fatalf("missing show fixture: exit %d, stderr:\n%s", res.exit, res.stderr)
		}
	})

	t.Run("unknown subcommand", func(t *testing.T) {
		res := runShim("rebless", "gate-01")
		if res.exit != 91 {
			t.Fatalf("unknown subcommand exited %d, want 91\nstderr: %s", res.exit, res.stderr)
		}
		if !strings.Contains(res.stderr, "unknown subcommand rebless") {
			t.Errorf("stderr does not name the subcommand:\n%s", res.stderr)
		}
	})

	t.Run("configured fixture replays and records", func(t *testing.T) {
		// The positive control for the loud failures above.
		f.beadListAll(t, []beadRec{readyBead("ready-01")})
		res := runShim("list", "--json")
		if res.exit != 0 {
			t.Fatalf("configured fixture exited %d\nstderr: %s", res.exit, res.stderr)
		}
		if !strings.Contains(res.stdout, `"id":"ready-01"`) {
			t.Errorf("fixture payload not replayed:\n%s", res.stdout)
		}
		if calls := f.beadCalls(t); len(calls) != 4 || calls[3] != "list --json" {
			t.Errorf("replayed call not recorded (want 3 prior + this one):\n%v", calls)
		}
	})
}

// TestGateWatchActsOnTheBeadStore drives one full pass of
// scripts/ci-gate-watch.sh -- the unattended loop that turns a verdict into
// a store action -- with the state dir redirected into the temp dir.
func TestGateWatchActsOnTheBeadStore(t *testing.T) {
	t.Run("red gate opens the frontier through the same shim", func(t *testing.T) {
		f := newFixture(t)
		repo, rev := f.originRepo(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-watchred", "2026-09-18T10:00:00Z", rev, "Failed"),
		}})
		f.beadListAll(t, nil)
		f.beadListReady(t, []beadRec{readyBead("ready-a"), readyBead("ready-b")})
		f.beadMutations(t, "gate-01")

		res := f.runWatch(t, repo)
		if res.exit != 0 {
			t.Fatalf("watch exited %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
		}
		// The watch wrote its log under the redirected state dir -- the
		// default ($HOME/.local/state/seam-ci-gate) was pointed into the
		// temp dir with HOME.
		check := mustRead(t, filepath.Join(f.state, "last-check.txt"))
		if !strings.HasPrefix(check, "GATE red revision="+rev[:9]) {
			t.Errorf("last-check.txt does not carry the red verdict:\n%s", check)
		}
		log := mustRead(t, filepath.Join(f.state, "watch.log"))
		if !strings.Contains(log, "red: frontier blocked") {
			t.Errorf("watch.log does not record the frontier block:\n%s", log)
		}
		calls := f.beadCalls(t)
		for _, want := range []string{
			"create", // opens the gate bead...
			"update gate-01 --status deferred --notes",
			"dep add ready-a gate-01", // ...and empties the frontier
			"dep add ready-b gate-01",
		} {
			if !containsCall(calls, want) {
				t.Errorf("red-gate watch pass did not issue %q:\n%s", want, strings.Join(calls, "\n"))
			}
		}
		f.noLiveStore(t)
	})

	t.Run("hold on error leaves the store alone", func(t *testing.T) {
		f := newFixture(t)
		f.beadMutations(t, "gate-01")

		// Non-repo cwd: ci-gate.sh cannot resolve a revision and exits 2,
		// which the watch maps to hold -- no open, no close, no bead calls.
		res := f.runWatch(t, f.scratch)
		if res.exit != 0 {
			t.Fatalf("watch exited %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
		}
		log := mustRead(t, filepath.Join(f.state, "watch.log"))
		if !strings.Contains(log, "hold: ci-gate.sh exit 2") {
			t.Errorf("watch.log does not record the hold:\n%s", log)
		}
		if calls := f.beadCalls(t); len(calls) != 0 {
			t.Errorf("a held pass still touched the bead store:\n%s", strings.Join(calls, "\n"))
		}
	})
}
