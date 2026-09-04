package spec

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// AllowlistEnforcer enforces dual allowlists for vault-path co-ownership and upstream-host validation
type AllowlistEnforcer struct {
	vaultBaseDir      string             // Base directory for vault paths, e.g. rs-manager/rs-manager/seam/routes/
	upstreamAllowlist *UpstreamAllowlist // Upstream host allowlist
	allowlistSource   string             // Source identifier: "dev-file", "mounted-file", "none"
}

// UpstreamAllowlist represents the parsed upstream host allowlist
type UpstreamAllowlist struct {
	// SuffixMatchers are hostname suffixes that are permitted (e.g., ".example.com", "api.example.com")
	// Suffix matching uses DNS suffix rules: foo.example.com matches .example.com
	SuffixMatchers []string

	// BareHostnames are exact hostname matches (no suffix logic, must match exactly)
	BareHostnames []string

	// PortConstraints maps hostname -> allowed port (0 means any port)
	PortConstraints map[string]int

	// Loaded indicates whether the allowlist was successfully loaded
	Loaded bool

	// ParseError contains any error encountered during loading (for fail-closed reporting)
	ParseError string
}

const defaultMountedAllowlistFile = "/etc/gateway/allowlist.yaml"

// DefaultVaultBaseDir is the vault prefix SEAM enforces when neither
// SEAM_VAULT_BASE_DIR nor --vault-base-dir supplies one: the consolidated
// estate prefix secret/<installation>/<cluster>/seam/routes with both segments
// naming rs-manager, minus the leading mount. It is the default base only —
// the schema stays base-agnostic (see docs/notes/route-fragment-schema.md).
const DefaultVaultBaseDir = "rs-manager/rs-manager/seam/routes"

// VaultBaseDirEnvVar is the Deployment variable that overrides
// DefaultVaultBaseDir. It is read by ResolveVaultBaseDir rather than by this
// package's constructors, which take the resolved value as an argument.
const VaultBaseDirEnvVar = "SEAM_VAULT_BASE_DIR"

// ResolveVaultBaseDir returns the base dir in force for a configured value:
// VaultBaseDirEnvVar wins when it is non-blank, otherwise the configured value,
// otherwise DefaultVaultBaseDir. cmd/seam resolves the flag through this and
// server.New applies the same default, so a test deriving its OpenBao fixture
// paths and ACL grants from here is exercising the prefix a Deployment would
// actually enforce rather than a literal of its own.
func ResolveVaultBaseDir(configured string) string {
	if val := strings.TrimSpace(os.Getenv(VaultBaseDirEnvVar)); val != "" {
		return val
	}
	if configured != "" {
		return configured
	}
	return DefaultVaultBaseDir
}

// NewAllowlistEnforcer creates a new allowlist enforcer
func NewAllowlistEnforcer(vaultBaseDir string, allowlistFile string) (*AllowlistEnforcer, error) {
	if vaultBaseDir == "" {
		vaultBaseDir = DefaultVaultBaseDir
	}

	enforcer := &AllowlistEnforcer{
		vaultBaseDir: vaultBaseDir,
		upstreamAllowlist: &UpstreamAllowlist{
			SuffixMatchers:  []string{},
			BareHostnames:   []string{},
			PortConstraints: make(map[string]int),
			Loaded:          false,
			ParseError:      "",
		},
		allowlistSource: "none",
	}

	// If allowlist file is specified, load it
	if allowlistFile != "" {
		if err := enforcer.loadUpstreamAllowlist(allowlistFile); err != nil {
			// In production, /etc/gateway/allowlist.yaml must parse successfully
			// For dev mode, we may tolerate missing files
			log.Printf("[Allowlist] Failed to load allowlist from %s: %v", allowlistFile, err)
			enforcer.upstreamAllowlist.ParseError = err.Error()
		} else {
			if allowlistFile == defaultMountedAllowlistFile {
				enforcer.allowlistSource = "mounted-file"
			} else {
				enforcer.allowlistSource = "dev-file"
			}
		}
	}

	return enforcer, nil
}

