package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// Readiness dependency names reported by /_seam/readyz. The response body is
// a flat map of these names to booleans — true when the dependency is
// currently satisfied — plus the aggregate "ready" key. Keeping every value a
// boolean preserves the historical map[string]bool response shape, so
// existing probe consumers keep decoding it.
const (
	// readyzCheckRouteTable is satisfied when the current route table carries
	// at least one route, i.e. at least one valid fragment loaded and merged.
	// A reload that quarantines every fragment therefore takes the pod out of
	// the Service while /_seam/health keeps answering: liveness and readiness
	// stay separate concerns.
	readyzCheckRouteTable = "route_table"

	// readyzCheckAllowlist is satisfied when vault-path and upstream-host
	// allowlist enforcement is not fail-closed (Phase 2.2).
	readyzCheckAllowlist = "allowlist"

	// readyzCheckOpenBao is satisfied once the asynchronous startup
	// Kubernetes-auth login has completed. It is the gate behind the
	// seam-a155e900 503 regressions: the login runs in the background so an
	// OpenBao outage degrades readiness instead of crash-looping the
	// container, but a pod that cannot read credentials must not receive
	// traffic.
	readyzCheckOpenBao = "openbao"

	// readyzCheckCredentialProbe is satisfied when no credential probe is
	// configured, or when every tracked probe carries a successful
	// verification no older than twice its configured cadence plus a small
	// grace. Readiness gates on the freshness of the verification signal, not
	// on any single credential's health — an unhealthy credential is reported
	// at /health/credentials and does not by itself remove the pod from the
	// Service.
	readyzCheckCredentialProbe = "credential_probe"
)

// probeFreshnessGrace extends each probe's freshness window past twice its
// cadence, so a probe that is merely due for its next run is not reported
// stale by a readiness probe landing at the boundary.
const probeFreshnessGrace = 5 * time.Minute

// setCredentialProbes attaches the credential-probe registry the readiness
// gate evaluates. The probe loop publishes results here once leadership
// wiring lands; a server without an attached registry has no probes
// configured, and the credential-probe dependency is satisfied.
func (s *Server) setCredentialProbes(registry *CredentialProbeRegistry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credentialProbes = registry
}

// routeTableReady reports whether the current route table can serve traffic.
func (s *Server) routeTableReady() bool {
	return s.routeTableHolder.RouteCount() > 0
}

// credentialProbesFresh reports whether the configured credential probes
// carry a fresh verification signal. An attached registry with no results yet
// counts as fresh: the probe loop is in cold start, and failing readiness
// there would recreate the startup-503 class of regressions the OpenBao gate
// produced. Once a probe has reported, freshness applies for real.
func (s *Server) credentialProbesFresh(now time.Time) bool {
	s.mu.RLock()
	registry := s.credentialProbes
	s.mu.RUnlock()

	// Snapshot is nil-receiver safe: no registry means nothing is configured.
	results := registry.Snapshot()
	for _, result := range results {
		if !probeResultFresh(result, now) {
			return false
		}
	}
	return true
}

// probeResultFresh reports whether a probe result carries a successful
// verification recent enough to trust. LastVerified is stamped only by a 2xx
// probe (CredentialProbeLoop.probeTarget), so a result without one has never
// verified and is not fresh. The window scales with the probe's own cadence
// because fragment intervals legitimately range from minutes to a day.
func probeResultFresh(result CredentialProbeResult, now time.Time) bool {
	if result.LastVerified == nil {
		return false
	}
	interval := result.Interval
	if interval <= 0 {
		interval = defaultCredentialProbeInterval
	}
	return now.Sub(*result.LastVerified) <= 2*interval+probeFreshnessGrace
}

// readinessChecks evaluates the readiness dependency set on demand. The
// returned map is flat — every value is a boolean — and includes the
// aggregate ready key.
func (s *Server) readinessChecks() map[string]bool {
	checks := map[string]bool{
		readyzCheckRouteTable:      s.routeTableReady(),
		readyzCheckAllowlist:       s.allowlistEnforcer == nil || !s.allowlistEnforcer.IsFailClosed(),
		readyzCheckOpenBao:         s.isOpenBaoReady(),
		readyzCheckCredentialProbe: s.credentialProbesFresh(time.Now()),
	}

	ready := true
	for _, satisfied := range checks {
		if !satisfied {
			ready = false
		}
	}
	checks["ready"] = ready
	return checks
}

// readyzHandler returns readiness status. It answers 503 while any readiness
// dependency is unmet and names every dependency in the body, so an operator
// reading a 503 from the Deployment events can tell a missing route table
// from a pending OpenBao login without querying further endpoints.
func (s *Server) readyzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	checks := s.readinessChecks()

	statusCode := http.StatusOK
	if !checks["ready"] {
		statusCode = http.StatusServiceUnavailable
	}

	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(checks)
}
