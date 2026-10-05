package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func fixedContractEvaluator(t *testing.T) *RetirementEvaluator {
	t.Helper()
	evaluator := testEvaluator(t, "http://127.0.0.1:1")
	evaluator.now = func() time.Time {
		return time.Date(2026, 9, 28, 11, 35, 23, 0, time.UTC)
	}
	return evaluator
}

func fixedContractCandidate() *RetirementCandidate {
	return &RetirementCandidate{
		RouteStats: RouteTrafficStats{
			Route:       "/users",
			APIVersion:  "_unversioned",
			SpecVersion: "abc123",
		},
		QuietSince: time.Date(2026, 8, 29, 11, 35, 23, 0, time.UTC),
		EvalWindow: 14 * 24 * time.Hour,
		Reason:     "Zero traffic for 720h0m0s (exceeds window 336h0m0s)",
	}
}

func TestFindingSchemaDeclaresOnlyRequiredStrings(t *testing.T) {
	if len(retirementFindingSchema) == 0 {
		t.Fatal("finding schema must declare at least one payload field")
	}
	seen := make(map[string]struct{}, len(retirementFindingSchema))
	for _, field := range retirementFindingSchema {
		if field.name == "" {
			t.Error("finding schema contains an unnamed field")
		}
		if field.typeName != "string" {
			t.Errorf("finding field %q has type %q, want string", field.name, field.typeName)
		}
		if !field.required {
			t.Errorf("finding field %q is optional; every payload field is required", field.name)
		}
		if _, duplicate := seen[field.name]; duplicate {
			t.Errorf("finding schema repeats field %q", field.name)
		}
		seen[field.name] = struct{}{}
	}
	if got, want := len(retirementFindingFields), len(retirementFindingSchema); got != want {
		t.Fatalf("field-name list has %d fields, schema has %d", got, want)
	}
}

func TestFindingRecordSchemaIsClosed(t *testing.T) {
	observed := observingLogger(t)
	evaluator := fixedContractEvaluator(t)
	evaluator.emitRetirementFinding(fixedContractCandidate())

	entries := observed.FilterMessage(retirementFindingMessage).All()
	if len(entries) != 1 {
		t.Fatalf("got %d findings, want one", len(entries))
	}
	fields := entries[0].ContextMap()
	if len(fields) != len(retirementFindingFields) {
		t.Fatalf("got %d payload fields, want %d: %v", len(fields), len(retirementFindingFields), fieldNames(fields))
	}

	want := make(map[string]struct{}, len(retirementFindingFields))
	for _, name := range retirementFindingFields {
		want[name] = struct{}{}
	}
	for name, value := range fields {
		if _, ok := want[name]; !ok {
			t.Errorf("finding contains uncontracted field %q", name)
		}
		if _, ok := value.(string); !ok {
			t.Errorf("finding field %q has type %T, want string", name, value)
		}
		if value == "" {
			t.Errorf("finding field %q is empty; required fields must be populated", name)
		}
	}
	for name := range want {
		if _, ok := fields[name]; !ok {
			t.Errorf("finding is missing contracted field %q", name)
		}
	}
}

func TestFindingMessageAndPayloadAreDeterministic(t *testing.T) {
	observed := observingLogger(t)
	evaluator := fixedContractEvaluator(t)
	candidate := fixedContractCandidate()

	evaluator.emitRetirementFinding(candidate)
	evaluator.emitRetirementFinding(candidate)

	entries := observed.FilterMessage(retirementFindingMessage).All()
	if len(entries) != 2 {
		t.Fatalf("got %d finding records, want two identical records", len(entries))
	}
	for i, entry := range entries {
		if entry.Message != retirementFindingMessage {
			t.Errorf("record %d message = %q, want %q", i, entry.Message, retirementFindingMessage)
		}
	}
	if !reflect.DeepEqual(entries[0].ContextMap(), entries[1].ContextMap()) {
		t.Fatalf("same candidate and evaluation time produced different payloads:\nfirst: %v\nsecond: %v",
			entries[0].ContextMap(), entries[1].ContextMap())
	}
}

func TestFindingOutputFormatsAreStable(t *testing.T) {
	observed := observingLogger(t)
	evaluator := fixedContractEvaluator(t)
	evaluator.emitRetirementFinding(fixedContractCandidate())
	fields := observed.FilterMessage(retirementFindingMessage).All()[0].ContextMap()

	if got := fields["quiet_since"]; got != "2026-08-29T11:35:23Z" {
		t.Errorf("quiet_since = %v, want RFC 3339 string", got)
	}
	if got := fields["eval_window"]; got != "336h0m0s" {
		t.Errorf("eval_window = %v, want Go duration string", got)
	}
	if got := fields["proposed_sunset"]; got != "2026-12-27" {
		t.Errorf("proposed_sunset = %v, want ISO date", got)
	}
}

