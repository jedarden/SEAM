package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ardenone/seam/internal/spec"
	"github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
	"go.yaml.in/yaml/v4"
)

func TestSanitizeRequestUsesAllMatchingRouteDeclarations(t *testing.T) {
	table := &RouteTable{routes: []RouteEntry{
		{PathTemplate: "/items/{id}", Method: http.MethodGet, APIVersion: "v1", InjectAs: &InjectAs{Kind: InjectionHeader, Name: "X-Api-Key"}},
		{PathTemplate: "/items/{id}", Method: http.MethodGet, APIVersion: "v2", InjectAs: &InjectAs{Kind: InjectionQuery, Name: "api_key"}},
	}}
	req := httptest.NewRequest(http.MethodGet, "/items/42?keep=%2F&api_key=caller&api_key=again&API_KEY=preserve", nil)
	req.Header["x-api-key"] = []string{"one", "two"}
	req.Header["AUTHORIZATION"] = []string{"caller-auth"}
	req.Header["X-SEAM-Forged"] = []string{"remove"}
	req.Header.Set("X-SEAM-Dry-Run", "1")
	req.Header.Set("X-SEAM-API-Version", "v2")

	if err := table.SanitizeRequest(req); err != nil {
		t.Fatal(err)
	}
	if len(req.Header.Values("X-Api-Key")) != 0 || len(req.Header.Values("Authorization")) != 0 {
		t.Fatalf("injectable and authorization headers survived: %#v", req.Header)
	}
	if req.Header.Get("X-SEAM-Forged") != "" {
		t.Fatal("forged X-SEAM header survived")
	}
	if req.Header.Get("X-SEAM-Dry-Run") != "1" || req.Header.Get("X-SEAM-API-Version") != "v2" {
		t.Fatalf("documented exceptions did not survive: %#v", req.Header)
	}
	if got, want := req.URL.RawQuery, "keep=%2F&API_KEY=preserve"; got != want {
		t.Fatalf("RawQuery = %q, want %q", got, want)
	}
}

func TestInjectSecretSupportsHeaderBearerAndQuery(t *testing.T) {
	tests := []struct {
		name      string
		injectAs  *InjectAs
		wantValue string
		wantQuery string
	}{
		{name: "header", injectAs: &InjectAs{Kind: InjectionHeader, Name: "X-Credential"}, wantValue: "injected-value"},
		{name: "bearer", injectAs: &InjectAs{Kind: InjectionBearer}, wantValue: "Bearer injected-value"},
		{name: "query", injectAs: &InjectAs{Kind: InjectionQuery, Name: "api_key"}, wantQuery: "keep=1&api_key=injected-value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/items?keep=1", nil)
			if err := InjectSecret(req, test.injectAs, []byte("injected-value")); err != nil {
				t.Fatal(err)
			}
			if test.wantQuery != "" {
				if req.URL.RawQuery != test.wantQuery {
					t.Fatalf("RawQuery = %q, want %q", req.URL.RawQuery, test.wantQuery)
				}
				return
			}
			if got := req.Header.Get(test.injectAs.Name); got != test.wantValue && test.name == "header" {
				t.Fatalf("header = %q, want %q", got, test.wantValue)
			}
			if test.name == "bearer" && req.Header.Get("Authorization") != test.wantValue {
				t.Fatalf("Authorization = %q, want %q", req.Header.Get("Authorization"), test.wantValue)
			}
		})
	}
}

