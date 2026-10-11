package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pb33f/libopenapi"
)

func TestFileCredentialSourceReopensAtomicKubernetesProjectionForEveryRequest(t *testing.T) {
	const (
		name = "SEAM_CRED_TEST_OWNER_API_KEY"
		old  = "old-file-value-fixture"
		new  = "new-file-value-fixture"
	)
	root := makeCredentialProjection(t, name, old, new)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		if authorization != "Bearer "+old && authorization != "Bearer "+new {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-Echo-Credential", authorization)
		_, _ = io.WriteString(w, authorization)
	}))
	defer upstream.Close()

	table := buildFileCredentialRouteTable(t, upstream.URL, name)
	table.SetCredentialSource(newFileCredentialSource(root))
	proxy, err := NewReverseProxyWithConfig(upstream.URL, &ReverseProxyConfig{MaxBufferedResponseBytes: 1024})
	if err != nil {
		t.Fatalf("NewReverseProxyWithConfig() error = %v", err)
	}
	perform := func() (*httptest.ResponseRecorder, error) {
		request := httptest.NewRequest(http.MethodGet, "http://seam.test/test", nil)
		withRouteMatch(request, &RouteMatch{Route: table.routes[0]}, routeTableCredentialResolver{table: table})
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			return response, fmt.Errorf("response status = %d", response.Code)
		}
		assertNoSecret(t, []byte(old), response.Body.Bytes())
		assertNoSecret(t, []byte(new), response.Body.Bytes())
		assertNoSecret(t, []byte(old), []byte(response.Header().Get("X-Echo-Credential")))
		assertNoSecret(t, []byte(new), []byte(response.Header().Get("X-Echo-Credential")))
		return response, nil
	}

	if _, err := perform(); err != nil {
		t.Fatalf("initial file credential request: %v", err)
	}
	swapCredentialProjection(t, root, "revision-new")
	if _, err := perform(); err != nil {
		t.Fatalf("request after Secret symlink swap: %v", err)
	}

	// Keep one route table and proxy running while the projected ..data link is
	// repeatedly replaced. Every request must see one complete revision.
	var wait sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < cap(errs); i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := perform()
			if err != nil {
				errs <- err
			}
		}()
	}
	for i := 0; i < 24; i++ {
		target := "revision-old"
		if i%2 == 0 {
			target = "revision-new"
		}
		swapCredentialProjection(t, root, target)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("request during atomic Secret rotation: %v", err)
	}
}

