package spec

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var routePathParamPattern = regexp.MustCompile(`\{([^{}]+)\}`)

const internalRouteMetadataPrefix = "x-seam-internal-"

var internalRouteMetadata = map[string]string{
	"x-api-version":           internalRouteMetadataPrefix + "api-version",
	"x-upstream":              internalRouteMetadataPrefix + "upstream",
	"x-upstream-map":          internalRouteMetadataPrefix + "upstream-map",
	"x-vault-path":            internalRouteMetadataPrefix + "vault-path",
	"x-inject-as":             internalRouteMetadataPrefix + "inject-as",
	"x-unscrubbable":          internalRouteMetadataPrefix + "unscrubbable",
	"x-instance-param":        internalRouteMetadataPrefix + "instance-param",
	"x-upstream-strip-prefix": internalRouteMetadataPrefix + "upstream-strip-prefix",
	"x-fanout-scope":          internalRouteMetadataPrefix + "fanout-scope",
}

type pathRewriteFinding struct {
	code    string
	message string
}

func pathRewriteFindings(data map[string]any) []pathRewriteFinding {
	paths, _ := data["paths"].(map[string]any)
	if len(paths) == 0 {
		return nil
	}
	var findings []pathRewriteFinding
	instanceParam, _ := data["x-instance-param"].(string)
	stripPrefix, _ := data["x-upstream-strip-prefix"].(string)
	if stripPrefix != "" {
		if !strings.HasPrefix(stripPrefix, "/") {
			findings = append(findings, pathRewriteFinding{
				code:    "path-rewrite.strip-prefix-start",
				message: fmt.Sprintf("x-upstream-strip-prefix %q must start with '/'", stripPrefix),
			})
		}
		if strings.HasSuffix(stripPrefix, "/") || strings.Contains(stripPrefix, "{") {
			findings = append(findings, pathRewriteFinding{
				code:    "path-rewrite.strip-prefix-literal",
				message: fmt.Sprintf("x-upstream-strip-prefix %q must be a literal prefix without a trailing slash or parameter", stripPrefix),
			})
		}
	}

	pathNames := make([]string, 0, len(paths))
	for path := range paths {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)
	for _, path := range pathNames {
		if stripPrefix != "" && path != stripPrefix && !strings.HasPrefix(path, strings.TrimSuffix(stripPrefix, "/")+"/") {
			findings = append(findings, pathRewriteFinding{
				code:    "path-rewrite.strip-prefix-not-prefix",
				message: fmt.Sprintf("x-upstream-strip-prefix %q must prefix every declared path; it does not prefix %q", stripPrefix, path),
			})
		}
		if instanceParam != "" && !pathHasParam(path, instanceParam) {
			findings = append(findings, pathRewriteFinding{
				code:    "path-rewrite.instance-param-missing",
				message: fmt.Sprintf("x-instance-param %q is not present in declared path %q", instanceParam, path),
			})
		}

		pathItem, _ := paths[path].(map[string]any)
		if pathItem == nil {
			continue
		}
		template, present := pathItem["x-upstream-path-template"]
		if !present {
			continue
		}
		templateString, ok := template.(string)
		if !ok || !strings.HasPrefix(templateString, "/") {
			findings = append(findings, pathRewriteFinding{
				code:    "path-rewrite.template-start",
				message: fmt.Sprintf("x-upstream-path-template on %q must start with '/'", path),
			})
			continue
		}
		for _, parameter := range routePathParamPattern.FindAllStringSubmatch(templateString, -1) {
			name := parameter[1]
			if !pathHasParam(path, name) {
				findings = append(findings, pathRewriteFinding{
					code:    "path-rewrite.template-param-missing",
					message: fmt.Sprintf("x-upstream-path-template parameter %q on %q is not declared by the matched path", name, path),
				})
			}
			if instanceParam != "" && name == instanceParam {
				findings = append(findings, pathRewriteFinding{
					code:    "path-rewrite.template-instance-param",
					message: fmt.Sprintf("x-upstream-path-template on %q must not name designated instance parameter %q", path, instanceParam),
				})
			}
		}
	}
	return findings
}

func pathHasParam(path, wanted string) bool {
	for _, parameter := range routePathParamPattern.FindAllStringSubmatch(path, -1) {
		if parameter[1] == wanted {
			return true
		}
	}
	return false
}

// ValidatePathRewriteFragment is the merge-time counterpart to seam lint.
// It is deliberately independent of the JSON Schema so a runtime merge never
// trusts that a CI lint command ran first.
func ValidatePathRewriteFragment(data map[string]any) error {
	findings := pathRewriteFindings(data)
	if len(findings) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s", findings[0].code, findings[0].message)
}

func (f *lintFragment) checkPathRewrite(report *LintReport) {
	for _, finding := range pathRewriteFindings(f.data) {
		f.addError(report, finding.code, finding.message)
	}
	f.checkInstanceParamRequirements(report)
	f.checkUpstreamMapEntries(report)
}

