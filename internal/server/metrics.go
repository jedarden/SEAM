package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ardenone/seam/internal/buildinfo"
	"github.com/ardenone/seam/internal/vault"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const unmatchedMetricRoute = "unmatched"

// metricRouteLabels is attached to a request before cache and dispatch
// middleware run. Route is always the OpenAPI template, never the concrete URL,
// which keeps label cardinality bounded when paths contain IDs.
type metricRouteLabels struct {
	Route       string
	Version     string
	SpecVersion string // Phase 8.4: Spec hash (truncated to 16 chars) for per-route-version metrics
}

type metricRouteContextKey struct{}

// Metrics owns one Prometheus registry per Server. Keeping instrumentation out
// of the global registry prevents tests and multiple in-process servers from
// leaking counters into one another.
type Metrics struct {
	registry *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	httpInFlight *prometheus.GaugeVec

	routeVersionRequests *prometheus.CounterVec // Phase 8.4: Per-route-version request counter

	cacheHits   *prometheus.CounterVec
	cacheMisses *prometheus.CounterVec

	quotaCost      *prometheus.CounterVec
	quotaRemaining *prometheus.GaugeVec
	quotaExceeded  *prometheus.CounterVec
	quotaBypassed  *prometheus.CounterVec

	// Starvation recovery metrics
	recoveryAttempts   *prometheus.CounterVec
	recoverySuccesses  *prometheus.CounterVec
	recoveryStageUsed  *prometheus.CounterVec
	recoveryDuration   *prometheus.HistogramVec
	checkpointRebuilds *prometheus.CounterVec

	// Repair queue and daemon metrics
	repairQueueSize        *prometheus.GaugeVec
	repairAttempts         *prometheus.CounterVec
	repairSuccesses        *prometheus.CounterVec
	repairEscalations      *prometheus.CounterVec
	repairDuration         *prometheus.HistogramVec
	repairQueueByRootCause *prometheus.GaugeVec

	// Starvation diagnostic daemon metrics
	diagnosticRuns       *prometheus.CounterVec
	diagnosticDurations  *prometheus.HistogramVec
	diagnosticRepairable *prometheus.GaugeVec
}

type responseCacheStatsProvider interface {
	Stats() CacheStats
}

type openBaoCacheStatsProvider interface {
	OpenBaoCacheStats() vault.CacheStats
}

