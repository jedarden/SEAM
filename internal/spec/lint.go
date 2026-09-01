package spec

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const (
	defaultLintFragmentsDir = "./fragments"
	defaultLintSchemaPath   = "./spec/route-fragment-schema.json"

	// This is the prefix encoded by the v1 route-fragment schema. The owner
	// segment after it is checked separately against x-seam-owner.
	vaultRoutesPrefix = "seam/routes/"
)

var (
	apiVersionPattern  = regexp.MustCompile(`^v[1-9][0-9]*$`)
	guardWindowPattern = regexp.MustCompile(`^([0-9]+)([smhd])$`)

	reservedExactPaths = map[string]struct{}{
		"/docs":               {},
		"/openapi.json":       {},
		"/whoami":             {},
		"/scopes":             {},
		"/changes":            {},
		"/health/credentials": {},
		"/health/upstreams":   {},
		"/config/status":      {},
	}
	reservedPathPrefixes = []string{
		"/docs/",
		"/health/",
		"/config/",
		"/approvals/",
		"/_seam/",
	}
)

// LintOptions controls the inputs to LintDirectory. An absent
// UpstreamAllowlistPath is intentionally different from an empty allowlist:
// before Phase 6a there is no operator manifest to inspect, so host membership
// is inert. Once a file is supplied, it is authoritative and fail-closed.
type LintOptions struct {
	FragmentsDir          string
	SchemaPath            string
	UpstreamAllowlistPath string
}

// LintFinding is one deterministic lint diagnostic. Warnings are surfaced for
// human review but do not make the lint command fail; errors do.
type LintFinding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	File     string `json:"file,omitempty"`
	Message  string `json:"message"`
}

// LintReport is the complete result of linting a fragment tree.
type LintReport struct {
	Files    int           `json:"files"`
	Errors   []LintFinding `json:"errors"`
	Warnings []LintFinding `json:"warnings"`
}

// HasErrors reports whether the report contains a hard lint failure.
func (r LintReport) HasErrors() bool { return len(r.Errors) != 0 }

// LintDirectory validates every YAML, YML, or JSON fragment below the given
// directory. It is the shared engine used by both the gateway's merge path
// and the seam lint CLI's pre-merge gate. Configuration failures (for example,
// an unreadable schema) are returned as Go errors; fragment violations are
// returned in the report so one invocation can show all actionable failures.
func LintDirectory(options LintOptions) (LintReport, error) {
	if options.FragmentsDir == "" {
		options.FragmentsDir = defaultLintFragmentsDir
	}
	if options.SchemaPath == "" {
		options.SchemaPath = defaultLintSchemaPath
	}

	info, err := os.Stat(options.FragmentsDir)
	if err != nil {
		return LintReport{}, fmt.Errorf("fragments directory %q: %w", options.FragmentsDir, err)
	}
	if !info.IsDir() {
		return LintReport{}, fmt.Errorf("fragments path %q is not a directory", options.FragmentsDir)
	}

	schema, err := compileLintSchema(options.SchemaPath)
	if err != nil {
		return LintReport{}, err
	}

	allowlist, err := loadUpstreamAllowlist(options.UpstreamAllowlistPath)
	if err != nil {
		return LintReport{}, err
	}

	files, err := readLintFragments(options.FragmentsDir)
	if err != nil {
		return LintReport{}, err
	}
	return lintLoadedFragments(files, schema, allowlist), nil
}

// LintFiles validates an explicit list of fragment files. This is useful for
// CI invocations such as `seam lint routes/*/*.yaml`, where the shell expands
// the changed files before invoking the binary.
func LintFiles(paths []string, options LintOptions) (LintReport, error) {
	if len(paths) == 0 {
		return LintReport{}, fmt.Errorf("no fragment files supplied")
	}
	if options.SchemaPath == "" {
		options.SchemaPath = defaultLintSchemaPath
	}
	schema, err := compileLintSchema(options.SchemaPath)
	if err != nil {
		return LintReport{}, err
	}
	allowlist, err := loadUpstreamAllowlist(options.UpstreamAllowlistPath)
	if err != nil {
		return LintReport{}, err
	}

	cleanPaths := append([]string(nil), paths...)
	sort.Strings(cleanPaths)
	for _, path := range cleanPaths {
		info, err := os.Stat(path)
		if err != nil {
			return LintReport{}, fmt.Errorf("fragment file %q: %w", path, err)
		}
		if info.IsDir() {
			return LintReport{}, fmt.Errorf("fragment input %q is a directory; pass directories alone or supply files", path)
		}
	}
	files, err := readLintFragmentPaths(cleanPaths)
	if err != nil {
		return LintReport{}, err
	}
	return lintLoadedFragments(files, schema, allowlist), nil
}

// LintPaths is an alias for callers that use the command's file-or-directory
// terminology.
func LintPaths(paths []string, options LintOptions) (LintReport, error) {
	return LintFiles(paths, options)
}

func lintLoadedFragments(files []*lintFragment, schema *jsonschema.Schema, allowlist *hostAllowlist) LintReport {

	report := LintReport{
		Errors:   make([]LintFinding, 0),
		Warnings: make([]LintFinding, 0),
	}
	report.Files = len(files)
	for _, fragment := range files {
		fragment.validate(&report, schema, allowlist)
	}
	detectLintCollisions(&report, files)
	sortLintFindings(&report)
	return report
}

// LintFragments is kept as a descriptive alias for callers that prefer the
// term used by the CLI and design documents.
func LintFragments(options LintOptions) (LintReport, error) {
	return LintDirectory(options)
}

type lintFragment struct {
	file         string
	data         map[string]any
	schemaValid  bool
	hasHardError bool
}

func compileLintSchema(path string) (*jsonschema.Schema, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read fragment schema %q: %w", path, err)
	}

	var definition map[string]any
	if err := json.Unmarshal(contents, &definition); err != nil {
		return nil, fmt.Errorf("parse fragment schema %q: %w", path, err)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("seam-lint-schema.json", definition); err != nil {
		return nil, fmt.Errorf("load fragment schema %q: %w", path, err)
	}
	schema, err := compiler.Compile("seam-lint-schema.json")
	if err != nil {
		return nil, fmt.Errorf("compile fragment schema %q: %w", path, err)
	}
	return schema, nil
}

func readLintFragments(root string) ([]*lintFragment, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".yaml" || ext == ".yml" || ext == ".json" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk fragments directory %q: %w", root, err)
	}
	sort.Strings(paths)

	return readLintFragmentPaths(paths)
}

func readLintFragmentPaths(paths []string) ([]*lintFragment, error) {
	fragments := make([]*lintFragment, 0, len(paths))
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			fragments = append(fragments, &lintFragment{file: path})
			// The read error is reported by validate below using the same
			// deterministic per-file path as parse and schema failures.
			continue
		}

		var decoded any
		if err := yaml.Unmarshal(contents, &decoded); err != nil {
			fragments = append(fragments, &lintFragment{file: path})
			continue
		}
		normalized, err := normalizeLintValue(decoded)
		if err != nil {
			fragments = append(fragments, &lintFragment{file: path})
			continue
		}
		data, ok := normalized.(map[string]any)
		if !ok {
			fragments = append(fragments, &lintFragment{file: path})
			continue
		}
		fragments = append(fragments, &lintFragment{file: path, data: data})
	}
	return fragments, nil
}

