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
	"x-instance-param":        internalRouteMetadataPrefix + "instance-param",
	"x-upstream-strip-prefix": internalRouteMetadataPrefix + "upstream-strip-prefix",
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
