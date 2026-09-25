package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/spec"
)

// newRouteTableHealthTestServer builds a server whose upstream-health sources
// are empty so route_table assertions are not polluted by breaker or
// last-2xx state.
func newRouteTableHealthTestServer() *Server {
	return &Server{
		circuitBreakers: NewCircuitBreakerStateRegistry(),
		last2xxTracker:  NewLast2xxTracker(),
	}
}

// writeCollisionFragment writes a fragment claiming GET /ping under its own
// service directory, so a second copy of the same body collides with the
// first and is quarantined by DetectPathCollisions.
func writeCollisionFragment(t *testing.T, fragmentsDir, service string) {
	t.Helper()

	serviceDir := filepath.Join(fragmentsDir, service)
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatalf("create service dir %s: %v", serviceDir, err)
	}

	const fragmentYAML = `x-seam-schema: test
x-api-version: _unversioned
paths:
  /ping:
    get:
      responses:
        "200":
          description: ok
`
	if err := os.WriteFile(filepath.Join(serviceDir, "route.yaml"), []byte(fragmentYAML), 0o644); err != nil {
		t.Fatalf("write fragment for %s: %v", service, err)
	}
}

func TestHealthUpstreamsRouteTableFragmentCounts(t *testing.T) {
	fragmentsDir := t.TempDir()
	writeCollisionFragment(t, fragmentsDir, "svc-a")
	writeCollisionFragment(t, fragmentsDir, "svc-b")

	fl, err := spec.NewFragmentLoader()
	if err != nil {
		t.Fatalf("NewFragmentLoader: %v", err)
	}
	if err := fl.LoadDirectory(fragmentsDir); err != nil {
		t.Fatalf("LoadDirectory: %v", err)
	}
	fl.DetectPathCollisions()

	// Sanity: the fixture must actually produce loaded-vs-quarantined split.
	if got := fl.GetValidFragmentCount(); got != 1 {
		t.Fatalf("valid fragment count = %d, want 1 (fixture broken)", got)
	}
	if got := fl.GetQuarantinedCount(); got != 1 {
		t.Fatalf("quarantined fragment count = %d, want 1 (fixture broken)", got)
	}

	table := NewRouteTable(nil)
	for _, path := range []string{"/one", "/two"} {
		table.AddRoute(RouteEntry{
			PathTemplate:   path,
			Method:         http.MethodGet,
			APIVersion:     "_unversioned",
			UpstreamTarget: "https://upstream.example.test",
		})
	}

	srv := newRouteTableHealthTestServer()
	srv.specLoader = &spec.Loader{FragmentLoader: fl}
	srv.routeTableHolder = NewThreadSafeTableHolder(table)

	rec := httptest.NewRecorder()
	srv.healthUpstreamsHandler(rec, httptest.NewRequest(http.MethodGet, "/health/upstreams", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var body UpstreamHealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	rt := body.RouteTable
	if !rt.FragmentMode {
		t.Error("route_table.fragment_mode = false, want true")
	}
	if rt.FragmentsLoaded != 1 {
		t.Errorf("route_table.fragments_loaded = %d, want 1", rt.FragmentsLoaded)
	}
	if rt.FragmentsQuarantined != 1 {
		t.Errorf("route_table.fragments_quarantined = %d, want 1", rt.FragmentsQuarantined)
	}
	if rt.Routes != 2 {
		t.Errorf("route_table.routes = %d, want 2", rt.Routes)
	}
	if rt.LastLoaded == nil {
		t.Fatal("route_table.last_loaded absent, want the LoadDirectory timestamp")
	}
	if age := time.Since(*rt.LastLoaded); age < 0 || age > time.Minute {
		t.Errorf("route_table.last_loaded = %v, want a fresh timestamp", *rt.LastLoaded)
	}
	if rt.HotReload != nil {
		t.Errorf("route_table.hot_reload = %+v, want absent without a hot-reload manager", rt.HotReload)
	}
}

func TestHealthUpstreamsRouteTableStaticMode(t *testing.T) {
	srv := newRouteTableHealthTestServer()
	srv.specLoader = &spec.Loader{} // static mode: no fragment loader

	rec := httptest.NewRecorder()
	srv.healthUpstreamsHandler(rec, httptest.NewRequest(http.MethodGet, "/health/upstreams", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var raw struct {
		RouteTable map[string]json.RawMessage `json:"route_table"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := raw.RouteTable["fragment_mode"]; !ok {
		t.Fatal("route_table.fragment_mode missing, want explicit false")
	}
	for _, key := range []string{"last_loaded", "hot_reload"} {
		if _, ok := raw.RouteTable[key]; ok {
			t.Errorf("route_table.%s present in static mode, want absent", key)
		}
	}

	var rt RouteTableHealth
	rtBytes, err := json.Marshal(raw.RouteTable)
	if err != nil {
		t.Fatalf("re-encode route_table keys: %v", err)
	}
	if err := json.Unmarshal(rtBytes, &rt); err != nil {
		t.Fatalf("decode route_table: %v", err)
	}
	if rt.FragmentMode || rt.FragmentsLoaded != 0 || rt.FragmentsQuarantined != 0 || rt.Routes != 0 {
		t.Errorf("static-mode route_table = %+v, want fragment_mode false and zero counts", rt)
	}
}

func TestHealthUpstreamsRouteTableHotReloadProjection(t *testing.T) {
	srv := newRouteTableHealthTestServer()
	mgr := NewHotReloadManager(srv)
	srv.hotReloadManager = mgr

	// A manager that was never enabled has zero counts and no successful
	// reload; the projection must render last_reload_time as null either way.
	rec := httptest.NewRecorder()
	srv.healthUpstreamsHandler(rec, httptest.NewRequest(http.MethodGet, "/health/upstreams", nil))
	var fresh UpstreamHealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &fresh); err != nil {
		t.Fatalf("decode fresh-manager response: %v", err)
	}
	if fresh.RouteTable.HotReload == nil {
		t.Fatal("route_table.hot_reload absent with a hot-reload manager, want present")
	}
	if fresh.RouteTable.HotReload.Enabled || fresh.RouteTable.HotReload.ReloadCount != 0 || fresh.RouteTable.HotReload.FailureCount != 0 {
		t.Errorf("fresh-manager hot_reload = %+v, want disabled with zero counts", fresh.RouteTable.HotReload)
	}
	if fresh.RouteTable.HotReload.LastReload != nil {
		t.Errorf("fresh-manager last_reload_time = %v, want null", *fresh.RouteTable.HotReload.LastReload)
	}

	// Inject reload state (same package) and pin the full projection.
	mgr.enabled = true
	mgr.reloadInProgress = true
	mgr.reloadCount = 3
	mgr.failCount = 2
	lastReload := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	mgr.lastReloadTime = lastReload

	rec = httptest.NewRecorder()
	srv.healthUpstreamsHandler(rec, httptest.NewRequest(http.MethodGet, "/health/upstreams", nil))
	var after UpstreamHealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode reloaded-manager response: %v", err)
	}
	hot := after.RouteTable.HotReload
	if hot == nil {
		t.Fatal("route_table.hot_reload absent after injecting manager state")
	}
	if !hot.Enabled {
		t.Error("hot_reload.enabled = false, want true")
	}
	if !hot.InProgress {
		t.Error("hot_reload.in_progress = false, want true")
	}
	if hot.ReloadCount != 3 {
		t.Errorf("hot_reload.reload_count = %d, want 3", hot.ReloadCount)
	}
	if hot.FailureCount != 2 {
		t.Errorf("hot_reload.failure_count = %d, want 2", hot.FailureCount)
	}
	if hot.LastReload == nil || !hot.LastReload.Equal(lastReload) {
		t.Errorf("hot_reload.last_reload_time = %v, want %v", hot.LastReload, lastReload)
	}
}

func TestHealthUpstreamsRouteTableNilSources(t *testing.T) {
	// No spec loader, no route table holder, no hot-reload manager: the
	// endpoint must still answer 200 with route_table reporting zeros.
	srv := newRouteTableHealthTestServer()

	rec := httptest.NewRecorder()
	srv.healthUpstreamsHandler(rec, httptest.NewRequest(http.MethodGet, "/health/upstreams", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var body UpstreamHealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.RouteTable.FragmentMode || body.RouteTable.Routes != 0 || body.RouteTable.FragmentsLoaded != 0 || body.RouteTable.FragmentsQuarantined != 0 {
		t.Errorf("route_table = %+v, want fragment_mode false and zero counts", body.RouteTable)
	}
}