// normalizeLintValue makes yaml.v3's map[any]any representation safe for the
// JSON Schema validator while retaining the natural JSON map representation.
func normalizeLintValue(value any) (any, error) {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			normalized, err := normalizeLintValue(child)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case map[any]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			stringKey, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("mapping key %v is not a string", key)
			}
			normalized, err := normalizeLintValue(child)
			if err != nil {
				return nil, err
			}
			result[stringKey] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(value))
		for i, child := range value {
			normalized, err := normalizeLintValue(child)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}

func (f *lintFragment) validate(report *LintReport, schema *jsonschema.Schema, allowlist *hostAllowlist) {
	if f.data == nil {
		f.addError(report, "fragment.parse", "file could not be parsed as a YAML/JSON object")
		return
	}

	if err := schema.Validate(f.data); err != nil {
		f.addError(report, "fragment.schema", fmt.Sprintf("fragment does not satisfy route-fragment-schema.json: %v", err))
	} else {
		f.schemaValid = true
	}

	f.checkOwnerChain(report)
	f.checkAPIVersion(report)
	f.checkReservedPaths(report)
	f.checkURLs(report, allowlist)
	f.checkTransport(report)
	f.checkUnscrubbable(report)
	f.checkPathRewrite(report)
	f.checkRouteGuards(report)
	f.checkBreakerDisagreements(report)
	f.checkDeprecation(report)
	f.checkAdapter(report)
}

func (f *lintFragment) addError(report *LintReport, code, message string) {
	f.hasHardError = true
	report.Errors = append(report.Errors, LintFinding{Severity: "error", Code: code, File: f.file, Message: message})
}

func (f *lintFragment) addWarning(report *LintReport, code, message string) {
	report.Warnings = append(report.Warnings, LintFinding{Severity: "warning", Code: code, File: f.file, Message: message})
}

func (f *lintFragment) checkOwnerChain(report *LintReport) {
	owner, ok := f.data["x-seam-owner"].(string)
	if !ok || owner == "" {
		return // The schema diagnostic gives the type/required-field detail.
	}

	directoryOwner := filepath.Base(filepath.Dir(f.file))
	if owner != directoryOwner {
		f.addError(report, "owner.directory-mismatch", fmt.Sprintf("x-seam-owner %q must match its parent directory %q (the basename is not authoritative)", owner, directoryOwner))
	}

	vaultPath, ok := f.data["x-vault-path"].(string)
	if !ok || vaultPath == "" {
		return
	}
	expectedPrefix := vaultRoutesPrefix + owner + "/"
	if !strings.HasPrefix(vaultPath, expectedPrefix) {
		f.addError(report, "owner.vault-path-mismatch", fmt.Sprintf("x-vault-path %q must be under %q for this owner", vaultPath, expectedPrefix))
	}
}

func (f *lintFragment) checkAPIVersion(report *LintReport) {
	value, present := f.data["x-api-version"]
	if present {
		version, ok := value.(string)
		if !ok || !apiVersionPattern.MatchString(version) {
			f.addError(report, "api-version.invalid", fmt.Sprintf("authored x-api-version %v must match ^v[1-9][0-9]*$; _unversioned is reserved for SEAM", value))
		}
	}

	// Contract versioning is fragment-scoped. The schema intentionally leaves
	// unknown OpenAPI extension keys permissive inside operations, so reject an
	// operation-level x-api-version here rather than silently ignoring it.
	paths, ok := f.data["paths"].(map[string]any)
	if !ok {
		return // Absence at the root is the authored form SEAM assigns _unversioned.
	}
	for path, pathValue := range paths {
		pathItem, ok := pathValue.(map[string]any)
		if !ok {
			continue
		}
		for method, operationValue := range pathItem {
			if !isLintOperationMethod(method) {
				continue
			}
			operation, ok := operationValue.(map[string]any)
			if ok {
				if _, present := operation["x-api-version"]; present {
					f.addError(report, "api-version.location", fmt.Sprintf("x-api-version on %s %s is not allowed; declare it at the fragment root", method, path))
				}
			}
		}
	}
}

func (f *lintFragment) checkReservedPaths(report *LintReport) {
	paths, ok := f.data["paths"].(map[string]any)
	if !ok {
		return
	}
	pathNames := make([]string, 0, len(paths))
	for path := range paths {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)
	for _, path := range pathNames {
		if _, reserved := reservedExactPaths[path]; reserved {
			f.addError(report, "path.reserved", fmt.Sprintf("fragment declares reserved control-plane path %q", path))
			continue
		}
		for _, prefix := range reservedPathPrefixes {
			if strings.HasPrefix(path, prefix) {
				f.addError(report, "path.reserved", fmt.Sprintf("fragment declares path %q in reserved control-plane namespace %q", path, prefix))
				break
			}
		}
	}
}

type lintURL struct {
	field string
	value string
}

func (f *lintFragment) checkURLs(report *LintReport, allowlist *hostAllowlist) {
	urls := make([]lintURL, 0, 1)
	if value, ok := f.data["x-upstream"].(string); ok {
		urls = append(urls, lintURL{field: "x-upstream", value: value})
	}
	if entries, ok := f.data["x-upstream-map"].(map[string]any); ok {
		keys := make([]string, 0, len(entries))
		for key := range entries {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			entry, ok := entries[key].(map[string]any)
			if !ok {
				continue
			}
			if value, ok := entry["url"].(string); ok {
				urls = append(urls, lintURL{field: fmt.Sprintf("x-upstream-map[%q].url", key), value: value})
			}
		}
	}

	for _, upstream := range urls {
		u, err := parseAbsoluteUpstreamURL(upstream.value)
		if err != nil {
			f.addError(report, "upstream.url-invalid", fmt.Sprintf("%s is not a well-formed absolute http(s) URL: %v", upstream.field, err))
			continue
		}
		if isIPLiteral(u.Hostname(), u.Host) {
			f.addError(report, "upstream.ip-literal", fmt.Sprintf("%s %q uses an IP-literal host; use an operator-allowlisted hostname", upstream.field, upstream.value))
			continue
		}
		if allowlist != nil && !allowlist.allowed(u.Hostname()) {
			f.addError(report, "upstream.host-not-allowed", fmt.Sprintf("%s host %q is not in the operator-owned upstream-host allowlist", upstream.field, u.Hostname()))
		}
	}
}

func parseAbsoluteUpstreamURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("scheme must be http or https")
	}
	if !u.IsAbs() || u.Opaque != "" || u.Host == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("URL must have a non-empty authority")
	}
	if strings.ContainsAny(u.Hostname(), "\r\n") {
		return nil, fmt.Errorf("host contains a control character")
	}
	// url.Parse accepts malformed ports in some Go versions until Port is
	// requested. Calling it here makes the check explicit and deterministic.
	if port := u.Port(); port != "" {
		for _, character := range port {
			if character < '0' || character > '9' {
				return nil, fmt.Errorf("port must be numeric")
			}
		}
	}
	return u, nil
}

func isIPLiteral(hostname, host string) bool {
	if strings.HasPrefix(host, "[") || net.ParseIP(hostname) != nil {
		return true
	}
	// net.ParseIP intentionally rejects some non-canonical dotted forms. A
	// dotted quad is still an IP literal even when written non-canonically.
	parts := strings.Split(hostname, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 3 {
			return false
		}
		value := 0
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
			value = value*10 + int(character-'0')
		}
		if value > 255 {
			return false
		}
	}
	return true
}

