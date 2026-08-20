package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Fragment represents a single SEAM route fragment loaded from disk
type Fragment struct {
	// Metadata
	Owner      string // Service name (from x-seam-owner or directory)
	SchemaVer  string // Schema version (x-seam-schema)
	APIVersion string // API version (x-api-version, or "_unversioned")
	SourceFile string // Path to the fragment file on disk
	Hash       string // Stable content hash for change detection

	// Content
	RawContent     []byte         // Original YAML/JSON content
	ParsedFragment map[string]any // Unmarshaled fragment content

	// Quarantine status
	QueuedForQuarantine bool     // Whether this fragment failed validation
	QuarantineReasons   []string // Human-readable reasons for quarantine
}

// FragmentLoader loads and validates SEAM route fragments
type FragmentLoader struct {
	schemaCompiler      *jsonschema.Compiler
	fragments           []*Fragment
	quarantined         []*Fragment
	lastLoadedTimestamp time.Time
	allowlistEnforcer   *AllowlistEnforcer // Allowlist enforcer for vault-path and upstream-host validation
}

// NewFragmentLoader creates a new fragment loader
func NewFragmentLoader() (*FragmentLoader, error) {
	// Initialize JSON Schema compiler
	compiler := jsonschema.NewCompiler()

	return &FragmentLoader{
		schemaCompiler:      compiler,
		fragments:           []*Fragment{},
		quarantined:         []*Fragment{},
		lastLoadedTimestamp: time.Time{},
		allowlistEnforcer:   nil, // Will be set later if needed
	}, nil
}

// SetAllowlistEnforcer sets the allowlist enforcer for vault-path and upstream-host validation
func (fl *FragmentLoader) SetAllowlistEnforcer(enforcer *AllowlistEnforcer) {
	fl.allowlistEnforcer = enforcer
	log.Printf("[Fragment] Allowlist enforcer configured")
}

// LoadDirectory loads all fragments from a directory tree
// Expected layout: fragments.d/<service>/<fragment-name>.yaml
func (fl *FragmentLoader) LoadDirectory(fragmentsDir string) error {
	log.Printf("[Fragment] Loading fragments from directory: %s", fragmentsDir)

	// Set the timestamp when loading starts
	fl.lastLoadedTimestamp = time.Now()

	if _, err := os.Stat(fragmentsDir); os.IsNotExist(err) {
		// Directory doesn't exist - not an error, just no fragments
		log.Printf("[Fragment] Fragments directory does not exist: %s (no fragments to load)", fragmentsDir)
		return nil
	}

	loadedCount := 0
	errorCount := 0

	// Walk the directory tree looking for .yaml and .yml files
	err := filepath.Walk(fragmentsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("[Fragment] Error accessing path %s: %v", path, err)
			return err
		}

		// Kubernetes projects ConfigMap volumes through an atomic-writer layout:
		// a real ..<timestamp> directory, a ..data symlink to that directory,
		// and canonical fragment symlinks through ..data. Ignore every internal
		// entry so the timestamped copy is not loaded alongside its canonical
		// fragment path. filepath.Walk does not follow the ..data symlink, but
		// handling all .. entries here keeps that behavior explicit.
		if path != fragmentsDir && strings.HasPrefix(info.Name(), "..") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			return nil
		}

		// Only process .yaml, .yml, and .json files
		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".json") {
			log.Printf("[Fragment] Loading fragment file: %s", path)
			fragment, err := fl.loadFragmentFile(path)
			if err != nil {
				// Log but don't fail - quarantine problematic fragments
				log.Printf("[Fragment] Warning: failed to load fragment %s: %v", path, err)
				errorCount++
				return nil
			}

			if fragment != nil {
				fl.fragments = append(fl.fragments, fragment)
				loadedCount++
				log.Printf("[Fragment] Successfully loaded fragment: %s (owner: %s, schema: %s)", path, fragment.Owner, fragment.SchemaVer)
			}
		}

		return nil
	})

	if err != nil {
		log.Printf("[Fragment] Error walking directory tree: %v", err)
	} else {
		log.Printf("[Fragment] Fragment loading complete: %d loaded, %d errors", loadedCount, errorCount)
	}

	return err
}

