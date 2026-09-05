package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// observingLogger installs an in-memory core as the global logger and returns
// what it recorded. emitRetirementFinding reports through zap.L(), so the
// global is what a test has to observe.
func observingLogger(t *testing.T) *observer.ObservedLogs {
	t.Helper()

	core, logs := observer.New(zapcore.InfoLevel)
	previous := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(func() { zap.ReplaceGlobals(previous) })

	return logs
}

func testEvaluator(t *testing.T, endpoint string) *RetirementEvaluator {
	t.Helper()

	return NewRetirementEvaluator(
		NewVictoriaMetricsClient(endpoint),
		&Config{DeclarativeConfigPath: "k8s/rs-manager/seam/routes.d"},
		newRetirementMetrics(),
	)
}

func quietCandidate() *RetirementCandidate {
	quietSince := time.Now().Add(-30 * 24 * time.Hour)
	return &RetirementCandidate{
		RouteStats: RouteTrafficStats{
			Route:       "/users",
			APIVersion:  "v1",
			SpecVersion: "abc123",
		},
		QuietSince: quietSince,
		MaxGap:     24 * time.Hour,
		EvalWindow: 14 * 24 * time.Hour,
		Reason:     "Zero traffic for 720h0m0s (exceeds window 336h0m0s)",
	}
}

func TestEmitRetirementFindingCarriesTheDetection(t *testing.T) {
	observed := observingLogger(t)
	evaluator := testEvaluator(t, "http://127.0.0.1:1") // never contacted
	candidate := quietCandidate()

	evaluator.emitRetirementFinding(candidate)

	entries := observed.FilterMessage("Deprecation candidate detected").All()
	if len(entries) != 1 {
		t.Fatalf("got %d detection records, want exactly 1", len(entries))
	}
	fields := entries[0].ContextMap()

	assertField(t, fields, "route", "/users")
	assertField(t, fields, "api_version", "v1")
	assertField(t, fields, "spec_version", "abc123")
	assertTimeField(t, fields, "quiet_since", candidate.QuietSince)
	assertField(t, fields, "eval_window", 14*24*time.Hour)
	assertField(t, fields, "reason", candidate.Reason)
	// The route label is a URL path and carries its own leading slash; the
	// rendered fragment path must not stack a second separator behind it.
	assertField(t, fields, "fragment_path", "k8s/rs-manager/seam/routes.d/users/fragment.yaml")

	// The proposed sunset is a date 90 days out, not a duration.
	sunset, ok := fields["proposed_sunset"].(string)
	if !ok {
		t.Fatalf("proposed_sunset = %T, want a string date", fields["proposed_sunset"])
	}
	if want := time.Now().Add(90 * 24 * time.Hour).Format("2006-01-02"); sunset != want {
		t.Errorf("proposed_sunset = %s, want %s (90 days out)", sunset, want)
	}

	// The computed brownout windows travel in the record, all three of them,
	// as fragment-shaped YAML entries.
	brownouts, ok := fields["brownout_windows"].(string)
	if !ok {
		t.Fatalf("brownout_windows = %T, want a string", fields["brownout_windows"])
	}
	if got := strings.Count(brownouts, "- start:"); got != 3 {
		t.Errorf("brownout_windows has %d windows, want 3:\n%s", got, brownouts)
	}

	// The human-readable body is the same content the PR body used to carry.
	body, ok := fields["body"].(string)
	if !ok || body == "" {
		t.Fatalf("body = %v, want the human-readable proposal text", fields["body"])
	}
	for _, want := range []string{"/users", "v1", "abc123", candidate.QuietSince.Format("2006-01-02T15:04:05Z"), sunset, "Zero observed traffic"} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not mention %q", want)
		}
	}

	// The block a human lands on the fragment is in the record too.
	block, ok := fields["x_seam_deprecated_block"].(string)
	if !ok {
		t.Fatalf("x_seam_deprecated_block = %T, want a string", fields["x_seam_deprecated_block"])
	}
	if !strings.Contains(block, "x-seam-deprecated:") || !strings.Contains(block, sunset) {
		t.Errorf("x_seam_deprecated_block does not carry the proposed block:\n%s", block)
	}
}

