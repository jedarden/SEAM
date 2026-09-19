package gatewatch

// The frontier property, end to end: pending or unreachable CI never changes
// the claim frontier (seam-c14e2c07). One fixture drives the real checked-in
// watch loop, scripts/ci-gate-watch.sh, through a green -> red -> pending ->
// unreachable -> green sequence over child-1's stubbed cluster -- the real
// ci-gate.sh fed by the kubectl shim -- acting on a store that exists only as
// the bead shim's recorded calls and replay fixtures. After every pass the
// real .claude/hooks/seam-ci-claim-gate.py is run against claim-shaped
// commands, so the pin covers the whole enforcement stack and not just the
// watch:
//
//	green       -> close is a no-op (nothing to release), claims allowed
//	red         -> the gate bead is created, deferred and wired as a blocker
//	               of every ready bead, exactly once; claims refused
//	pending     -> the watch holds silently, the recorded store state is
//	               exactly what red left, claims allowed again (fail open)
//	unreachable -> same hold, same untouched store, claims allowed (fail open)
//	green       -> the gate bead is closed exactly once; claims allowed as
//	               before the red spell
//
// The watch half is this package's harness unchanged (gateFixture: kubectl
// shim, bead shim, throwaway origin repo). The hook half follows
// internal/claimgate's conventions -- the checked-in hook bytes run from a
// throwaway repo tree whose scripts/ci-gate.sh is the checked-in gate, the
// verdict cache redirected via SEAM_CI_GATE_CACHE, PreToolUse payloads in,
// decision JSON or silence out -- because Go test helpers cannot cross a
// package boundary, those conventions are mirrored here rather than
// imported. Nothing here re-tests what internal/claimgate already pins
// (payload shapes, cache TTLs, the command matcher, fail-open edges in
// isolation); it pins only what a verdict looks like after it has travelled
// the watch path, where the same real gate -- not a stub -- decided it.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// claimGate is the claimgate-convention hook fixture inside the watch
// fixture's throwaway repo: the checked-in hook bytes at .claude/hooks/,
// the checked-in gate reachable at scripts/ci-gate.sh (symlinked, never
// copied, so both the watch and the hook consult the same bytes), and a
// verdict cache the test resets between phases.
type claimGate struct {
	hook  string // path of the hook copy
	cache string // SEAM_CI_GATE_CACHE for every probe
	repo  string // the hook's cwd and REPO_ROOT
}

// stageClaimGate copies the checked-in hook into repo/.claude/hooks the way
// internal/claimgate's setupHook does, and symlinks the checked-in
// ci-gate.sh into repo/scripts, where the hook's own REPO_ROOT resolution
// looks for it. That is the whole point of running the hook here at all: the
// verdict it consumes is produced by the same real gate over the same
// shimmed cluster the watch just consumed, not by a stub switched by file.
func stageClaimGate(t *testing.T, repo string) claimGate {
	t.Helper()
	root := repoRoot(t)
	hookSrc, err := os.ReadFile(filepath.Join(root, ".claude", "hooks", "seam-ci-claim-gate.py"))
	if err != nil {
		t.Fatalf("read checked-in hook (the tests run it, not a copy in this package): %v", err)
	}
	hooksDir := filepath.Join(repo, ".claude", "hooks")
	scriptsDir := filepath.Join(repo, "scripts")
	for _, dir := range []string{hooksDir, scriptsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "scripts", "ci-gate.sh"),
		filepath.Join(scriptsDir, "ci-gate.sh")); err != nil {
		t.Fatalf("symlink the checked-in gate into the hook tree: %v", err)
	}
	g := claimGate{
		hook:  filepath.Join(hooksDir, "seam-ci-claim-gate.py"),
		cache: filepath.Join(repo, "verdict-cache.json"),
		repo:  repo,
	}
	if err := os.WriteFile(g.hook, hookSrc, 0o755); err != nil {
		t.Fatalf("write hook copy: %v", err)
	}
	return g
}

