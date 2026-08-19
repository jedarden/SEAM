package spec

import (
	"encoding/json"
	"fmt"
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
	apiVersionPattern = regexp.MustCompile(`^v[1-9][0-9]*$`)

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
