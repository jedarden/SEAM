package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/spec"
	"github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
	"go.yaml.in/yaml/v4"
)

// The tests in this file are the acceptance gate for the retirement handoff:
// the workflow where the retirement evaluator DETECTS a quiet route version
// and a human lands the x-seam-deprecated block it proposed as an ordinary
// declarative-config commit. The full procedure is
// docs/retirement-handoff-runbook.md; the runtime semantics of the block are
// docs/notes/brownout-runtime-semantics.md. What this file pins is the
// SEAM-side half of the contract the evaluator's own output-contract tests
// (tools/seam-retirement-evaluator/output_contract_test.go) pin the other
// half of:
//
//   - the block the evaluator proposes, pasted at the fragment root where the
//     route-fragment schema sanctions it, is accepted by the real fragment
//     load → merge → route-table path — not just by lint;
//   - the acceptance travels the hot-reload path (Loader.LoadFragments →
//     BuildRouteTable → ThreadSafeTableHolder.Swap), so landing a verdict is
//     a reload, not a deployment;
//   - acceptance introduces no write path: the whole flow only reads the
//     fragments tree. (The evaluator half — no git credential, no exec — is
//     tools/seam-retirement-evaluator/write_contract_test.go.)

// evaluatorProposalBlock is the x_seam_deprecated_block field of the
// evaluator's structured log record, verbatim, for a route detected quiet on
// 2026-09-26 at 11:35:23Z. The dates are derived from that detection instant
// the way RetirementEvaluator.emitRetirementFinding derives them (fixed so
// the test is deterministic; do not re-format):
//
//	since  = the detection date
//	sunset = since + 90 days
//	window 1 = since+30d  → since+37d   (start/end keep the detection
//	window 2 = since+60d  → since+67d    time-of-day, 11:35:23Z)
//	window 3 = sunset−7d midnight → sunset midnight
//
// If the evaluator's emission format changes, its
// TestXSeamDeprecatedBlockIsFragmentShaped fails there; update this golden
// copy in the same change and this test keeps proving the proposed shape is
// the accepted shape.
const evaluatorProposalBlock = `x-seam-deprecated:
  since: "2026-09-26"
  sunset: "2026-12-25"
  brownout:
    - start: "2026-10-26T11:35:23Z"
      end: "2026-11-02T11:35:23Z"
    - start: "2026-11-25T11:35:23Z"
      end: "2026-12-02T11:35:23Z"
    - start: "2026-12-18T00:00:00Z"
      end: "2026-12-25T00:00:00Z"
`

// writeLegacyServiceFragment writes the legacy-service route fragment. With
// withBlock the fragment carries the evaluator's proposal at the fragment
// root — the placement the runbook prescribes and the schema sanctions; the
// proposed block is a fragment-root object covering every path the fragment
// declares.
func writeLegacyServiceFragment(t *testing.T, fragmentsDir string, withBlock bool) string {
	t.Helper()

	serviceDir := filepath.Join(fragmentsDir, "legacy-service")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatalf("create service dir: %v", err)
	}

	fragment := `x-seam-schema: v1
x-seam-owner: legacy-service
x-api-version: v1
x-upstream: https://legacy-service.example.internal
`
	if withBlock {
		fragment += evaluatorProposalBlock
	}
	fragment += `openapi: 3.1.0
info:
  title: legacy-service
  version: "1.0.0"
paths:
  /old-route:
    get:
      responses:
        "200":
          description: ok
`

	path := filepath.Join(serviceDir, "route.yaml")
	if err := os.WriteFile(path, []byte(fragment), 0o644); err != nil {
		t.Fatalf("write fragment: %v", err)
	}
	return path
}

func fragmentsSchemaPath(t *testing.T) string {
	t.Helper()
	schema, err := filepath.Abs(filepath.Join("..", "..", "spec", "route-fragment-schema.json"))
	if err != nil {
		t.Fatalf("resolve schema path: %v", err)
	}
	if _, err := os.Stat(schema); err != nil {
		t.Fatalf("route-fragment-schema.json not found next to the repo root: %v", err)
	}
	return schema
}