func (f *lintFragment) checkTransport(report *LintReport) {
	if tls, ok := f.data["x-upstream-tls"].(map[string]any); ok {
		if tls["insecureSkipVerify"] == "acknowledged" {
			f.addWarning(report, "transport.insecure-skip-verify", "x-upstream-tls.insecureSkipVerify: acknowledged requires human review")
		}
	}

	if f.data["x-upstream-plaintext"] == "acknowledged" {
		f.addWarning(report, "transport.plaintext", "x-upstream-plaintext: acknowledged requires human review")
	}

	if _, isMap := f.data["x-upstream-map"]; isMap {
		if _, present := f.data["x-upstream-plaintext"]; present {
			f.addError(report, "transport.map-plaintext", "x-upstream-plaintext is not valid alongside x-upstream-map; acknowledge plaintext per map entry")
		}
	}

	if upstream, ok := f.data["x-upstream"].(string); ok {
		if strings.HasPrefix(upstream, "http://") && f.data["x-upstream-plaintext"] != "acknowledged" {
			f.addError(report, "transport.plaintext-missing", "http:// x-upstream requires x-upstream-plaintext: acknowledged")
		}
	}

	entries, ok := f.data["x-upstream-map"].(map[string]any)
	if !ok {
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
			continue
		}
		upstream, _ := entry["url"].(string)
		if strings.HasPrefix(upstream, "http://") && entry["plaintext"] != "acknowledged" {
			f.addError(report, "transport.map-plaintext-missing", fmt.Sprintf("x-upstream-map[%q].url is http:// and requires plaintext: acknowledged on that entry", key))
		}
		if entryTLS, ok := entry["tls"].(map[string]any); ok && entryTLS["insecureSkipVerify"] == "acknowledged" {
			f.addWarning(report, "transport.insecure-skip-verify", fmt.Sprintf("x-upstream-map[%q].tls.insecureSkipVerify: acknowledged requires human review", key))
		}
		if entry["plaintext"] == "acknowledged" {
			f.addWarning(report, "transport.plaintext", fmt.Sprintf("x-upstream-map[%q].plaintext: acknowledged requires human review", key))
		}
	}

	// SEAM lint map-width warning for Phase 10.2 fan-out envelopes
	// Calculate envelope size and warn if it would exceed limits
	instanceCount := len(entries)
	if instanceCount > 0 {
		// Max envelope calculation:
		// maxFanoutEnvelopeBytes = min(maxBufferedResponseBytes × total_instances, 64 MiB)
		// Default maxBufferedResponseBytes = 1 MiB (1,048,576 bytes)
		const maxBufferedResponseBytes = 1 * 1024 * 1024 // 1 MiB
		const hardCap = 64 * 1024 * 1024                 // 64 MiB

		// Calculate the envelope size limit
		maxPerResponse := int64(maxBufferedResponseBytes) * int64(instanceCount)
		maxEnvelopeBytes := maxPerResponse
		if maxEnvelopeBytes > hardCap {
			maxEnvelopeBytes = hardCap
		}

		// Estimate per-instance overhead (JSON structure, status codes, etc.)
		// Rough estimate: ~200 bytes per instance for envelope metadata
		const perInstanceOverhead = 200
		estimatedEnvelopeSize := int64(perInstanceOverhead * instanceCount)

		// Warn if the estimated envelope size approaches the limit
		// Use 80% of the limit as the warning threshold
		warningThreshold := int64(float64(maxEnvelopeBytes) * 0.8)
		if estimatedEnvelopeSize > warningThreshold {
			f.addWarning(report, "fanout.map-width",
				fmt.Sprintf("x-upstream-map has %d instances; envelope size limit is %d bytes (min(%d × %d, %d MiB)). "+
					"Consider reducing instance count or increasing maxBufferedResponseBytes to avoid truncation. "+
					"Estimated envelope overhead: ~%d bytes (exceeds 80%% threshold)",
					instanceCount, maxEnvelopeBytes,
					maxBufferedResponseBytes, instanceCount, hardCap/(1024*1024),
					estimatedEnvelopeSize))
		}

		// Additional warning if instance count is very high (>100)
		if instanceCount > 100 {
			f.addWarning(report, "fanout.map-width",
				fmt.Sprintf("x-upstream-map has %d instances; large fan-outs (>100) may impact latency and timeout handling. "+
					"Consider implementing pagination or batch APIs instead of broadcasting to all instances",
					instanceCount))
		}
	}
}

func (f *lintFragment) checkUnscrubbable(report *LintReport) {
	if f.data["x-unscrubbable"] == "acknowledged" {
		f.addWarning(report, "scrubbing.unscrubbable", "x-unscrubbable: acknowledged requires human review")
	}
	paths, ok := f.data["paths"].(map[string]any)
	if !ok {
		return
	}
	for _, pathValue := range paths {
		pathItem, ok := pathValue.(map[string]any)
		if !ok {
			continue
		}
		for _, operationValue := range pathItem {
			operation, ok := operationValue.(map[string]any)
			if ok && operation["x-unscrubbable"] == "acknowledged" {
				f.addWarning(report, "scrubbing.unscrubbable", "operation x-unscrubbable: acknowledged requires human review")
			}
		}
	}
}

func (f *lintFragment) checkRouteGuards(report *LintReport) {
	for _, message := range quotaUnitMismatchMessages(f.data) {
		f.addError(report, "quota.unit-mismatch", message)
	}
	f.warnLongGuardWindow(report, f.data, "x-loop-guard", "fragment-root x-loop-guard.window")
	f.warnLongGuardWindow(report, f.data, "x-quota", "fragment-root x-quota.window")

	paths, ok := f.data["paths"].(map[string]any)
	if !ok {
		return
	}
	for path, pathValue := range paths {
		pathItem, ok := pathValue.(map[string]any)
		if !ok {
			continue
		}
		for method, operationValue := range pathItem {
			if !isLintOperationMethod(method) {
				continue
			}
			operation, ok := operationValue.(map[string]any)
			if !ok {
				continue
			}

			context := strings.ToUpper(method) + " " + path
			f.warnLongGuardWindow(report, operation, "x-loop-guard", context+" x-loop-guard.window")
			f.warnLongGuardWindow(report, operation, "x-quota", context+" x-quota.window")
		}
	}
}

