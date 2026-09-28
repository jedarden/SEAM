package main

// The input contract: what a VictoriaMetrics series must carry before it can
// become a deprecation candidate. The output contract (output_contract_test.go)
// pins one structured record and one counter per candidate; these tests pin
// the other half of that sentence — a duplicate series never becomes a second
// candidate, and a malformed series never becomes a candidate at all.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// serveVM starts an evaluator whose VictoriaMetrics stub answers every query
// with the given raw series JSON, exactly as the wire carries it.
func serveVM(t *testing.T, series string) *RetirementEvaluator {
	t.Helper()

	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[%s]}}`, series)
	}))
	t.Cleanup(vm.Close)

	return testEvaluator(t, vm.URL)
}

func detectionRecords(t *testing.T, observed *observer.ObservedLogs) []observer.LoggedEntry {
	t.Helper()
	return observed.FilterMessage("Deprecation candidate detected").All()
}

// TestDuplicateSeriesProduceOneCandidate drives an evaluation whose response
// reports one route version as two series (differing only in a label the
// parser ignores — the shape a future extra counter label would produce) and
// pins that the route version is still one candidate: one record, one series
// counting 1, and one route version considered.
func TestDuplicateSeriesProduceOneCandidate(t *testing.T) {
	observed := observingLogger(t)
	evaluator := serveVM(t, `{"metric":{"route":"/users","spec_version":"abc123","shard":"a"},"value":[1757000000,"0"]},
		{"metric":{"route":"/users","spec_version":"abc123","shard":"b"},"value":[1757000000,"0"]}`)

	if err := evaluator.RunEvaluation(context.Background()); err != nil {
		t.Fatalf("RunEvaluation: %v", err)
	}

	entries := detectionRecords(t, observed)
	if len(entries) != 1 {
		t.Fatalf("got %d detection records for one quiet route version, want exactly 1", len(entries))
	}
	if route, _ := entries[0].ContextMap()["route"].(string); route != "/users" {
		t.Errorf("record route = %q, want /users", route)
	}

	rendered := evaluator.metrics.render()
	if got := strings.Count(rendered, "seam_retirement_deprecation_candidates_total{"); got != 1 {
		t.Errorf("rendered %d candidate series, want exactly 1:\n%s", got, rendered)
	}
	if !strings.Contains(rendered, `seam_retirement_deprecation_candidates_total{route="/users",api_version="_unversioned",spec_version="abc123"} 1`) {
		t.Errorf("the collapsed candidate must count exactly 1, not 2:\n%s", rendered)
	}
	if !strings.Contains(rendered, "seam_retirement_routes_evaluated 1") {
		t.Errorf("routes_evaluated: the duplicate collapses to one considered route version:\n%s", rendered)
	}

	// The drop is observable, so an operator can see the normalization happen.
	drops := 0
	for _, entry := range observed.FilterMessage("Dropping duplicate series: one route version is one candidate").All() {
		if entry.Level == zapcore.WarnLevel {
			drops++
		}
	}
	if drops != 1 {
		t.Errorf("got %d duplicate-drop warnings, want 1", drops)
	}
}

// TestMalformedSeriesNeverBecomeCandidates drives evaluations over malformed
// series — the shapes that would otherwise mint a proposal with an empty
// route, an empty spec version, or a quietness nobody vouched for — and pins
// that none of them becomes a candidate while a well-formed quiet route in the
// same response still does.
func TestMalformedSeriesNeverBecomeCandidates(t *testing.T) {
	cases := []struct {
		name            string
		series          string
		wantCandidates  int // candidate series in the rendered registry
		wantRecords     int // detection records
		wantConsidered  int // routes_evaluated after the run
		forbiddenSeries string
	}{
		{
			name: "unreadable sample is traffic, not quiet",
			// The sample array is one field short; sampleValue fails and the
			// count becomes -1, which must fail the exactly-zero gate rather
			// than ride it as quietness.
			series: `{"metric":{"route":"/ghost","spec_version":"zzz999"},"value":[1757000000]},
				{"metric":{"route":"/users","spec_version":"abc123"},"value":[1757000000,"0"]}`,
			wantCandidates:  1,
			wantRecords:     1,
			wantConsidered:  2,
			forbiddenSeries: `route="/ghost"`,
		},
		{
			name: "sample that is not a string is traffic, not quiet",
			series: `{"metric":{"route":"/ghost","spec_version":"zzz999"},"value":[1757000000,0]},
				{"metric":{"route":"/users","spec_version":"abc123"},"value":[1757000000,"0"]}`,
			wantCandidates:  1,
			wantRecords:     1,
			wantConsidered:  2,
			forbiddenSeries: `route="/ghost"`,
		},
		{
			name: "series without a route label names no fragment",
			// Zero traffic too — it would otherwise qualify and mint a
			// proposal whose route, and so whose fragment path, is empty.
			series: `{"metric":{"spec_version":"bbb222"},"value":[1757000000,"0"]},
				{"metric":{"route":"/users","spec_version":"abc123"},"value":[1757000000,"0"]}`,
			wantCandidates:  1,
			wantRecords:     1,
			wantConsidered:  1,
			forbiddenSeries: `route=""`,
		},
		{
			name: "series without a spec_version is not a route version",
			series: `{"metric":{"route":"/nospec"},"value":[1757000000,"0"]},
				{"metric":{"route":"/users","spec_version":"abc123"},"value":[1757000000,"0"]}`,
			wantCandidates:  1,
			wantRecords:     1,
			wantConsidered:  1,
			forbiddenSeries: `spec_version=""`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observed := observingLogger(t)
			evaluator := serveVM(t, tc.series)

			if err := evaluator.RunEvaluation(context.Background()); err != nil {
				t.Fatalf("RunEvaluation: %v", err)
			}

			if got := len(detectionRecords(t, observed)); got != tc.wantRecords {
				t.Errorf("got %d detection records, want %d", got, tc.wantRecords)
			}

			rendered := evaluator.metrics.render()
			if got := strings.Count(rendered, "seam_retirement_deprecation_candidates_total{"); got != tc.wantCandidates {
				t.Errorf("rendered %d candidate series, want %d:\n%s", got, tc.wantCandidates, rendered)
			}
			if strings.Contains(rendered, tc.forbiddenSeries) {
				t.Errorf("the malformed series must not appear as a candidate series:\n%s", rendered)
			}
			if !strings.Contains(rendered, fmt.Sprintf("seam_retirement_routes_evaluated %d", tc.wantConsidered)) {
				t.Errorf("routes_evaluated: want %d:\n%s", tc.wantConsidered, rendered)
			}
			if !strings.Contains(rendered, `seam_retirement_evaluation_runs_total{result="success"} 1`) {
				t.Errorf("a run over malformed input is still a successful run:\n%s", rendered)
			}
		})
	}
}

// TestMalformedCandidateShapeIsRejectedAtEligibility pins the eligibility gate
// itself, not just the parse wiring: a candidate needs an exactly-zero count
// and a vouched quiet-since, so neither an unreadable sample nor a missing
// quiet period can ride through as quietness.
func TestMalformedCandidateShapeIsRejectedAtEligibility(t *testing.T) {
	evaluator := testEvaluator(t, "http://127.0.0.1:1") // never contacted
	vouched := time.Now().Add(-30 * 24 * time.Hour)
	window := 7 * 24 * time.Hour

	if eligible, reason := evaluator.isEligibleForRetirement(RouteTrafficStats{Route: "/ghost", TotalRequests: -1}, vouched, window); eligible {
		t.Errorf("an unreadable count (-1) must not be eligible, reason %q", reason)
	} else if reason != "Route has active traffic" {
		t.Errorf("reason = %q, want the active-traffic verdict", reason)
	}

	if eligible, reason := evaluator.isEligibleForRetirement(RouteTrafficStats{Route: "/ghost", TotalRequests: 0}, time.Time{}, window); eligible {
		t.Errorf("a zero quiet-since must not be eligible, reason %q", reason)
	} else if reason != "No vouched quiet period" {
		t.Errorf("reason = %q, want the no-vouched-quiet verdict", reason)
	}

	if eligible, reason := evaluator.isEligibleForRetirement(RouteTrafficStats{Route: "/users", TotalRequests: 0}, vouched, window); !eligible {
		t.Errorf("a vouched quiet route version must stay eligible, reason %q", reason)
	}
}
