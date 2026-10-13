package spec

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProviderProfilesEnvVar names the operator-owned provider-profile file
// (a ConfigMap mount in SEAM's own manifest directory). Unset means the
// compiled-in seed set below; set-but-unreadable is a hard failure, the
// same fail-closed posture as the upstream allowlist.
const ProviderProfilesEnvVar = "SEAM_PROVIDER_PROFILES"

// ownerPlaceholder is substituted with the upper-cased x-seam-owner
// (`-` mapped to `_`) wherever it appears in a profile string value, so a
// seed profile can name SEAM_CRED_{OWNER}_API_KEY without knowing which
// service will inherit it.
const ownerPlaceholder = "{OWNER}"

// profileDefaultFields is the closed set of fragment fields a provider
// profile may default: credential source and injection shape, probe path,
// streaming, usage pointers and body mode (plan, Multi-Provider Credentials
// §6). Everything else — operator-owned controls (x-unscrubbable,
// x-upstream-plaintext, scope and guard fields), per-fragment routing data
// (x-upstream, paths), and operator ceilings (x-max-*-bytes, x-timeout) —
// is refused at profile-load time in both lint and the gateway. A profile
// therefore cannot relax a control by construction, not merely by checks
// run after the merge.
var profileDefaultFields = map[string]struct{}{
	"x-credential-source":  {},
	"x-credential-env":     {},
	"x-credential-file":    {},
	"x-inject-as":          {},
	"x-inject-static":      {},
	"x-credential-probe":   {},
	"x-streaming":          {},
	"x-cost-from-response": {},
	"x-body-mode":          {},
}

// seedProviderProfilesYAML is the shipped seed set. The ConfigMap in SEAM's
// manifest directory (declarative-config/k8s/rs-manager/seam/) starts as a
// byte-identical copy of this literal; operators edit the mount, never a
// fragment, and the compiled copy is the fallback when no file is configured.
const seedProviderProfilesYAML = `profiles:
  openai-compatible:
    x-credential-source: env
    x-credential-env: SEAM_CRED_{OWNER}_API_KEY
    x-inject-as:
      kind: bearer
    x-body-mode: opaque
    x-streaming: true
    x-credential-probe:
      path: /models
      method: GET
      interval: 5m
    x-cost-from-response:
      charges:
        - pointer: /usage/prompt_tokens
          price: 1
        - pointer: /usage/completion_tokens
          price: 1
  anthropic-messages:
    x-credential-source: env
    x-credential-env: SEAM_CRED_{OWNER}_API_KEY
    x-inject-as:
      kind: header
      name: x-api-key
    x-inject-static:
      kind: header
      name: anthropic-version
      value: "2023-06-01"
    x-body-mode: opaque
    x-streaming: true
    x-credential-probe:
      path: /v1/models
      method: GET
      interval: 5m
    x-cost-from-response:
      charges:
        - pointer: /usage/input_tokens
          price: 1
        - pointer: /usage/output_tokens
          price: 1
  bearer-json:
    x-inject-as:
      kind: bearer
    x-body-mode: validated
    x-credential-probe:
      path: /health
      method: GET
      interval: 5m
`

// ProviderProfiles is a resolved, validated set of operator-owned profile
// defaults keyed by profile name.
type ProviderProfiles struct {
	byName map[string]map[string]any
	// Source names where the set came from ("seed", or the file path), for
	// diagnostics.
	Source string
}

