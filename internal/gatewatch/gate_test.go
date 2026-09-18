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
// case that exercises revision resolution runs in a directory that is not a
// repo. Every run cwd's into a throwaway directory with GIT_* stripped, so
// neither the tests nor a developer's checkout contribute state.
//
// See bead seam-8b2a75e6.
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

// gateFixture is one throwaway run environment: the checked-in script, a
// non-repo cwd, and a bin dir whose only resident is the kubectl shim.
type gateFixture struct {
	gate     string // the checked-in script under test
	scratch  string // cwd for every run: a temp dir outside any repo
	bin      string // holds the kubectl shim; prepended to PATH
	emptyBin string // PATH for the kubectl-missing case: a dir with nothing in it
	payload  string // file the shim replays
	calls    string // file the shim appends its argv to
	mode     string // shim mode: replay (default) or unreachable
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
	gate := filepath.Join(repoRoot(t), "scripts", "ci-gate.sh")
	if _, err := os.Stat(gate); err != nil {
		t.Fatalf("stat checked-in gate (the tests run it, not a copy in this package): %v", err)
	}
	scratch := t.TempDir()
	f := gateFixture{
		gate:     gate,
		scratch:  scratch,
		bin:      filepath.Join(scratch, "bin"),
		emptyBin: filepath.Join(scratch, "empty-bin"),
		payload:  filepath.Join(scratch, "kubectl-payload.json"),
		calls:    filepath.Join(scratch, "kubectl-calls.log"),
	}
	if err := os.MkdirAll(f.bin, 0o755); err != nil {
		t.Fatalf("mkdir shim dir: %v", err)
	}
	if err := os.MkdirAll(f.emptyBin, 0o755); err != nil {
		t.Fatalf("mkdir empty dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.bin, "kubectl"), []byte(kubectlShim), 0o755); err != nil {
		t.Fatalf("write kubectl shim: %v", err)
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

func (f *gateFixture) exec(t *testing.T, path string, args ...string) gateResult {
	t.Helper()
	cmd := exec.Command("bash", append([]string{f.gate}, args...)...)
	cmd.Dir = f.scratch
	cmd.Env = append(envWithoutGit(),
		"PATH="+path,
		"SEAM_CI_KUBECTL_SERVER="+fakeServer,
		"GATEWATCH_KUBECTL_PAYLOAD="+f.payload,
		"GATEWATCH_KUBECTL_CALLS="+f.calls,
		"GATEWATCH_KUBECTL_MODE="+f.mode,
	)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	res := gateResult{stdout: out.String(), stderr: errOut.String()}
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run gate: %v\nstdout: %s\nstderr: %s", err, res.stdout, res.stderr)
		}
		res.exit = exitErr.ExitCode()
	}
	return res
}

// envWithoutGit drops GIT_* from the environment: the harness's throwaway
// cwd must stay a non-repo even if an outer session exports GIT_DIR.
func envWithoutGit() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	return env
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
