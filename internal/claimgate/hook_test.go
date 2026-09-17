// Package claimgate holds regression tests for the seam-ci claim gate hook
// (.claude/hooks/seam-ci-claim-gate.py, added in 6473a57).
//
// The hook is a repo-scoped Claude Code PreToolUse guard: while
// scripts/ci-gate.sh reports the latest seam-ci run on origin/main Failed,
// a Bash call that invokes a claim -- `bead claim`, or
// `bead update ... --assignee` -- must be refused; every other call must
// pass through untouched; and anything the gate cannot judge (a pending
// run, a cluster error, a timeout, unparsable input) must fail open.
// Verdicts are cached for CACHE_TTL seconds so parallel workers do not each
// pay the cluster round trip, and only definitive verdicts (green/red) may
// stick.
//
// These tests exercise the real checked-in hook end to end: python3 runs a
// copy of the script inside a throwaway repo tree whose scripts/ci-gate.sh
// is a stub whose verdict each test switches by file, so no cluster is
// needed. Every run redirects the verdict cache via SEAM_CI_GATE_CACHE so
// neither the tests nor a developer's real /tmp cache can leak state in.
//
// See AGENTS.md, "The seam-ci gate", and bead seam-3ddb424b.
package claimgate

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stubGate stands in for scripts/ci-gate.sh. The hook reads only the exit
// code (0 green, 1 red, anything else unknown) and the first output line
// (quoted back in the deny message), so the stub reproduces exactly that
// contract. It appends to gate-calls so tests can count how often the gate
// was actually consulted, and reads its verdict from gate-mode so a test
// can flip the gate between hook runs.
const stubGate = `#!/usr/bin/env bash
# Test stub for scripts/ci-gate.sh: verdict chosen by the gate-mode file.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
echo called >> "$here/gate-calls"
mode="$(cat "$here/gate-mode" 2>/dev/null || echo green)"
case "$mode" in
  green)      echo "GATE green revision=abc1234567 workflow=seam-ci-fake phase=Succeeded"; exit 0 ;;
  red)        echo "GATE red revision=abc1234567 workflow=seam-ci-fake phase=Failed"; exit 1 ;;
  red-silent) exit 1 ;;
  pending)    echo "GATE pending revision=abc1234567 workflow=none (no seam-ci run for this revision yet)"; exit 3 ;;
  error)      echo "GATE error revision=abc1234567 workflow=none (gate parser failed)"; exit 2 ;;
  slow)       sleep 30 ;;
esac
echo "GATE error revision=abc1234567 workflow=none (gate stub unhandled mode: $mode)"
exit 2
`

// hookFixture is one throwaway repo tree with the checked-in hook copied
// into .claude/hooks and the stub gate in scripts/, where the hook's own
// REPO_ROOT resolution expects them.
type hookFixture struct {
	repo      string // throwaway repo root (also the hook's cwd)
	hook      string // path of the hook copy
	gate      string // path of the stub ci-gate.sh
	cache     string // SEAM_CI_GATE_CACHE for every run in this fixture
	gateMode  string // file the stub gate reads its verdict from
	gateCalls string // file the stub gate appends to; counts consultations
}

// repoRoot anchors on this file's location so the checked-in hook is found
// no matter which directory go test was invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

func setupHook(t *testing.T) hookFixture {
	t.Helper()
	hookSrc, err := os.ReadFile(filepath.Join(repoRoot(t), ".claude", "hooks", "seam-ci-claim-gate.py"))
	if err != nil {
		t.Fatalf("read checked-in hook (the tests run it, not a copy in this package): %v", err)
	}
	repo := t.TempDir()
	scriptsDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(filepath.Join(repo, ".claude", "hooks"), 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts dir: %v", err)
	}
	f := hookFixture{
		repo:      repo,
		hook:      filepath.Join(repo, ".claude", "hooks", "seam-ci-claim-gate.py"),
		gate:      filepath.Join(scriptsDir, "ci-gate.sh"),
		cache:     filepath.Join(repo, "verdict-cache.json"),
		gateMode:  filepath.Join(scriptsDir, "gate-mode"),
		gateCalls: filepath.Join(scriptsDir, "gate-calls"),
	}
	if err := os.WriteFile(f.hook, hookSrc, 0o755); err != nil {
		t.Fatalf("write hook copy: %v", err)
	}
	if err := os.WriteFile(f.gate, []byte(stubGate), 0o755); err != nil {
		t.Fatalf("write stub gate: %v", err)
	}
	f.setGateMode(t, "green") // safe default; each test switches explicitly
	return f
}

