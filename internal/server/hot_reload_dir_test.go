package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/spec"
)

// The tests below pin the hot-reload watch root documented in README.md
// ("Fragment directory and hot-reload scope"): the watcher roots at the
// resolved fragments directory, falling back to <spec-dir>/fragments.d only
// when that directory is empty — the same rule spec.NewWithFragments applies
// when loading, so a reload re-reads the tree that was watched.

func TestRoutesMountDirPrefersConfiguredFragmentsDir(t *testing.T) {
	got := routesMountDir(&Config{FragmentsDir: "/etc/gateway/routes.d", SpecDir: "/spec"})
	if want := "/etc/gateway/routes.d"; got != want {
		t.Fatalf("routesMountDir = %q, want %q", got, want)
	}
}

func TestRoutesMountDirFallsBackToSpecDirFragmentsD(t *testing.T) {
	got := routesMountDir(&Config{SpecDir: "/spec"})
	if want := filepath.Join("/spec", "fragments.d"); got != want {
		t.Fatalf("routesMountDir = %q, want %q", got, want)
	}
}

// Hot reload is fragment-mode-only: Enable refuses a static-mode server before
// any watcher is created.
func TestHotReloadEnableRequiresFragmentMode(t *testing.T) {
	hrm := NewHotReloadManager(&Server{config: &Config{FragmentsDir: t.TempDir()}})

	if err := hrm.Enable(); err == nil {
		t.Fatal("Enable on a non-fragment-mode server succeeded, want an error")
	}
	if hrm.enabled {
		t.Error("hot reload manager reported enabled after a refused Enable")
	}
	if status := hrm.Status(); status["enabled"] != false {
		t.Errorf("status[enabled] = %v, want false", status["enabled"])
	}
}

// A fragments directory that does not exist is not a startup error: the
// manager enables with nothing to watch, so adding the directory later still
// needs a restart — documented as "missing fragments directory means no
// reloads", not a failure.
func TestHotReloadEnableToleratesMissingRoutesDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	hrm := NewHotReloadManager(&Server{config: &Config{
		FragmentMode: true,
		FragmentsDir: missing,
	}})

	if err := hrm.Enable(); err != nil {
		t.Fatalf("Enable with a missing fragments directory: %v", err)
	}
	if !hrm.enabled {
		t.Error("hot reload manager not enabled after Enable")
	}

	hrm.Disable()
	if hrm.enabled {
		t.Error("hot reload manager still enabled after Disable")
	}
}

// fragmentJSONFixture renders a minimal valid fragment declaring routePaths.
func fragmentJSONFixture(owner string, routePaths ...string) string {
	var paths strings.Builder
	for i, routePath := range routePaths {
		if i > 0 {
			paths.WriteString(",")
		}
		fmt.Fprintf(&paths, "\n\t\t\t%q: {\"get\": {\"responses\": {\"200\": {\"description\": \"ok\"}}}}", routePath)
	}
	return fmt.Sprintf(`{
		"x-seam-schema": "v1",
		"x-seam-owner": %q,
		"x-api-version": "v1",
		"x-upstream": "https://upstream.example",
		"paths": {%s
		}
	}`, owner, paths.String())
}

