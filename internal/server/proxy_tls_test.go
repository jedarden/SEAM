package server

import (
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
	"go.yaml.in/yaml/v4"
)

func TestDispatchHandlerUsesRouteUpstreamTLSConfig(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("trusted upstream"))
	}))
	defer upstream.Close()

	caDir := t.TempDir()
	caBundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(caDir, "test-ca.pem"), caBundle, 0o600); err != nil {
		t.Fatalf("write test CA bundle: %v", err)
	}

	// Build the route as the fragment loader does: x-upstream-tls is parsed
	// into RouteEntry.TLSConfig before the dispatch handler sees the request.
	operation := &v3.Operation{
		Responses:  &v3.Responses{Codes: orderedmap.New[string, *v3.Response]()},
		Extensions: orderedmap.New[string, *yaml.Node](),
	}
	operation.Responses.Codes.Set("200", &v3.Response{Description: "OK"})
	operation.Extensions.Set("x-upstream", scalarYAMLNode(upstream.URL))
	operation.Extensions.Set("x-upstream-tls", mappingYAMLNode(
		"caBundle", "test-ca.pem",
	))
	document := &v3.Document{Paths: &v3.Paths{PathItems: orderedmap.New[string, *v3.PathItem]()}}
	document.Paths.PathItems.Set("/tls", &v3.PathItem{Get: operation})

	table, err := BuildRouteTable(document)
	if err != nil {
		t.Fatalf("build route table: %v", err)
	}
	if got := table.GetRoutes()[0].TLSConfig; got == nil || got.CaBundle != "test-ca.pem" {
		t.Fatalf("route TLS config = %#v, want test-ca.pem", got)
	}

	srv := newDispatchTLSConfigTestServer(table, caDir)
	req := httptest.NewRequest(http.MethodGet, "/tls", nil)
	withTLSResponse := httptest.NewRecorder()
	srv.dispatchHandler(withTLSResponse, req)
	if withTLSResponse.Code != http.StatusOK {
		t.Fatalf("configured TLS response status = %d, want 200; body = %s", withTLSResponse.Code, withTLSResponse.Body.String())
	}

	// The same upstream fails when the route TLS configuration is ignored,
	// because the test server certificate is not in the system trust store.
	ignoredTable := NewRouteTable(nil)
	ignoredTable.AddRoute(RouteEntry{
		PathTemplate:   "/tls",
		Method:         http.MethodGet,
		APIVersion:     "_unversioned",
		UpstreamTarget: upstream.URL,
	})
	ignoredServer := newDispatchTLSConfigTestServer(ignoredTable, caDir)
	ignoredResponse := httptest.NewRecorder()
	ignoredServer.dispatchHandler(ignoredResponse, httptest.NewRequest(http.MethodGet, "/tls", nil))
	if ignoredResponse.Code != http.StatusBadGateway {
		t.Fatalf("ignored TLS response status = %d, want 502; body = %s", ignoredResponse.Code, ignoredResponse.Body.String())
	}
}

func TestDispatchProxyCacheSeparatesUpstreamTLSConfigs(t *testing.T) {
	upstreamURL := "https://upstream.example.test"
	srv := newDispatchTLSConfigTestServer(NewRouteTable(nil), t.TempDir())

	firstTLS := &UpstreamTLSConfig{ServerName: "first.example.test", InsecureSkipVerify: true}
	secondTLS := &UpstreamTLSConfig{ServerName: "second.example.test", InsecureSkipVerify: true}

	firstProxy := srv.getOrCreateProxy(upstreamURL, firstTLS)
	secondProxy := srv.getOrCreateProxy(upstreamURL, secondTLS)
	if firstProxy == nil || secondProxy == nil {
		t.Fatal("getOrCreateProxy returned nil")
	}
	if firstProxy == secondProxy {
		t.Fatal("routes with different TLS configs shared a proxy")
	}
	if firstProxy.Client == secondProxy.Client {
		t.Fatal("routes with different TLS configs shared an HTTP client")
	}
	if got := len(srv.proxyMap); got != 2 {
		t.Fatalf("proxy cache size = %d, want 2", got)
	}
	if got := len(srv.upstreamClientMap); got != 2 {
		t.Fatalf("TLS client cache size = %d, want 2", got)
	}

	duplicateProxy := srv.getOrCreateProxy(upstreamURL, &UpstreamTLSConfig{
		ServerName:         firstTLS.ServerName,
		InsecureSkipVerify: firstTLS.InsecureSkipVerify,
	})
	if duplicateProxy != firstProxy {
		t.Fatal("equivalent TLS configs did not reuse the cached proxy")
	}

	otherUpstreamProxy := srv.getOrCreateProxy("https://other-upstream.example.test", firstTLS)
	if otherUpstreamProxy == nil {
		t.Fatal("getOrCreateProxy returned nil for a second upstream")
	}
	if otherUpstreamProxy.Client != firstProxy.Client {
		t.Fatal("equivalent TLS configs for different upstreams did not share the cached client")
	}
	if got := len(srv.upstreamClientMap); got != 2 {
		t.Fatalf("TLS client cache size after equivalent config = %d, want 2", got)
	}
}

