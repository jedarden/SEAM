package spec

import (
	"strings"
	"testing"
)

func TestLintPathRewriteCoherence(t *testing.T) {
	root := t.TempDir()
	fragment := strings.Join([]string{
		"x-seam-schema: v1",
		"x-seam-owner: owner",
		"x-api-version: v1",
		"x-upstream: https://api.example.com",
		"x-instance-param: cluster",
		"x-upstream-strip-prefix: /k8s",
		"x-upstream-map:",
		"  _default:",
		"    url: https://api.example.com",
		"paths:",
		"  /k8s/{cluster}/api:",
		"    x-upstream-path-template: /api/{missing}",
		"    get:",
		"      responses:",
		"        '200':",
		"          description: ok",
		"  /not-k8s:",
		"    get:",
		"      responses:",
		"        '200':",
		"          description: ok",
	}, "\n")
	writeLintTestFragment(t, root, "owner", "route.yaml", fragment)
	report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	assertFindingCodes(t, report.Errors, []string{
		"path-rewrite.strip-prefix-not-prefix",
		"path-rewrite.template-param-missing",
	})
}

func TestLintPathRewriteRejectsDesignatedInstanceInExplicitTemplate(t *testing.T) {
	root := t.TempDir()
	fragment := strings.Join([]string{
		"x-seam-schema: v1",
		"x-seam-owner: owner",
		"x-api-version: v1",
		"x-upstream: https://api.example.com",
		"x-instance-param: cluster",
		"x-upstream-map:",
		"  _default:",
		"    url: https://api.example.com",
		"paths:",
		"  /k8s/{cluster}/api:",
		"    x-upstream-path-template: /api/{cluster}",
		"    get:",
		"      responses:",
		"        '200':",
		"          description: ok",
	}, "\n")
	writeLintTestFragment(t, root, "owner", "route.yaml", fragment)
	report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	assertFindingCodes(t, report.Errors, []string{"path-rewrite.template-instance-param"})
}

func TestValidatePathRewriteFragmentIsMergeTimeGuard(t *testing.T) {
	data := map[string]any{
		"x-upstream-strip-prefix": "/k8s",
		"paths": map[string]any{
			"/other": map[string]any{},
		},
	}
	if err := ValidatePathRewriteFragment(data); err == nil {
		t.Fatal("expected a fragment-wide rewrite coherence error")
	}
}

// Phase 10 Tests

func TestPhase10_InstanceParamRequiredWithMap(t *testing.T) {
	root := t.TempDir()
	fragment := strings.Join([]string{
		"x-seam-schema: v1",
		"x-seam-owner: k8s-fleet",
		"x-api-version: v1",
		"x-upstream-map:",
		"  prod-us-east:",
		"    url: https://k8s-prod-us-east.ardenone.svc.cluster.local:6443",
		"paths:",
		"  /k8s/{cluster}/pods:",
		"    get:",
		"      responses:",
		"        '200':",
		"          description: ok",
	}, "\n")
	writeLintTestFragment(t, root, "k8s-fleet", "route.yaml", fragment)
	report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	assertFindingCodes(t, report.Errors, []string{"instance-param.missing-with-map"})
}

func TestPhase10_InstanceParamForbiddenWithoutMap(t *testing.T) {
	root := t.TempDir()
	fragment := strings.Join([]string{
		"x-seam-schema: v1",
		"x-seam-owner: k8s-fleet",
		"x-api-version: v1",
		"x-upstream: https://k8s-api.example.com",
		"x-instance-param: cluster",
		"paths:",
		"  /k8s/{cluster}/pods:",
		"    get:",
		"      responses:",
		"        '200':",
		"          description: ok",
	}, "\n")
	writeLintTestFragment(t, root, "k8s-fleet", "route.yaml", fragment)
	report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	assertFindingCodes(t, report.Errors, []string{"instance-param.forbidden-without-map"})
}