// loadFragmentFile loads a single fragment file
func (fl *FragmentLoader) loadFragmentFile(path string) (*Fragment, error) {
	// Read the file content
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Parse as JSON
	var parsed map[string]any
	if err := json.Unmarshal(content, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Compute stable hash
	hash := sha256.Sum256(content)
	hashStr := hex.EncodeToString(hash[:])[:16]

	// Extract owner from directory name (last component of path)
	dir := filepath.Dir(path)
	owner := filepath.Base(dir)

	// Extract schema version
	schemaVer, _ := parsed["x-seam-schema"].(string)
	if schemaVer == "" {
		schemaVer = "unknown"
	}

	// Extract API version
	apiVersion, _ := parsed["x-api-version"].(string)
	if apiVersion == "" {
		apiVersion = "_unversioned"
	}

	fragment := &Fragment{
		Owner:          owner,
		SchemaVer:      schemaVer,
		APIVersion:     apiVersion,
		SourceFile:     path,
		Hash:           hashStr,
		RawContent:     content,
		ParsedFragment: parsed,
	}

	return fragment, nil
}

// ValidateFragments validates all loaded fragments against the schema
func (fl *FragmentLoader) ValidateFragments(schemaPath string) error {
	log.Printf("[Fragment] Validating fragments against schema: %s", schemaPath)

	// Load the schema
	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		log.Printf("[Fragment] Error reading schema file: %v", err)
		return fmt.Errorf("failed to read schema file: %w", err)
	}

	var schemaDef map[string]any
	if err := json.Unmarshal(schemaBytes, &schemaDef); err != nil {
		log.Printf("[Fragment] Error parsing schema JSON: %v", err)
		return fmt.Errorf("failed to parse schema JSON: %w", err)
	}

	// Compile the schema
	if err := fl.schemaCompiler.AddResource("schema.json", schemaDef); err != nil {
		log.Printf("[Fragment] Error adding schema resource: %v", err)
		return fmt.Errorf("failed to add schema resource: %w", err)
	}

	schema, err := fl.schemaCompiler.Compile("schema.json")
	if err != nil {
		log.Printf("[Fragment] Error compiling schema: %v", err)
		return fmt.Errorf("failed to compile schema: %w", err)
	}

	log.Printf("[Fragment] Schema loaded successfully, validating %d fragments", len(fl.fragments))

	validCount := 0
	quarantinedCount := 0

	// Validate each fragment
	for _, fragment := range fl.fragments {
		if err := fl.validateFragment(fragment, schema); err != nil {
			fragment.QueuedForQuarantine = true
			fragment.QuarantineReasons = append(fragment.QuarantineReasons, err.Error())
			quarantinedCount++
			log.Printf("[Fragment] Fragment quarantined: %s - %v", fragment.SourceFile, err)
		} else {
			validCount++
			log.Printf("[Fragment] Fragment validated successfully: %s", fragment.SourceFile)
		}
	}

	// Move quarantined fragments to separate slice
	var kept []*Fragment
	for _, fragment := range fl.fragments {
		if fragment.QueuedForQuarantine {
			fl.quarantined = append(fl.quarantined, fragment)
		} else {
			kept = append(kept, fragment)
		}
	}
	fl.fragments = kept

	log.Printf("[Fragment] Validation complete: %d valid, %d quarantined", validCount, quarantinedCount)

	return nil
}

