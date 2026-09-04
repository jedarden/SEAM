package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// importCommand is the CLI wrapper for the import command
func importCommand(args []string) {
	os.Exit(runImportCommand(args, os.Stdout, os.Stderr))
}

// runImportCommand implements the import command
// Usage: seam import --from-url <url> [--owner <name>] [--output <file>] [--paths <paths>] [--methods <methods>]
//
// Flags:
//
//	--from-url, -u: URL of the OpenAPI spec to import (required)
//	--owner, -o: Owner/service name for the fragment (default: derived from URL)
//	--output, -f: Output fragment file path (default: <owner>/fragment.yaml)
//	--paths, -p: Comma-separated list of paths to import (default: all paths)
//	--methods, -m: Comma-separated list of HTTP methods to import (default: all methods)
//	--filter-prefix: Only import paths with this prefix
//	--strip-prefix: Strip this prefix from imported paths
//	--add-prefix: Add this prefix to all imported paths
//	--timeout: HTTP timeout for fetching the spec (default: 30s)
//
// The generated fragment is a curatable bootstrap, not a served fragment:
// it carries x-seam-schema/x-seam-owner/x-upstream and the imported paths,
// and leaves the credential/scope decisions (x-vault-path, x-inject-as,
// x-required-scope, ...) to the curator. x-api-version is deliberately
// absent — authored values must be v1, v2, ...; _unversioned is assigned by
// SEAM at merge time and rejected by lint as an authored value.
// Swagger 2.0 sources are accepted; their top-level definitions/parameters
// are carried across as components.schemas/components.parameters so $ref
// pointers in the imported operations keep resolving.
func runImportCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	fs.SetOutput(stderr)

	var fromURL string
	var owner string
	var outputFile string
	var pathsFilter string
	var methodsFilter string
	var filterPrefix string
	var stripPrefix string
	var addPrefix string
	var timeout time.Duration

	fs.StringVar(&fromURL, "from-url", fromURL, "URL of the OpenAPI spec to import (required)")
	fs.StringVar(&fromURL, "u", fromURL, "Alias for --from-url")
	fs.StringVar(&owner, "owner", owner, "Owner/service name for the fragment")
	fs.StringVar(&owner, "o", owner, "Alias for --owner")
	fs.StringVar(&outputFile, "output", outputFile, "Output fragment file path")
	fs.StringVar(&outputFile, "f", outputFile, "Alias for --output")
	fs.StringVar(&pathsFilter, "paths", pathsFilter, "Comma-separated list of paths to import")
	fs.StringVar(&pathsFilter, "p", pathsFilter, "Alias for --paths")
	fs.StringVar(&methodsFilter, "methods", methodsFilter, "Comma-separated list of HTTP methods to import")
	fs.StringVar(&methodsFilter, "m", methodsFilter, "Alias for --methods")
	fs.StringVar(&filterPrefix, "filter-prefix", filterPrefix, "Only import paths with this prefix")
	fs.StringVar(&stripPrefix, "strip-prefix", stripPrefix, "Strip this prefix from imported paths")
	fs.StringVar(&addPrefix, "add-prefix", addPrefix, "Add this prefix to all imported paths")
	fs.DurationVar(&timeout, "timeout", 30*time.Second, "HTTP timeout for fetching the spec")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if fromURL == "" {
		fmt.Fprintf(stderr, "seam import: --from-url is required\n")
		fmt.Fprintf(stderr, "Usage: seam import --from-url <url> [options]\n")
		return 2
	}

	// Validate URL
	parsedURL, err := url.Parse(fromURL)
	if err != nil {
		fmt.Fprintf(stderr, "seam import: invalid URL: %v\n", err)
		return 2
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		fmt.Fprintf(stderr, "seam import: URL must use http or https scheme\n")
		return 2
	}

	// Derive owner from URL if not specified
	if owner == "" {
		owner = deriveOwnerFromURL(parsedURL)
	}

	// Derive output file if not specified
	if outputFile == "" {
		outputFile = filepath.Join(owner, "fragment.yaml")
	}

	// Parse filters
	pathsToImport := parseCommaSeparatedList(pathsFilter)
	methodsToImport := parseCommaSeparatedList(methodsFilter)
	methodsToImport = normalizeHTTPMethods(methodsToImport)

	// Fetch the OpenAPI spec
	fmt.Fprintf(stderr, "seam import: fetching spec from %s\n", fromURL)
	spec, err := fetchOpenAPISpec(fromURL, timeout)
	if err != nil {
		fmt.Fprintf(stderr, "seam import: failed to fetch spec: %v\n", err)
		return 2
	}

	// Parse the spec as raw JSON/YAML
	var parsedSpec map[string]interface{}
	if err := json.Unmarshal(spec, &parsedSpec); err != nil {
		// Try YAML
		if err := yaml.Unmarshal(spec, &parsedSpec); err != nil {
			fmt.Fprintf(stderr, "seam import: failed to parse spec as JSON or YAML: %v\n", err)
			return 2
		}
	}

	// Extract and filter paths
	filteredPaths, pathCount := filterPaths(parsedSpec, pathsToImport, methodsToImport, filterPrefix)

	if pathCount == 0 {
		fmt.Fprintf(stderr, "seam import: no paths matched the filter criteria\n")
		return 1
	}

	// Transform paths (strip/add prefixes)
	transformedPaths := transformPaths(filteredPaths, stripPrefix, addPrefix)

	// The fragment forwards to the service the spec describes, not to the
	// spec document itself, so x-upstream is derived as scheme://host[:port].
	upstream := deriveBaseUpstreamURL(parsedURL)
	if strings.HasPrefix(upstream, "http://") {
		// route-fragment-schema requires the plaintext acknowledgement on any
		// http:// upstream. Emit it (with a loud note) so the bootstrap is
		// lintable; removing it or switching to https is the curator's call.
		fmt.Fprintf(stderr, "seam import: upstream %s is http://; emitted x-upstream-plaintext: acknowledged — switch x-upstream to https or remove the marker if this hop is not plaintext\n", upstream)
	}

	// Build the fragment
	fragment := buildFragment(owner, transformedPaths, upstream, fromURL, parsedSpec)

	// Ensure output directory exists
	outputDir := filepath.Dir(outputFile)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Fprintf(stderr, "seam import: failed to create output directory: %v\n", err)
		return 2
	}

	// Write the fragment
	if err := writeFragment(outputFile, fragment); err != nil {
		fmt.Fprintf(stderr, "seam import: failed to write fragment: %v\n", err)
		return 2
	}

	fmt.Fprintf(stderr, "seam import: successfully imported %d paths to %s\n", pathCount, outputFile)
	fmt.Fprintf(stdout, "# Fragment successfully imported from %s\n", fromURL)
	fmt.Fprintf(stdout, "# Owner: %s\n", owner)
	fmt.Fprintf(stdout, "# Paths imported: %d\n", pathCount)
	fmt.Fprintf(stdout, "# Upstream (derived from the spec URL): %s\n", upstream)
	fmt.Fprintf(stdout, "#\n")
	fmt.Fprintf(stdout, "# IMPORTANT: Add the following fields manually:\n")
	fmt.Fprintf(stdout, "#   - x-vault-path: <base>/%s/<secret-key> (<base> is deployment config —\n", owner)
	fmt.Fprintf(stdout, "#     SEAM_VAULT_BASE_DIR; the schema only pins that %s is an interior\n", owner)
	fmt.Fprintf(stdout, "#     segment with the secret name below it)\n")
	fmt.Fprintf(stdout, "#   - x-inject-as: {kind: header|bearer|query, name: <header-name>} (bearer takes no name)\n")
	fmt.Fprintf(stdout, "#   - x-required-scope: <service>:<action> (if scope-based access control is needed)\n")
	fmt.Fprintf(stdout, "#   - x-upstream-tls: (if upstream uses a custom CA or needs insecure skip)\n")
	fmt.Fprintf(stdout, "#   - x-cache-ttl: (if response caching is desired)\n")
	fmt.Fprintf(stdout, "#   - x-upstream-strip-prefix: (if the upstream expects paths without a prefix)\n")
	fmt.Fprintf(stdout, "#\n")
	fmt.Fprintf(stdout, "# x-api-version is intentionally absent: authored values must be v1, v2, ...;\n")
	fmt.Fprintf(stdout, "# _unversioned is assigned by SEAM at merge time and rejected by lint.\n")
	fmt.Fprintf(stdout, "# Keep this file in a directory named %s/ — seam lint checks x-seam-owner\n", owner)
	fmt.Fprintf(stdout, "# against the parent directory name.\n")
	fmt.Fprintf(stdout, "#\n")
	fmt.Fprintf(stdout, "# Run 'seam lint %s' to validate the fragment.\n", outputFile)

	return 0
}

