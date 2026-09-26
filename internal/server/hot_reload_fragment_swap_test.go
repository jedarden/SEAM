package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ardenone/seam/internal/spec"
)

// The tests in this file pin the hot-reload guarantees reloadRouteTable's log
// lines claim: an in-flight request keeps the routing decision it resolved from
// the previous table, new requests resolve against the reloaded table, and a
// reload that fails at build or validation time never reaches Swap — the
// previous table stays installed. Unlike the stress tests in
// route_table_hot_reload_test.go these are deterministic, sleep-free, and run
// in short mode.

// writeRouteFragment writes a one-route fragment declaring GET /reload/target
// routed to upstream under <fragmentsDir>/<service>/route.json — the layout
// FragmentLoader.LoadDirectory walks. The extensions ride on the operation
// because MergeFragments copies only the paths tree into the merged document;
// a fragment-root x-upstream would never reach BuildRouteTable.
func writeRouteFragment(t *testing.T, fragmentsDir, service, upstream string, withResponses bool) {
	t.Helper()

	// With withResponses the operation carries a responses object; without it
	// the key is omitted entirely — an empty "responses": {} is still a
	// present responses object to the OpenAPI model.
	responsesClause := ""
	if withResponses {
		responsesClause = `"responses": {"200": {"description": "ok"}},`
	}

	fragment := fmt.Sprintf(`{
		"x-seam-schema": "v1",
		"x-seam-owner": %q,
		"paths": {
			"/reload/target": {
				"get": {
					%s
					"x-api-version": "v1",
					"x-upstream": %q
				}
			}
		}
	}`, service, responsesClause, upstream)

	serviceDir := filepath.Join(fragmentsDir, service)
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatalf("create service dir %s: %v", serviceDir, err)
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "route.json"), []byte(fragment), 0o644); err != nil {
		t.Fatalf("write fragment for %s: %v", service, err)
	}
}

// newFragmentTableLoader loads fragmentsDir the way the serve path does and
// builds the route table a fresh server would install.
func newFragmentTableLoader(t *testing.T, fragmentsDir string) (*spec.Loader, *RouteTable) {
	t.Helper()

	loader, err := spec.NewWithFragments(t.TempDir(), "http://localhost:8080", "", fragmentsDir)
	if err != nil {
		t.Fatalf("NewWithFragments: %v", err)
	}
	table, err := BuildRouteTable(loader.OpenAPIModel())
	if err != nil {
		t.Fatalf("BuildRouteTable: %v", err)
	}
	if table == nil {
		t.Fatal("BuildRouteTable returned a nil table")
	}
	return loader, table
}

