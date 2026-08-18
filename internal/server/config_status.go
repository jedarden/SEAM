package server

import (
	"net/url"
	"os"
)

// runtimeConfigStatus builds the operator-facing runtime snapshot returned by
// /config/status. The snapshot deliberately contains configuration metadata
// only; credential material and URL user-info are never included.
func (s *Server) runtimeConfigStatus() map[string]interface{} {
	status := map[string]interface{}{
		"config": map[string]interface{}{
			"caller_port":     s.config.CallerPort,
			"operator_port":   s.config.OperatorPort,
			"base_url":        redactConfigURL(s.config.BaseURL),
			"spec_dir":        s.config.SpecDir,
			"fragment_mode":   s.config.FragmentMode,
			"schema_path":     s.config.SchemaPath,
			"capture_enabled": s.config.CaptureEnabled,
			"corpus_dir":      s.config.CorpusDir,
			"fragments_dir":   s.config.FragmentsDir,
			"upstream_ca_dir": s.config.UpstreamCADir,
		},
	}

	specStatus := map[string]interface{}{
		"hash":        "",
		"version":     "",
		"api_version": "",
	}
	routeCount := 0
	if s.specLoader != nil {
		specStatus["hash"] = s.specLoader.GetHash()
		specStatus["version"] = s.specLoader.GetVersion()
		specStatus["api_version"] = s.specLoader.GetAPIVersion()
		routeCount = len(s.specLoader.ListPaths())
	}
	status["spec"] = specStatus
	status["routes"] = map[string]interface{}{
		"enabled_count": routeCount,
	}

	// Enumerate routes with TLS exceptions for operator visibility
	tlsExceptions := s.enumerateTLSExceptions()
	if len(tlsExceptions) > 0 {
		status["tls_exceptions"] = tlsExceptions
	}

	// Detect and report dev-mode CA source
	devModeSource := detectDevModeCA()
	if devModeSource != "" {
		status["dev_mode_sources"] = map[string]interface{}{
			"upstream_ca_directory": devModeSource,
			"note":                  "Custom CA directory is active; ensure this is intentional for local development",
		}
	}

	corpusDir := s.config.CorpusDir
	corpusEnabled := false
	corpusEntryCount := 0
	if s.captureMiddleware != nil {
		corpusEnabled = s.captureMiddleware.IsEnabled()
		corpusEntryCount = s.captureMiddleware.GetEntryCount()
		corpusDir = s.captureMiddleware.corpusDir
	}
	status["corpus"] = map[string]interface{}{
		"enabled":     corpusEnabled,
		"entry_count": corpusEntryCount,
		"corpus_dir":  corpusDir,
	}

	if s.cache != nil {
		cacheStats := s.cache.Stats()
		totalRequests := cacheStats.Hits + cacheStats.Misses
		hitRate := 0.0
		if totalRequests > 0 {
			hitRate = float64(cacheStats.Hits) / float64(totalRequests)
		}
		cacheStatus := map[string]interface{}{
			"enabled":           true,
			"size":              cacheStats.Size,
			"hits":              cacheStats.Hits,
			"misses":            cacheStats.Misses,
			"evictions":         cacheStats.Evictions,
			"hit_rate":          hitRate,
			"routes_with_cache": len(s.cacheTTLs),
		}
		if s.singleFlight != nil {
			singleFlightStats := s.singleFlight.Stats()
			cacheStatus["single_flight"] = map[string]interface{}{
				"active_requests": singleFlightStats.ActiveRequests,
				"total_calls":     singleFlightStats.TotalCalls,
				"deduped_calls":   singleFlightStats.DedupedCalls,
				"coalesce_rate":   singleFlightStats.CoalesceRate,
			}
		}
		status["cache"] = cacheStatus
	} else {
		status["cache"] = map[string]interface{}{"enabled": false}
	}
	if s.quotaTracker != nil {
		status["quota"] = s.quotaTracker.GetQuotaStatus()
	} else {
		status["quota"] = map[string]interface{}{"enabled": false}
	}

	healthStatus := "healthy"
	issues := []string{}
	if specStatus["hash"] == "" {
		healthStatus = "unhealthy"
		issues = append(issues, "spec_not_loaded")
	}
	if s.config.CaptureEnabled && s.captureMiddleware == nil {
		if healthStatus == "healthy" {
			healthStatus = "degraded"
		}
		issues = append(issues, "capture_not_initialized")
	}

	// Check allowlist status (fail-closed condition)
	if s.allowlistEnforcer != nil && s.allowlistEnforcer.IsFailClosed() {
		healthStatus = "unhealthy"
		issues = append(issues, "allowlist_fail_closed")
		// Add allowlist condition to health checks
		if allowlistStatus := s.allowlistEnforcer.GetAllowlistStatus(); allowlistStatus != nil {
			if condition, ok := allowlistStatus["condition"].(string); ok {
				issues = append(issues, condition)
			}
		}
	}

	status["health"] = map[string]interface{}{
		"status": healthStatus,
		"issues": issues,
		"checks": map[string]bool{
			"spec_loaded":         specStatus["hash"] != "",
			"cache_available":     s.cache != nil,
			"quota_available":     s.quotaTracker != nil,
			"allowlist_available": s.allowlistEnforcer != nil && !s.allowlistEnforcer.IsFailClosed(),
		},
	}

	// Add allowlist status section
	if s.allowlistEnforcer != nil {
		status["allowlist"] = s.allowlistEnforcer.GetAllowlistStatus()
	}

	// Keep the original fragment status keys available for existing operators
	// while exposing the richer, stable sections above.
	if s.specLoader != nil {
		fragmentStatus := s.specLoader.GetFragmentStatus()
		for _, key := range []string{"fragments_loaded", "fragment_mode", "conditions"} {
			if value, ok := fragmentStatus[key]; ok {
				status[key] = value
			}
		}
	}

	return status
}