func TestEmitRetirementFindingCountsByRouteVersion(t *testing.T) {
	observingLogger(t)
	metrics := newRetirementMetrics()
	evaluator := NewRetirementEvaluator(
		NewVictoriaMetricsClient("http://127.0.0.1:1"),
		&Config{DeclarativeConfigPath: "k8s/rs-manager/seam/routes.d"},
		metrics,
	)

	candidate := quietCandidate()
	evaluator.emitRetirementFinding(candidate)
	evaluator.emitRetirementFinding(candidate)

	other := quietCandidate()
	other.RouteStats.Route = "/orders"
	other.RouteStats.SpecVersion = "def456"
	evaluator.emitRetirementFinding(other)

	rendered := metrics.render()
	want := `seam_retirement_deprecation_candidates_total{route="/orders",api_version="v1",spec_version="def456"} 1`
	if !strings.Contains(rendered, want) {
		t.Errorf("metric output missing %s:\n%s", want, rendered)
	}
	want = `seam_retirement_deprecation_candidates_total{route="/users",api_version="v1",spec_version="abc123"} 2`
	if !strings.Contains(rendered, want) {
		t.Errorf("a repeat candidate must accumulate, want %s:\n%s", want, rendered)
	}
}

func TestMetricsEndpointRendersExpositionFormat(t *testing.T) {
	observingLogger(t)
	metrics := newRetirementMetrics()
	metrics.recordCandidate(routeVersionKey{route: `/we"ird`, apiVersion: "v1", specVersion: "abc"})
	metrics.recordRun("success", 7)
	metrics.recordRun("error", 0)

	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "text/plain") {
		t.Errorf("Content-Type = %q, want the Prometheus text format", got)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, "# TYPE seam_retirement_deprecation_candidates_total counter") {
		t.Errorf("missing counter TYPE declaration:\n%s", body)
	}
	// Label values must be quoted and escaped, or a scraper rejects the
	// whole exposition.
	if !strings.Contains(body, `route="/we\"ird"`) {
		t.Errorf("label value not escaped:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE seam_retirement_evaluation_runs_total counter") {
		t.Errorf("missing run counter TYPE declaration:\n%s", body)
	}
	if !strings.Contains(body, `seam_retirement_evaluation_runs_total{result="error"} 1`) {
		t.Errorf("missing error run sample:\n%s", body)
	}
	if !strings.Contains(body, "seam_retirement_routes_evaluated 0") {
		t.Errorf("routes_evaluated should report the last run (the error), got:\n%s", body)
	}
}

// loadConfig must not require a third-party credential any more: the evaluator
// is detection-only, so an empty environment configures it fully. The absence
// of the GitHub fields on Config is what actually proves this — the test pins
// that an empty environment yields a usable config.
func TestLoadConfigNeedsNoCredential(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("VICTORIAMETRICS_ENDPOINT", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig with an empty environment: %v", err)
	}
	if cfg.VictoriaMetricsEndpoint == "" {
		t.Error("VictoriaMetricsEndpoint is empty, want the default")
	}
	if cfg.DeclarativeConfigPath == "" {
		t.Error("DeclarativeConfigPath is empty, want the default")
	}
}