// checkBreakerDisagreements validates that same-origin instances in x-upstream-map
// have identical breaker configuration. Per Phase 11.1, same-origin disagreement
// is a lint error that fails the PR.
func (f *lintFragment) checkBreakerDisagreements(report *LintReport) {
	// Get fragment-root breaker config
	var fragmentBreakerConfig map[string]any
	if breaker, ok := f.data["x-breaker"].(map[string]any); ok {
		fragmentBreakerConfig = breaker
	}

	// Check x-upstream-map entries
	upstreamMap, ok := f.data["x-upstream-map"].(map[string]any)
	if !ok {
		return // No upstream map, no disagreements possible
	}

	// Group instances by origin and collect breaker configs
	type originConfig struct {
		origin   string
		instance string
		config   map[string]any
		location string
	}

	originConfigs := make(map[string][]originConfig)

	for instanceKey, entryValue := range upstreamMap {
		// Skip reserved keys (_all, _default)
		if instanceKey == "_all" || instanceKey == "_default" {
			continue
		}

		entry, ok := entryValue.(map[string]any)
		if !ok {
			continue
		}

		// Extract URL
		urlStr, ok := entry["url"].(string)
		if !ok || urlStr == "" {
			continue
		}

		// Parse origin from URL
		u, err := url.Parse(urlStr)
		if err != nil {
			continue // Invalid URL will be caught by other checks
		}

		if u.Scheme == "" || u.Host == "" {
			continue
		}

		// Build origin: scheme://host:port
		origin := fmt.Sprintf("%s://%s", u.Scheme, u.Host)

		// Extract breaker config for this instance
		var breakerConfig map[string]any
		if breaker, ok := entry["breaker"].(map[string]any); ok {
			breakerConfig = breaker
		} else {
			// Use fragment-root config if no instance override
			breakerConfig = fragmentBreakerConfig
		}

		location := fmt.Sprintf("x-upstream-map[%q].breaker", instanceKey)
		originConfigs[origin] = append(originConfigs[origin], originConfig{
			origin:   origin,
			instance: instanceKey,
			config:   breakerConfig,
			location: location,
		})
	}

	// Check for disagreements within each origin
	for origin, configs := range originConfigs {
		if len(configs) < 2 {
			continue // No disagreement possible with single instance
		}

		// Compare all configs against the first one
		referenceConfig := configs[0].config

		for i := 1; i < len(configs); i++ {
			currentConfig := configs[i].config
			currentInstance := configs[i].instance

			if !breakerConfigsEqual(referenceConfig, currentConfig) {
				f.addError(report, "breaker.same-origin-disagreement",
					fmt.Sprintf("same-origin %q has conflicting breaker configs between %q and %s: "+
						"runtime uses the stricter (more likely to open) values, but this disagreement "+
						"must be resolved before merge. Configure identical x-breaker values for all instances "+
						"of the same origin, or omit per-instance overrides to use the fragment-root default.",
						origin, configs[0].instance, currentInstance))
				return // Only need one error per origin
			}
		}
	}
}

// breakerConfigsEqual compares two breaker configuration maps for equality.
// It compares the relevant fields: threshold, openSeconds, maxOpenSeconds, enabled.
func breakerConfigsEqual(a, b map[string]any) bool {
	// Both nil is equal
	if a == nil && b == nil {
		return true
	}

	// One nil, one not is not equal
	if (a == nil) != (b == nil) {
		return false
	}

	// Both non-nil, compare fields
	return breakerFieldEqual(a, b, "threshold") &&
		breakerFieldEqual(a, b, "openSeconds") &&
		breakerFieldEqual(a, b, "maxOpenSeconds") &&
		breakerFieldEqual(a, b, "enabled")
}

// breakerFieldEqual checks if two breaker config maps have equal values for a field.
func breakerFieldEqual(a, b map[string]any, field string) bool {
	aVal, aHas := a[field]
	bVal, bHas := b[field]

	// Both missing is equal (uses default)
	if !aHas && !bHas {
		return true
	}

	// One missing, one present is not equal
	if aHas != bHas {
		return false
	}

	// Both present, compare values
	return fmt.Sprintf("%v", aVal) == fmt.Sprintf("%v", bVal)
}

func guardUnit(container map[string]any, field string) (string, bool) {
	guard, ok := container[field].(map[string]any)
	if !ok {
		return "", false
	}
	unit, ok := guard["unit"].(string)
	return unit, ok
}

func quotaUnitMismatchMessages(fragment map[string]any) []string {
	var messages []string
	rootCostUnit, rootCostValid := guardUnit(fragment, "x-cost-per-call")
	rootQuotaUnit, rootQuotaValid := guardUnit(fragment, "x-quota")

	// Phase 13.2: x-quota without x-cost-per-call is an error
	_, hasCost := fragment["x-cost-per-call"]
	_, hasQuota := fragment["x-quota"]
	if hasQuota && !hasCost {
		messages = append(messages, "fragment-root x-quota is present but x-cost-per-call is missing; quota requires cost-per-call to define the unit and amount to deduct")
	}

	if rootCostValid && rootQuotaValid && rootCostUnit != rootQuotaUnit {
		messages = append(messages, fmt.Sprintf("fragment-root x-quota.unit %q must be byte-identical to x-cost-per-call.unit %q", rootQuotaUnit, rootCostUnit))
	}

	paths, ok := fragment["paths"].(map[string]any)
	if !ok {
		return messages
	}
	for path, pathValue := range paths {
		pathItem, ok := pathValue.(map[string]any)
		if !ok {
			continue
		}
		for method, operationValue := range pathItem {
			if !isLintOperationMethod(method) {
				continue
			}
			operation, ok := operationValue.(map[string]any)
			if !ok {
				continue
			}

			operationCostUnit, operationCostValid := guardUnit(operation, "x-cost-per-call")
			operationQuotaUnit, operationQuotaValid := guardUnit(operation, "x-quota")
			_, operationCostPresent := operation["x-cost-per-call"]
			_, operationQuotaPresent := operation["x-quota"]

			// Phase 13.2: operation-level x-quota without x-cost-per-call is an error
			if operationQuotaPresent && !operationCostPresent && !rootCostValid {
				messages = append(messages, fmt.Sprintf("operation-level x-quota on %s %s is present but x-cost-per-call is missing at both operation and fragment root; quota requires cost-per-call to define the unit and amount to deduct", strings.ToUpper(method), path))
			}

			switch {
			case operationQuotaPresent && operationQuotaValid && operationCostPresent && operationCostValid:
				messages = appendQuotaUnitMismatch(messages, path, method, operationQuotaUnit, operationCostUnit, "operation-level x-cost-per-call")
			case operationQuotaPresent && operationQuotaValid && !operationCostPresent && rootCostValid:
				messages = appendQuotaUnitMismatch(messages, path, method, operationQuotaUnit, rootCostUnit, "fragment-root x-cost-per-call default")
			case !operationQuotaPresent && rootQuotaValid && operationCostPresent && operationCostValid:
				messages = appendQuotaUnitMismatch(messages, path, method, rootQuotaUnit, operationCostUnit, "operation-level x-cost-per-call")
			}
		}
	}
	sort.Strings(messages)
	return messages
}

func appendQuotaUnitMismatch(messages []string, path, method, quotaUnit, costUnit, costSource string) []string {
	if quotaUnit == costUnit {
		return messages
	}
	return append(messages, fmt.Sprintf("x-quota.unit %q on %s %s must be byte-identical to %s unit %q", quotaUnit, strings.ToUpper(method), path, costSource, costUnit))
}

func (f *lintFragment) warnLongGuardWindow(report *LintReport, container map[string]any, field, location string) {
	guard, ok := container[field].(map[string]any)
	if !ok {
		return
	}
	window, ok := guard["window"].(string)
	if !ok || !guardWindowExceeds168Hours(window) {
		return
	}
	f.addWarning(report, "guard.window-over-168h", fmt.Sprintf("%s %q exceeds 168h; tumbling guard state resets on process restart", location, window))
}

func guardWindowExceeds168Hours(window string) bool {
	matches := guardWindowPattern.FindStringSubmatch(window)
	if matches == nil {
		return false
	}
	amount, ok := new(big.Int).SetString(matches[1], 10)
	if !ok {
		return false
	}
	secondsPerUnit := map[string]int64{
		"s": 1,
		"m": 60,
		"h": 60 * 60,
		"d": 24 * 60 * 60,
	}
	seconds := new(big.Int).Mul(amount, big.NewInt(secondsPerUnit[matches[2]]))
	return seconds.Cmp(big.NewInt(168*60*60)) > 0
}