// checkInstanceParamRequirements validates Phase 10 constraints:
// - x-instance-param is required with x-upstream-map
// - x-instance-param is forbidden without x-upstream-map
// Note: The per-path validation (named param exists in EVERY declared path) is
// already handled by pathRewriteFindings via the path-rewrite.instance-param-missing error.
func (f *lintFragment) checkInstanceParamRequirements(report *LintReport) {
	hasUpstreamMap := f.data["x-upstream-map"] != nil
	hasInstanceParam := f.data["x-instance-param"] != nil

	if hasUpstreamMap && !hasInstanceParam {
		f.addError(report, "instance-param.missing-with-map", "x-upstream-map requires x-instance-param at fragment root")
		return
	}

	if !hasUpstreamMap && hasInstanceParam {
		f.addError(report, "instance-param.forbidden-without-map", "x-instance-param is only valid with x-upstream-map")
		return
	}

	if hasInstanceParam {
		instanceParam, _ := f.data["x-instance-param"].(string)
		if instanceParam == "" {
			f.addError(report, "instance-param.empty", "x-instance-param must be a non-empty string")
			return
		}
	}
}

// checkUpstreamMapEntries validates Phase 10 map entry constraints:
// - Object-form entries with url (required), vaultPath, injectAs, tls, plaintext, probeInterval, breaker, requiredScope
// - Fragment-level fields default to map entries EXCEPT plaintext (per-entry, never inherited)
// - requiredScope is per-entry with no default (PARSE AND CARRY only)
func (f *lintFragment) checkUpstreamMapEntries(report *LintReport) {
	entries, ok := f.data["x-upstream-map"].(map[string]any)
	if !ok || len(entries) == 0 {
		return
	}

	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		entry, ok := entries[key].(map[string]any)
		if !ok {
			f.addError(report, "upstream-map.entry-invalid", fmt.Sprintf("x-upstream-map[%q] must be an object", key))
			continue
		}

		// url is required
		if _, hasURL := entry["url"]; !hasURL {
			f.addError(report, "upstream-map.missing-url", fmt.Sprintf("x-upstream-map[%q].url is required", key))
		}

		// vaultPath and injectAs must both be present or both absent (per-entry pairing)
		_, hasVaultPath := entry["vaultPath"]
		_, hasInjectAs := entry["injectAs"]
		if (hasVaultPath && !hasInjectAs) || (!hasVaultPath && hasInjectAs) {
			f.addError(report, "upstream-map.vault-inject-pairing", fmt.Sprintf("x-upstream-map[%q] must have both vaultPath and injectAs, or neither", key))
		}

		// plaintext is per-entry, never inherited from fragment level
		// This is already enforced by checkTransport's "plaintext-excludes-map" rule
		// which rejects fragment-root x-upstream-plaintext alongside x-upstream-map

		// requiredScope is per-entry with no default; PARSE AND CARRY only
		// Validation here just ensures it's a valid scope array if present
		if scope, hasScope := entry["requiredScope"]; hasScope {
			if err := validateScopeArray(scope); err != nil {
				f.addError(report, "upstream-map.invalid-scope", fmt.Sprintf("x-upstream-map[%q].requiredScope is invalid: %v", key, err))
			}
		}

		// Validate other field types if present
		if tls, hasTLS := entry["tls"].(map[string]any); hasTLS {
			// tls validation - already handled by schema
			_ = tls
		}

		if breaker, hasBreaker := entry["breaker"].(map[string]any); hasBreaker {
			// breaker validation - already handled by schema
			_ = breaker
		}

		if probeInterval, hasProbe := entry["probeInterval"].(string); hasProbe {
			// probeInterval validation - already handled by schema
			_ = probeInterval
		}
	}
}

// validateScopeArray checks if a scope value is valid (string or array of strings)
func validateScopeArray(value any) error {
	switch v := value.(type) {
	case string:
		if v == "" {
			return fmt.Errorf("scope string cannot be empty")
		}
		return nil
	case []any:
		if len(v) == 0 {
			return fmt.Errorf("scope array cannot be empty")
		}
		for i, item := range v {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("scope array element %d must be a string", i)
			}
		}
		return nil
	default:
		return fmt.Errorf("scope must be a string or array of strings")
	}
}

// ValidatePathRewriteCoherence quarantines a fragment as a whole if its
// cross-field path rewrite rules fail after schema validation.
func (fl *FragmentLoader) ValidatePathRewriteCoherence() {
	if fl == nil {
		return
	}
	kept := make([]*Fragment, 0, len(fl.fragments))
	for _, fragment := range fl.fragments {
		if err := ValidatePathRewriteFragment(fragment.ParsedFragment); err != nil {
			fragment.QueuedForQuarantine = true
			fragment.QuarantineReasons = append(fragment.QuarantineReasons, err.Error())
			fl.quarantined = append(fl.quarantined, fragment)
			continue
		}
		kept = append(kept, fragment)
	}
	fl.fragments = kept
}

// PropagateRouteMetadata retains fragment-root routing metadata on each path
// item for the route-table builder. The marker is internal and is stripped
// from the served OpenAPI document by Loader.GetRawJSON.
func (fl *FragmentLoader) PropagateRouteMetadata() {
	if fl == nil {
		return
	}
	for _, fragment := range fl.fragments {
		paths, _ := fragment.ParsedFragment["paths"].(map[string]any)
		for _, pathValue := range paths {
			pathItem, _ := pathValue.(map[string]any)
			if pathItem == nil {
				continue
			}
			for source, internal := range internalRouteMetadata {
				if value, ok := fragment.ParsedFragment[source]; ok {
					pathItem[internal] = value
				}
			}
		}
	}
}

func stripInternalRouteMetadata(value any) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key := range typed {
			if strings.HasPrefix(key, internalRouteMetadataPrefix) {
				delete(typed, key)
				continue
			}
			stripInternalRouteMetadata(typed[key])
		}
	case []interface{}:
		for _, child := range typed {
			stripInternalRouteMetadata(child)
		}
	}
}
