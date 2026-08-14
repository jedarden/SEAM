package server

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"runtime"
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

	// Quota metrics
	metricQuotaCost = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "seam_quota_cost_total",
		Help: "Total accumulated cost by route in USD",
	}, []string{"route"})

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
	_ = prometheus.Register(prometheus.NewGoCollector())
	_ = prometheus.Register(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	// Set SEAM build info metrics
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