// validateFragment validates a single fragment against the schema
func (fl *FragmentLoader) validateFragment(fragment *Fragment, schema *jsonschema.Schema) error {
	// Validate the parsed fragment
	if err := schema.Validate(fragment.ParsedFragment); err != nil {
		return fmt.Errorf("schema validation failed: %w", err)
	}

	// Check x-seam-owner matches directory (basename is free-form, never authoritative)
	// The PARENT DIRECTORY (from which fragment.Owner is derived) is the authoritative source
	if fragment.Owner != "" {
		ownerFromFragment, hasOwnerField := fragment.ParsedFragment["x-seam-owner"]
		ownerStr, _ := ownerFromFragment.(string)

		// Quarantine if x-seam-owner field is omitted
		if !hasOwnerField {
			return fmt.Errorf("x-seam-owner field omitted: fragment must declare x-seam-owner matching its parent directory (%s)", fragment.Owner)
		}

		// Quarantine if x-seam-owner doesn't match parent directory
		if ownerStr != "" && ownerStr != fragment.Owner {
			return fmt.Errorf("x-seam-owner mismatch: fragment declares '%s' but parent directory is '%s'", ownerStr, fragment.Owner)
		}
	}

	// Check for reserved path collisions
	paths, ok := fragment.ParsedFragment["paths"].(map[string]any)
	if !ok {
		return nil // No paths defined
	}

	reservedPaths := map[string]bool{
		"/docs":               true,
		"/docs/route":         true,
		"/openapi.json":       true,
		"/whoami":             true,
		"/scopes":             true,
		"/changes":            true,
		"/health/credentials": true,
		"/health/upstreams":   true,
		"/config/status":      true,
	}

	reservedPrefixes := []string{"/docs/", "/health/", "/config/", "/approvals/", "/_seam/"}

	// Check exact matches first so a fragment declaring both an exact reserved
	// path and a reserved-prefix path gets the more specific reason. Sort the
	// paths to keep quarantine reasons deterministic across map iteration.
	pathNames := make([]string, 0, len(paths))
	for path := range paths {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)

	for _, path := range pathNames {
		if reservedPaths[path] {
			return fmt.Errorf("fragment declares reserved path: %s", path)
		}
	}

	for _, path := range pathNames {
		for _, prefix := range reservedPrefixes {
			if strings.HasPrefix(path, prefix) {
				return fmt.Errorf("fragment declares path with reserved prefix: %s (prefix: %s)", path, prefix)
			}
		}
	}

	// Validate vault paths and upstream hosts if allowlist enforcer is configured
	if fl.allowlistEnforcer != nil {
		// Extract and validate x-vault-path from fragment root
		if vaultPath, hasVaultPath := fragment.ParsedFragment["x-vault-path"].(string); hasVaultPath {
			if err := fl.allowlistEnforcer.ValidateVaultPath(vaultPath, fragment.Owner); err != nil {
				return fmt.Errorf("vault_path_validation_failed: %w", err)
			}
		}

		// Validate upstream hosts in each operation
		for path, pathItem := range paths {
			pathItemMap, ok := pathItem.(map[string]any)
			if !ok {
				continue
			}

			// Check each HTTP method for x-upstream extension
			for _, method := range []string{"get", "post", "put", "delete", "patch", "options", "head", "trace"} {
				operation, hasOperation := pathItemMap[method].(map[string]any)
				if !hasOperation {
					continue
				}

				// Validate x-upstream if present
				if upstreamURL, hasUpstream := operation["x-upstream"].(string); hasUpstream {
					if err := fl.allowlistEnforcer.ValidateUpstreamHost(upstreamURL); err != nil {
						return fmt.Errorf("upstream_host_validation_failed for %s %s: %w", strings.ToUpper(method), path, err)
					}
				}
			}
		}
	}

	return nil
}

type fragmentCollisionKey struct {
	path       string
	method     string
	apiVersion string
}

// DetectPathCollisions detects collisions on the (path, method, x-api-version)
// key and quarantines the later fragment. Fragments are ordered by source
// filename so that the incumbent is deterministic and is never evicted.
// This should be called after ValidateFragments but before MergeFragments.
func (fl *FragmentLoader) DetectPathCollisions() {
	log.Printf("[Fragment] Detecting path collisions between %d fragments", len(fl.fragments))

	// filepath.Walk currently visits files in lexical order, but make that
	// ordering an explicit part of collision handling so callers that populate
	// fragments directly get the same deterministic incumbent.
	sort.SliceStable(fl.fragments, func(i, j int) bool {
		return fl.fragments[i].SourceFile < fl.fragments[j].SourceFile
	})

	// Track which exact route/version triple is claimed by which fragment.
	claims := make(map[fragmentCollisionKey]string)

	for _, fragment := range fl.fragments {
		if fragment.QueuedForQuarantine {
			continue // Skip already quarantined fragments
		}

		keys := fragmentCollisionKeys(fragment)
		collisionReasons := make([]string, 0)
		for _, key := range keys {
			if incumbent, exists := claims[key]; exists {
				reason := fmt.Sprintf(
					"path_collision: %s %s x-api-version=%s already claimed by %s",
					key.method, key.path, key.apiVersion, incumbent,
				)
				collisionReasons = append(collisionReasons, reason)
				log.Printf("[Fragment] %s in %s", reason, fragment.SourceFile)
			}
		}

		if len(collisionReasons) > 0 {
			// Quarantine the whole later fragment. Do not add any of its
			// keys, so the incumbent remains the sole claimant.
			fragment.QueuedForQuarantine = true
			fragment.QuarantineReasons = append(fragment.QuarantineReasons, collisionReasons...)
			continue
		}

		for _, key := range keys {
			claims[key] = fragment.SourceFile
		}
	}

	// Move quarantined fragments to separate slice
	var kept []*Fragment
	for _, fragment := range fl.fragments {
		if fragment.QueuedForQuarantine {
			fl.quarantined = append(fl.quarantined, fragment)
		} else {
			kept = append(kept, fragment)
		}
	}
	fl.fragments = kept

	log.Printf("[Fragment] Path collision detection complete: %d fragments quarantined due to collisions", len(fl.quarantined))
}

