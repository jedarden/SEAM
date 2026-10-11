package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/buildinfo"
)

func responseCostRoute(streaming bool) *RouteMatch {
	return &RouteMatch{Route: RouteEntry{
		PathTemplate: "/metered",
		Method:       http.MethodPost,
		Streaming:    streaming,
		CostPerCall:  &CostPerCallConfig{Amount: 0.25, Unit: "USD"},
		CostFromResponse: &ResponseCostConfig{Charges: []ResponseCostCharge{
			{Pointer: "/usage/input_tokens", Price: 0.01},
			{Pointer: "/usage/output_tokens", Price: 0.02},
		}},
	}}
}

func TestResponseCostBufferedResponseChargesReportedUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":10,"output_tokens":20}}`)
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewReverseProxy: %v", err)
	}
	proxy.QuotaTracker = NewQuotaTracker()
	proxy.QuotaTracker.SetQuota("/metered", QuotaConfig{Limit: 1, Window: 3600 * 1e9, Scope: "per-route"})
	proxy.QuotaTracker.SetCostPerCall("/metered", 0.25)

	req := httptest.NewRequest(http.MethodPost, "http://seam.test/metered", nil)
	withRouteMatch(req, responseCostRoute(false), nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-SEAM-Budget-Remaining"); !strings.HasPrefix(got, "amount=$0.5 unit=USD window=1h resets=") {
		t.Fatalf("budget header = %q, want post-response $0.5 USD balance", got)
	}
	if got := proxy.QuotaTracker.GetRemaining("/metered", "", ""); got != 0.5 {
		t.Fatalf("remaining = %v, want 0.5", got)
	}
}

func TestResponseCostQuotaMiddlewareDefersLedgerCharge(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":10,"output_tokens":20}}`)
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewReverseProxy: %v", err)
	}
	tracker := NewQuotaTracker()
	tracker.SetQuota("/metered", QuotaConfig{Limit: 1, Window: time.Hour, Scope: "per-route"})
	tracker.SetCostPerCall("/metered", 0.25)
	proxy.QuotaTracker = tracker
	server := &Server{quotaTracker: tracker, costPerCalls: map[string]float64{}}

	req := httptest.NewRequest(http.MethodPost, "http://seam.test/metered", nil)
	withRouteMatch(req, responseCostRoute(false), nil)
	rec := httptest.NewRecorder()
	server.quotaMiddleware(proxy).ServeHTTP(rec, req)

	if got := tracker.GetRemaining("/metered", "", ""); got != 0.5 {
		t.Fatalf("remaining = %v, want one post-response charge of 0.5", got)
	}
}

func TestResponseCostSkipsLegacyDispatchCharge(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":10,"output_tokens":20}}`)
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewReverseProxy: %v", err)
	}
	tracker := NewQuotaTracker()
	tracker.SetQuota("/metered", QuotaConfig{Limit: 1, Window: time.Hour, Scope: "per-route"})
	tracker.SetCostPerCall("/metered", 0.25)
	proxy.QuotaTracker = tracker

	req := httptest.NewRequest(http.MethodPost, "http://seam.test/metered", nil)
	withRouteMatch(req, responseCostRoute(false), nil)
	*req = *req.WithContext(contextWithQuotaCost(req.Context(), 0.25))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if got := tracker.GetRemaining("/metered", "", ""); got != 0.5 {
		t.Fatalf("remaining = %v, want only the post-response charge", got)
	}
}

func TestResponseCostStreamingChargesLastSSEEvent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("upstream does not support flushing")
		}
		_, _ = io.WriteString(w, "data: {\"usage\":{\"input_tokens\":4}}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: {\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n\ndata: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewReverseProxy: %v", err)
	}
	proxy.QuotaTracker = NewQuotaTracker()
	proxy.QuotaTracker.SetQuota("/metered", QuotaConfig{Limit: 1, Window: 3600 * 1e9, Scope: "per-route"})

	req := httptest.NewRequest(http.MethodPost, "http://seam.test/metered", nil)
	withRouteMatch(req, responseCostRoute(true), nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !strings.Contains(string(body), "output_tokens") {
		t.Fatalf("streamed body lost final event: %q", body)
	}
	if got := proxy.QuotaTracker.GetRemaining("/metered", "", ""); got != 0.5 {
		t.Fatalf("remaining = %v, want 0.5 from final event", got)
	}
	if got := rec.Header().Get("X-SEAM-Budget-Remaining"); !strings.HasPrefix(got, "amount=$0.5 unit=USD window=1h resets=") {
		t.Fatalf("budget header = %q, want post-stream balance", got)
	}
}

func TestResponseCostPointerMissFallsBackAndCountsMetric(t *testing.T) {
	config := &ResponseCostConfig{Charges: []ResponseCostCharge{{Pointer: "/usage/missing", Price: 1}}}
	cost, ok := config.responseCost([]byte(`{"usage":{}}`), false)
	if ok || cost != 0 {
		t.Fatalf("pointer miss = (%v, %v), want (0, false)", cost, ok)
	}

	metrics := newMetrics(nil, nil, nil, buildinfo.Read())
	metrics.recordResponseCostFallback("/metered")
	families, err := metrics.registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	var found bool
	for _, family := range families {
		if family.GetName() == "seam_cost_from_response_fallback_total" {
			found = true
		}
	}
	if !found {
		t.Fatal("response-cost fallback metric was not registered")
	}
}

func TestParseResponseCostConfigSupportsKeyedUsageCounters(t *testing.T) {
	config, err := parseResponseCostConfig(map[string]any{
		"input":  map[string]any{"pointer": "/usage/prompt_tokens", "price": 0.01},
		"output": map[string]any{"pointer": "/usage/completion_tokens", "price": 0.02},
	})
	if err != nil {
		t.Fatalf("parseResponseCostConfig: %v", err)
	}
	if len(config.Charges) != 2 {
		t.Fatalf("charges = %d, want 2", len(config.Charges))
	}

	var document map[string]any
	if err := json.Unmarshal([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":20}}`), &document); err != nil {
		t.Fatal(err)
	}
	if cost, ok := config.responseCost([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":20}}`), false); !ok || cost != 0.5 {
		t.Fatalf("cost = (%v, %v), want (0.5, true)", cost, ok)
	}
}