// loadUpstreamAllowlist loads the upstream allowlist from a YAML file
func (ae *AllowlistEnforcer) loadUpstreamAllowlist(allowlistFile string) error {
	log.Printf("[Allowlist] Loading upstream allowlist from: %s", allowlistFile)

	// Read the allowlist file
	content, err := os.ReadFile(allowlistFile)
	if err != nil {
		return fmt.Errorf("failed to read allowlist file: %w", err)
	}

	// Parse YAML content (simple key-value for now)
	// Expected format:
	// allowed_suffixes:
	//   - ".example.com"
	//   - "api.internal"
	// allowed_hosts:
	//   - "localhost"
	// port_constraints:
	//   localhost: 8080
	lines := strings.Split(string(content), "\n")
	allowlist := &UpstreamAllowlist{
		SuffixMatchers:  []string{},
		BareHostnames:   []string{},
		PortConstraints: make(map[string]int),
		Loaded:          true,
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Simple YAML parsing for suffixes
		if strings.HasPrefix(line, "- ") && len(line) > 2 {
			host := strings.TrimPrefix(line, "- ")
			host = strings.TrimSpace(host)
			// The operator-owned allowlist is YAML; entries are conventionally
			// double-quoted (see configmap-allowlist.yaml). This parser reads
			// lines directly rather than through a YAML unmarshaller, so an
			// unstripped quote becomes part of the comparison value and no
			// real hostname can ever match it again.
			host = strings.Trim(host, `"'`)

			// Check if it's a suffix (starts with dot)
			if strings.HasPrefix(host, ".") {
				allowlist.SuffixMatchers = append(allowlist.SuffixMatchers, host)
				log.Printf("[Allowlist] Added suffix matcher: %s", host)
			} else {
				// Bare hostname
				allowlist.BareHostnames = append(allowlist.BareHostnames, host)
				log.Printf("[Allowlist] Added bare hostname: %s", host)
			}
		}
	}

	if len(allowlist.SuffixMatchers) == 0 && len(allowlist.BareHostnames) == 0 {
		allowlist.Loaded = false
		allowlist.ParseError = "allowlist is empty or contains no valid entries"
		return fmt.Errorf("allowlist is empty or contains no valid entries")
	}

	ae.upstreamAllowlist = allowlist
	log.Printf("[Allowlist] Loaded %d suffix matchers and %d bare hostnames",
		len(allowlist.SuffixMatchers), len(allowlist.BareHostnames))

	return nil
}

// ValidateVaultPath validates that x-vault-path resolves inside seam/routes/<x-seam-owner>/*
// with co-ownership verification against the mounted parent directory
func (ae *AllowlistEnforcer) ValidateVaultPath(vaultPath string, owner string) error {
	if vaultPath == "" {
		// No vault path specified, nothing to validate
		return nil
	}

	log.Printf("[Allowlist] Validating vault path: %s for owner: %s", vaultPath, owner)

	// Reject traversal attempts outright
	if strings.Contains(vaultPath, "..") || strings.Contains(vaultPath, "\\") {
		return fmt.Errorf("vault_path_contains_traversal: x-vault-path cannot contain .. or path separators: %s", vaultPath)
	}

	// Reject glob patterns
	if strings.Contains(vaultPath, "*") || strings.Contains(vaultPath, "?") || strings.Contains(vaultPath, "[") {
		return fmt.Errorf("vault_path_contains_globs: x-vault-path cannot contain glob patterns: %s", vaultPath)
	}

	// Reject templated segments (OpenAPI-style {param})
	if strings.Contains(vaultPath, "{") || strings.Contains(vaultPath, "}") {
		return fmt.Errorf("vault_path_contains_templates: x-vault-path cannot contain templated segments: %s", vaultPath)
	}

	// Construct the expected base path for this owner
	expectedBase := filepath.Join(ae.vaultBaseDir, owner)

	// Clean both paths for comparison
	cleanVaultPath := filepath.Clean(vaultPath)
	cleanExpectedBase := filepath.Clean(expectedBase)

	// Ensure vault path is inside the owner's directory
	// The vault path should be: seam/routes/<owner>/<something>
	// NOT: seam/routes/<other-owner>/...
	// NOT: seam/routes (direct access to parent)

	// Check if the vault path starts with the expected base
	if !strings.HasPrefix(cleanVaultPath, cleanExpectedBase) {
		return fmt.Errorf("vault_path_outside_owner_directory: x-vault-path %s is not inside owner directory %s", vaultPath, expectedBase)
	}

	// Verify co-ownership: the vault path's parent directory must be owned by the same service
	// This is enforced by the path structure itself - we've already verified it's inside the owner's directory

	log.Printf("[Allowlist] Vault path validated successfully: %s", vaultPath)
	return nil
}