// fragmentCollisionKeys returns the collision key for every OpenAPI operation
// in a fragment. Path-item metadata such as parameters and summary is not a
// route operation and therefore does not participate in collision detection.
func fragmentCollisionKeys(fragment *Fragment) []fragmentCollisionKey {
	paths, ok := fragment.ParsedFragment["paths"].(map[string]any)
	if !ok {
		return nil
	}

	apiVersion := fragment.APIVersion
	if apiVersion == "" {
		apiVersion, _ = fragment.ParsedFragment["x-api-version"].(string)
	}
	if apiVersion == "" {
		apiVersion = "_unversioned"
	}

	pathNames := make([]string, 0, len(paths))
	for path := range paths {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)

	keys := make([]fragmentCollisionKey, 0)
	for _, path := range pathNames {
		pathItem, ok := paths[path].(map[string]any)
		if !ok {
			continue
		}

		methods := make([]string, 0, len(pathItem))
		for method := range pathItem {
			if isOpenAPIOperationMethod(method) {
				methods = append(methods, method)
			}
		}
		sort.Strings(methods)

		for _, method := range methods {
			keys = append(keys, fragmentCollisionKey{path: path, method: method, apiVersion: apiVersion})
		}
	}

	return keys
}

func isOpenAPIOperationMethod(method string) bool {
	switch method {
	case "get", "put", "post", "delete", "options", "head", "patch", "trace":
		return true
	default:
		return false
	}
}

