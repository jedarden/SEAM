package main

// The output-contract tests. emitRetirementFinding is the evaluator's whole
// verdict: one structured record and one counter per candidate, carrying an
// x-seam-deprecated block a human pastes onto the route fragment. These tests
// pin that the block is not merely present but valid under the canonical
// x-seam-deprecated schema (internal/spec/lint.go checkDeprecation — the same
// validation a landed fragment must survive), and that a run with nothing to
// report is a successful quiet run rather than a silent failure.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest/observer"
	"gopkg.in/yaml.v3"
)

// contractFields is the full field set one structured detection record must
// carry. The record is the whole proposal — a dropped field is a dropped part
// of it, so a candidate whose record lacks, say, the block or the fragment
// path is not actionable and fails here.
var contractFields = []string{
	"route",
	"api_version",
	"spec_version",
	"quiet_since",
	"eval_window",
	"reason",
	"proposed_sunset",
	"brownout_windows",
	"fragment_path",
	"x_seam_deprecated_block",
	"body",
}

// TestXSeamDeprecatedBlockIsFragmentShaped proves the proposed block parses
// as YAML, carries the singular brownout key both consumers read, and passes
// the same checks internal/spec/lint.go checkDeprecation applies to a landed
// fragment.
func TestXSeamDeprecatedBlockIsFragmentShaped(t *testing.T) {
	observed := observingLogger(t)
	evaluator := testEvaluator(t, "http://127.0.0.1:1") // never contacted

	evaluator.emitRetirementFinding(quietCandidate())

	entries := observed.FilterMessage("Deprecation candidate detected").All()
	if len(entries) != 1 {
		t.Fatalf("got %d detection records, want exactly 1", len(entries))
	}
	block, ok := entries[0].ContextMap()["x_seam_deprecated_block"].(string)
	if !ok {
		t.Fatalf("x_seam_deprecated_block = %T, want a string",
			entries[0].ContextMap()["x_seam_deprecated_block"])
	}

	// A real parse, not substrings: indentation or shape drift has to fail
	// here, while the proposal is still a log record, not after a human has
	// pasted it onto a live fragment.
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(block), &parsed); err != nil {
		t.Fatalf("proposed block does not parse as YAML: %v\n%s", err, block)
	}

	deprecated, ok := parsed["x-seam-deprecated"].(map[string]any)
	if !ok {
		t.Fatalf("block root = %v, want an x-seam-deprecated object:\n%s", parsed, block)
	}

	// The regression this pins: the block once carried the plural
	// "brownouts", which neither consumer reads — the windows would parse as
	// an unknown field and silently never fire.
	if _, plural := deprecated["brownouts"]; plural {
		t.Errorf("block carries plural brownouts; the schema key is singular brownout:\n%s", block)
	}

	validateFragmentDeprecation(t, deprecated)
}

