package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi-validator"
	"github.com/pb33f/libopenapi-validator/errors"
	validatorpaths "github.com/pb33f/libopenapi-validator/paths"
	"github.com/pb33f/libopenapi/datamodel/high/v3"
	"gopkg.in/yaml.v3"
)

// Loader handles loading and serving OpenAPI specs
//
// SERVER URL CONFIGURATION:
// The base URL is passed as a parameter to the loader constructors (New, NewWithFragments, NewLoader)
// and comes from runtime configuration (CLI flag or SEAM_BASE_URL environment variable).
// This URL is used to populate the OpenAPI spec's servers array when serving the spec via GetRawJSON().
//
// Expected servers array format (OpenAPI 3.1):
//
//	servers:
//	  - url: https://api.example.com
//	    description: SEAM caller-facing endpoint
//
// The base URL is NOT embedded in the source spec files - it's synthesized at runtime
// to allow the same spec fragments to be served from different endpoints in different environments.
type Loader struct {
	specPath       string
	baseURL        string // Runtime-configured caller-facing endpoint URL (from SEAM_BASE_URL or -base-url flag)
	rawDocument    []byte
	specVersion    string // Stable hash of the spec (truncated to 16 chars)
	specHash       string // Full SHA256 hash (64 hex chars)
	loadedDoc      libopenapi.Document
	model          *libopenapi.DocumentModel[v3.Document]
	validator      validator.Validator
	FragmentLoader *FragmentLoader // Fragment loader for fragment mode (public for allowlist configuration)
	fragmentMode   bool            // Whether we're in fragment mode
	fragmentsDir   string          // Directory containing fragment files
}

// New creates a new spec loader in static mode (single openapi.yaml file)
func New(specDir, baseURL string) (*Loader, error) {
	// Expect exactly one spec file in the directory
	specPath := filepath.Join(specDir, "openapi.yaml")
	if _, err := os.Stat(specPath); err != nil {
		return nil, fmt.Errorf("spec file not found: %s: %w", specPath, err)
	}

	// Read the raw spec content
	rawDocument, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read spec file: %w", err)
	}

	// Compute stable hash of the spec
	hash := sha256.Sum256(rawDocument)
	specHash := hex.EncodeToString(hash[:]) // Full 64-character SHA256 hash
	specVersion := specHash[:16]            // Use first 16 hex chars (64 bits) for version

	// Load the document using libopenapi
	loadedDoc, err := libopenapi.NewDocument(rawDocument)
	if err != nil {
		return nil, fmt.Errorf("failed to load OpenAPI document: %w", err)
	}

	// Build the model for validation and route extraction
	documentModel, err := loadedDoc.BuildV3Model()
	if err != nil {
		return nil, fmt.Errorf("failed to build OpenAPI model: %w", err)
	}

	// Store the document model for later use
	model := documentModel

	// Create the validator using the v3 model directly
	// We don't pass any options, so it uses defaults
	// NOTE: This validator is initialized but not yet used for validation (Phase 1a)
	v := validator.NewValidatorFromV3Model(&model.Model, nil)

	return &Loader{
		specPath:     specPath,
		baseURL:      baseURL,
		rawDocument:  rawDocument,
		specVersion:  specVersion,
		specHash:     specHash,
		loadedDoc:    loadedDoc,
		model:        model,
		validator:    v,
		fragmentMode: false,
		fragmentsDir: "",
	}, nil
}

