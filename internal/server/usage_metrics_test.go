package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/buildinfo"
)

func TestUsageMetricsModelLabelCollapsesUnallowlistedValues(t *testing.T) {
	config := &UsageMetricsConfig{Model: ModelMetricConfig{
		Pointer:   "/model",
		Allowlist: []string{"safe-model"},
	}}
	if got := config.modelLabel([]byte(`{"model":"arbitrary-model-from-caller"}`)); got != otherModelLabel {
		t.Fatalf("unallowlisted model label = %q, want %q", got, otherModelLabel)
	}
	if got := config.modelLabel([]byte(`{"model":"safe-model"}`)); got != "safe-model" {
		t.Fatalf("allowlisted model label = %q, want safe-model", got)
	}
}

func TestParseUsageMetricsConfigSupportsNestedUsageAndModel(t *testing.T) {
	config, err := parseUsageMetricsConfig(map[string]any{
		"usage": map[string]any{
			"inputTokens":  "/usage/input_tokens",
			"outputTokens": map[string]any{"pointer": "/usage/output_tokens"},
		},
		"model": map[string]any{
			"pointer":   "/model",
			"allowlist": []any{"safe-model"},
		},
	})
	if err != nil {
		t.Fatalf("parseUsageMetricsConfig: %v", err)
	}
	if config.InputTokensPointer != "/usage/input_tokens" || config.OutputTokensPointer != "/usage/output_tokens" {
		t.Fatalf("usage pointers = %#v, want input/output pointers", config)
	}
}

func TestRouteMetricsAppearForBufferedAndStreamedResponses(t *testing.T) {
	tests := []struct {
		name     string
		streamed bool
		response string
		content  string
	}{
		{name: "buffered", response: `{"usage":{"input_tokens":3,"output_tokens":5}}`, content: "application/json"},
		{name: "streamed", streamed: true, response: "data: {\"usage\":{\"input_tokens\":7,\"output_tokens\":11}}\n\ndata: [DONE]\n\n", content: "text/event-stream"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.content)
				if test.streamed {
					w.(http.Flusher).Flush()
				}
				_, _ = io.WriteString(w, test.response)
				if test.streamed {
					w.(http.Flusher).Flush()
				}
			}))
			defer upstream.Close()

			route := RouteEntry{
				PathTemplate: "/metered",
				Method:       http.MethodPost,
				APIVersion:   "v1",
				Streaming:    test.streamed,
				CostPerCall:  &CostPerCallConfig{Amount: 0.25, Unit: "USD"},
				CostFromResponse: &ResponseCostConfig{Charges: []ResponseCostCharge{
					{Name: "input_tokens", Pointer: "/usage/input_tokens", Price: 0.01},
					{Name: "output_tokens", Pointer: "/usage/output_tokens", Price: 0.02},
				}},
				UsageMetrics: &UsageMetricsConfig{
					InputTokensPointer:  "/usage/input_tokens",
					OutputTokensPointer: "/usage/output_tokens",
					Model:               ModelMetricConfig{Pointer: "/model", Allowlist: []string{"safe-model"}},
				},
			}
			table := NewRouteTable(nil)
			table.AddRoute(route)
			server := &Server{
				cache:            NewResponseCache(),
				routeTableHolder: NewThreadSafeTableHolder(table),
				circuitBreakers:  NewCircuitBreakerStateRegistry(),
			}
			server.metrics = newMetrics(server.cache, table, server.circuitBreakers, buildinfo.Read())
			proxy, err := NewReverseProxy(upstream.URL)
			if err != nil {
				t.Fatalf("NewReverseProxy: %v", err)
			}
			proxy.Metrics = server.metrics

			req := httptest.NewRequest(http.MethodPost, "http://seam.test/metered", strings.NewReader(`{"model":"safe-model"}`))
			withRouteMatch(req, &RouteMatch{Route: route}, nil)
			recorder := httptest.NewRecorder()
			server.metricsMiddleware(proxy).ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}

			body := scrapePrometheusMetrics(t, server)
			modelLabels := `model="safe-model",route="/metered"`
			if !strings.Contains(body, "seam_input_tokens_total{"+modelLabels+"}") || !strings.Contains(body, "seam_output_tokens_total{"+modelLabels+"}") {
				t.Fatalf("token metrics missing for %s response:\n%s", test.name, body)
			}
			if !strings.Contains(body, "seam_time_to_first_byte_seconds_count{"+modelLabels+"} 1") {
				t.Fatalf("TTFB metric missing for %s response:\n%s", test.name, body)
			}
			if !strings.Contains(body, "seam_request_latency_seconds_count{"+modelLabels+"} 1") {
				t.Fatalf("latency metric missing for %s response:\n%s", test.name, body)
			}
		})
	}
}

