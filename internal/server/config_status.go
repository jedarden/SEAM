package server

import (
	"net/url"
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
	status["health"] = map[string]interface{}{
		"status": healthStatus,
		"issues": issues,
		"checks": map[string]bool{
			"spec_loaded":     specStatus["hash"] != "",
			"cache_available": s.cache != nil,
			"quota_available": s.quotaTracker != nil,
		},
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