// NewWithFragments creates a new spec loader in fragment mode
// Loads fragments from the specified fragments directory and merges them into a single OpenAPI spec
func NewWithFragments(specDir, baseURL, schemaPath, fragmentsDir string) (*Loader, error) {
	log.Printf("[Loader] Creating new fragment-mode loader")
	log.Printf("[Loader] Base URL: %s", baseURL)
	log.Printf("[Loader] Spec directory: %s", specDir)
	log.Printf("[Loader] Fragments directory: %s", fragmentsDir)
	log.Printf("[Loader] Schema path: %s", schemaPath)

	// Initialize fragment loader
	fragmentLoader, err := NewFragmentLoader()
	if err != nil {
		log.Printf("[Loader] Error creating fragment loader: %v", err)
		return nil, fmt.Errorf("failed to create fragment loader: %w", err)
	}

	// Use default fragments directory if not specified
	if fragmentsDir == "" {
		fragmentsDir = filepath.Join(specDir, "fragments.d")
		log.Printf("[Loader] Using default fragments directory: %s", fragmentsDir)
	}

	// Load fragments from the fragments directory
	log.Printf("[Loader] Loading fragments from directory: %s", fragmentsDir)
	if err := fragmentLoader.LoadDirectory(fragmentsDir); err != nil {
		log.Printf("[Loader] Error loading fragments: %v", err)
		return nil, fmt.Errorf("failed to load fragments: %w", err)
	}

	// Validate fragments against the schema if schema is provided
	if schemaPath != "" {
		log.Printf("[Loader] Validating fragments against schema: %s", schemaPath)
		if err := fragmentLoader.ValidateFragments(schemaPath); err != nil {
			log.Printf("[Loader] Warning: fragment validation had errors: %v", err)
		}
	} else {
		log.Printf("[Loader] No schema provided, skipping fragment validation")
	}

	// Cross-field path rewrite rules cannot be expressed by JSON Schema. They
	// are enforced again at merge time so an unrun or stale lint command cannot
	// install a partly-applicable rewrite.
	fragmentLoader.ValidatePathRewriteCoherence()

	// Detect path collisions and quarantine conflicting fragments
	log.Printf("[Loader] Detecting path collisions between fragments")
	fragmentLoader.DetectPathCollisions()
	fragmentLoader.PropagateRouteMetadata()

	// Merge fragments into a single document
	log.Printf("[Loader] Merging fragments into single OpenAPI document")
	mergedJSON, err := fragmentLoader.MergeFragments(baseURL)
	if err != nil {
		log.Printf("[Loader] Error merging fragments: %v", err)
		return nil, fmt.Errorf("failed to merge fragments: %w", err)
	}

	// Compute stable hash of the merged spec
	hash := sha256.Sum256(mergedJSON)
	specHash := hex.EncodeToString(hash[:]) // Full 64-character SHA256 hash
	specVersion := specHash[:16]            // Use first 16 hex chars (64 bits) for version
	log.Printf("[Loader] Generated spec hash: %s (version: %s)", specHash, specVersion)

	// Load the merged document using libopenapi
	log.Printf("[Loader] Loading merged document with libopenapi")
	loadedDoc, err := libopenapi.NewDocument(mergedJSON)
	if err != nil {
		log.Printf("[Loader] Error loading OpenAPI document: %v", err)
		return nil, fmt.Errorf("failed to load merged OpenAPI document: %w", err)
	}

	// Build the model for validation and route extraction
	log.Printf("[Loader] Building OpenAPI v3 model")
	documentModel, err := loadedDoc.BuildV3Model()
	if err != nil {
		log.Printf("[Loader] Error building OpenAPI model: %v", err)
		return nil, fmt.Errorf("failed to build OpenAPI model: %w", err)
	}

	// Store the document model for later use
	model := documentModel

	// Create the validator using the v3 model directly
	log.Printf("[Loader] Creating validator from v3 model")
	v := validator.NewValidatorFromV3Model(&model.Model, nil)

	validFragmentCount := fragmentLoader.GetValidFragmentCount()
	quarantinedCount := fragmentLoader.GetQuarantinedCount()

	log.Printf("[Loader] Fragment-mode loader created successfully")
	log.Printf("[Loader] Valid fragments: %d, Quarantined: %d", validFragmentCount, quarantinedCount)

	return &Loader{
		specPath:       fragmentsDir,
		baseURL:        baseURL,
		rawDocument:    mergedJSON,
		specVersion:    specVersion,
		specHash:       specHash,
		loadedDoc:      loadedDoc,
		model:          model,
		validator:      v,
		FragmentLoader: fragmentLoader,
		fragmentMode:   true,
		fragmentsDir:   fragmentsDir,
	}, nil
}

