package spec

import (
	"encoding/json"
	"strings"
	"testing"
)

// lintScopeFragment builds a minimal valid route fragment with one path and
// one operation, plus extra top-level and operation-level lines the caller
// supplies. The extras are where x-required-scope values (valid or malformed)
// are injected. operationLines is a block body relative to the `get:` key:
// every line is indented six spaces as-is, so continuation lines carry their
// own two-space nesting.
func lintScopeFragment(rootLine, operationLines string) string {
	lines := []string{
		"x-seam-schema: v1",
		"x-seam-owner: geo-service",
		"x-api-version: v1",
		"x-upstream: https://geo-service.ardenone.internal",
	}
	if rootLine != "" {
		lines = append(lines, rootLine)
	}
	lines = append(lines, "paths:", "  /api:", "    get:")
	block := "responses:\n  '200':\n    description: ok"
	if operationLines != "" {
		block = operationLines + "\n" + block
	}
	for _, line := range strings.Split(block, "\n") {
		lines = append(lines, "      "+line)
	}
	return strings.Join(lines, "\n")
}

// scopeLintErrors lints a single fragment and returns the report. It fails the
// test only on harness errors, never on findings — the callers decide what
// the findings mean.
func scopeLintErrors(t *testing.T, fragment string) LintReport {
	t.Helper()
	root := t.TempDir()
	writeLintTestFragment(t, root, "geo-service", "route.yaml", fragment)
	report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// TestLint_MalformedOperationRequiredScope pins the lint-time rejection of
// malformed operation-level x-required-scope overrides. The fragment schema's
// scopeArray/scopeString defs are the authority (route-fragment-schema.md:
// operation-level value is "the authority" wherever both forms are present),
// so every malformed shape must surface as a fragment.schema error naming the
// extension — a silent drop would invert the documented default-deny posture.
func TestLint_MalformedOperationRequiredScope(t *testing.T) {
	cases := []struct {
		name       string
		operations string
	}{
		{name: "object instead of string or array", operations: "x-required-scope:\n  service: action"},
		{name: "empty array", operations: "x-required-scope: []"},
		{name: "non-string element", operations: "x-required-scope: [42]"},
		{name: "empty string element", operations: `x-required-scope: ["geo:read", ""]`},
		{name: "duplicate elements", operations: `x-required-scope: ["geo:read", "geo:read"]`},
		{name: "scope pattern violation", operations: `x-required-scope: ["Not A Scope"]`},
		{name: "single segment scope", operations: `x-required-scope: ["geo"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := scopeLintErrors(t, lintScopeFragment("", tc.operations))
			if !report.HasErrors() {
				t.Fatalf("expected a lint error for malformed operation-level x-required-scope, got none")
			}
			hasSchemaFinding := false
			for _, finding := range report.Errors {
				if finding.Code == "fragment.schema" && strings.Contains(finding.Message, "x-required-scope") {
					hasSchemaFinding = true
					break
				}
			}
			if !hasSchemaFinding {
				t.Fatalf("expected a fragment.schema finding naming x-required-scope, got: %+v", report.Errors)
			}
		})
	}
}

// TestLint_MalformedRootRequiredScope pins the same rejection at fragment
// root, where x-required-scope is the documented route-wide DEFAULT. The root
// form is lint-accepted (the schema has a property for it) but its shape is
// still constrained; a malformed default must not survive lint into a merge
// that would then either drop it or fail the route table at runtime.
func TestLint_MalformedRootRequiredScope(t *testing.T) {
	cases := []struct {
		name     string
		rootLine string
	}{
		{name: "object instead of string or array", rootLine: "x-required-scope:\n  service: action"},
		{name: "empty array", rootLine: "x-required-scope: []"},
		{name: "non-string element", rootLine: "x-required-scope: [true]"},
		{name: "scope pattern violation", rootLine: `x-required-scope: "GEO:READ"`},
		{name: "single segment scope", rootLine: `x-required-scope: "geo"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := scopeLintErrors(t, lintScopeFragment(tc.rootLine, ""))
			if !report.HasErrors() {
				t.Fatalf("expected a lint error for malformed fragment-root x-required-scope, got none")
			}
			hasSchemaFinding := false
			for _, finding := range report.Errors {
				if finding.Code == "fragment.schema" && strings.Contains(finding.Message, "x-required-scope") {
					hasSchemaFinding = true
					break
				}
			}
			if !hasSchemaFinding {
				t.Fatalf("expected a fragment.schema finding naming x-required-scope, got: %+v", report.Errors)
			}
		})
	}
}