func TestMetricContractGolden(t *testing.T) {
	metrics := newRetirementMetrics()
	metrics.recordCandidate(routeVersionKey{route: "/users", apiVersion: "_unversioned", specVersion: "abc123"})
	metrics.recordCandidate(routeVersionKey{route: "/users", apiVersion: "_unversioned", specVersion: "abc123"})
	metrics.recordCandidate(routeVersionKey{route: "/orders", apiVersion: "_unversioned", specVersion: "def456"})
	metrics.recordRun("success", 2)
	metrics.recordRun("error", 0)

	want, err := os.ReadFile(filepath.Join("testdata", "metrics.golden.txt"))
	if err != nil {
		t.Fatalf("read metrics golden: %v", err)
	}
	if got := metrics.render(); got != string(want) {
		t.Fatalf("metric exposition differs from golden:\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestMetricLabelsAreClosedAndCardinalityBounded(t *testing.T) {
	metrics := newRetirementMetrics()
	metrics.recordCandidate(routeVersionKey{route: "/users", apiVersion: "v1", specVersion: "abc123"})
	metrics.recordCandidate(routeVersionKey{route: "/users", apiVersion: "v1", specVersion: "abc123"})

	lines := strings.Split(metrics.render(), "\n")
	var candidateLines []string
	for _, line := range lines {
		if strings.HasPrefix(line, candidateMetricName+"{") {
			candidateLines = append(candidateLines, line)
		}
	}
	if len(candidateLines) != 1 {
		t.Fatalf("got %d candidate series, want one for one route version: %v", len(candidateLines), candidateLines)
	}
	labelsText := strings.TrimPrefix(strings.SplitN(candidateLines[0], "}", 2)[0], candidateMetricName+"{")
	gotLabels := make([]string, 0, len(candidateMetricLabels))
	for _, label := range strings.Split(labelsText, ",") {
		gotLabels = append(gotLabels, strings.SplitN(label, "=", 2)[0])
	}
	if !reflect.DeepEqual(gotLabels, candidateMetricLabels[:]) {
		t.Fatalf("metric labels = %v, want exactly %v", gotLabels, candidateMetricLabels)
	}
}

func TestFindingIgnoresForeignLabelsAndSecretValues(t *testing.T) {
	const secretValue = "bearer-secret-value-must-not-appear"
	observed := observingLogger(t)
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"route":"/users","spec_version":"abc123","authorization":"%s","cookie":"%s","password":"%s","tenant":"%s"},"value":[1757000000,"0"]},
			{"metric":{"route":"/users","spec_version":"abc123","authorization":"different","tenant":"other"},"value":[1757000000,"0"]}
		]}}`, secretValue, secretValue, secretValue, secretValue)
	}))
	defer vm.Close()

	evaluator := testEvaluator(t, vm.URL)
	if err := evaluator.RunEvaluation(context.Background()); err != nil {
		t.Fatalf("RunEvaluation: %v", err)
	}

	entries := observed.FilterMessage(retirementFindingMessage).All()
	if len(entries) != 1 {
		t.Fatalf("got %d findings, want one after duplicate collapse", len(entries))
	}
	rendered, err := json.Marshal(entries[0].ContextMap())
	if err != nil {
		t.Fatalf("marshal finding: %v", err)
	}
	if strings.Contains(string(rendered), secretValue) {
		t.Fatalf("finding contains a foreign-label secret value: %s", rendered)
	}
	if strings.Count(evaluator.metrics.render(), candidateMetricName+"{") != 1 {
		t.Fatalf("foreign labels must not create metric cardinality:\n%s", evaluator.metrics.render())
	}
}

func TestRunbookRepresentativeRecordMatchesContract(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "retirement-handoff-runbook.md"))
	if err != nil {
		t.Fatalf("read handoff runbook: %v", err)
	}
	const startMarker = "```json\n"
	start := strings.Index(string(doc), startMarker)
	if start < 0 {
		t.Fatal("runbook has no JSON representative record")
	}
	start += len(startMarker)
	rest := string(doc)[start:]
	end := strings.Index(rest, "\n```")
	if end < 0 {
		t.Fatal("runbook JSON representative record has no closing fence")
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(rest[:end]), &record); err != nil {
		t.Fatalf("representative record is not JSON: %v", err)
	}
	wantKeys := append([]string{"level", "ts", "caller", "msg"}, retirementFindingFields[:]...)
	sort.Strings(wantKeys)
	gotKeys := make([]string, 0, len(record))
	for key := range record {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("representative record keys = %v, want %v", gotKeys, wantKeys)
	}

	if _, err := time.Parse(time.RFC3339, record["quiet_since"].(string)); err != nil {
		t.Errorf("quiet_since is not RFC 3339: %v", err)
	}
	if _, err := time.ParseDuration(record["eval_window"].(string)); err != nil {
		t.Errorf("eval_window is not a Go duration: %v", err)
	}
	if !isValidISODate(record["proposed_sunset"].(string)) {
		t.Errorf("proposed_sunset = %q, want YYYY-MM-DD", record["proposed_sunset"])
	}
}