// deriveBaseUpstreamURL reduces a spec URL to scheme://host[:port]. A
// fragment's x-upstream is the base the imported routes forward to; the path
// that located the spec document (for example /swagger.json) is not part of it.
func deriveBaseUpstreamURL(u *url.URL) string {
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// sourceComponents carries the source spec's reusable definitions into the
// fragment so $ref pointers inside imported operations keep resolving.
// OpenAPI 3 sources pass components through unchanged; Swagger 2.0 sources
// map their top-level definitions/parameters into the components shape.
func sourceComponents(parsedSpec map[string]interface{}) map[string]interface{} {
	if components, ok := parsedSpec["components"].(map[string]interface{}); ok && len(components) > 0 {
		return components
	}

	result := make(map[string]interface{})
	if definitions, ok := parsedSpec["definitions"].(map[string]interface{}); ok && len(definitions) > 0 {
		result["schemas"] = definitions
	}
	if parameters, ok := parsedSpec["parameters"].(map[string]interface{}); ok && len(parameters) > 0 {
		result["parameters"] = parameters
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// deriveOwnerFromURL derives a service owner name from the URL
func deriveOwnerFromURL(u *url.URL) string {
	host := u.Host

	// Remove port if present
	if i := strings.Index(host, ":"); i != -1 {
		host = host[:i]
	}

	// Remove TLD
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		parts = parts[:len(parts)-1] // Remove TLD
	}
	host = strings.Join(parts, ".")

	// Convert to valid owner name (lowercase, alphanumeric and hyphens)
	owner := strings.ToLower(host)
	owner = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, owner)

	// Remove leading/trailing hyphens
	owner = strings.Trim(owner, "-")

	if owner == "" {
		owner = "imported-service"
	}

	return owner
}

// parseCommaSeparatedList parses a comma-separated string into a slice
func parseCommaSeparatedList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

// normalizeHTTPMethods normalizes HTTP method strings to uppercase
func normalizeHTTPMethods(methods []string) []string {
	if methods == nil {
		return nil
	}
	result := make([]string, len(methods))
	for i, m := range methods {
		result[i] = strings.ToUpper(strings.TrimSpace(m))
	}
	return result
}

// fetchOpenAPISpec fetches an OpenAPI spec from a URL
func fetchOpenAPISpec(u string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	spec, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return spec, nil
}

// filterPaths filters OpenAPI paths based on criteria
func filterPaths(paths interface{}, pathsToInclude, methodsToInclude []string, prefixFilter string) (map[string]map[string]interface{}, int) {
	// Parse the spec as raw JSON and filter manually
	result := make(map[string]map[string]interface{})
	count := 0

	// Build set of paths to include if specified
	pathsSet := make(map[string]bool)
	if len(pathsToInclude) > 0 {
		for _, p := range pathsToInclude {
			pathsSet[p] = true
		}
	}

	// Build set of methods to include if specified
	methodsSet := make(map[string]bool)
	if len(methodsToInclude) > 0 {
		for _, m := range methodsToInclude {
			methodsSet[m] = true
		}
	}

	// Try to extract paths from the raw spec
	pathsMap, ok := paths.(map[string]interface{})
	if !ok {
		return result, 0
	}

	// Extract the "paths" object
	pathsObj, ok := pathsMap["paths"].(map[string]interface{})
	if !ok {
		return result, 0
	}

	// Iterate through paths
	for path, pathItem := range pathsObj {
		// Skip if not in paths set (when set is specified)
		if len(pathsSet) > 0 && !pathsSet[path] {
			continue
		}

		// Skip if doesn't match prefix filter
		if prefixFilter != "" && !strings.HasPrefix(path, prefixFilter) {
			continue
		}

		// Extract operations from path item
		pathItemMap, ok := pathItem.(map[string]interface{})
		if !ok {
			continue
		}

		// Extract operations that match the methods filter
		operations := make(map[string]interface{})
		hasMatchingOperation := false

		// Check each HTTP method
		for _, method := range []string{"get", "put", "post", "delete", "patch", "head", "options", "trace"} {
			op, exists := pathItemMap[method]
			if !exists {
				continue
			}

			// Skip if method filter is specified and this method is not in it
			if len(methodsSet) > 0 && !methodsSet[strings.ToUpper(method)] {
				continue
			}

			// Convert operation to map
			opMap, err := operationToMap(op)
			if err != nil {
				continue
			}

			operations[method] = opMap
			hasMatchingOperation = true
		}

		if hasMatchingOperation {
			result[path] = operations
			count++
		}
	}

	return result, count
}

// operationToMap extracts operation fields from the raw map
func operationToMap(op interface{}) (map[string]interface{}, error) {
	if op == nil {
		return nil, fmt.Errorf("nil operation")
	}

	// Try to convert to map
	opMap, ok := op.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("operation is not a map")
	}

	// Return a simplified version with key fields
	result := make(map[string]interface{})

	// Copy basic fields
	if summary, exists := opMap["summary"]; exists {
		result["summary"] = summary
	}
	if description, exists := opMap["description"]; exists {
		result["description"] = description
	}
	if operationId, exists := opMap["operationId"]; exists {
		result["operationId"] = operationId
	}
	if parameters, exists := opMap["parameters"]; exists {
		result["parameters"] = parameters
	}
	if responses, exists := opMap["responses"]; exists {
		result["responses"] = responses
	}

	return result, nil
}