func TestPhase10_InstanceParamMustExistInEveryPath(t *testing.T) {
	root := t.TempDir()
	fragment := strings.Join([]string{
		"x-seam-schema: v1",
		"x-seam-owner: k8s-fleet",
		"x-api-version: v1",
		"x-instance-param: cluster",
		"x-upstream-map:",
		"  prod:",
		"    url: https://k8s-prod.example.com",
		"paths:",
		"  /k8s/{cluster}/pods:",
		"    get:",
		"      responses:",
		"        '200':",
		"          description: ok",
		"  /k8s/namespaces:", // Missing {cluster} parameter
		"    get:",
		"      responses:",
		"        '200':",
		"          description: ok",
	}, "\n")
	writeLintTestFragment(t, root, "k8s-fleet", "route.yaml", fragment)
	report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	// Should get error from pathRewriteFindings for missing instance param in path
	assertFindingCodes(t, report.Errors, []string{"path-rewrite.instance-param-missing"})
}

func TestPhase10_MapEntryRequiresUrl(t *testing.T) {
	root := t.TempDir()
	fragment := strings.Join([]string{
		"x-seam-schema: v1",
		"x-seam-owner: k8s-fleet",
		"x-api-version: v1",
		"x-instance-param: cluster",
		"x-upstream-map:",
		"  prod:",
		"    vaultPath: seam/routes/k8s-fleet/prod",
		"    injectAs:",
		"      kind: bearer",
		"paths:",
		"  /k8s/{cluster}/pods:",
		"    get:",
		"      responses:",
		"        '200':",
		"          description: ok",
	}, "\n")
	writeLintTestFragment(t, root, "k8s-fleet", "route.yaml", fragment)
	report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	assertFindingCodes(t, report.Errors, []string{"upstream-map.missing-url"})
}

func TestPhase10_MapEntryVaultInjectPairing(t *testing.T) {
	root := t.TempDir()
	fragment := strings.Join([]string{
		"x-seam-schema: v1",
		"x-seam-owner: k8s-fleet",
		"x-api-version: v1",
		"x-instance-param: cluster",
		"x-upstream-map:",
		"  prod:",
		"    url: https://k8s-prod.example.com",
		"    vaultPath: seam/routes/k8s-fleet/prod",
		"    # injectAs is missing",
		"paths:",
		"  /k8s/{cluster}/pods:",
		"    get:",
		"      responses:",
		"        '200':",
		"          description: ok",
	}, "\n")
	writeLintTestFragment(t, root, "k8s-fleet", "route.yaml", fragment)
	report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	assertFindingCodes(t, report.Errors, []string{"upstream-map.vault-inject-pairing"})
}

func TestPhase10_MapEntryInvalidScope(t *testing.T) {
	root := t.TempDir()
	fragment := strings.Join([]string{
		"x-seam-schema: v1",
		"x-seam-owner: k8s-fleet",
		"x-api-version: v1",
		"x-instance-param: cluster",
		"x-upstream-map:",
		"  prod:",
		"    url: https://k8s-prod.example.com",
		"    requiredScope: 123", // Invalid: not a string or array
		"paths:",
		"  /k8s/{cluster}/pods:",
		"    get:",
		"      responses:",
		"        '200':",
		"          description: ok",
	}, "\n")
	writeLintTestFragment(t, root, "k8s-fleet", "route.yaml", fragment)
	report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	// This should fail at the schema level since requiredScope must be a string or array
	// The Go validator adds an additional check
	if len(report.Errors) == 0 {
		t.Error("expected validation error for invalid requiredScope type")
	}
}

func TestPhase10_WorkedExample_K8sFleetWithStripPrefix(t *testing.T) {
	root := t.TempDir()
	fragment := strings.Join([]string{
		"x-seam-schema: v1",
		"x-seam-owner: k8s-fleet",
		"x-api-version: v1",
		"x-instance-param: cluster",
		"x-upstream-strip-prefix: /k8s",
		"x-upstream-map:",
		"  prod-us-east:",
		"    url: https://k8s-prod-us-east.ardenone.svc.cluster.local:6443",
		"    vaultPath: seam/routes/k8s-fleet/prod-us-east",
		"    injectAs:",
		"      kind: bearer",
		"  prod-us-west:",
		"    url: https://k8s-prod-us-west.ardenone.svc.cluster.local:6443",
		"    vaultPath: seam/routes/k8s-fleet/prod-us-west",
		"    injectAs:",
		"      kind: bearer",
		"x-fanout-scope:",
		"  - k8s-fleet:fanout",
		"paths:",
		"  /k8s/{cluster}/api/v1/pods:",
		"    get:",
		"      summary: List pods in a cluster",
		"      operationId: listPods",
		"      parameters:",
		"      - name: cluster",
		"        in: path",
		"        required: true",
		"        schema:",
		"          type: string",
		"        description: Cluster name",
		"      responses:",
		"        '200':",
		"          description: Pod list",
		"          content:",
		"            application/json:",
		"              schema:",
		"                type: array",
		"        '401':",
		"          description: Unauthorized",
	}, "\n")
	writeLintTestFragment(t, root, "k8s-fleet", "route.yaml", fragment)
	report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatal(err)
	}

	// This example should pass all validation
	if report.HasErrors() {
		t.Errorf("worked example should pass validation, got errors: %v", report.Errors)
	}

	// Verify the fragment parsed correctly
	// Path: /k8s/{cluster}/api/v1/pods
	// After strip-prefix /k8s: /{cluster}/api/v1/pods
	// After instance segment deletion: /api/v1/pods
	// Final: entry url + /api/v1/pods
	if len(report.Errors) > 0 {
		for _, e := range report.Errors {
			t.Logf("Unexpected error: %s - %s", e.Code, e.Message)
		}
	}
}