// validateFragmentsDir checks if the fragments directory exists and is readable
func validateFragmentsDir(fragmentsDir string) error {
	log.Printf("[Loader] Validating fragments directory: %s", fragmentsDir)

	// Check if directory exists
	info, err := os.Stat(fragmentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[Loader] Fragments directory does not exist: %s", fragmentsDir)
			return fmt.Errorf("fragments directory does not exist: %s", fragmentsDir)
		}
		log.Printf("[Loader] Failed to access fragments directory: %s - %v", fragmentsDir, err)
		return fmt.Errorf("failed to access fragments directory: %s: %w", fragmentsDir, err)
	}

	// Check if path is actually a directory
	if !info.IsDir() {
		log.Printf("[Loader] Fragments path is not a directory: %s", fragmentsDir)
		return fmt.Errorf("fragments path is not a directory: %s", fragmentsDir)
	}

	// Check if directory is readable by attempting to open it
	file, err := os.Open(fragmentsDir)
	if err != nil {
		log.Printf("[Loader] Fragments directory is not readable: %s - %v", fragmentsDir, err)
		return fmt.Errorf("fragments directory is not readable: %s: %w", fragmentsDir, err)
	}
	_ = file.Close()

	log.Printf("[Loader] Fragments directory validation passed: %s", fragmentsDir)
	return nil
}

// NewLoader creates a new spec loader using SEAM_FRAGMENTS_DIR environment variable
// Reads SEAM_FRAGMENTS_DIR from environment, defaults to "./fragments" if not set
func NewLoader(baseURL string) (*Loader, error) {
	fragmentsDir := getFragmentsDir()
	log.Printf("[Loader] NewLoader called with fragments dir from env: %s", fragmentsDir)

	// Validate the fragments directory before proceeding
	if err := validateFragmentsDir(fragmentsDir); err != nil {
		log.Printf("[Loader] Fragments directory validation failed: %v", err)
		return nil, err
	}

	log.Printf("[Loader] Fragments directory validated successfully: %s", fragmentsDir)
	return NewWithFragments("", baseURL, "", fragmentsDir)
}

// GetVersion returns the stable spec version hash
func (l *Loader) GetVersion() string {
	return l.specVersion
}

// GetHash returns the full SHA256 hash (64 hex characters)
func (l *Loader) GetHash() string {
	return l.specHash
}

// GetAPIVersion returns the API version (constant _unversioned in Phase 1a)
func (l *Loader) GetAPIVersion() string {
	return "_unversioned"
}

// GetRawJSON returns the spec as JSON with servers populated
func (l *Loader) GetRawJSON() ([]byte, error) {
	// Serialize the document to YAML
	yamlBytes, err := l.loadedDoc.Serialize()
	if err != nil {
		return nil, fmt.Errorf("failed to serialize document to YAML: %w", err)
	}

	// Parse YAML into a generic map
	var yamlMap map[string]interface{}
	if err := yaml.Unmarshal(yamlBytes, &yamlMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal YAML: %w", err)
	}
	stripInternalRouteMetadata(yamlMap)

	// Populate servers with the caller-facing base URL
	// This modifies the YAML map directly
	yamlMap["servers"] = []map[string]interface{}{
		{
			"url":         l.baseURL,
			"description": "SEAM caller-facing endpoint",
		},
	}

	// Convert to JSON
	docJSON, err := json.MarshalIndent(yamlMap, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal to JSON: %w", err)
	}

	return docJSON, nil
}