// validateFragmentDeprecation mirrors internal/spec/lint.go checkDeprecation
// rule for rule. The nested module cannot import the parent's internal
// package, so this mirror is what keeps a proposal the lint would reject from
// ever being emitted.
func validateFragmentDeprecation(t *testing.T, deprecated map[string]any) {
	t.Helper()

	since, ok := deprecated["since"].(string)
	if !ok || !isValidISODate(since) {
		t.Fatalf("x-seam-deprecated.since = %v, want an ISO date (YYYY-MM-DD)", deprecated["since"])
	}

	sunset, ok := deprecated["sunset"].(string)
	if !ok || !isValidISODate(sunset) {
		t.Fatalf("x-seam-deprecated.sunset = %v, want an ISO date (YYYY-MM-DD)", deprecated["sunset"])
	}
	// ISO dates sort lexicographically, the same comparison isDateAfter uses.
	if sunset <= since {
		t.Fatalf("sunset %s must be after since %s", sunset, since)
	}

	// Brownout requires sunset, is a non-empty array, and each window is an
	// RFC 3339 {start, end} pair inside [since, sunset], ordered and
	// non-overlapping.
	brownout, ok := deprecated["brownout"].([]any)
	if !ok || len(brownout) == 0 {
		t.Fatalf("x-seam-deprecated.brownout = %v, want a non-empty array", deprecated["brownout"])
	}

	sinceStart, err := time.Parse("2006-01-02", since)
	if err != nil {
		t.Fatalf("since %q: %v", since, err)
	}
	sunsetEnd, err := time.Parse("2006-01-02", sunset)
	if err != nil {
		t.Fatalf("sunset %q: %v", sunset, err)
	}
	// The interval is inclusive of the sunset day: [since 00:00Z, day after
	// sunset 00:00Z), matching isDateTimeWithinRange.
	sunsetEnd = sunsetEnd.AddDate(0, 0, 1)

	var previousEnd time.Time
	for i, window := range brownout {
		w, ok := window.(map[string]any)
		if !ok {
			t.Fatalf("brownout[%d] = %v, want an object", i, window)
		}
		start, ok := w["start"].(string)
		if !ok {
			t.Fatalf("brownout[%d].start = %v, want an RFC 3339 date-time", i, w["start"])
		}
		end, ok := w["end"].(string)
		if !ok {
			t.Fatalf("brownout[%d].end = %v, want an RFC 3339 date-time", i, w["end"])
		}
		startT, err := time.Parse(time.RFC3339, start)
		if err != nil {
			t.Fatalf("brownout[%d].start %q is not RFC 3339: %v", i, start, err)
		}
		endT, err := time.Parse(time.RFC3339, end)
		if err != nil {
			t.Fatalf("brownout[%d].end %q is not RFC 3339: %v", i, end, err)
		}
		if !endT.After(startT) {
			t.Errorf("brownout[%d] end %s is not after start %s", i, end, start)
		}
		// Instants, not strings: the emitted windows carry the process's own
		// zone offset, and the canonical range check compares parsed instants.
		if startT.Before(sinceStart) || !startT.Before(sunsetEnd) {
			t.Errorf("brownout[%d].start %s is outside [%s, %s]", i, start, since, sunset)
		}
		if endT.Before(sinceStart) || !endT.Before(sunsetEnd) {
			t.Errorf("brownout[%d].end %s is outside [%s, %s]", i, end, since, sunset)
		}
		if i > 0 && !startT.After(previousEnd) {
			t.Errorf("brownout[%d] starts %s at or before window %d's end %s; windows must be ordered and non-overlapping",
				i, start, i-1, previousEnd.Format(time.RFC3339))
		}
		previousEnd = endT
	}
}

// isValidISODate mirrors the strict YYYY-MM-DD shape check in
// internal/spec/lint.go. Go's time.Parse accepts "2026-9-5" for this layout,
// so the fixed width and separator positions are checked before parsing.
func isValidISODate(date string) bool {
	if len(date) != 10 || date[4] != '-' || date[7] != '-' {
		return false
	}
	for i := 0; i < 10; i++ {
		if i == 4 || i == 7 {
			continue
		}
		if date[i] < '0' || date[i] > '9' {
			return false
		}
	}
	_, err := time.Parse("2006-01-02", date)
	return err == nil
}