// TestLint_DocumentedRequiredScopeFormsAreClean is the control for the two
// malformed-shape tables above: every documented form (fragment-root bare
// string default, fragment-root array, operation-level string, operation-level
// array, and the root-default-plus-operation-override pairing itself) lints
// clean. If one of these starts failing, the schema tightened and the
// malformed tables above need re-review, not a blanket loosening.
func TestLint_DocumentedRequiredScopeFormsAreClean(t *testing.T) {
	cases := []struct {
		name       string
		rootLine   string
		operations string
	}{
		{
			name:     "root bare string default",
			rootLine: `x-required-scope: "geo-service:query"`,
		},
		{
			name:     "root array default",
			rootLine: `x-required-scope: ["geo-service:query", "geo-service:export"]`,
		},
		{
			name:       "operation-level string",
			operations: `x-required-scope: "geo-service:query"`,
		},
		{
			name:       "operation-level array",
			operations: `x-required-scope: ["geo-service:query"]`,
		},
		{
			name:       "operation override over root default",
			rootLine:   `x-required-scope: "geo-service:query"`,
			operations: `x-required-scope: ["geo-service:mutate"]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := scopeLintErrors(t, lintScopeFragment(tc.rootLine, tc.operations))
			if report.HasErrors() {
				t.Fatalf("expected documented x-required-scope form to lint clean, got: %+v", report.Errors)
			}
		})
	}
}

// TestPropagateRouteMetadata_RequiredScopeDefaultReachesMergedRoutes proves
// the merge-time half of the documented semantics end to end: a fragment-root
// x-required-scope default must land on every merged path item as
// x-seam-internal-required-scope (the marker the route-table builder resolves
// for silent operations), an operation-level value must survive untouched
// beside it, and the internal marker must never leak into the served spec.
func TestPropagateRouteMetadata_RequiredScopeDefaultReachesMergedRoutes(t *testing.T) {
	root := t.TempDir()
	fragment := strings.Join([]string{
		"x-seam-schema: v1",
		"x-seam-owner: geo-service",
		"x-api-version: v1",
		"x-upstream: https://geo-service.ardenone.internal",
		`x-required-scope: "geo-service:query"`,
		"paths:",
		"  /items:",
		"    get:",
		"      responses:",
		"        '200':",
		"          description: ok",
		"    post:",
		"      x-required-scope: [geo-service:mutate]",
		"      responses:",
		"        '200':",
		"          description: ok",
	}, "\n")
	writeLintTestFragment(t, root, "geo-service", "route.yaml", fragment)

	loader, err := NewWithFragments(root, "http://localhost:9999", "", root)
	if err != nil {
		t.Fatal(err)
	}
	if quarantined := loader.FragmentLoader.GetQuarantinedCount(); quarantined != 0 {
		t.Fatalf("expected no quarantined fragments, got %d", quarantined)
	}

	merged := loader.GetRawDocument()
	var mergedMap map[string]any
	if err := json.Unmarshal(merged, &mergedMap); err != nil {
		t.Fatal(err)
	}
	paths, _ := mergedMap["paths"].(map[string]any)
	if paths == nil {
		t.Fatal("merged document has no paths")
	}
	items, _ := paths["/items"].(map[string]any)
	if items == nil {
		t.Fatal("merged document is missing /items")
	}

	// The fragment-root default is stamped on the path item under the
	// internal marker namespace the route-table builder resolves.
	if marker := items["x-seam-internal-required-scope"]; marker != "geo-service:query" {
		t.Fatalf("expected x-seam-internal-required-scope %q on the merged path item, got %v", "geo-service:query", marker)
	}

	// The overriding operation-level value survives beside it, untouched.
	post, _ := items["post"].(map[string]any)
	if post == nil {
		t.Fatal("merged path item is missing the post operation")
	}
	opScopes, ok := post["x-required-scope"].([]any)
	if !ok || len(opScopes) != 1 || opScopes[0] != "geo-service:mutate" {
		t.Fatalf("expected operation-level x-required-scope [geo-service:mutate] to survive the merge, got %v", post["x-required-scope"])
	}

	// The internal marker is stripped from the served document...
	served, err := loader.GetRawJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(served), "x-seam-internal-") {
		t.Fatalf("served spec leaks an internal route marker: %s", served)
	}
	// ...and the fragment-root default is stripped with it: the root form is
	// enforcement-side metadata, never published to callers.
	var servedMap map[string]any
	if err := json.Unmarshal(served, &servedMap); err != nil {
		t.Fatal(err)
	}
	servedItems := servedMap["paths"].(map[string]any)["/items"].(map[string]any)
	if _, leak := servedItems["x-seam-internal-required-scope"]; leak {
		t.Fatal("served spec leaks x-seam-internal-required-scope on the path item")
	}
	if _, leak := servedItems["x-required-scope"]; leak {
		t.Fatal("served spec leaks the fragment-root default as a path-item x-required-scope")
	}
}