// GetFilteredJSON returns the spec as JSON filtered by identity scopes
// Only includes routes that the identity has at least one required scope for.
// A map route is visible if the caller holds scope for at least one instance's effective requirement.
func (l *Loader) GetFilteredJSON(identityScopes []string) ([]byte, error) {
	// Get the full spec first
	fullSpec, err := l.GetRawJSON()
	if err != nil {
		return nil, err
	}

	// If no identity scopes provided, return empty spec
	if len(identityScopes) == 0 {
		return l.buildEmptySpec(), nil
	}

	// Parse the spec into a map
	var specMap map[string]interface{}
	if err := json.Unmarshal(fullSpec, &specMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal spec: %w", err)
	}

	// Get paths from the spec
	paths, ok := specMap["paths"].(map[string]interface{})
	if !ok {
		// No paths, return as-is
		return fullSpec, nil
	}

	// Normalize identity scopes for comparison
	normalizedIdentityScopes := make(map[string]bool)
	for _, scope := range identityScopes {
		normalized := strings.ToLower(strings.TrimSpace(scope))
		if normalized != "" {
			normalizedIdentityScopes[normalized] = true
		}
	}

	// Filter paths: keep only routes where identity has at least one required scope
	filteredPaths := make(map[string]interface{})
	for path, pathItem := range paths {
		pathItemMap, ok := pathItem.(map[string]interface{})
		if !ok {
			// Keep path items that aren't maps (shouldn't happen, but be safe)
			filteredPaths[path] = pathItem
			continue
		}

		// Check each method in the path item
		filteredPathItem := make(map[string]interface{})
		methodVisible := false

		for httpMethod, methodOp := range pathItemMap {
			// Skip non-method fields (like $ref, summary, etc.)
			if !isHTTPMethod(httpMethod) {
				filteredPathItem[httpMethod] = methodOp
				continue
			}

			// Get the operation
			methodOpMap, ok := methodOp.(map[string]interface{})
			if !ok {
				filteredPathItem[httpMethod] = methodOp
				continue
			}

			// Check if this operation has x-required-scope
			requiredScopes := extractRequiredScopesFromMap(methodOpMap)

			// Determine visibility:
			// - No required scopes: visible (public route)
			// - Has required scopes: visible if identity has at least one matching scope
			visible := false
			if len(requiredScopes) == 0 {
				visible = true // No scope requirement = public route
			} else {
				// Check if identity has any of the required scopes
				for _, requiredScope := range requiredScopes {
					normalizedRequired := strings.ToLower(strings.TrimSpace(requiredScope))
					if normalizedIdentityScopes[normalizedRequired] {
						visible = true
						break
					}
				}
			}

			if visible {
				filteredPathItem[httpMethod] = methodOp
				methodVisible = true
			}
		}

		// Keep the path if at least one method is visible
		if methodVisible {
			filteredPaths[path] = filteredPathItem
		}
	}

	// Build the filtered spec
	specMap["paths"] = filteredPaths
	specMap["x-seam-filtered"] = true
	specMap["x-seam-filter-reason"] = "Scope-based filtering: only routes visible to caller's identity are included"

	// Marshal back to JSON
	filteredJSON, err := json.MarshalIndent(specMap, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal filtered spec: %w", err)
	}

	return filteredJSON, nil
}

// buildEmptySpec returns a minimal valid OpenAPI spec with no paths
func (l *Loader) buildEmptySpec() []byte {
	emptySpec := map[string]interface{}{
		"openapi": "3.1.0",
		"info": map[string]interface{}{
			"title":       "SEAM API",
			"version":     l.GetAPIVersion(),
			"description": "No routes are visible with your current scope",
		},
		"servers": []map[string]interface{}{
			{
				"url":         l.baseURL,
				"description": "SEAM caller-facing endpoint",
			},
		},
		"paths": map[string]interface{}{},
		"x-seam-filtered": true,
		"x-seam-filter-reason": "Scope-based filtering: no routes visible (no scopes provided)",
	}

	specJSON, _ := json.MarshalIndent(emptySpec, "", "  ")
	return specJSON
}

// isHTTPMethod checks if a string is an HTTP method
func isHTTPMethod(s string) bool {
	switch strings.ToUpper(s) {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "TRACE":
		return true
	default:
		return false
	}
}

// extractRequiredScopesFromMap extracts x-required-scope from an operation map
func extractRequiredScopesFromMap(opMap map[string]interface{}) []string {
	extensions, ok := opMap["x-required-scope"]
	if !ok {
		return nil
	}

	// Handle different formats
	switch v := extensions.(type) {
	case []interface{}:
		scopes := make([]string, 0, len(v))
		for _, item := range v {
			if scope, ok := item.(string); ok {
				scopes = append(scopes, scope)
			}
		}
		return scopes
	case []string:
		return v
	case string:
		// Single scope as string
		return []string{v}
	default:
		return nil
	}
}

// GetRawDocument returns the original raw document bytes
func (l *Loader) GetRawDocument() []byte {
	return l.rawDocument
}

// ValidateRequest validates an HTTP request against the OpenAPI spec
// Returns nil if valid, or a ValidationError if invalid
func (l *Loader) ValidateRequest(r *http.Request) *ValidationError {
	if l == nil || l.validator == nil || l.model == nil || r == nil {
		return nil
	}

	// Resolve the request once for the contract path. The validator populates
	// SpecPath on most errors, but that value is not guaranteed for decoder and
	// schema errors. Keeping the template here is also what lets a pointer for
	// /widgets/123 name /widgets/{id} rather than the concrete request path.
	_, _, pathTemplate := validatorpaths.FindPath(r, &l.model.Model, nil)
	if pathTemplate == "" {
		pathTemplate = r.URL.Path
	}

	// Validate the request
	valid, validationErrors := l.validator.ValidateHttpRequest(r)

	// If valid, request is valid
	if valid {
		return nil
	}

	// Build structured error response
	return &ValidationError{
		Errors:       validationErrors,
		PathTemplate: pathTemplate,
	}
}