// fragmentsTreeDigest hashes every file in the fragments tree so a test can
// prove the acceptance path read the tree without writing any of it.
func fragmentsTreeDigest(t *testing.T, fragmentsDir string) string {
	t.Helper()
	digest := sha256.New()
	err := filepath.Walk(fragmentsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, chunk := range [][]byte{[]byte(path), payload} {
			if _, err := digest.Write(chunk); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("hash fragments tree: %v", err)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func buildTableFromLoader(t *testing.T, loader *spec.Loader) *RouteTable {
	t.Helper()
	table, err := BuildRouteTable(loader.OpenAPIModel())
	if err != nil {
		t.Fatalf("BuildRouteTable: %v", err)
	}
	return table
}

func findRouteEntry(t *testing.T, table *RouteTable, pathTemplate string) RouteEntry {
	t.Helper()
	for _, route := range table.GetRoutes() {
		if route.PathTemplate == pathTemplate {
			return route
		}
	}
	t.Fatalf("route %s not found in built table", pathTemplate)
	return RouteEntry{}
}

// TestRetirementHandoff_EvaluatorProposalAcceptedByHotReload walks the whole
// handoff in one continuous story: an undeprecated fragment is serving, the
// evaluator's proposed block is landed at the fragment root (the human
// commit the runbook describes), lint accepts it, the hot-reload path picks
// it up, the route table swap makes it caller-visible as Deprecation/Sunset
// headers outside windows and a 410 inside one — and none of that wrote
// anything.
func TestRetirementHandoff_EvaluatorProposalAcceptedByHotReload(t *testing.T) {
	fragmentsDir := t.TempDir()
	writeLegacyServiceFragment(t, fragmentsDir, false)
	schemaPath := fragmentsSchemaPath(t)
	baseURL := "http://gateway.example"

	loader, err := spec.NewWithFragments(fragmentsDir, baseURL, schemaPath, fragmentsDir)
	if err != nil {
		t.Fatalf("NewWithFragments: %v", err)
	}
	if dep := findRouteEntry(t, buildTableFromLoader(t, loader), "/old-route").Deprecated; dep != nil {
		t.Fatalf("pre-handoff route already deprecated: %+v", dep)
	}

	// The human commit: paste the evaluator's proposed block at the fragment
	// root, then run the pre-land gate the runbook prescribes. The commit is
	// the one write in the whole handoff and it belongs to the human, not to
	// SEAM — the no-write baseline below is taken after it.
	fragmentPath := writeLegacyServiceFragment(t, fragmentsDir, true)
	digestBefore := fragmentsTreeDigest(t, fragmentsDir)
	report, err := spec.LintFiles([]string{fragmentPath}, spec.LintOptions{
		FragmentsDir: fragmentsDir,
		SchemaPath:   schemaPath,
	})
	if err != nil {
		t.Fatalf("LintFiles: %v", err)
	}
	if report.HasErrors() {
		t.Fatalf("evaluator proposal rejected by lint: %+v", report.Errors)
	}

	// SEAM observes the change through the hot-reload path — the same
	// Loader.LoadFragments call HotReloadManager.reloadRouteTable makes.
	if err := loader.LoadFragments(); err != nil {
		t.Fatalf("LoadFragments: %v", err)
	}
	table := buildTableFromLoader(t, loader)

	dep := findRouteEntry(t, table, "/old-route").Deprecated
	if dep == nil {
		t.Fatal("landed proposal never reached the route table; fragment-root x-seam-deprecated was dropped")
	}
	if dep.Since != "2026-09-26" || dep.Sunset != "2026-12-25" {
		t.Errorf("Since/Sunset = %q/%q, want 2026-09-26/2026-12-25", dep.Since, dep.Sunset)
	}
	wantWindows := []BrownoutWindow{
		{Start: "2026-10-26T11:35:23Z", End: "2026-11-02T11:35:23Z"},
		{Start: "2026-11-25T11:35:23Z", End: "2026-12-02T11:35:23Z"},
		{Start: "2026-12-18T00:00:00Z", End: "2026-12-25T00:00:00Z"},
	}
	if len(dep.Brownouts) != len(wantWindows) {
		t.Fatalf("Brownouts = %+v, want %d windows", dep.Brownouts, len(wantWindows))
	}
	for i, want := range wantWindows {
		if dep.Brownouts[i] != want {
			t.Errorf("Brownouts[%d] = %+v, want %+v", i, dep.Brownouts[i], want)
		}
	}

	// The atomic swap HotReloadManager performs after a successful rebuild —
	// the verdict is live without a deployment.
	holder := NewThreadSafeTableHolder(table)
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})
	scheduler := NewBrownoutScheduler()
	server := &Server{routeTableHolder: holder, brownoutScheduler: scheduler}
	handler := server.brownoutMiddleware(next)

	serveAt := func(clock string) *httptest.ResponseRecorder {
		t.Helper()
		scheduler.SetClock(func() time.Time { return mustClock(t, clock) })
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/old-route", nil))
		return rec
	}

	// Between declaration and the first window: advertised as deprecated,
	// still served.
	rec := serveAt("2026-10-20T00:00:00Z")
	if !nextCalled || rec.Code != http.StatusOK {
		t.Errorf("outside windows: status = %d, nextCalled = %v, want 200 and served", rec.Code, nextCalled)
	}
	if dep := rec.Header().Get("Deprecation"); dep != "since=2026-09-26" {
		t.Errorf("outside windows: Deprecation = %q, want since=2026-09-26", dep)
	}
	if sun := rec.Header().Get("Sunset"); sun != "2026-12-25" {
		t.Errorf("outside windows: Sunset = %q, want 2026-12-25", sun)
	}

	// Inside window 1: the scheduled 410.
	nextCalled = false
	rec = serveAt("2026-10-27T12:00:00Z")
	if nextCalled || rec.Code != http.StatusGone {
		t.Errorf("inside window 1: status = %d, nextCalled = %v, want 410 and not served", rec.Code, nextCalled)
	}
	if marker := rec.Header().Get("X-SEAM-Brownout"); marker != "active" {
		t.Errorf("inside window 1: X-SEAM-Brownout = %q, want active", marker)
	}

	// Past sunset: advisory only — served again, still advertised.
	rec = serveAt("2027-01-15T00:00:00Z")
	if !nextCalled || rec.Code != http.StatusOK {
		t.Errorf("past sunset: status = %d, nextCalled = %v, want 200 and served", rec.Code, nextCalled)
	}
	if rec.Header().Get("Deprecation") == "" {
		t.Error("past sunset: Deprecation header absent, want still advertised")
	}

	// And the whole acceptance read the fragments tree without writing it.
	if digestAfter := fragmentsTreeDigest(t, fragmentsDir); digestAfter != digestBefore {
		t.Error("acceptance path modified the fragments tree; the handoff must stay read-only on the SEAM side")
	}
}

// mustExtensions builds a PathItem extension map from key/raw-YAML pairs, so
// extraction can be exercised the way a parsed fragment populates it.
func mustExtensions(t *testing.T, pairs ...string) *orderedmap.Map[string, *yaml.Node] {
	t.Helper()
	extensions := orderedmap.New[string, *yaml.Node]()
	for i := 0; i+1 < len(pairs); i += 2 {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(pairs[i+1]), &node); err != nil {
			t.Fatalf("unmarshal extension %s: %v", pairs[i], err)
		}
		// yaml.Unmarshal of a mapping wraps it in a document node; the
		// extension value is the mapping itself, as the parser produces.
		value := &node
		if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
			value = node.Content[0]
		}
		extensions.Set(pairs[i], value)
	}
	return extensions
}