// hookEnv is the hermetic environment a probe runs under: scriptEnv's
// filters (no outer GIT_*/XDG_*, HOME inside the temp dir) plus a redirected
// verdict cache. The kubectl half is wired exactly as the watch's own runs
// wire it, so the hook consults the same real gate over the same shimmed
// cluster. The bead half is wired too, as a tripwire: the hook decides by
// pattern-matching the command string and must never exec bead, so if it
// ever did, the shim would record the call and the store-unchanged
// assertions in the surrounding test would fail.
func (f *gateFixture) hookEnv(cache string) []string {
	env := make([]string, 0, len(os.Environ())+12)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "XDG_") ||
			strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "SEAM_CI_GATE_STATE_DIR=") ||
			strings.HasPrefix(kv, "SEAM_CI_GATE_CACHE=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+f.scratch,
		"SEAM_CI_GATE_STATE_DIR="+f.state,
		"SEAM_CI_GATE_CACHE="+cache,
		"PATH="+f.shimPath(),
		"SEAM_CI_KUBECTL_SERVER="+fakeServer,
		"GATEWATCH_KUBECTL_PAYLOAD="+f.payload,
		"GATEWATCH_KUBECTL_CALLS="+f.calls,
		"GATEWATCH_KUBECTL_MODE="+f.mode,
		"GATEWATCH_BEAD_FIXTURES="+f.beadFixtures,
		"GATEWATCH_BEAD_CALLS="+f.beadLog,
	)
}

// hookVerdict is one probe's observable output, mirroring
// internal/claimgate's hookResult.
type hookVerdict struct {
	stdout string
	stderr string
	exit   int
}

// probe sends one claim-shaped Bash payload through the real hook and
// returns what it said. The verdict cache is removed first: the hook's
// CACHE_TTL is 60s and each watch pass here stands for a five-minute timer
// interval, so in production a verdict from before the pass would long since
// have expired by the time the next claim arrives; the TTL semantics
// themselves are internal/claimgate's pins (TestVerdictCaching), not this
// test's.
func (g claimGate) probe(t *testing.T, f *gateFixture, command string) hookVerdict {
	t.Helper()
	if err := os.Remove(g.cache); err != nil && !os.IsNotExist(err) {
		t.Fatalf("reset verdict cache: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": command},
	})
	if err != nil {
		t.Fatalf("marshal hook payload: %v", err)
	}
	cmd := exec.Command("python3", g.hook)
	cmd.Dir = g.repo
	cmd.Env = f.hookEnv(g.cache)
	cmd.Stdin = strings.NewReader(string(payload))
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run hook: %v\nstdout: %s\nstderr: %s", err, out.String(), errOut.String())
		}
		return hookVerdict{stdout: out.String(), stderr: errOut.String(), exit: exitErr.ExitCode()}
	}
	return hookVerdict{stdout: out.String(), stderr: errOut.String()}
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