// GetRoute returns the route information for a given path, method, and version
func (l *Loader) GetRoute(path, method, version string) (*RouteInfo, error) {
	// Find the path item - Get returns (value, present)
	pathItem, present := l.model.Model.Paths.PathItems.Get(path)
	if !present || pathItem == nil {
		return nil, fmt.Errorf("path not found: %s", path)
	}

	// Get the operation
	var operation *v3.Operation
	switch method {
	case http.MethodGet:
		operation = pathItem.Get
	case http.MethodPost:
		operation = pathItem.Post
	case http.MethodPut:
		operation = pathItem.Put
	case http.MethodDelete:
		operation = pathItem.Delete
	case http.MethodPatch:
		operation = pathItem.Patch
	case http.MethodHead:
		operation = pathItem.Head
	case http.MethodOptions:
		operation = pathItem.Options
	case http.MethodTrace:
		operation = pathItem.Trace
	case "":
		// No method specified - return info for all methods
		return l.getAllMethodsForPath(pathItem, path)
	default:
		return nil, fmt.Errorf("method not found: %s", method)
	}

	if operation == nil {
		return nil, fmt.Errorf("method %s not defined for path: %s", method, path)
	}

	return &RouteInfo{
		Path:      path,
		Method:    method,
		Version:   version,
		Operation: operation,
		PathItem:  pathItem,
	}, nil
}

// getAllMethodsForPath returns route information for all methods on a path
func (l *Loader) getAllMethodsForPath(pathItem *v3.PathItem, path string) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:     path,
		Method:   "",
		Version:  "_unversioned",
		PathItem: pathItem,
	}

	methods := []string{}

	if pathItem.Get != nil {
		methods = append(methods, http.MethodGet)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodGet, Operation: pathItem.Get})
	}
	if pathItem.Post != nil {
		methods = append(methods, http.MethodPost)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodPost, Operation: pathItem.Post})
	}
	if pathItem.Put != nil {
		methods = append(methods, http.MethodPut)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodPut, Operation: pathItem.Put})
	}
	if pathItem.Delete != nil {
		methods = append(methods, http.MethodDelete)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodDelete, Operation: pathItem.Delete})
	}
	if pathItem.Patch != nil {
		methods = append(methods, http.MethodPatch)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodPatch, Operation: pathItem.Patch})
	}
	if pathItem.Head != nil {
		methods = append(methods, http.MethodHead)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodHead, Operation: pathItem.Head})
	}
	if pathItem.Options != nil {
		methods = append(methods, http.MethodOptions)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodOptions, Operation: pathItem.Options})
	}
	if pathItem.Trace != nil {
		methods = append(methods, http.MethodTrace)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodTrace, Operation: pathItem.Trace})
	}

	route.AvailableMethods = methods
	return route, nil
}

// ListPaths returns all paths defined in the spec
func (l *Loader) ListPaths() []string {
	paths := []string{}
	// Use range iteration over the iterator from FromOldest
	for path, _ := range l.model.Model.Paths.PathItems.FromOldest() {
		paths = append(paths, path)
	}
	return paths
}

// ValidationError represents a structured validation error
type ValidationError struct {
	Errors       []*errors.ValidationError
	PathTemplate string
}

// ValidationErrorItem represents a single validation error from libopenapi-validator
type ValidationErrorItem struct {
	Message           string `json:"message"`
	Reason            string `json:"reason"`
	ValidationType    string `json:"validationType"`
	ValidationSubType string `json:"validationSubType"`
	SpecLine          int    `json:"specLine"`
	SpecCol           int    `json:"specColumn"`
	HowToFix          string `json:"howToFix"`
	RequestPath       string `json:"requestPath"`
	SpecPath          string `json:"specPath"`
}