// writeProjectionRevision creates a Kubernetes atomic-writer revision under
// mount: a ..<rev> payload directory holding the real fragment files, a ..data
// symlink to it, and a canonical symlink for every file path through ..data —
// the layout LoadDirectory walks and MountWatcher watches. When retire names a
// previous payload directory it is removed last, the way kubelet retires old
// revisions; the fsnotify watch on ..data dereferences to the retiring payload,
// so that removal is the event that signals the swap. Returns the new payload
// directory.
func writeProjectionRevision(t *testing.T, mount, rev string, files map[string]string, retire string) string {
	t.Helper()
	payload := filepath.Join(mount, ".."+rev)
	for rel, contents := range files {
		path := filepath.Join(payload, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Point ..data at the new payload the way kubelet does: symlink a
	// temporary name, then rename it over ..data so the swap is atomic.
	dataLink := filepath.Join(mount, "..data")
	tmpLink := filepath.Join(mount, "..data-new")
	if err := os.Symlink(".."+rev, tmpLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpLink, dataLink); err != nil {
		t.Fatal(err)
	}
	for rel := range files {
		canonical := filepath.Join(mount, rel)
		if _, err := os.Lstat(canonical); err == nil {
			// Created with an earlier revision; it keeps pointing through
			// ..data, which now resolves to the new payload.
			continue
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
			t.Fatal(err)
		}
		depth := 0
		if dir := filepath.Dir(rel); dir != "." {
			depth = len(strings.Split(dir, "/"))
		}
		up := strings.Repeat("../", depth)
		if err := os.Symlink(up+"..data/"+rel, canonical); err != nil {
			t.Fatal(err)
		}
	}
	if retire != "" {
		if err := os.RemoveAll(retire); err != nil {
			t.Fatal(err)
		}
	}
	return payload
}

// waitForRouteTablePaths polls the holder until the live route set equals want,
// failing with the observed set and the hot-reload status when the deadline
// passes.
func waitForRouteTablePaths(t *testing.T, holder *ThreadSafeTableHolder, hrm *HotReloadManager, want []string) {
	t.Helper()
	wantSet := make(map[string]bool, len(want))
	for _, path := range want {
		wantSet[path] = true
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		live := make(map[string]bool)
		for _, route := range holder.Snapshot() {
			live[route.PathTemplate] = true
		}
		matches := len(live) == len(wantSet)
		for path := range wantSet {
			if !live[path] {
				matches = false
			}
		}
		if matches {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("route table never reached %v (live: %v); hot-reload status: %v", want, live, hrm.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Enable roots the watcher at the resolved fragments directory, so an atomic
// ConfigMap-style revision under the resolved tree drives a reload that
// re-reads it. A legacy <spec-dir>/fragments.d route sits the test out: with
// FragmentsDir configured, the legacy tree is neither loaded nor watched.
func TestHotReloadWatchRootsAtResolvedFragmentsDir(t *testing.T) {
	resolved := t.TempDir()
	unrelatedSpec := t.TempDir()
	legacyRoute := filepath.Join(unrelatedSpec, "fragments.d", "legacy-owner", "route.json")
	if err := os.MkdirAll(filepath.Dir(legacyRoute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyRoute, []byte(fragmentJSONFixture("legacy-owner", "/legacy/path")), 0o644); err != nil {
		t.Fatal(err)
	}

	service := filepath.Join(resolved, "service-a")
	v1 := writeProjectionRevision(t, service, "rev-1", map[string]string{
		"route.json": fragmentJSONFixture("service-a", "/a/original"),
	}, "")

	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", resolved)
	if err != nil {
		t.Fatalf("NewWithFragments: %v", err)
	}
	initial, err := BuildRouteTable(loader.OpenAPIModel())
	if err != nil {
		t.Fatalf("BuildRouteTable: %v", err)
	}

	srv := &Server{
		config:           &Config{FragmentMode: true, FragmentsDir: resolved, SpecDir: unrelatedSpec},
		specLoader:       loader,
		routeTableHolder: NewThreadSafeTableHolder(initial),
	}
	hrm := NewHotReloadManager(srv)
	if err := hrm.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	defer hrm.Disable()

	writeProjectionRevision(t, service, "rev-2", map[string]string{
		"route.json": fragmentJSONFixture("service-a", "/a/original", "/a/added"),
	}, v1)

	// The swapped-in revision serves both routes; the out-of-tree legacy route
	// never joins.
	waitForRouteTablePaths(t, srv.routeTableHolder, hrm, []string{"/a/original", "/a/added"})
}

// With no configured fragments dir the watcher's root falls back to the legacy
// <spec-dir>/fragments.d directory — the same tree spec.NewWithFragments merged
// at startup, so a watch event reloads what was loaded rather than an unwatched
// tree.
func TestHotReloadWatchRootsAtLegacyFragmentsDFallback(t *testing.T) {
	specDir := t.TempDir()
	legacy := filepath.Join(specDir, "fragments.d")
	v1 := writeProjectionRevision(t, legacy, "rev-1", map[string]string{
		"service-a/route.json": fragmentJSONFixture("service-a", "/legacy/original"),
	}, "")

	loader, err := spec.NewWithFragments(specDir, "http://localhost:8080", "", "")
	if err != nil {
		t.Fatalf("NewWithFragments: %v", err)
	}
	initial, err := BuildRouteTable(loader.OpenAPIModel())
	if err != nil {
		t.Fatalf("BuildRouteTable: %v", err)
	}

	srv := &Server{
		config:           &Config{FragmentMode: true, FragmentsDir: "", SpecDir: specDir},
		specLoader:       loader,
		routeTableHolder: NewThreadSafeTableHolder(initial),
	}
	hrm := NewHotReloadManager(srv)
	if err := hrm.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	defer hrm.Disable()

	writeProjectionRevision(t, legacy, "rev-2", map[string]string{
		"service-a/route.json": fragmentJSONFixture("service-a", "/legacy/original", "/legacy/added"),
	}, v1)

	waitForRouteTablePaths(t, srv.routeTableHolder, hrm, []string{"/legacy/original", "/legacy/added"})
}