func (f hookFixture) setGateMode(t *testing.T, mode string) {
	t.Helper()
	if err := os.WriteFile(f.gateMode, []byte(mode+"\n"), 0o644); err != nil {
		t.Fatalf("set gate mode %q: %v", mode, err)
	}
}

// gateCallCount reports how many times the hook actually consulted the gate
// script, which is how the caching behaviour is observed.
func (f hookFixture) gateCallCount(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(f.gateCalls)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read gate call log: %v", err)
	}
	return strings.Count(string(data), "\n")
}

// ageCache backdates the cached verdict's timestamp, standing in for a TTL
// that would otherwise take a minute of real time to expire.
func (f hookFixture) ageCache(t *testing.T, d time.Duration) {
	t.Helper()
	data, err := os.ReadFile(f.cache)
	if err != nil {
		t.Fatalf("read verdict cache: %v", err)
	}
	var cached map[string]any
	if err := json.Unmarshal(data, &cached); err != nil {
		t.Fatalf("parse verdict cache: %v", err)
	}
	ts, _ := cached["ts"].(float64)
	cached["ts"] = ts - d.Seconds()
	out, err := json.Marshal(cached)
	if err != nil {
		t.Fatalf("re-encode verdict cache: %v", err)
	}
	if err := os.WriteFile(f.cache, out, 0o644); err != nil {
		t.Fatalf("rewrite verdict cache: %v", err)
	}
}

type hookResult struct {
	stdout string
	stderr string
	exit   int
}

// run executes the hook copy with the given stdin and returns what it said.
// It never fails on a nonzero exit -- --check legitimately exits 1 on red --
// so each assertion decides what exit code its case allows.
func (f hookFixture) run(t *testing.T, stdin string, args ...string) hookResult {
	t.Helper()
	cmd := exec.Command("python3", append([]string{f.hook}, args...)...)
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "SEAM_CI_GATE_CACHE="+f.cache)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	res := hookResult{stdout: out.String(), stderr: errOut.String()}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			res.exit = exitErr.ExitCode()
		} else {
			t.Fatalf("run hook: %v\nstdout: %s\nstderr: %s", err, res.stdout, res.stderr)
		}
	}
	return res
}

func bashPayload(t *testing.T, command string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": command},
	})
	if err != nil {
		t.Fatalf("marshal hook payload: %v", err)
	}
	return string(payload)
}

// hookDecision mirrors the only output shape the hook uses to refuse
// something: a PreToolUse permissionDecision on stdout, exit code 0.
type hookDecision struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// wantDeny asserts the hook refused via the decision JSON on stdout, not an
// exit code, and that the reason names the red gate and quotes the stub
// workflow so the message stays actionable.
func (f hookFixture) wantDeny(t *testing.T, res hookResult, reasonParts ...string) {
	t.Helper()
	if res.exit != 0 {
		t.Fatalf("hook exit %d, want 0 (a refusal is a stdout decision, not an exit code)\nstderr: %s", res.exit, res.stderr)
	}
	if res.stdout == "" {
		t.Fatalf("expected a deny decision on stdout, got none\nstderr: %s", res.stderr)
	}
	var dec hookDecision
	if err := json.Unmarshal([]byte(res.stdout), &dec); err != nil {
		t.Fatalf("stdout is not a hook decision: %v\nstdout: %s", err, res.stdout)
	}
	out := dec.HookSpecificOutput
	if out.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q, want PreToolUse", out.HookEventName)
	}
	if out.PermissionDecision != "deny" {
		t.Errorf("permissionDecision = %q, want deny\nstdout: %s", out.PermissionDecision, res.stdout)
	}
	for _, part := range reasonParts {
		if !strings.Contains(out.PermissionDecisionReason, part) {
			t.Errorf("deny reason does not mention %q; reason: %s", part, out.PermissionDecisionReason)
		}
	}
}