// ToJSON converts the validation error to a structured JSON response
func (ve *ValidationError) ToJSON(path, method string) map[string]interface{} {
	if path == "" && ve != nil {
		path = ve.PathTemplate
	}
	errorDetails := []map[string]interface{}{}

	for _, err := range ve.Errors {
		errorDetails = append(errorDetails, map[string]interface{}{
			"field":          validationField(err),
			"expected_shape": extractExpectedShape(err),
			"actual":         err.RequestPath,
			"reason":         err.Reason,
			"line":           err.SpecLine,
			"column":         err.SpecCol,
		})
	}

	return map[string]interface{}{
		"error":             "validation_failed",
		"message":           "Request does not conform to the OpenAPI specification",
		"validation_errors": errorDetails,
		"docs_url":          fmt.Sprintf("/docs/route?path=%s&method=%s&version=_unversioned", path, method),
	}
}

// RouteInfo contains information about a specific route
type RouteInfo struct {
	Path             string
	Method           string
	Version          string
	Operation        *v3.Operation
	PathItem         *v3.PathItem
	AvailableMethods []string
	Operations       []*OperationHolder
}

// OperationHolder holds an operation with its HTTP method
type OperationHolder struct {
	Method    string
	Operation *v3.Operation
}

// FormatValidationErrorTo400 converts libopenapi-validator errors to structured 400 format
func FormatValidationErrorTo400(validationErrors []*errors.ValidationError, path, method string) map[string]interface{} {
	errorDetails := []map[string]interface{}{}

	for _, err := range validationErrors {
		errorDetails = append(errorDetails, map[string]interface{}{
			"field":          validationField(err),
			"expected_shape": extractExpectedShape(err),
			"actual":         err.RequestPath,
			"reason":         err.Reason,
			"line":           err.SpecLine,
			"column":         err.SpecCol,
		})
	}

	return map[string]interface{}{
		"error":             "validation_failed",
		"message":           "Request does not conform to the OpenAPI specification",
		"validation_errors": errorDetails,
		"docs_url":          FormatDocsURL(path, method),
	}
}

// FormatDocsURL generates the docs URL for a given path and method
func FormatDocsURL(path, method string) string {
	return fmt.Sprintf("/docs/route?path=%s&method=%s&version=_unversioned", path, method)
}

// extractExpectedShape derives the expected shape/type from a validation error
func extractExpectedShape(err *errors.ValidationError) string {
	if err == nil {
		return "See OpenAPI specification for required format"
	}

	// Schema failures carry the useful field-level diagnostic separately from
	// HowToFix, whose generic value is often just "invalid schema". Include the
	// detail so callers can tell, for example, that a field must be an integer.
	if len(err.SchemaValidationErrors) > 0 {
		details := make([]string, 0, len(err.SchemaValidationErrors))
		for _, schemaErr := range err.SchemaValidationErrors {
			if schemaErr != nil && schemaErr.Reason != "" {
				details = append(details, schemaErr.Reason)
			}
		}
		if len(details) > 0 {
			return strings.Join(details, "; ")
		}
	}

	// Start with the HowToFix as the base expected shape
	expectedShape := err.HowToFix

	// If no HowToFix is provided, fall back to generic message
	// Don't try to enhance with validation type if we have no substantive content
	if expectedShape == "" {
		return "See OpenAPI specification for required format"
	}

	// Enhance with validation type information only if we have HowToFix content
	if err.ValidationType != "" {
		var typeInfo string
		switch strings.ToLower(err.ValidationType) {
		case "request":
			typeInfo = "Request validation"
		case "response":
			typeInfo = "Response validation"
		case "parameter":
			typeInfo = "Parameter validation"
		case "requestbody":
			typeInfo = "Request body validation"
		case "security":
			typeInfo = "Security validation"
		default:
			typeInfo = err.ValidationType + " validation"
		}

		expectedShape = typeInfo + ": " + expectedShape
	}

	// Add validation subtype if available
	if err.ValidationSubType != "" {
		expectedShape = expectedShape + " (" + err.ValidationSubType + ")"
	}

	return expectedShape
}