func TestComputeUpstreamPathFlagshipsAndSingleSegmentEncoding(t *testing.T) {
	tests := []struct {
		name  string
		match *RouteMatch
		want  string
	}{
		{
			name: "kubernetes instance strip",
			match: &RouteMatch{Route: RouteEntry{
				PathTemplate:        "/k8s/{cluster}/api/v1/pods",
				InstanceParam:       "cluster",
				UpstreamStripPrefix: "/k8s",
			}, PathParams: map[string]string{"cluster": "ardenone-cluster"}},
			want: "/api/v1/pods",
		},
		{
			name: "argocd strip",
			match: &RouteMatch{Route: RouteEntry{
				PathTemplate:        "/argocd/api/v1/applications",
				UpstreamStripPrefix: "/argocd",
			}},
			want: "/api/v1/applications",
		},
		{
			name: "encoded binding",
			match: &RouteMatch{Route: RouteEntry{
				PathTemplate: "/objects/{name}",
			}, PathParams: map[string]string{"name": "a/b.."}},
			want: "/objects/a%2Fb%2E%2E",
		},
		{
			name: "explicit template wins",
			match: &RouteMatch{Route: RouteEntry{
				PathTemplate:         "/argocd/{name}/sync",
				UpstreamPathTemplate: "/api/v1/applications/{name}/sync",
				UpstreamStripPrefix:  "/argocd",
			}, PathParams: map[string]string{"name": "demo"}},
			want: "/api/v1/applications/demo/sync",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ComputeUpstreamPath(test.match)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("path = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReverseProxyUsesComputedPathAndPreservesBasePrefix(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/base/api/v1/pods"; got != want {
			t.Errorf("upstream path = %q, want %q", got, want)
		}
		if got, want := r.URL.RawQuery, "keep=%2F"; got != want {
			t.Errorf("upstream query = %q, want %q", got, want)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	table := &RouteTable{routes: []RouteEntry{{
		PathTemplate:        "/k8s/{cluster}/api/v1/pods",
		Method:              http.MethodGet,
		APIVersion:          "v1",
		UpstreamTarget:      upstream.URL + "/base",
		InstanceParam:       "cluster",
		UpstreamStripPrefix: "/k8s",
	}}}
	req := httptest.NewRequest(http.MethodGet, "/k8s/ardenone-cluster/api/v1/pods?keep=%2F", nil)
	match, err := table.Match(req)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := NewReverseProxy(match.Route.UpstreamTarget)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusNoContent, recorder.Body.String())
	}
}

func TestInjectSecretQueryRemovesCallerDuplicates(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/items?keep=1&api_key=caller&API_KEY=case-sensitive", nil)
	if err := InjectSecret(req, &InjectAs{Kind: InjectionQuery, Name: "api_key"}, []byte("injected-value")); err != nil {
		t.Fatal(err)
	}
	if got, want := req.URL.RawQuery, "keep=1&API_KEY=case-sensitive&api_key=injected-value"; got != want {
		t.Fatalf("RawQuery = %q, want %q", got, want)
	}
}

func TestBuildRouteTableExtractsInjectionAndPathItemRewrite(t *testing.T) {
	pathItem := &v3.PathItem{
		Extensions: orderedmap.New[string, *yaml.Node](),
		Get:        &v3.Operation{Responses: &v3.Responses{Codes: orderedmap.New[string, *v3.Response]()}},
	}
	pathItem.Extensions.Set("x-upstream-path-template", scalarNode("/api/{name}"))
	pathItem.Get.Extensions = orderedmap.New[string, *yaml.Node]()
	pathItem.Get.Extensions.Set("x-upstream", scalarNode("https://upstream.example"))
	pathItem.Get.Extensions.Set("x-vault-path", scalarNode("seam/routes/example/token"))
	pathItem.Get.Extensions.Set("x-inject-as", objectNode("kind: header\nname: X-Api-Key"))
	pathItem.Get.Parameters = nil
	pathItem.Get.Responses.Codes.Set("200", &v3.Response{Description: "ok"})
	document := &v3.Document{Paths: &v3.Paths{PathItems: orderedmap.New[string, *v3.PathItem]()}}
	document.Paths.PathItems.Set("/objects/{name}", pathItem)

	table, err := BuildRouteTable(document)
	if err != nil {
		t.Fatal(err)
	}
	route := table.GetRoutes()[0]
	if route.UpstreamPathTemplate != "/api/{name}" || route.InjectAs == nil || route.InjectAs.Name != "X-Api-Key" {
		t.Fatalf("route metadata was not extracted: %+v", route)
	}
}

func TestFragmentRootStripPrefixReachesOutboundPath(t *testing.T) {
	var outboundPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outboundPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	fragmentsDir := filepath.Join(t.TempDir(), "fragments.d")
	fragmentPath := filepath.Join(fragmentsDir, "argocd-ro", "route.json")
	if err := os.MkdirAll(filepath.Dir(fragmentPath), 0755); err != nil {
		t.Fatal(err)
	}
	fragment := fmt.Sprintf(`{
		"x-seam-schema": "v1",
		"x-seam-owner": "argocd-ro",
		"x-api-version": "v1",
		"x-upstream": %q,
		"x-upstream-strip-prefix": "/argocd",
		"paths": {
			"/argocd/api/v1/clusters": {
				"get": {
					"responses": {"200": {"description": "ok"}}
				}
			}
		}
	}`, upstream.URL)
	if err := os.WriteFile(fragmentPath, []byte(fragment), 0644); err != nil {
		t.Fatal(err)
	}

	loader, err := spec.NewWithFragments("", "http://localhost:8080", "", fragmentsDir)
	if err != nil {
		t.Fatalf("load fragment: %v", err)
	}
	table, err := BuildRouteTable(loader.OpenAPIModel())
	if err != nil {
		t.Fatalf("build route table: %v", err)
	}
	routes := table.GetRoutes()
	if len(routes) != 1 {
		t.Fatalf("route count = %d, want 1", len(routes))
	}
	if got, want := routes[0].UpstreamStripPrefix, "/argocd"; got != want {
		t.Fatalf("route strip prefix = %q, want %q", got, want)
	}

	req := httptest.NewRequest(http.MethodGet, "/argocd/api/v1/clusters", nil)
	match, err := table.Match(req)
	if err != nil {
		t.Fatalf("match route: %v", err)
	}
	proxy, err := NewReverseProxy(match.Route.UpstreamTarget)
	if err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, req)
	if response.Code != http.StatusNoContent {
		t.Fatalf("proxy status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if got, want := outboundPath, "/api/v1/clusters"; got != want {
		t.Fatalf("outbound path = %q, want %q", got, want)
	}
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: value}
}

func objectNode(value string) *yaml.Node {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(value), &node); err != nil {
		panic(err)
	}
	return &node
}