func newMetrics(
	cache responseCacheStatsProvider,
	openBao openBaoCacheStatsProvider,
	upstreams CircuitBreakerStateProvider,
	build buildinfo.Info,
) *Metrics {
	registry := prometheus.NewRegistry()
	metrics := &Metrics{
		registry: registry,
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_http_requests_total",
			Help: "Total caller requests by OpenAPI route template, method, API version, and HTTP status.",
		}, []string{"route", "method", "version", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "seam_http_request_duration_seconds",
			Help:    "Caller request duration by OpenAPI route template, method, and API version.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"route", "method", "version"}),
		httpInFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "seam_http_requests_in_flight",
			Help: "Current caller requests by OpenAPI route template, method, and API version.",
		}, []string{"route", "method", "version"}),
		cacheHits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_cache_hits_total",
			Help: "Total response-cache hits by OpenAPI route template and API version.",
		}, []string{"route", "version"}),
		cacheMisses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_cache_misses_total",
			Help: "Total response-cache misses by OpenAPI route template and API version.",
		}, []string{"route", "version"}),
		quotaCost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_quota_cost_total",
			Help: "Total accumulated route cost in USD.",
		}, []string{"route"}),
		quotaRemaining: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "seam_quota_remaining",
			Help: "Remaining quota by scope.",
		}, []string{"scope"}),
		quotaExceeded: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_quota_exceeded_total",
			Help: "Total quota-exceeded responses by route.",
		}, []string{"route"}),
		quotaBypassed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_quota_bypassed_total",
			Help: "Total quota checks bypassed by response-cache hits.",
		}, []string{"route"}),
		routeVersionRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_route_version_requests_total",
			Help: "Total requests by OpenAPI route template and spec version hash (Phase 8.4: probe-free, origin-tag matched). A probe-inclusive counter means no probed fragment ever retires.",
		}, []string{"route", "spec_version"}),
		recoveryAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_starvation_recovery_attempts_total",
			Help: "Total starvation recovery attempts by workspace.",
		}, []string{"workspace"}),
		recoverySuccesses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_starvation_recovery_successes_total",
			Help: "Total successful starvation recoveries by workspace and recovery stage.",
		}, []string{"workspace", "stage"}),
		recoveryStageUsed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_starvation_recovery_stage_used_total",
			Help: "Total usage count of each recovery stage.",
		}, []string{"stage"}),
		recoveryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "seam_starvation_recovery_duration_seconds",
			Help:    "Starvation recovery operation duration by workspace and result.",
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"workspace", "result"}),
		checkpointRebuilds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_starvation_checkpoint_rebuilds_total",
			Help: "Total checkpoint database rebuilds by workspace and result.",
		}, []string{"workspace", "result"}),
		repairQueueSize: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "seam_repair_queue_size",
			Help: "Current number of items in the repair queue by workspace.",
		}, []string{"workspace"}),
		repairAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_repair_attempts_total",
			Help: "Total repair attempts by workspace and root cause.",
		}, []string{"workspace", "root_cause"}),
		repairSuccesses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_repair_successes_total",
			Help: "Total successful repairs by workspace and root cause.",
		}, []string{"workspace", "root_cause"}),
		repairEscalations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_repair_escalations_total",
			Help: "Total repairs escalated to human review by workspace and root cause.",
		}, []string{"workspace", "root_cause"}),
		repairDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "seam_repair_duration_seconds",
			Help:    "Repair operation duration by workspace, root cause, and result.",
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"workspace", "root_cause", "result"}),
		repairQueueByRootCause: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "seam_repair_queue_by_root_cause",
			Help: "Current number of queued items by root cause category.",
		}, []string{"root_cause"}),
		diagnosticRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "seam_diagnostic_runs_total",
			Help: "Total diagnostic runs by workspace and root cause.",
		}, []string{"workspace", "root_cause"}),
		diagnosticDurations: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "seam_diagnostic_duration_seconds",
			Help:    "Diagnostic operation duration by workspace.",
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"workspace"}),
		diagnosticRepairable: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "seam_diagnostic_repairable",
			Help: "Whether diagnostic result was auto-repairable by workspace.",
		}, []string{"workspace"}),
	}

	registry.MustRegister(
		metrics.httpRequests,
		metrics.httpDuration,
		metrics.httpInFlight,
		metrics.routeVersionRequests, // Phase 8.4
		metrics.cacheHits,
		metrics.cacheMisses,
		metrics.quotaCost,
		metrics.quotaRemaining,
		metrics.quotaExceeded,
		metrics.quotaBypassed,
		metrics.recoveryAttempts,
		metrics.recoverySuccesses,
		metrics.recoveryStageUsed,
		metrics.recoveryDuration,
		metrics.checkpointRebuilds,
		metrics.repairQueueSize,
		metrics.repairAttempts,
		metrics.repairSuccesses,
		metrics.repairEscalations,
		metrics.repairDuration,
		metrics.repairQueueByRootCause,
		metrics.diagnosticRuns,
		metrics.diagnosticDurations,
		metrics.diagnosticRepairable,
		newStateMetricsCollector(cache, openBao, upstreams, build),
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return metrics
}

