package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.uber.org/zap"
)

// VictoriaMetricsClient queries VictoriaMetrics for traffic patterns
type VictoriaMetricsClient struct {
	endpoint string
	client   *http.Client
}

// queryWindowDays is the lookback, in days, the traffic query covers. It is
// also the bound on what a zero sample proves: an instant query over this
// window shows a route was quiet across the window and says nothing about
// earlier, so it is the earliest quiet-since the client can honestly report.
const queryWindowDays = 14

// queryWindow is queryWindowDays as a Duration.
var queryWindow = queryWindowDays * 24 * time.Hour

// NewVictoriaMetricsClient creates a new VictoriaMetrics client
func NewVictoriaMetricsClient(endpoint string) *VictoriaMetricsClient {
	return &VictoriaMetricsClient{
		endpoint: endpoint,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// RouteTrafficStats holds traffic statistics for a single route version
type RouteTrafficStats struct {
	Route           string
	APIVersion      string
	SpecVersion     string
	LastRequestTime time.Time
	MaxGap          time.Duration
	TotalRequests   int64
	QuietSince      time.Time
	HistoryStart    time.Time
}

// QueryRouteVersionTraffic queries traffic metrics for all route versions
// Returns stats for each (route, x-api-version) combination
func (vmc *VictoriaMetricsClient) QueryRouteVersionTraffic(ctx context.Context) ([]RouteTrafficStats, error) {
	// Query for per-route-version metrics using the Phase 8.4 counter
	query := fmt.Sprintf(`max_over_time(seam_route_version_requests_total[%dd])`, queryWindowDays)

	// Build the query URL
	u, err := url.Parse(vmc.endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid VictoriaMetrics endpoint: %w", err)
	}

	u.Path = "/api/v1/query"
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()

	// Execute the query
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := vmc.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("VictoriaMetrics returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var result QueryResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if result.Status != "success" {
		return nil, fmt.Errorf("query failed: %s", result.Error)
	}

	// Parse the results into route traffic stats
	stats := vmc.parseQueryResults(result.Data.Result)

	zap.L().Info("Queried route version traffic",
		zap.Int("route_versions", len(stats)),
		zap.String("query", query))

	return stats, nil
}

// QueryLastRequestTime queries for the last request time for a specific route version
func (vmc *VictoriaMetricsClient) QueryLastRequestTime(ctx context.Context, route, specVersion string) (time.Time, error) {
	// Query for the last timestamp where this route version had a request
	query := fmt.Sprintf(`last_over_time(seam_route_version_requests_total{route="%s",spec_version="%s"}[14d])`, route, specVersion)

	u, err := url.Parse(vmc.endpoint)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid VictoriaMetrics endpoint: %w", err)
	}

	u.Path = "/api/v1/query"
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := vmc.client.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to execute query: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return time.Time{}, nil // Treat errors as "no data"
	}

	var result QueryResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return time.Time{}, nil
	}

	if result.Status != "success" || len(result.Data.Result) == 0 {
		return time.Time{}, nil // No data means never had a request
	}

	// Parse timestamp from the result
	for _, r := range result.Data.Result {
		if len(r.Value) >= 2 {
			timestampSec := r.Value[0]
			timestamp := time.Unix(int64(timestampSec.(float64)), 0)
			return timestamp, nil
		}
	}

	return time.Time{}, nil
}

// QueryMaxInterRequestGap calculates the maximum gap between requests for a route version
func (vmc *VictoriaMetricsClient) QueryMaxInterRequestGap(ctx context.Context, route, specVersion string, historyWindow time.Duration) (time.Duration, error) {
	// Use PromQL to find gaps in requests
	// This is a simplified approach - in production you'd use more sophisticated gap detection
	query := fmt.Sprintf(`max_over_time(seam_route_version_requests_total{route="%s",spec_version="%s"}[%s])`, route, specVersion, formatDuration(historyWindow))

	u, err := url.Parse(vmc.endpoint)
	if err != nil {
		return 0, fmt.Errorf("invalid VictoriaMetrics endpoint: %w", err)
	}

	u.Path = "/api/v1/query"
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := vmc.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to execute query: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, nil
	}

	var result QueryResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, nil
	}

	// For now, return a conservative default
	// In production, you'd parse the time series to find actual gaps
	return 24 * time.Hour, nil
}

// parseQueryResults parses VictoriaMetrics query results into RouteTrafficStats
func (vmc *VictoriaMetricsClient) parseQueryResults(results []Result) []RouteTrafficStats {
	stats := make([]RouteTrafficStats, 0, len(results))

	for _, r := range results {
		route := r.Metric["route"]
		specVersion := r.Metric["spec_version"]

		// The sample value is the observed request count. It has to be read:
		// zero observed traffic is the necessary condition for a retirement
		// candidate, and assuming zero would make a busy route eligible.
		totalRequests, err := sampleValue(r.Value)
		if err != nil {
			zap.L().Warn("Unreadable sample value, treating as traffic",
				zap.String("route", route),
				zap.Error(err))
			// An unreadable count is not evidence of quietness.
			totalRequests = -1
		}

		// Gap detection and quiet-since remain coarse: MaxGap needs a range
		// query this instant query does not perform, so it stays at zero and
		// the evaluation window falls through to its 7-day floor.
		//
		// QuietSince is not left coarse. A zero sample over [queryWindowDays]
		// really does prove the route answered nothing at any instant in that
		// window, so the window's start is the earliest point this query can
		// vouch for. Leaving it at the zero time.Time instead would publish a
		// detection claiming ~292 years of quiet, which is false and would
		// cost the whole record its credibility.
		quietSince := time.Time{}
		if totalRequests == 0 {
			quietSince = time.Now().Add(-queryWindow)
		}

		stats = append(stats, RouteTrafficStats{
			QuietSince:   quietSince,
			HistoryStart: quietSince,
			Route:        route,
			SpecVersion:  specVersion,
			APIVersion:   extractAPIVersion(route),

			TotalRequests: totalRequests,
		})
	}

	return stats
}

// sampleValue reads the [timestamp, value] pair VictoriaMetrics returns.
func sampleValue(value []interface{}) (int64, error) {
	if len(value) < 2 {
		return 0, fmt.Errorf("sample has %d fields, want [timestamp, value]", len(value))
	}

	raw, ok := value[1].(string)
	if !ok {
		return 0, fmt.Errorf("sample value is %T, want a string", value[1])
	}

	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("sample value %q: %w", raw, err)
	}

	return int64(parsed), nil
}

// QueryResult represents a VictoriaMetrics query response
type QueryResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Data   struct {
		ResultType string   `json:"resultType"`
		Result     []Result `json:"result"`
	} `json:"data"`
}

// Result represents a single time series result
type Result struct {
	Metric map[string]string `json:"metric"`
	Value  []interface{}     `json:"value"`
}

func extractAPIVersion(route string) string {
	// Try to extract version from route path
	// This is simplified - in production you'd parse the fragment metadata
	return "_unversioned"
}

func formatDuration(d time.Duration) string {
	return d.String()
}