// TestUsageMetricsCardinalityCollapsesArbitraryModelsToOther exercises the
// full request path, not just modelLabel: a caller-invented model string must
// land on the single bounded "other" series instead of minting a new
// Prometheus label value per caller, for buffered and streamed responses
// alike — the streamed cell routes the usage document through the SSE
// extractor, so it proves the collapse on its own response path too.
func TestUsageMetricsCardinalityCollapsesArbitraryModelsToOther(t *testing.T) {
	tests := []struct {
		name     string
		streamed bool
		response string
		content  string
	}{
		{name: "buffered", response: `{"usage":{"input_tokens":2,"output_tokens":4}}`, content: "application/json"},
		{name: "streamed", streamed: true, response: "data: {\"usage\":{\"input_tokens\":2,\"output_tokens\":4}}\n\ndata: [DONE]\n\n", content: "text/event-stream"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.content)
				if test.streamed {
					w.(http.Flusher).Flush()
				}
				_, _ = io.WriteString(w, test.response)
				if test.streamed {
					w.(http.Flusher).Flush()
				}
			}))
			defer upstream.Close()

			route := RouteEntry{
				PathTemplate: "/metered",
				Method:       http.MethodPost,
				APIVersion:   "v1",
				Streaming:    test.streamed,
				CostPerCall:  &CostPerCallConfig{Amount: 0.25, Unit: "USD"},
				UsageMetrics: &UsageMetricsConfig{
					InputTokensPointer:  "/usage/input_tokens",
					OutputTokensPointer: "/usage/output_tokens",
					Model:               ModelMetricConfig{Pointer: "/model", Allowlist: []string{"safe-model"}},
				},
			}
			table := NewRouteTable(nil)
			table.AddRoute(route)
			server := &Server{
				cache:            NewResponseCache(),
				routeTableHolder: NewThreadSafeTableHolder(table),
				circuitBreakers:  NewCircuitBreakerStateRegistry(),
			}
			server.metrics = newMetrics(server.cache, table, server.circuitBreakers, buildinfo.Read())
			proxy, err := NewReverseProxy(upstream.URL)
			if err != nil {
				t.Fatalf("NewReverseProxy: %v", err)
			}
			proxy.Metrics = server.metrics

			// Lowercase alphanumeric plus dashes keeps the value safe to assert
			// against Prometheus text exposition without escaping.
			arbitrary := "caller-invented-model-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
			req := httptest.NewRequest(http.MethodPost, "http://seam.test/metered", strings.NewReader(`{"model":"`+arbitrary+`"}`))
			withRouteMatch(req, &RouteMatch{Route: route}, nil)
			recorder := httptest.NewRecorder()
			server.metricsMiddleware(proxy).ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}

			body := scrapePrometheusMetrics(t, server)
			otherLabels := `model="other",route="/metered"`
			if !strings.Contains(body, "seam_input_tokens_total{"+otherLabels+"}") || !strings.Contains(body, "seam_output_tokens_total{"+otherLabels+"}") {
				t.Fatalf("token counters did not collapse to %s for %s response:\n%s", otherLabels, test.name, body)
			}
			if strings.Contains(body, arbitrary) {
				t.Fatalf("arbitrary model string leaked onto the metrics surface for %s response:\n%s", test.name, body)
			}
		})
	}
}

func TestMetricsResponseWriterRecordsFirstByteAtHeaderWrite(t *testing.T) {
	writer := &metricsResponseWriter{ResponseWriter: httptest.NewRecorder()}
	start := time.Now()
	writer.WriteHeader(http.StatusOK)
	if writer.firstByteAt.Before(start) {
		t.Fatalf("first-byte timestamp %v precedes start %v", writer.firstByteAt, start)
	}
}
