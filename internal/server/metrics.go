package server

import (
	"fmt"
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Track previous eviction count to calculate deltas
	previousEvictions int64

	// Cache metrics
	metricCacheHits = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "seam_cache_hits_total",
		Help: "Total number of cache hits by route",
	}, []string{"route"})

	metricCacheMisses = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "seam_cache_misses_total",
		Help: "Total number of cache misses by route",
	}, []string{"route"})

	metricCacheHitRate = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "seam_cache_hit_rate",
		Help: "Overall cache hit rate (0-1)",
	})

	metricCacheSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "seam_cache_size",
		Help: "Current number of entries in the cache",
	})

	metricCacheEvictions = promauto.NewCounter(prometheus.CounterOpts{
		Name: "seam_cache_evictions_total",
		Help: "Total number of cache evictions",
	})

	// HTTP request metrics
	metricHTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "seam_http_requests_total",
		Help: "Total number of HTTP requests by route and method",
	}, []string{"route", "method", "status"})

	metricHTTPLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "seam_http_latency_seconds",
		Help:    "HTTP request latency in seconds by route and method",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"route", "method"})

	metricHTTPInFlight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "seam_http_requests_in_flight",
		Help: "Current number of in-flight HTTP requests by route and method",
	}, []string{"route", "method"})

	// Upstream health metrics
	metricUpstreamHealth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "seam_upstream_health",
		Help: "Upstream health status: 0=closed, 1=open, 2=half_open",
	}, []string{"origin"})

	metricUpstreamConsecutiveFailures = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "seam_upstream_consecutive_failures",
		Help: "Number of consecutive failures for upstream circuit breaker",
	}, []string{"origin"})

	// OpenBao cache metrics (placeholder for future OpenBao integration)
	metricOpenBaoCacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "seam_openbao_cache_hits_total",
		Help: "Total number of OpenBao cache hits (placeholder - OpenBao integration not yet implemented)",
	})

	// Quota metrics
	metricQuotaCost = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "seam_quota_cost_total",
		Help: "Total accumulated cost by route in USD",
	}, []string{"route"})

	// Registered with Prometheus at init but not yet populated by any caller.
	//nolint:unused // Awaiting the quota-accounting wiring.
	metricQuotaRemaining = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "seam_quota_remaining",
		Help: "Remaining quota by scope",
	}, []string{"scope"})

	metricQuotaExceeded = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "seam_quota_exceeded_total",
		Help: "Total number of quota exceeded errors by route",
	}, []string{"route"})

	metricQuotaBypassed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "seam_quota_bypassed_total",
		Help: "Total number of quota checks bypassed due to cache hit",
	}, []string{"route"})

	// SEAM build info metric
	seamBuildInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "seam_build_info",
		Help: "SEAM gateway build information",
	}, []string{"version", "go_version"})
)

func init() {
	// Register Go runtime collectors (goroutines, memory stats, GC stats, etc.)
	// Use Register instead of MustRegister to handle potential duplicates gracefully
	_ = prometheus.Register(collectors.NewGoCollector())
	_ = prometheus.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Set SEAM build info metrics
	// TODO: Populate version from build info (e.g., via ldflags)
	// For now, use runtime version as a placeholder
	seamBuildInfo.WithLabelValues("dev", runtime.Version()).Set(1)
}

// recordCacheHit records a cache hit for metrics
func recordCacheHit(route string) {
	metricCacheHits.WithLabelValues(route).Inc()
}

// recordCacheMiss records a cache miss for metrics
func recordCacheMiss(route string) {
	metricCacheMisses.WithLabelValues(route).Inc()
}

// updateCacheHitRate updates the overall cache hit rate gauge
// Hit rate is calculated as hits / (hits + misses)
func updateCacheHitRate(stats CacheStats) {
	total := stats.Hits + stats.Misses
	if total > 0 {
		hitRate := float64(stats.Hits) / float64(total)
		metricCacheHitRate.Set(hitRate)
	}
}

// updateCacheMetrics updates cache size, eviction, and hit rate metrics
func updateCacheMetrics(stats CacheStats) {
	metricCacheSize.Set(float64(stats.Size))

	// Calculate delta for evictions to avoid double-counting
	delta := stats.Evictions - previousEvictions
	if delta > 0 {
		metricCacheEvictions.Add(float64(delta))
		previousEvictions = stats.Evictions
	}

	// Update cache hit rate
	updateCacheHitRate(stats)
}

// recordQuotaCost records quota cost accumulation
func recordQuotaCost(route string, cost float64) {
	metricQuotaCost.WithLabelValues(route).Add(cost)
}

// recordQuotaExceeded records a quota exceeded event
func recordQuotaExceeded(route string) {
	metricQuotaExceeded.WithLabelValues(route).Inc()
}

// recordQuotaBypassed records a quota check bypassed due to cache hit
func recordQuotaBypassed(route string) {
	metricQuotaBypassed.WithLabelValues(route).Inc()
}

// recordHTTPRequest records an HTTP request completion with status code
func recordHTTPRequest(route, method string, statusCode int, duration float64) {
	metricHTTPRequests.WithLabelValues(route, method, fmt.Sprintf("%d", statusCode)).Inc()
	metricHTTPLatency.WithLabelValues(route, method).Observe(duration)
}

// incrementInFlight increments the in-flight request counter
func incrementInFlight(route, method string) {
	metricHTTPInFlight.WithLabelValues(route, method).Inc()
}

// decrementInFlight decrements the in-flight request counter
func decrementInFlight(route, method string) {
	metricHTTPInFlight.WithLabelValues(route, method).Dec()
}

// recordOpenBaoCacheHit records an OpenBao cache hit
func recordOpenBaoCacheHit() {
	metricOpenBaoCacheHits.Inc()
}

// setUpstreamHealth records upstream circuit breaker state
func setUpstreamHealth(origin string, state CircuitBreakerState, consecutiveFailures int) {
	var stateValue float64
	switch state {
	case CircuitBreakerClosed:
		stateValue = 0
	case CircuitBreakerOpen:
		stateValue = 1
	case CircuitBreakerHalfOpen:
		stateValue = 2
	default:
		stateValue = 0
	}

	metricUpstreamHealth.WithLabelValues(origin).Set(stateValue)
	metricUpstreamConsecutiveFailures.WithLabelValues(origin).Set(float64(consecutiveFailures))
}
