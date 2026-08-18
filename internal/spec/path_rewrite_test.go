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