func TestCalculateEvaluationWindowUnchanged(t *testing.T) {
	evaluator := testEvaluator(t, "http://127.0.0.1:1")

	cases := []struct {
		name    string
		maxGap  time.Duration
		history time.Duration
		want    time.Duration
	}{
		{"3x a 72h gap clears the floor", 72 * time.Hour, 30 * 24 * time.Hour, 216 * time.Hour},
		{"3x a 24h gap sits under the floor, so the floor wins", 24 * time.Hour, 30 * 24 * time.Hour, 7 * 24 * time.Hour},
		{"insufficient history takes the floor", 24 * time.Hour, 5 * 24 * time.Hour, 7 * 24 * time.Hour},
	}
	for _, tc := range cases {
		if got := evaluator.calculateEvaluationWindow(tc.maxGap, tc.history); got != tc.want {
			t.Errorf("%s: window = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestCalculateBrownoutsUnchanged(t *testing.T) {
	evaluator := testEvaluator(t, "http://127.0.0.1:1")

	since := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	sunset := since.Add(90 * 24 * time.Hour).Format("2006-01-02")

	brownouts := evaluator.calculateBrownouts(since, sunset)

	want := []string{
		brownoutLine(since.Add(30*24*time.Hour), since.Add(37*24*time.Hour)),
		brownoutLine(since.Add(60*24*time.Hour), since.Add(67*24*time.Hour)),
		brownoutLine(since.Add(83*24*time.Hour), since.Add(90*24*time.Hour)),
	}
	for i, w := range want {
		if !strings.Contains(brownouts, w) {
			t.Errorf("brownout window %d missing, want:\n%s\nfull block:\n%s", i, w, brownouts)
		}
	}
}

// brownoutLine returns the fragment-shaped two-line window the evaluator emits.
func brownoutLine(start, end time.Time) string {
	return fmt.Sprintf("- start: %q\n      end: %q", start.Format(time.RFC3339), end.Format(time.RFC3339))
}

// TestRunEvaluationEmitsDetectionFromVictoriaMetrics drives one evaluation
// against a canned VictoriaMetrics response — the same wire shape the real
// endpoint returns — and asserts the outcome is a log record and a metric, and
// nothing sent to any git host.
func TestRunEvaluationEmitsDetectionFromVictoriaMetrics(t *testing.T) {
	observed := observingLogger(t)

	vmRequests := 0
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vmRequests++
		if r.URL.Path != "/api/v1/query" {
			t.Errorf("VictoriaMetrics request path = %q, want /api/v1/query", r.URL.Path)
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"route":"/users","spec_version":"abc123"},"value":[1757000000,"0"]},
			{"metric":{"route":"/orders","spec_version":"def456"},"value":[1757000000,"4213"]}
		]}}`)
	}))
	defer vm.Close()

	evaluator := testEvaluator(t, vm.URL)
	if err := evaluator.RunEvaluation(context.Background()); err != nil {
		t.Fatalf("RunEvaluation: %v", err)
	}

	if vmRequests != 1 {
		t.Errorf("made %d outbound requests, want exactly the 1 VictoriaMetrics query", vmRequests)
	}

	// One detection record per quiet route version.
	entries := observed.FilterMessage("Deprecation candidate detected").All()
	if len(entries) != 1 {
		t.Fatalf("got %d detection records, want 1 (only /users is quiet)", len(entries))
	}
	fields := entries[0].ContextMap()
	assertField(t, fields, "route", "/users")

	// quiet_since must be the earliest instant the 14-day query window can
	// actually vouch for. The zero time.Time would read as ~292 years of quiet
	// and make the record a lie.
	quietSince, ok := fields["quiet_since"].(time.Time)
	if !ok {
		t.Fatalf("quiet_since = %T, want a time.Time", fields["quiet_since"])
	}
	if quietSince.IsZero() {
		t.Fatal("quiet_since is the zero time; the record would claim ~292 years of quiet")
	}
	if age := time.Since(quietSince); age < queryWindow-time.Hour || age > queryWindow+time.Minute {
		t.Errorf("quiet_since is %s ago, want ~%s ago (the query window)", age.Round(time.Hour), queryWindow)
	}

	rendered := evaluator.metrics.render()
	if !strings.Contains(rendered, `seam_retirement_deprecation_candidates_total{route="/users",api_version="_unversioned",spec_version="abc123"} 1`) {
		t.Errorf("metric missing the /users candidate:\n%s", rendered)
	}
	if !strings.Contains(rendered, `seam_retirement_evaluation_runs_total{result="success"} 1`) {
		t.Errorf("metric missing the successful run:\n%s", rendered)
	}
	if !strings.Contains(rendered, "seam_retirement_routes_evaluated 2") {
		t.Errorf("routes_evaluated = want 2 route versions considered:\n%s", rendered)
	}
	// The busy route must not be counted as a candidate: zero observed
	// traffic is the necessary condition, and /orders answered 4213.
	if strings.Contains(rendered, `candidates_total{route="/orders"`) {
		t.Errorf("/orders has traffic and must not be a candidate:\n%s", rendered)
	}
}

func assertField(t *testing.T, fields map[string]any, key string, want any) {
	t.Helper()

	got, ok := fields[key]
	if !ok {
		t.Errorf("record is missing field %q (has %v)", key, fieldNames(fields))
		return
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("field %q = %v (%T), want %v (%T)", key, got, got, want, want)
	}
}

// assertTimeField compares times with Equal, because a time captured from
// time.Now carries a monotonic reading that a round-trip through the logger
// drops; Sprint would call those unequal.
func assertTimeField(t *testing.T, fields map[string]any, key string, want time.Time) {
	t.Helper()

	got, ok := fields[key].(time.Time)
	if !ok {
		t.Errorf("field %q = %v (%T), want a time.Time", key, fields[key], fields[key])
		return
	}
	if !got.Equal(want) {
		t.Errorf("field %q = %v, want %v", key, got, want)
	}
}

func fieldNames(fields map[string]any) []string {
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	return names
}
