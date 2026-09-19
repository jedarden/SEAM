package gatewatch

// Tests for scripts/ci-gate-watch.sh's reconciliation mapping: the four-branch
// case in its header that turns a ci-gate.sh exit code into a bead-store
// action (0 -> close, 1 -> open, 3 -> hold, other -> hold).
//
// Both boundaries are stubbed. The verdict half is the real ci-gate.sh fed by
// the kubectl-shim fixtures (child-1's harness): a replayed workflow phase is
// what makes the gate green, red or pending, and a non-repo cwd is what makes
// it error. The action half is ci-gate-bead.sh replaced by a recording stub,
// so a pass's entire effect on the store is one visible line -- the mapping
// tests pin exactly which subcommand fired and how many times, nothing else.
//
// The watch resolves ci-gate.sh and ci-gate-bead.sh next to itself
// ($SCRIPT_DIR), not off PATH, so the stubbed runs stage a run dir: the
// checked-in watch and gate scripts symlinked in beside the stub. The idempotence
// tests need the opposite -- the real bead gate acting on a modeled store --
// so they run the checked-in scripts in place through the bead shim, exactly
// as TestGateWatchActsOnTheBeadStore does.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ciGateBeadStub stands in for scripts/ci-gate-bead.sh in the mapping tests.
// It appends its argv -- the reconciliation subcommand the watch chose -- one
// line per call, to the same GATEWATCH_BEAD_CALLS log the bead shim records
// to, and always exits 0. The shared log is safe because nothing else in a
// stubbed run can exec bead: the watch reaches the store only through
// ci-gate-bead.sh, and the stub is not the shim and never calls it -- so
// every line in the log is the stub's.
const ciGateBeadStub = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$GATEWATCH_BEAD_CALLS"
exit 0
`

// watchStubDir stages the directory the stubbed watch runs from: the
// checked-in ci-gate-watch.sh and ci-gate.sh symlinked in (never copies, so
// the tests always run the checked-in bytes), with the recording stub in
// ci-gate-bead.sh's place. SCRIPT_DIR is this dir, so the watch consults the
// real gate for its verdict and the stub for its action.
func (f *gateFixture) watchStubDir(t *testing.T) string {
	t.Helper()
	scripts := filepath.Join(repoRoot(t), "scripts")
	dir := filepath.Join(f.scratch, "watch-run")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir watch run dir: %v", err)
	}
	for _, name := range []string{"ci-gate-watch.sh", "ci-gate.sh"} {
		if err := os.Symlink(filepath.Join(scripts, name), filepath.Join(dir, name)); err != nil {
			t.Fatalf("symlink checked-in %s into the run dir: %v", name, err)
		}
	}
	stub := filepath.Join(dir, "ci-gate-bead.sh")
	if err := os.WriteFile(stub, []byte(ciGateBeadStub), 0o755); err != nil {
		t.Fatalf("write ci-gate-bead.sh stub: %v", err)
	}
	return dir
}

// runWatchStubbed runs the staged watch copy. The script comes from runDir
// (which fixes SCRIPT_DIR and therefore the stubbed bead gate) while cwd is
// where ci-gate.sh resolves the revision: a throwaway repo for a real
// verdict, the bare scratch dir for the unresolvable one.
func (f *gateFixture) runWatchStubbed(t *testing.T, runDir, cwd string) gateResult {
	t.Helper()
	return f.runScript(t, filepath.Join(runDir, "ci-gate-watch.sh"), cwd, f.shimPath())
}

// TestGateWatchReconciliationMapping pins the header's four-branch mapping
// with both boundaries stubbed: one green pass drives exactly one close, one
// red pass exactly one open, and the two hold branches -- pending and error
// -- drive no open or close at all while still refreshing last-check.txt.
// The subcommand counts are read off the stub's log; the run's exit is 0 in
// every branch, because a watch pass never fails its caller, whatever the
// gate said.
func TestGateWatchReconciliationMapping(t *testing.T) {
	t.Run("green drives exactly one close", func(t *testing.T) {
		f := newFixture(t)
		runDir := f.watchStubDir(t)
		repo, rev := f.originRepo(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-mapgrn", "2026-09-18T10:00:00Z", rev, "Succeeded"),
		}})

		res := f.runWatchStubbed(t, runDir, repo)
		if res.exit != 0 {
			t.Fatalf("watch exited %d\nstderr: %s", res.exit, res.stderr)
		}
		calls := f.beadCalls(t)
		if len(calls) != 1 || calls[0] != "close" {
			t.Fatalf("a green pass invoked ci-gate-bead.sh %d time(s), want exactly one close:\n%s",
				len(calls), strings.Join(calls, "\n"))
		}
		log := mustRead(t, filepath.Join(f.state, "watch.log"))
		if !strings.Contains(log, "green: frontier release attempted (GATE green revision="+rev[:9]) {
			t.Errorf("watch.log does not record the release:\n%s", log)
		}
		check := mustRead(t, filepath.Join(f.state, "last-check.txt"))
		if !strings.HasPrefix(check, "GATE green revision="+rev[:9]) {
			t.Errorf("last-check.txt does not carry the green verdict:\n%s", check)
		}
	})

	t.Run("red drives exactly one open", func(t *testing.T) {
		f := newFixture(t)
		runDir := f.watchStubDir(t)
		repo, rev := f.originRepo(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-mapred", "2026-09-18T10:00:00Z", rev, "Failed"),
		}})

		res := f.runWatchStubbed(t, runDir, repo)
		if res.exit != 0 {
			t.Fatalf("watch exited %d\nstderr: %s", res.exit, res.stderr)
		}
		calls := f.beadCalls(t)
		if len(calls) != 1 || calls[0] != "open" {
			t.Fatalf("a red pass invoked ci-gate-bead.sh %d time(s), want exactly one open:\n%s",
				len(calls), strings.Join(calls, "\n"))
		}
		log := mustRead(t, filepath.Join(f.state, "watch.log"))
		if !strings.Contains(log, "red: frontier blocked (GATE red revision="+rev[:9]) {
			t.Errorf("watch.log does not record the block:\n%s", log)
		}
		check := mustRead(t, filepath.Join(f.state, "last-check.txt"))
		if !strings.HasPrefix(check, "GATE red revision="+rev[:9]) {
			t.Errorf("last-check.txt does not carry the red verdict:\n%s", check)
		}
	})

	t.Run("pending drives no open or close and holds silently", func(t *testing.T) {
		// A run in flight is the normal state for most of the day, so the
		// pending branch is deliberately silent -- the header promises
		// "nothing to log on every pass". The refresh of last-check.txt is
		// the pass's only trace, which is what lets an operator read the
		// held verdict without the log filling with holds.
		f := newFixture(t)
		runDir := f.watchStubDir(t)
		repo, rev := f.originRepo(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-mappend", "2026-09-18T10:00:00Z", rev, "Running"),
		}})

		res := f.runWatchStubbed(t, runDir, repo)
		if res.exit != 0 {
			t.Fatalf("watch exited %d\nstderr: %s", res.exit, res.stderr)
		}
		if calls := f.beadCalls(t); len(calls) != 0 {
			t.Errorf("a pending pass invoked ci-gate-bead.sh:\n%s", strings.Join(calls, "\n"))
		}
		check := mustRead(t, filepath.Join(f.state, "last-check.txt"))
		if !strings.HasPrefix(check, "GATE pending revision="+rev[:9]) {
			t.Errorf("last-check.txt was not refreshed with the pending verdict:\n%s", check)
		}
		if _, err := os.Stat(filepath.Join(f.state, "watch.log")); err == nil {
			t.Errorf("a pending pass wrote to watch.log; the header promises a silent hold")
		}
	})

	t.Run("error drives no open or close and logs the hold", func(t *testing.T) {
		// A non-repo cwd: ci-gate.sh cannot resolve a revision and exits 2,
		// which is the never-block-or-release-on-a-dead-cluster branch. The
		// hold is loud here -- unlike pending, an error is not normal -- so
		// watch.log carries it while the store stays untouched.
		f := newFixture(t)
		runDir := f.watchStubDir(t)

		res := f.runWatchStubbed(t, runDir, f.scratch)
		if res.exit != 0 {
			t.Fatalf("watch exited %d\nstderr: %s", res.exit, res.stderr)
		}
		if calls := f.beadCalls(t); len(calls) != 0 {
			t.Errorf("an errored pass invoked ci-gate-bead.sh:\n%s", strings.Join(calls, "\n"))
		}
		check := mustRead(t, filepath.Join(f.state, "last-check.txt"))
		if !strings.HasPrefix(check, "GATE error revision=unknown") {
			t.Errorf("last-check.txt was not refreshed with the error verdict:\n%s", check)
		}
		log := mustRead(t, filepath.Join(f.state, "watch.log"))
		if !strings.Contains(log, "hold: ci-gate.sh exit 2 (GATE error revision=unknown") {
			t.Errorf("watch.log does not record the hold:\n%s", log)
		}
	})
}

// TestGateWatchIdempotentReconciliation pins what back-to-back passes on the
// same verdict do to the store. The watch itself keeps no cross-pass memory:
// it re-issues the same reconciliation every pass, and idempotence is
// delegated to ci-gate-bead.sh -- the unique-ref create, the leave-alone
// glance at a deferred bead, the already-closed no-op. So the pin needs the
// real bead gate, not the stub: two watch passes on the same verdict must
// mutate the store exactly once, the second pass only re-checking.
func TestGateWatchIdempotentReconciliation(t *testing.T) {
	t.Run("two red passes block the frontier exactly once", func(t *testing.T) {
		f := newFixture(t)
		repo, rev := f.originRepo(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-idemred", "2026-09-18T10:00:00Z", rev, "Failed"),
		}})
		f.beadListAll(t, nil)
		f.beadListReady(t, []beadRec{readyBead("ready-a")})
		f.beadMutations(t, "gate-01")

		first := f.runWatch(t, repo)
		if first.exit != 0 {
			t.Fatalf("first watch pass exited %d\nstderr: %s", first.exit, first.stderr)
		}

		// The store as the first pass left it: gate bead deferred and doing
		// its job, plus ready-b created by a worker who missed the gate. The
		// kubectl payload is unchanged -- the gate is still red.
		f.beadListAll(t, []beadRec{gateBead("gate-01", "deferred")})
		f.beadShow(t, gateBead("gate-01", "deferred"))
		f.beadListReady(t, []beadRec{readyBead("ready-a"), readyBead("ready-b")})

		second := f.runWatch(t, repo)
		if second.exit != 0 {
			t.Fatalf("second watch pass exited %d\nstderr: %s", second.exit, second.stderr)
		}

		calls := f.beadCalls(t)
		if n := countCalls(calls, "create"); n != 1 {
			t.Errorf("two red passes issued %d create calls, want exactly 1:\n%s", n, strings.Join(calls, "\n"))
		}
		if n := countCalls(calls, "update gate-01"); n != 1 {
			t.Errorf("two red passes issued %d defer updates, want exactly the first pass's 1:\n%s",
				n, strings.Join(calls, "\n"))
		}
		if containsCall(calls, "reopen") {
			t.Errorf("two red passes reopened the gate bead:\n%s", strings.Join(calls, "\n"))
		}
		if !containsCall(calls, "dep add ready-b gate-01") {
			t.Errorf("the bead created during the red spell never picked up the blocker edge:\n%s",
				strings.Join(calls, "\n"))
		}
		log := mustRead(t, filepath.Join(f.state, "watch.log"))
		if got := strings.Count(log, "red: frontier blocked"); got != 2 {
			t.Errorf("watch.log records %d blocked verdicts, want 2 (the pass re-attempts; the store does not):\n%s",
				got, log)
		}
		f.noLiveStore(t)
	})

	t.Run("two green passes release the frontier exactly once", func(t *testing.T) {
		f := newFixture(t)
		repo, rev := f.originRepo(t)
		f.replay(t, gateWorkflowList{Items: []gateWorkflow{
			seamRun("seam-ci-idemgrn", "2026-09-18T10:00:00Z", rev, "Succeeded"),
		}})
		f.beadListAll(t, []beadRec{gateBead("gate-01", "deferred")})
		f.beadShow(t, gateBead("gate-01", "deferred"))
		f.beadMutations(t, "gate-01")

		first := f.runWatch(t, repo)
		if first.exit != 0 {
			t.Fatalf("first watch pass exited %d\nstderr: %s", first.exit, first.stderr)
		}

		// The store as the first pass left it: the gate bead closed, so the
		// second pass's close has nothing left to do.
		f.beadListAll(t, []beadRec{gateBead("gate-01", "closed")})
		f.beadShow(t, gateBead("gate-01", "closed"))

		second := f.runWatch(t, repo)
		if second.exit != 0 {
			t.Fatalf("second watch pass exited %d\nstderr: %s", second.exit, second.stderr)
		}

		calls := f.beadCalls(t)
		if n := countCalls(calls, "close gate-01"); n != 1 {
			t.Errorf("two green passes issued %d close calls, want exactly 1:\n%s", n, strings.Join(calls, "\n"))
		}
		if n := countCalls(calls, "update gate-01"); n != 1 {
			// The first pass's deferred->open walk; a second would mean the
			// already-closed pass re-walked a bead that was done.
			t.Errorf("two green passes issued %d status updates, want exactly the first pass's 1:\n%s",
				n, strings.Join(calls, "\n"))
		}
		if containsCall(calls, "reopen") || containsCall(calls, "create") {
			t.Errorf("a green pass issued a red-gate action:\n%s", strings.Join(calls, "\n"))
		}
		log := mustRead(t, filepath.Join(f.state, "watch.log"))
		if got := strings.Count(log, "green: frontier release attempted"); got != 2 {
			t.Errorf("watch.log records %d release attempts, want 2:\n%s", got, log)
		}
		if strings.Contains(log, "close failed") {
			t.Errorf("the second pass reported a failed close instead of a no-op:\n%s", log)
		}
		f.noLiveStore(t)
	})
}

// TestGateWatchSecondPassLosesTheFlock pins the concurrency guard: while one
// pass holds watch.lock, a second pass must skip -- exit 0, no verdict check,
// no bead action, not even a last-check.txt refresh -- with only the skip
// line in watch.log. A util-linux flock holding the same file stands in for
// the first pass, which is the same lock the script takes.
func TestGateWatchSecondPassLosesTheFlock(t *testing.T) {
	f := newFixture(t)
	runDir := f.watchStubDir(t)

	lock := filepath.Join(f.state, "watch.lock")
	if err := os.MkdirAll(f.state, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	lf, err := os.OpenFile(lock, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("create watch.lock: %v", err)
	}
	lf.Close()

	// A blocking flock on the lock file: the stand-in for a watch pass that
	// is mid-flight when the second one starts. Killed by cleanup.
	helper := exec.Command("flock", lock, "-c", "sleep 30")
	if err := helper.Start(); err != nil {
		t.Fatalf("start lock helper: %v", err)
	}
	t.Cleanup(func() {
		_ = helper.Process.Kill()
		_ = helper.Wait()
	})

	// Wait until the lock is genuinely held: a nonblocking probe fails
	// exactly while the helper owns the file, and if it wins the race
	// instead it releases instantly and the helper, which blocks, takes it
	// next turn.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := exec.Command("flock", "-n", lock, "true").Run(); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock helper never acquired watch.lock")
		}
		time.Sleep(25 * time.Millisecond)
	}

	// The skip fires before the verdict check, so the cwd needs no
	// repository and kubectl is never reached.
	res := f.runWatchStubbed(t, runDir, f.scratch)
	if res.exit != 0 {
		t.Fatalf("the skipped pass exited %d, want 0\nstderr: %s", res.exit, res.stderr)
	}
	if calls := f.beadCalls(t); len(calls) != 0 {
		t.Errorf("a skipped pass invoked ci-gate-bead.sh:\n%s", strings.Join(calls, "\n"))
	}
	if _, err := os.Stat(filepath.Join(f.state, "last-check.txt")); !os.IsNotExist(err) {
		t.Errorf("a skipped pass refreshed last-check.txt; it must act not at all: %v", err)
	}
	log := mustRead(t, filepath.Join(f.state, "watch.log"))
	if !strings.Contains(log, "skip: another watch pass is already running") {
		t.Errorf("watch.log does not record the skip:\n%s", log)
	}
}