// MergeFragments merges all valid fragments into a single OpenAPI document
func (fl *FragmentLoader) MergeFragments(baseURL string) ([]byte, error) {
	log.Printf("[Fragment] Merging %d fragments into single OpenAPI 3.1 spec", len(fl.fragments))

	if len(fl.fragments) == 0 {
		log.Printf("[Fragment] No fragments to merge, creating minimal empty document")
		// Return minimal empty document
		emptyDoc := map[string]any{
			"openapi": "3.1.0",
			"info": map[string]any{
				"title":       "SEAM Gateway",
				"version":     "1.0.0",
				"description": "Service Endpoint API Mesh",
			},
			"paths": map[string]any{},
			"servers": []map[string]any{
				{
					"url":         baseURL,
					"description": "SEAM caller-facing endpoint",
				},
			},
		}
		return json.MarshalIndent(emptyDoc, "", "  ")
	}

	log.Printf("[Fragment] Building merged OpenAPI 3.1 document with base URL: %s", baseURL)

	// Build merged document
	merged := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "SEAM Gateway",
			"version":     "1.0.0",
			"description": "Service Endpoint API Mesh",
		},
		"paths":      map[string]any{},
		"components": map[string]any{},
		"servers": []map[string]any{
			{
				"url":         baseURL,
				"description": "SEAM caller-facing endpoint",
			},
		},
	}

	totalPaths := 0
	skippedPaths := 0

	// Merge all fragments
	for _, fragment := range fl.fragments {
		log.Printf("[Fragment] Merging fragment from: %s (owner: %s)", fragment.SourceFile, fragment.Owner)

		// Merge paths
		if paths, ok := fragment.ParsedFragment["paths"].(map[string]any); ok {
			mergedPaths := merged["paths"].(map[string]any)
			for path, pathItem := range paths {
				// Check for collisions (same path, method, version)
				if _, exists := mergedPaths[path]; exists {
					// Collision detected - skip this fragment's path
					log.Printf("[Fragment] Warning: path collision detected for %s, skipping fragment %s", path, fragment.SourceFile)
					skippedPaths++
					continue
				}
				mergedPaths[path] = pathItem
				totalPaths++
				log.Printf("[Fragment] Added path: %s from %s", path, fragment.SourceFile)
			}
		}

		// Merge components
		if components, ok := fragment.ParsedFragment["components"].(map[string]any); ok {
			mergedComponents := merged["components"].(map[string]any)
			for key, value := range components {
				// Note: This is a simple merge - real implementation needs more sophisticated handling
				if existing, exists := mergedComponents[key]; exists {
					if existingMap, ok := existing.(map[string]any); ok {
						if valueMap, ok := value.(map[string]any); ok {
							for k, v := range valueMap {
								existingMap[k] = v
							}
							log.Printf("[Fragment] Merged component section: %s", key)
						}
					}
				} else {
					mergedComponents[key] = value
					log.Printf("[Fragment] Added component section: %s", key)
				}
			}
		}
	}

	log.Printf("[Fragment] Merge complete: %d paths added, %d paths skipped due to collisions", totalPaths, skippedPaths)

	mergedJSON, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		log.Printf("[Fragment] Error serializing merged document: %v", err)
		return nil, fmt.Errorf("failed to serialize merged document: %w", err)
	}

	log.Printf("[Fragment] Successfully merged %d fragments into OpenAPI 3.1 spec (%d bytes)", len(fl.fragments), len(mergedJSON))

	return mergedJSON, nil
}

// GetQuarantined returns all quarantined fragments
func (fl *FragmentLoader) GetQuarantined() []*Fragment {
	return fl.quarantined
}

// GetValidFragmentCount returns the number of valid (non-quarantined) fragments
func (fl *FragmentLoader) GetValidFragmentCount() int {
	return len(fl.fragments)
}

// GetQuarantinedCount returns the number of quarantined fragments
func (fl *FragmentLoader) GetQuarantinedCount() int {
	return len(fl.quarantined)
}

// FragmentStatus represents the status of a single fragment
type FragmentStatus struct {
	SourceFile        string   `json:"source_file"`
	Owner             string   `json:"owner"`
	SchemaVersion     string   `json:"schema_version"`
	APIVersion        string   `json:"api_version"`
	Status            string   `json:"status"` // "valid" or "quarantined"
	QuarantineReasons []string `json:"quarantine_reasons,omitempty"`
	Hash              string   `json:"hash"`
}

// GetFragmentStatus returns detailed status information about all fragments
func (fl *FragmentLoader) GetFragmentStatus() map[string]interface{} {
	// Collect all fragment statuses
	allFragments := []FragmentStatus{}
	for _, fragment := range fl.fragments {
		allFragments = append(allFragments, FragmentStatus{
			SourceFile:        fragment.SourceFile,
			Owner:             fragment.Owner,
			SchemaVersion:     fragment.SchemaVer,
			APIVersion:        fragment.APIVersion,
			Status:            "valid",
			QuarantineReasons: []string{},
			Hash:              fragment.Hash,
		})
	}
	for _, fragment := range fl.quarantined {
		allFragments = append(allFragments, FragmentStatus{
			SourceFile:        fragment.SourceFile,
			Owner:             fragment.Owner,
			SchemaVersion:     fragment.SchemaVer,
			APIVersion:        fragment.APIVersion,
			Status:            "quarantined",
			QuarantineReasons: fragment.QuarantineReasons,
			Hash:              fragment.Hash,
		})
	}

	// Build the response
	validCount := len(fl.fragments)
	quarantinedCount := len(fl.quarantined)

	return map[string]interface{}{
		"fragments":             allFragments,
		"valid_count":           validCount,
		"quarantined_count":     quarantinedCount,
		"total_count":           validCount + quarantinedCount,
		"last_loaded_timestamp": fl.lastLoadedTimestamp.Format(time.RFC3339),
		"server_continues":      true, // Server continues even if all fragments are quarantined
	}
}