// transformPaths applies prefix transformations to paths
func transformPaths(paths map[string]map[string]interface{}, stripPrefix, addPrefix string) map[string]map[string]interface{} {
	result := make(map[string]map[string]interface{})

	for path, operations := range paths {
		newPath := path

		// Strip prefix if specified
		if stripPrefix != "" && strings.HasPrefix(newPath, stripPrefix) {
			newPath = strings.TrimPrefix(newPath, stripPrefix)
			// Ensure path starts with /
			if !strings.HasPrefix(newPath, "/") {
				newPath = "/" + newPath
			}
		}

		// Add prefix if specified
		if addPrefix != "" {
			newPath = addPrefix + newPath
		}

		result[newPath] = operations
	}

	return result
}

// fragmentDocument is the generated fragment's field order. yaml.Marshal of a
// plain map sorts keys alphabetically, which buries the SEAM metadata the
// curator must edit below the paths section; a struct keeps it at the top.
// Authored fields a curator must decide (x-vault-path, x-inject-as,
// x-required-scope, x-api-version, ...) are deliberately not generated.
type fragmentDocument struct {
	XSeamSchema        string                 `yaml:"x-seam-schema"`
	XSeamOwner         string                 `yaml:"x-seam-owner"`
	XUpstream          string                 `yaml:"x-upstream"`
	XUpstreamPlaintext string                 `yaml:"x-upstream-plaintext,omitempty"`
	OpenAPI            string                 `yaml:"openapi,omitempty"`
	Info               map[string]interface{} `yaml:"info,omitempty"`
	Paths              interface{}            `yaml:"paths"`
	Components         interface{}            `yaml:"components,omitempty"`
}