// TestExtractDeprecation_PropagatedFragmentRootMarker pins the extraction
// order behind the end-to-end acceptance above: a path-item-level
// x-seam-deprecated (the form a static spec or a hand-placed override uses)
// wins, and the x-seam-internal-deprecated marker PropagateRouteMetadata
// stamps for a fragment-root block is honored when no plain key is present.
func TestExtractDeprecation_PropagatedFragmentRootMarker(t *testing.T) {
	markerOnly := &v3.PathItem{
		Extensions: mustExtensions(t,
			"x-seam-internal-deprecated", `since: "2026-09-26"`),
	}
	dep, err := extractDeprecation(nil, markerOnly, nil)
	if err != nil {
		t.Fatalf("marker-only extraction: %v", err)
	}
	if dep == nil || dep.Since != "2026-09-26" {
		t.Fatalf("marker-only extraction = %+v, want Since 2026-09-26", dep)
	}

	both := &v3.PathItem{
		Extensions: mustExtensions(t,
			"x-seam-deprecated", `since: "2020-01-01"`,
			"x-seam-internal-deprecated", `since: "2026-09-26"`),
	}
	dep, err = extractDeprecation(nil, both, nil)
	if err != nil {
		t.Fatalf("plain-over-marker extraction: %v", err)
	}
	if dep == nil || dep.Since != "2020-01-01" {
		t.Fatalf("plain-over-marker extraction = %+v, want the plain key to win (Since 2020-01-01)", dep)
	}
}