// wantHookAllowedQuietly mirrors internal/claimgate's wantAllow and adds the
// half this end-to-end pin needs: a silent stderr. On a definitive green the
// hook allows without a note; any stderr here would mean the claim was
// released by the fail-open path instead -- exactly the confusion a pending
// or unreachable cluster must not be able to pass off as a green light.
func wantHookAllowedQuietly(t *testing.T, res hookVerdict) {
	t.Helper()
	if res.exit != 0 {
		t.Fatalf("hook exit %d, want 0\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if res.stdout != "" {
		t.Fatalf("expected a silent allow (empty stdout), got: %s", res.stdout)
	}
	if res.stderr != "" {
		t.Fatalf("expected a quiet allow (empty stderr) -- this was a fail-open, not a green light: %s", res.stderr)
	}
}

// wantHookAllowedFailOpen pins the unknown-verdict half of the contract: the
// claim goes through, but only via the fail-open path, whose note on stderr
// says so -- "allowed exactly as before the pass" must never be read as the
// gate having judged the run.
func wantHookAllowedFailOpen(t *testing.T, res hookVerdict, detailParts ...string) {
	t.Helper()
	if res.exit != 0 {
		t.Fatalf("hook exit %d, want 0\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if res.stdout != "" {
		t.Fatalf("expected a silent allow (empty stdout), got: %s", res.stdout)
	}
	for _, part := range append([]string{"fail open"}, detailParts...) {
		if !strings.Contains(res.stderr, part) {
			t.Errorf("allow note does not mention %q; stderr: %s", part, res.stderr)
		}
	}
}

// wantHookDenied mirrors internal/claimgate's wantDeny: the refusal is the
// decision JSON on stdout (never an exit code), and the reason must name the
// red gate and quote the verdict it judged.
func wantHookDenied(t *testing.T, res hookVerdict, reasonParts ...string) {
	t.Helper()
	if res.exit != 0 {
		t.Fatalf("hook exit %d, want 0 (a refusal is a stdout decision, not an exit code)\nstderr: %s",
			res.exit, res.stderr)
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

// probeClaims runs both claim shapes -- the bare claim and the --assignee
// form of update -- through the real hook, asserting each one's verdict. The
// label handed to assert is the hook's own short name for the shape (what
// its is_claim_command returns), which is what a deny reason quotes.
func (g claimGate) probeClaims(t *testing.T, f *gateFixture, assert func(t *testing.T, what string, res hookVerdict)) {
	t.Helper()
	for _, claim := range []struct{ cmd, label string }{
		{"bead claim", "bead claim"},
		{"bead update seam-abc --assignee some-worker", "bead update --assignee"},
	} {
		assert(t, claim.label, g.probe(t, f, claim.cmd))
	}
}

// TestGateWatchFrontierInvarianceForPendingAndUnreachableCI runs the whole
// sequence against one fixture. The store's recorded call log is the
// frontier's ground truth: it grows only when a pass actually mutates the
// store, so "the recorded store state is unchanged" is a byte-for-byte
// comparison of everything the bead shim was ever asked to do.
func TestGateWatchFrontierInvarianceForPendingAndUnreachableCI(t *testing.T) {
	f := newFixture(t)
	repo, rev := f.originRepo(t)
	gate := stageClaimGate(t, repo)
	watchLog := filepath.Join(f.state, "watch.log")

	// The store as the sequence starts: no gate bead anywhere, two ordinary
	// beads ready to claim -- the frontier a pending or unreachable cluster
	// must never change.
	f.beadListAll(t, nil)
	f.beadListReady(t, []beadRec{readyBead("ready-a"), readyBead("ready-b")})
	f.beadMutations(t, "gate-01")

	t.Run("green pass closes nothing and allows claims quietly", func(t *testing.T) {
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-frgrn1", "2026-09-18T10:00:00Z", rev, "Succeeded"),
		}})
		res := f.runWatch(t, repo)
		if res.exit != 0 {
			t.Fatalf("watch exited %d\nstderr: %s", res.exit, res.stderr)
		}
		// The close is attempted -- the watch cannot know the frontier was
		// never blocked -- but reaches the store only to find no gate bead.
		calls := f.beadCalls(t)
		if len(calls) != 1 || calls[0] != "list --limit 999999 --json" {
			t.Fatalf("a green close with no gate bead touched the store %d time(s), want exactly one list:\n%s",
				len(calls), strings.Join(calls, "\n"))
		}
		log := mustRead(t, watchLog)
		if !strings.Contains(log, "no gate bead to close") {
			t.Errorf("the green close did not report the missing gate bead:\n%s", log)
		}
		if !strings.Contains(log, "green: frontier release attempted (GATE green revision="+rev[:9]) {
			t.Errorf("watch.log does not record the release:\n%s", log)
		}
		gate.probeClaims(t, &f, func(t *testing.T, what string, res hookVerdict) {
			wantHookAllowedQuietly(t, res)
		})
	})

	t.Run("red pass opens the gate bead exactly once and refuses claims", func(t *testing.T) {
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-frred", "2026-09-18T10:10:00Z", rev, "Failed"),
		}})
		res := f.runWatch(t, repo)
		if res.exit != 0 {
			t.Fatalf("watch exited %d\nstderr: %s", res.exit, res.stderr)
		}
		calls := f.beadCalls(t)
		if n := countCalls(calls, "create"); n != 1 {
			t.Fatalf("the red pass issued %d create calls, want exactly 1:\n%s", n, strings.Join(calls, "\n"))
		}
		if !containsCall(calls, "update gate-01 --status deferred") {
			t.Errorf("the gate bead was never deferred out of the ready frontier:\n%s", strings.Join(calls, "\n"))
		}
		for _, ready := range []string{"ready-a", "ready-b"} {
			if !containsCall(calls, "dep add "+ready+" gate-01") {
				t.Errorf("%s never picked up the blocker edge:\n%s", ready, strings.Join(calls, "\n"))
			}
		}
		if n := countCalls(calls, "close gate-01"); n != 0 {
			t.Errorf("a red pass closed the gate bead:\n%s", strings.Join(calls, "\n"))
		}
		log := mustRead(t, watchLog)
		if !strings.Contains(log, "red: frontier blocked (GATE red revision="+rev[:9]) {
			t.Errorf("watch.log does not record the block:\n%s", log)
		}
		if !strings.Contains(log, "2 ready bead(s) newly blocked") {
			t.Errorf("watch.log does not record the frontier being emptied:\n%s", log)
		}
		gate.probeClaims(t, &f, func(t *testing.T, what string, res hookVerdict) {
			// The claimgate contract, holding through the watch path: the
			// refusal quotes the verdict the real gate produced, not a stub's.
			wantHookDenied(t, res, "RED", "phase=Failed", what, "refused")
		})

		// The store as the open left it: gate bead in place and doing its
		// job, frontier emptied by the blocker edges.
		f.beadListAll(t, []beadRec{gateBead("gate-01", "deferred")})
		f.beadShow(t, gateBead("gate-01", "deferred"))
		f.beadListReady(t, nil)
	})

	// From here on every hold pass is measured against what the red pass
	// left: the same recorded calls, the same watch.log -- unless the pass
	// is the unreachable one, whose hold is loud by design.
	storeAfterRed := f.beadCalls(t)
	logAfterRed := mustRead(t, watchLog)

	t.Run("pending pass holds silently and leaves the store untouched", func(t *testing.T) {
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-frpnd", "2026-09-18T10:20:00Z", rev, "Running"),
		}})
		res := f.runWatch(t, repo)
		if res.exit != 0 {
			t.Fatalf("watch exited %d\nstderr: %s", res.exit, res.stderr)
		}
		if calls := f.beadCalls(t); !slices.Equal(storeAfterRed, calls) {
			t.Fatalf("a pending pass changed the recorded store state:\nhave: %s\nwant: %s",
				strings.Join(calls, "\n"), strings.Join(storeAfterRed, "\n"))
		}
		if log := mustRead(t, watchLog); log != logAfterRed {
			t.Fatalf("a pending pass wrote to watch.log; the header promises a silent hold:\nhave: %s\nwant: %s",
				log, logAfterRed)
		}
		// The frontier is unchanged, so a claim is allowed exactly as it was
		// before the pass -- but through the fail-open path, with the note
		// saying the gate never judged the run.
		gate.probeClaims(t, &f, func(t *testing.T, what string, res hookVerdict) {
			wantHookAllowedFailOpen(t, res, "gate state unavailable", "exit 3")
		})
	})

	t.Run("unreachable pass holds loudly and leaves the store untouched", func(t *testing.T) {
		// A dead endpoint, not a missing repo: the revision still resolves,
		// and the verdict is still a hold -- never a block, never a release.
		f.failKubectl()
		res := f.runWatch(t, repo)
		if res.exit != 0 {
			t.Fatalf("watch exited %d\nstderr: %s", res.exit, res.stderr)
		}
		if calls := f.beadCalls(t); !slices.Equal(storeAfterRed, calls) {
			t.Fatalf("an unreachable pass changed the recorded store state:\nhave: %s\nwant: %s",
				strings.Join(calls, "\n"), strings.Join(storeAfterRed, "\n"))
		}
		// Unlike pending, an error is not normal, so the hold is loud: the
		// log grows by exactly its one line.
		log := mustRead(t, watchLog)
		before := strings.Split(strings.TrimRight(logAfterRed, "\n"), "\n")
		after := strings.Split(strings.TrimRight(log, "\n"), "\n")
		if len(after) != len(before)+1 {
			t.Fatalf("an unreachable pass wrote %d log lines, want exactly the one hold:\n%s",
				len(after)-len(before), log)
		}
		hold := after[len(after)-1]
		for _, part := range []string{"hold: ci-gate.sh exit 2", "iad-ci unreachable", "GATE error revision=" + rev[:9]} {
			if !strings.Contains(hold, part) {
				t.Errorf("the hold line does not mention %q: %s", part, hold)
			}
		}
		gate.probeClaims(t, &f, func(t *testing.T, what string, res hookVerdict) {
			wantHookAllowedFailOpen(t, res, "gate state unavailable", "exit 2", "iad-ci unreachable")
		})
	})

	t.Run("second green pass closes the gate bead exactly once", func(t *testing.T) {
		// replay resets the shim to replay mode, so the endpoint is
		// reachable again and the same green verdict closes what red opened.
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-frgrn2", "2026-09-18T10:30:00Z", rev, "Succeeded"),
		}})
		res := f.runWatch(t, repo)
		if res.exit != 0 {
			t.Fatalf("watch exited %d\nstderr: %s", res.exit, res.stderr)
		}
		calls := f.beadCalls(t)
		// Across the whole sequence: opened once, on red; closed once, only
		// on green; the pending and unreachable passes in between closed
		// nothing because they touched nothing.
		if n := countCalls(calls, "create"); n != 1 {
			t.Errorf("the sequence issued %d create calls, want exactly the red pass's 1:\n%s",
				n, strings.Join(calls, "\n"))
		}
		if n := countCalls(calls, "close gate-01"); n != 1 {
			t.Errorf("the sequence issued %d close calls, want exactly 1:\n%s", n, strings.Join(calls, "\n"))
		}
		if !containsCall(calls, "close gate-01 --reason "+gateCloseReason) {
			t.Errorf("the close does not carry the documented release reason:\n%s", strings.Join(calls, "\n"))
		}
		if !containsCall(calls, "update gate-01 --status open") {
			t.Errorf("the close never walked the gate bead out of deferred:\n%s", strings.Join(calls, "\n"))
		}
		if containsCall(calls, "reopen") {
			t.Errorf("the sequence reopened the gate bead:\n%s", strings.Join(calls, "\n"))
		}
		log := mustRead(t, watchLog)
		if !strings.Contains(log, "closed gate bead gate-01") {
			t.Errorf("watch.log does not record the release:\n%s", log)
		}
		if got := strings.Count(log, "green: frontier release attempted"); got != 2 {
			t.Errorf("watch.log records %d release attempts, want 2:\n%s", got, log)
		}
		// The frontier is back where the first green pass left it, so the
		// same claims are allowed exactly as they were before the red spell
		// -- quietly, on a definitive green, not via fail-open.
		gate.probeClaims(t, &f, func(t *testing.T, what string, res hookVerdict) {
			wantHookAllowedQuietly(t, res)
		})
		f.noLiveStore(t)
	})
}