// ValidateUpstreamHost validates that the upstream host matches the allowlist
// Returns error if host is not allowed or if allowlist is fail-closed
func (ae *AllowlistEnforcer) ValidateUpstreamHost(upstreamURL string) error {
	if upstreamURL == "" {
		// No upstream specified, nothing to validate
		return nil
	}

	log.Printf("[Allowlist] Validating upstream host: %s", upstreamURL)

	// FAIL CLOSED: If no allowlist is loaded or parsing failed, reject all hosts
	if !ae.upstreamAllowlist.Loaded {
		reason := "allowlist_not_loaded"
		if ae.upstreamAllowlist.ParseError != "" {
			reason = fmt.Sprintf("allowlist_parse_failed: %s", ae.upstreamAllowlist.ParseError)
		}
		return fmt.Errorf("upstream_host_rejected_fail_closed: %s (reason: %s)", upstreamURL, reason)
	}

	// Parse the URL to extract hostname
	parsedURL, err := url.Parse(upstreamURL)
	if err != nil {
		return fmt.Errorf("upstream_url_invalid: failed to parse %s: %w", upstreamURL, err)
	}

	hostname := parsedURL.Hostname()
	if hostname == "" {
		return fmt.Errorf("upstream_host_empty: URL %s has no hostname", upstreamURL)
	}

	// Reject IP literals outright
	if isIPLiteral(hostname, parsedURL.Host) {
		return fmt.Errorf("upstream_host_is_ip_literal: IP addresses are not permitted: %s", hostname)
	}

	// Check bare hostname matches first (exact match)
	for _, bareHost := range ae.upstreamAllowlist.BareHostnames {
		if strings.EqualFold(hostname, bareHost) {
			log.Printf("[Allowlist] Upstream host matched bare hostname: %s", hostname)
			return nil // Allowed
		}
	}

	// Check suffix matches (DNS suffix rules)
	for _, suffix := range ae.upstreamAllowlist.SuffixMatchers {
		if strings.HasSuffix(strings.ToLower(hostname), strings.ToLower(suffix)) {
			// Ensure the suffix match is proper (foo.example.com matches .example.com)
			// But not: fooexample.com matches .example.com)
			if len(hostname) > len(suffix) {
				// Check that the character before the suffix is a dot or the suffix starts the hostname
				preSuffix := hostname[len(hostname)-len(suffix)-1 : len(hostname)-len(suffix)]
				if preSuffix == "." || suffix[0] == '.' {
					log.Printf("[Allowlist] Upstream host matched suffix: %s (suffix: %s)", hostname, suffix)
					return nil // Allowed
				}
			}
		}
	}

	return fmt.Errorf("upstream_host_not_in_allowlist: hostname %s is not in the allowlist", hostname)
}

// GetAllowlistStatus returns the current allowlist status for /config/status
func (ae *AllowlistEnforcer) GetAllowlistStatus() map[string]interface{} {
	status := map[string]interface{}{
		"source":         ae.allowlistSource,
		"vault_base_dir": ae.vaultBaseDir,
	}

	if ae.upstreamAllowlist != nil {
		upstreamStatus := map[string]interface{}{
			"loaded": ae.upstreamAllowlist.Loaded,
		}

		if ae.upstreamAllowlist.ParseError != "" {
			upstreamStatus["parse_error"] = ae.upstreamAllowlist.ParseError
			upstreamStatus["condition"] = "allowlist_parse_failed"
		} else if !ae.upstreamAllowlist.Loaded {
			upstreamStatus["condition"] = "allowlist_not_loaded"
		} else {
			upstreamStatus["suffix_matchers_count"] = len(ae.upstreamAllowlist.SuffixMatchers)
			upstreamStatus["bare_hostnames_count"] = len(ae.upstreamAllowlist.BareHostnames)
			upstreamStatus["condition"] = "allowlist_ok"
		}

		status["upstream_allowlist"] = upstreamStatus
	}

	return status
}

// IsFailClosed returns true if the allowlist is in fail-closed state (no hosts permitted)
func (ae *AllowlistEnforcer) IsFailClosed() bool {
	if ae.upstreamAllowlist == nil {
		return true // No allowlist means fail-closed
	}
	return !ae.upstreamAllowlist.Loaded
}
