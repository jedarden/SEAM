package spec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRouteGuardSchemaAcceptsMaxRepeatsAndWindow(t *testing.T) {
	report := lintGuardTestFragment(t, nil, map[string]any{
		"x-loop-guard": map[string]any{
			"maxRepeats": 5,
			"window":     "10m",
		},
	})
	assertGuardLintAccepted(t, report)
}

func TestRouteGuardSchemaRejectsLegacyLoopGuardShape(t *testing.T) {
	legacyGuard := map[string]any{
		"max_" + "depth":     5,
		"max_" + "redirects": 2,
	}
	report := lintGuardTestFragment(t, nil, map[string]any{"x-loop-guard": legacyGuard})
	assertGuardFinding(t, report.Errors, "fragment.schema")
}

func TestRouteGuardSchemaRejectsBareNumberCostPerCall(t *testing.T) {
	report := lintGuardTestFragment(t, nil, map[string]any{"x-cost-per-call": 0.25})
	assertGuardFinding(t, report.Errors, "fragment.schema")
}

func TestRouteGuardSchemaRejectsUnitMismatch(t *testing.T) {
	tests := []struct {
		name      string
		root      map[string]any
		operation map[string]any
	}{
		{
			name: "fragment root",
			root: map[string]any{
				"x-cost-per-call": guardCost(1, "credits"),
				"x-quota":         guardQuota(100, "USD", "1h"),
			},
		},
		{
			name: "operation",
			operation: map[string]any{
				"x-cost-per-call": guardCost(1, "credits"),
				"x-quota":         guardQuota(100, "USD", "1h"),
			},
		},
		{
			name: "operation quota against root cost default",
			root: map[string]any{
				"x-cost-per-call": guardCost(1, "credits"),
			},
			operation: map[string]any{
				"x-quota": guardQuota(100, "USD", "1h"),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := lintGuardTestFragment(t, test.root, test.operation)
			assertGuardFinding(t, report.Errors, "quota.unit-mismatch")
		})
	}
}

func TestFragmentLoaderQuarantinesRouteGuardUnitMismatch(t *testing.T) {
	fragmentsDir := writeGuardTestFragment(t, map[string]any{
		"x-cost-per-call": guardCost(1, "credits"),
	}, map[string]any{
		"x-quota": guardQuota(100, "USD", "1h"),
	})
	loader, err := NewFragmentLoader()
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.LoadDirectory(fragmentsDir); err != nil {
		t.Fatal(err)
	}
	if err := loader.ValidateFragments(lintTestSchemaPath(t)); err != nil {
		t.Fatalf("ValidateFragments returned setup error: %v", err)
	}
	if loader.GetQuarantinedCount() != 1 {
		t.Fatalf("unit mismatch quarantined %d fragments, want 1", loader.GetQuarantinedCount())
	}
	reasons := strings.Join(loader.GetQuarantined()[0].QuarantineReasons, "\n")
	if !strings.Contains(reasons, "quota unit validation failed") {
		t.Fatalf("quarantine reason does not identify unit mismatch: %s", reasons)
	}
}

func TestRouteGuardSchemaAcceptsIdenticalUnits(t *testing.T) {
	tests := []struct {
		name      string
		root      map[string]any
		operation map[string]any
	}{
		{
			name: "fragment root",
			root: map[string]any{
				"x-cost-per-call": guardCost(0, "requests"),
				"x-quota":         guardQuota(100, "requests", "1h"),
			},
		},
		{
			name: "operation",
			operation: map[string]any{
				"x-cost-per-call": guardCost(0.5, "credits"),
				"x-quota":         guardQuota(10, "credits", "30m"),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := lintGuardTestFragment(t, test.root, test.operation)
			assertGuardLintAccepted(t, report)
		})
	}
}

func TestRouteGuardSchemaRejectsBadWindowGrammar(t *testing.T) {
	tests := []struct {
		name      string
		operation map[string]any
	}{
		{
			name: "unsupported week suffix",
			operation: map[string]any{
				"x-loop-guard": map[string]any{"maxRepeats": 3, "window": "1w"},
			},
		},
		{
			name: "missing suffix",
			operation: map[string]any{
				"x-cost-per-call": guardCost(1, "requests"),
				"x-quota":         guardQuota(100, "requests", "60"),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := lintGuardTestFragment(t, nil, test.operation)
			assertGuardFinding(t, report.Errors, "fragment.schema")
		})
	}
}

func TestRouteGuardWindowAbove168HoursIsLintWarning(t *testing.T) {
	tests := []struct {
		name      string
		operation map[string]any
		wantWarn  bool
	}{
		{
			name: "quota above boundary",
			operation: map[string]any{
				"x-cost-per-call": guardCost(1, "requests"),
				"x-quota":         guardQuota(100, "requests", "8d"),
			},
			wantWarn: true,
		},
		{
			name: "loop guard at boundary",
			operation: map[string]any{
				"x-loop-guard": map[string]any{"maxRepeats": 3, "window": "168h"},
			},
			wantWarn: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := lintGuardTestFragment(t, nil, test.operation)
			if report.HasErrors() {
				t.Fatalf("valid guard window was rejected: %+v", report.Errors)
			}
			gotWarn := guardFindingPresent(report.Warnings, "guard.window-over-168h")
			if gotWarn != test.wantWarn {
				t.Fatalf("guard.window-over-168h presence = %v, want %v; warnings: %+v", gotWarn, test.wantWarn, report.Warnings)
			}
		})
	}
}

func lintGuardTestFragment(t *testing.T, rootFields, operationFields map[string]any) LintReport {
	t.Helper()
	fragmentsDir := writeGuardTestFragment(t, rootFields, operationFields)
	report, err := LintDirectory(LintOptions{FragmentsDir: fragmentsDir, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatalf("LintDirectory returned setup error: %v", err)
	}
	return report
}

func writeGuardTestFragment(t *testing.T, rootFields, operationFields map[string]any) string {
	t.Helper()
	fragment := map[string]any{
		"x-seam-schema": "v1",
		"x-seam-owner":  "guard-test",
		"x-upstream":    "https://api.example.com",
		"paths": map[string]any{
			"/test": map[string]any{
				"get": operationFields,
			},
		},
	}
	for field, value := range rootFields {
		fragment[field] = value
	}
	if operationFields == nil {
		fragment["paths"].(map[string]any)["/test"].(map[string]any)["get"] = map[string]any{}
	}

	contents, err := json.Marshal(fragment)
	if err != nil {
		t.Fatal(err)
	}
	fragmentsDir := t.TempDir()
	ownerDir := filepath.Join(fragmentsDir, "guard-test")
	if err := os.MkdirAll(ownerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownerDir, "route.json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return fragmentsDir
}

func guardCost(amount float64, unit string) map[string]any {
	return map[string]any{"amount": amount, "unit": unit}
}

func guardQuota(amount float64, unit, window string) map[string]any {
	return map[string]any{"amount": amount, "unit": unit, "window": window}
}

func assertGuardLintAccepted(t *testing.T, report LintReport) {
	t.Helper()
	if report.HasErrors() {
		t.Fatalf("valid guard fragment was rejected: %+v", report.Errors)
	}
}

func assertGuardFinding(t *testing.T, findings []LintFinding, code string) {
	t.Helper()
	if !guardFindingPresent(findings, code) {
		t.Fatalf("missing finding %q in %+v", code, findings)
	}
}

func guardFindingPresent(findings []LintFinding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