// validationField returns a stable, caller-useful field description. The
// validator's SpecPath identifies the route for body errors, while the nested
// schema diagnostics identify the actual JSON field. Keep both when both are
// available: it preserves the contract context and names the bad field.
func validationField(err *errors.ValidationError) string {
	if err == nil {
		return "request"
	}

	field := err.SpecPath
	if len(err.SchemaValidationErrors) > 0 {
		for _, schemaErr := range err.SchemaValidationErrors {
			if schemaErr == nil {
				continue
			}
			if schemaErr.FieldPath != "" {
				if field != "" && field != schemaErr.FieldPath {
					return field + ": " + schemaErr.FieldPath
				}
				return schemaErr.FieldPath
			}
			if schemaErr.FieldName != "" {
				if field != "" && field != schemaErr.FieldName {
					return field + ": " + schemaErr.FieldName
				}
				return schemaErr.FieldName
			}
		}
	}
	if err.ParameterName != "" {
		if field != "" {
			return field + ": " + err.ParameterName
		}
		return err.ParameterName
	}
	if field != "" {
		return field
	}
	return err.RequestPath
}

// ValidationFieldError represents a single field error in the structured 400 response
type ValidationFieldError struct {
	Field         string `json:"field"`
	ExpectedShape string `json:"expected_shape"`
	Actual        string `json:"actual,omitempty"`
	Reason        string `json:"reason"`
	Line          int    `json:"line,omitempty"`
	Column        int    `json:"column,omitempty"`
}

// Structured400Response represents the complete structured 400 error response
type Structured400Response struct {
	Error            string                 `json:"error"`
	Message          string                 `json:"message"`
	ValidationErrors []ValidationFieldError `json:"validation_errors"`
	DocsURL          string                 `json:"docs_url"`
}

// ConvertToStructured400 converts a ValidationError to Structured400Response
func ConvertToStructured400(ve *ValidationError, path, method string) *Structured400Response {
	response := &Structured400Response{
		Error:   "validation_failed",
		Message: "Request does not conform to the OpenAPI specification",
		DocsURL: FormatDocsURL(path, method),
	}

	for _, err := range ve.Errors {
		response.ValidationErrors = append(response.ValidationErrors, ValidationFieldError{
			Field:         validationField(err),
			ExpectedShape: extractExpectedShape(err),
			Actual:        err.RequestPath,
			Reason:        err.Reason,
			Line:          err.SpecLine,
			Column:        err.SpecCol,
		})
	}

	return response
}

// getFragmentsDir returns the fragments directory path from the environment variable
// or the default "./fragments" if not set
func getFragmentsDir() string {
	if val := os.Getenv("SEAM_FRAGMENTS_DIR"); val != "" {
		log.Printf("[Loader] Using SEAM_FRAGMENTS_DIR from environment: %s", val)
		return val
	}
	log.Printf("[Loader] SEAM_FRAGMENTS_DIR not set, using default: ./fragments")
	return "./fragments"
}

// GetFragmentStatus returns status information about loaded fragments
func (l *Loader) GetFragmentStatus() map[string]interface{} {
	if !l.fragmentMode || l.FragmentLoader == nil {
		return map[string]interface{}{
			"fragments_loaded": false,
			"fragment_mode":    false,
			"conditions":       []interface{}{}, // Empty conditions in static mode
		}
	}

	// Get detailed status from fragment loader
	status := l.FragmentLoader.GetFragmentStatus()
	status["fragments_loaded"] = true
	status["fragment_mode"] = true

	return status
}

// GetCacheTTLs returns a map of route paths to their cache TTL values
// This extracts x-cache-ttl from all loaded fragments
func (l *Loader) GetCacheTTLs() map[string]int {
	cacheTTLs := make(map[string]int)

	if !l.fragmentMode || l.FragmentLoader == nil {
		return cacheTTLs
	}

	// Iterate through all fragments and extract cache TTL
	for _, fragment := range l.FragmentLoader.fragments {
		if fragment.QueuedForQuarantine {
			continue
		}

		// Extract x-cache-ttl from fragment root
		ttl := extractCacheTTLFromFragment(fragment.ParsedFragment)
		if ttl > 0 {
			// Apply this TTL to all paths in this fragment
			if paths, ok := fragment.ParsedFragment["paths"].(map[string]interface{}); ok {
				for path := range paths {
					cacheTTLs[path] = ttl
					log.Printf("[Cache] Route %s has cache TTL: %d seconds", path, ttl)
				}
			}
		}
	}

	return cacheTTLs
}

