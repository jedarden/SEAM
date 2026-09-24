package server

import (
	"path/filepath"
	"testing"
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