func TestDispatchHandlerTLSConfigErrorFailsClosed(t *testing.T) {
	table := NewRouteTable(nil)
	table.AddRoute(RouteEntry{
		PathTemplate:   "/tls",
		Method:         http.MethodGet,
		APIVersion:     "_unversioned",
		UpstreamTarget: "https://upstream.example.test",
		TLSConfig:      &UpstreamTLSConfig{CaBundle: "does-not-exist.pem"},
	})
	srv := newDispatchTLSConfigTestServer(table, t.TempDir())

	response := httptest.NewRecorder()
	srv.dispatchHandler(response, httptest.NewRequest(http.MethodGet, "/tls", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid CA response status = %d, want 503; body = %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode invalid CA response: %v", err)
	}
	if body["error"] != "proxy_creation_failed" {
		t.Fatalf("invalid CA response error = %v, want proxy_creation_failed", body["error"])
	}
	if len(srv.proxyMap) != 0 {
		t.Fatalf("proxy cache size = %d after TLS config error, want 0", len(srv.proxyMap))
	}
}

func newDispatchTLSConfigTestServer(table *RouteTable, upstreamCADir string) *Server {
	return &Server{
		config: &Config{
			MaxReplayableRequestBytes: DefaultMaxReplayableRequestBytes,
			MaxBufferedResponseBytes:  DefaultMaxBufferedResponseBytes,
			UpstreamCADir:             upstreamCADir,
		},
		routeTable:        table,
		proxyMap:          make(map[string]*ReverseProxy),
		upstreamClientMap: make(map[string]*http.Client),
		cache:             NewResponseCache(),
		singleFlight:      NewSingleFlight(),
		cacheTTLs:         make(map[string]int),
		circuitBreakers:   NewCircuitBreakerStateRegistry(),
		quotaTracker:      NewQuotaTracker(),
		costPerCalls:      make(map[string]float64),
	}
}

func scalarYAMLNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: value}
}

func mappingYAMLNode(keyValues ...string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}
	for i := 0; i < len(keyValues); i += 2 {
		node.Content = append(node.Content, scalarYAMLNode(keyValues[i]), scalarYAMLNode(keyValues[i+1]))
	}
	return node
}

func TestBuildTLSConfigRejectsMissingBundle(t *testing.T) {
	_, err := newUpstreamHTTPClientWithTLS(&UpstreamTLSConfig{CaBundle: "missing.pem"}, t.TempDir())
	if err == nil {
		t.Fatal("newUpstreamHTTPClientWithTLS() error = nil, want missing CA bundle error")
	}
	if got := err.Error(); got == "" {
		t.Fatal("newUpstreamHTTPClientWithTLS() returned an empty error")
	}
}

func TestUpstreamTLSConfigUsesMinimumTLSVersion(t *testing.T) {
	client, err := newUpstreamHTTPClientWithTLS(&UpstreamTLSConfig{InsecureSkipVerify: true}, t.TempDir())
	if err != nil {
		t.Fatalf("newUpstreamHTTPClientWithTLS() error = %v", err)
	}
	transport := client.Transport.(*http.Transport)
	if got := transport.TLSClientConfig.MinVersion; got != tls.VersionTLS12 {
		t.Fatalf("TLS minimum version = %d, want %d", got, tls.VersionTLS12)
	}
}
