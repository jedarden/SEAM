package gatewatch

// The tests here pin the red-gate failsafe on scripts/ci-gate-bead.sh close
// -- the guarantee scripts/ci-gate-watch.sh leans on when it maps green to
// close:
//
//	ci-gate-bead.sh close re-checks ci-gate.sh itself and refuses when it
//	does not report green, so a race between this script's check and its
//	close still fails safe.
//
// The watch loop decides from one verdict and acts seconds later; a push
// landing in between means the close's own check sees a different gate than
// the loop did. The failsafe is what keeps that race from releasing the
// frontier on a gate that just stopped being green. It is pinned here
// against every non-green verdict ci-gate.sh can report -- one red, the
// three pending shapes (a run in flight, a run queued, no completed run for
// the revision), and two error shapes (an unreachable cluster, an unparsable
// cluster response) -- each against a gate bead in both of the statuses
// close can find it in: plain open, and deferred, where the green path would
// walk the bead back to open before closing. The refusal must fire before
// the store is touched at all -- the re-check is the script's first act --
// so zero recorded bead calls is the pin. That subsumes "no close, no
// update, no reopen" and specifically the deferred-to-open walk, which must
// never start for a close that is about to refuse.
//
// One green control per status proves the failsafe is not a hair-trigger:
// the identical fixture with a Succeeded run for the resolved revision
// proceeds to close, walking a deferred bead and going straight to the close
// on an open one.
//
// Like the rest of the package, every run goes through the kubectl and bead
// shims with HOME and SEAM_CI_GATE_STATE_DIR redirected into the temp dir
// (scriptEnv), so neither the cluster, the live bead store, nor the watch's
// real state dir is reachable from the suite -- there is no case here, green
// or not, that can mutate anything outside the fixture. See bead
// seam-4fc93140.

import (
	"strings"
	"testing"
)

func TestCiGateBeadCloseRefusesEveryNonGreenVerdict(t *testing.T) {
	// arm installs one non-green gate verdict on the fixture. Every case
	// runs from the throwaway repo, so the revision resolves the same way
	// the real close resolves it and the verdict is the only variable.
	nonGreen := []struct {
		name string
		arm  func(t *testing.T, f *gateFixture, rev string)
	}{
		{
			name: "red: the latest run for this revision failed",
			arm: func(t *testing.T, f *gateFixture, rev string) {
				f.replay(t, gateWorkflowList{Items: []gateWorkflow{
					seamRun("seam-ci-fs-red", "2026-09-19T10:00:00Z", rev, "Failed"),
				}})
			},
		},
		{
			name: "pending: a run for the revision is in flight",
			arm: func(t *testing.T, f *gateFixture, rev string) {
				f.replay(t, gateWorkflowList{Items: []gateWorkflow{
					seamRun("seam-ci-fs-run", "2026-09-19T10:00:00Z", rev, "Running"),
				}})
			},
		},
		{
			name: "pending: a run for the revision is queued",
			arm: func(t *testing.T, f *gateFixture, rev string) {
				f.replay(t, gateWorkflowList{Items: []gateWorkflow{
					seamRun("seam-ci-fs-que", "2026-09-19T10:00:00Z", rev, "Pending"),
				}})
			},
		},
		{
			name: "pending: no completed run for the revision at all",
			arm: func(t *testing.T, f *gateFixture, _ string) {
				f.replay(t, gateWorkflowList{})
			},
		},
		{
			name: "error: the cluster endpoint is unreachable",
			arm: func(t *testing.T, f *gateFixture, _ string) {
				f.failKubectl()
			},
		},
		{
			name: "error: the cluster response cannot be parsed",
			arm: func(t *testing.T, f *gateFixture, _ string) {
				f.replay(t, []byte("<html>503 Service Unavailable</html>"))
			},
		},
	}

	for _, status := range []string{"open", "deferred"} {
		for _, tc := range nonGreen {
			t.Run(status+" gate bead, "+tc.name, func(t *testing.T) {
				f := newFixture(t)
				repo, rev := f.originRepo(t)
				tc.arm(t, &f, rev)
				// The store says there is a gate bead to release, in the
				// status under test, and every mutator is armed: if the
				// refusal so much as probes past its first act, the log
				// shows it.
				f.beadListAll(t, []beadRec{gateBead("gate-01", status)})
				f.beadShow(t, gateBead("gate-01", status))
				f.beadMutations(t, "gate-01")

				res := f.runBeadGateIn(t, repo, "close")
				if res.exit != 1 {
					t.Fatalf("close exited %d, want the refusal's 1\nstdout: %s\nstderr: %s",
						res.exit, res.stdout, res.stderr)
				}
				if !strings.Contains(res.stderr, "refusing to close the gate bead") {
					t.Errorf("refusal not explained on stderr:\n%s", res.stderr)
				}
				// The verdict ci-gate.sh actually saw is printed on close's
				// stdout on its way to the refusal -- and it must not be a
				// green one, here of all places.
				if strings.Contains(res.stdout, "GATE green") {
					t.Errorf("a refused close reported a green gate:\n%s", res.stdout)
				}
				// The failsafe fires before the store is reached: no close,
				// no update, no reopen -- for a deferred bead, no walk to
				// open either -- not one bead call of any kind.
				if calls := f.beadCalls(t); len(calls) != 0 {
					t.Errorf("a refused close still touched the store (%s gate bead):\n%s",
						status, strings.Join(calls, "\n"))
				}
				f.noLiveStore(t)
			})
		}

		t.Run(status+" gate bead, green control: the same close proceeds", func(t *testing.T) {
			// The over-refusal guard: store and cwd identical to the
			// refusal cases, only the verdict different. A failsafe that
			// refused here would hold the frontier shut forever.
			f := newFixture(t)
			repo, rev := f.originRepo(t)
			f.replay(t, gateWorkflowList{Items: []gateWorkflow{
				seamRun("seam-ci-fs-grn", "2026-09-19T10:00:00Z", rev, "Succeeded"),
			}})
			f.beadListAll(t, []beadRec{gateBead("gate-01", status)})
			f.beadShow(t, gateBead("gate-01", status))
			f.beadMutations(t, "gate-01")

			res := f.runBeadGateIn(t, repo, "close")
			if res.exit != 0 {
				t.Fatalf("green close exited %d -- the failsafe over-refuses\nstdout: %s\nstderr: %s",
					res.exit, res.stdout, res.stderr)
			}
			if !strings.Contains(res.stdout, "closed gate bead gate-01") {
				t.Errorf("green close does not report the release:\n%s", res.stdout)
			}
			calls := f.beadCalls(t)
			if !containsCall(calls, "close gate-01 --reason "+gateCloseReason) {
				t.Errorf("a green close never closed the gate bead:\n%s", strings.Join(calls, "\n"))
			}
			// The deferred-to-open walk is deferred-only: exactly one on a
			// deferred bead, none on an open one. These are the updates the
			// refusals above must never have started.
			wantWalk := map[string]int{"deferred": 1, "open": 0}[status]
			if got := countCalls(calls, "update gate-01 --status open"); got != wantWalk {
				t.Errorf("green close issued %d walk-to-open updates for a %s gate bead, want %d:\n%s",
					got, status, wantWalk, strings.Join(calls, "\n"))
			}
			f.noLiveStore(t)
		})
	}
}