// redactConfigURL preserves the useful endpoint identity while removing URL
// user-info and query values, both of which are common places for credentials.
func redactConfigURL(raw string) string {
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "<invalid URL>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// enumerateTLSExceptions scans the route table for routes with TLS exceptions
// (skip-verify or plaintext) and returns a structured report for operator visibility.
func (s *Server) enumerateTLSExceptions() map[string]interface{} {
	if s.routeTable == nil {
		return map[string]interface{}{
			"skip_verify_routes": []interface{}{},
			"plaintext_routes":   []interface{}{},
		}
	}

	skipVerifyRoutes := make([]map[string]interface{}, 0)
	plaintextRoutes := make([]map[string]interface{}, 0)

	routes := s.routeTable.GetRoutes()
	for _, route := range routes {
		if route.TLSConfig == nil {
			continue
		}

		// Check for skip-verify (insecureSkipVerify)
		if route.TLSConfig.InsecureSkipVerify {
			skipVerifyRoutes = append(skipVerifyRoutes, map[string]interface{}{
				"path":        route.PathTemplate,
				"method":      route.Method,
				"api_version": route.APIVersion,
				"upstream":    redactConfigURL(route.UpstreamTarget),
				"server_name": route.TLSConfig.ServerName,
				"ca_bundle":   route.TLSConfig.CaBundle,
				"reason":      "x-upstream-tls.insecureSkipVerify: acknowledged",
			})
		}

		// Check for plaintext acknowledgment
		if route.TLSConfig.PlaintextAck {
			plaintextRoutes = append(plaintextRoutes, map[string]interface{}{
				"path":        route.PathTemplate,
				"method":      route.Method,
				"api_version": route.APIVersion,
				"upstream":    redactConfigURL(route.UpstreamTarget),
				"reason":      "x-upstream-plaintext: acknowledged",
			})
		}
	}

	return map[string]interface{}{
		"skip_verify_routes": skipVerifyRoutes,
		"plaintext_routes":   plaintextRoutes,
		"total_count":        len(skipVerifyRoutes) + len(plaintextRoutes),
	}
}

// detectDevModeCA checks if a custom upstream CA directory is being used,
// indicating local development mode. Returns the directory path if custom,
// empty string if using the default production path.
func detectDevModeCA() string {
	// Check if the upstream CA directory differs from the production default
	// This is set by the --upstream-ca-dir flag in local development
	if customDir := os.Getenv("SEAM_UPSTREAM_CA_DIR"); customDir != "" && customDir != DefaultUpstreamCADir {
		return customDir
	}
	return ""
}