func TestFileCredentialProbeResolvesCurrentProjection(t *testing.T) {
	const (
		name = "SEAM_CRED_TEST_OWNER_API_KEY"
		old  = "old-probe-value-fixture"
		new  = "new-probe-value-fixture"
	)
	root := makeCredentialProjection(t, name, old, new)
	swapCredentialProjection(t, root, "revision-new")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+new {
			t.Errorf("probe Authorization = %q, want the rotated file value", got)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	table := NewRouteTable(nil)
	table.SetCredentialSource(newFileCredentialSource(root))
	registry := NewCredentialProbeRegistry()
	loop := &CredentialProbeLoop{
		probeRegistry:    registry,
		routeTableHolder: NewThreadSafeTableHolder(table),
		credentialSource: newFileCredentialSource(root),
		httpClient:       upstream.Client(),
		lastProbeTimes:   make(map[string]time.Time),
	}
	loop.probeTarget(context.Background(), &CredentialProbeTarget{
		FragmentID: "file-probe:get", InstanceID: "_default",
		CredentialSource: credentialSourceFile, CredentialFile: name,
		ProbeURL: upstream.URL + "/probe", ProbePath: "/probe", ProbeMethod: http.MethodGet,
		Interval: time.Minute, InjectAs: &InjectAs{Kind: InjectionBearer},
	}, time.Now())
	probe := registry.Get("file-probe:get", "_default")
	if probe == nil || probe.Status != CredentialHealthy {
		t.Fatalf("file credential probe = %+v, want healthy after rotation", probe)
	}
}

func TestFileCredentialRefreshableReopensAfterRotation(t *testing.T) {
	const (
		name = "SEAM_CRED_TEST_OWNER_API_KEY"
		old  = "old-refresh-value-fixture"
		new  = "new-refresh-value-fixture"
	)
	root := makeCredentialProjection(t, name, old, new)
	source := newFileCredentialSource(root)
	route := RouteEntry{CredentialSource: credentialSourceFile, CredentialFile: name}
	first, err := source.Resolve(context.Background(), route)
	if err != nil || string(first) != old {
		t.Fatalf("initial Resolve() = %q, %v; want initial projection", first, err)
	}
	swapCredentialProjection(t, root, "revision-new")
	refreshed, err := source.Refreshable(context.Background(), route)
	if err != nil || string(refreshed) != new {
		t.Fatalf("Refreshable() = %q, %v; want rotated projection", refreshed, err)
	}
}

func TestFileCredentialSourceFailsClosedDuringEmptyPartialWrite(t *testing.T) {
	const name = "SEAM_CRED_TEST_OWNER_API_KEY"
	root := t.TempDir()
	path := filepath.Join(root, name)
	source := newFileCredentialSource(root)
	route := RouteEntry{CredentialSource: credentialSourceFile, CredentialFile: name}

	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write partial empty credential: %v", err)
	}
	if value, err := source.Resolve(context.Background(), route); err == nil {
		t.Fatalf("Resolve() during empty partial write returned %q, want an error", value)
	}

	const complete = "complete-after-partial-write-fixture"
	if err := os.WriteFile(path, []byte(complete+"\n"), 0o600); err != nil {
		t.Fatalf("write completed credential: %v", err)
	}
	value, err := source.Resolve(context.Background(), route)
	if err != nil || string(value) != complete {
		t.Fatalf("Resolve() after completed write = %q, %v; want the complete value", value, err)
	}
}

func buildFileCredentialRouteTable(t *testing.T, upstream, name string) *RouteTable {
	t.Helper()
	document, err := libopenapi.NewDocument([]byte(fmt.Sprintf(`{
		"openapi":"3.0.0",
		"info":{"title":"file credential test","version":"1"},
		"x-upstream":%q,
		"x-credential-source":"file",
		"x-credential-file":%q,
		"x-inject-as":{"kind":"bearer"},
		"paths":{"/test":{"get":{"responses":{"200":{"description":"ok"}}}}}
	}`, upstream, name)))
	if err != nil {
		t.Fatalf("NewDocument() error = %v", err)
	}
	model, err := document.BuildV3Model()
	if err != nil {
		t.Fatalf("BuildV3Model() error = %v", err)
	}
	table, err := BuildRouteTable(&model.Model)
	if err != nil {
		t.Fatalf("BuildRouteTable() error = %v", err)
	}
	if len(table.routes) != 1 || table.routes[0].CredentialSource != credentialSourceFile || table.routes[0].CredentialFile != name {
		t.Fatalf("built route did not retain file reference: %+v", table.routes)
	}
	return table
}

func makeCredentialProjection(t *testing.T, name, old, new string) string {
	t.Helper()
	root := t.TempDir()
	for revision, value := range map[string]string{"revision-old": old, "revision-new": new} {
		dir := filepath.Join(root, revision)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\r\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
	if err := os.Symlink("revision-old", filepath.Join(root, "..data")); err != nil {
		t.Fatalf("create initial ..data symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join("..data", name), filepath.Join(root, name)); err != nil {
		t.Fatalf("create projected key symlink: %v", err)
	}
	return root
}

func swapCredentialProjection(t *testing.T, root, revision string) {
	t.Helper()
	temporary := filepath.Join(root, "..data_tmp")
	_ = os.Remove(temporary)
	if err := os.Symlink(revision, temporary); err != nil {
		t.Fatalf("create replacement ..data symlink: %v", err)
	}
	if err := os.Rename(temporary, filepath.Join(root, "..data")); err != nil {
		t.Fatalf("atomically replace ..data symlink: %v", err)
	}
}