// TestFragmentReloadSwapKeepsInFlightOnPreviousTable pins the directionality of
// an atomic swap through the real fragment → merge → BuildRouteTable pipeline:
// the routing decision an in-flight request carries is a copy taken from the
// previous table and must survive a swap unchanged, while a request resolved
// after the swap routes to the reloaded upstream. A Swap that mutated the old
// table in place, or a Match that returned a shared reference instead of a
// copy, would turn this test red.
func TestFragmentReloadSwapKeepsInFlightOnPreviousTable(t *testing.T) {
	fragmentsDir := t.TempDir()
	writeRouteFragment(t, fragmentsDir, "svc", "https://upstream-v1.example", true)

	loader, tableV1 := newFragmentTableLoader(t, fragmentsDir)
	if got := tableV1.RouteCount(); got != 1 {
		t.Fatalf("initial route count = %d, want 1 (fixture broken)", got)
	}

	holder := NewThreadSafeTableHolder(tableV1)

	req, err := http.NewRequest(http.MethodGet, "/reload/target", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	// The in-flight request resolves its route from the previous table.
	inFlight, err := holder.Match(req)
	if err != nil {
		t.Fatalf("pre-swap Match: %v", err)
	}
	if got, want := inFlight.Route.UpstreamTarget, "https://upstream-v1.example"; got != want {
		t.Fatalf("pre-swap upstream = %q, want %q (fixture broken)", got, want)
	}

	// Hot reload: the fragment now routes the same path to a different
	// upstream. These are reloadRouteTable's own steps, minus the swap.
	writeRouteFragment(t, fragmentsDir, "svc", "https://upstream-v2.example", true)
	if err := loader.LoadFragments(); err != nil {
		t.Fatalf("LoadFragments: %v", err)
	}
	tableV2, err := BuildRouteTable(loader.OpenAPIModel())
	if err != nil {
		t.Fatalf("BuildRouteTable after reload: %v", err)
	}
	if got, want := tableV2.RouteCount(), 1; got != want {
		t.Fatalf("reloaded route count = %d, want %d", got, want)
	}
	if err := holder.Swap(tableV2); err != nil {
		t.Fatalf("Swap: %v", err)
	}

	// The in-flight decision is a copy from the previous table: the swap must
	// not reach it.
	if got, want := inFlight.Route.UpstreamTarget, "https://upstream-v1.example"; got != want {
		t.Errorf("in-flight upstream after swap = %q, want %q (the previous table's decision)", got, want)
	}

	// New requests resolve against the reloaded table.
	reloaded, err := holder.Match(req)
	if err != nil {
		t.Fatalf("post-swap Match: %v", err)
	}
	if got, want := reloaded.Route.UpstreamTarget, "https://upstream-v2.example"; got != want {
		t.Errorf("post-swap upstream = %q, want %q (the reloaded table's decision)", got, want)
	}
}

// TestHotReloadManagerReloadSwapsAndKeepsPreviousTableOnFailure drives
// HotReloadManager.reloadRouteTable itself: a successful reload swaps the
// reloaded table in, and a reload whose merged spec fails route-table build
// returns an error with the previous table still installed — a broken reload
// must never leave the gateway without routes.
func TestHotReloadManagerReloadSwapsAndKeepsPreviousTableOnFailure(t *testing.T) {
	fragmentsDir := t.TempDir()
	writeRouteFragment(t, fragmentsDir, "svc", "https://upstream-v1.example", true)

	loader, tableV1 := newFragmentTableLoader(t, fragmentsDir)
	holder := NewThreadSafeTableHolder(tableV1)
	srv := &Server{specLoader: loader, routeTableHolder: holder}
	mgr := NewHotReloadManager(srv)

	if got := mgr.ReloadCount(); got != 0 {
		t.Fatalf("fresh manager reload count = %d, want 0", got)
	}

	req, err := http.NewRequest(http.MethodGet, "/reload/target", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	// A successful reload installs the reloaded table.
	writeRouteFragment(t, fragmentsDir, "svc", "https://upstream-v2.example", true)
	if err := mgr.reloadRouteTable(); err != nil {
		t.Fatalf("reloadRouteTable: %v", err)
	}
	reloaded, err := holder.Match(req)
	if err != nil {
		t.Fatalf("Match after successful reload: %v", err)
	}
	if got, want := reloaded.Route.UpstreamTarget, "https://upstream-v2.example"; got != want {
		t.Errorf("upstream after successful reload = %q, want %q", got, want)
	}
	if got := mgr.ReloadCount(); got != 1 {
		t.Errorf("reload count = %d, want 1", got)
	}

	// A failing reload — the merged operation has no responses, which
	// BuildRouteTable rejects — must return an error and leave the previous
	// table installed.
	writeRouteFragment(t, fragmentsDir, "svc", "https://upstream-v3.example", false)
	if err := mgr.reloadRouteTable(); err == nil {
		t.Fatal("reloadRouteTable with a responses-less operation succeeded, want an error")
	}
	if got := mgr.FailureCount(); got != 1 {
		t.Errorf("failure count = %d, want 1", got)
	}
	kept, err := holder.Match(req)
	if err != nil {
		t.Fatalf("Match after failed reload: %v", err)
	}
	if got, want := kept.Route.UpstreamTarget, "https://upstream-v2.example"; got != want {
		t.Errorf("upstream after failed reload = %q, want %q (the previous table must survive a failed reload)", got, want)
	}
	if got := mgr.ReloadCount(); got != 1 {
		t.Errorf("reload count after failed reload = %d, want 1 (the failure must not count as a reload)", got)
	}
}
