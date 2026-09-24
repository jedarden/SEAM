package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests in this file pin the fragment-directory semantics documented in
// README.md ("Fragment directory and hot-reload scope"): an explicit
// fragments directory is authoritative, <spec-dir>/fragments.d is only a
// legacy fallback for an empty directory, and a reload re-reads the directory
// captured at construction rather than re-consulting SEAM_FRAGMENTS_DIR.

// writeFragmentFixture writes a minimal valid fragment declaring routePath
// into <root>/<owner>/route.json, mirroring the <fragments-dir>/<owner>/
// layout LoadDirectory walks.
func writeFragmentFixture(t *testing.T, root, owner, routePath string) {
	t.Helper()
	fragmentDir := filepath.Join(root, owner)
	if err := os.MkdirAll(fragmentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fragment := fmt.Sprintf(`{
		"x-seam-schema": "v1",
		"x-seam-owner": %q,
		"x-api-version": "v1",
		"x-upstream": "https://upstream.example",
		"paths": {
			%q: {
				"get": {
					"responses": {"200": {"description": "ok"}}
				}
			}
		}
	}`, owner, routePath)
	if err := os.WriteFile(filepath.Join(fragmentDir, "route.json"), []byte(fragment), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listPathsContains(t *testing.T, paths []string, want string) {
	t.Helper()
	for _, p := range paths {
		if p == want {
			return
		}
	}
	t.Fatalf("merged spec paths %v do not contain %q", paths, want)
}

func listPathsExcludes(t *testing.T, paths []string, unwanted string) {
	t.Helper()
	for _, p := range paths {
		if p == unwanted {
			t.Fatalf("merged spec paths %v unexpectedly contain %q", paths, unwanted)
		}
	}
}

// An explicit fragments directory wins: fragments sitting under the legacy
// <spec-dir>/fragments.d layout are not consulted.
func TestNewWithFragmentsExplicitDirIsAuthoritative(t *testing.T) {
	specDir := t.TempDir()
	explicitDir := t.TempDir()

	writeFragmentFixture(t, filepath.Join(specDir, "fragments.d"), "legacy-owner", "/legacy/path")
	writeFragmentFixture(t, explicitDir, "explicit-owner", "/explicit/path")

	loader, err := NewWithFragments(specDir, "http://localhost:8080", "", explicitDir)
	if err != nil {
		t.Fatalf("NewWithFragments: %v", err)
	}

	if got, want := loader.fragmentsDir, explicitDir; got != want {
		t.Errorf("resolved fragments dir = %q, want %q", got, want)
	}
	paths := loader.ListPaths()
	listPathsContains(t, paths, "/explicit/path")
	listPathsExcludes(t, paths, "/legacy/path")
}

// An empty fragments directory falls back to the legacy <spec-dir>/fragments.d
// layout. The serve path never exercises this (the flag default is ./fragments);
// it exists for direct loader callers and the pre-flag layout.
func TestNewWithFragmentsEmptyDirFallsBackToSpecDirFragmentsD(t *testing.T) {
	specDir := t.TempDir()
	writeFragmentFixture(t, filepath.Join(specDir, "fragments.d"), "legacy-owner", "/legacy/path")

	loader, err := NewWithFragments(specDir, "http://localhost:8080", "", "")
	if err != nil {
		t.Fatalf("NewWithFragments: %v", err)
	}

	if got, want := loader.fragmentsDir, filepath.Join(specDir, "fragments.d"); got != want {
		t.Errorf("resolved fragments dir = %q, want the legacy %q", got, want)
	}
	listPathsContains(t, loader.ListPaths(), "/legacy/path")
}

// A reload re-reads the directory captured at construction. A SEAM_FRAGMENTS_DIR
// change in the process environment must not redirect it — the hot-reload
// watcher watches the startup directory, so redirecting a reload would merge a
// tree nobody was watching.
func TestLoadFragmentsReReadsConstructionTimeDir(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeFragmentFixture(t, dirA, "service-a", "/a/original")

	loader, err := NewWithFragments("", "http://localhost:8080", "", dirA)
	if err != nil {
		t.Fatalf("NewWithFragments: %v", err)
	}

	t.Setenv("SEAM_FRAGMENTS_DIR", dirB)
	writeFragmentFixture(t, dirA, "service-a2", "/a/added")
	writeFragmentFixture(t, dirB, "service-b", "/b/unwatched")

	if err := loader.LoadFragments(); err != nil {
		t.Fatalf("LoadFragments: %v", err)
	}

	if got, want := loader.fragmentsDir, dirA; got != want {
		t.Errorf("reload resolved fragments dir = %q, want the construction-time %q", got, want)
	}
	paths := loader.ListPaths()
	listPathsContains(t, paths, "/a/original")
	listPathsContains(t, paths, "/a/added")
	listPathsExcludes(t, paths, "/b/unwatched")
}

// NewLoader resolves its directory from SEAM_FRAGMENTS_DIR with the ./fragments
// default — independent of the serve flag wiring, which resolves the same
// variable through applyEnvOverrides (pinned in cmd/seam).
func TestNewLoaderResolvesSEAMFragmentsDir(t *testing.T) {
	t.Run("environment value wins over the default", func(t *testing.T) {
		dir := t.TempDir()
		writeFragmentFixture(t, dir, "env-owner", "/env/path")
		t.Setenv("SEAM_FRAGMENTS_DIR", dir)

		loader, err := NewLoader("http://localhost:8080")
		if err != nil {
			t.Fatalf("NewLoader: %v", err)
		}
		if got, want := loader.fragmentsDir, dir; got != want {
			t.Errorf("resolved fragments dir = %q, want %q", got, want)
		}
		listPathsContains(t, loader.ListPaths(), "/env/path")
	})

	t.Run("unset variable falls back to ./fragments", func(t *testing.T) {
		t.Setenv("SEAM_FRAGMENTS_DIR", "")
		t.Chdir(t.TempDir())

		_, err := NewLoader("http://localhost:8080")
		if err == nil {
			t.Fatal("NewLoader with no fragments present succeeded, want an error")
		}
		if !strings.Contains(err.Error(), "./fragments") {
			t.Errorf("error = %v, want it to name the ./fragments default", err)
		}
	})
}