func (m *Metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

func (m *Metrics) recordHTTPRequest(labels metricRouteLabels, method string, statusCode int, duration time.Duration) {
	if m == nil {
		return
	}
	m.httpRequests.WithLabelValues(labels.Route, method, labels.Version, strconv.Itoa(statusCode)).Inc()
	m.httpDuration.WithLabelValues(labels.Route, method, labels.Version).Observe(duration.Seconds())
}

func (m *Metrics) incrementInFlight(labels metricRouteLabels, method string) {
	if m != nil {
		m.httpInFlight.WithLabelValues(labels.Route, method, labels.Version).Inc()
	}
}

func (m *Metrics) decrementInFlight(labels metricRouteLabels, method string) {
	if m != nil {
		m.httpInFlight.WithLabelValues(labels.Route, method, labels.Version).Dec()
	}
}

func (m *Metrics) recordCacheHit(labels metricRouteLabels) {
	if m != nil {
		m.cacheHits.WithLabelValues(labels.Route, labels.Version).Inc()
	}
}

func (m *Metrics) recordCacheMiss(labels metricRouteLabels) {
	if m != nil {
		m.cacheMisses.WithLabelValues(labels.Route, labels.Version).Inc()
	}
}

func (m *Metrics) recordQuotaCost(route string, cost float64) {
	if m != nil {
		m.quotaCost.WithLabelValues(route).Add(cost)
	}
}

func (m *Metrics) recordQuotaExceeded(route string) {
	if m != nil {
		m.quotaExceeded.WithLabelValues(route).Inc()
	}
}

func (m *Metrics) recordQuotaBypassed(route string) {
	if m != nil {
		m.quotaBypassed.WithLabelValues(route).Inc()
	}
}

// recordRouteVersionRequest records a per-route-version request (Phase 8.4).
// This is probe-free by definition: origin-tag matched, so a probe-inclusive
// counter means no probed fragment ever retires.
func (m *Metrics) recordRouteVersionRequest(route string, specVersion string) {
	if m != nil {
		m.routeVersionRequests.WithLabelValues(route, specVersion).Inc()
	}
}

// stateMetricsCollector translates existing in-memory health and cache state
// into Prometheus samples at scrape time. Pulling these values avoids a second
// mutable copy and means removed upstreams disappear on the next scrape.
type stateMetricsCollector struct {
	cache     responseCacheStatsProvider
	openBao   openBaoCacheStatsProvider
	upstreams CircuitBreakerStateProvider
	build     buildinfo.Info

	buildInfo                   *prometheus.Desc
	cacheHitRate                *prometheus.Desc
	cacheSize                   *prometheus.Desc
	cacheEvictions              *prometheus.Desc
	openBaoCacheHits            *prometheus.Desc
	openBaoCacheMisses          *prometheus.Desc
	openBaoCacheFetches         *prometheus.Desc
	openBaoCacheEntries         *prometheus.Desc
	upstreamHealth              *prometheus.Desc
	upstreamBreakerEnabled      *prometheus.Desc
	upstreamConsecutiveFailures *prometheus.Desc
}

func newStateMetricsCollector(
	cache responseCacheStatsProvider,
	openBao openBaoCacheStatsProvider,
	upstreams CircuitBreakerStateProvider,
	build buildinfo.Info,
) *stateMetricsCollector {
	return &stateMetricsCollector{
		cache:     cache,
		openBao:   openBao,
		upstreams: upstreams,
		build:     build,
		buildInfo: prometheus.NewDesc(
			"seam_build_info",
			"SEAM build and runtime information.",
			[]string{"version", "commit", "go_version", "modified"}, nil,
		),
		cacheHitRate: prometheus.NewDesc(
			"seam_cache_hit_rate",
			"Process-wide response-cache hit ratio from zero to one.", nil, nil,
		),
		cacheSize: prometheus.NewDesc(
			"seam_cache_size",
			"Current number of response-cache entries.", nil, nil,
		),
		cacheEvictions: prometheus.NewDesc(
			"seam_cache_evictions_total",
			"Total response-cache evictions.", nil, nil,
		),
		openBaoCacheHits: prometheus.NewDesc(
			"seam_openbao_cache_hits_total",
			"Total OpenBao secret-cache hits.", nil, nil,
		),
		openBaoCacheMisses: prometheus.NewDesc(
			"seam_openbao_cache_misses_total",
			"Total OpenBao secret-cache misses.", nil, nil,
		),
		openBaoCacheFetches: prometheus.NewDesc(
			"seam_openbao_cache_fetches_total",
			"Total remote OpenBao secret fetches after request coalescing.", nil, nil,
		),
		openBaoCacheEntries: prometheus.NewDesc(
			"seam_openbao_cache_entries",
			"Current number of OpenBao secret-cache entries.", nil, nil,
		),
		upstreamHealth: prometheus.NewDesc(
			"seam_upstream_health",
			"Upstream circuit-breaker state as a one-hot gauge.",
			[]string{"origin", "state"}, nil,
		),
		upstreamBreakerEnabled: prometheus.NewDesc(
			"seam_upstream_breaker_enabled",
			"Whether the upstream circuit breaker is enabled (1) or disabled (0).",
			[]string{"origin"}, nil,
		),
		upstreamConsecutiveFailures: prometheus.NewDesc(
			"seam_upstream_consecutive_failures",
			"Current consecutive qualifying failures for an upstream.",
			[]string{"origin"}, nil,
		),
	}
}

func (c *stateMetricsCollector) Describe(descriptions chan<- *prometheus.Desc) {
	for _, description := range []*prometheus.Desc{
		c.buildInfo,
		c.cacheHitRate,
		c.cacheSize,
		c.cacheEvictions,
		c.openBaoCacheHits,
		c.openBaoCacheMisses,
		c.openBaoCacheFetches,
		c.openBaoCacheEntries,
		c.upstreamHealth,
		c.upstreamBreakerEnabled,
		c.upstreamConsecutiveFailures,
	} {
		descriptions <- description
	}
}

func (c *stateMetricsCollector) Collect(metrics chan<- prometheus.Metric) {
	metrics <- prometheus.MustNewConstMetric(
		c.buildInfo,
		prometheus.GaugeValue,
		1,
		c.build.Version,
		c.build.Revision,
		c.build.GoVersion,
		c.build.Modified,
	)

	cacheStats := CacheStats{}
	if c.cache != nil {
		cacheStats = c.cache.Stats()
	}
	hitRate := float64(0)
	if requests := cacheStats.Hits + cacheStats.Misses; requests > 0 {
		hitRate = float64(cacheStats.Hits) / float64(requests)
	}
	metrics <- prometheus.MustNewConstMetric(c.cacheHitRate, prometheus.GaugeValue, hitRate)
	metrics <- prometheus.MustNewConstMetric(c.cacheSize, prometheus.GaugeValue, float64(cacheStats.Size))
	metrics <- prometheus.MustNewConstMetric(c.cacheEvictions, prometheus.CounterValue, float64(cacheStats.Evictions))

	openBaoStats := vault.CacheStats{}
	if c.openBao != nil {
		openBaoStats = c.openBao.OpenBaoCacheStats()
	}
	metrics <- prometheus.MustNewConstMetric(c.openBaoCacheHits, prometheus.CounterValue, float64(openBaoStats.Hits))
	metrics <- prometheus.MustNewConstMetric(c.openBaoCacheMisses, prometheus.CounterValue, float64(openBaoStats.Misses))
	metrics <- prometheus.MustNewConstMetric(c.openBaoCacheFetches, prometheus.CounterValue, float64(openBaoStats.Fetches))
	metrics <- prometheus.MustNewConstMetric(c.openBaoCacheEntries, prometheus.GaugeValue, float64(openBaoStats.Entries))

	if c.upstreams == nil {
		return
	}
	for _, upstream := range c.upstreams.Snapshot() {
		for _, state := range []CircuitBreakerState{CircuitBreakerClosed, CircuitBreakerOpen, CircuitBreakerHalfOpen} {
			value := float64(0)
			if upstream.State == state {
				value = 1
			}
			metrics <- prometheus.MustNewConstMetric(
				c.upstreamHealth,
				prometheus.GaugeValue,
				value,
				upstream.Origin,
				string(state),
			)
		}
		enabled := float64(0)
		if upstream.Enabled {
			enabled = 1
		}
		metrics <- prometheus.MustNewConstMetric(
			c.upstreamBreakerEnabled,
			prometheus.GaugeValue,
			enabled,
			upstream.Origin,
		)
		metrics <- prometheus.MustNewConstMetric(
			c.upstreamConsecutiveFailures,
			prometheus.GaugeValue,
			float64(upstream.ConsecutiveFailures),
			upstream.Origin,
		)
	}
}

// Recovery metric recording methods

//nolint:unused // recovery recorders land with the checkpoint-recovery chain
func (m *Metrics) recordRecoveryAttempt(workspace string) {
	if m == nil {
		return
	}
	m.recoveryAttempts.WithLabelValues(workspace).Inc()
}

//nolint:unused // recovery recorders land with the checkpoint-recovery chain
func (m *Metrics) recordRecoverySuccess(workspace, stage string) {
	if m == nil {
		return
	}
	m.recoverySuccesses.WithLabelValues(workspace, stage).Inc()
}

//nolint:unused // recovery recorders land with the checkpoint-recovery chain
func (m *Metrics) recordRecoveryStageUsed(stage string) {
	if m == nil {
		return
	}
	m.recoveryStageUsed.WithLabelValues(stage).Inc()
}

//nolint:unused // recovery recorders land with the checkpoint-recovery chain
func (m *Metrics) recordRecoveryDuration(workspace string, result string, duration float64) {
	if m == nil {
		return
	}
	m.recoveryDuration.WithLabelValues(workspace, result).Observe(duration)
}

//nolint:unused // recovery recorders land with the checkpoint-recovery chain
func (m *Metrics) recordCheckpointRebuild(workspace string, result string) {
	if m == nil {
		return
	}
	m.checkpointRebuilds.WithLabelValues(workspace, result).Inc()
}

// Repair metric recording methods

func (m *Metrics) RecordRepairResult(workspace, rootCause string, success bool) {
	if m == nil {
		return
	}
	if success {
		m.repairSuccesses.WithLabelValues(workspace, rootCause).Inc()
	}
	m.repairAttempts.WithLabelValues(workspace, rootCause).Inc()
}

func (m *Metrics) RecordRepairEscalation(workspace, rootCause string) {
	if m == nil {
		return
	}
	m.repairEscalations.WithLabelValues(workspace, rootCause).Inc()
}

func (m *Metrics) RecordRepairDuration(workspace, rootCause, result string, duration float64) {
	if m == nil {
		return
	}
	m.repairDuration.WithLabelValues(workspace, rootCause, result).Observe(duration)
}

func (m *Metrics) UpdateRepairQueueSize(workspace string, size float64) {
	if m == nil {
		return
	}
	m.repairQueueSize.WithLabelValues(workspace).Set(size)
}

func (m *Metrics) UpdateRepairQueueByRootCause(rootCause string, count float64) {
	if m == nil {
		return
	}
	m.repairQueueByRootCause.WithLabelValues(rootCause).Set(count)
}

// Diagnostic metric recording methods

func (m *Metrics) RecordDiagnosticRun(workspace, rootCause string, repairable bool) {
	if m == nil {
		return
	}
	m.diagnosticRuns.WithLabelValues(workspace, rootCause).Inc()
	repairableValue := 0.0
	if repairable {
		repairableValue = 1.0
	}
	m.diagnosticRepairable.WithLabelValues(workspace).Set(repairableValue)
}

func (m *Metrics) RecordDiagnosticDuration(workspace string, duration float64) {
	if m == nil {
		return
	}
	m.diagnosticDurations.WithLabelValues(workspace).Observe(duration)
}