type lintCollisionKey struct {
	path       string
	method     string
	apiVersion string
}

func detectLintCollisions(report *LintReport, fragments []*lintFragment) {
	claims := make(map[lintCollisionKey]string)
	for _, fragment := range fragments {
		if !fragment.schemaValid || fragment.hasHardError {
			continue
		}
		keys := fragmentCollisionKeysForLint(fragment)
		for _, key := range keys {
			if incumbent, exists := claims[key]; exists {
				report.Errors = append(report.Errors, LintFinding{
					Severity: "error",
					Code:     "path.collision",
					File:     fragment.file,
					Message:  fmt.Sprintf("(%s, %s, %s) collides with %s; deterministic merge keeps the earlier fragment", key.path, key.method, key.apiVersion, incumbent),
				})
				continue
			}
			claims[key] = fragment.file
		}
	}
}

func fragmentCollisionKeysForLint(fragment *lintFragment) []lintCollisionKey {
	paths, ok := fragment.data["paths"].(map[string]any)
	if !ok {
		return nil
	}
	apiVersion := "_unversioned"
	if value, ok := fragment.data["x-api-version"].(string); ok && value != "" {
		apiVersion = value
	}

	pathNames := make([]string, 0, len(paths))
	for path := range paths {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)
	var result []lintCollisionKey
	for _, path := range pathNames {
		pathItem, ok := paths[path].(map[string]any)
		if !ok {
			continue
		}
		methods := make([]string, 0, len(pathItem))
		for method := range pathItem {
			if isLintOperationMethod(method) {
				methods = append(methods, method)
			}
		}
		sort.Strings(methods)
		for _, method := range methods {
			result = append(result, lintCollisionKey{path: path, method: method, apiVersion: apiVersion})
		}
	}
	return result
}

func isLintOperationMethod(method string) bool {
	switch method {
	case "get", "put", "post", "delete", "options", "head", "patch", "trace":
		return true
	default:
		return false
	}
}

type hostAllowlist struct {
	entries []string
}

func loadUpstreamAllowlist(path string) (*hostAllowlist, error) {
	if path == "" {
		return nil, nil
	}
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// The operator-owned manifest is intentionally absent before Phase 6a.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read upstream allowlist %q: %w", path, err)
	}
	if strings.TrimSpace(string(contents)) == "" {
		return &hostAllowlist{}, nil
	}

	var decoded any
	if err := yaml.Unmarshal(contents, &decoded); err != nil {
		return nil, fmt.Errorf("parse upstream allowlist %q: %w", path, err)
	}
	normalized, err := normalizeLintValue(decoded)
	if err != nil {
		return nil, fmt.Errorf("parse upstream allowlist %q: %w", path, err)
	}
	values := collectAllowlistValues(normalized)
	if len(values) == 0 {
		return &hostAllowlist{}, nil
	}
	entries := make([]string, 0, len(values))
	for _, value := range values {
		entry, ok := normalizeHostEntry(value)
		if ok {
			entries = append(entries, entry)
		}
	}
	return &hostAllowlist{entries: entries}, nil
}

func collectAllowlistValues(value any) []string {
	switch value := value.(type) {
	case string:
		var values []string
		for _, line := range strings.Split(value, "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
			if line != "" && !strings.HasPrefix(line, "#") {
				values = append(values, line)
			}
		}
		return values
	case []any:
		var values []string
		for _, item := range value {
			values = append(values, collectAllowlistValues(item)...)
		}
		return values
	case map[string]any:
		var values []string
		for _, key := range []string{"hosts", "allowlist", "allowedHosts", "upstreamHosts", "suffixes", "hostSuffixes", "allowedSuffixes"} {
			if child, ok := value[key]; ok {
				values = append(values, collectAllowlistValues(child)...)
			}
		}
		if data, ok := value["data"].(map[string]any); ok {
			// Kubernetes ConfigMaps commonly store the actual allowlist under
			// a key such as hosts.yaml rather than a literal "hosts" key.
			for _, child := range data {
				values = append(values, collectAllowlistValues(child)...)
			}
		}
		if len(values) != 0 {
			return values
		}
		// Also accept a simple {hostname: true} form, which is convenient
		// for a small operator-owned ConfigMap.
		for key, child := range value {
			if enabled, ok := child.(bool); ok && enabled {
				values = append(values, key)
			}
		}
		return values
	default:
		return nil
	}
}

func normalizeHostEntry(value string) (string, bool) {
	value = strings.TrimSpace(strings.TrimSuffix(value, "."))
	if value == "" {
		return "", false
	}
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.Hostname() == "" {
			return "", false
		}
		value = u.Hostname()
	}
	return strings.ToLower(strings.TrimSuffix(value, ".")), true
}