// ParseProviderProfiles validates raw profile data and returns the set.
// The input shape mirrors the ConfigMap key: {"profiles": {name: {field: value}}}.
func ParseProviderProfiles(raw []byte, source string) (*ProviderProfiles, error) {
	var document struct {
		Profiles map[string]map[string]any `yaml:"profiles" json:"profiles"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("provider profiles %s: parse failed: %w", source, err)
	}
	if len(document.Profiles) == 0 {
		return nil, fmt.Errorf("provider profiles %s: no profiles defined", source)
	}
	profiles := &ProviderProfiles{byName: document.Profiles, Source: source}
	if err := profiles.validate(); err != nil {
		return nil, err
	}
	return profiles, nil
}

// SeedProviderProfiles returns the compiled-in seed set.
func SeedProviderProfiles() (*ProviderProfiles, error) {
	return ParseProviderProfiles([]byte(seedProviderProfilesYAML), "seed")
}

// LoadProviderProfilesFile reads and validates an operator-owned profile
// file. Any parse or policy failure is an error: the caller fails closed
// rather than silently falling back to the seed set.
func LoadProviderProfilesFile(path string) (*ProviderProfiles, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("provider profiles: cannot read %s: %w", path, err)
	}
	return ParseProviderProfiles(content, path)
}

// ResolveProviderProfiles applies the operator precedence: the file named
// by SEAM_PROVIDER_PROFILES when set, otherwise the compiled-in seed set.
func ResolveProviderProfiles() (*ProviderProfiles, error) {
	if path := os.Getenv(ProviderProfilesEnvVar); path != "" {
		return LoadProviderProfilesFile(path)
	}
	return SeedProviderProfiles()
}

// Names returns the known profile names in stable order.
func (p *ProviderProfiles) Names() []string {
	if p == nil {
		return nil
	}
	names := make([]string, 0, len(p.byName))
	for name := range p.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Lookup returns the defaults for one profile name.
func (p *ProviderProfiles) Lookup(name string) (map[string]any, bool) {
	if p == nil {
		return nil, false
	}
	defaults, ok := p.byName[name]
	return defaults, ok
}

// validate enforces the closed field set. A profile entry carrying any key
// outside profileDefaultFields — in particular an operator-owned control or
// a routing field — is rejected here, before any fragment can inherit it.
func (p *ProviderProfiles) validate() error {
	for name, defaults := range p.byName {
		if len(defaults) == 0 {
			return fmt.Errorf("provider profiles %s: profile %q is empty", p.Source, name)
		}
		for field := range defaults {
			if _, ok := profileDefaultFields[field]; !ok {
				return fmt.Errorf(
					"provider profiles %s: profile %q sets %q, which is not a defaultable field (allowed: %s); operator-owned controls and routing fields cannot come from a profile",
					p.Source, name, field, strings.Join(sortedProfileFields(), ", "))
			}
		}
	}
	return nil
}

func sortedProfileFields() []string {
	fields := make([]string, 0, len(profileDefaultFields))
	for field := range profileDefaultFields {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

// ApplyProviderProfile merges the named profile's defaults into a parsed
// fragment in place. A field the fragment already declares is left
// untouched — fragment fields override profile defaults — and every string
// value carries {OWNER} substitution. An unknown profile name is an error;
// the caller quarantines (gateway) or reports (lint) the whole fragment.
func ApplyProviderProfile(parsed map[string]any, profiles *ProviderProfiles) (string, error) {
	rawName, declared := parsed["x-provider-profile"]
	if !declared || rawName == nil {
		return "", nil
	}
	name, ok := rawName.(string)
	if !ok || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("x-provider-profile must be a non-empty string")
	}
	name = strings.TrimSpace(name)
	defaults, known := profiles.Lookup(name)
	if !known {
		return name, fmt.Errorf("unknown provider profile %q (known profiles: %s)", name, strings.Join(profiles.Names(), ", "))
	}
	ownerToken := profileOwnerToken(parsed)
	for _, field := range sortedDefaultKeys(defaults) {
		if _, exists := parsed[field]; exists {
			continue
		}
		parsed[field] = substituteProfileOwner(deepCopyLintValue(defaults[field]), ownerToken)
	}
	return name, nil
}

// profileOwnerToken derives the SEAM_CRED_<OWNER>_ namespace token from the
// fragment's declared owner, using the same mapping as the ownership rule.
func profileOwnerToken(parsed map[string]any) string {
	owner, _ := parsed["x-seam-owner"].(string)
	return strings.ToUpper(strings.ReplaceAll(owner, "-", "_"))
}

func substituteProfileOwner(value any, token string) any {
	switch typed := value.(type) {
	case string:
		return strings.ReplaceAll(typed, ownerPlaceholder, token)
	case map[string]any:
		for key, child := range typed {
			typed[key] = substituteProfileOwner(child, token)
		}
		return typed
	case []any:
		for index, child := range typed {
			typed[index] = substituteProfileOwner(child, token)
		}
		return typed
	default:
		return value
	}
}

// sortedDefaultKeys iterates a defaults map deterministically so applying a
// profile is reproducible regardless of map order.
func sortedDefaultKeys(defaults map[string]any) []string {
	keys := make([]string, 0, len(defaults))
	for key := range defaults {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func deepCopyLintValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		copied := make(map[string]any, len(typed))
		for key, child := range typed {
			copied[key] = deepCopyLintValue(child)
		}
		return copied
	case []any:
		copied := make([]any, len(typed))
		for index, child := range typed {
			copied[index] = deepCopyLintValue(child)
		}
		return copied
	default:
		return value
	}
}