// TestNoCandidateRunIsQuietlySuccessful covers the no-candidate behavior: a
// run where nothing qualifies must still be a successful run — the run
// counter and the routes-evaluated gauge report it, and no detection record
// or candidate series appears.
func TestNoCandidateRunIsQuietlySuccessful(t *testing.T) {
	t.Run("every route version has traffic", func(t *testing.T) {
		observed := observingLogger(t)
		vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"route":"/users","spec_version":"abc123"},"value":[1757000000,"1"]},
				{"metric":{"route":"/orders","spec_version":"def456"},"value":[1757000000,"4213"]}
			]}}`)
		}))
		defer vm.Close()

		evaluator := testEvaluator(t, vm.URL)
		if err := evaluator.RunEvaluation(context.Background()); err != nil {
			t.Fatalf("RunEvaluation: %v", err)
		}

		assertNoCandidates(t, observed, evaluator.metrics, 2)
	})

	t.Run("VictoriaMetrics reports no route versions", func(t *testing.T) {
		observed := observingLogger(t)
		vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}))
		defer vm.Close()

		evaluator := testEvaluator(t, vm.URL)
		if err := evaluator.RunEvaluation(context.Background()); err != nil {
			t.Fatalf("RunEvaluation: %v", err)
		}

		assertNoCandidates(t, observed, evaluator.metrics, 0)
	})
}

// assertNoCandidates pins the quiet-run contract: no detection record, no
// candidate series, one successful-run sample, and a routes-evaluated gauge
// reporting what the run actually considered.
func assertNoCandidates(t *testing.T, observed *observer.ObservedLogs, metrics *retirementMetrics, wantRoutes int) {
	t.Helper()

	if entries := observed.FilterMessage("Deprecation candidate detected").All(); len(entries) != 0 {
		t.Errorf("got %d detection records, want 0", len(entries))
	}

	rendered := metrics.render()
	if strings.Contains(rendered, "seam_retirement_deprecation_candidates_total{") {
		t.Errorf("a run with no candidates must emit no candidate series:\n%s", rendered)
	}
	if !strings.Contains(rendered, `seam_retirement_evaluation_runs_total{result="success"} 1`) {
		t.Errorf("the no-candidate run must still record a successful run:\n%s", rendered)
	}
	if !strings.Contains(rendered, fmt.Sprintf("seam_retirement_routes_evaluated %d", wantRoutes)) {
		t.Errorf("routes_evaluated: want %d (what the run considered):\n%s", wantRoutes, rendered)
	}
}

// TestOneStructuredRecordAndCounterPerCandidate drives a full evaluation over
// three quiet route versions and asserts the output contract is exactly
// linear: one structured detection record and one counter increment per
// candidate, every record carrying the full contract field set, and each
// route-version series counting its own candidate and nobody else's.
func TestOneStructuredRecordAndCounterPerCandidate(t *testing.T) {
	observed := observingLogger(t)

	quiet := []struct{ route, spec string }{
		{"/users", "abc123"},
		{"/orders", "def456"},
		{"/reports", "ghi789"},
	}
	series := make([]string, 0, len(quiet))
	for _, q := range quiet {
		series = append(series, fmt.Sprintf(
			`{"metric":{"route":"%s","spec_version":"%s"},"value":[1757000000,"0"]}`, q.route, q.spec))
	}
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[%s]}}`,
			strings.Join(series, ","))
	}))
	defer vm.Close()

	evaluator := testEvaluator(t, vm.URL)
	if err := evaluator.RunEvaluation(context.Background()); err != nil {
		t.Fatalf("RunEvaluation: %v", err)
	}

	entries := observed.FilterMessage("Deprecation candidate detected").All()
	if len(entries) != len(quiet) {
		t.Fatalf("got %d detection records, want exactly %d (one per quiet route version)",
			len(entries), len(quiet))
	}

	seen := make(map[string]bool, len(quiet))
	for _, entry := range entries {
		fields := entry.ContextMap()

		route, ok := fields["route"].(string)
		if !ok {
			t.Fatalf("record carries no route field (has %v)", fieldNames(fields))
		}
		if seen[route] {
			t.Errorf("route %s produced a second detection record; the contract is one record per candidate", route)
		}
		seen[route] = true

		for _, key := range contractFields {
			if _, ok := fields[key]; !ok {
				t.Errorf("record for %s is missing contract field %q (has %v)", route, key, fieldNames(fields))
			}
		}
	}
	for _, q := range quiet {
		if !seen[q.route] {
			t.Errorf("quiet route %s produced no detection record", q.route)
		}
	}

	rendered := evaluator.metrics.render()
	if got := strings.Count(rendered, "seam_retirement_deprecation_candidates_total{"); got != len(quiet) {
		t.Errorf("rendered %d candidate series, want exactly %d:\n%s", got, len(quiet), rendered)
	}
	for _, q := range quiet {
		want := fmt.Sprintf(`seam_retirement_deprecation_candidates_total{route="%s",api_version="_unversioned",spec_version="%s"} 1`,
			q.route, q.spec)
		if !strings.Contains(rendered, want) {
			t.Errorf("metric missing an exactly-one count for %s:\n%s", q.route, rendered)
		}
	}
}