func TestPhase10_ValidMultiInstanceFragment(t *testing.T) {
	root := t.TempDir()
	fragment := strings.Join([]string{
		"x-seam-schema: v1",
		"x-seam-owner: geo-service",
		"x-api-version: v1",
		"x-instance-param: region",
		"x-upstream-map:",
		"  us-east:",
		"    url: https://geo-service.us-east.ardenone.internal",
		"    vaultPath: seam/routes/geo-service/us-east-key",
		"    injectAs:",
		"      kind: header",
		"      name: X-Region-Key",
		"    probeInterval: 5m",
		"    breaker:",
		"      threshold: 8",
		"      openSeconds: 30",
		"    requiredScope: geo-service:us-east",
		"  us-west:",
		"    url: https://geo-service.us-west.ardenone.internal",
		"    vaultPath: seam/routes/geo-service/us-west-key",
		"    injectAs:",
		"      kind: header",
		"      name: X-Region-Key",
		"    probeInterval: 5m",
		"    breaker:",
		"      threshold: 8",
		"      openSeconds: 30",
		"    requiredScope: geo-service:us-west",
		"x-breaker:",
		"  threshold: 10",
		"  openSeconds: 45",
		"x-required-scope: geo-service:query",
		"paths:",
		"  /geo/{region}/location:",
		"    get:",
		"      summary: Geocode location to coordinates",
		"      parameters:",
		"      - name: region",
		"        in: path",
		"        required: true",
		"        schema:",
		"          type: string",
		"      responses:",
		"        '200':",
		"          description: Geocoding successful",
		"  /geo/{region}/reverse:",
		"    get:",
		"      summary: Reverse geocode",
		"      parameters:",
		"      - name: region",
		"        in: path",
		"        required: true",
		"        schema:",
		"          type: string",
		"      responses:",
		"        '200':",
		"          description: Reverse geocoding successful",
	}, "\n")
	writeLintTestFragment(t, root, "geo-service", "route.yaml", fragment)
	report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatal(err)
	}

	// This should pass all validation
	if report.HasErrors() {
		t.Errorf("valid multi-instance fragment should pass, got errors: %v", report.Errors)
	}
}

func TestPhase10_FanoutScopeParsing(t *testing.T) {
	// Test that x-fanout-scope is parsed and carried
	// This is PARSE AND CARRY only - enforcement is Phase 7
	root := t.TempDir()
	fragment := strings.Join([]string{
		"x-seam-schema: v1",
		"x-seam-owner: k8s-fleet",
		"x-api-version: v1",
		"x-instance-param: cluster",
		"x-upstream-map:",
		"  prod:",
		"    url: https://k8s-prod.example.com",
		"x-fanout-scope:",
		"  - k8s-fleet:fanout",
		"  - k8s-fleet:admin",
		"paths:",
		"  /k8s/{cluster}/pods:",
		"    get:",
		"      responses:",
		"        '200':",
		"          description: ok",
	}, "\n")
	writeLintTestFragment(t, root, "k8s-fleet", "route.yaml", fragment)
	report, err := LintDirectory(LintOptions{FragmentsDir: root, SchemaPath: lintTestSchemaPath(t)})
	if err != nil {
		t.Fatal(err)
	}

	// x-fanout-scope should parse without errors
	if report.HasErrors() {
		t.Errorf("x-fanout-scope should parse successfully, got errors: %v", report.Errors)
	}
}
