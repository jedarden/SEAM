package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// retirementMetrics is the evaluator's whole Prometheus surface. The evaluator
// is detection-only and deliberately carries no third-party metric client, so
// the registry renders the standard text exposition format itself; the label
// sets are small and closed, which is what makes that tractable.
type retirementMetrics struct {
	mu sync.Mutex

	// candidateCounts is keyed by the label triple of a deprecation
	// candidate, so one route version that stays quiet across many
	// evaluation runs accumulates rather than resetting.
	candidateCounts map[routeVersionKey]uint64
	runCounts       map[string]uint64
	evaluatedRoutes int
}

// routeVersionKey identifies one (route, x-api-version, spec-version) triple,
// the same unit VictoriaMetrics reports and the same unit a deprecation
// candidate is emitted for.
type routeVersionKey struct {
	route       string
	apiVersion  string
	specVersion string
}

func newRetirementMetrics() *retirementMetrics {
	return &retirementMetrics{
		candidateCounts: make(map[routeVersionKey]uint64),
		runCounts:       make(map[string]uint64),
	}
}

// recordCandidate counts one deprecation candidate emitted for a route version.
func (m *retirementMetrics) recordCandidate(k routeVersionKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.candidateCounts[k]++
}

// recordRun counts one evaluation run, keyed by its outcome.
func (m *retirementMetrics) recordRun(result string, routes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runCounts[result]++
	m.evaluatedRoutes = routes
}

// render returns the registry in the Prometheus text exposition format.
// Series are ordered by label so repeated scrapes of an unchanged registry
// produce byte-identical output.
func (m *retirementMetrics) render() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var b strings.Builder

	fmt.Fprintln(&b, "# HELP seam_retirement_deprecation_candidates_total Deprecation candidates emitted, by route version. Detection-only: the evaluator never writes to a git host.")
	fmt.Fprintln(&b, "# TYPE seam_retirement_deprecation_candidates_total counter")

	keys := make([]routeVersionKey, 0, len(m.candidateCounts))
	for k := range m.candidateCounts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		if keys[i].apiVersion != keys[j].apiVersion {
			return keys[i].apiVersion < keys[j].apiVersion
		}
		return keys[i].specVersion < keys[j].specVersion
	})
	for _, k := range keys {
		fmt.Fprintf(&b, "seam_retirement_deprecation_candidates_total{route=%s,api_version=%s,spec_version=%s} %d\n",
			renderLabelValue(k.route), renderLabelValue(k.apiVersion), renderLabelValue(k.specVersion), m.candidateCounts[k])
	}

	fmt.Fprintln(&b, "# HELP seam_retirement_evaluation_runs_total Retirement evaluation runs, by outcome.")
	fmt.Fprintln(&b, "# TYPE seam_retirement_evaluation_runs_total counter")
	results := make([]string, 0, len(m.runCounts))
	for r := range m.runCounts {
		results = append(results, r)
	}
	sort.Strings(results)
	for _, r := range results {
		fmt.Fprintf(&b, "seam_retirement_evaluation_runs_total{result=%s} %d\n", renderLabelValue(r), m.runCounts[r])
	}

	fmt.Fprintln(&b, "# HELP seam_retirement_routes_evaluated Route versions considered by the most recent evaluation run.")
	fmt.Fprintln(&b, "# TYPE seam_retirement_routes_evaluated gauge")
	fmt.Fprintf(&b, "seam_retirement_routes_evaluated %d\n", m.evaluatedRoutes)

	return b.String()
}

// renderLabelValue escapes a string for use as a quoted label value.
func renderLabelValue(v string) string {
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
	return `"` + r.Replace(v) + `"`
}

// ServeHTTP exposes the registry for a scraper.
func (m *retirementMetrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprint(w, m.render())
}
