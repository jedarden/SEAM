package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/buildinfo"
	"github.com/ardenone/seam/internal/vault"
)

func TestPrometheusCollectorExportsMetricTaxonomy(t *testing.T) {
	cache := NewResponseCache()
	table := NewRouteTable(nil)
	table.AddRoute(RouteEntry{
		PathTemplate: "/widgets/{id}",
		Method:       http.MethodGet,
		APIVersion:   "v2",
	})
	breakers := NewCircuitBreakerStateRegistry()
	server := &Server{
		cache:            cache,
		routeTableHolder: NewThreadSafeTableHolder(table),
		circuitBreakers:  breakers,
		operatorMux:      http.NewServeMux(),
	}
	server.metrics = newMetrics(cache, table, breakers, buildinfo.Info{
		Version:   "1.2.3",
		Revision:  "abc123",
		GoVersion: "go-test",
		Modified:  "false",
	})
	server.operatorMux.HandleFunc("/_seam/metrics", server.metricsHandler)

	requestHandler := server.metricsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	for _, path := range []string{"/widgets/one", "/widgets/two"} {
		requestHandler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	labels := metricRouteLabels{Route: "/widgets/{id}", Version: "v2"}
	server.metrics.recordCacheHit(labels)
	server.metrics.recordCacheMiss(labels)
	cache.Get(CacheKey("missing"))
	cache.Set(CacheKey("present"), &cachedResponse{StatusCode: http.StatusOK}, 60)
	cache.Get(CacheKey("present"))
	cache.Delete(CacheKey("present"))

	openBao := newMetricsOpenBaoClient(t)
	table.secretClient = openBao
	if _, err := openBao.GetSecret(context.Background(), "seam/routes/example/credential"); err != nil {
		t.Fatalf("first OpenBao read: %v", err)
	}
	if _, err := openBao.GetSecret(context.Background(), "seam/routes/example/credential"); err != nil {
		t.Fatalf("cached OpenBao read: %v", err)
	}

	breakers.Set(CircuitBreakerStatus{
		Origin:              "https://upstream.example",
		State:               CircuitBreakerOpen,
		Enabled:             true,
		ConsecutiveFailures: 5,
	})

	body := scrapePrometheusMetrics(t, server)
	for _, sample := range []string{
		`seam_build_info{commit="abc123",go_version="go-test",modified="false",version="1.2.3"} 1`,
		`seam_http_requests_total{method="GET",route="/widgets/{id}",status="201",version="v2"} 2`,
		`seam_http_request_duration_seconds_count{method="GET",route="/widgets/{id}",version="v2"} 2`,
		`seam_cache_hits_total{route="/widgets/{id}",version="v2"} 1`,
		`seam_cache_misses_total{route="/widgets/{id}",version="v2"} 1`,
		`seam_cache_hit_rate 0.5`,
		`seam_cache_evictions_total 1`,
		`seam_openbao_cache_hits_total 1`,
		`seam_openbao_cache_misses_total 1`,
		`seam_openbao_cache_fetches_total 1`,
		`seam_openbao_cache_entries 1`,
		`seam_upstream_health{origin="https://upstream.example",state="open"} 1`,
		`seam_upstream_health{origin="https://upstream.example",state="closed"} 0`,
		`seam_upstream_breaker_enabled{origin="https://upstream.example"} 1`,
		`seam_upstream_consecutive_failures{origin="https://upstream.example"} 5`,
	} {
		if !strings.Contains(body, sample) {
			t.Errorf("metrics output missing %q", sample)
		}
	}
	if strings.Contains(body, "/widgets/one") || strings.Contains(body, "/widgets/two") {
		t.Errorf("metrics used a concrete request path instead of the route template")
	}

	breakers.Remove("https://upstream.example")
	if body = scrapePrometheusMetrics(t, server); strings.Contains(body, "https://upstream.example") {
		t.Errorf("removed upstream remained in the next metrics scrape")
	}
}

func TestMetricsEndpointRejectsNonGET(t *testing.T) {
	server := &Server{
		cache:            NewResponseCache(),
		routeTableHolder: NewThreadSafeTableHolder(NewRouteTable(nil)),
		circuitBreakers:  NewCircuitBreakerStateRegistry(),
	}
	recorder := httptest.NewRecorder()
	server.metricsHandler(recorder, httptest.NewRequest(http.MethodPost, "/_seam/metrics", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /_seam/metrics status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}

func TestMetricsRegistriesAreServerScoped(t *testing.T) {
	first := &Server{
		cache:            NewResponseCache(),
		routeTableHolder: NewThreadSafeTableHolder(NewRouteTable(nil)),
		circuitBreakers:  NewCircuitBreakerStateRegistry(),
	}
	second := &Server{
		cache:            NewResponseCache(),
		routeTableHolder: NewThreadSafeTableHolder(NewRouteTable(nil)),
		circuitBreakers:  NewCircuitBreakerStateRegistry(),
	}
	first.ensureMetrics().recordCacheHit(metricRouteLabels{Route: "/first", Version: "v1"})
	second.ensureMetrics().recordCacheHit(metricRouteLabels{Route: "/second", Version: "v1"})

	firstBody := scrapePrometheusMetrics(t, first)
	if !strings.Contains(firstBody, `route="/first"`) {
		t.Fatal("first server metrics omitted its own cache counter")
	}
	if strings.Contains(firstBody, `route="/second"`) {
		t.Fatal("first server metrics included another server's cache counter")
	}
}

func newMetricsOpenBaoClient(t *testing.T) *vault.Client {
	t.Helper()
	openBaoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"data":{"credential":"example"}}}`))
	}))
	t.Cleanup(openBaoServer.Close)
	inCluster := false
	client, err := vault.New(vault.Config{
		Address:   openBaoServer.URL,
		DevToken:  "example",
		InCluster: &inCluster,
		CacheTTL:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create OpenBao client: %v", err)
	}
	return client
}

func scrapePrometheusMetrics(t *testing.T, server *Server) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/_seam/metrics", nil)
	recorder := httptest.NewRecorder()
	server.metricsHandler(recorder, request)
	response := recorder.Result()
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /_seam/metrics status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.Contains(contentType, "text/plain") {
		t.Fatalf("GET /_seam/metrics Content-Type = %q, want Prometheus text", contentType)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read metrics response: %v", err)
	}
	return string(body)
}