// wantAllow asserts the hook let the call through: exit 0 and nothing on
// stdout. Allow-path notes (fail-open warnings) go to stderr only.
func (f hookFixture) wantAllow(t *testing.T, res hookResult) {
	t.Helper()
	if res.exit != 0 {
		t.Fatalf("hook exit %d, want 0\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if res.stdout != "" {
		t.Fatalf("expected a silent allow (empty stdout), got: %s", res.stdout)
	}
}

// TestRedGateBlocksClaimCommands pins the core contract: while ci-gate.sh
// reports red, anything that invokes a claim is refused -- either claim
// verb, the bf alias, env-assignment or `env` prefixes, the --assignee form
// of update with any flag order, and a claim buried mid-compound.
func TestRedGateBlocksClaimCommands(t *testing.T) {
	claims := []string{
		"bead claim",
		"bf claim",
		"env bead claim",
		"FOO=bar bead claim",
		"bead update seam-abc --assignee some-worker",
		"bead update seam-abc --assignee=some-worker",
		"bead update seam-abc --notes x --assignee some-worker",
		"bead show seam-abc && bead claim",
		"bead show seam-abc || bead claim",
		"bead show seam-abc; bead claim",
		"cat queue.txt | bead claim",
		"bead list --ready\nbead claim",
	}
	for _, cmd := range claims {
		t.Run(cmd, func(t *testing.T) {
			f := setupHook(t)
			f.setGateMode(t, "red")
			f.wantDeny(t, f.run(t, bashPayload(t, cmd)), "RED", "seam-ci-fake")
		})
	}
}

// TestRedGateAllowsNonClaimCommands pins the other half: on the same red
// gate everything that is not a claim passes through untouched -- including
// finishing in-flight work (show/close/update/release), and commands that
// merely mention "bead claim" without invoking it.
func TestRedGateAllowsNonClaimCommands(t *testing.T) {
	passThrough := []string{
		"bead show seam-abc",
		"bead list --ready",
		"bead close seam-abc --reason done",
		"bead update seam-abc --status in_progress",
		"bead release seam-abc",
		"git commit -m 'bead claim while gate red'",
		"grep -rn 'bead claim' docs/",
		"echo bead claim",
		"kubectl get workflows -n argo-workflows",
		"scripts/definition-of-done.sh --fast",
	}
	for _, cmd := range passThrough {
		t.Run(cmd, func(t *testing.T) {
			f := setupHook(t)
			f.setGateMode(t, "red")
			f.wantAllow(t, f.run(t, bashPayload(t, cmd)))
		})
	}

	t.Run("non-Bash tools are never gated", func(t *testing.T) {
		f := setupHook(t)
		f.setGateMode(t, "red")
		payload, err := json.Marshal(map[string]any{
			"tool_name":  "Write",
			"tool_input": map[string]any{"command": "bead claim"},
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		f.wantAllow(t, f.run(t, string(payload)))
	})

	t.Run("Bash call with no command is allowed", func(t *testing.T) {
		f := setupHook(t)
		f.setGateMode(t, "red")
		f.wantAllow(t, f.run(t, `{"tool_name":"Bash","tool_input":{}}`))
	})
}

// TestGreenGateAllowsClaims pins that the gate blocks only on red: the same
// claim commands go through on a green gate.
func TestGreenGateAllowsClaims(t *testing.T) {
	f := setupHook(t) // green default
	for _, cmd := range []string{"bead claim", "bead update seam-abc --assignee some-worker"} {
		f.wantAllow(t, f.run(t, bashPayload(t, cmd)))
	}
}

// TestFailOpenWhenGateCannotJudge pins the fail-open contract: a pending
// run (exit 3), a gate error (exit 2), a missing or non-executable gate
// script, unparsable stdin and a payload with no command all allow, with
// the fail-open note on stderr. A could-not-tell must never be cached as a
// verdict.
func TestFailOpenWhenGateCannotJudge(t *testing.T) {
	for _, tc := range []struct{ name, mode string }{
		{"pending run", "pending"},
		{"gate error", "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupHook(t)
			f.setGateMode(t, tc.mode)
			res := f.run(t, bashPayload(t, "bead claim"))
			f.wantAllow(t, res)
			if !strings.Contains(res.stderr, "fail open") {
				t.Errorf("allow note does not say fail open: %q", res.stderr)
			}
			if got := f.gateCallCount(t); got != 1 {
				t.Errorf("gate consulted %d times after one call, want 1", got)
			}
		})
	}

	t.Run("gate script missing", func(t *testing.T) {
		f := setupHook(t)
		if err := os.Remove(f.gate); err != nil {
			t.Fatalf("remove stub gate: %v", err)
		}
		res := f.run(t, bashPayload(t, "bead claim"))
		f.wantAllow(t, res)
		if !strings.Contains(res.stderr, "fail open") {
			t.Errorf("allow note does not say fail open: %q", res.stderr)
		}
	})

	t.Run("gate script not executable", func(t *testing.T) {
		f := setupHook(t)
		if err := os.Chmod(f.gate, 0o644); err != nil {
			t.Fatalf("chmod stub gate: %v", err)
		}
		f.wantAllow(t, f.run(t, bashPayload(t, "bead claim")))
	})

	t.Run("unparsable stdin", func(t *testing.T) {
		f := setupHook(t)
		res := f.run(t, "this is not json")
		f.wantAllow(t, res)
		if !strings.Contains(res.stderr, "unparseable input") {
			t.Errorf("allow note does not mention unparsable input: %q", res.stderr)
		}
	})
}

// TestRedVerdictComesFromTheExitCode pins that the verdict is the gate's
// exit code: a red exit (1) with no usable text still refuses, because the
// first output line is detail for the message, never the verdict itself.
func TestRedVerdictComesFromTheExitCode(t *testing.T) {
	f := setupHook(t)
	f.setGateMode(t, "red-silent")
	f.wantDeny(t, f.run(t, bashPayload(t, "bead claim")), "RED")
}

// TestVerdictCaching pins the cache contract: definitive verdicts are
// reused within the TTL (parallel workers do not each pay the round trip),
// a cached red keeps blocking even if the gate has since gone green inside
// the TTL window, an unknown verdict never sticks, expiry re-consults the
// gate, and a corrupt cache is a miss, not a verdict.
func TestVerdictCaching(t *testing.T) {
	t.Run("red verdict is reused within the ttl", func(t *testing.T) {
		f := setupHook(t)
		f.setGateMode(t, "red")
		f.wantDeny(t, f.run(t, bashPayload(t, "bead claim")), "RED")
		f.wantDeny(t, f.run(t, bashPayload(t, "bead claim")), "RED")
		if got := f.gateCallCount(t); got != 1 {
			t.Fatalf("gate consulted %d times, want 1 (second claim must hit the cache)", got)
		}
	})

	t.Run("cached red survives a gate flipped green inside the ttl", func(t *testing.T) {
		f := setupHook(t)
		f.setGateMode(t, "red")
		f.wantDeny(t, f.run(t, bashPayload(t, "bead claim")), "RED")
		f.setGateMode(t, "green")
		f.wantDeny(t, f.run(t, bashPayload(t, "bead claim")), "RED")
		if got := f.gateCallCount(t); got != 1 {
			t.Fatalf("gate consulted %d times, want 1 (verdict must come from cache)", got)
		}
	})

	t.Run("unknown verdict is never cached", func(t *testing.T) {
		f := setupHook(t)
		f.setGateMode(t, "error")
		f.wantAllow(t, f.run(t, bashPayload(t, "bead claim")))
		f.wantAllow(t, f.run(t, bashPayload(t, "bead claim")))
		if got := f.gateCallCount(t); got != 2 {
			t.Fatalf("gate consulted %d times, want 2 (a could-not-tell must not stick)", got)
		}
	})

	t.Run("cache expiry re-consults the gate", func(t *testing.T) {
		f := setupHook(t)
		f.setGateMode(t, "red")
		f.wantDeny(t, f.run(t, bashPayload(t, "bead claim")), "RED")
		f.ageCache(t, 61*time.Second) // CACHE_TTL is 60s
		f.setGateMode(t, "green")
		f.wantAllow(t, f.run(t, bashPayload(t, "bead claim")))
		if got := f.gateCallCount(t); got != 2 {
			t.Fatalf("gate consulted %d times, want 2 (expired cache must be re-queried)", got)
		}
	})

	t.Run("corrupt cache re-consults the gate", func(t *testing.T) {
		f := setupHook(t)
		f.setGateMode(t, "red")
		f.wantDeny(t, f.run(t, bashPayload(t, "bead claim")), "RED")
		if err := os.WriteFile(f.cache, []byte("{not json"), 0o644); err != nil {
			t.Fatalf("corrupt verdict cache: %v", err)
		}
		f.wantDeny(t, f.run(t, bashPayload(t, "bead claim")), "RED")
		if got := f.gateCallCount(t); got != 2 {
			t.Fatalf("gate consulted %d times, want 2 (corrupt cache must be a miss)", got)
		}
	})
}

// TestCheckModeReportsGateState pins the --check contract for humans and
// workers consulting the gate without attempting a claim: state on stdout,
// exit 1 exactly when red.
func TestCheckModeReportsGateState(t *testing.T) {
	t.Run("red exits 1", func(t *testing.T) {
		f := setupHook(t)
		f.setGateMode(t, "red")
		res := f.run(t, "", "--check")
		if res.exit != 1 {
			t.Fatalf("--check exit %d, want 1 on red\nstdout: %s", res.exit, res.stdout)
		}
		if !strings.Contains(res.stdout, "RED") {
			t.Errorf("--check output does not name the state: %q", res.stdout)
		}
	})

	t.Run("green exits 0", func(t *testing.T) {
		f := setupHook(t)
		res := f.run(t, "", "--check")
		if res.exit != 0 {
			t.Fatalf("--check exit %d, want 0 on green\nstdout: %s", res.exit, res.stdout)
		}
		if !strings.Contains(res.stdout, "GREEN") {
			t.Errorf("--check output does not name the state: %q", res.stdout)
		}
	})

	t.Run("unjudgeable exits 0", func(t *testing.T) {
		f := setupHook(t)
		f.setGateMode(t, "error")
		res := f.run(t, "", "--check")
		if res.exit != 0 {
			t.Fatalf("--check exit %d, want 0 when the gate cannot judge\nstdout: %s", res.exit, res.stdout)
		}
		if !strings.Contains(res.stdout, "UNKNOWN") {
			t.Errorf("--check output does not name the state: %q", res.stdout)
		}
	})
}

// TestGateTimeoutFailsOpen pins the last fail-open path: a gate that hangs
// past the hook's own timeout is a could-not-tell, so the claim is allowed.
// It costs one real GATE_TIMEOUT (12s), so it skips under -short.
func TestGateTimeoutFailsOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the hook's 12s gate timeout")
	}
	f := setupHook(t)
	f.setGateMode(t, "slow")
	res := f.run(t, bashPayload(t, "bead claim"))
	f.wantAllow(t, res)
	if !strings.Contains(res.stderr, "fail open") {
		t.Errorf("allow note does not say fail open: %q", res.stderr)
	}
}