// extractCacheTTLFromFragment extracts the x-cache-ttl value from a fragment
func extractCacheTTLFromFragment(fragmentData map[string]interface{}) int {
	if fragmentData == nil {
		return 0
	}

	ttlValue, exists := fragmentData["x-cache-ttl"]
	if !exists {
		return 0
	}

	// Convert to int based on actual type
	switch v := ttlValue.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}

// LoadFragments reloads fragments from the fragments directory
// This is a placeholder for future hot-reload functionality
//
// TODO(bf-3q12): Implement fragment reloading with the following features:
//   - Watch fragments directory for changes (new, modified, deleted files)
//   - Validate new/modified fragments against the schema
//   - Merge fragments and rebuild the OpenAPI document
//   - Update the validator with the new document
//   - Provide atomic update (don't serve partial specs)
//   - Emit metrics on reload success/failure
//
// Expected behavior when implemented:
//   - Returns nil on successful reload
//   - Returns error if fragments fail validation or merge
//   - The loader's document, model, and validator are atomically updated
//
// Usage:
//
//	if err := loader.LoadFragments(); err != nil {
//	    log.Printf("Fragment reload failed: %v", err)
//	}
func (l *Loader) LoadFragments() error {
	if !l.fragmentMode {
		return fmt.Errorf("LoadFragments only supported in fragment mode")
	}
	if l.FragmentLoader == nil {
		return fmt.Errorf("fragment loader is nil")
	}

	log.Printf("[Loader] Reloading fragments from directory: %s", l.fragmentsDir)

	// Create a new fragment loader to reload all fragments
	newFragmentLoader, err := NewFragmentLoader()
	if err != nil {
		return fmt.Errorf("failed to create new fragment loader: %w", err)
	}

	// Preserve the allowlist enforcer if it was configured
	if l.FragmentLoader.allowlistEnforcer != nil {
		newFragmentLoader.SetAllowlistEnforcer(l.FragmentLoader.allowlistEnforcer)
	}

	// Load fragments from the directory
	if err := newFragmentLoader.LoadDirectory(l.fragmentsDir); err != nil {
		return fmt.Errorf("failed to reload fragments: %w", err)
	}

	// Validate fragments if schema path is configured
	// The schema path is stored in the fragment loader's state
	// For now, we skip schema validation on reload to avoid failures
	// if the schema file itself hasn't changed

	// Detect path collisions and propagate metadata
	newFragmentLoader.DetectPathCollisions()
	newFragmentLoader.PropagateRouteMetadata()

	// Merge fragments into a single document
	mergedJSON, err := newFragmentLoader.MergeFragments(l.baseURL)
	if err != nil {
		return fmt.Errorf("failed to merge fragments: %w", err)
	}

	// Compute stable hash of the merged spec
	hash := sha256.Sum256(mergedJSON)
	newSpecHash := hex.EncodeToString(hash[:])
	newSpecVersion := newSpecHash[:16]

	// Check if the spec actually changed
	if newSpecHash == l.specHash {
		log.Printf("[Loader] Fragment reload: no changes detected (hash: %s)", newSpecVersion)
		return nil
	}

	log.Printf("[Loader] Fragment reload: changes detected (old: %s, new: %s)", l.specVersion, newSpecVersion)

	// Load the merged document using libopenapi
	loadedDoc, err := libopenapi.NewDocument(mergedJSON)
	if err != nil {
		return fmt.Errorf("failed to load merged OpenAPI document: %w", err)
	}

	// Build the model for validation and route extraction
	documentModel, err := loadedDoc.BuildV3Model()
	if err != nil {
		return fmt.Errorf("failed to build OpenAPI model: %w", err)
	}

	// Create the validator using the v3 model directly
	v := validator.NewValidatorFromV3Model(&documentModel.Model, nil)

	validFragmentCount := newFragmentLoader.GetValidFragmentCount()
	quarantinedCount := newFragmentLoader.GetQuarantinedCount()

	log.Printf("[Loader] Fragment reload successful: %d valid, %d quarantined", validFragmentCount, quarantinedCount)

	// Atomically update the loader's state
	l.rawDocument = mergedJSON
	l.specVersion = newSpecVersion
	l.specHash = newSpecHash
	l.loadedDoc = loadedDoc
	l.model = documentModel
	l.validator = v
	l.FragmentLoader = newFragmentLoader

	return nil
}