func (a *hostAllowlist) allowed(host string) bool {
	if a == nil {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, entry := range a.entries {
		if strings.HasPrefix(entry, ".") {
			if strings.HasSuffix(host, entry) && host != strings.TrimPrefix(entry, ".") {
				return true
			}
			continue
		}
		if strings.HasPrefix(entry, "*.") {
			suffix := strings.TrimPrefix(entry, "*")
			if strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(entry, "*.") {
				return true
			}
			continue
		}
		if host == entry {
			return true
		}
	}
	return false
}

// checkAdapter validates x-adapter constraints per Phase 8.2:
// - targetVersion liveness (validator-side, after merge)
// - Transform vocabulary compliance
// - buffered-transform-on-unbufferable-route flag
func (f *lintFragment) checkAdapter(report *LintReport) {
	adapter, hasAdapter := f.data["x-adapter"].(map[string]any)
	if !hasAdapter {
		return
	}

	// Extract targetVersion
	targetVersion, hasTarget := adapter["targetVersion"].(string)
	if !hasTarget || targetVersion == "" {
		f.addError(report, "adapter.target-version-missing", "x-adapter.targetVersion is required and must be a non-empty string")
		return
	}

	// Validate targetVersion format (must match apiVersion pattern)
	if !apiVersionPattern.MatchString(targetVersion) {
		f.addError(report, "adapter.target-version-invalid", fmt.Sprintf("x-adapter.targetVersion %q must match ^v[1-9][0-9]*$", targetVersion))
	}

	// Check for mutually exclusive upstream-facing fields
	upstreamFields := []string{
		"x-upstream", "x-upstream-map", "x-instance-param",
		"x-upstream-strip-prefix", "x-upstream-tls", "x-upstream-plaintext",
		"x-vault-path", "x-inject-as", "x-credential-probe", "x-breaker",
	}

	for _, field := range upstreamFields {
		if _, exists := f.data[field]; exists {
			f.addError(report, "adapter.upstream-field-forbidden",
				fmt.Sprintf("x-adapter is mutually exclusive with %s (all upstream-facing fields are taken from targetVersion)", field))
		}
	}

	// Validate request transforms
	requestTransforms, hasRequest := adapter["request"].([]any)
	if !hasRequest {
		f.addError(report, "adapter.request-missing", "x-adapter.request array is required")
	} else {
		f.validateAdapterTransforms(report, requestTransforms, "request")
	}

	// Validate response transforms
	responseTransforms, hasResponse := adapter["response"].([]any)
	if !hasResponse {
		f.addError(report, "adapter.response-missing", "x-adapter.response array is required")
	} else {
		f.validateAdapterTransforms(report, responseTransforms, "response")
	}

	// Check for buffered transforms on streaming routes
	f.checkAdapterBuffering(report, adapter)
}

// validateAdapterTransforms validates each transform in the array
func (f *lintFragment) validateAdapterTransforms(report *LintReport, transforms []any, direction string) {
	for i, transform := range transforms {
		transformMap, ok := transform.(map[string]any)
		if !ok {
			f.addError(report, "adapter.transform-invalid",
				fmt.Sprintf("x-adapter.%s[%d] must be an object", direction, i))
			continue
		}

		// Check that exactly one transform operation is specified
		opCount := 0
		var opName string

		if _, hasRename := transformMap["rename"]; hasRename {
			opCount++
			opName = "rename"
		}
		if _, hasDefault := transformMap["default"]; hasDefault {
			opCount++
			opName = "default"
		}
		if _, hasDrop := transformMap["drop"]; hasDrop {
			opCount++
			opName = "drop"
		}
		if _, hasWrap := transformMap["wrap"]; hasWrap {
			opCount++
			opName = "wrap"
		}
		if _, hasUnwrap := transformMap["unwrap"]; hasUnwrap {
			opCount++
			opName = "unwrap"
		}
		if _, hasRenameParam := transformMap["renameParam"]; hasRenameParam {
			opCount++
			opName = "renameParam"
		}

		if opCount == 0 {
			f.addError(report, "adapter.transform-empty",
				fmt.Sprintf("x-adapter.%s[%d] must specify exactly one transform operation", direction, i))
			continue
		}
		if opCount > 1 {
			f.addError(report, "adapter.transform-multiple",
				fmt.Sprintf("x-adapter.%s[%d] specifies multiple operations; only one transform is allowed per item", direction, i))
			continue
		}

		// Validate specific transform types
		switch opName {
		case "rename":
			f.validateRenameTransform(report, transformMap, direction, i)
		case "default":
			f.validateDefaultTransform(report, transformMap, direction, i)
		case "drop":
			f.validateDropTransform(report, transformMap, direction, i)
		case "wrap":
			f.validateWrapTransform(report, transformMap, direction, i)
		case "unwrap":
			f.validateUnwrapTransform(report, transformMap, direction, i)
		case "renameParam":
			f.validateRenameParamTransform(report, transformMap, direction, i)
		}
	}
}

// validateRenameTransform validates a rename/move transform
func (f *lintFragment) validateRenameTransform(report *LintReport, transform map[string]any, direction string, index int) {
	rename, ok := transform["rename"].(map[string]any)
	if !ok {
		f.addError(report, "adapter.rename-shape",
			fmt.Sprintf("x-adapter.%s[%d].rename must be an object", direction, index))
		return
	}

	from, hasFrom := rename["from"].(string)
	to, hasTo := rename["to"].(string)

	if !hasFrom || from == "" {
		f.addError(report, "adapter.rename-from-missing",
			fmt.Sprintf("x-adapter.%s[%d].rename.from is required and must be a non-empty string", direction, index))
	}
	if !hasTo || to == "" {
		f.addError(report, "adapter.rename-to-missing",
			fmt.Sprintf("x-adapter.%s[%d].rename.to is required and must be a non-empty string", direction, index))
	}

	// Validate JSON Pointer format (basic check)
	if hasFrom && !strings.HasPrefix(from, "/") {
		f.addError(report, "adapter.rename-from-invalid",
			fmt.Sprintf("x-adapter.%s[%d].rename.from must be a JSON Pointer starting with '/'", direction, index))
	}
	if hasTo && !strings.HasPrefix(to, "/") {
		f.addError(report, "adapter.rename-to-invalid",
			fmt.Sprintf("x-adapter.%s[%d].rename.to must be a JSON Pointer starting with '/'", direction, index))
	}
}

// validateDefaultTransform validates a default value transform
func (f *lintFragment) validateDefaultTransform(report *LintReport, transform map[string]any, direction string, index int) {
	defaultOp, ok := transform["default"].(map[string]any)
	if !ok {
		f.addError(report, "adapter.default-shape",
			fmt.Sprintf("x-adapter.%s[%d].default must be an object", direction, index))
		return
	}

	pointer, hasPointer := defaultOp["pointer"].(string)
	_, hasValue := defaultOp["value"]

	if !hasPointer || pointer == "" {
		f.addError(report, "adapter.default-pointer-missing",
			fmt.Sprintf("x-adapter.%s[%d].default.pointer is required and must be a non-empty string", direction, index))
	}
	if !hasValue {
		f.addError(report, "adapter.default-value-missing",
			fmt.Sprintf("x-adapter.%s[%d].default.value is required", direction, index))
	}

	// Validate JSON Pointer format
	if hasPointer && !strings.HasPrefix(pointer, "/") {
		f.addError(report, "adapter.default-pointer-invalid",
			fmt.Sprintf("x-adapter.%s[%d].default.pointer must be a JSON Pointer starting with '/'", direction, index))
	}
}

// validateDropTransform validates a drop field transform
func (f *lintFragment) validateDropTransform(report *LintReport, transform map[string]any, direction string, index int) {
	drop, ok := transform["drop"].(string)
	if !ok || drop == "" {
		f.addError(report, "adapter.drop-invalid",
			fmt.Sprintf("x-adapter.%s[%d].drop must be a non-empty string (JSON Pointer)", direction, index))
		return
	}

	// Validate JSON Pointer format
	if !strings.HasPrefix(drop, "/") {
		f.addError(report, "adapter.drop-invalid",
			fmt.Sprintf("x-adapter.%s[%d].drop must be a JSON Pointer starting with '/'", direction, index))
	}
}

// validateWrapTransform validates a wrap envelope transform
func (f *lintFragment) validateWrapTransform(report *LintReport, transform map[string]any, direction string, index int) {
	wrap, ok := transform["wrap"].(map[string]any)
	if !ok {
		f.addError(report, "adapter.wrap-shape",
			fmt.Sprintf("x-adapter.%s[%d].wrap must be an object", direction, index))
		return
	}

	pointer, hasPointer := wrap["pointer"].(string)
	envelope, hasEnvelope := wrap["envelope"].(string)

	if !hasPointer || pointer == "" {
		f.addError(report, "adapter.wrap-pointer-missing",
			fmt.Sprintf("x-adapter.%s[%d].wrap.pointer is required and must be a non-empty string", direction, index))
	}
	if !hasEnvelope || envelope == "" {
		f.addError(report, "adapter.wrap-envelope-missing",
			fmt.Sprintf("x-adapter.%s[%d].wrap.envelope is required and must be a non-empty string", direction, index))
	}

	// Validate JSON Pointer format
	if hasPointer && !strings.HasPrefix(pointer, "/") {
		f.addError(report, "adapter.wrap-pointer-invalid",
			fmt.Sprintf("x-adapter.%s[%d].wrap.pointer must be a JSON Pointer starting with '/'", direction, index))
	}
}

// validateUnwrapTransform validates an unwrap envelope transform
func (f *lintFragment) validateUnwrapTransform(report *LintReport, transform map[string]any, direction string, index int) {
	unwrap, ok := transform["unwrap"].(map[string]any)
	if !ok {
		f.addError(report, "adapter.unwrap-shape",
			fmt.Sprintf("x-adapter.%s[%d].unwrap must be an object", direction, index))
		return
	}

	pointer, hasPointer := unwrap["pointer"].(string)
	envelope, hasEnvelope := unwrap["envelope"].(string)

	if !hasPointer || pointer == "" {
		f.addError(report, "adapter.unwrap-pointer-missing",
			fmt.Sprintf("x-adapter.%s[%d].unwrap.pointer is required and must be a non-empty string", direction, index))
	}
	if !hasEnvelope || envelope == "" {
		f.addError(report, "adapter.unwrap-envelope-missing",
			fmt.Sprintf("x-adapter.%s[%d].unwrap.envelope is required and must be a non-empty string", direction, index))
	}

	// Validate JSON Pointer format
	if hasPointer && !strings.HasPrefix(pointer, "/") {
		f.addError(report, "adapter.unwrap-pointer-invalid",
			fmt.Sprintf("x-adapter.%s[%d].unwrap.pointer must be a JSON Pointer starting with '/'", direction, index))
	}
}

// validateRenameParamTransform validates a rename parameter transform
func (f *lintFragment) validateRenameParamTransform(report *LintReport, transform map[string]any, direction string, index int) {
	// renameParam is only valid for request transforms
	if direction != "request" {
		f.addError(report, "adapter.rename-param-response",
			fmt.Sprintf("x-adapter.%s[%d].renameParam is only valid for request transforms, not response", direction, index))
		return
	}

	renameParam, ok := transform["renameParam"].(map[string]any)
	if !ok {
		f.addError(report, "adapter.rename-param-shape",
			fmt.Sprintf("x-adapter.%s[%d].renameParam must be an object", direction, index))
		return
	}

	from, hasFrom := renameParam["from"].(string)
	to, hasTo := renameParam["to"].(string)
	location, hasLocation := renameParam["location"].(string)

	if !hasFrom || from == "" {
		f.addError(report, "adapter.rename-param-from-missing",
			fmt.Sprintf("x-adapter.%s[%d].renameParam.from is required and must be a non-empty string", direction, index))
	}
	if !hasTo || to == "" {
		f.addError(report, "adapter.rename-param-to-missing",
			fmt.Sprintf("x-adapter.%s[%d].renameParam.to is required and must be a non-empty string", direction, index))
	}
	if !hasLocation || location == "" {
		f.addError(report, "adapter.rename-param-location-missing",
			fmt.Sprintf("x-adapter.%s[%d].renameParam.location is required and must be 'query' or 'header'", direction, index))
		return
	}

	if location != "query" && location != "header" {
		f.addError(report, "adapter.rename-param-location-invalid",
			fmt.Sprintf("x-adapter.%s[%d].renameParam.location must be 'query' or 'header', got %q", direction, index, location))
	}
}

// checkAdapterBuffering validates that response transforms requiring buffering
// are not used on unbufferable (streaming) routes
func (f *lintFragment) checkAdapterBuffering(report *LintReport, adapter map[string]any) {
	responseTransforms, hasResponse := adapter["response"].([]any)
	if !hasResponse {
		return
	}

	// Check if this route is unbufferable (streaming)
	// For now, we assume all routes are bufferable unless explicitly marked
	// In a full implementation, this would check the route's content-type
	// and response handling configuration

	for i, transform := range responseTransforms {
		transformMap, ok := transform.(map[string]any)
		if !ok {
			continue
		}

		// Check for structure-altering transforms that require buffering
		requiresBuffering := false
		transformType := ""

		if _, hasWrap := transformMap["wrap"]; hasWrap {
			requiresBuffering = true
			transformType = "wrap"
		} else if _, hasUnwrap := transformMap["unwrap"]; hasUnwrap {
			// unwrap doesn't inherently require buffering
			// but may if the response structure changes significantly
		}

		if requiresBuffering {
			// TODO: In full implementation, check if route is streaming
			// For now, emit a warning that buffering will be forced
			f.addWarning(report, "adapter.buffering-required",
				fmt.Sprintf("x-adapter.response[%d] uses %s transform which requires buffered response path", i, transformType))
		}
	}
}

// checkDeprecation validates x-seam-deprecated structure
// Per Phase 8.3: ordered, non-overlapping brownout windows inside [since, sunset]
func (f *lintFragment) checkDeprecation(report *LintReport) {
	deprecated, hasDeprecated := f.data["x-seam-deprecated"]
	if !hasDeprecated {
		return
	}

	// A bare true is a lint error - must be an object
	if _, isBool := deprecated.(bool); isBool {
		f.addError(report, "deprecation.bare-true", "x-seam-deprecated must be an object with 'since' field; bare true is not allowed")
		return
	}

	deprecatedMap, ok := deprecated.(map[string]any)
	if !ok {
		// Schema should catch this, but validate anyway
		f.addError(report, "deprecation.invalid-type", "x-seam-deprecated must be an object")
		return
	}

	// Validate required 'since' field (ISO date)
	since, hasSince := deprecatedMap["since"].(string)
	if !hasSince || since == "" {
		f.addError(report, "deprecation.since-missing", "x-seam-deprecated.since is required and must be an ISO date string")
		return
	}

	if !isValidISODate(since) {
		f.addError(report, "deprecation.since-invalid", fmt.Sprintf("x-seam-deprecated.since must be a valid ISO date (YYYY-MM-DD), got %q", since))
		return
	}

	// Validate optional 'sunset' field
	sunset, hasSunset := deprecatedMap["sunset"].(string)
	if hasSunset && sunset != "" {
		if !isValidISODate(sunset) {
			f.addError(report, "deprecation.sunset-invalid", fmt.Sprintf("x-seam-deprecated.sunset must be a valid ISO date (YYYY-MM-DD), got %q", sunset))
			return
		}

		// Sunset must be after since
		if !isDateAfter(sunset, since) {
			f.addError(report, "deprecation.sunset-before-since", fmt.Sprintf("x-seam-deprecated.sunset %q must be after since %q", sunset, since))
		}
	}

	// Validate optional 'brownout' array
	brownout, hasBrownout := deprecatedMap["brownout"]
	if !hasBrownout {
		return
	}

	// Handle both []any and []map[string]any - YAML parsing can produce either
	var brownoutArray []any
	if arrayAny, ok := brownout.([]any); ok {
		brownoutArray = arrayAny
	} else if arrayMap, ok := brownout.([]map[string]any); ok {
		// Convert []map[string]any to []any for consistent processing
		brownoutArray = make([]any, len(arrayMap))
		for i, v := range arrayMap {
			brownoutArray[i] = v
		}
	} else {
		f.addError(report, "deprecation.brownout-invalid", "x-seam-deprecated.brownout must be an array")
		return
	}

	if len(brownoutArray) == 0 {
		f.addError(report, "deprecation.brownout-empty", "x-seam-deprecated.brownout array must not be empty")
		return
	}

	// Brownout requires sunset
	if !hasSunset || sunset == "" {
		f.addError(report, "deprecation.brownout-without-sunset", "x-seam-deprecated.brownout requires x-seam-deprecated.sunset to be set")
		return
	}

	// Validate each brownout window and check ordering/non-overlap
	var lastEnd string
	for i, window := range brownoutArray {
		windowMap, ok := window.(map[string]any)
		if !ok {
			f.addError(report, "deprecation.brownout-window-invalid", fmt.Sprintf("x-seam-deprecated.brownout[%d] must be an object", i))
			continue
		}

		start, hasStart := windowMap["start"].(string)
		end, hasEnd := windowMap["end"].(string)

		if !hasStart || start == "" {
			f.addError(report, "deprecation.brownout-start-missing", fmt.Sprintf("x-seam-deprecated.brownout[%d].start is required and must be an ISO date-time string", i))
			continue
		}
		if !hasEnd || end == "" {
			f.addError(report, "deprecation.brownout-end-missing", fmt.Sprintf("x-seam-deprecated.brownout[%d].end is required and must be an ISO date-time string", i))
			continue
		}

		if !isValidISODateTime(start) {
			f.addError(report, "deprecation.brownout-start-invalid", fmt.Sprintf("x-seam-deprecated.brownout[%d].start must be a valid ISO date-time (RFC 3339), got %q", i, start))
			continue
		}
		if !isValidISODateTime(end) {
			f.addError(report, "deprecation.brownout-end-invalid", fmt.Sprintf("x-seam-deprecated.brownout[%d].end must be a valid ISO date-time (RFC 3339), got %q", i, end))
			continue
		}

		// End must be after start
		if !isDateTimeAfter(end, start) {
			f.addError(report, "deprecation.brownout-end-before-start", fmt.Sprintf("x-seam-deprecated.brownout[%d].end %q must be after start %q", i, end, start))
			continue
		}

		// Window must be within [since, sunset]
		if !isDateTimeWithinRange(start, since, sunset) {
			f.addError(report, "deprecation.brownout-out-of-range", fmt.Sprintf("x-seam-deprecated.brownout[%d].start %q is outside the deprecation range [%s, %s]", i, start, since, sunset))
			continue
		}
		if !isDateTimeWithinRange(end, since, sunset) {
			f.addError(report, "deprecation.brownout-out-of-range", fmt.Sprintf("x-seam-deprecated.brownout[%d].end %q is outside the deprecation range [%s, %s]", i, end, since, sunset))
			continue
		}

		// Check for ordering (no overlapping, sequential windows)
		if lastEnd != "" && !isDateTimeAfterOrEqual(start, lastEnd) {
			f.addError(report, "deprecation.brownout-overlapping", fmt.Sprintf("x-seam-deprecated.brownout[%d].start %q must be after or equal to previous window's end %q (windows must be ordered and non-overlapping)", i, start, lastEnd))
			continue
		}

		lastEnd = end
	}
}

// isValidISODate checks if a string is a valid ISO date (YYYY-MM-DD)
func isValidISODate(date string) bool {
	// Basic format check: YYYY-MM-DD
	if len(date) != 10 {
		return false
	}
	if date[4] != '-' || date[7] != '-' {
		return false
	}
	for i := 0; i < 10; i++ {
		if i == 4 || i == 7 {
			continue
		}
		if date[i] < '0' || date[i] > '9' {
			return false
		}
	}

	// Parse year, month, day
	year := (int(date[0]-'0')*1000 + int(date[1]-'0')*100 + int(date[2]-'0')*10 + int(date[3]-'0'))
	month := (int(date[5]-'0')*10 + int(date[6]-'0'))
	day := (int(date[8]-'0')*10 + int(date[9]-'0'))

	// Validate ranges
	if month < 1 || month > 12 {
		return false
	}
	if day < 1 || day > 31 {
		return false
	}

	// Validate day based on month
	daysInMonth := []int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	// Handle leap years for February
	if month == 2 && ((year%400 == 0) || (year%100 != 0 && year%4 == 0)) {
		daysInMonth[1] = 29
	}
	if day > daysInMonth[month-1] {
		return false
	}

	return true
}

// isValidISODateTime checks if a string is a valid ISO date-time (RFC 3339)
func isValidISODateTime(datetime string) bool {
	// RFC 3339 allows both date-time and full date-time with timezone
	// Basic check: YYYY-MM-DDTHH:MM:SSZ or YYYY-MM-DDTHH:MM:SS+HH:MM
	if len(datetime) < 20 {
		return false
	}
	// Check date part
	if !isValidISODate(datetime[:10]) {
		return false
	}
	// Check T separator
	if datetime[10] != 'T' && datetime[10] != 't' && datetime[10] != ' ' {
		return false
	}

	// Minimum time part: HH:MM:SSZ (8 chars)
	timePart := datetime[11:]
	if len(timePart) < 8 {
		return false
	}

	// Check time separators
	if timePart[2] != ':' || timePart[5] != ':' {
		return false
	}

	// Validate hours (HH must be 00-23)
	hours := (int(timePart[0]-'0')*10 + int(timePart[1]-'0'))
	if hours > 23 {
		return false
	}

	// Validate minutes (MM must be 00-59)
	minutes := (int(timePart[3]-'0')*10 + int(timePart[4]-'0'))
	if minutes > 59 {
		return false
	}

	// Validate seconds (SS must be 00-59)
	seconds := (int(timePart[6]-'0')*10 + int(timePart[7]-'0'))
	if seconds > 59 {
		return false
	}

	// Check timezone suffix if present (Z, +HH:MM, -HH:MM)
	if len(timePart) > 8 {
		suffix := timePart[8:]
		if suffix == "Z" || suffix == "z" {
			return true
		}
		// Handle ±HH:MM format
		if len(suffix) == 6 && (suffix[0] == '+' || suffix[0] == '-') {
			if suffix[3] != ':' {
				return false
			}
			// Validate offset hours and minutes
			offsetHours := (int(suffix[1]-'0')*10 + int(suffix[2]-'0'))
			offsetMinutes := (int(suffix[4]-'0')*10 + int(suffix[5]-'0'))
			if offsetHours > 23 || offsetMinutes > 59 {
				return false
			}
			return true
		}
		return false
	}

	return true
}

// isDateAfter checks if date1 is after date2 (both ISO dates YYYY-MM-DD)
func isDateAfter(date1, date2 string) bool {
	return date1 > date2
}

// isDateTimeAfter checks if datetime1 is after datetime2 (both RFC 3339)
func isDateTimeAfter(datetime1, datetime2 string) bool {
	return datetime1 > datetime2
}

// isDateTimeAfterOrEqual checks if datetime1 is after or equal to datetime2
func isDateTimeAfterOrEqual(datetime1, datetime2 string) bool {
	return datetime1 >= datetime2
}

// isDateTimeWithinRange checks if a datetime is within [sinceDate, sunsetDate]
// Assumes since and sunset are ISO dates (YYYY-MM-DD) and datetime is RFC 3339
func isDateTimeWithinRange(datetime, sinceDate, sunsetDate string) bool {
	// Extract date part from datetime (first 10 characters)
	if len(datetime) < 10 {
		return false
	}
	dateOnly := datetime[:10]

	// Must be >= since and <= sunset
	return dateOnly >= sinceDate && dateOnly <= sunsetDate
}

func sortLintFindings(report *LintReport) {
	less := func(left, right LintFinding) bool {
		if left.File != right.File {
			return left.File < right.File
		}
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		return left.Message < right.Message
	}
	sort.Slice(report.Errors, func(i, j int) bool { return less(report.Errors[i], report.Errors[j]) })
	sort.Slice(report.Warnings, func(i, j int) bool { return less(report.Warnings[i], report.Warnings[j]) })
}