// buildFragment builds a SEAM fragment structure
func buildFragment(owner string, paths map[string]map[string]interface{}, upstream, sourceURL string, parsedSpec map[string]interface{}) *fragmentDocument {
	fragment := &fragmentDocument{
		XSeamSchema: "v1",
		XSeamOwner:  owner,
		XUpstream:   upstream,
		OpenAPI:     "3.1.0",
		Info: map[string]interface{}{
			"title":       fmt.Sprintf("%s API", owner),
			"version":     "1.0.0",
			"description": fmt.Sprintf("Fragment imported from %s", sourceURL),
		},
		Paths: paths,
	}

	if strings.HasPrefix(upstream, "http://") {
		fragment.XUpstreamPlaintext = "acknowledged"
	}
	if components := sourceComponents(parsedSpec); components != nil {
		fragment.Components = components
	}

	return fragment
}

// writeFragment writes a fragment to a file in YAML format
func writeFragment(path string, fragment *fragmentDocument) error {
	// Marshal to YAML
	yamlBytes, err := yaml.Marshal(fragment)
	if err != nil {
		return fmt.Errorf("failed to marshal fragment: %w", err)
	}

	// Write to file
	if err := os.WriteFile(path, yamlBytes, 0644); err != nil {
		return fmt.Errorf("failed to write fragment file: %w", err)
	}

	return nil
}
