package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pb33f/libopenapi"
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
	RawContent    []byte          // Original YAML/JSON content
	ParsedFragment map[string]any // Unmarshaled fragment content

	// Quarantine status
	QueuedForQuarantine bool     // Whether this fragment failed validation
	QuarantineReasons   []string // Human-readable reasons for quarantine
}

// FragmentLoader loads and validates SEAM route fragments
type FragmentLoader struct {
	schemaCompiler *jsonschema.Compiler
	fragments      []*Fragment
	quarantined    []*Fragment
	document       libopenapi.Document // Merged document
}

// NewFragmentLoader creates a new fragment loader
func NewFragmentLoader() (*FragmentLoader, error) {
	// Initialize JSON Schema compiler
	compiler := jsonschema.NewCompiler()

	return &FragmentLoader{
		schemaCompiler: compiler,
		fragments:      []*Fragment{},
		quarantined:    []*Fragment{},
	}, nil
}

// LoadDirectory loads all fragments from a directory tree
// Expected layout: fragments.d/<service>/<fragment-name>.yaml
func (fl *FragmentLoader) LoadDirectory(fragmentsDir string) error {
	if _, err := os.Stat(fragmentsDir); os.IsNotExist(err) {
		// Directory doesn't exist - not an error, just no fragments
		return nil
	}

	// Walk the directory tree looking for .yaml and .yml files
	err := filepath.Walk(fragmentsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		// Only process .yaml and .yml files
		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
			fragment, err := fl.loadFragmentFile(path)
			if err != nil {
				// Log but don't fail - quarantine problematic fragments
				fmt.Printf("Warning: failed to load fragment %s: %v\n", path, err)
				return nil
			}

			if fragment != nil {
				fl.fragments = append(fl.fragments, fragment)
			}
		}

		return nil
	})

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
		Owner:       owner,
		SchemaVer:   schemaVer,
		APIVersion:  apiVersion,
		SourceFile:  path,
		Hash:        hashStr,
		RawContent:  content,
		ParsedFragment: parsed,
	}

	return fragment, nil
}

// ValidateFragments validates all loaded fragments against the schema
func (fl *FragmentLoader) ValidateFragments(schemaPath string) error {
	// Load the schema
	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("failed to read schema file: %w", err)
	}

	var schemaDef map[string]any
	if err := json.Unmarshal(schemaBytes, &schemaDef); err != nil {
		return fmt.Errorf("failed to parse schema JSON: %w", err)
	}

	// Compile the schema
	if err := fl.schemaCompiler.AddResource("schema.json", schemaDef); err != nil {
		return fmt.Errorf("failed to add schema resource: %w", err)
	}

	schema, err := fl.schemaCompiler.Compile("schema.json")
	if err != nil {
		return fmt.Errorf("failed to compile schema: %w", err)
	}

	// Validate each fragment
	for _, fragment := range fl.fragments {
		if err := fl.validateFragment(fragment, schema); err != nil {
			fragment.QueuedForQuarantine = true
			fragment.QuarantineReasons = append(fragment.QuarantineReasons, err.Error())
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

	return nil
}

// validateFragment validates a single fragment against the schema
func (fl *FragmentLoader) validateFragment(fragment *Fragment, schema *jsonschema.Schema) error {
	// Validate the parsed fragment
	if err := schema.Validate(fragment.ParsedFragment); err != nil {
		return fmt.Errorf("schema validation failed: %w", err)
	}

	// Check x-seam-owner matches directory
	if fragment.Owner != "" {
		ownerFromFragment, _ := fragment.ParsedFragment["x-seam-owner"].(string)
		if ownerFromFragment != "" && ownerFromFragment != fragment.Owner {
			return fmt.Errorf("x-seam-owner mismatch: fragment declares %s but loaded from %s directory", ownerFromFragment, fragment.Owner)
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

	for path := range paths {
		// Check exact matches
		if reservedPaths[path] {
			return fmt.Errorf("fragment declares reserved path: %s", path)
		}

		// Check prefix matches
		for _, prefix := range reservedPrefixes {
			if strings.HasPrefix(path, prefix) {
				return fmt.Errorf("fragment declares path with reserved prefix: %s (prefix: %s)", path, prefix)
			}
		}
	}

	return nil
}

// MergeFragments merges all valid fragments into a single OpenAPI document
func (fl *FragmentLoader) MergeFragments(baseURL string) ([]byte, error) {
	if len(fl.fragments) == 0 {
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

	// Merge all fragments
	for _, fragment := range fl.fragments {
		// Merge paths
		if paths, ok := fragment.ParsedFragment["paths"].(map[string]any); ok {
			mergedPaths := merged["paths"].(map[string]any)
			for path, pathItem := range paths {
				// Check for collisions (same path, method, version)
				if _, exists := mergedPaths[path]; exists {
					// Collision detected - skip this fragment's path
					fmt.Printf("Warning: path collision detected for %s, skipping fragment %s\n", path, fragment.SourceFile)
					continue
				}
				mergedPaths[path] = pathItem
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
						}
					}
				} else {
					mergedComponents[key] = value
				}
			}
		}
	}

	return json.MarshalIndent(merged, "", "  ")
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
